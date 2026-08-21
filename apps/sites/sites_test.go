package sites

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeResolver records the slugs it is asked to resolve so a test can assert that
// the tenant is ALWAYS keyed by the validated host slug and never by the path.
type fakeResolver struct {
	mu       sync.Mutex
	calls    []string
	orgCalls []string
	site     Site
	found    bool
	err      error
}

func (f *fakeResolver) Resolve(_ context.Context, slug string) (Site, bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slug)
	f.mu.Unlock()
	return f.site, f.found, f.err
}

func (f *fakeResolver) ResolveOrg(_ context.Context, org, slug string) (Site, bool, error) {
	f.mu.Lock()
	f.orgCalls = append(f.orgCalls, org+"/"+slug)
	f.mu.Unlock()
	return f.site, f.found, f.err
}

func (f *fakeResolver) slugs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func testServer() *Server {
	return New(Config{Apex: "hanzo.app", Reserved: []string{"app", "api", "admin"}}, luxlog.New("test"))
}

// ---- resolveKey / objectKey: the traversal boundary (RED focus) ----------

// TestResolveKeyNeverEscapesPrefix is the core tenant-isolation proof. For a
// battery of hostile paths, the object key that would be fetched MUST stay under
// the project's own `<org>/<slug>/` prefix — never another org's, never another
// project's, never absolute, never a parent. If any input escaped, a
// `<slug>.hanzo.app` request could read another tenant's S3 objects.
func TestResolveKeyNeverEscapesPrefix(t *testing.T) {
	const prefix = "orgA/site1" // this project's hard-bounded prefix
	hostile := []string{
		"/",
		"/index.html",
		"/../../../etc/passwd",
		"/../orgB/site9/secret.env",
		"/..%2f..%2forgB%2fsecret", // (already-decoded form fasthttp would pass)
		"/....//....//orgB",
		"/a/b/../../../../orgB/x",
		"//orgB/x",
		"/./././../orgB",
		"/assets/../../orgB/site9/index.html",
		"/%2e%2e/%2e%2e/orgB",
		`/..\..\orgB\x`,
		"/foo/..",
		"/..",
		"/.",
		"/legit/deep/path/app.css",
		strings.Repeat("/..", 50) + "/orgB/secret",
	}
	for _, in := range hostile {
		rel := resolveKey(in)
		if strings.HasPrefix(rel, "/") {
			t.Fatalf("resolveKey(%q) = %q is absolute", in, rel)
		}
		// No ".." path SEGMENT survives: resolveKey decodes BEFORE it cleans, so an
		// encoded traversal is collapsed as the traversal it is rather than kept as
		// an opaque literal. Either way the decisive guard is prefix containment.
		if strings.Contains("/"+rel+"/", "/../") {
			t.Fatalf("resolveKey(%q) = %q has a .. segment", in, rel)
		}
		key := objectKey(prefix, rel)
		if !strings.HasPrefix(key, prefix+"/") {
			t.Fatalf("objectKey(%q, resolveKey(%q)=%q) = %q escaped prefix", prefix, in, rel, key)
		}
		// The decisive assertion: no hostile input can reach orgB's namespace — every
		// produced key is physically under this project's own prefix.
		if !strings.HasPrefix(key, "orgA/site1/") {
			t.Fatalf("input %q produced out-of-tenant key %q", in, key)
		}
	}
}

