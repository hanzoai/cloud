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
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/fqdn"
	"github.com/zap-proto/zip"
)

// Hostname syntax, canonical form, the challenge name, the token, and the
// ownership check itself all live in internal/fqdn — the ONE implementation,
// shared with the PaaS sites path (clients/projects), which binds customer
// hostnames against the same rules.

// dns returns the configured resolver, defaulting to the system resolver so a
// production Service that never set one still verifies against real DNS.
func dns(s *cloud.Service[state]) fqdn.Resolver {
	if s.State.resolver != nil {
		return s.State.resolver
	}
	return net.DefaultResolver
}

// ── host classification (pure) ─────────────────────────────────────────────────

// defaultHost is an app's canonical, always-attached URL host.
func defaultHost(s *cloud.Service[state], org, slug string) string {
	return slug + "." + org + "." + s.State.sitesHost
}

// isOrgSubtreeHost reports whether host lies strictly under the org's own
// "<org>.<sitesHost>" subtree (a real label beneath it) — the structural
// ownership the org has by construction. Byte-identical to the original
// validateOrgDomains suffix test (the RED bar), extracted for reuse.
func isOrgSubtreeHost(s *cloud.Service[state], org, host string) bool {
	suffix := "." + org + "." + s.State.sitesHost
	return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
}

// underSitesApex reports whether host is the platform apps apex itself or any
// host beneath it. Such hosts belong to their owning org (or Hanzo) and are NEVER
// claimable as a BYO custom domain — only the caller's OWN subtree is.
func underSitesApex(s *cloud.Service[state], host string) bool {
	return host == s.State.sitesHost || strings.HasSuffix(host, "."+s.State.sitesHost)
}

// ── views ──────────────────────────────────────────────────────────────────────

// domainView is one row in the app's domains panel. Status is honest and derived
// from the operator CR (never fabricated):
//
//	live           host in status.endpoints AND phase Running (serving with TLS)
//	provisioning   host is an active ingress host, cert/replicas still coming up
//	pending_deploy host is attached but the app has no Service CR yet (deploy it)
//	pending        custom claim awaiting DNS verification (Records show the proof)
type domainView struct {
	// Host is the hostname itself.
	Host string `json:"host"`
	// Kind is `default`, `subtree` or `custom` — how the org came to own it.
	Kind string `json:"kind"`
	// Status is `live`, `provisioning`, `pending_deploy` or `pending`, derived
	// from the operator CR and never fabricated.
	Status string `json:"status"`
	// URL is the host as an HTTPS address.
	URL string `json:"url"`
	// Verified is whether ownership is settled — always true for a host the org
	// structurally owns.
	Verified bool `json:"verified"`
	// Primary marks the app's permanent default host.
	Primary bool `json:"primary,omitempty"`
	// Records are the DNS records to publish while a custom claim is pending.
	Records []fqdn.Record `json:"records,omitempty"`
	// Detail says why a claim is still pending, in the resolver's own words.
	Detail string `json:"detail,omitempty"`
	// CreatedAt is the unix second the custom claim was made.
	CreatedAt int64 `json:"createdAt,omitempty"`
	// created is whether THIS answer created the claim, and it is server-side
	// only: it never reaches the wire (no json tag), it exists solely so
	// StatusCode can tell a fresh 201 from an idempotent 200. Carrying it as a
	// field rather than a status written from inside the handler is what keeps the
	// code a declared part of the contract every projection can read.
	created bool
}

// StatusCode is the code an attach answers with: 201 for a claim this call
// created, 200 for one that already existed. Declared on the op with
// zip.WithStatus(200, 201), so the document publishes both. Every other route
// serving a domainView declares no status and answers the default 200, which is
// what they have always answered.
func (v *domainView) StatusCode() int {
	if v.created {
		return http.StatusCreated
	}
	return http.StatusOK
}

