package auditlog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// newStore opens an in-memory audit recorder (single connection keeps :memory:
// alive across queries) and seeds it with records across two orgs.
func newStore(t *testing.T) *audit.Recorder {
	t.Helper()
	rec, err := audit.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	seed := []audit.Record{
		{Actor: audit.Actor{Org: "maxpower", Sub: "dave"}, Action: "machine.create", Resource: audit.Resource{Type: "machine", ID: "m-1"}, Outcome: audit.Outcome{Result: "success", Status: 201}},
		{Actor: audit.Actor{Org: "maxpower", Sub: "dave"}, Action: "machine.delete", Resource: audit.Resource{Type: "machine", ID: "m-1"}, Outcome: audit.Outcome{Result: "success", Status: 204}},
		{Actor: audit.Actor{Org: "maxpower", Sub: "eve"}, Action: "key.create", Resource: audit.Resource{Type: "apikey", ID: "k-9"}, Outcome: audit.Outcome{Result: "deny", Status: 403}},
		{Actor: audit.Actor{Org: "victim", Sub: "mallory"}, Action: "machine.create", Resource: audit.Resource{Type: "machine", ID: "m-2"}, Outcome: audit.Outcome{Result: "success", Status: 201}},
	}
	for i := range seed {
		if _, err := rec.Append(context.Background(), seed[i]); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	return rec
}

func mountApp(t *testing.T, store *audit.Recorder) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Audit: store}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func call(t *testing.T, app *zip.App, path, user, org string) (int, []audit.Wire, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return resp.StatusCode, nil, 0
	}
	var env struct {
		Data  []audit.Wire `json:"data"`
		Data2 int          `json:"data2"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, b)
	}
	return resp.StatusCode, env.Data, env.Data2
}

func TestList_ScopedToCallerOrg(t *testing.T) {
	app := mountApp(t, newStore(t))
	code, rows, total := call(t, app, "/v1/audit", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	// maxpower has 3 records; victim's 1 must NEVER appear.
	if total != 3 || len(rows) != 3 {
		t.Fatalf("scope: want 3 rows/total, got %d rows / total %d", len(rows), total)
	}
	for _, r := range rows {
		if r.Org != "maxpower" {
			t.Fatalf("cross-tenant leak: saw org %q", r.Org)
		}
		if r.Hash == "" {
			t.Fatalf("row must carry its chain hash (tamper-evidence), seq %d", r.Seq)
		}
	}
}

func TestList_CannotWidenScopeViaOrgParam(t *testing.T) {
	app := mountApp(t, newStore(t))
	// A forged ?org=victim must be IGNORED — the pinned org is the caller's.
	code, rows, _ := call(t, app, "/v1/audit?org=victim", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	for _, r := range rows {
		if r.Org != "maxpower" {
			t.Fatalf("client widened scope via ?org: saw %q", r.Org)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("want the caller's own 3 rows, got %d", len(rows))
	}
}

func TestList_ActionFilter(t *testing.T) {
	app := mountApp(t, newStore(t))
	code, rows, total := call(t, app, "/v1/audit?action=machine.create", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	// Only maxpower's ONE machine.create (victim's is a different org, excluded by scope).
	if total != 1 || len(rows) != 1 || rows[0].Action != "machine.create" {
		t.Fatalf("action filter: want 1 machine.create, got %d rows total %d", len(rows), total)
	}
}

func TestList_ResourceIDFilter(t *testing.T) {
	app := mountApp(t, newStore(t))
	code, rows, total := call(t, app, "/v1/audit?resource=machine&resourceId=m-1", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	// Both maxpower machine events on m-1 (create + delete); m-2 is victim's, out of scope.
	if total != 2 || len(rows) != 2 {
		t.Fatalf("resourceId filter: want 2 events on m-1, got %d rows total %d", len(rows), total)
	}
	for _, r := range rows {
		if r.ResourceID != "m-1" {
			t.Fatalf("resourceId filter leaked %q", r.ResourceID)
		}
	}
}

func TestList_ResultFilter(t *testing.T) {
	app := mountApp(t, newStore(t))
	code, rows, total := call(t, app, "/v1/audit?result=deny", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	if total != 1 || len(rows) != 1 || rows[0].Result != "deny" {
		t.Fatalf("result filter: want 1 deny, got %d rows total %d", len(rows), total)
	}
}

func TestList_Pagination(t *testing.T) {
	app := mountApp(t, newStore(t))
	// pageSize=2, page 1 → 2 rows, total still 3.
	code, rows, total := call(t, app, "/v1/audit?pageSize=2&p=1", "maxpower/dave", "maxpower")
	if code != 200 || len(rows) != 2 || total != 3 {
		t.Fatalf("page1: want 2 rows / total 3, got %d rows total %d", len(rows), total)
	}
	// page 2 → the remaining 1 row.
	code, rows, total = call(t, app, "/v1/audit?pageSize=2&p=2", "maxpower/dave", "maxpower")
	if code != 200 || len(rows) != 1 || total != 3 {
		t.Fatalf("page2: want 1 row / total 3, got %d rows total %d", len(rows), total)
	}
}

func TestList_NoValidatedPrincipal_401(t *testing.T) {
	app := mountApp(t, newStore(t))
	code, _, _ := call(t, app, "/v1/audit", "", "victim")
	if code != http.StatusUnauthorized {
		t.Fatalf("no principal: want 401, got %d", code)
	}
}

func TestList_NoStore_501(t *testing.T) {
	app := mountApp(t, nil) // deps.Audit nil → store not configured
	code, _, _ := call(t, app, "/v1/audit", "maxpower/dave", "maxpower")
	if code != http.StatusNotImplemented {
		t.Fatalf("no store: want 501, got %d", code)
	}
}
