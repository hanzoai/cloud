package todo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
	_ "github.com/hanzoai/cloud/internal/devmaster"
	"github.com/zap-proto/zip"
)

// theRoom is a room addressed the way meet spells one: the space uuid, the
// separator, then the room's own id. This package never parses it — it is an
// opaque key here — and the test uses the real shape so a reader can see that the
// value crossing between the two surfaces is one value.
const theRoom = "11111111-1111-4111-8111-111111111111_ch-bugfix-1010"

func roomItem(org, projectID, room, title, status string, updated int64) Issue {
	return Issue{
		ID: "iss_" + title, Org: org, ProjectID: projectID, Room: room,
		Title: title, Status: status, Priority: "none",
		CreatedAt: 100, UpdatedAt: updated,
	}
}

// TestRoomSurvivesTheRoundTrip is the column's own gate: a binding written is a
// binding read back.
//
// It is worth a test rather than being assumed because the column was added to
// FOUR places that must agree — the struct, issueCols, scanIssue and the INSERT's
// placeholder list — and a mismatch there does not fail to compile. It shifts
// every column after it, so the failure is a row whose fields are silently one
// position out.
func TestRoomSurvivesTheRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("hanzo", "ENG", "Engineering")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	in := roomItem("hanzo", "prj_hanzo_ENG", theRoom, "the-item", "todo", 200)
	in.Repo, in.ExtRef, in.Assignee = "hanzoai/cloud", "github:hanzoai/cloud#1", "ada"
	created, err := s.CreateIssue(ctx, in)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	got, err := s.GetIssue(ctx, "hanzo", "prj_hanzo_ENG", created.Number)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	// Every neighbour of the new column is asserted, not just the column: a
	// placeholder off by one reads back a row whose fields have all shifted, and
	// checking Room alone would pass while Repo held the ExtRef.
	for _, c := range []struct{ name, got, want string }{
		{"room", got.Room, theRoom},
		{"repo", got.Repo, "hanzoai/cloud"},
		{"extRef", got.ExtRef, "github:hanzoai/cloud#1"},
		{"title", got.Title, "the-item"},
		{"status", got.Status, "todo"},
		{"assignee", got.Assignee, "ada"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestRoomFilterSelectsOneChannelsWork: the filter is what makes a channel's todo
// list a query rather than a stored list. It must select the room's items across
// EVERY board, and exclude another room's and the unbound ones.
func TestRoomFilterSelectsOneChannelsWork(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, p := range []Project{mkProject("hanzo", "ENG", "Engineering"), mkProject("hanzo", "OPS", "Operations")} {
		if err := s.CreateProject(ctx, p); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}
	other := "22222222-2222-4222-8222-222222222222_ch-other"
	for _, i := range []Issue{
		roomItem("hanzo", "prj_hanzo_ENG", theRoom, "eng-one", "todo", 300),
		// The SECOND board. A room's work is not confined to one board, which is
		// the whole reason the filter spans them and the index is keyed (org, room).
		roomItem("hanzo", "prj_hanzo_OPS", theRoom, "ops-one", "backlog", 400),
		roomItem("hanzo", "prj_hanzo_ENG", other, "another-room", "todo", 500),
		roomItem("hanzo", "prj_hanzo_ENG", "", "unbound", "todo", 600),
	} {
		if _, err := s.CreateIssue(ctx, i); err != nil {
			t.Fatalf("create issue %s: %v", i.Title, err)
		}
	}

	rows, err := s.ListIssues(ctx, "hanzo", "", IssueFilter{Room: theRoom})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Title] = true
	}
	if len(rows) != 2 || !seen["eng-one"] || !seen["ops-one"] {
		t.Fatalf("room filter returned %d rows %v, want exactly eng-one and ops-one", len(rows), seen)
	}

	// An UNFILTERED read still returns everything — the binding narrows a query
	// and never hides a row from the surfaces that do not ask about rooms.
	all, err := s.ListIssues(ctx, "hanzo", "", IssueFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("unfiltered read returned %d rows, want 4", len(all))
	}
}

