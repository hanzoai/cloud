package projects

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/sites"
	"github.com/zap-proto/zip"
)

// hostnameRE validates a public FQDN a caller may bind as a custom domain: one or
// more DNS labels then a TLD, lowercase. It rejects schemes, ports, paths, and
// bare labels — a bound host must be a real hostname the ingress can route to the
// site edge.
var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

type setDomainsReq struct {
	Domains []string `json:"domains"`
}

// setDomains binds one or more CUSTOM public hostnames to this org's static site,
// so the site edge (clients/sites) serves them from the site's S3 prefix once the
// hostname is pointed at this edge. Binding a host you do not own would let you
// shadow it at the edge, so a custom host requires a global admin today — the same
// trust the operator uses to seed a customer's apex. DNS-ownership verification
// (the challenge/verify the /v1/platform apps path already implements) is the
// planned path for a tenant to self-bind; until it is wired here this stays
// admin-gated, and it never fabricates ownership. First-come + reserved-label
// guards are enforced by the store (BindHost); binds are idempotent for the same
// (org, slug), so a redeploy or a repeat call is safe.
func (s *svc) setDomains(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body setDomainsReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if len(body.Domains) == 0 {
		return zip.ErrBadRequest("no domains to bind")
	}
	admin := c.IsAdmin()
	now := time.Now().Unix()
	bound := make([]string, 0, len(body.Domains))
	for _, d := range body.Domains {
		host := strings.ToLower(strings.TrimSpace(d))
		if host == "" {
			continue
		}
		if !hostnameRE.MatchString(host) {
			return zip.ErrBadRequest("invalid domain: " + d)
		}
		if !admin {
			return zip.Errorf(http.StatusForbidden,
				"binding custom domain %q requires ownership verification; only a global admin may bind it today", host)
		}
		if err := s.store.BindHost(c.Context(), host, org, p.Slug, now); err != nil {
			switch {
			case errors.Is(err, errHostTaken):
				return zip.ErrConflict("domain " + host + " is already bound to another site")
			case errors.Is(err, errReservedHost):
				return zip.ErrBadRequest("domain " + host + " is a reserved label")
			default:
				return zip.Errorf(http.StatusInternalServerError, "bind %q: %v", host, err)
			}
		}
		bound = append(bound, host)
	}
	// Purge the edge so the newly-bound host serves the current content immediately.
	if err := s.cf.PurgeTags(c.Context(), sites.CacheTag(org, p.Slug)); err != nil {
		s.log.Warn("cloudflare purge failed after domain bind (continuing)", "org", org, "slug", p.Slug, "err", err)
	}
	hosts, _ := s.store.ListHostsForProject(c.Context(), org, p.Slug)
	return c.JSON(http.StatusOK, map[string]any{"slug": p.Slug, "org": org, "bound": bound, "domains": hosts})
}

// listDomains returns every public host bound to this site — its hanzo.app
// subdomain plus any bound custom domains.
func (s *svc) listDomains(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	hosts, err := s.store.ListHostsForProject(c.Context(), org, p.Slug)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list domains: %v", err)
	}
	if hosts == nil {
		hosts = []string{}
	}
	return c.JSON(http.StatusOK, map[string]any{"slug": p.Slug, "org": org, "domains": hosts})
}
