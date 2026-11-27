package sites

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"iter"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/zap-proto/zip"

	luxlog "github.com/luxfi/log"
)

// slugRE is the subdomain-label grammar. It is byte-identical to projects's
// slug grammar (the slug IS the subdomain), so a label that could never be a
// project slug is rejected before any store lookup — the injection/traversal
// guard at the host boundary. A label with a dot, slash, uppercase, or leading/
// trailing dash never matches, so `../otherorg`, `a.b`, and `WWW` all 404.
var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Site is the authoritative binding for a published subdomain: the tenant (Org),
// the S3 location (Bucket + Prefix), and the publish Status. Every field is
// server-owned — produced by the store from the validated slug, never from the
// request. Prefix is the hard tenant boundary for object reads.
type Site struct {
	Org string
	// Namespace is the first segment of the site's storage prefix (projects
	// namespace.go), and of its edge cache-tag.
	Namespace string
	Slug      string
	Bucket    string
	Prefix    string
	Status    string
	// CrossOriginIsolation, when true, makes the site server emit the cross-origin
	// isolation headers (COOP/COEP on documents, CORP on assets) so a multithreaded
	// Unity/Unreal/Godot WebGL build can use SharedArrayBuffer. It is OPT-IN per
	// site — isolation blocks embedding third-party cross-origin content, so it is
	// NEVER global. Server-owned like every other field: the resolver sets it (from
	// the project's declared WebGL game-engine framework), never the request.
	CrossOriginIsolation bool
	// CacheControl is the site's own Cache-Control policy for its DOCUMENTS, or
	// empty for the default. It is the htmlOverride argument of CacheControlFor,
	// carried here so the serving path composes the override with the class policy
	// itself instead of reading a header back off the object — which is what let a
	// client that wrote the bytes decide how the edge caches them. Server-owned:
	// the resolver reads it from the project row, never from the request.
	CacheControl string
}

// Resolver maps a validated subdomain slug to its authoritative Site. It is the
// projects store (the ONE source of project truth). found=false ⇒ no such
// published subdomain (honest 404); err ⇒ a real store failure (honest 500).
type Resolver interface {
	Resolve(ctx context.Context, slug string) (Site, bool, error)
	// ResolveOrg resolves a slug PINNED to org — the first-party-host path
	// (cd.hanzo.ai → org "hanzo"). It must NEVER fall back to unique-across-orgs, so
	// an internal host can only ever be served by OUR own project, never shadowed by
	// a customer who happens to name a project the same.
	ResolveOrg(ctx context.Context, org, slug string) (Site, bool, error)
}

var (
	resolverMu sync.RWMutex
	resolver   Resolver
	fallback   Resolver
)

// SetResolver installs the slug→Site resolver. projects.Use calls this once
// with its store — the no-hop answer when the edge and projects share a process.
func SetResolver(r Resolver) {
	resolverMu.Lock()
	resolver = r
	resolverMu.Unlock()
}

// SetFallbackResolver installs the resolver used when projects is NOT in this
// process. cloud's composition root sets it to a plane-backed client.
//
// It exists because in production they are never in the same process: the pod
// boots ~25 single-app processes, so the registry above was written inside
// `projects` and read inside whichever process fronts :8000, where it is nil. A
// nil registry is a clean miss, not a fault — so every published site resolved
// as not-found with no error anywhere, fell through to the API pipeline, and
// <slug>.hanzo.app served the console SPA with the whole cloud API answering on
// the customer's own hostname. Measured at the pod, ingress bypassed.
//
// The old comment here read "until it is set, every site request is an honest
// 404 (the projects subsystem is not mounted)". That premise was the bug:
// projects IS mounted, just somewhere else, and 404 is not honest when the site
// exists.
// PlaneSite is the wire shape of a resolved site, exported so a host that has
// no business importing the root package can still speak this call.
//
// Found is explicit: the edge must tell "no such site" (an honest 404) from
// "could not ask" (503). Collapsing them serves 404s for live customer sites
// during any transient failure of the owning app, which is indistinguishable
// from the site being deleted.
type PlaneSite struct {
	Found                bool   `json:"found"`
	Org                  string `json:"org"`
	Namespace            string `json:"namespace"`
	Slug                 string `json:"slug"`
	Bucket               string `json:"bucket"`
	Prefix               string `json:"prefix"`
	Status               string `json:"status"`
	CrossOriginIsolation bool   `json:"crossOriginIsolation"`
}

// PlaneSiteIn names the site to resolve. Org is set only on the first-party
// path, which pins the lookup to one org.
type PlaneSiteIn struct {
	Slug string `json:"slug"`
	Org  string `json:"org,omitempty"`
}

// SiteOf projects a wire answer onto a Site. Exported for the same reason the
// types are: the caller lives outside this package and must not restate the
// mapping.
func SiteOf(out *PlaneSite) (Site, bool) {
	if out == nil || !out.Found {
		return Site{}, false
	}
	return Site{
		Org:                  out.Org,
		Namespace:            cmp.Or(out.Namespace, out.Org),
		Slug:                 out.Slug,
		Bucket:               out.Bucket,
		Prefix:               out.Prefix,
		Status:               out.Status,
		CrossOriginIsolation: out.CrossOriginIsolation,
	}, true
}

func SetFallbackResolver(r Resolver) {
	resolverMu.Lock()
	fallback = r
	resolverMu.Unlock()
}

// currentResolver prefers the in-process store and falls back to the plane. A
// process that owns the store never pays for a hop; one that does not can still
// answer, instead of silently serving the API for every customer's site.
// HasFallbackResolver reports whether a cross-process resolver is installed. It
// exists so the host can PROVE it wired the edge: the defect this guards was a
// middleware that ran nowhere, which no behavioural test in this package could
// have caught, because the package itself was always correct.
func HasFallbackResolver() bool {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	return fallback != nil
}

func currentResolver() Resolver {
	resolverMu.RLock()
	r, fb := resolver, fallback
	resolverMu.RUnlock()
	if r != nil {
		return r
	}
	return fb
}

// CurrentResolver is the resolver in force for this process — the in-process
// store when projects is co-resident, else the cross-process fallback. nil means
// nothing can answer "which release does this site serve", which is a wiring
// fault, not a miss.
//
// It is exported because the site edge is no longer the only reader: the CONSOLE
// is a published site too (webui/release), and it must read the active-release
// pointer through this ONE registry. A second lookup path would be a second
// answer to "where do a site's bytes live", and the two would drift the first
// time one of them learned something — which is the defect SetFallbackResolver
// was added to close, one layer down.
func CurrentResolver() Resolver { return currentResolver() }