// TestRoomFilterIsScopedToTheOrg: the binding must not become a way to read
// another tenant's channel. Two orgs naming the same room string is the case,
// because a room id is unique within a space and nothing stops two tenants
// from spelling one the same way.
func TestRoomFilterIsScopedToTheOrg(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, p := range []Project{mkProject("hanzo", "ENG", "Engineering"), mkProject("acme", "ENG", "Engineering")} {
		if err := s.CreateProject(ctx, p); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}
	for _, i := range []Issue{
		roomItem("hanzo", "prj_hanzo_ENG", theRoom, "ours", "todo", 300),
		roomItem("acme", "prj_acme_ENG", theRoom, "theirs", "todo", 300),
	} {
		if _, err := s.CreateIssue(ctx, i); err != nil {
			t.Fatalf("create issue %s: %v", i.Title, err)
		}
	}
	rows, err := s.ListIssues(ctx, "hanzo", "", IssueFilter{Room: theRoom})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Title != "ours" {
		t.Fatalf("got %d rows (%+v), want only this org's", len(rows), rows)
	}
}

// TestSettledIsOnePredicate pins the vocabulary the open count and the forge's
// close both read. It is the one fact this rollup and source.go's PATCH share, so
// a status added to the closed set without a decision here would make a channel's
// header disagree with which issues the forge has closed.
func TestSettledIsOnePredicate(t *testing.T) {
	for s, want := range map[string]bool{
		"done": true, "canceled": true,
		"backlog": false, "todo": false, "in_progress": false, "": false,
	} {
		if got := settled(s); got != want {
			t.Errorf("settled(%q) = %v, want %v", s, got, want)
		}
	}
	// Every status the surface accepts is classified — a new column cannot arrive
	// as neither open nor settled, because the rollup counts on that partition.
	for s := range statuses {
		_ = settled(s)
	}
}

// ── the wire ────────────────────────────────────────────────────────────────

