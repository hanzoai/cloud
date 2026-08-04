package product

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of product operations that are NOT typed
// ops. It is EMPTY: all four routes are typed, and this list exists so that
// dropping one back out takes a deliberate edit with a reason.
var untypedByDesign = map[string]string{}

const (
	searchKeyEnv = "searchApiKey"
	vectorKeyEnv = "vectorApiKey"
)

// mountApp mounts the product surface with both bearer keys configured and both
// upstreams pointed at a dead address, so every read exercises the DEGRADE path
// (the honest empty state) rather than a live Meilisearch/Qdrant.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv(searchKeyEnv, "search-key")
	t.Setenv(vectorKeyEnv, "vector-key")
	// 127.0.0.1:1 refuses instantly — an unreachable upstream, not a slow one.
	t.Setenv("searchEndpoint", "http://127.0.0.1:1")
	t.Setenv("vectorEndpoint", "http://127.0.0.1:1")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func get(t *testing.T, app *zip.App, path, bearer string) (int, []byte) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		rq.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestKeyGateRunsBeforeTheOp is the assertion the conversion turns on. The bearer
// arrives as a header field of keyedIn — the typed way an op sees a request
// header — and requireKey opens every handler, so an unset key still 503s, a
// wrong key still 401s, and the SEARCH key never admits a VECTOR read. (It was
// briefly middleware on the two subtrees; product does not OWN those subtrees —
// provisioning does — so the confinement gate rightly refused that boot.)
func TestKeyGateRunsBeforeTheOp(t *testing.T) {
	app := mountApp(t)

	searchPaths := []string{"/v1/search/indexes", "/v1/search/stats"}
	vectorPaths := []string{"/v1/vector/collections", "/v1/vector/stats"}

	for _, p := range append(append([]string{}, searchPaths...), vectorPaths...) {
		if code, _ := get(t, app, p, ""); code != http.StatusUnauthorized {
			t.Errorf("GET %s with no bearer = %d, want 401", p, code)
		}
		if code, _ := get(t, app, p, "wrong"); code != http.StatusUnauthorized {
			t.Errorf("GET %s with a wrong bearer = %d, want 401", p, code)
		}
	}
	// Cross-surface: the search key must not open the vector surface, or vice versa.
	for _, p := range vectorPaths {
		if code, _ := get(t, app, p, "search-key"); code != http.StatusUnauthorized {
			t.Errorf("GET %s with the SEARCH key = %d, want 401", p, code)
		}
	}
	for _, p := range searchPaths {
		if code, _ := get(t, app, p, "vector-key"); code != http.StatusUnauthorized {
			t.Errorf("GET %s with the VECTOR key = %d, want 401", p, code)
		}
	}
}

// TestUnconfiguredSurfaceFailsClosed pins the 503 an unset key answers, so a
// mis-provisioned deploy can never silently serve an open endpoint.
func TestUnconfiguredSurfaceFailsClosed(t *testing.T) {
	t.Setenv(searchKeyEnv, "")
	t.Setenv(vectorKeyEnv, "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	for _, p := range []string{
		"/v1/search/indexes", "/v1/search/stats",
		"/v1/vector/collections", "/v1/vector/stats",
	} {
		// Even WITH a bearer: an unset upstream key is 503, never 200 and never 401.
		if code, _ := get(t, app, p, "anything"); code != http.StatusServiceUnavailable {
			t.Errorf("GET %s on an unconfigured deploy = %d, want 503", p, code)
		}
	}
}

// TestUnreachableUpstreamsDegradeTo200 pins the empty-but-valid bodies the console
// panels depend on: this surface prefers an honest empty state over a 5xx whenever
// an upstream hiccups, and the typed Outs must keep emitting the empty ARRAY rather
// than null.
func TestUnreachableUpstreamsDegradeTo200(t *testing.T) {
	app := mountApp(t)

	code, body := get(t, app, "/v1/search/indexes", "search-key")
	if code != http.StatusOK {
		t.Fatalf("indexes: %d (%s)", code, body)
	}
	if string(body) != `{"indexes":[]}` {
		t.Errorf("indexes body = %s, want {\"indexes\":[]}", body)
	}

	code, body = get(t, app, "/v1/vector/collections", "vector-key")
	if code != http.StatusOK {
		t.Fatalf("collections: %d (%s)", code, body)
	}
	if string(body) != `{"collections":[]}` {
		t.Errorf("collections body = %s, want {\"collections\":[]}", body)
	}

	code, body = get(t, app, "/v1/search/stats", "search-key")
	if code != http.StatusOK {
		t.Fatalf("search stats: %d (%s)", code, body)
	}
	var ss searchStats
	if err := json.Unmarshal(body, &ss); err != nil {
		t.Fatalf("search stats decode: %v", err)
	}
	if ss.SearchesPerDay == nil {
		t.Error("searchesPerDay is null — the console reads an array")
	}

	code, body = get(t, app, "/v1/vector/stats", "vector-key")
	if code != http.StatusOK {
		t.Fatalf("vector stats: %d (%s)", code, body)
	}
	if string(body) != `{"totalCollections":0,"totalVectors":0,"totalStorageBytes":0}` {
		t.Errorf("vector stats body = %s", body)
	}
}

// productSurface reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry.
func productSurface(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "product", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool {
		return strings.HasPrefix(p, "/v1/search") || strings.HasPrefix(p, "/v1/vector")
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when a product operation is neither a typed
// op nor named above — so the next route added here is typed by default.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := productSurface(t)
	if len(served) != 4 {
		t.Errorf("product serves %d operations, expected 4 — update this gate deliberately", len(served))
	}

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which product no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := productSurface(t)
	if len(typed) == 0 {
		t.Fatal("no typed product ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/product/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see: the FIELDS of the published shapes.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := productSurface(t)
	if len(schemas) == 0 {
		t.Fatal("no product schemas in the typed registry at all")
	}
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue
		}
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s", strings.Join(bare, ", "))
	}
}
