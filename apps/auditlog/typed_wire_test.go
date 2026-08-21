package auditlog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// routePrefix is the ONE address this subsystem owns. It is deliberately NOT
// "/v1/auditlog": the subsystem is registered under that name so serve.go's
// generic /v1/<name>/health never shadows the trail, and the surface it actually
// serves is /v1/audit.
const routePrefix = "/v1/audit"

func doRaw(t *testing.T, app *zip.App, path, user, org string) (*http.Response, string) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		rq.Header.Set("X-User-Id", user)
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// TestEnvelopeBytesDidNotMove pins the one thing the map→struct conversion could
// silently have changed. The untyped handler wrote a map[string]any, and
// encoding/json SORTS a map's keys; a struct emits its DECLARATION order. So the
// Out's fields are declared alphabetically on purpose, and this is the assertion
// that says so — a reordering edit that reads as harmless moves every byte of
// every response on this surface.
func TestEnvelopeBytesDidNotMove(t *testing.T) {
	app := mountApp(t, newStore(t))
	resp, body := doRaw(t, app, "/v1/audit?pageSize=1", "maxpower/dave", "maxpower")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/audit = %d, want 200", resp.StatusCode)
	}
	if !strings.HasPrefix(body, `{"data":[`) {
		t.Errorf("envelope starts %.20q, want the map's sorted first key `data`", body)
	}
	// The ENVELOPE's own keys, read after the row array ends — every audit row
	// carries a `status` of its own, so the envelope's has to be located past it.
	tail := body[strings.LastIndex(body, "]"):]
	if tail != `],"msg":"","status":"ok","total":3}` {
		t.Errorf("envelope tail = %s, want `],\"msg\":\"\",\"status\":\"ok\",\"total\":3}` — the exact "+
			"order a sorted map[string]any produced", tail)
	}
}

// TestNoStoreRidesTheSuccessAnswerOnly pins the response header the untyped
// handler set by hand and a typed op cannot: per-tenant security events must
// never be cached. It rides SUCCESS only — the refusals never carried it, and a
// middleware that set it unconditionally would be a different wire.
func TestNoStoreRidesTheSuccessAnswerOnly(t *testing.T) {
	app := mountApp(t, newStore(t))
	resp, _ := doRaw(t, app, "/v1/audit", "maxpower/dave", "maxpower")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/audit = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	// 401: no validated principal. The header was never set on this path.
	un, _ := doRaw(t, app, "/v1/audit", "", "maxpower")
	if un.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET = %d, want 401", un.StatusCode)
	}
	if cc := un.Header.Get("Cache-Control"); cc != "" {
		t.Errorf("401 Cache-Control = %q, want unset", cc)
	}
}

// TestUnparseablePagingFallsBackRatherThanRefusing pins the paging tolerance the
// query-string reader had and a naive int-typed In would have lost. zip's
// bindURL leaves an unparseable value at the field's ZERO, so `?pageSize=0` and
// `?pageSize=abc` would arrive identically through an int — while the wire this
// surface has always served treats both as "unset, use 100". The fields stay
// STRINGS for exactly that reason, and the admin twin (GET /v1/admin/audit)
// publishes them the same way.
func TestUnparseablePagingFallsBackRatherThanRefusing(t *testing.T) {
	app := mountApp(t, newStore(t))
	for _, q := range []string{"?pageSize=abc", "?pageSize=0", "?pageSize=-5", "?p=abc", ""} {
		code, rows, total := call(t, app, "/v1/audit"+q, "maxpower/dave", "maxpower")
		if code != http.StatusOK {
			t.Fatalf("GET /v1/audit%s = %d, want 200 — a malformed filter must not refuse the trail", q, code)
		}
		if total != 3 || len(rows) != 3 {
			t.Errorf("GET /v1/audit%s → %d rows / total %d, want the default page of 3", q, len(rows), total)
		}
	}
}

// auditOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router rather than the source is what makes the two
// tests below gates instead of prose.
func auditOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountApp(t, newStore(t))
	doc, err := openapi.Spec(app, openapi.Info{Title: "audit", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return p == routePrefix || strings.HasPrefix(p, routePrefix+"/") }
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

// TestEveryRouteIsTypedOrNamed fails when an audit operation is neither a typed
// op nor named below — so the next route added here is typed by default. The
// named list is EMPTY: this surface is one read, and it types cleanly.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := auditOps(t)
	if len(served) == 0 {
		t.Fatal("the live router serves no /v1/audit operation at all")
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; !ok {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method.", strings.Join(untyped, ", "))
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema: it
// becomes the OpenAPI description AND the MCP tool description a model reads to
// pick the tool. zipdoc_gen.go carries it into the binary, so an op added without
// regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := auditOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed audit ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/auditlog/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; the shape's
// FIELDS come from a different place — a doc comment on each one, which zipdoc
// lifts one at a time.
//
// The whole product here is one row type, so every field of it is the product.
// `seq` is a gapless total order and a missing number is a missing record;
// `hash`/`prevHash` are what a reader recomputes to prove nothing was edited;
// `result` is a closed vocabulary in which a deny is evidence rather than an error;
// `org` is the tenant acted IN while `home` — present only when they differ — is
// what makes a row an impersonation. None of that is legible from the names.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t, newStore(t)), openapi.Info{Title: "audit", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("audit publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/auditlog describe",
			len(bare), strings.Join(bare, ", "))
	}
}