func TestResolveKeyFallbacks(t *testing.T) {
	cases := map[string]string{
		"/":           "",
		"":            "",
		"/index.html": "index.html",
		"/docs/":      "docs", // path.Clean strips the trailing slash
		"/docs":       "docs",
		"/a/b/c.js":   "a/b/c.js",
		"/./a":        "a",
		"/a/./b/":     "a/b",
		`/a\b`:        "a/b",
		// A browser percent-encodes what it must, so the key stored at deploy is
		// only ever found if the request is decoded first. Next.js names a dynamic
		// route's chunk after the literal segment, so EVERY such page asked for
		// %5Bslug%5D, got a 404, and never hydrated.
		"/_next/static/chunks/app/blog/%5Bslug%5D/page-abc.js": "_next/static/chunks/app/blog/[slug]/page-abc.js",
		"/a%20b/c.css": "a b/c.css",
		"/a%2Bb.js":    "a+b.js",
		// Not valid percent-encoding: kept verbatim rather than dropped.
		"/100%off.html": "100%off.html",
	}
	for in, want := range cases {
		if got := resolveKey(in); got != want {
			t.Errorf("resolveKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCandidates(t *testing.T) {
	s := testServer()
	eq := func(in string, want ...string) {
		got := s.candidates(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("candidates(%q) = %v, want %v", in, got, want)
		}
	}
	eq("", "index.html")
	// The flat `.html` spelling is what Next's `output: export` writes without
	// `trailingSlash`, and omitting it made every route but the homepage 404 on a
	// Next site. The directory-index form stays for Hugo/Jekyll/trailingSlash
	// exports — both conventions are legitimate, so both are tried.
	eq("docs", "docs", "docs.html", "docs/index.html")
	eq("zen/models", "zen/models", "zen/models.html", "zen/models/index.html")
	eq("assets/app.js", "assets/app.js")
}

// ---- siteSlug: host routing + reserved exclusions -----------------------

func TestSiteSlug(t *testing.T) {
	s := testServer()
	site := func(host, wantSlug string) {
		slug, _, ok := s.siteSlug(host)
		if !ok || slug != wantSlug {
			t.Errorf("siteSlug(%q) = (%q,%v), want (%q,true)", host, slug, ok, wantSlug)
		}
	}
	notSite := func(host string) {
		if slug, _, ok := s.siteSlug(host); ok {
			t.Errorf("siteSlug(%q) = (%q,true), want not-a-site", host, slug)
		}
	}
	// The ONE servable shape: bare `<slug>.<apex>` — the product URL every surface
	// advertises (and the only host the one-label ingress wildcard + LE cert can
	// route/secure). Key is the bare slug; the resolver serves it iff it maps to
	// exactly one live project.
	site("dave-synapse-demo.hanzo.app", "dave-synapse-demo")
	site("Brew.Hanzo.App", "brew")                 // case-insensitive
	site("vibe-check.hanzo.app:443", "vibe-check") // port stripped
	site("my-cool-site.hanzo.app", "my-cool-site")

	notSite("hanzo.app")           // apex, no label
	notSite("www.hanzo.app")       // reserved bare label
	notSite("api.hanzo.app")       // reserved bare label
	notSite("app.hanzo.app")       // reserved (real app host)
	notSite("-bad.hanzo.app")      // invalid bare slug
	notSite("UPPER_bad.hanzo.app") // underscore invalid
	// A dotted key is NOT a servable host (wildcard cert/ingress match one label),
	// so `<slug>.<org>.<apex>` and deeper fall through to the normal pipeline.
	notSite("myapp.maxpower.hanzo.app")
	notSite("my-cool-site.acme.hanzo.app")
	notSite("a.b.c.hanzo.app")   // >2 labels
	notSite("api.hanzo.ai")      // different zone → normal pipeline
	notSite("console.hanzo.ai")  // different zone
	notSite("../orgb.hanzo.app") // traversal-shaped label rejected
	notSite("myapp.evil.hanzo.app.evil.com")
}

// ---- siteSlug: first-party apex (hanzo.ai) — OPT-IN allowlist boundary ----

// The brand apex (hanzo.ai) serves ONLY our explicit internal sites; every other
// host — including internal hosts reserved.go never listed — falls through
// PROTECTED. This opt-in allowlist is the security boundary that replaces the
// denylist-completeness burden: on the brand's own domain a missing reserved label
// must NOT become a publishable site (the OAuth account-takeover in reserved.go).
func TestSiteSlugFirstParty(t *testing.T) {
	s := New(Config{
		Apex:            "hanzo.app",
		Reserved:        []string{"app", "api", "admin"},
		SelfDomains:     []string{"hanzo.ai"},
		FirstPartyApex:  "hanzo.ai",
		FirstPartySites: []string{"cd", "flow", "gallery"},
		FirstPartyOrg:   "hanzo",
	}, luxlog.New("test"))
	site := func(host, want string) {
		if slug, _, ok := s.siteSlug(host); !ok || slug != want {
			t.Errorf("siteSlug(%q) = (%q,%v), want (%q,true)", host, slug, ok, want)
		}
	}
	notSite := func(host string) {
		if slug, _, ok := s.siteSlug(host); ok {
			t.Errorf("siteSlug(%q) = (%q,true), want not-a-site (protected)", host, slug)
		}
	}
	// Allow-listed internal sites serve on the brand apex.
	site("cd.hanzo.ai", "cd")
	site("flow.hanzo.ai", "flow")
	site("Gallery.Hanzo.AI", "gallery") // case-insensitive
	site("cd.hanzo.ai:443", "cd")       // port stripped
	// THE BOUNDARY: every non-allow-listed brand-apex host is protected — it falls
	// through to the normal /v1 + console pipeline, NEVER served as a site. This holds
	// for real internal hosts reserved.go may never have listed (iam/kms/world/chat),
	// so a first-come project can never shadow one.
	notSite("api.hanzo.ai")
	notSite("console.hanzo.ai")
	notSite("iam.hanzo.ai") // not in baseReserved — protected anyway (opt-in default)
	notSite("kms.hanzo.ai")
	notSite("world.hanzo.ai")
	notSite("chat.hanzo.ai")
	notSite("models.hanzo.ai")
	notSite("anything-unlisted.hanzo.ai")
	notSite("cd.acme.hanzo.ai") // a dotted key can never match a bare allowlist entry
	notSite("hanzo.ai")         // bare apex, no label
	// The multi-tenant apex is unaffected: users' sites still resolve on hanzo.app.
	site("my-cool-site.hanzo.app", "my-cool-site")
	notSite("api.hanzo.app") // still reserved on the multi-tenant apex
}

// ---- middleware: passthrough vs terminal + resolver keying --------------

func newTestApp(s *Server) *zip.App {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(s.Middleware())
	// Sentinel terminal handler: reached ONLY when the middleware passed through
	// (i.e. the request was NOT a site host). A site host is terminal in the
	// middleware and must never reach here.
	app.All("/*", func(c *zip.Ctx) error {
		c.SetHeader("X-Sentinel", "hit")
		return c.String(200, "sentinel")
	})
	return app
}

func TestMiddlewarePassthroughForNonSiteHosts(t *testing.T) {
	SetResolver(&fakeResolver{})
	defer SetResolver(nil)
	app := newTestApp(testServer())
	for _, host := range []string{"api.hanzo.ai", "console.hanzo.ai", "hanzo.app", "www.hanzo.app"} {
		req := httptest.NewRequest("GET", "http://"+host+"/anything", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("test %s: %v", host, err)
		}
		if resp.Header.Get("X-Sentinel") != "hit" {
			t.Errorf("host %q did not pass through to the normal pipeline", host)
		}
	}
}

// TestMiddlewareTenantKeyedByHostNotPath is the second half of the isolation
// proof: even when the path screams "give me another org", the resolver is only
// ever asked about the HOST slug. The org/prefix therefore cannot be influenced
// by the path or any client header.
func TestMiddlewareTenantKeyedByHostNotPath(t *testing.T) {
	fr := &fakeResolver{found: false} // not found → honest 404, no S3 needed
	SetResolver(fr)
	defer SetResolver(nil)
	app := newTestApp(testServer())

	req := httptest.NewRequest("GET", "http://victim.hanzo.app/index.html", nil)
	// Attacker-controlled headers that must be ignored by the site server.
	req.Header.Set("X-Org-Id", "attacker-org")
	req.Header.Set("X-Forwarded-Host", "otherorg.evil.hanzo.app")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.Header.Get("X-Sentinel") == "hit" {
		t.Fatal("site host leaked into the normal API pipeline")
	}
	if resp.Header.Get("X-Hanzo-Site") != "victim" {
		t.Errorf("X-Hanzo-Site = %q, want victim", resp.Header.Get("X-Hanzo-Site"))
	}
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404 (not found)", resp.StatusCode)
	}
	got := fr.slugs()
	if len(got) != 1 || got[0] != "victim" {
		t.Fatalf("resolver called with %v, want exactly [victim] — tenant must be keyed by host only", got)
	}
}

func TestMiddlewareNoResolverIs404(t *testing.T) {
	SetResolver(nil)
	app := newTestApp(testServer())
	req := httptest.NewRequest("GET", "http://mysite.hanzo.app/", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.StatusCode != 404 || resp.Header.Get("X-Sentinel") == "hit" {
		t.Errorf("no-resolver site host: status=%d sentinel=%q, want 404 terminal",
			resp.StatusCode, resp.Header.Get("X-Sentinel"))
	}
}

// ---- cache policy -------------------------------------------------------

func TestCacheControlFor(t *testing.T) {
	cases := map[string]string{
		"index.html":                "public, max-age=60, s-maxage=86400",
		"about/index.html":          "public, max-age=60, s-maxage=86400",
		"assets/app.4f3a9c21.js":    "public, max-age=31536000, immutable",
		"assets/style.a1b2c3d4.css": "public, max-age=31536000, immutable",
		"logo.svg":                  "public, max-age=3600", // not fingerprinted
		"app.js":                    "public, max-age=3600", // not fingerprinted
		"data.json":                 "public, max-age=3600", // default class
		"favicon.ico":               "public, max-age=3600",
		// engine payloads: fingerprinted ones are immutable like any other asset
		"Build/game-0881644a.data": "public, max-age=31536000, immutable",
		"orb-dd8b3278.pck":         "public, max-age=31536000, immutable",
		"Build/game.data":          "public, max-age=3600", // not fingerprinted
		// film, the same rule again. A marketing export is mostly video by
		// weight — hanzo.ai ships ~105 product films — and the unfingerprinted
		// case already answered 3600 through `default`, so what this pins is the
		// FINGERPRINTED one: before video was a media class it could not reach
		// `immutable` at all, which is the asset that gains the most from a year.
		"mock/containers-a1b2c3d4.mp4": "public, max-age=31536000, immutable",
		"hero-9f8e7d6c.webm":           "public, max-age=31536000, immutable",
		"workload/containers.mp4":      "public, max-age=3600", // not fingerprinted
	}
	for key, want := range cases {
		if got := CacheControlFor(key, ""); got != want {
			t.Errorf("CacheControlFor(%q) = %q, want %q", key, got, want)
		}
	}
	// The per-project HTML override applies to documents only, never to immutable assets.
	if got := CacheControlFor("index.html", "public, max-age=10"); got != "public, max-age=10" {
		t.Errorf("html override = %q, want public, max-age=10", got)
	}
	if got := CacheControlFor("app.4f3a9c21.js", "public, max-age=10"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset must ignore html override, got %q", got)
	}
}

func TestIsFingerprinted(t *testing.T) {
	yes := []string{"app.4f3a9c21.js", "chunk-AB12CD34.css", "vendor_0a1b2c3d.js", "x.deadbeef12345678.woff2"}
	no := []string{"app.js", "style.css", "index.html", "logo.svg", "v2.css"}
	for _, k := range yes {
		if !isFingerprinted(k) {
			t.Errorf("isFingerprinted(%q) = false, want true", k)
		}
	}
	for _, k := range no {
		if isFingerprinted(k) {
			t.Errorf("isFingerprinted(%q) = true, want false", k)
		}
	}
}

// TestBuildOutputIsImmutableWhateverTheBasename is a measured regression, with
// the real filenames.
//
// The basename rule requires a SEPARATOR before the hash run
// ([.\-_][0-9a-fA-F]{8,}\.ext). Next.js does not emit one: its CSS and its
// single-hash chunks are BARE hashes, so 5 of the 44 `_next/static` objects of
// the console bundle were served `public, max-age=3600` at the origin and
// re-fetched every hour, forever, for files whose whole design is that they can
// never change. The embedded console handler never had this bug because it keyed
// off the PATH (webui: `assets/` or `_next/`); the path is the right key, because
// what makes those files immutable is the bundler's naming contract for that
// DIRECTORY, not the shape of any one name.
func TestBuildOutputIsImmutableWhateverTheBasename(t *testing.T) {
	const immutable = "public, max-age=31536000, immutable"
	for _, key := range []string{
		// Bare-hash basenames — what Next.js actually writes, and what the
		// basename rule misses.
		"_next/static/css/bdec3a94ead6ad5f.css",
		"_next/static/css/14a0d01599b56097.css",
		// Separator forms from the same tree, which matched before and must still.
		"_next/static/chunks/113-5543f1ec0e9ff9a3.js",
		"_next/static/chunks/1a258343.a953edc46b595a62.js",
		// The build id directory: a name with no hash shape at all, immutable
		// because of WHERE it is.
		"_next/static/O-3AmMBn6NpuJXn8kFZKF/_buildManifest.js",
		// Vite's directory, same rule.
		"assets/index.js",
	} {
		if got := CacheControlFor(key, ""); got != immutable {
			t.Errorf("CacheControlFor(%q) = %q, want %q — build output is immutable by construction", key, got, immutable)
		}
	}

	// The NEGATIVE set carries as much weight as the positive one, and it is why
	// the prefixes are `_next/static/` and not the broader `_next/`.
	//
	// `_next/` holds more than build output: a real export also carries
	// `_next/data/` and a build-id directory beside static. This function serves
	// ARBITRARY TENANT SITES, so a prefix wider than the bundler's own
	// content-addressed directory would hand a year of immutability to files that
	// are not content-addressed — and the worst of those is a service worker, which
	// would then outlive every deploy of that site with no way to reach the
	// browsers holding it. (The console handler in webui keys on the broader
	// `_next/`, which is correct THERE: it serves one known bundle that has no
	// `_next/data`. That width must not be copied here.)
	for _, key := range []string{
		"sw.js",                    // a stale service worker outlives every deploy
		"service-worker.js",        //
		"config.json",              // runtime config, re-read on purpose
		"_next/data/latest.json",   // not build output, despite the _next/ prefix
		"_next/7e4e0526/page.json", // the build-id dir beside static — also not
		"favicon.ico", "icon.svg",  // root icons a real export ships
		"apple-icon.png", "index.txt",
	} {
		if got := CacheControlFor(key, ""); got == immutable {
			t.Errorf("CacheControlFor(%q) = %q — only build output may be cached forever", key, got)
		}
	}

	// HTML policy is untouched: a document is a document, wherever it sits.
	for _, key := range []string{"index.html", "404.html", "docs/index.html"} {
		if got := CacheControlFor(key, ""); got != "public, max-age=60, s-maxage=86400" {
			t.Errorf("CacheControlFor(%q) = %q — the document policy must not move", key, got)
		}
	}
}

func TestCacheTag(t *testing.T) {
	if got := CacheTag("acme", "blog"); got != "site-acme-blog" {
		t.Errorf("CacheTag = %q, want site-acme-blog", got)
	}
}

// ---- reserved policy (the ONE shared source) ----------------------------

func TestIsReserved(t *testing.T) {
	SetReservedExtra([]string{"custom1", "custom2"})
	defer SetReservedExtra(nil)
	// Baked-in (can't be removed by config) + operator extras, case-insensitive.
	for _, l := range []string{"", "www", "api", "admin", "login", "secure", "wallet",
		"account", "signin", "gateway", "cdn", "static", "assets", "hanzo", "lux", "zoo",
		"API", "Admin", "custom1", "custom2"} {
		if !IsReserved(l) {
			t.Errorf("IsReserved(%q) = false, want true", l)
		}
	}
	for _, l := range []string{"maxpower", "my-site", "cool-thing", "blog2", "acme-app"} {
		if IsReserved(l) {
			t.Errorf("IsReserved(%q) = true, want false", l)
		}
	}
}

// TestReservedHostNeverServes is the serve-time backstop: even with a resolver that
// WOULD serve any slug as a live site, a reserved host is passed through to the
// normal pipeline (sentinel) — it is never served as a site. Combined with the
// create+bind rejects, a reserved subdomain can never shadow a real app/api host.
func TestReservedHostNeverServes(t *testing.T) {
	SetResolver(&fakeResolver{found: true, site: Site{Org: "x", Slug: "api", Bucket: "b", Prefix: "x/api", Status: "live"}})
	defer SetResolver(nil)
	app := newTestApp(testServer())
	for _, host := range []string{"api.hanzo.app", "admin.hanzo.app", "login.hanzo.app", "wallet.hanzo.app", "www.hanzo.app"} {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("test %s: %v", host, err)
		}
		if resp.Header.Get("X-Sentinel") != "hit" {
			t.Errorf("reserved host %q was served as a site instead of passthrough", host)
		}
	}
}

// TestSiteRejectsNonGet: a site host answers only GET/HEAD; anything else is 405
// with an Allow header (and never reaches resolution).
func TestSiteRejectsNonGet(t *testing.T) {
	SetResolver(&fakeResolver{found: false})
	defer SetResolver(nil)
	app := newTestApp(testServer())
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		req := httptest.NewRequest(m, "http://mysite.hanzo.app/", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("test %s: %v", m, err)
		}
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s → %d, want 405", m, resp.StatusCode)
		}
		if resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s Allow=%q, want 'GET, HEAD'", m, resp.Header.Get("Allow"))
		}
		if resp.Header.Get("X-Sentinel") == "hit" {
			t.Errorf("%s leaked into the API pipeline", m)
		}
	}
}

