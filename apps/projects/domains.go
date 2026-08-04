package projects

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/internal/fqdn"
	"github.com/zap-proto/zip"
)

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

// ours reports whether host is a name WE hold — a domain we run, or anything
// beneath it. Those names are ours to assign, and no DNS proof is even possible for
// them: a customer cannot publish a TXT record in a zone we run.
//
// It asks the SAME shared predicate the storage invariant asks (sites.Ours, which
// for a hostname is the self-domain set the serve gate uses), not just the
// published-site apex. Checking only the apex left the brand domain claimable:
// `api.hanzo.ai` — our production API host — passed this gate and took a first-come
// claim row. It could never serve (the serve gate excluded it), but the row denied
// the host to its real owner for good. The sites apex is always in that set, so
// this is strictly wider, never narrower; the explicit apex test remains as the
// floor, since the apex this app serves under is its own state.
func ours(s *cloud.Service[state], host string) bool {
	return sites.Ours(host) ||
		host == s.State.apex || strings.HasSuffix(host, "."+s.State.apex)
}

// vouches reports whether this caller may bind a host WITHOUT proving control of
// it: the bind lands VERIFIED and routes immediately, and the "a host we operate"
// refusal (ours) does not apply. It is the whole of the authority question this
// surface asks, so it is one function and the gate below reads as one word.
//
// ONE GRANT: SuperAdmin — the caller is a member of the reserved `admin` org
// (owner == "admin"). Cross-tenant by construction, so it vouches in ANY org: this
// is the operator switched into a customer's org to bind the domain it manages DNS
// for, which is how a customer domain is onboarded.
//
// Skipping the ownership proof is PLATFORM authority, so it takes the platform
// predicate and no other. SuperAdmin ⟺ `owner == "admin"` is the one predicate the
// whole estate gates on; anything else admitted here is a second, weaker spelling
// of platform authority, and a second spelling is the escalation.
//
// The org-scoped IAM admin bit is NOT a second spelling of it, however the org is
// chosen. `isAdmin` is SELF-SERVICE — an org's own admin sets it on a member of
// THEIR org — so admitting "admin of org X" here makes the gate reachable by
// anything X's admins can already do to their own membership, and X's admins are
// not the platform. Naming the org in config does not fix that: config can grant a
// capability TO a tenant, but the tenant still decides who inside it holds the
// role, so the deployment ends up delegating a proof bypass to an authority it does
// not administer. A gate whose far side can enrol its own callers is not a gate.
//
// The stakes are why the bar is the top one: a vouched bind takes the name
// FIRST-COME and GLOBAL and starts routing at once, so it both serves attacker
// content at any custom-domain customer whose DNS already points at our edge and
// denies the name to its rightful owner for good (the real owner then gets 409).
//
// FAIL-SECURE: every other caller — including an admin of the deployment's own
// brand org — self-serves, and its bind is a PENDING claim carrying the DNS
// challenge. Nothing opens on a claim that has not been proven, and an issuer that
// stops signing the admin-org membership takes the vouch away rather than granting
// one. The bit is stripped on ingress and re-minted only from validated claims, so
// it is not forgeable.
func vouches(c *zip.Ctx) bool { return principal.IsSuperAdmin(c) }

// hostOf is the ONE reading of a custom hostname off a request: canonical form,
// then the syntax this surface deals in. Bind, verify and release all ask it, so
// they cannot disagree about what a hostname IS.
//
// They did disagree, and the asymmetry was a takeover primitive. Bind required
// fqdn.Valid; release required only non-empty. But site_hosts also holds each
// project's BARE SLUG — the structural row deploy.go binds so `<slug>.<apex>`
// serves — and a bare label is not Valid (nameRE wants labels, a dot and a TLD),
// so release accepted a row bind could never re-create. The domains panel renders
// that row like any other, as a live `https://<slug>`, with a delete control beside
// it: a tenant deleting the odd-looking entry drops its OWN subdomain, which the
// domains API then cannot restore. Resolution falls back to ResolveUniqueLiveSlug,
// which refuses once two live projects share the slug, so the subdomain 404s for
// everyone — and the next tenant holding that slug to deploy takes the freed row,
// and the subdomain, for good.
//
// One predicate closes it in the direction that matters: this surface only ever
// addresses names it could also have created.
func hostOf(raw string) (string, error) {
	host := fqdn.Clean(raw)
	if !fqdn.Valid(host) {
		return "", zip.ErrBadRequest("invalid domain: " + raw)
	}
	return host, nil
}

