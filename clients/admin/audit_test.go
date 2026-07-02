package admin

// Tests for the store-backed /v1/admin/audit + /v1/admin/audit/verify surface.
// They wire admin against a REAL audit.Recorder (on-disk SQLite) seeded with
// records, drive requests through the whole zip app, and assert the query
// results, the integrity summary, and the global-admin gate.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// mountWithStore builds a zip app with admin's audit routes wired to a real audit
// store, and returns the store + a request helper. Only the audit routes are
// mounted here (the rest are covered by mount()); this keeps the store-backed
// tests focused.
func mountWithStore(t *testing.T) (*audit.Recorder, func(method, path string, hdr map[string]string) (*http.Response, []byte)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := audit.Open(path, nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &svc{adminOrg: "admin", auditStore: rec}
	app.Get("/v1/admin/audit", s.guard(s.audit))
	app.Get("/v1/admin/audit/verify", s.guard(s.auditVerify))
	fa := app.Fiber()

	do := func(method, p string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, p, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s %s: %v", method, p, err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	return rec, do
}

func seedAudit(t *testing.T, rec *audit.Recorder, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		_, err := rec.Append(ctx, audit.Record{
			Time:     time.Now().UTC(),
			Actor:    audit.Actor{Org: "admin", Sub: "z@hanzo.ai"},
			Action:   "DELETE /v1/admin/orgs",
			Resource: audit.Resource{Type: "org", ID: "acme"},
			Auth:     audit.AuthContext{Method: "jwt", IsAdmin: true},
			Outcome:  audit.Outcome{Result: "success", Status: 200},
			Method:   "DELETE",
			Path:     "/v1/admin/orgs/acme",
		})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

var globalAdmin = map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "z@hanzo.ai"}

// TestAdminAudit_ReturnsRealRecords proves GET /v1/admin/audit returns the
// store's records (newest-first) with an accurate total and an integrity summary.
func TestAdminAudit_ReturnsRealRecords(t *testing.T) {
	rec, do := mountWithStore(t)
	seedAudit(t, rec, 5)

	resp, body := do("GET", "/v1/admin/audit", globalAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data []struct {
			Seq    uint64 `json:"seq"`
			Action string `json:"action"`
			Hash   string `json:"hash"`
			Result string `json:"result"`
		} `json:"data"`
		Data2     int `json:"data2"`
		Integrity struct {
			OK    bool   `json:"ok"`
			Count uint64 `json:"count"`
		} `json:"integrity"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	if env.Data2 != 5 || len(env.Data) != 5 {
		t.Fatalf("got %d rows / total %d, want 5/5", len(env.Data), env.Data2)
	}
	if env.Data[0].Seq < env.Data[len(env.Data)-1].Seq {
		t.Errorf("not newest-first: %d..%d", env.Data[0].Seq, env.Data[len(env.Data)-1].Seq)
	}
	if env.Data[0].Hash == "" {
		t.Error("row has no hash — chain linkage not surfaced")
	}
	if !env.Integrity.OK || env.Integrity.Count != 5 {
		t.Errorf("integrity summary = %+v, want ok/count=5", env.Integrity)
	}
}

// TestAdminAudit_Filters proves the query filters (result) reach the store.
func TestAdminAudit_Filters(t *testing.T) {
	rec, do := mountWithStore(t)
	ctx := context.Background()
	// One deny among successes.
	_, _ = rec.Append(ctx, audit.Record{Action: "POST /v1/admin/roles", Actor: audit.Actor{Org: "admin"}, Outcome: audit.Outcome{Result: "deny", Status: 403}})
	seedAudit(t, rec, 3)

	resp, body := do("GET", "/v1/admin/audit?result=deny", globalAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data  []map[string]any `json:"data"`
		Data2 int              `json:"data2"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Data2 != 1 || len(env.Data) != 1 {
		t.Fatalf("result=deny returned %d/%d, want 1/1", len(env.Data), env.Data2)
	}
	if env.Data[0]["result"] != "deny" {
		t.Errorf("filtered row result = %v, want deny", env.Data[0]["result"])
	}
}

// TestAdminAudit_VerifyEndpoint proves GET /v1/admin/audit/verify returns the
// integrity result for the chain.
func TestAdminAudit_VerifyEndpoint(t *testing.T) {
	rec, do := mountWithStore(t)
	seedAudit(t, rec, 8)

	resp, body := do("GET", "/v1/admin/audit/verify", globalAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data struct {
			OK       bool   `json:"ok"`
			Count    uint64 `json:"count"`
			BrokenAt int64  `json:"brokenAt"`
			HeadHash string `json:"headHash"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	if !env.Data.OK || env.Data.Count != 8 || env.Data.BrokenAt != -1 {
		t.Errorf("verify result = %+v, want ok/count=8/brokenAt=-1", env.Data)
	}
	if env.Data.HeadHash == "" {
		t.Error("verify returned no head hash")
	}
}

// TestAdminAudit_DeniedWithoutGlobalAdmin proves BOTH audit endpoints fail-closed
// 403 for a non-global-admin, and — critically — the store is NEVER read on a
// denied request (the gate runs before the handler, so no records leak to an
// unauthorized caller). We assert non-leakage by seeding records and confirming
// the denied response body contains none of them.
func TestAdminAudit_DeniedWithoutGlobalAdmin(t *testing.T) {
	rec, do := mountWithStore(t)
	seedAudit(t, rec, 3)

	cases := []struct {
		name string
		hdr  map[string]string
	}{
		{"no identity", map[string]string{}},
		{"tenant admin (org != adminOrg, no minted IsAdmin)", map[string]string{"X-Org-Id": "acme", "X-User-Id": "mallory"}},
		{"forged-looking but non-admin", map[string]string{"X-Org-Id": "acme"}},
	}
	for _, ep := range []string{"/v1/admin/audit", "/v1/admin/audit/verify"} {
		for _, tc := range cases {
			resp, body := do("GET", ep, tc.hdr)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s [%s]: got %d, want 403 (body=%s)", ep, tc.name, resp.StatusCode, body)
			}
			// No record content must appear in a denied response.
			if len(body) > 0 && (contains(body, "DELETE /v1/admin/orgs") || contains(body, `"hash"`)) {
				t.Errorf("%s [%s]: denied response leaked audit data: %s", ep, tc.name, body)
			}
		}
	}
}

// TestAdminAudit_FallsBackToIAMWhenNoStore proves that when no local store is
// configured (auditStore == nil), /v1/admin/audit still serves the federated IAM
// view rather than erroring — preserving the prior capability. Covered by the
// existing TestAudit_MapsRecords (IAM proxy path); here we assert the nil-store
// verify endpoint reports "not configured" rather than panicking.
func TestAdminAudit_VerifyWithoutStore(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &svc{adminOrg: "admin"} // no auditStore
	app.Get("/v1/admin/audit/verify", s.guard(s.auditVerify))
	req := httptest.NewRequest("GET", "/v1/admin/audit/verify", nil)
	for k, v := range globalAdmin {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	// A well-formed error envelope, not a 500/panic.
	if resp.StatusCode != http.StatusOK || !contains(body, "not configured") {
		t.Errorf("nil-store verify = %d %s, want an ok-envelope 'not configured' error", resp.StatusCode, body)
	}
}

func contains(b []byte, sub string) bool {
	s := string(b)
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