func TestGameAssetContentType(t *testing.T) {
	// The critical WebGL cases the stdlib mime table gets wrong or omits.
	cases := map[string]string{
		"Build/game.wasm":      "application/wasm",         // instantiateStreaming requires this exactly
		"Build/game.data":      "application/octet-stream", // Unity payload — stdlib returns ""
		"Build/game.mem":       "application/octet-stream", // Emscripten memory init
		"Build/build.unityweb": "application/octet-stream", // Unity compressed
		"game.pck":             "application/octet-stream", // Godot pack
	}
	for key, want := range cases {
		if got := contentType(key); got != want {
			t.Errorf("contentType(%q) = %q, want %q", key, got, want)
		}
	}
	// Non-game assets still defer to the stdlib table (non-empty, sane).
	if got := contentType("app.css"); got == "" || !strings.Contains(got, "css") {
		t.Errorf("contentType(app.css) = %q, want a css type", got)
	}
	if got := contentType("index.html"); got == "" || !strings.Contains(got, "html") {
		t.Errorf("contentType(index.html) = %q, want an html type", got)
	}
}

// TestCrossOriginIsolation pins the opt-in header policy: OFF ⇒ no isolation
// headers on anything; ON ⇒ the document (text/html) carries the COOP+COEP pair a
// browser requires to grant crossOriginIsolated (and NO CORP), while every asset
// carries CORP:same-origin (and NO COOP/COEP). This is what lets a multithreaded
// WebGL/WASM build use SharedArrayBuffer without the isolation ever leaking to a
// site that did not opt in.
func TestCrossOriginIsolation(t *testing.T) {
	// Disabled: never any isolation header, whatever the content type.
	for _, ct := range []string{"text/html; charset=utf-8", "application/wasm", "text/css; charset=utf-8"} {
		if h := crossOriginIsolation(false, ct); h != nil {
			t.Errorf("crossOriginIsolation(false, %q) = %v, want nil", ct, h)
		}
	}

	// Enabled + document: COOP + COEP, and NEVER CORP.
	doc := headerMap(crossOriginIsolation(true, "text/html; charset=utf-8"))
	if doc["Cross-Origin-Opener-Policy"] != "same-origin" {
		t.Errorf("document COOP = %q, want same-origin", doc["Cross-Origin-Opener-Policy"])
	}
	if doc["Cross-Origin-Embedder-Policy"] != "require-corp" {
		t.Errorf("document COEP = %q, want require-corp", doc["Cross-Origin-Embedder-Policy"])
	}
	if _, ok := doc["Cross-Origin-Resource-Policy"]; ok {
		t.Errorf("document must not carry CORP, got %v", doc)
	}

	// Enabled + asset (wasm/js/data/css): CORP:same-origin, and NEVER COOP/COEP.
	for _, ct := range []string{"application/wasm", "application/octet-stream", "text/javascript; charset=utf-8", "text/css; charset=utf-8"} {
		a := headerMap(crossOriginIsolation(true, ct))
		if a["Cross-Origin-Resource-Policy"] != "same-origin" {
			t.Errorf("asset %q CORP = %q, want same-origin", ct, a["Cross-Origin-Resource-Policy"])
		}
		if _, ok := a["Cross-Origin-Opener-Policy"]; ok {
			t.Errorf("asset %q must not carry COOP, got %v", ct, a)
		}
		if _, ok := a["Cross-Origin-Embedder-Policy"]; ok {
			t.Errorf("asset %q must not carry COEP, got %v", ct, a)
		}
	}
}