// projectsDomain is one row of a site's domains panel.
//
//	live     the edge answers for this host now
//	pending  claimed, awaiting DNS proof; Records is exactly what to publish
type projectsDomain struct {
	Host      string        `json:"host"`
	Status    string        `json:"status"`
	Verified  bool          `json:"verified"`
	URL       string        `json:"url"`
	Records   []fqdn.Record `json:"records,omitempty"`
	Detail    string        `json:"detail,omitempty"`
	CreatedAt int64         `json:"createdAt,omitempty"`
}

// toDomain renders a claim, attaching the challenge records a pending one still owes.
// target is the site's own Hanzo host — the CNAME the customer points at.
func toDomain(h HostClaim, target string) projectsDomain {
	v := projectsDomain{Host: h.Host, URL: "https://" + h.Host, CreatedAt: h.CreatedAt}
	if h.Status == HostVerified {
		v.Status, v.Verified = "live", true
		return v
	}
	v.Status, v.Verified = "pending", false
	v.Records = fqdn.Records(h.Host, h.Token, target)
	v.Detail = "awaiting DNS verification — publish the records below, then Verify"
	return v
}

// projectsDomainsBind is the body of the bind call: the custom hostnames to
// attach to this site.
type projectsDomainsBind struct {
	// Slug is the site the hosts attach to, from the path.
	Slug string `json:"slug"`
	// Domains are the custom hostnames to attach, in order. An empty list is a
	// 400 rather than a clear — releasing a host is its own call.
	Domains []string `json:"domains"`
}

// projectsDomains is a site's domains panel: every host it holds, live and
// pending alike.
//
// FIELD ORDER IS ALPHABETICAL BY JSON TAG — this answer used to be a
// map[string]any, which encoding/json serialises in sorted key order, so the
// typed op writes the same object in the same order rather than merely the same
// JSON. Same reason on the bind answer below.
type projectsDomains struct {
	// Claims is one row per host — live, or pending with the DNS records it still
	// owes.
	Claims []projectsDomain `json:"claims"`
	// Domains are the hostnames that are VERIFIED and routing right now.
	Domains []string `json:"domains"`
	// Org and Slug identify the site the panel belongs to.
	Org  string `json:"org"`
	Slug string `json:"slug"`
}

// projectsBoundDomains is what a bind answers: the same panel keyed on THIS
// call's result rather than the full claim list.
//
// It is a SECOND type rather than an optional field on the first, because the
// two answers have always been two shapes: a bind carries `bound` and a list
// carries `claims`, each always present and neither ever carrying the other.
// Folding them into one struct with omitempty would drop an EMPTY list from the
// wire — `[]` becoming absent — which is a different answer to "how many hosts
// did that bind touch", not a tidier one.
type projectsBoundDomains struct {
	// Bound is the result of THIS call, one row per host in the request: live for
	// an already-vouched host, pending with the DNS records to publish otherwise.
	Bound []projectsDomain `json:"bound"`
	// Domains are the hostnames that are VERIFIED and routing right now, after
	// this bind.
	Domains []string `json:"domains"`
	// Org and Slug identify the site the hosts were bound to.
	Org  string `json:"org"`
	Slug string `json:"slug"`
}

// BindDomains attaches one or more CUSTOM public hostnames to this org's site.
//
// Binding a host you do not own would let you shadow it at the edge, so which
// outcome you get depends on whether ownership is already established: a SuperAdmin
// vouches (the operator manages the customer's DNS, so its bind IS the proof) and
// binds VERIFIED immediately; every other caller, INCLUDING an admin of the
// deployment's own brand org, has the host CLAIMED as pending and gets the DNS
// challenge back in `bound[].records`. A pending claim HOLDS the name so nobody
// else can take it, but it does not route until POST .../domains/{host}/verify
// proves control.
//
// A hostname we operate is refused to a non-vouched caller (those are assigned
// by the platform, never claimed), a host another site already holds is a 409,
// and a name the platform holds is a 400 for EVERY caller — a vouch skips the
// ownership proof, never the host table's own invariant. Claims and binds are
// idempotent for the same
// (org, slug), and re-claiming returns the SAME token rather than invalidating a
// record the customer has already published. The edge cache-tag is flushed
// afterwards so a newly-verified host serves the current build immediately.
//
// Scope: a validated principal is required (403 without one) and the site is
// resolved within that principal's org, so another tenant's slug is a 404.
func (o ops) bindDomains(ctx context.Context, in *projectsDomainsBind) (*projectsBoundDomains, error) {
	c, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	s := o.s
	if err := requireBody(c); err != nil {
		return nil, err
	}
	if len(in.Domains) == 0 {
		return nil, zip.ErrBadRequest("no domains to bind")
	}
	vouched := vouches(c)
	now := time.Now().Unix()
	target := publicHost(s, p.Slug)

	out := make([]projectsDomain, 0, len(in.Domains))
	for _, d := range in.Domains {
		if fqdn.Clean(d) == "" {
			continue // a blank entry in the list is skipped, never an error
		}
		host, err := hostOf(d)
		if err != nil {
			return nil, err
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
			return nil, zip.ErrBadRequest("domain " + host + " is a name the platform holds")
		case bindErr != nil:
			return nil, zip.Errorf(http.StatusInternalServerError, "bind %q: %v", host, bindErr)
		}

		claim, err := s.State.store.HostClaimFor(ctx, host, org, p.Slug)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "read claim %q: %v", host, err)
		}
		out = append(out, toDomain(claim, target))
	}
	// Purge the edge cache-tag so a newly-VERIFIED host serves the current build
	// immediately. A pending claim routes nothing, so it has nothing to purge.
	purgeTag(s, ctx, org, p.Slug)
	hosts, _ := s.State.store.ListHostsForProject(ctx, org, p.Slug)
	return &projectsBoundDomains{Slug: p.Slug, Org: org, Bound: out, Domains: hosts}, nil
}

