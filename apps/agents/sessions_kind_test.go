package agents

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// TestCodingRunsPageAndCarryTheirRun proves ?kind=coding lists exactly the
// sessions a coding run tagged, pages them by an opaque cursor that loses and
// repeats nothing, answers each with the run it is the view of, and leaves who
// may see a session as it was: a member sees their own, an org admin the org's.
func TestCodingRunsPageAndCarryTheirRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		ctx := context.Background()

		open := func(user string) string {
			t.Helper()
			code, b := reqAs(t, app, http.MethodPost, "/v1/agent/sessions", "acme", user, false, "", map[string]any{"agent": "coding"})
			if code != http.StatusCreated {
				t.Fatalf("open as %s: %d %s", user, code, b)
			}
			var v sessionView
			mustJSON(t, b, &v)
			return v.ID
		}
		tag := func(id, branch string) {
			t.Helper()
			if err := TagSession(ctx, "acme", id, Session{
				Kind: "coding", Repo: "https://github.com/octocat/hello.git", Base: "main",
				Branch: branch, Environment: "sandbox", PR: "https://github.com/octocat/hello/pull/7",
			}); err != nil {
				t.Fatalf("tag %s: %v", id, err)
			}
		}

		mine := map[string]bool{}
		for i := 0; i < 3; i++ {
			id := open("alice")
			tag(id, id)
			mine[id] = true
		}
		open("alice") // a session no coding run tagged
		theirs := open("bob")
		tag(theirs, theirs)

		list := func(user string, admin bool, q url.Values) sessionList {
			t.Helper()
			code, b := reqAs(t, app, http.MethodGet, "/v1/agent/sessions?"+q.Encode(), "acme", user, admin, "", nil)
			if code != http.StatusOK {
				t.Fatalf("list %v as %s: %d %s", q, user, code, b)
			}
			var l sessionList
			mustJSON(t, b, &l)
			return l
		}

		seen := map[string]bool{}
		q := url.Values{"kind": {"coding"}, "limit": {"2"}}
		first := list("alice", false, q)
		if len(first.Sessions) != 2 || first.Next == "" {
			t.Fatalf("first page want 2 rows and a cursor, got %d rows next=%q", len(first.Sessions), first.Next)
		}
		q.Set("after", first.Next)
		second := list("alice", false, q)
		if len(second.Sessions) != 1 || second.Next != "" {
			t.Fatalf("last page want 1 row and no cursor, got %d rows next=%q", len(second.Sessions), second.Next)
		}
		for _, v := range append(first.Sessions, second.Sessions...) {
			if seen[v.ID] || !mine[v.ID] {
				t.Fatalf("session %s repeated or not alice's coding run", v.ID)
			}
			seen[v.ID] = true
			if v.Kind != "coding" || v.Repo != "https://github.com/octocat/hello.git" || v.Base != "main" ||
				v.Branch != v.ID || v.Environment != "sandbox" || v.PR != "https://github.com/octocat/hello/pull/7" {
				t.Fatalf("session %s does not carry its run: %+v", v.ID, v)
			}
		}
		if len(seen) != 3 {
			t.Fatalf("alice's coding runs: want 3, got %d", len(seen))
		}

		// Without the kind the listing is what it was: every root alice opened.
		if all := list("alice", false, url.Values{}); len(all.Sessions) != 4 {
			t.Fatalf("alice's roots want 4, got %d", len(all.Sessions))
		}
		// A member sees only their own run; an admin sees the org's.
		if b := list("bob", false, url.Values{"kind": {"coding"}}); len(b.Sessions) != 1 || b.Sessions[0].ID != theirs {
			t.Fatalf("bob want only his run, got %+v", b.Sessions)
		}
		if a := list("root", true, url.Values{"kind": {"coding"}}); len(a.Sessions) != 4 {
			t.Fatalf("admin want the org's 4 coding runs, got %d", len(a.Sessions))
		}
		if code, _ := reqAs(t, app, http.MethodGet, "/v1/agent/sessions?after=not-a-cursor", "acme", "alice", false, "", nil); code != http.StatusBadRequest {
			t.Fatalf("a foreign cursor want 400, got %d", code)
		}

	})
}

