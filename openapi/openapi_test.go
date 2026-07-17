package openapi

import (
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func newApp() *zip.App {
	return zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
}

// The product axis must be mechanical — first segment after /v1/, no judgment.
// The "" cases are the honesty boundary: a segment that is not a product name
// gets no tag rather than a fabricated one.
func TestProduct(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/v1/kms/orgs/:org/secrets", "kms"},
		{"/v1/billing/usage", "billing"},
		{"/v1/finance/balance", "finance"}, // clients/billing serves it: product != subsystem
		{"/v1/billing", "billing"},
		{"/v1/billing/*", "billing"}, // a catch-all still names its product
		{"/v1/openapi.json", ""},     // a file, not a product
		{"/v1/*", ""},                // a wildcard, not a product
		{"/v1/:id", ""},              // a param, not a product
		{"/v1/", ""},
		{"/health", ""},           // not under /v1
		{"/.well-known/jwks", ""}, // not under /v1
		{"/tasks/*", ""},          // not under /v1
		{"/v2/kms/keys", ""},      // house law: there is no v2
	} {
		if got := Product(tc.path); got != tc.want {
			t.Errorf("Product(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// fiber :param → OpenAPI {param}, and a fiber wildcard becomes a named
// {wildcardN} because "*1" is not a legal URI-template name.
func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		in     string
		want   string
		params []string
	}{
		{"/v1/billing/usage", "/v1/billing/usage", nil},
		{"/v1/kms/orgs/:org/secrets", "/v1/kms/orgs/{org}/secrets", []string{"org"}},
		{"/v1/kms/orgs/:org/secrets/*", "/v1/kms/orgs/{org}/secrets/{wildcard1}", []string{"org", "wildcard1"}},
		{"/v1/a/:x/b/:y", "/v1/a/{x}/b/{y}", []string{"x", "y"}},
		{"/v1/*", "/v1/{wildcard1}", []string{"wildcard1"}},
	} {
		got, params := translate(tc.in)
		if got != tc.want {
			t.Errorf("translate(%q) path = %q, want %q", tc.in, got, tc.want)
		}
		if len(params) != len(tc.params) {
			t.Fatalf("translate(%q) params = %v, want %v", tc.in, params, tc.params)
		}
		for i := range params {
			if params[i] != tc.params[i] {
				t.Errorf("translate(%q) params = %v, want %v", tc.in, params, tc.params)
			}
		}
	}
}

// THE LOAD-BEARING FACT, pinned so a zip/fiber bump that changes it fails here.
//
// A duplicate registration and a middleware chain produce the SAME observable
// route: one entry, N chained handlers. They are indistinguishable through the
// public API — which is why this generator never reads the handler count, and
// why "handlers > 1 means collision" is not a fleet-wide truth.
//
// Both cases must still project to exactly ONE operation, because one pattern IS
// one operation regardless of what is chained behind it.
func TestMergedAndChainedAreIndistinguishableAndBothYieldOneOperation(t *testing.T) {
	// (a) a genuine duplicate: two separate registrations of one pattern.
	dup := newApp()
	dup.Get("/v1/bots", func(c *zip.Ctx) error { return c.JSON(200, "machine") })
	dup.Get("/v1/bots", func(c *zip.Ctx) error { return c.JSON(200, "run") })

	// (b) a legitimate chain: ONE registration, middleware + terminal handler —
	// the apps/commerce.go:151 shape.
	chain := newApp()
	chain.Get("/v1/bots",
		func(c *zip.Ctx) error { return c.Next() },
		func(c *zip.Ctx) error { return c.JSON(200, "run") },
	)

	dr, cr := dup.Fiber().GetRoutes(true), chain.Fiber().GetRoutes(true)
	if len(dr) != 1 || len(cr) != 1 {
		t.Fatalf("both shapes should be ONE route entry; dup=%d chain=%d", len(dr), len(cr))
	}
	if len(dr[0].Handlers) != 2 || len(cr[0].Handlers) != 2 {
		t.Fatalf("both shapes should carry 2 chained handlers; dup=%d chain=%d — "+
			"if this diverged, the handler count became meaningful and this package should revisit it",
			len(dr[0].Handlers), len(cr[0].Handlers))
	}

	// Indistinguishable: same method, same path, same handler count.
	if dr[0].Method != cr[0].Method || dr[0].Path != cr[0].Path {
		t.Fatalf("routes differ: %v vs %v", dr[0], cr[0])
	}

	// And each projects to exactly one operation.
	for name, app := range map[string]*zip.App{"duplicate": dup, "chain": chain} {
		doc, err := Spec(app, Info{Title: "t", Version: "v1"})
		if err != nil {
			t.Fatalf("%s: Spec: %v", name, err)
		}
		item, ok := doc.Paths["/v1/bots"]
		if !ok || len(item) != 1 || item["get"] == nil {
			t.Fatalf("%s: want exactly one get operation at /v1/bots, got %v", name, doc.Paths)
		}
	}
}

// Middleware matches path prefixes and is not an operation — fiber's own
// GetRoutes(true) filter drops it, and Live must use it.
func TestLiveDropsMiddleware(t *testing.T) {
	app := newApp()
	app.Use(func(c *zip.Ctx) error { return c.Next() })
	app.Get("/v1/kms/health", func(c *zip.Ctx) error { return c.JSON(200, "ok") })

	live := Live(app)
	if len(live) != 1 {
		t.Fatalf("Live() = %d routes, want 1 (the Use() middleware must not be an operation): %+v", len(live), live)
	}
	if live[0].Path != "/v1/kms/health" {
		t.Errorf("Live()[0].Path = %q", live[0].Path)
	}
}

// CONNECT has no OpenAPI Path Item field, and HEAD cannot be stated stably
// (fiber auto-generates it at startupProcess, not at registration). Live must
// drop both — see the `methods` doc comment.
func TestLiveDropsUnrepresentableMethods(t *testing.T) {
	app := newApp()
	app.All("/v1/tasks", func(c *zip.Ctx) error { return c.JSON(200, "ok") })

	for _, r := range Live(app) {
		if r.Method == "CONNECT" || r.Method == "HEAD" {
			t.Errorf("Live() emitted %s — it is not representable/stable in the document", r.Method)
		}
	}
	// The representable ones from All() survive.
	got := map[string]bool{}
	for _, r := range Live(app) {
		got[r.Method] = true
	}
	for _, m := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		if !got[m] {
			t.Errorf("Live() dropped %s, which All() registers and OpenAPI can express", m)
		}
	}
}

// A path param and a literal of the same name must not collapse onto one
// operationId — OpenAPI requires them unique.
func TestOperationIDDistinguishesParamFromLiteral(t *testing.T) {
	if a, b := operationID("GET", "/v1/a/{b}"), operationID("GET", "/v1/a/b"); a == b {
		t.Fatalf("operationId collision: /v1/a/{b} and /v1/a/b both → %q", a)
	}
}

// From must reject a duplicate operationId rather than emit a document a
// generator would mis-consume. This is also what keeps (method,path) injective.
//
// The pair below is the residual aliasing the encoding cannot remove: '_' is the
// separator, so a literal '_' in a segment can alias a '/'. The guard is what
// makes ids trustworthy, not the encoding.
func TestFromRejectsDuplicateOperationID(t *testing.T) {
	rs := []Route{
		{Method: "GET", Path: "/v1/a/b_c"},
		{Method: "GET", Path: "/v1/a/b/c"}, // both → get_v1_a_b_c
	}
	if _, err := From(rs, Info{Title: "t", Version: "v1"}); err == nil {
		t.Fatal("From() accepted two routes with the same derived operationId; it must refuse")
	}
}

// A REAL pair from the live router: /v1/pricing-policy and /v1/pricing/policy
// both exist. Folding '-' into '_' collapsed them onto one id and made the whole
// document unemittable. Pinned so the encoding never regresses.
func TestOperationIDKeepsHyphenDistinctFromPathSeparator(t *testing.T) {
	rs := []Route{
		{Method: "GET", Path: "/v1/pricing-policy"},
		{Method: "GET", Path: "/v1/pricing/policy"},
	}
	doc, err := From(rs, Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatalf("From: %v — these are two distinct live routes and must yield two distinct ids", err)
	}
	got := []string{
		doc.Paths["/v1/pricing-policy"]["get"].OperationID,
		doc.Paths["/v1/pricing/policy"]["get"].OperationID,
	}
	if got[0] == got[1] {
		t.Fatalf("both routes derived operationId %q", got[0])
	}
	if got[0] != "get_v1_pricing-policy" || got[1] != "get_v1_pricing_policy" {
		t.Errorf("ids = %v, want [get_v1_pricing-policy get_v1_pricing_policy]", got)
	}
}

// The document shape: 3.1.0, product tags, path params required, and no
// fabricated responses.
func TestFromShape(t *testing.T) {
	rs := []Route{
		{Method: "GET", Path: "/v1/kms/orgs/:org/secrets"},
		{Method: "POST", Path: "/v1/kms/orgs/:org/secrets"},
		{Method: "GET", Path: "/v1/billing/usage"},
	}
	doc, err := From(rs, Info{Title: "Hanzo Cloud", Version: "v1"}, Server{URL: "https://api.hanzo.ai"})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", doc.OpenAPI)
	}
	if len(doc.Tags) != 2 || doc.Tags[0].Name != "billing" || doc.Tags[1].Name != "kms" {
		t.Errorf("tags = %+v, want sorted [billing kms]", doc.Tags)
	}
	item, ok := doc.Paths["/v1/kms/orgs/{org}/secrets"]
	if !ok {
		t.Fatalf("missing translated path; have %v", doc.Paths)
	}
	if len(item) != 2 || item["get"] == nil || item["post"] == nil {
		t.Fatalf("path item should carry get+post, got %v", item)
	}
	get := item["get"]
	if len(get.Tags) != 1 || get.Tags[0] != "kms" {
		t.Errorf("tags = %v, want [kms]", get.Tags)
	}
	if len(get.Parameters) != 1 {
		t.Fatalf("parameters = %+v, want 1", get.Parameters)
	}
	if p := get.Parameters[0]; p.Name != "org" || p.In != "path" || !p.Required || p.Schema.Type != "string" {
		t.Errorf("param = %+v, want {org path required string}", p)
	}
	if get.OperationID == item["post"].OperationID {
		t.Errorf("get and post share operationId %q", get.OperationID)
	}
}

// Mount serves the document off the app it is registered on, and the document
// includes its OWN route — proof the lazy build sees the final table.
func TestMountServesLiveSpecIncludingItself(t *testing.T) {
	app := newApp()
	app.Get("/v1/kms/health", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	Mount(app, Info{Title: "Hanzo Cloud", Version: "v1"})

	doc, err := Spec(app, Info{Title: "Hanzo Cloud", Version: "v1"})
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if _, ok := doc.Paths[Path]; !ok {
		t.Errorf("document omits its own endpoint %s; have %v", Path, doc.Paths)
	}
	if _, ok := doc.Paths["/v1/kms/health"]; !ok {
		t.Errorf("document omits /v1/kms/health")
	}
}