// Config configures the site host-router. Apex is the zone whose subdomains are
// site hosts (hanzo.app). Reserved is the set of subdomain labels that are NOT
// sites (they belong to real app hosts) and must fall through to the normal
// pipeline — the reserved-host exclusion that stops a site from shadowing a real
// hanzo.app app or api.
type Config struct {
	Apex     string
	Reserved []string
	// SelfDomains are the registrable domains of OUR OWN infrastructure (e.g.
	// hanzo.ai, hanzo.app). A Host at or under any of them is never a customer
	// custom-domain candidate, so the high-traffic api/console path is never
	// subjected to a per-request binding lookup, and a customer binding can never
	// shadow a real Hanzo host. The apex is always treated as self.
	SelfDomains []string
	// FirstPartyApex is a SelfDomain (e.g. hanzo.ai) on which we serve OUR OWN
	// sites — the brand's own pages, not customer sites.
	//
	// It uses the SAME security model as Apex: sites are the default and the
	// Reserved denylist carves out our app hosts. It used to be the opposite, an
	// opt-in allowlist (CLOUD_SITES_FIRSTPARTY), on the reasoning that this apex
	// carries api/console/iam/kms and cannot afford a denylist gap. Two things
	// retired that. The gap is not an account-takeover here, because resolution on
	// this apex is PINNED to FirstPartyOrg — a customer's same-named project can
	// never be served on it, so the worst case is our own org shadowing our own
	// host. And one policy is better than two that can disagree about a label:
	// createProject and BindHost already enforce Reserved, so the allowlist was a
	// second gate answering the same question, kept by hand, in an env var.
	//
	// The obligation it takes on is real and is stated where it bites: adding an
	// internal host to the estate means naming its label in Reserved in the same
	// change. TestSiteSlugBrandApex is what fails when that is forgotten.
	FirstPartyApex string
	FirstPartyOrg  string
}

// Server is the host-routed public site edge. It holds the object store
// (sdk/object, the SAME store as the deploy blob store) plus the apex. The
// reserved-label policy is the package-level shared source (reserved.go), so
// serve/create/bind never disagree. It reads the resolver at request time.
type Server struct {
	apex      string
	brandApex string // the apex serving OUR OWN sites (hanzo.ai); "" = none
	brandOrg  string // the org that owns them — resolution on brandApex is PINNED to it
	store     object.Store
	log       luxlog.Logger
}

// New builds the Server from Config. An empty apex defaults to hanzo.app.
// Operator-supplied Reserved labels are registered into the shared reserved source
// (ADDING to the baked-in defaults, never removing them), so createProject and
// BindHost enforce the exact same set the serve gate does.
func New(cfg Config, log luxlog.Logger) *Server {
	apex := siteZone(cfg.Apex)
	// Publish the COMPLETE policy — the reserved labels AND the domains we run — so
	// the claim gate refuses what the serve gate would refuse. selfOf folds the
	// first-party apex in itself, so the published set does not depend on where in
	// this function it is published from; see selfOf for the bug that ordering used
	// to carry. A process that never constructs a Server derives the same policy
	// from the same environment (reserved.go seed).
	SetReservedExtra(cfg.Reserved)
	SetSelfDomains(selfOf(cfg))
	// The brand apex serves sites owned by ONE org, and needs both: the org PINS
	// resolution so a customer's same-named project can never shadow an internal
	// host. Missing either ⇒ disabled (fail-closed).
	brandApex := strings.ToLower(strings.TrimSpace(cfg.FirstPartyApex))
	brandOrg := strings.ToLower(strings.TrimSpace(cfg.FirstPartyOrg))
	if brandOrg == "" {
		brandApex = ""
	}
	return &Server{apex: apex, brandApex: brandApex, brandOrg: brandOrg, store: object.Default(), log: log.New("subsystem", "sites")}
}

// Middleware is the host-router. Three outcomes, in order:
//
//  1. `<slug>.<apex>` (e.g. myapp.hanzo.app) → serve the site that holds that
//     name (terminal). Names are one namespace across every org, its apps and
//     its sites, claimed first come first served at create.
//  2. a bound CUSTOM domain (e.g. yadota.tech, a customer's own apex pointed at
//     this edge) → serve that project's site from its S3 prefix (terminal). Only
//     an external host (not one of OUR self domains) with a LIVE binding qualifies.
//  3. anything else — our API/console hosts, or an unbound external host routed
//     here — → Continue(), so the normal /v1 + console pipeline runs unchanged.
//
// A published site (either shape) is a PUBLIC artifact: it returns HERE, never
// entering the authenticated/billed API pipeline.
// baseHostHandler serves the org's Base data plane (/v1/base, /v1/realtime, /_/)
// on a published site host — host-as-project-ref (HIP-0014). Nil (the default)
// leaves a site host serving only its static files; base.Use installs it,
// gated by CLOUD_BASE_PUBLIC_HOST. The org comes ONLY from the resolved Site
// (the subdomain), never the caller — the same server-supplied tenant key the
// file plane trusts. Authz is Base's own collection rules.
var baseHostHandler func(org string, c *zip.Ctx) error

// SetBaseHostHandler installs the per-org Base handler (see baseHostHandler).
func SetBaseHostHandler(h func(org string, c *zip.Ctx) error) { baseHostHandler = h }

// isBasePath reports whether a path targets the Base data plane or admin.
func isBasePath(p string) bool {
	return strings.HasPrefix(p, "/v1/base") || strings.HasPrefix(p, "/v1/realtime") ||
		p == "/_" || strings.HasPrefix(p, "/_/")
}

// A site host serves BYTES, and nothing else. There used to be an ingest carve
// here: a POST from a page on <slug>.hanzo.app was routed into the analytics
// anonymous lane with the resolved Site's org as the tenant — host-as-tenant, a
// SECOND attribution mechanism beside the key, and one that could only ever write
// the projected subset because this middleware runs before the identity boundary
// and so can vouch for nothing.
//
// It is gone. A site's beacon carries the project key minted with the project and
// posts it to the ingest endpoint like every other caller, which is one mechanism
// instead of two and gives a site's own analytics full fidelity rather than the
// anonymous projection. A beacon POST to a site host is now what every other
// unknown path on a site host is: served from the site's bytes, or 404.

