package pricing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// compose installs what a HOST installs, which is cloud.Bridge app-wide, once at
// the root. In a test the test IS the composer, so it owes that install —
// skipping it does not test a stricter program, it tests one production has
// never run.
//
// Mount installs a Bridge of its own on the subsystem's router as well, and this
// is deliberately not redundant with it: that one covers pricing's own prefixes
// wherever pricing is mounted, INCLUDING with no composer at all, while this one
// is what every other app in the binary rides. Bridge derives everything it parks
// from the request, so the second pass parks the same values.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// doer drives the mounted subsystem over HTTP. Both tests here build one.
type doer func(method, path, body string, hdr map[string]string) (*http.Response, []byte)

// slashed reports whether an id is provider-qualified ("anthropic/claude-…"),
// which is the shape the greedy wildcard route exists for.
func slashed(id string) bool { return strings.Contains(id, "/") }

// modelFrom returns a model id the MOUNTED catalog actually serves, chosen by
// SHAPE rather than written down.
//
// A literal catalog id is a claim about the snapshot, and snapshots retire
// models: zen4 was dropped from between zen3 and zen5, and every push after that
// bump went red on the lines that named it — a 404 far from its cause, because
// "model not found" and "model hidden from you" reach the wire identically. What
// these tests are actually about is the SHAPE (a provider-qualified id proves the
// greedy wildcard, a single-segment one proves the ordinary route), so the shape
// is the argument and which id has it is the catalog's business.
//
// The read is the PUBLIC view on purpose: an id in it is one a normal org sees at
// baseline, which is what makes "disabled → 404 for a customer" and "beta → only
// acme sees it" mean anything. An id only an admin could see would satisfy both
// vacuously.
func modelFrom(t *testing.T, do doer, want func(id string) bool) string {
	t.Helper()
	_, body := do("GET", "/v1/pricing/models", "", nil)
	var wrap struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		t.Fatalf("decode /v1/pricing/models: %v", err)
	}
	for _, m := range wrap.Models {
		if id := modelID(m); id != "" && want(id) {
			return id
		}
	}
	t.Fatal("the mounted catalog serves no public model id of the shape this test needs")
	return ""
}

// TestCatalog_ServesBothRouteShapes pins the precondition every route-shape test
// below rests on: the mounted catalog publicly serves BOTH a provider-qualified
// id (which the greedy wildcard exists for) and a single-segment one (which the
// ordinary route serves). Those tests derive their ids by shape rather than
// naming one, so a snapshot that stopped carrying a shape would strand them in a
// 404 far from the cause — which is exactly what retiring zen4 did. This says it
// in one line, against the catalog itself.
func TestCatalog_ServesBothRouteShapes(t *testing.T) {
	do := mountEnablement(t)
	if id := modelFrom(t, do, slashed); !slashed(id) {
		t.Errorf("modelFrom(slashed) = %q, which is not provider-qualified", id)
	}
	if id := modelFrom(t, do, func(id string) bool { return !slashed(id) }); slashed(id) {
		t.Errorf("modelFrom(single-segment) = %q, which is provider-qualified", id)
	}
}

