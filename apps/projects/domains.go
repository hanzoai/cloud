package projects

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/internal/fqdn"
	"github.com/zap-proto/zip"
)

// operatorOrgsFromEnv builds the set of orgs that may bind a custom domain
// WITHOUT proving ownership (besides a SuperAdmin): CLOUD_PLATFORM_OPERATOR_ORGS
// (comma-separated) when set, else the deployment's own brand org (sanitized).
// The brand org is the platform operator and manages customer DNS on their
// behalf, so its bind IS the vouch. Every other org self-serves through the DNS
// challenge below.
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

// Hostname syntax, canonical form, the challenge name, the token and the
// ownership check all come from internal/fqdn — the same rules and the same
// DNS-01 proof the /v1/platform apps path uses, so "do you own this hostname"
// has one answer across the product.

// dns returns the resolver ownership proof reads through, defaulting to the
// system resolver so a production Service that never set one still verifies
// against real DNS. Tests inject a fake.
func dns(s *cloud.Service[state]) fqdn.Resolver {
	if s.State.resolver != nil {
		return s.State.resolver
	}
	return net.DefaultResolver
}

// publicHost is the site's own always-served Hanzo hostname, `<slug>.<apex>`, and
// the CNAME target a custom domain is pointed at. Distinct from siteHost
// (deploy.go), which is the BARE slug this project binds in site_hosts.
func publicHost(s *cloud.Service[state], slug string) string {
	return slug + "." + s.State.apex
}

// ours reports whether host is a domain WE run, or anything beneath it. Those names
// are ours to assign, and no DNS proof is even possible for them — a customer cannot
// publish a TXT record in a zone we run.
//
// It asks the SAME shared self-domain set the sites serve gate uses
// (sites.IsSelfHost), not just the published-site apex. Checking only the apex left
// the brand domain claimable: `api.hanzo.ai` — our production API host — passed this
// gate and took a first-come claim row. It could never serve (the serve gate excluded
// it), but the row denied the host to its real owner for good. The sites apex is
// always in that set, so this is strictly wider, never narrower; the explicit apex
// test remains as the floor for a deployment that registered no self domains.
func ours(s *cloud.Service[state], host string) bool {
	return sites.IsSelfHost(host) ||
		host == s.State.apex || strings.HasSuffix(host, "."+s.State.apex)
}

// hostClaim is one row of a site's domains panel.
//
//	live     the edge answers for this host now
//	pending  claimed, awaiting DNS proof; Records is exactly what to publish
type hostClaim struct {
	Host      string        `json:"host"`
	Status    string        `json:"status"`
	Verified  bool          `json:"verified"`
	URL       string        `json:"url"`
	Records   []fqdn.Record `json:"records,omitempty"`
	Detail    string        `json:"detail,omitempty"`
	CreatedAt int64         `json:"createdAt,omitempty"`
}

// view renders a claim, attaching the challenge records a pending one still owes.
// target is the site's own Hanzo host — the CNAME the customer points at.
func view(h HostClaim, target string) hostClaim {
	v := hostClaim{Host: h.Host, URL: "https://" + h.Host, CreatedAt: h.CreatedAt}
	if h.Status == HostVerified {
		v.Status, v.Verified = "live", true
		return v
	}
	v.Status, v.Verified = "pending", false
	v.Records = fqdn.Records(h.Host, h.Token, target)
	v.Detail = "awaiting DNS verification — publish the records below, then Verify"
	return v
}

type setDomainsReq struct {
	// Slug is the project slug from the path — which site to bind the hosts to.
	Slug string `json:"slug"`
	// Domains are the custom hostnames to attach. At least one is required.
	Domains []string `json:"domains"`
}

// domainRef addresses one host of one project.
type domainRef struct {
	// Slug is the project slug from the path.
	Slug string `json:"slug"`
	// Host is the custom hostname from the path.
	Host string `json:"host"`
}

