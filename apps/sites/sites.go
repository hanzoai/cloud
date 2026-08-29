// Package sites is your published site, live on the public web at
// <slug>.hanzo.app.
//
// It is the host-routed edge that turns `<slug>.hanzo.app` into the static site
// a user deployed to OUR S3.
//
// It is NOT a /v1 API. It owns the ROOT path space for requests whose Host is a
// site host (`<slug>.<apex>`, apex default hanzo.app). It is installed as the
// FIRST middleware in the compose root (serve.go), before identity/billing, so a
// public site GET never enters the authenticated API pipeline — a site is a
// public artifact, not a tenant API call.
//
// Tenant isolation is the whole point (this is RED-reviewed). The org and the S3
// prefix a request may read come ONLY from the store lookup keyed by the
// validated subdomain slug — NEVER from the request path, a client header, or the
// Host beyond the one validated label. One slug ⇒ exactly one `<org>/<slug>/` S3
// prefix, hard-bounded, and the object key is rooted-clean so no `..`/encoded
// traversal can escape that prefix into another project or org. See resolveKey +
// its exhaustive test.
//
// The resolver (slug → {org,bucket,prefix,status}) is the projects store,
// injected via SetResolver at mount. sites does NOT import projects (projects
// imports cloud, cloud imports sites — importing projects here would form a
// cycle); the store implements the tiny Resolver interface and registers itself.
package sites

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"

	s3 "github.com/hanzos3/go"
	"github.com/zap-proto/zip"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/s3admin"
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
	Org    string
	Slug   string
	Bucket string
	Prefix string
	Status string
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

// VerifiedHost reports the org that owns host as a VERIFIED public site host.
//
// It is the SAME read the site edge serves from — Resolver.Resolve, which is
// Store.ResolveHost, which filters `status='verified'` — asked for the one fact a
// caller outside this package can need about a hostname: whose is it, and has the
// owner PROVED it. A host with only a pending claim resolves to nothing here,
// because a pending row holds its name against the PK but never routes; that
// filter is the hostname-hijack boundary, and asking through this function is what
// keeps every caller on the right side of it instead of growing a second lookup
// that could forget the status.
//
// found=false on a miss AND on a resolver error, which is deliberate and is the
// difference between this and Resolve: the serve path must tell "no such site"
// (404) from "could not ask" (503), because serving a 404 for a live customer site
// during a transient failure looks exactly like deletion. A caller asking "is this
// host proven" has no such distinction to make — an answer we could not obtain is
// not a proof — so the error collapses into "no", and a caller cannot forget to
// check a second return.
func VerifiedHost(ctx context.Context, host string) (string, bool) {
	r := currentResolver()
	if r == nil {
		return "", false
	}
	site, ok, err := r.Resolve(ctx, host)
	if err != nil || !ok || site.Org == "" {
		return "", false
	}
	return site.Org, true
}

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
	// FirstPartyApex is a SelfDomain (e.g. hanzo.ai) on which we serve a small set
	// of OUR OWN first-party sites (cd/flow/gallery.hanzo.ai) — the brand's own
	// pages, not customer sites. It uses the OPPOSITE security model to Apex: Apex
	// is multi-tenant, so sites are the DEFAULT and a denylist (Reserved) carves out
	// app hosts; the brand apex carries api/console/iam/kms/… and CANNOT afford a
	// denylist gap (one missing label = a project shadowing a real host → the OAuth
	// account-takeover in reserved.go), so here sites are OPT-IN: ONLY a label in
	// FirstPartySites serves, everything else falls through to the normal pipeline,
	// protected by default. Empty = no first-party sites (the multi-tenant-only
	// default). The apex itself is unaffected (hanzo.app stays the site default).
	FirstPartyApex  string
	FirstPartySites []string
	// FirstPartyOrg is the org that OWNS the first-party sites (hanzo). A first-party
	// host resolves PINNED to this org — never unique-across-orgs — so a customer's
	// same-named project can never be served on our internal apex. Empty ⇒ the
	// first-party sites cannot resolve (fail-closed), so it must be set when
	// FirstPartyApex is.
	FirstPartyOrg string
}