func headerMap(pairs [][2]string) map[string]string {
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		m[p[0]] = p[1]
	}
	return m
}

// TestFirstPartyOrgPinned is the ship-blocker proof (RED #4): a first-party host
// resolves through ResolveOrg PINNED to the owning org, NEVER the unique-across-orgs
// Resolve — so a customer who names a project "cd" can never be served on cd.hanzo.ai.
func TestFirstPartyOrgPinned(t *testing.T) {
	s := New(Config{
		Apex: "hanzo.app", SelfDomains: []string{"hanzo.ai"},
		FirstPartyApex: "hanzo.ai", FirstPartySites: []string{"cd"}, FirstPartyOrg: "hanzo",
	}, luxlog.New("test"))
	fr := &fakeResolver{site: Site{Org: "hanzo", Slug: "cd", Status: "live", Bucket: "b", Prefix: "hanzo/cd"}, found: true}
	SetResolver(fr)
	defer SetResolver(nil)

	// First-party host → org-pinned resolve, and NOT the unique-slug path.
	slug, fp, ok := s.siteSlug("cd.hanzo.ai")
	if !ok || !fp || slug != "cd" {
		t.Fatalf("siteSlug(cd.hanzo.ai) = (%q,%v,%v), want (cd,true,true)", slug, fp, ok)
	}
	if _, ok := s.resolveLivePinned(context.Background(), slug, fp); !ok {
		t.Fatal("first-party resolve failed")
	}
	if len(fr.orgCalls) != 1 || fr.orgCalls[0] != "hanzo/cd" {
		t.Errorf("first-party did NOT org-pin: orgCalls=%v", fr.orgCalls)
	}
	if len(fr.calls) != 0 {
		t.Errorf("first-party must NEVER use unique-slug Resolve: calls=%v", fr.calls)
	}

	// Multi-tenant host → unique-slug Resolve, never org-pinned.
	fr.calls, fr.orgCalls = nil, nil
	slug2, fp2, ok2 := s.siteSlug("my-site.hanzo.app")
	if !ok2 || fp2 || slug2 != "my-site" {
		t.Fatalf("siteSlug(my-site.hanzo.app) = (%q,%v,%v), want (my-site,false,true)", slug2, fp2, ok2)
	}
	if _, ok := s.resolveLivePinned(context.Background(), slug2, fp2); !ok {
		t.Fatal("multi-tenant resolve failed")
	}
	if len(fr.calls) != 1 || fr.calls[0] != "my-site" {
		t.Errorf("multi-tenant should use Resolve: calls=%v", fr.calls)
	}
	if len(fr.orgCalls) != 0 {
		t.Errorf("multi-tenant must NEVER org-pin: orgCalls=%v", fr.orgCalls)
	}
}

