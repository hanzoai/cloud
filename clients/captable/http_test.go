package captable

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// mountApp builds a bare zip.App (no SanitizeIdentity middleware, so X-Org-Id +
// X-User-Id are trusted verbatim — the standard cloud leaf test harness) and
// mounts the captable leaf on it. This exercises the REAL HTTP path: routing →
// body decode → principal gate → NewBase dispatch → per-tenant Base → response,
// the same path the live binary serves under CLOUD_ENABLE=captable.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })
	return app
}

func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
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
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org) // validated principal (principal.Org gates on it)
	}
	// A generous timeout: fiber's 1s default is too tight for the in-memory harness
	// under an occasional GC / cold-sqlite pause (the goja dispatch does real DB
	// work), which would flake this suite.
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestHTTPEndToEnd is the wire proof of the full captable fold over Base: the
// principal gate, stakeholder create→read-back, security issuance reflected in
// the computed cap table, the bundle's OWN 404 (not a proxy 502), and
// cross-tenant isolation.
func TestHTTPEndToEnd(t *testing.T) {
	app := mountApp(t)

	// No validated principal → 403 (the tenant gate).
	if code, _ := req(t, app, http.MethodGet, "/v1/captable/stakeholders", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org GET want 403, got %d", code)
	}

	// A missing round → the BUNDLE's own 404 JSON (a proxy would 502).
	code, body := req(t, app, http.MethodGet, "/v1/captable/rounds/does-not-exist", "acme", nil)
	if code != http.StatusNotFound {
		t.Fatalf("missing round want 404, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), "round not found") {
		t.Fatalf("expected the bundle's own 404 body, got %s", body)
	}

	// Create a stakeholder, then read it back.
	code, body = req(t, app, http.MethodPost, "/v1/captable/stakeholders", "acme", map[string]any{
		"name": "Ada Lovelace", "email": "ada@example.com",
		"stakeholderType": "INDIVIDUAL", "currentRelationship": "FOUNDER",
	})
	if code != http.StatusCreated {
		t.Fatalf("create stakeholder want 201, got %d (%s)", code, body)
	}
	code, body = req(t, app, http.MethodGet, "/v1/captable/stakeholders", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list stakeholders want 200, got %d", code)
	}
	var stakeholders []map[string]any
	if err := json.Unmarshal(body, &stakeholders); err != nil || len(stakeholders) != 1 {
		t.Fatalf("read-back mismatch: %v (%s)", err, body)
	}
	if stakeholders[0]["email"] != "ada@example.com" {
		t.Fatalf("read-back wrong stakeholder: %s", body)
	}
	founderID := stakeholders[0]["id"].(string)

	// Issue a share class + 1,000,000 shares.
	code, body = req(t, app, http.MethodPost, "/v1/captable/share-classes", "acme", map[string]any{
		"name": "Common", "classType": "COMMON", "initialSharesAuthorized": 10000000,
		"boardApprovalDate": "2026-01-01", "stockholderApprovalDate": "2026-01-02",
		"votesPerShare": 1, "parValue": 0.0001, "pricePerShare": 0.0001, "seniority": 0,
		"conversionRights": "CONVERTS_TO_FUTURE_ROUND", "convertsToShareClassId": nil,
		"liquidationPreferenceMultiple": 1, "participationCapMultiple": 0,
	})
	if code != http.StatusCreated {
		t.Fatalf("create share class want 201, got %d (%s)", code, body)
	}
	code, body = req(t, app, http.MethodGet, "/v1/captable/share-classes", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list share classes: %d", code)
	}
	var classes []map[string]any
	_ = json.Unmarshal(body, &classes)
	shareClassID := classes[0]["id"].(string)

	code, body = req(t, app, http.MethodPost, "/v1/captable/shares", "acme", map[string]any{
		"stakeholderId": founderID, "shareClassId": shareClassID, "certificateId": "CS-1",
		"quantity": 1000000, "status": "ACTIVE", "issueDate": "2026-01-03",
		"boardApprovalDate": "2026-01-03", "cliffYears": 0, "vestingYears": 4,
	})
	if code != http.StatusCreated {
		t.Fatalf("issue share want 201, got %d (%s)", code, body)
	}

	// The computed cap table must reflect the issuance.
	code, body = req(t, app, http.MethodGet, "/v1/captable/summary", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("cap table want 200, got %d", code)
	}
	var ct map[string]any
	_ = json.Unmarshal(body, &ct)
	if ct["totals"].(map[string]any)["outstandingShares"].(float64) != 1000000 {
		t.Fatalf("cap table did not reflect issuance: %s", body)
	}
	if pct := ct["byStakeholder"].([]any)[0].(map[string]any)["ownershipPct"].(float64); pct != 100 {
		t.Fatalf("founder ownership want 100%%, got %v", pct)
	}

	// Cross-tenant isolation: globex sees none of acme's stakeholders.
	code, body = req(t, app, http.MethodGet, "/v1/captable/stakeholders", "globex", nil)
	if code != http.StatusOK {
		t.Fatalf("globex list: %d", code)
	}
	var other []map[string]any
	_ = json.Unmarshal(body, &other)
	if len(other) != 0 {
		t.Fatalf("cross-tenant leak: globex saw %d rows", len(other))
	}
}