// boundDomains is what setDomains answers with: the claims it just made plus the
// site's verified hosts.
type boundDomains struct {
	// Slug is the site the hosts were bound to.
	Slug string `json:"slug"`
	// Org is the owning tenant.
	Org string `json:"org"`
	// Bound is one claim view per host in the request, with its DNS challenge.
	Bound []hostClaim `json:"bound"`
	// Domains are the site's hosts that are verified and routing.
	Domains []string `json:"domains"`
}

// siteDomains is what listDomains answers with: the routing hosts plus every claim.
type siteDomains struct {
	// Slug is the site the hosts belong to.
	Slug string `json:"slug"`
	// Org is the owning tenant.
	Org string `json:"org"`
	// Domains are the hosts that are verified and routing.
	Domains []string `json:"domains"`
	// Claims is every host on record, verified or still pending its DNS challenge.
	Claims []hostClaim `json:"claims"`
}

// setDomains attaches custom public hostnames to this org's static site. Binding a
// host you do not own would let you shadow it at the edge, so which outcome you get
// depends on whether ownership is already established:
//
//   - SuperAdmin or the platform-operator org → bound VERIFIED immediately. The
//     operator manages customer DNS, so its bind is itself the vouch.
//   - any other org → the host is CLAIMED as pending and the response carries the
//     DNS challenge. The claim HOLDS the name so nobody else can take it, but it
//     does not route until POST .../domains/{host}/verify proves control.
//
// Before this was wired, a non-operator org got a flat 403 and could not bind at
// all; the only way onto a custom domain was to ask an operator. Now it is the
// same DNS-01 self-serve the apps path has always had, over the same primitives.
//
// First-come and reserved-label guards are the store's. Claims and binds are
// idempotent for the same (org, slug) — a redeploy or repeat call is safe, and a
// re-claim returns the SAME token rather than invalidating a record the customer
// has already published.
// Example: {"slug": "spring-launch", "domains": ["launch.example.com"]}
func (o ops) setDomains(ctx context.Context, in *setDomainsReq) (*boundDomains, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, err := o.project(ctx, c, org, in.Slug)
	if err != nil {
		return nil, err
	}
	body := *in
	if len(body.Domains) == 0 {
		return nil, zip.ErrBadRequest("no domains to bind")
	}
	vouched := c.IsAdmin() || s.State.operatorOrgs[org]
	now := time.Now().Unix()
	target := publicHost(s, p.Slug)

	out := make([]hostClaim, 0, len(body.Domains))
	for _, d := range body.Domains {
		host := fqdn.Clean(d)
		if host == "" {
			continue
		}
		if !fqdn.Valid(host) {
			return nil, zip.ErrBadRequest("invalid domain: " + d)
		}
		if !vouched && ours(s, host) {
			return nil, zip.Errorf(http.StatusForbidden,
				"%s is a host we operate; those are assigned by the platform, not claimed", host)
		}

		var bindErr error
		if vouched {
			bindErr = s.State.store.BindHost(ctx, host, org, p.Slug, now)
		} else {
			token, err := fqdn.Token()
			if err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
			}
			bindErr = s.State.store.ClaimHost(ctx, host, org, p.Slug, token, now)
		}
		switch {
		case errors.Is(bindErr, errHostTaken):
			return nil, zip.ErrConflict("domain " + host + " is already bound to another site")
		case errors.Is(bindErr, errReservedHost):
			return nil, zip.ErrBadRequest("domain " + host + " is a reserved label")
		case bindErr != nil:
			return nil, zip.Errorf(http.StatusInternalServerError, "bind %q: %v", host, bindErr)
		}

		claim, err := s.State.store.HostClaimFor(ctx, host, org, p.Slug)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "read claim %q: %v", host, err)
		}
		out = append(out, view(claim, target))
	}
	// Purge the edge cache-tag so a newly-VERIFIED host serves the current build
	// immediately. A pending claim routes nothing, so it has nothing to purge.
	purgeTag(s, ctx, org, p.Slug)
	hosts, _ := s.State.store.ListHostsForProject(ctx, org, p.Slug)
	return &boundDomains{Slug: p.Slug, Org: org, Bound: out, Domains: hosts}, nil
}

