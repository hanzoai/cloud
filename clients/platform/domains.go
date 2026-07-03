// domains.go — customer-self-serve domain management for /v1/platform apps: the
// default host, org-subtree hosts, and VERIFIED BYO custom domains
// (`yourco.com`), all rendered into the app's operator Service CR ingress so the
// operator materializes the Ingress + cert-manager TLS. Two host classes, one
// render source (the app's DomainsJSON):
//
//   - default / org-subtree host (`*.<org>.<sitesHost>`) — STRUCTURAL: the org
//     owns its whole subtree by construction, so these are active the moment they
//     are added; no ownership proof is possible or needed.
//   - BYO custom host (arbitrary `yourco.com`) — needs an OWNERSHIP proof before
//     it can be rendered, or a tenant could claim a host it does not control (the
//     RED hijack boundary). The customer publishes a per-claim TXT token at
//     `_hanzo-challenge.<host>`; only someone who controls that zone's DNS can, so
//     a matching token proves control (the DNS-01 model). Until verified, the
//     claim is a pending row that is NEVER in the ingress. Global uniqueness (one
//     org per host) is the `platform_domains.host` PRIMARY KEY.
//
// Every handler is org-scoped through s.tenant and mutates only tenant-<org>.
package platform

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// hostnameRE is a syntactic DNS hostname: ≥2 dot-separated labels ending in an
// alphabetic TLD, no scheme/port/path. The boundary guard for a BYO host — a
// value that could never be a real domain is refused before any store/DNS work.
var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// challengePrefix is the label under which the TXT ownership token is published.
const challengePrefix = "_hanzo-challenge."

func challengeName(host string) string { return challengePrefix + host }

// dnsResolver is the minimal DNS surface custom-domain verification needs. The
// default impl is the stdlib *net.Resolver (no dependency); tests inject a fake
// so verification is deterministic without touching real DNS.
type dnsResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupCNAME(ctx context.Context, name string) (string, error)
}

// dns returns the configured resolver, defaulting to the system resolver so a
// production svc that never set one still verifies against real DNS.
func (s *svc) dns() dnsResolver {
	if s.resolver != nil {
		return s.resolver
	}
	return net.DefaultResolver
}

// ── host classification (pure) ─────────────────────────────────────────────────

// defaultHost is an app's canonical, always-attached URL host.
func (s *svc) defaultHost(org, slug string) string {
	return slug + "." + org + "." + s.sitesHost
}

// isOrgSubtreeHost reports whether host lies strictly under the org's own
// "<org>.<sitesHost>" subtree (a real label beneath it) — the structural
// ownership the org has by construction. Byte-identical to the original
// validateOrgDomains suffix test (the RED bar), extracted for reuse.
func (s *svc) isOrgSubtreeHost(org, host string) bool {
	suffix := "." + org + "." + s.sitesHost
	return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
}

// underSitesApex reports whether host is the platform apps apex itself or any
// host beneath it. Such hosts belong to their owning org (or Hanzo) and are NEVER
// claimable as a BYO custom domain — only the caller's OWN subtree is.
func (s *svc) underSitesApex(host string) bool {
	return host == s.sitesHost || strings.HasSuffix(host, "."+s.sitesHost)
}

