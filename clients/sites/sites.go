// Package sites is the public site-server for published projects: the
// host-routed edge that turns `<slug>.hanzo.app` into the static site a user
// deployed to OUR S3.
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
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"

	minio "github.com/hanzoai/s3-go"
	"github.com/zap-proto/zip"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/clients/s3admin"
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
}

// Resolver maps a validated subdomain slug to its authoritative Site. It is the
// projects store (the ONE source of project truth). found=false ⇒ no such
// published subdomain (honest 404); err ⇒ a real store failure (honest 500).
type Resolver interface {
	Resolve(ctx context.Context, slug string) (Site, bool, error)
}

var (
	resolverMu sync.RWMutex
	resolver   Resolver
)

// SetResolver installs the slug→Site resolver. projects.Mount calls this once
// with its store. Until it is set, every site request is an honest 404 (the
// projects subsystem is not mounted), never a crash.
func SetResolver(r Resolver) {
	resolverMu.Lock()
	resolver = r
	resolverMu.Unlock()
}

func currentResolver() Resolver {
	resolverMu.RLock()
	r := resolver
	resolverMu.RUnlock()
	return r
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
}

// Server is the host-routed public site edge. It holds the S3 access path
// (s3admin, the SAME credentials as the deploy blob store) plus the apex. The
// reserved-label policy is the package-level shared source (reserved.go), so
// serve/create/bind never disagree. It reads the resolver at request time.
type Server struct {
	apex        string
	selfDomains []string // OUR own registrable domains — never custom-domain candidates
	admin       s3admin.Admin
	log         luxlog.Logger
}

// New builds the Server from Config. An empty apex defaults to hanzo.app.
// Operator-supplied Reserved labels are registered into the shared reserved source
// (ADDING to the baked-in defaults, never removing them), so createProject and
// BindHost enforce the exact same set the serve gate does.
func New(cfg Config, log luxlog.Logger) *Server {
	apex := strings.ToLower(strings.TrimSpace(cfg.Apex))
	if apex == "" {
		apex = "hanzo.app"
	}
	SetReservedExtra(cfg.Reserved)
	// The apex is always self; add the operator-supplied self domains (deduped,
	// normalized). This is the exclusion set the custom-domain branch consults.
	self := []string{apex}
	seen := map[string]bool{apex: true}
	for _, d := range cfg.SelfDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		self = append(self, d)
	}
	return &Server{apex: apex, selfDomains: self, admin: s3admin.New(), log: log.New("subsystem", "sites")}
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
// leaves a site host serving only its static files; base.Mount installs it,
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

// analyticsHostHandler ingests a published site's OWN analytics beacon (the
// anonymous POST a page emits on unload) on the site host — host-as-project-ref
// (HIP-0014), the exact twin of baseHostHandler. Nil (the default) leaves a site
// host 405-ing a beacon POST; analytics.Mount installs it. The org comes ONLY from
// the resolved Site (the subdomain / bound custom host), never the caller — the
// SAME server-supplied tenant key the file plane and the base carve trust, so a
// page for yadota.hanzo.app always ingests as that site's Org regardless of any
// body/header claim. The authenticated GET read lenses on api.hanzo.ai
// (/v1/analytics/overview|timeseries|top) are untouched: this carve is POST-only
// and never runs on an API host.
var analyticsHostHandler func(org string, c *zip.Ctx) error

// SetAnalyticsHostHandler installs the per-org analytics ingest handler (see
// analyticsHostHandler).
func SetAnalyticsHostHandler(h func(org string, c *zip.Ctx) error) { analyticsHostHandler = h }

// isAnalyticsPath reports whether a path targets the anonymous analytics-beacon
// ingest: the Segment/beacon wire at /v1/analytics{,/batch} and the PostHog wire
// at /v1/insights/e. The Middleware carve additionally gates on POST, so the
// authenticated GET read lenses (/v1/analytics/overview|timeseries|top) — which
// live on api.hanzo.ai, never a site host — are never hijacked.
func isAnalyticsPath(p string) bool {
	return strings.HasPrefix(p, "/v1/analytics") || p == "/v1/insights/e"
}

