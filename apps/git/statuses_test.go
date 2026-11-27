package git

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

const shaA = "7dca5a5c406040cceafbaa147e0e27b3734ad576"

func readCombined(t *testing.T, b []byte) combinedStatus {
	t.Helper()
	var v combinedStatus
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("combined json: %v (%s)", err, b)
	}
	return v
}

// TestStatusesStateAndCombine: a commit's contexts are stated, restated in place,
// and read back as one word that is the worst of them; another org sees nothing.
func TestStatusesStateAndCombine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		pushTo(t, app, "acme", "code", "main", "README.md", "hi")
		url := "/v1/git/repos/code/commits/" + shaA + "/statuses"

		code, b := do(t, app, http.MethodGet, url, "acme", nil)
		if code != http.StatusOK || readCombined(t, b).State != StatePending || readCombined(t, b).Total != 0 {
			t.Fatalf("empty: %d %s", code, b)
		}
		for _, in := range []map[string]any{
			{"state": "success", "context": "ci/lint", "description": "ok"},
			{"state": "pending", "context": "ci/test"},
		} {
			if code, b = do(t, app, http.MethodPost, url, "acme", in); code != http.StatusCreated {
				t.Fatalf("post: %d %s", code, b)
			}
		}
		_, b = do(t, app, http.MethodGet, url, "acme", nil)
		if c := readCombined(t, b); c.State != StatePending || c.Total != 2 || c.Statuses[0].Context != "ci/lint" || c.Statuses[0].Creator != "u_acme" {
			t.Fatalf("two contexts: %s", b)
		}
		// Restating a context replaces its word and adds no row.
		do(t, app, http.MethodPost, url, "acme", map[string]any{"state": "failure", "context": "ci/test"})
		_, b = do(t, app, http.MethodGet, url, "acme", nil)
		if c := readCombined(t, b); c.State != StateFailure || c.Total != 2 {
			t.Fatalf("restated: %s", b)
		}
		do(t, app, http.MethodPost, url, "acme", map[string]any{"state": "success", "context": "ci/test"})
		_, b = do(t, app, http.MethodGet, url, "acme", nil)
		if c := readCombined(t, b); c.State != StateSuccess {
			t.Fatalf("all green: %s", b)
		}
		// Isolation, and refusals.
		pushTo(t, app, "other", "code", "main", "README.md", "hi")
		_, b = do(t, app, http.MethodGet, url, "other", nil)
		if c := readCombined(t, b); c.Total != 0 {
			t.Fatalf("another org read %s", b)
		}
		if code, _ = do(t, app, http.MethodPost, url, "acme", map[string]any{"state": "bogus"}); code != http.StatusBadRequest {
			t.Fatalf("bad state: %d", code)
		}
		if code, _ = do(t, app, http.MethodPost, "/v1/git/repos/code/commits/main/statuses", "acme", map[string]any{"state": "success"}); code != http.StatusBadRequest {
			t.Fatalf("short sha: %d", code)
		}
		if code, _ = do(t, app, http.MethodPost, "/v1/git/repos/nope/commits/"+shaA+"/statuses", "acme", map[string]any{"state": "success"}); code != http.StatusNotFound {
			t.Fatalf("missing repo: %d", code)
		}
		if code, _ = do(t, app, http.MethodPost, url, "acme", map[string]any{"state": "success", "context": strings.Repeat("x", 300)}); code != http.StatusBadRequest {
			t.Fatalf("long context: %d", code)
		}
	})
}

// TestRunEngineStatesItsJobs: opening a run states each job pending on the commit,
// and a runner's report moves that same status in the task's transaction.
func TestRunEngineStatesItsJobs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		pushTo(t, app, "acme", "code", "main", "README.md", "hi")
		st, err := storeFor(mounted.Load(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		r, fresh, err := st.Open(ctx, Run{Org: "acme", Repo: "code", Ref: "refs/heads/main", Commit: shaA,
			Event: "push", Workflow: "ci", Doc: []byte("x")}, []Job{{Name: "test", Pool: "p"}})
		if err != nil || !fresh {
			t.Fatalf("open: %v %v", fresh, err)
		}
		want := "ci / test (push)"
		rows, _ := st.Statuses(ctx, "acme", "", "code", shaA)
		if len(rows) != 1 || rows[0].Context != want || rows[0].State != StatePending || rows[0].Creator != "run" {
			t.Fatalf("after open: %+v", rows)
		}
		tasks, _ := st.Tasks(ctx, r.ID)
		if _, err := st.Report(ctx, tasks[0].ID, Failure, 1, ""); err != nil {
			t.Fatal(err)
		}
		rows, _ = st.Statuses(ctx, "acme", "", "code", shaA)
		if rows[0].State != StateFailure || combined(rows) != StateFailure {
			t.Fatalf("after report: %+v", rows)
		}
		// A redelivered trigger opens nothing and states nothing again.
		if _, fresh, _ = st.Open(ctx, Run{Org: "acme", Repo: "code", Commit: shaA, Workflow: "ci", Event: "push", Doc: []byte("x")}, nil); fresh {
			t.Fatal("redelivery opened a second run")
		}
	})
}
