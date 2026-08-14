package todo

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// READING A WORK ITEM. Every other read on this surface answers a board, and a
// board is a summary — it carries no description. The description is where the
// content of an item actually is, so these two are what make an issue readable
// at all: the text search that finds it, and the address that returns it whole.
//
// Both were broken in production at the same time and for unrelated reasons,
// which is why they are pinned together here.

// THE SEARCH. `q=` answered 500 on every call — 22 of 22 probes — because the
// LIKE clause was written `ESCAPE '\\'` inside a BACKTICKED string, so SQLite
// received a two-character escape expression and refuses anything but one
// character. likeEscape doubled its replacements the same way.
//
// Nothing caught it because the clause is only appended when the caller passes
// text: every test that listed a board passed, and the one read that made an
// issue's body reachable was the one that never ran.
func TestSearchText_FindsTheRowAndTreatsAWildcardAsText(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("hanzo", "ENG", "Engineering")); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := mkProject("hanzo", "ENG", "Engineering").ID
	for _, row := range []Issue{
		{ID: "i1", ProjectID: pid, Org: "hanzo", Number: 1, Title: "Track 0 — ACCESS",
			Description: "acceptance: every agent can reach the board", Status: "todo"},
		{ID: "i2", ProjectID: pid, Org: "hanzo", Number: 2, Title: "cut over at 50% traffic",
			Description: "half the fleet", Status: "todo"},
		{ID: "i3", ProjectID: pid, Org: "hanzo", Number: 3, Title: "unrelated",
			Description: "nothing here", Status: "todo"},
	} {
		if _, err := s.CreateIssue(ctx, row); err != nil {
			t.Fatalf("CreateIssue %s: %v", row.ID, err)
		}
	}

	find := func(q string) []Issue {
		t.Helper()
		got, err := s.ListIssues(ctx, "hanzo", "", IssueFilter{Text: q})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		return got
	}

	// A word from the title.
	if got := find("Track 0"); len(got) != 1 || got[0].ID != "i1" {
		t.Fatalf(`search "Track 0" returned %d rows, want just i1: %+v`, len(got), got)
	}
	// A word from the DESCRIPTION — the half of the row no board read returns.
	if got := find("acceptance"); len(got) != 1 || got[0].ID != "i1" {
		t.Fatalf(`search "acceptance" returned %d rows, want just i1: %+v`, len(got), got)
	}
	// A WILDCARD IS TEXT. `%` unescaped matches every row, which turns the search
	// box into a way to select the whole table — the reason the ESCAPE clause is
	// there at all, and the property that made it get written wrongly.
	if got := find("50%"); len(got) != 1 || got[0].ID != "i2" {
		t.Fatalf(`search "50%%" returned %d rows, want just i2: %+v`, len(got), got)
	}
	// And a bare "%" is a search for the CHARACTER, so it finds the one row that
	// contains one — not all three, which is what an unescaped wildcard does.
	if got := find("%"); len(got) != 1 || got[0].ID != "i2" {
		t.Fatalf(`a bare "%%" matched %d rows; it must match the literal character: %+v`, len(got), got)
	}
	// `_` is the other LIKE wildcard, and a backslash is what escapes both — so a
	// search for one must not become a search for any character, and one for the
	// escape itself must not corrupt the pattern.
	if got := find("_"); len(got) != 0 {
		t.Fatalf(`a bare "_" matched %d rows; it must match the literal character: %+v`, len(got), got)
	}
	if got := find(`\`); len(got) != 0 {
		t.Fatalf(`a bare backslash matched %d rows: %+v`, len(got), got)
	}
}

// THE ADDRESS. PATCH accepts /projects/:key/issues/:num, so an item you can move
// is an item you can name — and until this route existed there was no way to GET
// it. An epic's acceptance criteria were therefore unreachable from any API: the
// board reads omit the description and the search hit carries only a title.
func TestGetIssue_ReturnsTheRowInFullFromEitherSource(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(7, "api work", "open", "todo"))
	app := mountForge(t, f)

	one := func(path string) map[string]any {
		t.Helper()
		code, raw := asUser(t, app, http.MethodGet, path, "hanzo", "alice", nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, code, raw)
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, raw)
		}
		return v
	}

	// A row the forge holds, read at its own address rather than filtered out of
	// an org-wide fan-out.
	got := one("/v1/todo/projects/api/issues/7")
	if got["identifier"] != "api-7" && got["number"] != float64(7) {
		t.Fatalf("wrong row: %+v", got)
	}

	// A row only the index holds — an agent's own roadmap. Same URL, because a
	// work item is one kind of thing however it came to exist.
	st, err := storeFor(mounted, "hanzo", "default")
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	ctx := context.Background()
	board := Project{ID: "prj_ops", Org: "hanzo", Key: "OPS", Name: "Roadmap", CreatedAt: 1, UpdatedAt: 1}
	if err := st.CreateProject(ctx, board); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateIssue(ctx, Issue{
		ID: "iss_t0", ProjectID: board.ID, Org: "hanzo", Kind: "epic", Source: "team",
		Title: "Track 0 — ACCESS", Description: "acceptance: every agent can reach the board",
		Status: "todo", Assignee: "neo", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	rows, err := st.ListIssues(ctx, "hanzo", "", IssueFilter{})
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListIssues: %v (%d rows)", err, len(rows))
	}
	num := rows[0].Number

	got = one("/v1/todo/projects/OPS/issues/" + itoa(num))
	// THE DESCRIPTION IS THE POINT. A read that omits it answers the same thing
	// the board already answered, and the acceptance criteria stay unreachable.
	if got["description"] != "acceptance: every agent can reach the board" {
		t.Fatalf("the description did not come back: %+v", got)
	}
	if got["assignee"] != "neo" {
		t.Fatalf("assignee = %v, want neo: %+v", got["assignee"], got)
	}

	// A number nobody filed is 404, not somebody else's row.
	if code, _ := asUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues/9999", "hanzo", "alice", nil); code != http.StatusNotFound {
		t.Fatalf("GET a missing issue = %d, want 404", code)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