func sanitizeHost(raw string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

// ── views ──────────────────────────────────────────────────────────────────────

type dnsRecordView struct {
	Type  string `json:"type"`  // TXT | CNAME
	Name  string `json:"name"`  // the record name the customer creates
	Value string `json:"value"` // the record value
}

// domainView is one row in the app's domains panel. Status is honest and derived
// from the operator CR (never fabricated):
//
//	live           host in status.endpoints AND phase Running (serving with TLS)
//	provisioning   host is an active ingress host, cert/replicas still coming up
//	pending_deploy host is attached but the app has no Service CR yet (deploy it)
//	pending        custom claim awaiting DNS verification (Records show the proof)
type domainView struct {
	Host      string          `json:"host"`
	Kind      string          `json:"kind"`   // default | subtree | custom
	Status    string          `json:"status"` // live | provisioning | pending_deploy | pending
	URL       string          `json:"url"`
	Verified  bool            `json:"verified"`
	Primary   bool            `json:"primary,omitempty"`
	Records   []dnsRecordView `json:"records,omitempty"`
	Detail    string          `json:"detail,omitempty"`
	CreatedAt int64           `json:"createdAt,omitempty"`
}

// challengeRecords are the exact DNS records a customer must publish for a custom
// host: the TXT ownership token, plus the CNAME that routes traffic at the app's
// Hanzo host once verified.
func (s *svc) challengeRecords(host, token, target string) []dnsRecordView {
	return []dnsRecordView{
		{Type: "TXT", Name: challengeName(host), Value: token},
		{Type: "CNAME", Name: host, Value: target},
	}
}

// customDomainView renders a custom domain row. A verified row's status is the
// live/provisioning signal from the operator; a pending row carries the DNS
// records to publish. def is the app's default host (the CNAME target).
func (s *svc) customDomainView(d Domain, def string, endpoints []string, phase string) domainView {
	v := domainView{Host: d.Host, Kind: "custom", URL: "https://" + d.Host, CreatedAt: d.CreatedAt}
	if d.Status == "verified" {
		v.Verified = true
		v.Status = activeHostStatus(d.Host, endpoints, phase)
		return v
	}
	v.Verified = false
	v.Status = "pending"
	v.Records = s.challengeRecords(d.Host, d.Token, def)
	v.Detail = "awaiting DNS verification — publish the records below, then Verify"
	return v
}

// activeHostStatus maps an ACTIVE ingress host (in DomainsJSON) to an honest live
// signal from the operator CR: status.endpoints (URLs it built an Ingress + cert
// for) + status.phase.
func activeHostStatus(host string, endpoints []string, phase string) string {
	inEP := hostInEndpoints(host, endpoints)
	switch {
	case inEP && phase == "Running":
		return "live"
	case inEP:
		return "provisioning"
	case len(endpoints) == 0 && phase == "":
		return "pending_deploy"
	default:
		return "provisioning"
	}
}

func hostInEndpoints(host string, endpoints []string) bool {
	for _, e := range endpoints {
		if i := strings.Index(e, "://"); i >= 0 {
			e = e[i+3:]
		}
		if strings.EqualFold(strings.TrimSuffix(e, "/"), host) {
			return true
		}
	}
	return false
}

// ── handlers ─────────────────────────────────────────────────────────────────

// listDomains returns the app's domains: the default host, any org-subtree hosts,
// and every BYO custom claim (pending with its challenge records, or verified with
// live status). One merged, honest view.
func (s *svc) listDomains(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	endpoints, phase := s.k8s.observeDomains(c.Context(), org, a.Slug)
	def := s.defaultHost(org, a.Slug)
	customs, err := s.store.ListDomainsByApp(c.Context(), org, a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list domains: %v", err)
	}
	customSet := make(map[string]bool, len(customs))
	for _, d := range customs {
		customSet[d.Host] = true
	}

	out := []domainView{{
		Host: def, Kind: "default", Primary: true, Verified: true,
		URL: "https://" + def, Status: activeHostStatus(def, endpoints, phase),
	}}
	// org-subtree active hosts (custom hosts are rendered from their rows instead).
	for _, h := range domainList(a.DomainsJSON) {
		if h == def || customSet[h] {
			continue
		}
		out = append(out, domainView{
			Host: h, Kind: "subtree", Verified: true,
			URL: "https://" + h, Status: activeHostStatus(h, endpoints, phase),
		})
	}
	for _, d := range customs {
		out = append(out, s.customDomainView(d, def, endpoints, phase))
	}
	return c.JSON(http.StatusOK, out)
}

type addDomainReq struct {
	Host string `json:"host"`
}

// addDomain attaches a host to an app. An org-subtree host is active immediately;
// a BYO custom host is claimed as PENDING and returns the DNS challenge records to
// publish (it is not rendered into the ingress until verified).
func (s *svc) addDomain(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	proj, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	var body addDomainReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	host := sanitizeHost(body.Host)
	if host == "" {
		return zip.ErrBadRequest("host is required")
	}
	if len(host) > 253 || !hostnameRE.MatchString(host) {
		return zip.ErrBadRequest("host must be a valid DNS hostname (e.g. app.yourco.com)")
	}
	def := s.defaultHost(org, a.Slug)
	if host == def {
		return zip.ErrConflict("the default domain is always attached and cannot be re-added")
	}
	now := time.Now().Unix()

	// Org-subtree host → active immediately (structurally owned, no proof needed).
	if s.isOrgSubtreeHost(org, host) {
		hosts := addHost(domainList(a.DomainsJSON), host)
		if err := s.persistHostsAndIngress(c.Context(), &a, hosts, now); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "attach domain: %v", err)
		}
		endpoints, phase := s.k8s.observeDomains(c.Context(), org, a.Slug)
		return c.JSON(http.StatusCreated, domainView{
			Host: host, Kind: "subtree", Verified: true, URL: "https://" + host,
			Status: activeHostStatus(host, endpoints, phase),
		})
	}

	// A host under the platform apex that is NOT the caller's own subtree belongs to
	// its owning org (or Hanzo) — never BYO-claimable (RED: no cross-tenant grab of
	// another org's *.hanzo.app host via the custom path).
	if s.underSitesApex(host) {
		return zip.ErrForbidden("hosts under " + s.sitesHost + " cannot be claimed as a custom domain")
	}

	// BYO custom host → global-uniqueness check, then a pending claim + challenge.
	existing, found, err := s.store.LookupDomain(c.Context(), host)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "domain lookup: %v", err)
	}
	if found {
		if existing.Org != org {
			return zip.ErrConflict("domain is already claimed by another organization")
		}
		if existing.AppID != a.ID {
			return zip.ErrConflict("domain is already claimed by another application in your organization")
		}
		// Idempotent: this app re-adds its own claim → return the current state.
		endpoints, phase := s.k8s.observeDomains(c.Context(), org, a.Slug)
		return c.JSON(http.StatusOK, s.customDomainView(existing, def, endpoints, phase))
	}
	token, err := genDomainToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	d := Domain{Host: host, Org: org, ProjectID: proj.ID, AppID: a.ID, AppSlug: a.Slug, Status: "pending", Token: token, CreatedAt: now}
	if err := s.store.CreateDomain(c.Context(), d); err != nil {
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("domain is already claimed") // lost a create race
		}
		return zip.Errorf(http.StatusInternalServerError, "claim domain: %v", err)
	}
	return c.JSON(http.StatusCreated, s.customDomainView(d, def, nil, ""))
}