// TestAdminCatalog_HTTP drives the real subsystem over HTTP: it Mounts
// pricing on a zip app and exercises the admin write surface + the gated
// read path end-to-end. This verifies the load-bearing pieces the pure-gate
// unit tests can't: the greedy-wildcard route for slashed model ids, the
// IsAdmin gate, and the enable→customer-sees flow.
func TestAdminCatalog_HTTP(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	deps := cloud.Deps{Brand: "hanzo", DataDir: t.TempDir()}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	defer func() { _ = Shutdown(context.Background()) }()
	fa := app.Fiber()

	do := func(method, path, body string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		var req *http.Request
		if body != "" {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}

	admin := map[string]string{"X-User-IsAdmin": "true"}
	acme := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}
	other := map[string]string{"X-Org-Id": "other", "X-User-Id": "u_other"}

	// Both ids are READ from the catalog this test just mounted, before anything
	// below mutates it — never written as literals. See modelFrom: the sections
	// prove ROUTE SHAPES (greedy wildcard vs single segment, the visibility gate,
	// the over-deep 400) against whatever the catalog actually carries.
	slashID := modelFrom(t, do, slashed)
	single := modelFrom(t, do, func(id string) bool { return id != slashID && !slashed(id) })

	// --- gating of the admin surface itself ---------------------------------
	if resp, _ := do("PATCH", "/v1/admin/pricing/catalog/models/"+slashID, `{"enabled":false}`, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin PATCH must be 403, got %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/v1/admin/pricing/catalog", "", acme); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin GET /v1/admin/pricing/catalog must be 403, got %d", resp.StatusCode)
	}

	// --- admin disables a slashed-id model (verifies wildcard routing) ------
	resp, body := do("PATCH", "/v1/admin/pricing/catalog/models/"+slashID, `{"enabled":false}`, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin PATCH must be 200 (wildcard route), got %d: %s", resp.StatusCode, body)
	}
	var ov Overlay
	if err := json.Unmarshal(body, &ov); err != nil {
		t.Fatalf("decode overlay echo: %v", err)
	}
	if ov.ID != slashID || ov.Enabled {
		t.Errorf("overlay echo wrong: %+v", ov)
	}

	// --- the gate: disabled model hidden from customers, visible to admin ---
	if _, lb := do("GET", "/v1/pricing/models", "", acme); modelsContain(lb, slashID) {
		t.Errorf("disabled model leaked into public /v1/pricing/models")
	}
	if _, lab := do("GET", "/v1/pricing/models", "", admin); !modelsContain(lab, slashID) {
		t.Errorf("admin must still see the disabled model in /v1/pricing/models")
	}

	// --- per-customer beta: add acme, only acme sees it ---------------------
	if resp, _ := do("PATCH", "/v1/admin/pricing/catalog/models/"+slashID, `{"betaOrgs":["acme"]}`, admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin PATCH betaOrgs must be 200, got %d", resp.StatusCode)
	}
	if _, lbeta := do("GET", "/v1/pricing/models", "", acme); !modelsContain(lbeta, slashID) {
		t.Errorf("beta org acme must see the disabled-but-beta model")
	}
	if _, lother := do("GET", "/v1/pricing/models", "", other); modelsContain(lother, slashID) {
		t.Errorf("non-beta org must not see the beta model")
	}

	// --- FIX #2: the root /v1/pricing blob is gated like the leaves ---------
	// slashID is disabled with beta=[acme] at this point.
	_, rOther := do("GET", "/v1/pricing", "", other)
	if rootContainsModel(rOther, slashID) {
		t.Errorf("FIX#2: disabled model leaked into the root /v1/pricing for a non-beta org")
	}
	if rootFreeContains(rOther, slashID) {
		t.Errorf("FIX#2: disabled model id leaked into root freeModels for a non-beta org")
	}
	if _, rAcme := do("GET", "/v1/pricing", "", acme); !rootContainsModel(rAcme, slashID) {
		t.Errorf("FIX#2: beta org acme must see the beta model in the root blob")
	}
	if _, rAdmin := do("GET", "/v1/pricing", "", admin); !rootContainsModel(rAdmin, slashID) {
		t.Errorf("FIX#2: admin must see the disabled model in the root blob")
	}

	// --- FIX #5: oversized / over-deep overrides are rejected at the boundary
	deep := `{"betaOrgs":[],"overrides":` + strings.Repeat(`{"a":`, 40) + "1" + strings.Repeat("}", 40) + `}`
	if resp, _ := do("PATCH", "/v1/admin/pricing/catalog/models/"+single, deep, admin); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("FIX#5: over-deep override must be 400, got %d", resp.StatusCode)
	}

	// --- single-model gate (single-segment id) 404s without an oracle ------
	if resp, _ := do("PATCH", "/v1/admin/pricing/catalog/models/"+single, `{"enabled":false}`, admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin PATCH %s must be 200, got %d", single, resp.StatusCode)
	}
	if resp, _ := do("GET", "/v1/pricing/model/"+single, "", acme); resp.StatusCode != http.StatusNotFound {
		t.Errorf("disabled %s single-lookup must 404 for public, got %d", single, resp.StatusCode)
	}
	if resp, _ := do("GET", "/v1/pricing/model/"+single, "", admin); resp.StatusCode != http.StatusOK {
		t.Errorf("admin single-lookup of disabled %s must be 200, got %d", single, resp.StatusCode)
	}

	// --- admin catalog returns annotated entries ----------------------------
	resp, ab := do("GET", "/v1/admin/pricing/catalog", "", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin GET /v1/admin/pricing/catalog must be 200, got %d", resp.StatusCode)
	}
	var ac struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(ab, &ac); err != nil {
		t.Fatalf("decode admin catalog: %v", err)
	}
	m := catFind(ac.Models, slashID)
	if m == nil {
		t.Fatal("admin catalog missing the disabled model")
	}
	if _, ok := m["_overlay"].(map[string]any); !ok {
		t.Errorf("admin catalog model must carry _overlay state")
	}
}