// TestFirstPartyDropsReserved (RED F-2): a reserved label an operator mistakenly
// lists in the first-party allowlist must NOT become a site on the brand apex —
// api/login stay real auth surfaces; only clean labels (cd) serve.
func TestFirstPartyDropsReserved(t *testing.T) {
	s := New(Config{
		Apex: "hanzo.app", Reserved: []string{"app", "api", "admin"},
		SelfDomains:    []string{"hanzo.ai"},
		FirstPartyApex: "hanzo.ai", FirstPartySites: []string{"cd", "flow", "gallery", "api", "login", "wallet"},
		FirstPartyOrg: "hanzo",
	}, luxlog.New("test"))
	for _, l := range []string{"cd", "flow", "gallery"} {
		if _, _, ok := s.siteSlug(l + ".hanzo.ai"); !ok {
			t.Errorf("%s.hanzo.ai (clean label) should serve as a first-party site", l)
		}
	}
	for _, l := range []string{"api", "login", "wallet"} {
		if _, _, ok := s.siteSlug(l + ".hanzo.ai"); ok {
			t.Errorf("%s.hanzo.ai (reserved) must be DROPPED from the first-party allowlist", l)
		}
	}
}

// TestNotFoundAnswersDataRequestsInType pins the decision notFound makes: a
// missing DATA asset is answered as JSON, a missing document as HTML. Both
// hanzo-team (/config.json) and edge (/presets/wigglewobble.json) shipped without
// a file their bundle fetches at runtime; the plane answered an honest 404 whose
// BODY was an HTML page, so both apps died inside minified vendor code on
//
//	Unexpected token '<', "<!doctype "... is not valid JSON
//
// with no path in the message. Answering in-type means .json() parses and the app
// receives the missing path.
func TestNotFoundAnswersDataRequestsInType(t *testing.T) {
	for _, tc := range []struct {
		path string
		json bool
	}{
		{"/config.json", true},
		{"/presets/wigglewobble.json", true},
		{"/", false},
		{"/about", false},
		{"/index.html", false},
		{"/assets/main.js", false},
		{"/logo.png", false},
		{"/model.wasm", false},
	} {
		if got := isJSON(contentType(resolveKey(tc.path))); got != tc.json {
			t.Errorf("%s: json body = %v, want %v (type %q)",
				tc.path, got, tc.json, contentType(resolveKey(tc.path)))
		}
	}
}