// releaseDomain gives a custom host back so it can be reused. A claim is FIRST-COME
// and global, so until this existed a bound or merely pending host was held
// forever: setDomains only ever adds
// (an empty list is a 400, not a clear), delete-project unbinds the site's own bare
// slug and nothing else, and there was no third writer. A customer who mistyped a
// domain, or claimed one they later moved elsewhere, could neither reuse it nor let
// anyone else — the row outlived every path that could reach it. Add-only ownership
// of a global namespace is not ownership, it is a leak.
//
// Scoped to (host, org, slug) by UnbindHost, so a release can only ever drop THIS
// tenant's own claim; another org's identical-host row is untouched (it cannot exist
// — the host is unique — but the scoping states the guarantee). Idempotent: releasing
// a host we do not hold is a clean 204, never a 404 that would let a caller probe
// which hosts other tenants hold.
// Example: {"slug": "spring-launch", "host": "launch.example.com"}
func (o ops) releaseDomain(ctx context.Context, in *domainRef) (*struct{}, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, err := o.project(ctx, c, org, in.Slug)
	if err != nil {
		return nil, err
	}
	host := fqdn.Clean(in.Host)
	if host == "" {
		return nil, zip.ErrBadRequest("host is required")
	}
	if err := s.State.store.UnbindHost(ctx, host, org, p.Slug); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "release %q: %v", host, err)
	}
	// The host stops routing here, so drop anything the edge still holds for it.
	purgeTag(s, ctx, org, p.Slug)
	s.Log.Info("site custom domain released", "org", org, "slug", p.Slug, "host", host)
	return nil, nil
}

// verifyDomain resolves the DNS challenge for a pending custom-host claim. On
// success the host is promoted and begins routing at the edge; on not-yet it
// returns the honest still-pending view rather than an error, because the check
// ran and simply did not find the record, so the customer retries once DNS
// propagates.
// Example: {"slug": "spring-launch", "host": "launch.example.com"}
func (o ops) verifyDomain(ctx context.Context, in *domainRef) (*hostClaim, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, err := o.project(ctx, c, org, in.Slug)
	if err != nil {
		return nil, err
	}
	host := fqdn.Clean(in.Host)
	claim, err := s.State.store.HostClaimFor(ctx, host, org, p.Slug)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("domain not claimed by this site")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read claim: %v", err)
	}
	target := publicHost(s, p.Slug)
	if claim.Status == HostVerified {
		out := view(claim, target)
		return &out, nil
	}
	if err := fqdn.Verify(ctx, dns(s), host, claim.Token); err != nil {
		v := view(claim, target)
		v.Detail = err.Error()
		return &v, nil // honest "still pending" — the check ran
	}
	now := time.Now().Unix()
	if err := s.State.store.VerifyHost(ctx, host, org, p.Slug, now); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "verify: %v", err)
	}
	s.Log.Info("site custom domain verified", "org", org, "slug", p.Slug, "host", host)
	// It routes as of now, so clear any edge cache held against the site.
	purgeTag(s, ctx, org, p.Slug)
	claim.Status, claim.Token, claim.VerifiedAt = HostVerified, "", now
	out := view(claim, target)
	return &out, nil
}

// listDomains returns every host this site holds. The live ones come back beside
// any pending claim with the DNS records it still owes.
// Example: {"slug": "spring-launch"}
func (o ops) listDomains(ctx context.Context, in *projectRef) (*siteDomains, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, err := o.project(ctx, c, org, in.Slug)
	if err != nil {
		return nil, err
	}
	claims, err := s.State.store.ListHostClaims(ctx, org, p.Slug)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list domains: %v", err)
	}
	target := publicHost(s, p.Slug)
	out := make([]hostClaim, 0, len(claims))
	hosts := make([]string, 0, len(claims))
	for _, h := range claims {
		out = append(out, view(h, target))
		if h.Status == HostVerified {
			hosts = append(hosts, h.Host)
		}
	}
	return &siteDomains{Slug: p.Slug, Org: org, Domains: hosts, Claims: out}, nil
}