func modelsContain(body []byte, id string) bool {
	var p struct {
		Models []Model `json:"models"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	return catHasID(p.Models, id)
}

// rootContainsModel reports whether the root /v1/pricing blob exposes a model id
// in either of its raw arrays.
func rootContainsModel(body []byte, id string) bool {
	var p struct {
		HanzoModels      []Model `json:"hanzoModels"`
		ThirdPartyModels []Model `json:"thirdPartyModels"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	return catHasID(p.HanzoModels, id) || catHasID(p.ThirdPartyModels, id)
}

func rootFreeContains(body []byte, id string) bool {
	var p struct {
		FreeModels []string `json:"freeModels"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	return slices.Contains(p.FreeModels, id)
}

// TestMount_EmptyDataDir_FailsClosed proves FIX #3: the overlay is a security
// control, so an empty DataDir is a hard boot error — never a silent in-memory
// (fail-open) downgrade that would re-expose admin-hidden models on restart.
func TestMount_EmptyDataDir_FailsClosed(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	defer func() { _ = Shutdown(context.Background()) }()
	if err := Mount(app, cloud.Deps{Brand: "hanzo", DataDir: ""}); err == nil {
		t.Fatal("Mount with empty DataDir must fail closed (got nil error)")
	}
}

// TestTheURLOutranksABodyID is what makes the model id safe to carry a wire name.
//
// The id binds from the greedy capture AND is a body field, because a typed op is
// reached off HTTP too — the call plane hands arguments across as the body with no
// path map, so a URL-only id would leave those callers unable to name a model. The
// cost of that is a second place a caller could put one, and the whole safety of it
// rests on which place wins: bindURL binds path LAST, so the address decides.
//
// Driven rather than argued, because the ordering is a property of the binder and
// binders change. A body id that could redirect the write would let a caller patch
// a model they did not address — which reads as success and moves the wrong row.
func TestTheURLOutranksABodyID(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{Brand: "hanzo", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	defer func() { _ = Shutdown(context.Background()) }()

	const addressed, decoy = "acme/addressed-model", "acme/decoy-model"
	req := httptest.NewRequest(http.MethodPatch,
		"/v1/admin/pricing/catalog/models/"+addressed,
		strings.NewReader(`{"id":"`+decoy+`","enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-IsAdmin", "true")

	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}
	var got Overlay
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode overlay: %v (%s)", err, body)
	}
	if got.ID != addressed {
		t.Fatalf("the write landed on %q, want %q — a body id outranked the address, so a caller "+
			"can patch a model they did not name", got.ID, addressed)
	}
}