// Behind the ingress the parsed URI host is EMPTY and the only carrier of the
// customer-facing name is X-Forwarded-Host. Without this, siteSlug("") failed,
// customCandidate("") failed, and every published site fell through to the API
// pipeline — <slug>.hanzo.app served the console SPA and mounted the whole cloud
// API under a customer's own hostname (measured live 2026-08-03).
func TestMiddlewareResolvesFromForwardedHostWhenParsedHostIsEmpty(t *testing.T) {
	fr := &fakeResolver{found: false}
	SetResolver(fr)
	defer SetResolver(nil)
	app := newTestApp(testServer())

	// The shape behind the ingress: the parsed host is not a site host (the
	// ingress' own name), and the customer-facing name rides X-Forwarded-Host.
	// NOTE httptest synthesizes "localhost" for an empty Host, so an empty
	// string cannot be used to express "no parsed host" — measured, not assumed.
	req := httptest.NewRequest("GET", "http://localhost/index.html", nil)
	req.Header.Set("X-Forwarded-Host", "quest.hanzo.app")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.Header.Get("X-Sentinel") == "hit" {
		t.Fatal("a published site fell through to the API pipeline — this is the console-instead-of-site defect")
	}
	if got := fr.slugs(); len(got) != 1 || got[0] != "quest" {
		t.Fatalf("resolver called with %v, want exactly [quest]", got)
	}
}