// Server is the host-routed public site edge. It holds the S3 access path
// (s3admin, the SAME credentials as the deploy blob store) plus the apex. The
// reserved-label policy is the package-level shared source (reserved.go), so
// serve/create/bind never disagree. It reads the resolver at request time.
type Server struct {
	apex            string
	firstPartyApex  string          // internal apex serving OUR opt-in first-party sites (hanzo.ai); "" = none
	firstPartySites map[string]bool // the explicit allowlist of first-party site labels on that apex
	firstPartyOrg   string          // the org that owns the first-party sites — resolution is PINNED to it
	admin           s3admin.Admin
	log             luxlog.Logger
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
	// First-party apex (internal, opt-in sites) — normalize and build the explicit
	// allowlist. Empty apex or empty owning org ⇒ disabled.
	fpApex := strings.ToLower(strings.TrimSpace(cfg.FirstPartyApex))
	fpOrg := strings.ToLower(strings.TrimSpace(cfg.FirstPartyOrg))
	fpSites := map[string]bool{}
	// First-party sites require BOTH an apex AND an owning org: the org PINS
	// resolution so a customer's same-named project can never shadow an internal
	// host. Missing either ⇒ disabled (fail-closed) — no <label>.<fpApex> ever
	// resolves as a site; every such host falls through to the normal pipeline.
	if fpApex != "" && fpOrg != "" {
		for _, l := range cfg.FirstPartySites {
			l = strings.ToLower(strings.TrimSpace(l))
			if l == "" {
				continue
			}
			// Belt-and-suspenders on the brand apex (RED F-2): NEVER let a reserved
			// label (api/login/wallet/console/…) be published as a first-party site,
			// even if an operator lists it — those hosts must stay real app/auth
			// surfaces. Drop it loudly; it can never resolve as a site.
			if IsReserved(l) {
				log.Warn("first-party site label is reserved — dropped", "label", l)
				continue
			}
			fpSites[l] = true
		}
	} else {
		fpApex = "" // no owning org ⇒ never serve a first-party site (fail-closed)
	}
	return &Server{apex: apex, firstPartyApex: fpApex, firstPartySites: fpSites, firstPartyOrg: fpOrg, admin: s3admin.New(), log: log.New("subsystem", "sites")}
}

// Middleware is the host-router. Three outcomes, in order:
//
//  1. `<slug>.<org>.<apex>` (e.g. myapp.maxpower.hanzo.app) → serve that org's
//     site (terminal). The org is IN the hostname, so slug uniqueness is per-org.
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

// requestHost is the ONE way this server learns which host was asked for.
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
func (s *Server) requestHost(c *zip.Ctx) string {
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
		raw := s.requestHost(c)
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
// host (ResolveOrg over s.firstPartyOrg — never the unique-across-orgs fallback, so
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
	site, found, err := r.ResolveOrg(ctx, s.firstPartyOrg, slug)
	if err != nil {
		s.log.Error("resolve failed", "slug", slug, "org", s.firstPartyOrg, "err", err)
		return Site{}, false
	}
	if !found || site.Status != "live" {
		return Site{}, false
	}
	return site, true
}

// serveCustom serves a resolved custom-domain Site. It shares the object-serving
// core (streamSite) with the slug path; only the method + storage guards are
// re-applied here (the slug path applies them in serve).
func (s *Server) serveCustom(c *zip.Ctx, site Site) error {
	c.SetHeader("X-Hanzo-Site", site.Slug)
	if m := c.Method(); m != http.MethodGet && m != http.MethodHead {
		c.SetHeader("Allow", "GET, HEAD")
		return s.errorPage(c, http.StatusMethodNotAllowed, "method not allowed")
	}
	if !s.admin.Configured() {
		return s.errorPage(c, http.StatusServiceUnavailable, "storage not configured")
	}
	cli, err := s.admin.Client()
	if err != nil {
		return s.errorPage(c, http.StatusServiceUnavailable, "storage not configured")
	}
	return s.streamSite(c, cli, site)
}