// verifyDomain resolves the DNS challenge for a pending custom domain. On success
// it marks the claim verified, renders the host into the app ingress, and patches
// the operator CR (which then issues the ACME cert). On not-yet it returns the
// honest still-pending view (not an error) so the customer can retry after DNS
// propagates.
func (s *svc) verifyDomain(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	host := sanitizeHost(c.Param("host"))
	d, err := s.store.GetDomain(c.Context(), org, a.ID, host)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("domain not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get domain: %v", err)
	}
	def := s.defaultHost(org, a.Slug)

	if d.Status == "verified" {
		endpoints, phase := s.k8s.observeDomains(c.Context(), org, a.Slug)
		return c.JSON(http.StatusOK, s.customDomainView(d, def, endpoints, phase))
	}

	owned, detail := s.verifyDNS(c.Context(), host, d.Token)
	if !owned {
		v := s.customDomainView(d, def, nil, "")
		v.Detail = detail
		return c.JSON(http.StatusOK, v) // honest "still pending" — the check ran
	}

	now := time.Now().Unix()
	if _, err := s.store.MarkDomainVerified(c.Context(), org, a.ID, host, now); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "mark verified: %v", err)
	}
	d.Status, d.VerifiedAt = "verified", now
	hosts := addHost(domainList(a.DomainsJSON), host)
	if err := s.persistHostsAndIngress(c.Context(), &a, hosts, now); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "attach verified domain: %v", err)
	}
	s.log.Info("custom domain verified", "org", org, "app", a.Slug, "host", host)
	endpoints, phase := s.k8s.observeDomains(c.Context(), org, a.Slug)
	return c.JSON(http.StatusOK, s.customDomainView(d, def, endpoints, phase))
}

