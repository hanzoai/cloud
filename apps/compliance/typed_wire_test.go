package compliance

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/openapi"
)

// doRaw issues a request and returns the raw response — for the assertions that
// are about the envelope (headers, status on a non-JSON body) rather than the
// decoded JSON that do() hands back.
func doRaw(t *testing.T, app *zip.App, rq *http.Request) *http.Response {
	t.Helper()
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", rq.Method, rq.URL.Path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestTypedOpsPreserveTheWire pins the envelope details the typed conversion had
// to carry over from the untyped handlers, none of which the behavior suite
// above measures: the no-store cache pin on the PII-bearing and per-org reads,
// the 1 MiB body cap on the JSON writes, the ?limit query binding, and the
// empty-body tolerance a zero In must keep.
func TestTypedOpsPreserveTheWire(t *testing.T) {
	app, _ := mount(t)
	org := "org_wire"

	// Seed two subjects so the limit assertion has something to cap.
	for _, ref := range []string{"a", "b"} {
		code, _ := do(t, app, http.MethodPost, "/v1/compliance/subjects", org,
			map[string]any{"kind": "individual", "ref": ref})
		if code != http.StatusCreated {
			t.Fatalf("seed subject %s: %d", ref, code)
		}
	}

	t.Run("no-store rides the per-org and PII reads", func(t *testing.T) {
		for _, path := range []string{
			"/v1/compliance/status",
			"/v1/compliance/subjects",
			"/v1/compliance/audit",
		} {
			rq := httptest.NewRequest(http.MethodGet, path, nil)
			rq.Header.Set("X-Org-Id", org)
			rq.Header.Set("X-User-Id", "u_"+org)
			resp := doRaw(t, app, rq)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: %d", path, resp.StatusCode)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("GET %s Cache-Control = %q, want no-store", path, cc)
			}
		}
	})

	t.Run("?limit binds through the typed op", func(t *testing.T) {
		code, out := do(t, app, http.MethodGet, "/v1/compliance/subjects?limit=1", org, nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d", code)
		}
		if data, _ := out["data"].([]any); len(data) != 1 {
			t.Errorf("limit=1 returned %d rows", len(out["data"].([]any)))
		}
	})

	t.Run("the 1 MiB body cap holds", func(t *testing.T) {
		rq := httptest.NewRequest(http.MethodPost, "/v1/compliance/subjects",
			bytes.NewReader(make([]byte, maxBody+1)))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
		if resp := doRaw(t, app, rq); resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("oversized body: %d, want 413", resp.StatusCode)
		}
	})

	t.Run("an empty POST body still reaches the handler's own refusal", func(t *testing.T) {
		// The untyped decode() read an empty body as the zero value; zip's binder
		// does the same, so the answer stays the handler's 400 about kind — never
		// a binder error about the body's absence.
		code, out := do(t, app, http.MethodPost, "/v1/compliance/subjects", org, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("empty body: %d, want 400", code)
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, "kind must be") {
			t.Errorf("empty body answered %q, want the handler's kind refusal", msg)
		}
	})
}

// untypedByDesign is the CLOSED list of compliance operations that are NOT typed
// ops, each with the wire fact that keeps it out. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. This one is missing on purpose. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on.
var untypedByDesign = map[string]string{
	"POST /v1/compliance/verifications/webhook": "authenticates by HMAC over the EXACT received bytes " +
		"(apps/idv/webhook.go Verify: mac.Write(body)), which zip's invoke has already json.Unmarshaled " +
		"into In before the handler runs (typed.go:234) — a re-encoded In is not the signed value, so the " +
		"signature could never match. It also answers TWO 200 shapes (the reconciled check, or " +
		`{"ignored": …} for a reference it does not know) where an op declares exactly one Out.`,
}

// complianceOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router (not the source) is what makes this a
// gate rather than prose — a route added anywhere in routes() shows up here.
func complianceOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app, _ := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "compliance", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, routePrefix) }
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
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a compliance operation is neither a
// typed op nor the one named above — so the next route added here is typed by
// default, and dropping one out of the registry takes a deliberate edit with a
// reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := complianceOps(t)

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
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which compliance no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface: it becomes the OpenAPI description
// AND the MCP tool description a model reads to pick the tool. zipdoc_gen.go is
// what carries it into the binary, so an op added without regenerating shows up
// here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := complianceOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed compliance ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/compliance/...", key)
		}
	}
}