// ...and the fallback must never become an override. A request that HAS a host
// ignores the header completely — the host picks the ORG, so a client able to
// override a real host could serve itself another tenant's site.
func TestMiddlewareForwardedHostNeverOverridesARealHost(t *testing.T) {
	fr := &fakeResolver{found: false}
	SetResolver(fr)
	defer SetResolver(nil)
	app := newTestApp(testServer())

	req := httptest.NewRequest("GET", "http://victim.hanzo.app/index.html", nil)
	req.Header.Set("X-Forwarded-Host", "attacker.hanzo.app")
	if _, err := app.Test(req); err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := fr.slugs(); len(got) != 1 || got[0] != "victim" {
		t.Fatalf("resolver called with %v, want exactly [victim] — the header must not override a real host", got)
	}
}

// The edge must resolve a site when projects is in ANOTHER process.
//
// This is the production shape and it is the defect this fallback exists for:
// the pod boots ~25 single-app processes, so the in-process registry is nil at
// the edge. A nil resolver is a clean miss, so every published site fell through
// to the API pipeline and <slug>.hanzo.app served the console SPA — with no
// error logged anywhere, because nothing had failed.
func TestFallbackResolverServesWhenProjectsIsElsewhere(t *testing.T) {
	SetResolver(nil) // projects is NOT in this process — the production case.
	fb := &fakeResolver{found: false}
	SetFallbackResolver(fb)
	defer SetFallbackResolver(nil)
	app := newTestApp(testServer())

	req := httptest.NewRequest("GET", "http://quest.hanzo.app/index.html", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.Header.Get("X-Sentinel") == "hit" {
		t.Fatal("fell through to the API pipeline — this is the console-instead-of-site defect")
	}
	if got := fb.slugs(); len(got) != 1 || got[0] != "quest" {
		t.Fatalf("fallback called with %v, want exactly [quest]", got)
	}
}

// A co-resident store still answers WITHOUT the hop: the in-process resolver
// wins whenever it is set, so sharing a process costs nothing.
func TestInProcessResolverWinsOverTheFallback(t *testing.T) {
	inproc := &fakeResolver{found: false}
	fb := &fakeResolver{found: false}
	SetResolver(inproc)
	SetFallbackResolver(fb)
	defer func() { SetResolver(nil); SetFallbackResolver(nil) }()
	app := newTestApp(testServer())

	req := httptest.NewRequest("GET", "http://quest.hanzo.app/index.html", nil)
	if _, err := app.Test(req); err != nil {
		t.Fatalf("test: %v", err)
	}
	if len(inproc.slugs()) != 1 {
		t.Errorf("in-process resolver was not used: %v", inproc.slugs())
	}
	if n := len(fb.slugs()); n != 0 {
		t.Errorf("fallback was consulted %d times; the in-process store must win", n)
	}
}

// TestForwardedHostNeverOverridesOurOwnHost is the host-confusion regression.
//
// requestHost used to decide "is the parsed host real" by asking "is it a host we
// would SERVE" — siteSlug, else customCandidate. The gap between those two
// questions is exactly OUR OWN domains: `login.hanzo.ai` names no site, and
// customCandidate excludes it BY DESIGN (IsSelfHost), so neither arm fired and the
// client-supplied X-Forwarded-Host won. A request addressed to our auth apex then
// resolved and served whatever custom domain the header named — a tenant's content
// under our hostname, needing no site_hosts row on hanzo.ai at all.
//
// The two tests that look like they pinned this (TestMiddlewareTenantKeyedByHostNotPath,
// TestMiddlewareForwardedHostNeverOverridesARealHost) both send a host that IS a
// site, so the early return fired and the header was never read. They proved the
// property only in the case where it already held.
func TestForwardedHostNeverOverridesOurOwnHost(t *testing.T) {
	fr := &fakeResolver{found: false}
	SetResolver(fr)
	defer SetResolver(nil)
	// hanzo.ai is OURS here, exactly as ConfigFromEnv derives it in production.
	s := New(Config{Apex: "hanzo.app", SelfDomains: []string{"hanzo.ai"}}, luxlog.New("test"))
	t.Cleanup(func() { SetSelfDomains(nil) })
	app := newTestApp(s)

	for _, host := range []string{"login.hanzo.ai", "api.hanzo.ai", "hanzo.ai"} {
		fr.reset()
		req := httptest.NewRequest("GET", "http://"+host+"/index.html", nil)
		req.Header.Set("X-Forwarded-Host", "attacker.example")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if got := fr.slugs(); len(got) != 0 {
			t.Errorf("%s: the binding resolver was asked about %v — a client header "+
				"overrode a host WE operate, so a request addressed to our own name "+
				"would serve a tenant's site", host, got)
		}
		if resp.Header.Get("X-Sentinel") != "hit" {
			t.Errorf("%s: did not reach the normal API pipeline", host)
		}
	}
}

// reset clears the recorded calls so one fake can serve a table of cases.
func (f *fakeResolver) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.orgCalls = nil, nil
}

