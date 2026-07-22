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
		// No real ".." path SEGMENT survives (unencoded traversal is collapsed by
		// rooted path.Clean). A literal "%2f" is NOT a separator to S3 — object
		// keys are opaque strings — so an encoded sequence stays inside the prefix
		// as a (nonexistent) literal key; the decisive guard is prefix containment.
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
	eq("docs", "docs", "docs/index.html")
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
		resp, err := app.Fiber().Test(req)
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
	resp, err := app.Fiber().Test(req)
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
	resp, err := app.Fiber().Test(req)
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
		resp, err := app.Fiber().Test(req)
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
		resp, err := app.Fiber().Test(req)
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
	if got := contentType("app.css"); got == "" || !contains(got, "css") {
		t.Errorf("contentType(app.css) = %q, want a css type", got)
	}
	if got := contentType("index.html"); got == "" || !contains(got, "html") {
		t.Errorf("contentType(index.html) = %q, want an html type", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
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
