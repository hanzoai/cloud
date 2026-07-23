package guide

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// reqH issues a request with explicit gateway-minted identity headers — the escape
// hatch req() (org-only) doesn't cover: a SuperAdmin (X-User-IsAdmin) or an org admin
// (X-User-IsOrgAdmin). SanitizeIdentity mints these server-side; a test sets them.
func reqH(t *testing.T, app *zip.App, method, path string, headers map[string]string, body any) httpResult {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		rq.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Body: b}
}

// superHdr is a platform SuperAdmin identity (member of the reserved admin org — the ONE
// cross-tenant predicate). orgAdminHdr is a per-org admin: validated + IsOrgAdmin but
// NOT SuperAdmin — the privilege-escalation case the blueprint plane must refuse.
var (
	superHdr    = map[string]string{"X-Org-Id": "admin", "X-User-Id": "admin/z", "X-User-IsAdmin": "true", "X-User-Owner": "admin"}
	orgAdminHdr = map[string]string{"X-Org-Id": "acme", "X-User-Id": "acme/u", "X-User-IsOrgAdmin": "true"}
)

// TestBlueprintAdminRequiresSuperAdmin is the load-bearing authority proof: every
// blueprint route is SuperAdmin-only. A validated non-admin, a per-org admin
// (IsOrgAdmin), and an anonymous request are ALL 403; only a SuperAdmin passes.
// Trusting per-org isAdmin here would be a privilege escalation.
func TestBlueprintAdminRequiresSuperAdmin(t *testing.T) {
	app := newApp(t)

	patchBody := map[string]any{"enabled": false}
	putBody := map[string]any{"version": "x", "steps": []map[string]any{{"id": "a", "title": "A"}}}

	// Reads and writes across the whole plane.
	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/guide/blueprint", nil},
		{http.MethodGet, "/v1/guide/blueprint/versions", nil},
		{http.MethodPut, "/v1/guide/blueprint", putBody},
		{http.MethodPatch, "/v1/guide/blueprint/steps/incorporate", patchBody},
	}
	for _, rt := range routes {
		// Anonymous (no principal) → 403.
		if r := reqH(t, app, rt.method, rt.path, nil, rt.body); r.Code != http.StatusForbidden {
			t.Fatalf("anon %s %s want 403, got %d", rt.method, rt.path, r.Code)
		}
		// Validated NON-admin org → 403.
		if r := req(t, app, rt.method, rt.path, "acme", rt.body); r.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s %s want 403, got %d (%s)", rt.method, rt.path, r.Code, r.Body)
		}
		// Per-org admin (IsOrgAdmin, NOT SuperAdmin) → 403 — the privilege-escalation guard.
		if r := reqH(t, app, rt.method, rt.path, orgAdminHdr, rt.body); r.Code != http.StatusForbidden {
			t.Fatalf("org-admin %s %s must be refused (not SuperAdmin), got %d", rt.method, rt.path, r.Code)
		}
	}

	// A SuperAdmin passes the gate (GET the seeded base blueprint).
	r := reqH(t, app, http.MethodGet, "/v1/guide/blueprint", superHdr, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("SuperAdmin GET blueprint want 200, got %d (%s)", r.Code, r.Body)
	}
	var got struct {
		Version int    `json:"version"`
		Brand   string `json:"brand"`
		Counts  struct {
			Steps int `json:"steps"`
		} `json:"counts"`
	}
	_ = json.Unmarshal(r.Body, &got)
	if got.Version != 1 || got.Counts.Steps != 67 {
		t.Fatalf("SuperAdmin should read the seeded base blueprint (v1, 67 steps), got %+v", got)
	}
}

