package pricing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// TestAdminCatalog_HTTP drives the real subsystem over HTTP: it Mounts
// pricingsvc on a zip app and exercises the admin write surface + the gated
// read path end-to-end. This verifies the load-bearing pieces the pure-gate
// unit tests can't: the greedy-wildcard route for slashed model ids, the
// IsAdmin gate, and the enable→customer-sees flow.
func TestAdminCatalog_HTTP(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo", DataDir: t.TempDir()}
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

	const slashID = "anthropic/claude-opus-4.6"
	admin := map[string]string{"X-User-IsAdmin": "true"}
	acme := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}
	other := map[string]string{"X-Org-Id": "other", "X-User-Id": "u_other"}

	// --- gating of the admin surface itself ---------------------------------
	if resp, _ := do("PATCH", "/v1/admin/catalog/models/"+slashID, `{"enabled":false}`, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin PATCH must be 403, got %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/v1/admin/catalog", "", acme); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin GET /v1/admin/catalog must be 403, got %d", resp.StatusCode)
	}

	// --- admin disables a slashed-id model (verifies wildcard routing) ------
	resp, body := do("PATCH", "/v1/admin/catalog/models/"+slashID, `{"enabled":false}`, admin)
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
	if resp, _ := do("PATCH", "/v1/admin/catalog/models/"+slashID, `{"betaOrgs":["acme"]}`, admin); resp.StatusCode != http.StatusOK {
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
	if resp, _ := do("PATCH", "/v1/admin/catalog/models/zen5", deep, admin); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("FIX#5: over-deep override must be 400, got %d", resp.StatusCode)
	}

	// --- single-model gate (single-segment id) 404s without an oracle ------
	if resp, _ := do("PATCH", "/v1/admin/catalog/models/zen4", `{"enabled":false}`, admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin PATCH zen4 must be 200, got %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/v1/pricing/model/zen4", "", acme); resp.StatusCode != http.StatusNotFound {
		t.Errorf("disabled zen4 single-lookup must 404 for public, got %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/v1/pricing/model/zen4", "", admin); resp.StatusCode != http.StatusOK {
		t.Errorf("admin single-lookup of disabled zen4 must be 200, got %d", resp.StatusCode)
	}

	// --- admin catalog returns annotated entries ----------------------------
	resp, ab := do("GET", "/v1/admin/catalog", "", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin GET /v1/admin/catalog must be 200, got %d", resp.StatusCode)
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
	for _, s := range p.FreeModels {
		if s == id {
			return true
		}
	}
	return false
}

// TestMount_EmptyDataDir_FailsClosed proves FIX #3: the overlay is a security
// control, so an empty DataDir is a hard boot error — never a silent in-memory
// (fail-open) downgrade that would re-expose admin-hidden models on restart.
func TestMount_EmptyDataDir_FailsClosed(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	defer func() { _ = Shutdown(context.Background()) }()
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo", DataDir: ""}); err == nil {
		t.Fatal("Mount with empty DataDir must fail closed (got nil error)")
	}
}