// TestCors pins the header that decides whether the builder can preview a
// deployed site at all.
//
// Measured in a real sandboxed frame (sandbox="allow-scripts", the preview's own
// attributes) against megashop.hanzo.app: the bundle loads as an absolute CLASSIC
// script and is BLOCKED as a module. Vite emits a module, so a site with no
// Access-Control-Allow-Origin previews as a blank white page.
func TestCors(t *testing.T) {
	// A subresource is readable cross-origin. This is the whole fix.
	for _, ct := range []string{
		"text/javascript; charset=utf-8",
		"text/css; charset=utf-8",
		"application/wasm",
		"image/png",
		"application/json",
		"font/woff2",
	} {
		if got := headerMap(cors(false, ct))["Access-Control-Allow-Origin"]; got != "*" {
			t.Errorf("cors(false, %q) ACAO = %q, want *", ct, got)
		}
	}

	// A DOCUMENT is navigated to, never read cross-origin — and the preview
	// supplies its own. Granting it nothing keeps the surface as small as the
	// problem.
	for _, ct := range []string{"text/html; charset=utf-8", "text/html"} {
		if h := cors(false, ct); h != nil {
			t.Errorf("cors(false, %q) = %v, want nil", ct, h)
		}
	}

	// A site that opted into cross-origin isolation asked for same-origin
	// subresources. ACAO must not quietly widen that back out — if this ever
	// returns a header, isolation became decorative.
	for _, ct := range []string{"text/javascript; charset=utf-8", "application/wasm", "text/html; charset=utf-8"} {
		if h := cors(true, ct); h != nil {
			t.Errorf("cors(true, %q) = %v, want nil — isolation must win", ct, h)
		}
	}

	// The two policies stay orthogonal: neither emits the other's headers.
	a := headerMap(cors(false, "application/wasm"))
	for _, k := range []string{"Cross-Origin-Resource-Policy", "Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy"} {
		if _, ok := a[k]; ok {
			t.Errorf("cors must not emit %s, got %v", k, a)
		}
	}
	i := headerMap(crossOriginIsolation(true, "application/wasm"))
	if _, ok := i["Access-Control-Allow-Origin"]; ok {
		t.Errorf("crossOriginIsolation must not emit ACAO, got %v", i)
	}
}