// TestBlueprintPatchDisableDropsFromJourney proves an admin disable takes effect LIVE:
// after a SuperAdmin PATCHes a step disabled, the org-facing journey (a normal org's
// GET /v1/guide) drops it on the very next resolve.
func TestBlueprintPatchDisableDropsFromJourney(t *testing.T) {
	app := newApp(t)

	// Baseline: a normal org sees gsuite in the journey.
	before := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if !hasStep(before, "gsuite") {
		t.Fatal("baseline journey must contain gsuite")
	}

	// SuperAdmin disables gsuite (the headline lever — PATCH {enabled:false}).
	r := reqH(t, app, http.MethodPatch, "/v1/guide/blueprint/steps/gsuite", superHdr, map[string]any{"enabled": false})
	if r.Code != http.StatusOK {
		t.Fatalf("SuperAdmin disable gsuite want 200, got %d (%s)", r.Code, r.Body)
	}

	// The next resolve for a normal org drops gsuite (and its dependents lose the edge).
	after := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if hasStep(after, "gsuite") {
		t.Fatal("a disabled step must drop from the journey on the next resolve")
	}
	if len(after.Steps) != len(before.Steps)-1 {
		t.Fatalf("exactly gsuite should drop: before=%d after=%d", len(before.Steps), len(after.Steps))
	}
	// password-manager depended only on gsuite → the edge is dropped, it is now available.
	for _, s := range after.Steps {
		if s.ID == "password-manager" && !s.Available {
			t.Fatal("password-manager should be available after its only blocker (gsuite) is disabled")
		}
	}
}

// TestBlueprintPutVersioningAndFailClosed proves the versioning discipline + fail-closed
// authoring: a valid PUT appends a version; a cyclic PUT is 422 and never becomes active.
func TestBlueprintPutVersioningAndFailClosed(t *testing.T) {
	app := newApp(t)

	valid := map[string]any{"version": "v2-blueprint", "steps": []map[string]any{
		{"id": "one", "title": "One"},
		{"id": "two", "title": "Two", "deps": []string{"one"}},
	}}
	r := reqH(t, app, http.MethodPut, "/v1/guide/blueprint", superHdr, valid)
	if r.Code != http.StatusOK {
		t.Fatalf("SuperAdmin PUT want 200, got %d (%s)", r.Code, r.Body)
	}
	var put struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(r.Body, &put)
	if put.Version != 2 {
		t.Fatalf("PUT must append version 2 (seed is 1), got %d", put.Version)
	}

	// A normal org now resolves the new brand blueprint.
	ov := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if ov.Version != "v2-blueprint" || len(ov.Steps) != 2 {
		t.Fatalf("PUT must take effect on the next org resolve, got version=%q steps=%d", ov.Version, len(ov.Steps))
	}

	// A cyclic PUT is rejected 422 and does NOT corrupt the active blueprint.
	bad := map[string]any{"version": "bad", "steps": []map[string]any{
		{"id": "a", "title": "A", "deps": []string{"b"}},
		{"id": "b", "title": "B", "deps": []string{"a"}},
	}}
	if r := reqH(t, app, http.MethodPut, "/v1/guide/blueprint", superHdr, bad); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cyclic PUT want 422, got %d (%s)", r.Code, r.Body)
	}
	ov = decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if ov.Version != "v2-blueprint" {
		t.Fatalf("a rejected PUT must not corrupt the active blueprint, got %q", ov.Version)
	}

	// The version history lists both versions, newest first.
	vr := reqH(t, app, http.MethodGet, "/v1/guide/blueprint/versions", superHdr, nil)
	var vh struct {
		Versions []VersionMeta `json:"versions"`
	}
	_ = json.Unmarshal(vr.Body, &vh)
	if len(vh.Versions) != 2 || vh.Versions[0].Version != 2 || vh.Versions[1].Version != 1 {
		t.Fatalf("version history want [2,1], got %+v", vh.Versions)
	}
}

// TestBlueprintPatchUnknownItem: a patch to a missing item is 404; an unknown collection
// is 400.
func TestBlueprintPatchUnknownItem(t *testing.T) {
	app := newApp(t)
	if r := reqH(t, app, http.MethodPatch, "/v1/guide/blueprint/steps/ghost", superHdr, map[string]any{"enabled": false}); r.Code != http.StatusNotFound {
		t.Fatalf("patch missing step want 404, got %d", r.Code)
	}
	if r := reqH(t, app, http.MethodPatch, "/v1/guide/blueprint/widgets/x", superHdr, map[string]any{"enabled": false}); r.Code != http.StatusBadRequest {
		t.Fatalf("patch unknown collection want 400, got %d", r.Code)
	}
}

func hasStep(v overviewView, id string) bool {
	for _, s := range v.Steps {
		if s.ID == id {
			return true
		}
	}
	return false
}
