package tracker

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
)

// ONE CORPUS. A board filled by a repository and a board filled by an agent are
// the same kind of thing, so every read on this surface answers for both.
//
// Measured on production before this held: /v1/tracker/projects listed 745
// repo-derived boards carrying 3 rows between them, while the org's actual
// roadmap — 15 rows under two project ids, assigned to named agents — was
// reachable only through /v1/tracker/issues. The board UI reads the first, so
// the work nobody could see was the only work that mattered. Worse, the ids that
// search DID return were not addresses: GET /projects/prj_b4b4bd4c… answered
// "no such project", so there was no way to get from a search hit to a board.
//
// Every assertion below fails if either source is dropped from either read.
func TestOneCorpus_TheIndexIsOnTheSameBoardAsTheForge(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"acme"}
	f.repo("acme", "api", issue(1, "api work", "open", "todo"))
	app := mountForge(t, f)

	// A board the forge has never heard of: an agent's own roadmap, filed into
	// the index exactly as the mirror and the plane upsert file into it.
	st, err := storeFor(mounted, "acme", principal.DefaultProject)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	ctx, now := context.Background(), time.Now().Unix()
	board := Project{ID: "prj_b4b4bd4c", Org: "acme", Key: "OPS", Name: "Roadmap", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateProject(ctx, board); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateIssue(ctx, Issue{
		ID: "iss_track0", ProjectID: board.ID, Org: "acme", Kind: "epic", Source: "team",
		Title: "Track 0 — ACCESS", Status: "todo", Priority: "high", Assignee: "neo",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	get := func(path string) []map[string]any {
		t.Helper()
		code, raw := asUser(t, app, http.MethodGet, path, "acme", "alice", nil)
		if code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, code, raw)
		}
		var rows []map[string]any
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, raw)
		}
		return rows
	}
	field := func(rows []map[string]any, k string) []string {
		out := []string{}
		for _, r := range rows {
			if v, ok := r[k].(string); ok {
				out = append(out, v)
			}
		}
		sort.Strings(out)
		return out
	}
	has := func(vals []string, want string) bool {
		for _, v := range vals {
			if v == want {
				return true
			}
		}
		return false
	}

	// THE BOARD LIST names both. A board is a place work is, whichever source put
	// the work there.
	keys := field(get("/v1/tracker/projects"), "key")
	if !has(keys, "api") || !has(keys, "OPS") {
		t.Errorf("board list = %v, want both the repository board and the index board", keys)
	}

	// THE GLOBAL BOARD carries both. This is the read the product's Todo view
	// makes, and the one that showed only bot pull requests.
	titles := field(get("/v1/tracker/board"), "title")
	if !has(titles, "api work") || !has(titles, "Track 0 — ACCESS") {
		t.Errorf("global board = %v, want work from both sources", titles)
	}

	// THE INDEX BOARD IS ADDRESSABLE — by the same URL shape as a repository
	// board, because a caller must not have to know which store a key lives in.
	code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects/OPS", "acme", "alice", nil)
	if code != http.StatusOK {
		t.Errorf("GET the index board = %d %s, want 200 — an unaddressable board is invisible to the UI", code, raw)
	}
	if got := field(get("/v1/tracker/projects/OPS/issues"), "title"); !has(got, "Track 0 — ACCESS") {
		t.Errorf("index board issues = %v, want the epic filed on it", got)
	}
	// …and the key filter still EXCLUDES the other source's rows, or "one corpus"
	// would just mean every board shows everything.
	if got := field(get("/v1/tracker/projects/api/issues"), "title"); has(got, "Track 0 — ACCESS") {
		t.Errorf("repository board = %v, want only its own work", got)
	}

	// SOURCE IS A REAL FILTER now that rows genuinely differ in it. It was
	// declared, documented and ignored while every row on the surface read "git".
	if got := field(get("/v1/tracker/board?source=team"), "title"); len(got) != 1 || got[0] != "Track 0 — ACCESS" {
		t.Errorf("source=team = %v, want only the index row", got)
	}
	if got := field(get("/v1/tracker/board?source=git"), "title"); has(got, "Track 0 — ACCESS") {
		t.Errorf("source=git = %v, want the index row excluded", got)
	}

	// A SEARCH HIT IS AN ADDRESS. The wire carries the board KEY, never the
	// internal id — returning the id is what made every agent-filed row a result
	// you could find and could not open.
	code, raw = asUser(t, app, http.MethodGet, "/v1/tracker/issues", "acme", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("search = %d %s", code, raw)
	}
	var hits struct {
		Issues []struct {
			Project string `json:"project"`
			Title   string `json:"title"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(raw, &hits); err != nil {
		t.Fatalf("search body: %v (%s)", err, raw)
	}
	if len(hits.Issues) == 0 {
		t.Fatal("search returned nothing, want the indexed row")
	}
	for _, h := range hits.Issues {
		if h.Project != "OPS" {
			t.Errorf("search hit %q carries project %q, want the addressable key OPS", h.Title, h.Project)
		}
	}
}
