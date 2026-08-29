package audit

// Tests for the store-backed /v1/admin/audit + /v1/admin/audit/verify surface. They wire
// the audit domain against a REAL audit.Recorder (on-disk SQLite) seeded with records,
// drive requests through the whole zip app, and assert the query results, the integrity
// summary, and the SuperAdmin gate.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	auditstore "github.com/hanzoai/cloud/audit"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// mountWithStore builds a zip app with the audit routes wired to a real audit store, and
// returns the data dir, the store and a request helper. Only the audit routes are
// mounted here.
//
// The DATA DIR is returned because the trail is a FAMILY: verify enumerates every chain
// under it, so a test can write a sibling chain there and see whether the endpoint
// answers for it. That is the whole defect, so it has to be reachable from a test.
func mountWithStore(t *testing.T) (string, *auditstore.Recorder, func(method, path string, hdr map[string]string) (*http.Response, []byte)) {
	t.Helper()
	dir := t.TempDir()
	rec, err := auditstore.Open(dir, "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &cloud.Service[core.State]{State: core.State{AuditStore: rec}}
	// Stand in for the composer: the principal enrichment at the root, then the
	// typed ops — the order cloud.App gives every production program. A typed op
	// sees the caller only through what the enrichment parks, and a group at
	// /v1/admin would be a node of its own with no routes beneath it, which zip
	// refuses to compose.
	app.Use(cloud.Bridge())
	Routes(app, s)
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
	return dir, rec, do
}

func seedAudit(t *testing.T, rec *auditstore.Recorder, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		_, err := rec.Append(ctx, auditstore.Record{
			Time:     time.Now().UTC(),
			Actor:    auditstore.Actor{Org: "admin", Sub: "z@hanzo.ai"},
			Action:   "DELETE /v1/admin/orgs",
			Resource: auditstore.Resource{Type: "org", ID: "acme"},
			Auth:     auditstore.AuthContext{Method: "jwt", IsAdmin: true},
			Outcome:  auditstore.Outcome{Result: "success", Status: 200},
			Method:   "DELETE",
			Path:     "/v1/admin/orgs/acme",
		})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

var superAdmin = map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "z@hanzo.ai"}

// TestAdminAudit_ReturnsRealRecords proves GET /v1/admin/audit returns the store's
// records (newest-first) with an accurate total and an integrity summary.
func TestAdminAudit_ReturnsRealRecords(t *testing.T) {
	_, rec, do := mountWithStore(t)
	seedAudit(t, rec, 5)

	resp, body := do("GET", "/v1/admin/audit", superAdmin)
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
		Total     int `json:"total"`
		Integrity struct {
			Name    string `json:"name"`
			Verdict string `json:"verdict"`
			Count   uint64 `json:"count"`
		} `json:"integrity"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	if env.Total != 5 || len(env.Data) != 5 {
		t.Fatalf("got %d rows / total %d, want 5/5", len(env.Data), env.Total)
	}
	if env.Data[0].Seq < env.Data[len(env.Data)-1].Seq {
		t.Errorf("not newest-first: %d..%d", env.Data[0].Seq, env.Data[len(env.Data)-1].Seq)
	}
	if env.Data[0].Hash == "" {
		t.Error("row has no hash — chain linkage not surfaced")
	}
	if env.Integrity.Verdict != "intact" || env.Integrity.Count != 5 {
		t.Errorf("integrity summary = %+v, want intact/count=5", env.Integrity)
	}
	// The badge must NAME its chain. Without the name a console renders one chain's
	// verdict as the whole trail's, which is exactly the claim this listing cannot
	// make: it read one of the deployment's chains.
	if env.Integrity.Name != "audit" {
		t.Errorf("integrity names chain %q, want the chain these rows came from (\"audit\")", env.Integrity.Name)
	}
}

// TestAdminAudit_Filters proves the query filters (result) reach the store.
func TestAdminAudit_Filters(t *testing.T) {
	_, rec, do := mountWithStore(t)
	ctx := context.Background()
	// One deny among successes.
	_, _ = rec.Append(ctx, auditstore.Record{Action: "POST /v1/admin/roles", Actor: auditstore.Actor{Org: "admin"}, Outcome: auditstore.Outcome{Result: "deny", Status: 403}})
	seedAudit(t, rec, 3)

	resp, body := do("GET", "/v1/admin/audit?result=deny", superAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Total != 1 || len(env.Data) != 1 {
		t.Fatalf("result=deny returned %d/%d, want 1/1", len(env.Data), env.Total)
	}
	if env.Data[0]["result"] != "deny" {
		t.Errorf("filtered row result = %v, want deny", env.Data[0]["result"])
	}
}

// trailOf drives GET /v1/admin/audit/verify and decodes the trail.
func trailOf(t *testing.T, do func(string, string, map[string]string) (*http.Response, []byte)) auditstore.Trail {
	t.Helper()
	resp, body := do("GET", "/v1/admin/audit/verify", superAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data auditstore.Trail `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	return env.Data
}

// TestAdminAudit_VerifyEndpoint proves GET /v1/admin/audit/verify reports this
// process's own chain, with a verdict and a head to pin externally.
func TestAdminAudit_VerifyEndpoint(t *testing.T) {
	_, rec, do := mountWithStore(t)
	seedAudit(t, rec, 8)

	tr := trailOf(t, do)
	own, ok := chainNamed(tr, "audit")
	if !ok {
		t.Fatalf("verify did not report this process's own chain; got %+v", tr)
	}
	if own.Verdict != auditstore.Intact || own.Count != 8 || own.BrokenAt != -1 {
		t.Errorf("own chain = %+v, want intact/count=8/brokenAt=-1", own)
	}
	if own.Head == "" {
		t.Error("verify returned no head hash to pin against tail-truncation")
	}
	if tr.Records != 8 {
		t.Errorf("records = %d, want 8", tr.Records)
	}
	// The three verdicts must account for every chain — a chain that fell out of the
	// counts is a chain a reader cannot see it failed to check.
	if tr.Intact+tr.Broken+tr.Unread != len(tr.Chains) {
		t.Errorf("counts %d/%d/%d do not sum to %d chains", tr.Intact, tr.Broken, tr.Unread, len(tr.Chains))
	}
}

// TestAdminAudit_VerifyAnswersForEveryChain is the test the shipped defect was
// invisible to, and it is the reason this endpoint changed shape.
//
// A deployment holds ONE chain PER PROCESS (audit.Name), so /v1/admin/audit/verify —
// which runs inside the admin plugin — walked audit-admin.db and attached that one
// chain's boolean as THE TRAIL'S. Nothing anywhere enumerated the family: measured
// live, 128 chains and 1.7 GB, of which the surface read one. audit-iam.db (224 MB)
// and audit-tasks.db (374 MB) were never opened, and the answer was a clean bill of
// health for a trail that had not been looked at.
//
// Two sibling chains are written here, NEITHER of them the recorder the op holds. A
// reader that sees only its own chain reports one and fails on the first assertion.
func TestAdminAudit_VerifyAnswersForEveryChain(t *testing.T) {
	dir, rec, do := mountWithStore(t)
	seedAudit(t, rec, 8)
	writeChain(t, dir, "audit-iam", 3)
	writeChain(t, dir, "audit-tasks", 5)

	tr := trailOf(t, do)

	want := []string{"audit", "audit-iam", "audit-tasks"}
	got := make([]string, 0, len(tr.Chains))
	for _, ch := range tr.Chains {
		got = append(got, ch.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("verify reported %d chains %v, want all %d %v — the family is not being enumerated", len(got), got, len(want), want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("chain %d = %q, want %q (chains must come back in name order)", i, got[i], w)
		}
	}

	// Every chain must carry its OWN verdict. A set of results whose members are
	// indistinguishable is the single boolean again, wearing a list.
	for _, ch := range tr.Chains {
		if ch.Verdict == "" {
			t.Errorf("chain %q carries no verdict", ch.Name)
		}
	}
	if tr.Intact+tr.Broken+tr.Unread != len(tr.Chains) {
		t.Errorf("counts %d/%d/%d do not sum to %d chains", tr.Intact, tr.Broken, tr.Unread, len(tr.Chains))
	}
}

// chainNamed finds one chain's verdict in a trail.
func chainNamed(tr auditstore.Trail, name string) (auditstore.Integrity, bool) {
	for _, ch := range tr.Chains {
		if ch.Name == name {
			return ch, true
		}
	}
	return auditstore.Integrity{}, false
}

// writeChain creates a SIBLING chain beside the one under test and seeds it, then
// CLOSES it — modelling another process's chain at rest. It is closed because the
// pure-Go envelope keeps a handle-private copy and seals it on close, so leaving two
// handles open would make the fixture, not the code, decide what is on disk.
func writeChain(t *testing.T, dir, name string, n int) {
	t.Helper()
	sib, err := auditstore.Open(dir, name, nil)
	if err != nil {
		t.Fatalf("open sibling chain %s: %v", name, err)
	}
	seedAudit(t, sib, n)
	if err := sib.Close(); err != nil {
		t.Fatalf("close sibling chain %s: %v", name, err)
	}
}

// TestAdminAudit_DeniedWithoutSuperAdmin proves BOTH audit endpoints fail-closed 403 for
// a non-SuperAdmin, and — critically — the store is NEVER read on a denied request.
func TestAdminAudit_DeniedWithoutSuperAdmin(t *testing.T) {
	_, rec, do := mountWithStore(t)
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
			if len(body) > 0 && (bytes.Contains(body, []byte("DELETE /v1/admin/orgs")) || bytes.Contains(body, []byte(`"hash"`))) {
				t.Errorf("%s [%s]: denied response leaked audit data: %s", ep, tc.name, body)
			}
		}
	}
}

// TestAdminAudit_VerifyWithoutStore proves the nil-store verify endpoint reports "not
// configured" rather than panicking.
func TestAdminAudit_VerifyWithoutStore(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &cloud.Service[core.State]{State: core.State{}} // no auditStore
	app.Use(cloud.Bridge())
	Routes(app, s)
	req := httptest.NewRequest("GET", "/v1/admin/audit/verify", nil)
	for k, v := range superAdmin {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	// A well-formed error envelope, not a 500/panic.
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("not configured")) {
		t.Errorf("nil-store verify = %d %s, want an ok-envelope 'not configured' error", resp.StatusCode, body)
	}
}