// customDomainView renders a custom domain row. A verified row's status is the
// live/provisioning signal from the operator; a pending row carries the DNS
// records to publish. def is the app's default host (the CNAME target).
func customDomainView(s *cloud.Service[state], d Domain, def string, endpoints []string, phase string) domainView {
	v := domainView{Host: d.Host, Kind: "custom", URL: "https://" + d.Host, CreatedAt: d.CreatedAt}
	if d.Status == "verified" {
		v.Verified = true
		v.Status = activeHostStatus(d.Host, endpoints, phase)
		return v
	}
	v.Verified = false
	v.Status = "pending"
	v.Records = fqdn.Records(d.Host, d.Token, def)
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

// domainList is one app's hosts as a list answers them — a bare JSON array, named
// so the document can describe it.
type domainList []domainView

// listDomains returns every hostname this app answers on.
//
// It lists the app's hosts: the permanent default host it was born with, any
// org-subtree hosts attached to it, and every custom host claimed for it with its
// verification state and, while pending, the DNS challenge records to publish. Live
// endpoint status for each host is observed from the cluster. Requires a validated
// principal; 403 without one.
func (o ops) listDomains(ctx context.Context, in *appRef) (*domainList, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	endpoints, phase := s.State.k8s.observeDomains(ctx, org, a.Slug)
	def := defaultHost(s, org, a.Slug)
	customs, err := s.State.store.ListDomainsByApp(ctx, org, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list domains: %v", err)
	}
	customSet := make(map[string]bool, len(customs))
	for _, d := range customs {
		customSet[d.Host] = true
	}

	out := domainList{{
		Host: def, Kind: "default", Primary: true, Verified: true,
		URL: "https://" + def, Status: activeHostStatus(def, endpoints, phase),
	}}
	// org-subtree active hosts (custom hosts are rendered from their rows instead).
	for _, h := range activeHosts(a.DomainsJSON) {
		if h == def || customSet[h] {
			continue
		}
		out = append(out, domainView{
			Host: h, Kind: "subtree", Verified: true,
			URL: "https://" + h, Status: activeHostStatus(h, endpoints, phase),
		})
	}
	for _, d := range customs {
		out = append(out, customDomainView(s, d, def, endpoints, phase))
	}
	return &out, nil
}

// addDomainReq is the host to attach. Host carries `url:"-"` because the URL
// addresses the app and the hostname has always ridden in the body.
type addDomainReq struct {
	// Project is the project the application lives under, from the path.
	Project string `json:"project"`
	// App is the application's slug, from the path.
	App string `json:"app"`
	// Host is the hostname to attach. Required, and must be a valid DNS hostname.
	Host string `json:"host" url:"-"`
}

// addDomain attaches a hostname — instantly if you already own it, otherwise with a
// DNS challenge.
//
// It attaches `host` to the app, and which of two things happens depends on who
// owns the name. A host inside the caller org's own subtree is structurally owned,
// so it goes ACTIVE immediately and answers 201. A bring-your-own host is claimed
// as PENDING and answers the DNS challenge records to publish; it is NOT rendered
// into the app's ingress until /verify passes.
//
// Claims are globally unique. A host already claimed by another organization is
// 409, and so is one claimed by a different app in your own; re-adding this app's
// OWN claim is idempotent and answers its current state at 200. The default host is
// always attached and re-adding it is 409. A host under the platform's shared apex
// that is not the caller's own subtree is 403 — it belongs to whoever owns that
// subtree and can never be grabbed through the custom path.
//
// `host` must be a valid DNS hostname; anything else is 400. Requires a validated
// principal; 403 without one.
func (o ops) addDomain(ctx context.Context, body *addDomainReq) (*domainView, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	proj, a, err := loadApp(s, ctx, org, body.Project, body.App)
	if err != nil {
		return nil, err
	}
	host := fqdn.Clean(body.Host)
	if host == "" {
		return nil, zip.ErrBadRequest("host is required")
	}
	if !fqdn.Valid(host) {
		return nil, zip.ErrBadRequest("host must be a valid DNS hostname (e.g. app.yourco.com)")
	}
	def := defaultHost(s, org, a.Slug)
	if host == def {
		return nil, zip.ErrConflict("the default domain is always attached and cannot be re-added")
	}
	now := time.Now().Unix()

	// Org-subtree host → active immediately (structurally owned, no proof needed).
	if isOrgSubtreeHost(s, org, host) {
		hosts := addHost(activeHosts(a.DomainsJSON), host)
		if err := persistHostsAndIngress(s, ctx, &a, hosts, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "attach domain: %v", err)
		}
		endpoints, phase := s.State.k8s.observeDomains(ctx, org, a.Slug)
		return &domainView{
			Host: host, Kind: "subtree", Verified: true, URL: "https://" + host,
			Status: activeHostStatus(host, endpoints, phase), created: true,
		}, nil
	}

	// A host under the platform apex that is NOT the caller's own subtree belongs to
	// its owning org (or Hanzo) — never BYO-claimable (RED: no cross-tenant grab of
	// another org's *.hanzo.app host via the custom path).
	if underSitesApex(s, host) {
		return nil, zip.ErrForbidden("hosts under " + s.State.sitesHost + " cannot be claimed as a custom domain")
	}

	// BYO custom host → global-uniqueness check, then a pending claim + challenge.
	existing, found, err := s.State.store.LookupDomain(ctx, host)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "domain lookup: %v", err)
	}
	if found {
		if existing.Org != org {
			return nil, zip.ErrConflict("domain is already claimed by another organization")
		}
		if existing.AppID != a.ID {
			return nil, zip.ErrConflict("domain is already claimed by another application in your organization")
		}
		// Idempotent: this app re-adds its own claim → return the current state.
		endpoints, phase := s.State.k8s.observeDomains(ctx, org, a.Slug)
		v := customDomainView(s, existing, def, endpoints, phase)
		return &v, nil
	}
	token, err := fqdn.Token()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	d := Domain{Host: host, Org: org, ProjectID: proj, AppID: a.ID, AppSlug: a.Slug, Status: "pending", Token: token, CreatedAt: now}
	if err := s.State.store.CreateDomain(ctx, d); err != nil {
		if errors.Is(err, errConflict) {
			return nil, zip.ErrConflict("domain is already claimed") // lost a create race
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "claim domain: %v", err)
	}
	v := customDomainView(s, d, def, nil, "")
	v.created = true
	return &v, nil
}