// Host is the ONE way this server learns which host was asked for.
//
// fiber parses the request URI once, and behind the ingress the parsed host is
// EMPTY — so Hostname() alone resolved nothing, every published site fell
// through to c.Continue(), and <slug>.hanzo.app served the console SPA with the
// whole cloud API mounted under a customer's own hostname. Measured 2026-08-03:
// quest.hanzo.app returned <title>Hanzo Cloud Console and
// quest.hanzo.app/v1/billing/plans returned 200. Same accessor, same failure as
// commerce's tenant resolver earlier the same night.
//
// The parsed host ALWAYS wins. X-Forwarded-Host is consulted only when there is no
// parsed HOSTNAME at all, which is exactly the ingress case and never a direct
// request. That ordering is the security property, not a detail: the host picks the
// ORG here, so a client that could override a real host could serve itself another
// tenant's site.
//
// "Is this a hostname" is the whole question, and it used to be asked as "is this a
// host we would SERVE" — siteSlug, else customCandidate. Those are not the same
// question, and the gap between them is precisely OUR OWN domains: api.hanzo.ai
// names no site, and customCandidate excludes it BY DESIGN (IsSelfHost), so neither
// arm fired and the client-supplied header won. `Host: login.hanzo.ai` with
// `X-Forwarded-Host: <any bound custom domain>` served that domain's site — a
// tenant's content returned for a request addressed to our auth apex, needing no
// site_hosts row on hanzo.ai at all. The two tests that look like they pinned this
// both use a host that IS a site, so the early return fired and the header was
// never read; they proved the property only where it already held.
//
// Whether we serve a host has nothing to do with which host was asked for, so this
// no longer asks: a parsed name with a dot is a hostname and is final. What remains
// for the header is what it was added for — the ingress case, where fiber parses no
// host at all — plus bare internal names (`localhost`, a short service name) that
// no client is addressing us by.
func Host(c *zip.Ctx) string {
	if parsed := hostOnly(c.Fiber().Hostname()); strings.Contains(parsed, ".") {
		return parsed
	}
	// Left-most entry: proxies append, so the first is the client-facing name.
	fwd := c.Header("X-Forwarded-Host")
	if i := strings.IndexByte(fwd, ','); i >= 0 {
		fwd = fwd[:i]
	}
	return hostOnly(fwd)
}

func (s *Server) Middleware() zip.Handler {
	return func(c *zip.Ctx) error {
		raw := Host(c)
		if slug, firstParty, ok := s.siteSlug(raw); ok {
			if baseHostHandler != nil && isBasePath(c.Path()) {
				if site, ok := s.resolveLivePinned(c.Context(), slug, firstParty); ok {
					return baseHostHandler(site.Org, c)
				}
			}
			return s.serve(c, slug, firstParty)
		}
		if host := hostOnly(raw); s.customCandidate(host) {
			if site, ok := s.resolveLive(c.Context(), host); ok {
				if baseHostHandler != nil && isBasePath(c.Path()) {
					return baseHostHandler(site.Org, c)
				}
				return s.serveCustom(c, site)
			}
		}
		return c.Continue()
	}
}

// hostOnly lowercases a Host header value and strips any :port.
func hostOnly(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host
}

// customCandidate reports whether a Host is eligible to be a bound CUSTOM domain.
// It excludes anything at or under one of OUR self domains (hanzo.ai / hanzo.app /
// …), the empty/bare host, and any host without a dot (never a public FQDN).
// Excluding self hosts keeps the api/console path free of any per-request binding
// lookup and makes it structurally impossible for a customer binding to shadow a
// real Hanzo host — only genuinely external domains reach the binding resolver.
func (s *Server) customCandidate(host string) bool {
	return host != "" && strings.Contains(host, ".") && !IsSelfHost(host)
}

// resolveLive resolves a key (a subdomain slug OR a full custom host) to a LIVE
// Site via the current resolver. A missing resolver, a resolve error, an unbound
// key, or a non-live status all report ok=false — the caller then falls through
// (custom path) or renders an honest 404 (slug path).
func (s *Server) resolveLive(ctx context.Context, key string) (Site, bool) {
	r := currentResolver()
	if r == nil {
		return Site{}, false
	}
	site, found, err := r.Resolve(ctx, key)
	if err != nil {
		s.log.Error("resolve failed", "key", key, "err", err)
		return Site{}, false
	}
	if !found || site.Status != "live" {
		return Site{}, false
	}
	return site, true
}

// resolveLivePinned resolves a slug to a LIVE Site, org-PINNED for a first-party
// host (ResolveOrg over s.brandOrg — never the unique-across-orgs fallback, so
// a customer's same-named project can never be served on our internal apex) and via
// the normal resolver otherwise. Same ok=false failure modes as resolveLive.
func (s *Server) resolveLivePinned(ctx context.Context, slug string, firstParty bool) (Site, bool) {
	if !firstParty {
		return s.resolveLive(ctx, slug)
	}
	r := currentResolver()
	if r == nil {
		return Site{}, false
	}
	site, found, err := r.ResolveOrg(ctx, s.brandOrg, slug)
	if err != nil {
		s.log.Error("resolve failed", "slug", slug, "org", s.brandOrg, "err", err)
		return Site{}, false
	}
	if !found || site.Status != "live" {
		return Site{}, false
	}
	return site, true
}

// framers is who may draw a published site in a frame: the surfaces the brand
// registry names as framing ours (brand.Framers), origin by origin — Dev's
// project panel shows the project's live site in one. The edge answers every host
// under the sites apex with X-Frame-Options: SAMEORIGIN, which refused that frame
// ("refused to connect"); a frame-ancestors policy is what a browser obeys in its
// place. It names our own surfaces and nothing else, so no other site can frame a
// published one, as none could before.
var framers = sync.OnceValue(func() string {
	return "frame-ancestors 'self' " + strings.Join(brand.Framers(), " ")
})

// serveCustom serves a resolved custom-domain Site. It shares the object-serving
// core (streamSite) with the slug path; only the method + storage guards are
// re-applied here (the slug path applies them in serve).
func (s *Server) serveCustom(c *zip.Ctx, site Site) error {
	c.SetHeader("X-Hanzo-Site", site.Slug)
	c.SetHeader("Content-Security-Policy", framers())
	if m := c.Method(); m != http.MethodGet && m != http.MethodHead {
		c.SetHeader("Allow", "GET, HEAD")
		return s.errorPage(c, http.StatusMethodNotAllowed, "method not allowed")
	}
	if err := object.Check(); err != nil {
		return s.errorPage(c, http.StatusServiceUnavailable, "storage not configured")
	}
	return s.streamSite(c, site)
}