// ReleaseDomain gives a custom hostname back, so the name is free to reuse.
//
// A claim is FIRST-COME and global, so an add-only surface was not ownership but
// a leak: a customer who mistyped a domain, or claimed one they later moved
// elsewhere, could neither reuse it nor let anyone else. This is the third
// writer that closes it. The release is scoped to (host, org, slug), so it can
// only ever drop THIS tenant's own claim, and it is IDEMPOTENT: releasing a host
// we do not hold is a clean 204, never a 404 that would let a caller probe which
// hosts other tenants hold. The edge cache-tag is flushed, since the host stops
// routing here.
//
// Scope: a validated principal is required (403 without one) and the site is
// resolved within that principal's org, so another tenant's slug is a 404.
func (o ops) releaseDomain(ctx context.Context, in *projectsDomainRef) (*void, error) {
	_, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	host, err := hostOf(in.Host)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.store.UnbindHost(ctx, host, org, p.Slug); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "release %q: %v", host, err)
	}
	// The host stops routing here, so drop anything the edge still holds for it.
	purgeTag(o.s, ctx, org, p.Slug)
	o.s.Log.Info("site custom domain released", "org", org, "slug", p.Slug, "host", host)
	return nil, nil
}

// VerifyDomain checks the DNS challenge for a pending custom hostname and, when
// it passes, promotes the host so it begins routing at the edge.
//
// It answers 200 either way, with the host's honest current state: verified once
// the TXT record is found, still pending — with the records to publish and the
// resolver's own explanation in `detail` — when it is not. A not-yet is not an
// error: the check ran, DNS simply has not propagated, and the customer retries.
// An already-verified host is returned unchanged without re-resolving. On a
// successful promotion the edge cache-tag is flushed, since the host routes as
// of that moment.
//
// Scope: a validated principal is required (403 without one). Both the site and
// the claim are resolved within that principal's org, so a host claimed by
// another tenant is "not claimed by this site".
func (o ops) verifyDomain(ctx context.Context, in *projectsDomainRef) (*projectsDomain, error) {
	_, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	s := o.s
	host, err := hostOf(in.Host)
	if err != nil {
		return nil, err
	}
	claim, err := s.State.store.HostClaimFor(ctx, host, org, p.Slug)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("domain not claimed by this site")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read claim: %v", err)
	}
	target := publicHost(s, p.Slug)
	if claim.Status == HostVerified {
		v := toDomain(claim, target)
		return &v, nil
	}
	if err := fqdn.Verify(ctx, dns(s), host, claim.Token); err != nil {
		v := toDomain(claim, target)
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
	v := toDomain(claim, target)
	return &v, nil
}

// ListDomains returns every custom hostname this site holds: the live ones, plus
// any pending claim with the DNS records it still owes.
//
// `domains` is the routing answer — the hosts that are verified right now —
// while `claims` is the full panel, one row per host, each saying whether it is
// live or pending and, if pending, exactly what to publish.
//
// Scope: a validated principal is required (403 without one) and the site is
// resolved within that principal's org, so another tenant's slug is a 404.
func (o ops) listDomains(ctx context.Context, in *projectsRef) (*projectsDomains, error) {
	_, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	claims, err := o.s.State.store.ListHostClaims(ctx, org, p.Slug)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list domains: %v", err)
	}
	target := publicHost(o.s, p.Slug)
	out := make([]projectsDomain, 0, len(claims))
	hosts := make([]string, 0, len(claims))
	for _, h := range claims {
		out = append(out, toDomain(h, target))
		if h.Status == HostVerified {
			hosts = append(hosts, h.Host)
		}
	}
	return &projectsDomains{Slug: p.Slug, Org: org, Domains: hosts, Claims: out}, nil
}