func (s *Server) Middleware() zip.Handler {
	return func(c *zip.Ctx) error {
		raw := c.Fiber().Hostname()
		if slug, ok := s.siteSlug(raw); ok {
			if baseHostHandler != nil && isBasePath(c.Path()) {
				if site, ok := s.resolveLive(c.Context(), slug); ok {
					return baseHostHandler(site.Org, c)
				}
			}
			if analyticsHostHandler != nil && c.Method() == http.MethodPost && isAnalyticsPath(c.Path()) {
				if site, ok := s.resolveLive(c.Context(), slug); ok {
					return analyticsHostHandler(site.Org, c)
				}
			}
			return s.serve(c, slug)
		}
		if host := hostOnly(raw); s.customCandidate(host) {
			if site, ok := s.resolveLive(c.Context(), host); ok {
				if baseHostHandler != nil && isBasePath(c.Path()) {
					return baseHostHandler(site.Org, c)
				}
				if analyticsHostHandler != nil && c.Method() == http.MethodPost && isAnalyticsPath(c.Path()) {
					return analyticsHostHandler(site.Org, c)
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
	if host == "" || !strings.Contains(host, ".") {
		return false
	}
	for _, d := range s.selfDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return false
		}
	}
	return true
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
func (s *Server) siteSlug(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // strip any :port
	}
	suffix := "." + s.apex
	if !strings.HasSuffix(host, suffix) {
		return "", false
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
		return "", false
	}
	return label, true
}

// serve resolves the slug to its Site and streams the requested object from the
// project's S3 prefix. Every failure renders an honest page; nothing is ever read
// outside `<Bucket>/<Prefix>/`.
func (s *Server) serve(c *zip.Ctx, slug string) error {
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
	site, found, err := r.Resolve(c.Context(), slug)
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
func (s *Server) streamSite(c *zip.Ctx, cli *minio.Client, site Site) error {
	// Emit the edge cache-tag on every served object so a tag-purge
	// (projects.purgeTag → Purger.PurgeTags) invalidates exactly this project's site
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
		// The per-project cache override lives on the stored object's Cache-Control
		// (set at deploy); honor it so the site-server path and the direct-S3 path
		// return the same TTL. Fall back to the computed policy when the object
		// carries none.
		cc := info.Metadata.Get("Cache-Control")
		if cc == "" {
			cc = CacheControlFor(key, "")
		}
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
// its index.html child.
//   - ""            → index.html                    (site root)
//   - "assets/a.js" → assets/a.js                    (a file with an extension)
//   - "docs"        → docs, then docs/index.html     (file, else directory index)
func (s *Server) candidates(rel string) []string {
	switch {
	case rel == "":
		return []string{"index.html"}
	case path.Ext(rel) == "":
		return []string{rel, rel + "/index.html"}
	default:
		return []string{rel}
	}
}

// open fetches an object and returns it plus its info, or ok=false when the key
// is absent (or any read error — a site request never surfaces an S3 error to the
// client, it just misses). The caller must Close the returned object.
func (s *Server) open(ctx context.Context, cli *minio.Client, bucket, key string) (*minio.Object, minio.ObjectInfo, bool) {
	obj, err := cli.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, minio.ObjectInfo{}, false
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, minio.ObjectInfo{}, false
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
func (s *Server) streamObject(c *zip.Ctx, site Site, obj *minio.Object, size int64, contentType, cacheControl string, status int) error {
	c.SetHeader("Content-Type", contentType)
	if cacheControl != "" {
		c.SetHeader("Cache-Control", cacheControl)
	}
	// Opt-in cross-origin isolation, decided by object role from the content type
	// just set (documents vs subresources). No-op unless the site enabled it.
	for _, h := range crossOriginIsolation(site.CrossOriginIsolation, contentType) {
		c.SetHeader(h[0], h[1])
	}
	c.Status(status)
	return c.Fiber().SendStream(obj, int(size))
}

// notFound serves the site's own 404.html when it published one, else a plain
// default 404. The 404.html is fetched from the SAME prefix (tenant-bounded) and
// streamed, not buffered.
func (s *Server) notFound(c *zip.Ctx, cli *minio.Client, site Site) error {
	obj, info, ok := s.open(c.Context(), cli, site.Bucket, objectKey(site.Prefix, "404.html"))
	if ok {
		return s.streamObject(c, site, obj, info.Size, "text/html; charset=utf-8", "no-cache", http.StatusNotFound)
	}
	return s.errorPage(c, http.StatusNotFound, "page not found")
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
// This is the traversal boundary: it roots the path at "/", runs path.Clean
// (which resolves every "." and ".." segment against that root), then strips the
// leading "/". The result provably contains no ".." segment, so joining it under
// a fixed prefix can never escape that prefix — regardless of how many "..",
// backslashes, or percent-encoded dots the client sends (fasthttp has already
// percent-decoded + normalized c.Path(); this re-clean is the defensive guarantee
// that does not depend on that normalization).
func resolveKey(reqPath string) string {
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
		".gif", ".svg", ".webp", ".avif", ".ico", ".ttf", ".otf", ".wasm":
		if isFingerprinted(key) {
			return "public, max-age=31536000, immutable"
		}
		return "public, max-age=3600"
	default:
		return "public, max-age=3600"
	}
}

// fingerprintRE matches a content-hash segment in a filename (Vite/Next/webpack
// emit e.g. `app.4f3a9c21.js` or `chunk-AB12CD34.css`). Such names are immutable:
// a new build changes the hash, so the old URL is safe to cache forever.
var fingerprintRE = regexp.MustCompile(`[.\-_][0-9a-fA-F]{8,}\.[a-z0-9]+$`)

func isFingerprinted(key string) bool { return fingerprintRE.MatchString(path.Base(key)) }

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