// siteSlug resolves a Host to a site slug, or reports that this is not a site
// host. A site host is exactly `<slug>.<apex>`: one non-reserved label matching
// slugRE under the apex. The returned key is the bare slug, the exact string
// projects binds into site_hosts when the project is created, so the resolver's
// exact-host match finds it. The bare apex, a dotted label, reserved or malformed
// labels are NOT sites — they fall through to the normal pipeline. `firstParty` is
// true when the host is
// under the internal first-party apex — its resolution MUST be org-pinned (serve
// routes it through ResolveOrg, never the unique-across-orgs fallback).
func (s *Server) siteSlug(host string) (slug string, firstParty bool, ok bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // strip any :port
	}
	// Brand apex (e.g. hanzo.ai). The RESERVED SET is the boundary — the same one
	// createProject and BindHost enforce — so there is one policy here rather than
	// two. It used to be a hand-kept opt-in allowlist (CLOUD_SITES_FIRSTPARTY),
	// which meant every new site needed an operator to edit an env var, and the
	// two gates could disagree about the same label.
	//
	// Dropping the allowlist is safe because resolution on this apex is PINNED to
	// the owning org (resolveLivePinned → ResolveOrg): a customer's same-named
	// project can never be served here, so the exposure is not the account-takeover
	// reserved.go describes. What the reserved set must cover is OUR OWN internal
	// hosts, and it names them — api, console, iam, kms, id, and the artifact and
	// delivery plane (oci, pkg, ci, cd, s3, git).
	//
	// Terminal for this apex: a reserved host never reaches the multi-tenant branch
	// below, and the label must still be a bare valid slug so a dotted key
	// (`x.y.hanzo.ai`) cannot match.
	if s.brandApex != "" {
		if label, under := strings.CutSuffix(host, "."+s.brandApex); under {
			if !IsReserved(label) && !strings.Contains(label, ".") && slugRE.MatchString(label) {
				return label, true, true
			}
			return "", false, false
		}
	}
	suffix := "." + s.apex
	if !strings.HasSuffix(host, suffix) {
		return "", false, false
	}
	label := strings.TrimSuffix(host, suffix)
	// EXACTLY ONE non-reserved slug label — the bare `<slug>.<apex>`. That is the
	// ONE servable shape: a k8s wildcard Ingress host and a Let's Encrypt wildcard
	// cert each match exactly one label, so a dotted key neither routes nor gets
	// TLS — it is never a site, it falls through to the normal pipeline. The slug
	// must be non-reserved so a published site can never shadow a real app/api
	// host. The resolver then serves it iff it maps to exactly one live project
	// (its binding, else a unique live slug across orgs) — an ambiguous bare host
	// is an honest 404.
	if strings.Contains(label, ".") || IsReserved(label) || !slugRE.MatchString(label) {
		return "", false, false
	}
	return label, false, true
}

// serve resolves the slug to its Site and streams the requested object from the
// project's S3 prefix. Every failure renders an honest page; nothing is ever read
// outside `<Bucket>/<Prefix>/`.
func (s *Server) serve(c *zip.Ctx, slug string, firstParty bool) error {
	c.SetHeader("X-Hanzo-Site", slug)
	// Who may frame it, on every answer, its error pages included (framers).
	c.SetHeader("Content-Security-Policy", framers())

	// A published site is a static read surface: only GET/HEAD. Anything else is
	// 405 (hygiene + avoids cache-key confusion at the CDN).
	if m := c.Method(); m != http.MethodGet && m != http.MethodHead {
		c.SetHeader("Allow", "GET, HEAD")
		return s.errorPage(c, http.StatusMethodNotAllowed, "method not allowed")
	}

	r := currentResolver()
	if r == nil {
		return s.errorPage(c, http.StatusNotFound, "site not found")
	}
	var (
		site  Site
		found bool
		err   error
	)
	if firstParty {
		// Internal first-party host — resolve PINNED to the owning org, never the
		// unique-across-orgs fallback, so a customer's same-named project can't shadow it.
		site, found, err = r.ResolveOrg(c.Context(), s.brandOrg, slug)
	} else {
		site, found, err = r.Resolve(c.Context(), slug)
	}
	if err != nil {
		s.log.Error("resolve failed", "slug", slug, "err", err)
		return s.errorPage(c, http.StatusInternalServerError, "temporarily unavailable")
	}
	if !found || site.Status != "live" {
		return s.errorPage(c, http.StatusNotFound, "site not found")
	}
	if err := object.Check(); err != nil {
		return s.errorPage(c, http.StatusServiceUnavailable, "storage not configured")
	}

	return s.streamSite(c, site)
}