// removeDomain detaches a host: it drops it from the ingress host set (re-applying
// the CR) and releases any custom claim (freeing the host for re-claim, anywhere).
// The default host is permanent and cannot be removed.
func (s *svc) removeDomain(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	host := sanitizeHost(c.Param("host"))
	if host == s.defaultHost(org, a.Slug) {
		return zip.ErrBadRequest("the default domain cannot be removed")
	}
	now := time.Now().Unix()

	newHosts, removed := removeHost(domainList(a.DomainsJSON), host)
	if removed {
		if err := s.persistHostsAndIngress(c.Context(), &a, newHosts, now); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "detach domain: %v", err)
		}
	}
	deleted, err := s.store.DeleteDomain(c.Context(), org, a.ID, host)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "release domain: %v", err)
	}
	if !removed && !deleted {
		return zip.ErrNotFound("domain not attached to this application")
	}
	return c.NoContent(http.StatusNoContent)
}

// verifyDNS checks the ownership challenge for host: the TXT record at
// `_hanzo-challenge.<host>` must contain the claim token. This is the security
// boundary — only someone who controls host's DNS zone can publish that record,
// so a matching token proves control (the DNS-01 model). Apex-safe (no CNAME
// requirement). Returns (owned, detail) where detail names what is missing.
func (s *svc) verifyDNS(ctx context.Context, host, token string) (owned bool, detail string) {
	txts, err := s.dns().LookupTXT(ctx, challengeName(host))
	if err == nil {
		for _, t := range txts {
			if strings.TrimSpace(t) == token {
				return true, ""
			}
		}
	}
	return false, fmt.Sprintf(
		"TXT record %s = %q not found yet — publish it and retry (DNS can take a few minutes to propagate)",
		challengeName(host), token)
}

// persistHostsAndIngress writes the app's new active host set and re-applies the
// operator ingress in ONE place, so every domain mutation (subtree add, verify,
// remove) keeps DomainsJSON and the CR ingress consistent. The ingress patch is
// best-effort: a not-yet-deployed app has no CR, and the hosts render on its next
// deploy.
func (s *svc) persistHostsAndIngress(ctx context.Context, a *Application, hosts []string, now int64) error {
	a.DomainsJSON = marshalHosts(hosts)
	a.UpdatedAt = now
	if err := s.store.UpdateApplication(ctx, *a); err != nil {
		return err
	}
	if err := s.k8s.applyIngress(ctx, a.Org, a.Slug, hosts); err != nil {
		s.log.Warn("apply ingress after domain change (continuing)", "org", a.Org, "app", a.Slug, "err", err)
	}
	return nil
}

// ── small helpers ──────────────────────────────────────────────────────────────

// seedDefaultDomain guarantees an app carries its canonical default host
// (<slug>.<org>.<sitesHost>) as its first ingress host, so every app has a working
// HTTPS URL the moment it deploys (the operator issues the cert). Deduped.
func (s *svc) seedDefaultDomain(org, slug string, domains []string) []string {
	def := s.defaultHost(org, slug)
	out := []string{def}
	for _, d := range domains {
		if d != def {
			out = append(out, d)
		}
	}
	return out
}

func addHost(hosts []string, h string) []string {
	for _, x := range hosts {
		if x == h {
			return hosts
		}
	}
	return append(hosts, h)
}

func removeHost(hosts []string, h string) ([]string, bool) {
	var out []string
	removed := false
	for _, x := range hosts {
		if x == h {
			removed = true
			continue
		}
		out = append(out, x)
	}
	return out, removed
}

func marshalHosts(hosts []string) string {
	if len(hosts) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(hosts)
	return string(b)
}

// genDomainToken returns a 22-char URL-safe verification token (96 bits).
func genDomainToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