// TestARunsModeIsOnItsSession proves the mode a run was started in is kept on
// its session and read back with it: a later tag that names no mode leaves it
// as it was, and a session no run tagged carries none.
func TestARunsModeIsOnItsSession(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		ctx := context.Background()
		read := func(id string) (sessionView, string) {
			t.Helper()
			code, b := reqAs(t, app, http.MethodGet, "/v1/agent/sessions/"+id, "acme", "alice", false, "", nil)
			if code != http.StatusOK {
				t.Fatalf("read %s: %d %s", id, code, b)
			}
			var v sessionView
			mustJSON(t, b, &v)
			return v, string(b)
		}
		open := func() string {
			t.Helper()
			code, b := reqAs(t, app, http.MethodPost, "/v1/agent/sessions", "acme", "alice", false, "", map[string]any{"agent": "coding"})
			if code != http.StatusCreated {
				t.Fatalf("open: %d %s", code, b)
			}
			var v sessionView
			mustJSON(t, b, &v)
			return v.ID
		}

		plan := open()
		if err := TagSession(ctx, "acme", plan, Session{Kind: "coding", Repo: "https://github.com/octocat/hello.git", Mode: "plan"}); err != nil {
			t.Fatal(err)
		}
		if err := TagSession(ctx, "acme", plan, Session{Environment: "sandbox"}); err != nil {
			t.Fatal(err)
		}
		if v, _ := read(plan); v.Mode != "plan" || v.Environment != "sandbox" {
			t.Fatalf("the plan's session says mode %q environment %q", v.Mode, v.Environment)
		}
		untagged := open()
		if v, raw := read(untagged); v.Mode != "" || strings.Contains(raw, `"mode"`) {
			t.Fatalf("a session no run tagged must carry no mode: %s", raw)
		}

	})
}

// TestARunsSandboxIsOnItsSession proves the sandbox a run leases is kept on its
// session and read back as `sandbox` — by the detail, the list and the live
// stream's session frame alike, since all three are the one view — and that a
// later tag naming none leaves it as it was.
func TestARunsSandboxIsOnItsSession(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		ctx := context.Background()
		code, b := reqAs(t, app, http.MethodPost, "/v1/agent/sessions", "acme", "alice", false, "", map[string]any{"agent": "coding"})
		if code != http.StatusCreated {
			t.Fatalf("open: %d %s", code, b)
		}
		var v sessionView
		mustJSON(t, b, &v)
		if strings.Contains(string(b), `"sandbox"`) {
			t.Fatalf("a session no run tagged names a sandbox: %s", b)
		}

		const box = "m_0123456789abcdef01234567"
		if err := TagSession(ctx, "acme", v.ID, Session{Kind: "coding", Box: box}); err != nil {
			t.Fatal(err)
		}
		if err := TagSession(ctx, "acme", v.ID, Session{PR: "https://git.hanzo.ai/hanzoai/cloud/pulls/1"}); err != nil {
			t.Fatal(err)
		}
		code, b = reqAs(t, app, http.MethodGet, "/v1/agent/sessions/"+v.ID, "acme", "alice", false, "", nil)
		if code != http.StatusOK {
			t.Fatalf("read: %d %s", code, b)
		}
		var got sessionView
		mustJSON(t, b, &got)
		if got.Sandbox != box || !strings.Contains(string(b), `"sandbox":"`+box+`"`) {
			t.Fatalf("the detail says sandbox %q: %s", got.Sandbox, b)
		}
		code, b = reqAs(t, app, http.MethodGet, "/v1/agent/sessions?kind=coding", "acme", "alice", false, "", nil)
		var l sessionList
		mustJSON(t, b, &l)
		if code != http.StatusOK || len(l.Sessions) != 1 || l.Sessions[0].Sandbox != box {
			t.Fatalf("the list says %d %s", code, b)
		}
		x, err := mounted.State.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		row, err := x.GetSession(ctx, "acme", v.ID)
		if err != nil {
			t.Fatal(err)
		}
		if frame := toSessionView(row, 0, 0); frame.Sandbox != box {
			t.Fatalf("the stream's session frame says sandbox %q", frame.Sandbox)
		}

	})
}

// TestVisibleIsTheSessionsOwnRule proves the session another op reads a run
// through is the one GET /v1/agent/sessions/{id} would show: the member's own,
// or the org's to an admin, and another member's or another org's is absent.
func TestVisibleIsTheSessionsOwnRule(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		app.Group("/test").Get("/visible/:id", func(ctx context.Context, in *sessionRef) (*sessionView, error) {
			x, err := Visible(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			v := toSessionView(x, 0, 0)
			return &v, nil
		})
		code, b := reqAs(t, app, http.MethodPost, "/v1/agent/sessions", "acme", "alice", false, "", map[string]any{"agent": "coding"})
		if code != http.StatusCreated {
			t.Fatalf("open: %d %s", code, b)
		}
		var v sessionView
		mustJSON(t, b, &v)

		for _, c := range []struct {
			org, user string
			admin     bool
			want      int
		}{
			{"acme", "alice", false, http.StatusOK},
			{"acme", "bob", true, http.StatusOK},
			{"acme", "bob", false, http.StatusNotFound},
			{"evil", "alice", false, http.StatusNotFound},
		} {
			code, b := reqAs(t, app, http.MethodGet, "/test/visible/"+v.ID, c.org, c.user, c.admin, "", nil)
			if code != c.want {
				t.Errorf("%s/%s admin=%v read alice's session: %d %s, want %d", c.org, c.user, c.admin, code, b, c.want)
			}
		}

	})
}