// streamSite streams the requested path from a resolved site's S3 prefix. It is
// the shared object-serving core for BOTH the `<slug>.hanzo.app` path (serve) and
// the custom-domain path (serveCustom). Tenant containment — every object key is
// rooted-clean under site.Prefix — lives in objectKey/resolveKey; this function
// only chooses the candidate keys, streams the first hit, and falls back to the
// site's own 404.
func (s *Server) streamSite(c *zip.Ctx, site Site) error {
	// Emit the edge cache-tag on every served object so a tag-purge
	// (projects.purgeTag → Edge.PurgeTags) invalidates exactly this project's site
	// at the edge. Derived from server-owned Namespace+Slug — the SAME tag the purger
	// targets, and the one the ingress stamps on the same prefix — never from the
	// request, so emit and purge never diverge.
	c.SetHeader("Cache-Tag", CacheTag(cmp.Or(site.Namespace, site.Org), site.Slug))

	rel := resolveKey(c.Path())
	// Candidate keys, tried in order: the exact object, then a directory-index
	// fallback for extension-less paths (so /docs serves docs/index.html), then
	// the SPA/site root index for the bare root, then the page the site declared
	// for the path (fallback.go). All candidates are prefix-bounded by objectKey
	// (rooted-clean), so no candidate can escape the tenant prefix.
	declared := func() (routes, error) { return s.declared(c.Context(), site) }
	for key, err := range s.candidates(rel, declared) {
		if err != nil {
			s.log.Warn("fallback table read failed", "org", site.Org, "slug", site.Slug, "err", err)
			return s.errorPage(c, http.StatusServiceUnavailable, "temporarily unavailable")
		}
		body, info, found, err := s.open(c.Context(), site.Bucket, objectKey(site.Prefix, key))
		if err != nil {
			// The store did not answer, twice. That is not a missing page, so it is
			// not a 404, and it is not a page at all, so it is not a 200.
			s.log.Warn("object read failed", "org", site.Org, "slug", site.Slug, "key", key, "err", err)
			return s.errorPage(c, http.StatusServiceUnavailable, "temporarily unavailable")
		}
		if !found {
			continue
		}
		// ONE cache policy, computed HERE, because here is what serves. The stored
		// object's own Cache-Control is deliberately NOT read.
		//
		// It used to be read first, with the computed policy only as a fallback,
		// and that made the STORED header authoritative — so whoever wrote the
		// object decided how the edge cached it. That is fine for the two paths the
		// server writes itself (uploadSite, copyRelease: both stamp
		// CacheControlFor). It is not fine for the third: a build writing its own
		// files against a presigned grant stamps whatever IT computed, and a client
		// re-implementation of this rule is a second copy that drifts. It had
		// drifted — measured on the same asset in the same request,
		// `_next/static/chunk.js` stored `max-age=3600` while this rule says
		// `max-age=31536000, immutable`, because isFingerprinted also matches the
		// build-output PREFIXES (`_next/static/`, `assets/`) and the client copy
		// only matched hashed BASENAMES. Every unhashed-but-immutable asset in
		// every Next export was being re-fetched hourly forever.
		//
		// The per-project override is not lost, it is passed properly: it is the
		// htmlOverride argument this function has always taken, carried on the Site
		// the resolver builds from the project row. So the override is an INPUT to
		// the one rule rather than a value smuggled through object metadata, and
		// changing it takes effect on the next request instead of on the next
		// redeploy.
		cc := CacheControlFor(key, site.CacheControl)
		ct := contentType(key)
		if ct == "" {
			ct = info.ContentType
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		return s.streamObject(c, site, body, info.Size, ct, cc, http.StatusOK)
	}
	if agentDoc(c.Path()) && brandHost(Host(c)) {
		return c.Continue()
	}
	return s.notFound(c, site)
}

// agentDocs are the discovery documents an agent reads off a host before it
// holds a credential: OAuth authorization-server (RFC 8414) and protected-resource
// (RFC 9728) metadata, the MCP server card and the A2A agent card, each at every
// address scanners probe. A brand's own site that ships none of these yields the
// address to what comes after this edge — the API's agent door (openapi/agent.go)
// and the iam app — which answer for the host the request reached, so hanzo.ai,
// cloud.hanzo.ai and hanzo.bot each describe themselves instead of 404ing
// (hanzo-inc/cloud#420, #421, #422). A site that ships its own copy keeps it:
// this runs only on a miss.
var agentDocs = map[string]bool{
	"/.well-known/oauth-authorization-server": true,
	"/.well-known/oauth-protected-resource":   true,
	"/.well-known/mcp/server-card.json":       true,
	"/.well-known/mcp.json":                   true,
	"/.well-known/agent-card.json":            true,
	"/.well-known/agent.json":                 true,
}

func agentDoc(p string) bool { return agentDocs[p] }

// brandHost reports whether host is one a brand claims for itself — its own
// domains and the first-party sites it lists, never a customer's custom domain
// and never a site published under a brand's domain (brand.Claimed).
func brandHost(host string) bool {
	_, ok := brand.Claimed(hostOnly(host))
	return ok
}

// candidates yields the ordered object keys to try for a cleaned request path.
// resolveKey never yields a trailing slash (path.Clean strips it), so a directory
// request and an extension-less path are the same case: try the exact key, then
// the two ways a static export can spell that page.
//   - ""            → index.html                                 (site root)
//   - "assets/a.js" → assets/a.js                                 (a file with an extension)
//   - "docs"        → docs, docs.html, then docs/index.html
//
// `rel + ".html"` is the one that makes Next.js hostable here. `output: export`
// without `trailingSlash` writes a route as the FLAT file `docs.html`, not as
// `docs/index.html` — so before this, a Next export served its homepage and 404'd
// every other route. Measured on the hanzo.ai export (759 pages): `/` and
// `/pricing.html` were 200 while `/pricing`, `/zen` and `/zen/models` were all
// 404, which is the shape of a site that looks deployed and is unusable.
//
// Both spellings are tried because both are legitimate: `trailingSlash: true`,
// Hugo, Jekyll and Vite's MPA output emit the directory-index form, and Next's
// default emits the flat form. Fixing this by setting `trailingSlash` in every
// repo would push a server limitation onto each site and change every canonical
// URL to do it; one extra candidate here costs a single HEAD miss on the paths
// that use the other convention.
//
// Last, a page-shaped path (the root, or no extension) is answered by the page
// the site declared for it in _fallback.json (fallback.go). declared is asked
// only once every key before it missed — the caller stops at the first hit — so
// a site without the file pays one HEAD, on a request that was a 404 anyway. A
// nil declared declares nothing. Its error is yielded as it is: a store that
// could not say whether a page was declared has not said the page is missing.
func (s *Server) candidates(rel string, declared func() (routes, error)) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		var keys []string
		switch {
		case rel == "":
			keys = []string{"index.html"}
		case path.Ext(rel) == "":
			keys = []string{rel, rel + ".html", rel + "/index.html"}
		default:
			keys = []string{rel}
		}
		for _, key := range keys {
			if !yield(key, nil) {
				return
			}
		}
		if declared == nil || path.Ext(rel) != "" {
			return
		}
		table, err := declared()
		if err != nil {
			yield("", err)
			return
		}
		if page, ok := table.page(rel); ok && !slices.Contains(keys, page) {
			yield(page, nil)
		}
	}
}

// headBytes is how much of an object the edge reads before it answers. Enough to
// hold a small page whole; small enough that holding it for every request in
// flight costs no more than fasthttp's own per-connection buffers.
const headBytes = 8 << 10

// open reads the start of an object and returns a body that yields the WHOLE
// object: those first bytes, then the rest of the same read. found=false means the
// store does not have the key; err means the store could not answer, after one
// retry. The caller must Close the body (fasthttp does, once it is written).
//
// The first bytes are read HERE, in the handler, and that is the point of it. The
// response writer runs after the status is committed, where a failed read can only
// drop the connection. Read here, a failure (a publish replaced the object between
// the stat and the read, a store error) is retried, and a store that still cannot
// answer gets an honest 503.
func (s *Server) open(ctx context.Context, bucket, key string) (io.ReadCloser, object.Info, bool, error) {
	var err error
	for range 2 {
		body, info, found, ferr := s.fetch(ctx, bucket, key)
		if ferr == nil {
			return body, info, found, nil
		}
		err = ferr
	}
	return nil, object.Info{}, false, err
}