// siteSlug extracts the org-scoped host KEY from a Host, or reports that this is
// not a site host. A site host is exactly `<slug>.<org>.<apex>`: two DNS labels
// under the apex, where <slug> is a non-reserved label matching slugRE and <org>
// is a label matching slugRE. Org-scoping is STRUCTURAL — the org lives in the
// hostname, so two orgs can each publish the SAME slug without collision. The
// returned key is `<slug>.<org>` (apex stripped): the exact string projects binds
// into site_hosts at publish (projects.onPublish), so the resolver's exact-host
// match finds it. The bare apex, a single-label host, a >2-label host, reserved
// or malformed labels are NOT sites — they fall through to the normal pipeline.
// siteSlug resolves a Host to a site slug. `firstParty` is true when the host is
// under the internal first-party apex — its resolution MUST be org-pinned (serve
// routes it through ResolveOrg, never the unique-across-orgs fallback).
func (s *Server) siteSlug(host string) (slug string, firstParty bool, ok bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // strip any :port
	}
	// First-party apex (internal, e.g. hanzo.ai) — OPT-IN sites. ONLY an explicitly
	// allow-listed label serves as a site; EVERY other `<label>.<firstPartyApex>`
	// (api, console, iam, kms, world, chat, …) returns false and falls through to the
	// normal /v1 + console pipeline, protected BY DEFAULT. There is no denylist to
	// keep complete — the allowlist IS the boundary — so a NEW internal host can never
	// be shadowed by a first-come project (the account-takeover in reserved.go).
	// Terminal for this apex: a non-allow-listed host never reaches the multi-tenant
	// branch below, and the label must still be a bare valid slug so a dotted key
	// (`x.y.hanzo.ai`) can't match a map entry.
	if s.firstPartyApex != "" {
		fpSuffix := "." + s.firstPartyApex
		if label, under := strings.CutSuffix(host, fpSuffix); under {
			if s.firstPartySites[label] && !strings.Contains(label, ".") && slugRE.MatchString(label) {
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
	// cert each match exactly one label, so a dotted key (`<slug>.<org>.<apex>` or
	// deeper) neither routes nor gets TLS — it is never a site, it falls through to
	// the normal pipeline. The slug must be non-reserved so a published site can
	// never shadow a real app/api host. The resolver then serves it iff it maps to
	// exactly one live project (explicit binding, else a unique live slug across
	// orgs) — an ambiguous bare host is an honest 404.
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
		site, found, err = r.ResolveOrg(c.Context(), s.firstPartyOrg, slug)
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
	if !s.admin.Configured() {
		return s.errorPage(c, http.StatusServiceUnavailable, "storage not configured")
	}
	cli, err := s.admin.Client()
	if err != nil {
		return s.errorPage(c, http.StatusServiceUnavailable, "storage not configured")
	}

	return s.streamSite(c, cli, site)
}

// streamSite streams the requested path from a resolved site's S3 prefix. It is
// the shared object-serving core for BOTH the `<slug>.hanzo.app` path (serve) and
// the custom-domain path (serveCustom). Tenant containment — every object key is
// rooted-clean under site.Prefix — lives in objectKey/resolveKey; this function
// only chooses the candidate keys, streams the first hit, and falls back to the
// site's own 404.
func (s *Server) streamSite(c *zip.Ctx, cli *s3.Client, site Site) error {
	// Emit the edge cache-tag on every served object so a tag-purge
	// (projects.purgeTag → Edge.PurgeTags) invalidates exactly this project's site
	// at the edge. Derived from server-owned Org+Slug — the SAME tag the purger
	// targets — never from the request, so emit and purge never diverge.
	c.SetHeader("Cache-Tag", CacheTag(site.Org, site.Slug))

	rel := resolveKey(c.Path())
	// Candidate keys, tried in order: the exact object, then a directory-index
	// fallback for extension-less paths (so /docs serves docs/index.html), then
	// the SPA/site root index for the bare root. All candidates are prefix-bounded
	// by objectKey (rooted-clean), so no candidate can escape the tenant prefix.
	candidates := s.candidates(rel)
	for _, key := range candidates {
		obj, info, ok := s.open(c.Context(), cli, site.Bucket, objectKey(site.Prefix, key))
		if !ok {
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
		return s.streamObject(c, site, obj, info.Size, ct, cc, http.StatusOK)
	}
	return s.notFound(c, cli, site)
}

// candidates returns the ordered object keys to try for a cleaned request path.
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
func (s *Server) candidates(rel string) []string {
	switch {
	case rel == "":
		return []string{"index.html"}
	case path.Ext(rel) == "":
		return []string{rel, rel + ".html", rel + "/index.html"}
	default:
		return []string{rel}
	}
}

// open fetches an object and returns it plus its info, or ok=false when the key
// is absent (or any read error — a site request never surfaces an S3 error to the
// client, it just misses). The caller must Close the returned object.
func (s *Server) open(ctx context.Context, cli *s3.Client, bucket, key string) (*s3.Object, s3.ObjectInfo, bool) {
	obj, err := cli.GetObject(ctx, bucket, key, s3.GetObjectOptions{})
	if err != nil {
		return nil, s3.ObjectInfo{}, false
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, s3.ObjectInfo{}, false
	}
	return obj, info, true
}

// streamObject streams an S3 object straight to the client — NO full-object
// buffering. This runs on the unauthenticated, gateway-bypassed edge, so buffering
// (even bounded) would let concurrent large-asset requests OOM the pod; a direct
// stream costs one small fasthttp copy buffer regardless of object size.
//
// Content-Length is set from info.Size (SendStream's size arg). fasthttp CLOSES the
// object after the body is written (it implements io.Closer) — so we must NOT Close
// it here (that would truncate the stream); the response writer owns the close.
func (s *Server) streamObject(c *zip.Ctx, site Site, obj *s3.Object, size int64, contentType, cacheControl string, status int) error {
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
	return c.Fiber().SendStream(obj, int(size))
}

// notFound serves the site's own 404.html when it published one, else a plain
// default 404. The 404.html is fetched from the SAME prefix (tenant-bounded) and
// streamed, not buffered.
func (s *Server) notFound(c *zip.Ctx, cli *s3.Client, site Site) error {
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
		c.SetHeader("Cache-Control", "no-cache")
		body, err := json.Marshal(map[string]string{"error": "not found", "path": c.Path()})
		if err != nil { // unreachable: two strings always marshal
			return s.errorPage(c, http.StatusNotFound, "page not found")
		}
		return c.Bytes(http.StatusNotFound, body)
	}
	obj, info, ok := s.open(c.Context(), cli, site.Bucket, objectKey(site.Prefix, "404.html"))
	if ok {
		return s.streamObject(c, site, obj, info.Size, "text/html; charset=utf-8", "no-cache", http.StatusNotFound)
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
	c.SetHeader("Cache-Control", "no-cache")
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

// contentType maps a key to a MIME type by extension. The standard library table
// misses the binary asset extensions game engines emit (Unity/Emscripten/Godot):
// .wasm may resolve to the wrong type (WebAssembly.instantiateStreaming REQUIRES
// application/wasm), and .data/.mem/.unityweb/.pck resolve to "" — leaving the
// Content-Type header empty and breaking the loader's streaming fetch.
// gameAssetType pins those; everything else uses the stdlib table.
func contentType(key string) string {
	if ct := gameAssetType(path.Ext(key)); ct != "" {
		return ct
	}
	return mime.TypeByExtension(path.Ext(key))
}

// gameAssetType returns the correct MIME for web-game engine binary assets the
// stdlib omits or mis-types, or "" to defer to the stdlib table. One place, one
// map — so a WebGL/WASM build served from a site loads with the types its loader
// requires. (.symbols.json / .json.gz resolve via their final .json/.gz extension
// and need no entry here.)
func gameAssetType(ext string) string {
	switch strings.ToLower(ext) {
	case ".wasm":
		return "application/wasm"
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
	switch path.Ext(key) {
	case ".html", ".htm":
		if strings.TrimSpace(htmlOverride) != "" {
			return htmlOverride
		}
		return "public, max-age=60, s-maxage=86400"
	case ".js", ".mjs", ".css", ".woff", ".woff2", ".png", ".jpg", ".jpeg",
		".gif", ".svg", ".webp", ".avif", ".ico", ".ttf", ".otf", ".wasm",
		// Game-engine payloads. gameAssetType already teaches this file that a
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
		if isFingerprinted(key) {
			return "public, max-age=31536000, immutable"
		}
		return "public, max-age=3600"
	default:
		return "public, max-age=3600"
	}
}

// buildOutputPrefixes are the directories a bundler writes its CONTENT-ADDRESSED
// output to. Everything under one is immutable by construction — the bundler
// renames the file whenever its bytes change — so the PATH answers the question
// and the basename does not have to.
//
// This is the primary signal, and it has to be, because the basename test below
// gets Next.js wrong. Next emits BARE-hash names (`_next/static/css/
// bdec3a94ead6ad5f.css`, `_next/static/chunks/1a258343.a953edc46b595a62.js`) with
// no separator before the hash run, which fingerprintRE requires — so 5 of the 44
// `_next/static` objects of the console bundle were served
// `public, max-age=3600` and re-fetched hourly forever. The embedded console
// handler always keyed off the prefix (webui: `assets/` or `_next/`) and was
// right; this is the site edge learning the same rule.
var buildOutputPrefixes = []string{"_next/static/", "assets/"}

// fingerprintRE matches a content-hash segment in a filename (Vite/webpack emit
// e.g. `app.4f3a9c21.js` or `chunk-AB12CD34.css`). Such names are immutable: a new
// build changes the hash, so the old URL is safe to cache forever. It is kept as
// an ADDITIONAL signal, for the bundlers that fingerprint outside the two
// directories above.
var fingerprintRE = regexp.MustCompile(`[.\-_][0-9a-fA-F]{8,}\.[a-z0-9]+$`)

func isFingerprinted(key string) bool {
	k := strings.TrimPrefix(key, "/")
	for _, p := range buildOutputPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return fingerprintRE.MatchString(path.Base(key))
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