// verifyDomain checks a custom domain's DNS and turns it on if it passes.
//
// It runs the DNS challenge check for a pending custom host and, when it passes,
// marks the host verified and renders it into the app's ingress so it starts
// serving.
//
// A check that RAN and did not pass is not an error: it answers 200 with the host
// still pending and the reason in `detail`, so a console can show the operator what
// DNS is actually returning. An already-verified host answers as-is without
// re-checking. A host not claimed by this app is 404. Requires a validated
// principal; 403 without one.
func (o ops) verifyDomain(ctx context.Context, in *domainRef) (*domainView, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	host := fqdn.Clean(in.Host)
	d, err := s.State.store.GetDomain(ctx, org, a.ID, host)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("domain not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get domain: %v", err)
	}
	def := defaultHost(s, org, a.Slug)

	if d.Status == "verified" {
		endpoints, phase := s.State.k8s.observeDomains(ctx, org, a.Slug)
		v := customDomainView(s, d, def, endpoints, phase)
		return &v, nil
	}

	if err := fqdn.Verify(ctx, dns(s), host, d.Token); err != nil {
		v := customDomainView(s, d, def, nil, "")
		v.Detail = err.Error()
		return &v, nil // honest "still pending" — the check ran
	}

	now := time.Now().Unix()
	if _, err := s.State.store.MarkDomainVerified(ctx, org, a.ID, host, now); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "mark verified: %v", err)
	}
	d.Status, d.VerifiedAt = "verified", now
	hosts := addHost(activeHosts(a.DomainsJSON), host)
	if err := persistHostsAndIngress(s, ctx, &a, hosts, now); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "attach verified domain: %v", err)
	}
	s.Log.Info("custom domain verified", "org", org, "app", a.Slug, "host", host)
	endpoints, phase := s.State.k8s.observeDomains(ctx, org, a.Slug)
	v := customDomainView(s, d, def, endpoints, phase)
	return &v, nil
}

// removeDomain detaches a hostname and releases the claim.
//
// It drops the host from the app's ingress and releases any custom claim on it, so
// the name becomes claimable again — by this org or any other. Answers 204.
//
// The default host is permanent and cannot be removed: that is 400, not 404. A host
// that is neither attached nor claimed here is 404. Requires a validated principal;
// 403 without one.
func (o ops) removeDomain(ctx context.Context, in *domainRef) (*noContent, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	host := fqdn.Clean(in.Host)
	if host == defaultHost(s, org, a.Slug) {
		return nil, zip.ErrBadRequest("the default domain cannot be removed")
	}
	now := time.Now().Unix()

	newHosts, removed := removeHost(activeHosts(a.DomainsJSON), host)
	if removed {
		if err := persistHostsAndIngress(s, ctx, &a, newHosts, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "detach domain: %v", err)
		}
	}
	deleted, err := s.State.store.DeleteDomain(ctx, org, a.ID, host)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "release domain: %v", err)
	}
	if !removed && !deleted {
		return nil, zip.ErrNotFound("domain not attached to this application")
	}
	return nil, nil
}

// persistHostsAndIngress writes the app's new active host set and re-applies the
// operator ingress in ONE place, so every domain mutation (subtree add, verify,
// remove) keeps DomainsJSON and the CR ingress consistent. The ingress patch is
// best-effort: a not-yet-deployed app has no CR, and the hosts render on its next
// deploy.
func persistHostsAndIngress(s *cloud.Service[state], ctx context.Context, a *Application, hosts []string, now int64) error {
	a.DomainsJSON = marshalHosts(hosts)
	a.UpdatedAt = now
	if err := s.State.store.UpdateApplication(ctx, *a); err != nil {
		return err
	}
	if err := s.State.k8s.applyIngress(ctx, a.Org, a.Slug, hosts); err != nil {
		s.Log.Warn("apply ingress after domain change (continuing)", "org", a.Org, "app", a.Slug, "err", err)
	}
	return nil
}

// ── small helpers ──────────────────────────────────────────────────────────────

// seedDefaultDomain guarantees an app carries its canonical default host
// (<slug>.<org>.<sitesHost>) as its first ingress host, so every app has a working
// HTTPS URL the moment it deploys (the operator issues the cert). Deduped.
func seedDefaultDomain(s *cloud.Service[state], org, slug string, domains []string) []string {
	def := defaultHost(s, org, slug)
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