// fetch is one attempt of open. Existence is the stat's answer: a store may answer
// a read of a DIRECTORY key with an empty 200 and a stat with 404, so only the stat
// can tell `docs` the directory from `docs` the page. The read is then held to the
// object the stat described: one that changed in between is a failed attempt.
func (s *Server) fetch(ctx context.Context, bucket, key string) (io.ReadCloser, object.Info, bool, error) {
	info, err := s.store.Stat(ctx, bucket, key)
	if err != nil {
		if missing(err) {
			return nil, object.Info{}, false, nil
		}
		return nil, object.Info{}, false, err
	}
	obj, err := s.store.Open(ctx, bucket, key, object.Get{})
	if err != nil {
		// The stat said the object is there, so a read that fails is the store
		// failing, whatever it says; a 404 (removed since the stat) is settled by the
		// retry's own stat, not guessed here.
		return nil, object.Info{}, false, err
	}
	if obj.Info.ETag != info.ETag {
		_ = obj.Close()
		return nil, object.Info{}, false, errors.New("sites: the object changed while it was read")
	}
	// A size the store did not give reads nothing here.
	head := make([]byte, max(min(info.Size, headBytes), 0))
	if len(head) > 0 {
		if _, err := io.ReadFull(obj, head); err != nil {
			_ = obj.Close()
			return nil, object.Info{}, false, err
		}
	}
	if info.Size >= 0 && info.Size <= int64(len(head)) {
		_ = obj.Close()
		return io.NopCloser(bytes.NewReader(head)), info, true, nil
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), obj), obj}, info, true, nil
}

// missing reports a store answer that means "no such object": the object or its
// bucket is not there, or the store refused the name as no name it holds (a
// refusal that is not "not now": a failed precondition, a timeout, a rate limit).
// A 5xx or a transport error is the store failing, which open retries and then
// reports.
func missing(err error) bool { return object.Absent(err) || object.Refused(err) }

// streamObject streams an object body straight to the client — NO full-object
// buffering. This runs on the unauthenticated, gateway-bypassed edge, so buffering
// a whole object would let concurrent large-asset requests OOM the pod; past the
// headBytes open already holds, a direct stream costs one small fasthttp copy
// buffer regardless of object size.
//
// Content-Length is set from info.Size (SendStream's size arg). fasthttp CLOSES the
// body after it is written (it implements io.Closer) — so we must NOT Close it
// here (that would truncate the stream); the response writer owns the close.
func (s *Server) streamObject(c *zip.Ctx, site Site, body io.ReadCloser, size int64, contentType, cacheControl string, status int) error {
	c.SetHeader("Content-Type", contentType)
	if cacheControl != "" {
		c.SetHeader("Cache-Control", cacheControl)
	}
	// Opt-in cross-origin isolation, decided by object role from the content type
	// just set (documents vs subresources). No-op unless the site enabled it.
	for _, h := range crossOriginIsolation(site.CrossOriginIsolation, contentType) {
		c.SetHeader(h[0], h[1])
	}
	// Who may READ a subresource, decided the same way, from the same content type.
	for _, h := range cors(site.CrossOriginIsolation, contentType) {
		c.SetHeader(h[0], h[1])
	}
	c.Status(status)
	return c.Fiber().SendStream(body, int(size))
}

// notFound serves the site's own 404.html when it published one, else a plain
// default 404. The 404.html is fetched from the SAME prefix (tenant-bounded) and
// streamed, not buffered.
func (s *Server) notFound(c *zip.Ctx, site Site) error {
	// A missing DATA asset is answered in the media type it asked for, never with
	// markup. A single-page app fetches its data files and calls .json() on the
	// result; handing that call an HTML page (the site's 404.html, or ours) turns
	// "the file is not deployed" into
	//
	//     Unexpected token '<', "<!doctype "... is not valid JSON
	//
	// thrown from inside minified vendor code with no URL attached — which is the
	// error hanzo-team (a missing /config.json) and edge (a missing
	// presets/wigglewobble.json) BOTH surfaced, from two unrelated causes. The
	// status was always an honest 404; only the body lied about its type. Answering
	// in-type makes .json() succeed and hands the app the path that is missing, so
	// the next person reads the cause instead of a parser's opinion of an HTML
	// doctype. 404.html is for humans reading a page, so a data request never gets
	// it. Keyed off the SAME contentType() the 200 path uses — one type table.
	if isJSON(contentType(resolveKey(c.Path()))) {
		c.SetHeader("Content-Type", "application/json; charset=utf-8")
		c.SetHeader("Cache-Control", "no-store, max-age=0")
		body, err := json.Marshal(map[string]string{"error": "not found", "path": c.Path()})
		if err != nil { // unreachable: two strings always marshal
			return s.errorPage(c, http.StatusNotFound, "page not found")
		}
		return c.Bytes(http.StatusNotFound, body)
	}
	// Every candidate is already known missing, so the answer is 404 whichever
	// page carries it: a store that cannot read 404.html right now gets ours.
	body, info, found, err := s.open(c.Context(), site.Bucket, objectKey(site.Prefix, "404.html"))
	if err == nil && found {
		return s.streamObject(c, site, body, info.Size, "text/html; charset=utf-8", "no-store, max-age=0", http.StatusNotFound)
	}
	return s.errorPage(c, http.StatusNotFound, "page not found")
}

// isJSON reports whether a media type is JSON — the bare type or any +json
// structured suffix (application/manifest+json, application/ld+json), which are
// all parsed with JSON.parse by the code that fetched them.
func isJSON(ct string) bool {
	base, _, _ := strings.Cut(ct, ";")
	base = strings.TrimSpace(base)
	return base == "application/json" || base == "text/json" || strings.HasSuffix(base, "+json")
}

// errorPage renders a minimal, honest HTML status page. It never leaks internal
// detail — msg is a fixed, safe string chosen by the caller.
func (s *Server) errorPage(c *zip.Ctx, status int, msg string) error {
	c.SetHeader("Content-Type", "text/html; charset=utf-8")
	c.SetHeader("Cache-Control", "no-store, max-age=0")
	body := "<!doctype html><html><head><meta charset=\"utf-8\"><title>" +
		httpStatusText(status) + "</title></head><body style=\"font-family:system-ui;text-align:center;padding:4rem\"><h1>" +
		httpStatusText(status) + "</h1><p>" + htmlEscape(msg) + "</p></body></html>"
	return c.Bytes(status, []byte(body))
}