// askTodo drives the REAL routes as a signed-in member of org would, so what is
// asserted below is the wire a channel view receives rather than a handler's
// return value.
func askTodo(t *testing.T, app *zip.App, path, org string) (int, string) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodGet, path, nil)
	rq.Header.Set("X-Org-Id", org)
	// X-User-Id is what satisfies the validated-principal gate; the identity
	// boundary mints both from verified claims and a test is that boundary.
	rq.Header.Set("X-User-Id", "u_ada")
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// seed writes items straight into the store the mounted routes read, which is the
// only write path a deployment with no forge has for these rows (the HTTP create
// writes the forge; the agent tool writes here).
func seed(t *testing.T, org string, items ...Issue) {
	t.Helper()
	store, err := storeFor(mounted, org, principal.DefaultProject)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if err := store.CreateProject(context.Background(), mkProject(org, "ENG", "Engineering")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, i := range items {
		i.Org, i.ProjectID = org, "prj_"+org+"_ENG"
		if _, err := store.CreateIssue(context.Background(), i); err != nil {
			t.Fatalf("seed %s: %v", i.Title, err)
		}
	}
}

// TestAChannelsWorkOverTheWire is the integration as a surface receives it: the
// LIST and the SUMMARY are two readings of one query, so the header a channel
// renders cannot disagree with the list beneath it.
func TestAChannelsWorkOverTheWire(t *testing.T) {
	app := mountWire(t)
	other := "22222222-2222-4222-8222-222222222222_ch-other"
	seed(t, "hanzo",
		roomItem("hanzo", "", theRoom, "open-one", "todo", 300),
		roomItem("hanzo", "", theRoom, "open-two", "in_progress", 500),
		roomItem("hanzo", "", theRoom, "finished", "done", 400),
		roomItem("hanzo", "", other, "another-room", "todo", 900),
		roomItem("hanzo", "", "", "unbound", "todo", 900),
	)

	code, body := askTodo(t, app, "/v1/todo/rooms/"+theRoom, "hanzo")
	if code != http.StatusOK {
		t.Fatalf("rollup: %d (%s)", code, body)
	}
	var got roomWork
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if got.Room != theRoom || got.Total != 3 || got.Open != 2 {
		t.Errorf("rollup = %+v, want room=%s total=3 open=2", got, theRoom)
	}
	// Every column is present, including the empty ones — a surface renders the
	// board without inventing the vocabulary.
	for col, want := range map[string]int{"todo": 1, "in_progress": 1, "done": 1, "backlog": 0, "canceled": 0} {
		if got.Status[col] != want {
			t.Errorf("status[%s] = %d, want %d", col, got.Status[col], want)
		}
	}
	// The NEWEST item's timestamp, and only from this room: the 900 belongs to
	// another channel and must not be reported as this one's last activity.
	if got.Updated != 500 {
		t.Errorf("updated = %d, want 500 (this room's newest, not another's)", got.Updated)
	}

	// The list is the same filter over the same store, so the two agree by
	// construction — asserting it is what catches them being wired to different
	// stores or different defaults.
	code, body = askTodo(t, app, "/v1/todo/issues?room="+theRoom, "hanzo")
	if code != http.StatusOK {
		t.Fatalf("list: %d (%s)", code, body)
	}
	var hits issueHits
	if err := json.Unmarshal([]byte(body), &hits); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if hits.Count != got.Total {
		t.Fatalf("the list says %d and the summary says %d — one query, two answers", hits.Count, got.Total)
	}
	for _, h := range hits.Issues {
		if h.Room != theRoom {
			t.Errorf("listed %q from room %q", h.Title, h.Room)
		}
	}
}

// TestAnEmptyChannelIsEmptyRatherThanAbsent: a room nobody has filed work in
// answers a board of zeros, and its last activity is ABSENT rather than the
// epoch. A channel view can then say "no work yet" instead of "last active
// January 1970", and an unknown room reads the same as an empty one because this
// package cannot tell them apart and does not pretend to.
func TestAnEmptyChannelIsEmptyRatherThanAbsent(t *testing.T) {
	app := mountWire(t)
	code, body := askTodo(t, app, "/v1/todo/rooms/"+theRoom, "hanzo")
	if code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, body)
	}
	if strings.Contains(body, `"updated"`) {
		t.Errorf("an empty room reports a last activity: %s", body)
	}
	var got roomWork
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 0 || got.Open != 0 || len(got.Status) != len(statuses) {
		t.Errorf("empty rollup = %+v, want zeros across all %d columns", got, len(statuses))
	}
}

// TestOneChannelsWorkIsNotAnothersOverTheWire: the tenancy boundary, asserted at
// the surface rather than at the store, because that is where a caller reaches it.
func TestOneChannelsWorkIsNotAnothersOverTheWire(t *testing.T) {
	app := mountWire(t)
	seed(t, "hanzo", roomItem("hanzo", "", theRoom, "ours", "todo", 300))
	seed(t, "acme", roomItem("acme", "", theRoom, "theirs", "todo", 300))

	code, body := askTodo(t, app, "/v1/todo/rooms/"+theRoom, "acme")
	if code != http.StatusOK {
		t.Fatalf("got %d (%s)", code, body)
	}
	var got roomWork
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 1 {
		t.Fatalf("acme sees %d items in a room name it shares with hanzo, want 1", got.Total)
	}
	if _, list := askTodo(t, app, "/v1/todo/issues?room="+theRoom, "acme"); strings.Contains(list, "ours") {
		t.Errorf("another org's item is in acme's channel list: %s", list)
	}
}

// TestTheRollupRefusesAnUnvalidatedCaller: the room is a path segment, so the
// tenant must come from the principal — a caller with no validated org cannot
// read a channel by naming one.
func TestTheRollupRefusesAnUnvalidatedCaller(t *testing.T) {
	app := mountWire(t)
	rq := httptest.NewRequest(http.MethodGet, "/v1/todo/rooms/"+theRoom, nil)
	rq.Header.Set("X-Org-Id", "hanzo") // an org with no user behind it is not validated
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403 for a caller with no validated principal", resp.StatusCode)
	}
}
