package projects

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/sites"
	"github.com/zap-proto/zip"
)

// operatorOrgsFromEnv builds the set of orgs allowed to bind custom domains
// (besides a SuperAdmin): CLOUD_PLATFORM_OPERATOR_ORGS (comma-separated) when
// set, else the deployment's own brand org (sanitized). The brand org is the
// platform operator, which manages customer domains until DNS-ownership
// verification is wired here.
func operatorOrgsFromEnv(brand string) map[string]bool {
	out := map[string]bool{}
	if raw := strings.TrimSpace(os.Getenv("CLOUD_PLATFORM_OPERATOR_ORGS")); raw != "" {
		for _, o := range strings.Split(raw, ",") {
			if o := sanitizeOrg(o); o != "" {
				out[o] = true
			}
		}
		return out
	}
	if b := sanitizeOrg(brand); b != "" {
		out[b] = true
	}
	return out
}

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
// shadow it at the edge, so a custom host requires EITHER a SuperAdmin OR the
// platform-operator org (the brand's own org — it manages customer DNS).
// DNS-ownership verification (the challenge/verify the /v1/platform apps path
// already implements) is the planned path for any org to self-bind; until it
// is wired here binding is operator/admin-gated and never fabricates ownership. A
// bound domain is inert until its owner points DNS at this edge, so the real gate
// is DNS control. First-come + reserved-label guards are enforced by the store
// (BindHost); binds are idempotent for the same (org, slug), so a redeploy or a
// repeat call is safe.
func setDomains(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.State.store.GetProject(c.Context(), org, slugParam(c))
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
	authorized := c.IsAdmin() || s.State.operatorOrgs[org]
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
		if !authorized {
			return zip.Errorf(http.StatusForbidden,
				"binding custom domain %q requires ownership verification; a SuperAdmin or the platform-operator org may bind it today", host)
		}
		if err := s.State.store.BindHost(c.Context(), host, org, p.Slug, now); err != nil {
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
	if err := s.State.cf.PurgeTags(c.Context(), sites.CacheTag(org, p.Slug)); err != nil {
		s.Log.Warn("cloudflare purge failed after domain bind (continuing)", "org", org, "slug", p.Slug, "err", err)
	}
	hosts, _ := s.State.store.ListHostsForProject(c.Context(), org, p.Slug)
	return c.JSON(http.StatusOK, map[string]any{"slug": p.Slug, "org": org, "bound": bound, "domains": hosts})
}

// listDomains returns every public host bound to this site — its hanzo.app
// subdomain plus any bound custom domains.
func listDomains(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.State.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	hosts, err := s.State.store.ListHostsForProject(c.Context(), org, p.Slug)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list domains: %v", err)
	}
	if hosts == nil {
		hosts = []string{}
	}
	return c.JSON(http.StatusOK, map[string]any{"slug": p.Slug, "org": org, "domains": hosts})
}