// resolveKey turns a request path into a cleaned, tenant-relative key fragment.
// This is the traversal boundary: it percent-decodes, roots the path at "/",
// runs path.Clean (which resolves every "." and ".." segment against that root),
// then strips the leading "/". The result provably contains no ".." segment, so
// joining it under a fixed prefix can never escape that prefix — regardless of
// how many "..", backslashes, or percent-encoded dots the client sends.
//
// Decoding is FIRST and it is not optional. c.Path() is the RAW request target —
// zip hands back Fiber's path verbatim, nothing upstream unescapes it — while an
// object key is stored decoded, so an encoded request could never match its own
// file. Next.js names a dynamic route's chunk after the literal segment
// (app/blog/[slug]/page-*.js), every browser sends that as %5Bslug%5D, and so
// every dynamic page on hanzo.app 404'd its own JS and rendered without ever
// hydrating. Decoding before Clean also means "%2e%2e" is collapsed as the
// traversal it is, rather than surviving as an opaque literal.
func resolveKey(reqPath string) string {
	if dec, err := url.PathUnescape(reqPath); err == nil {
		reqPath = dec // malformed escapes stay verbatim: a miss, never an error
	}
	p := strings.ReplaceAll(reqPath, `\`, "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	clean := path.Clean(p) // e.g. "/../../a" → "/a", "/a/./b" → "/a/b"
	rel := strings.TrimPrefix(clean, "/")
	if rel == "." {
		return ""
	}
	return rel
}

// objectKey joins a tenant prefix with a cleaned relative key. rel comes from
// resolveKey (no ".." segments) OR is a fixed literal ("index.html", "404.html"),
// so the result is always within `<prefix>/`. This is the single join site —
// tenant containment lives here and in resolveKey, nowhere else.
func objectKey(prefix, rel string) string {
	return prefix + "/" + rel
}

// ContentType maps a key to a MIME type by extension, pinning standard web and
// game-engine asset types explicitly before falling back to mime.TypeByExtension.
// Minimal container images often lack or carry outdated /etc/mime.types entries
// (missing .mjs/.cjs/.map or mis-typing scripts/WASM), which causes browsers to
// reject module scripts and stylesheets under X-Content-Type-Options: nosniff
// and WebAssembly.instantiateStreaming to fail. Exported so the deploy upload
// and release-copy paths share the exact same type table as the site server.
func ContentType(key string) string {
	if ct := pinnedAssetType(path.Ext(key)); ct != "" {
		return ct
	}
	return mime.TypeByExtension(path.Ext(key))
}

func contentType(key string) string { return ContentType(key) }

// pinnedAssetType returns the canonical MIME type for web and web-game assets
// that must never drift across host /etc/mime.types tables, or "" to defer to
// the stdlib table.
func pinnedAssetType(ext string) string {
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".cjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".wasm":
		return "application/wasm"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".data", ".mem", ".unityweb", ".pck": // Unity/Emscripten payloads, Godot pack
		return "application/octet-stream"
	default:
		return ""
	}
}

// crossOriginIsolation is the ONE cross-origin-isolation policy by object role,
// mirroring contentType/CacheControlFor: a pure function keyed off the object's
// content type. It returns nil (no headers) unless the site opted in — isolation
// blocks a page from embedding third-party cross-origin content, so it is NEVER
// global, only for sites the resolver marked (a declared WebGL game engine).
//
//   - The DOCUMENT that spawns the workers (text/html) carries
//     Cross-Origin-Opener-Policy: same-origin + Cross-Origin-Embedder-Policy:
//     require-corp — the exact pair a browser requires before it grants the page a
//     crossOriginIsolated context (the gate that re-enables SharedArrayBuffer).
//   - every OTHER object is a subresource (.js/.wasm/.data/...) that a require-corp
//     document loads only if it is same-origin, so it carries
//     Cross-Origin-Resource-Policy: same-origin. Assets are same-origin here (one
//     S3 prefix, one host), so this makes them loadable rather than blocked.
func crossOriginIsolation(enabled bool, contentType string) [][2]string {
	if !enabled {
		return nil
	}
	if strings.HasPrefix(contentType, "text/html") {
		return [][2]string{
			{"Cross-Origin-Opener-Policy", "same-origin"},
			{"Cross-Origin-Embedder-Policy", "require-corp"},
		}
	}
	return [][2]string{{"Cross-Origin-Resource-Policy", "same-origin"}}
}

// cors is the ONE cross-origin READ policy for a site's subresources, keyed off
// the same content type as the two policies above.
//
// It grants nothing that was not already public. These bytes are served on the
// unauthenticated edge with no credentials, so anyone can fetch them today; a
// CORS header decides whether a script may READ the response it already
// received, and for public files that distinction protects nobody.
//
// What it buys is the builder. hanzo.app previews a project inside a frame
// sandboxed WITHOUT allow-same-origin — deliberately, so untrusted generated
// HTML cannot reach the IAM tokens — which makes the frame an opaque origin. A
// Vite build's entry is `<script type="module" crossorigin>`, and a module
// ALWAYS fetches in CORS mode, so with no Access-Control-Allow-Origin the bundle
// is refused, nothing mounts into `<div id="root">`, and a perfectly healthy
// deployed site previews as a blank white page.
//
//   - a DOCUMENT is navigated to, not read cross-origin, and the preview brings
//     its own; it gets nothing.
//   - a site that opted into cross-origin ISOLATION asked for same-origin
//     subresources, and this must not quietly widen that back out.
func cors(isolated bool, contentType string) [][2]string {
	if isolated || strings.HasPrefix(contentType, "text/html") {
		return nil
	}
	return [][2]string{{"Access-Control-Allow-Origin", "*"}}
}

// CacheControlFor is the ONE canonical cache policy by asset class, used both when
// WRITING an object at deploy (projects/blob.go) and when SERVING one here, so a
// site's TTL is identical on the site-server path and the direct-S3 path.
//
//   - HTML documents  → short browser TTL + long shared-cache TTL: a redeploy is
//     seen fast (60s), while the CDN holds it for a day (purged instantly on
//     redeploy by cache-tag). htmlOverride, when non-empty, replaces this for a
//     project (the per-project cacheControl knob); it applies to documents only.
//   - content-hashed / immutable assets → cache for a year (a new build changes
//     the hash, so the URL is safe forever). NOT overridable — always correct.
//   - everything else → a conservative middle TTL.
func CacheControlFor(key, htmlOverride string) string {
	switch ext := strings.ToLower(path.Ext(key)); {
	case ext == ".html" || ext == ".htm":
		if strings.TrimSpace(htmlOverride) != "" {
			return htmlOverride
		}
		return "public, max-age=60, s-maxage=86400"
	case Immutable(key):
		return "public, max-age=31536000, immutable"
	default:
		return "public, max-age=3600"
	}
}

// Immutable reports whether key is served as immutable: an asset type whose
// bytes a bundler fingerprints, at a fingerprinted address. A new build changes
// the address, so the bytes at this one never change — which is also why a
// release may leave such a key behind for a while: a page of the previous
// release that is still open asks for it by that address.
func Immutable(key string) bool {
	switch strings.ToLower(path.Ext(key)) {
	case ".js", ".mjs", ".cjs", ".css", ".map", ".woff", ".woff2", ".png", ".jpg", ".jpeg",
		".gif", ".svg", ".webp", ".avif", ".ico", ".ttf", ".otf", ".wasm",
		// Game-engine payloads. pinnedAssetType already teaches this file that a
		// site can be a WebGL build; the cache policy has to know it too, or the
		// biggest object in the deploy (Unity's .data, Godot's .pck — megabytes
		// each) is the ONLY fingerprinted asset that still gets re-fetched every
		// hour. Same rule, same reason: a content-hashed name cannot go stale.
		".data", ".pck", ".unityweb", ".mem",
		// Film. The same sentence as the line above, and it was missed for the
		// same reason — the list grew from what a page is BUILT of rather than
		// from what it SHIPS. A marketing export is mostly video by weight now:
		// hanzo.ai carries ~105 mp4 product films, several megabytes apiece and
		// far heavier than any script it serves, and every one of them fell to
		// `default`. That is the same TTL, so nothing was mis-cached — but a
		// FINGERPRINTED film could never earn `immutable` either, which is the
		// one asset class where a year of edge life is worth the most bytes.
		".mp4", ".webm", ".mov", ".m4v", ".ogv":
		return isFingerprinted(key)
	}
	return false
}

// buildOutputDirs are the content-addressed directories bundlers write
// fingerprinted output to: Next.js (`_next/static/`), Vite/Rollup/esbuild
// (`assets/`), Astro (`_astro/`), and SvelteKit (`_app/immutable/`). Everything
// under one — at the site root OR under a nested mount (`pay/assets/...`,
// `sandbox/assets/...`, `assets/_next/static/...`) — is immutable by
// construction, because the bundler renames the file whenever its bytes change.
//
// Matching both top-level and nested segments is load-bearing: a sub-path app
// mounted inside a site prefix (e.g. `pay/assets/index-DeKYn5x6.js`) used to
// miss a root-only HasPrefix check, so deploy reconciliation deleted its old
// hashed chunks immediately instead of retaining them for the 36-hour grace
// window, causing open tabs to receive 404 text/plain under strict MIME checks.
var buildOutputDirs = []string{
	"_next/static/",
	"assets/",
	"_astro/",
	"_app/immutable/",
}

func isFingerprinted(key string) bool {
	k := strings.TrimPrefix(key, "/")
	for _, dir := range buildOutputDirs {
		if strings.HasPrefix(k, dir) || strings.Contains(k, "/"+dir) {
			return true
		}
	}
	return hasContentHash(path.Base(k))
}

// hasContentHash matches content-hashed basenames outside buildOutputDirs across
// modern bundlers: webpack/Rollup hex (`app.4f3a9c21.js`), Vite base64url
// (`index-DeKYn5x6.js`, `index-De_Yn5x6.js`), esbuild base32 (`main-KSQZTTM6.js`),
// and bare Turbopack/Next hashes (`3h6wuqw8qhrkb.js`, `bdec3a94ead6ad5f.css`).
// Ordinary hyphenated filenames (`service-worker.js`, `hanzo-platform.svg`,
// `Google-Analytics.js`) are rejected so root assets are never pinned forever.
func hasContentHash(base string) bool {
	ext := path.Ext(base)
	if ext == "" {
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	if strings.EqualFold(ext, ".map") {
		if inner := path.Ext(stem); inner != "" {
			stem = strings.TrimSuffix(stem, inner)
		}
	}
	if stem == "" {
		return false
	}
	// Separated hash token after '.' or '-' (allowing '_' inside Vite base64url hashes).
	if i := strings.LastIndexAny(stem, ".-"); i >= 0 {
		if isHashToken(stem[i+1:], true) {
			return true
		}
	}
	// Separated hash token after '_' (e.g. vendor_0a1b2c3d.js).
	if i := strings.LastIndexByte(stem, '_'); i >= 0 {
		if isHashToken(stem[i+1:], false) {
			return true
		}
	}
	// Bare hash basename with no separator (e.g. Turbopack 13-char base36 or
	// Next.js 16-char hex: `3h6wuqw8qhrkb.js`, `bdec3a94ead6ad5f.css`).
	if !strings.ContainsAny(stem, ".-_") && len(stem) >= 12 {
		return isBareHash(stem)
	}
	return false
}

// isHashToken reports whether tok (length >= 8) looks like a bundler content
// hash: pure hex, alphanumeric mixing digits and letters (base32/base36/base64url),
// or high-entropy mixed-case base64url with multiple case transitions.
func isHashToken(tok string, allowUnderscore bool) bool {
	if len(tok) < 8 {
		return false
	}
	allHex := true
	var digits, upper, lower, transitions int
	prevClass := 0
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		var class int
		switch {
		case c >= '0' && c <= '9':
			digits++
			class = 1
		case c >= 'a' && c <= 'f':
			lower++
			class = 3
		case c >= 'A' && c <= 'F':
			upper++
			class = 2
		case c >= 'g' && c <= 'z':
			allHex = false
			lower++
			class = 3
		case c >= 'G' && c <= 'Z':
			allHex = false
			upper++
			class = 2
		case allowUnderscore && c == '_':
			allHex = false
			class = 4
		default:
			return false
		}
		if prevClass != 0 && class != prevClass {
			transitions++
		}
		prevClass = class
	}
	if allHex {
		return true
	}
	if digits > 0 && (upper > 0 || lower > 0) {
		return true
	}
	return upper >= 2 && lower >= 2 && transitions >= 4
}

// isBareHash reports whether a separator-free basename stem (length >= 12) is a
// bare bundler hash (pure hex, or alphanumeric containing at least one digit).
func isBareHash(stem string) bool {
	allHex := true
	hasDigit := false
	for i := 0; i < len(stem); i++ {
		c := stem[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
		case (c >= 'g' && c <= 'z') || (c >= 'G' && c <= 'Z'):
			allHex = false
		default:
			return false
		}
	}
	return allHex || hasDigit
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func httpStatusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return t
	}
	return "Error"
}
