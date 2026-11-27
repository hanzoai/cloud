package task

// home_test.go pins a board whose issues live on GitHub, and an issue handed to
// the agent: an issue asked for in a thread is filed where its repository lives
// and bound to that thread; `me` is the caller; `agent` is the label and a run
// that closes the issue, narrating in the thread it was asked from.

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// peers stands up the provider and the agent on the plane, recording what each
// was asked, as the caller the plane carried it for.
type peers struct {
	mu      sync.Mutex
	writes  []client.GitHubIssueIn
	runs    []client.CodingStartIn
	callers []zip.Caller
	seen    bool // IntegrationsGitHubRepo admits
	channel string
}

func servePeers(t *testing.T) *peers {
	t.Helper()
	p := &peers{seen: true}
	up := func(name string, register func(*zip.App)) {
		app := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
		register(app)
		go func() { _ = app.Listen(zip.SocketPath(name)) }()
		t.Cleanup(func() { _ = app.Shutdown() })
		for range 200 {
			if c, err := net.Dial("unix", zip.SocketPath(name)); err == nil {
				_ = c.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("the %s peer never came up", name)
	}
	up("provider", func(app *zip.App) {
		app.Post("/integrations/github/issue", func(ctx context.Context, in *client.GitHubIssueIn) (*client.GitHubIssue, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.writes = append(p.writes, *in)
			if p.channel == "" {
				p.channel = in.Channel
			}
			n := in.Number
			if n == 0 {
				n = 41
			}
			labels := append([]string{"bug"}, in.Labels...)
			return &client.GitHubIssue{Owner: in.Owner, Repo: in.Repo, Number: n, Title: "the ask", Body: "what it needs",
				State: "open", URL: "https://github.com/" + in.Owner + "/" + in.Repo + "/issues/41", Labels: labels, Channel: p.channel}, nil
		}, zip.WithOperationID(client.IntegrationsGitHubIssue))
		app.Post("/integrations/github/repo", func(ctx context.Context, in *client.GitHubRepoIn) (*client.GitHubRepo, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.seen {
				return nil, zip.ErrNotFound("github repo: not found")
			}
			return &client.GitHubRepo{Owner: in.Owner, Repo: in.Repo}, nil
		}, zip.WithOperationID(client.IntegrationsGitHubRepo))
	})
	up("agent", func(app *zip.App) {
		app.Post("/agents/coding/start", func(ctx context.Context, in *client.CodingStartIn) (*client.CodingStarted, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.runs = append(p.runs, *in)
			p.callers = append(p.callers, cloud.Who(ctx))
			return &client.CodingStarted{SessionID: "sess_1"}, nil
		}, zip.WithOperationID(client.AgentsCodingStart))
	})
	return p
}

// mirror adds a forge repository that is a pull mirror of GitHub.
func (f *stubForge) mirror(org, name, origin string, issues ...map[string]any) {
	f.repo(org, name, issues...)
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[org][len(f.repos[org])-1]
	r["mirror"], r["original_url"] = true, origin
}

var asked = client.Reply{Provider: "slack", Channel: "C024BE91L", Thread: "1726459200.001900", User: "U1"}

// An issue asked for on a board that mirrors GitHub is filed ON GITHUB, through
// the App, bound to the thread it was asked in — never on the mirror, where no
// sync would carry it home.
func TestAnIssueOnAMirrorIsFiledWhereTheRepositoryLives(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		f.mirror("hanzoai", "api", "https://github.com/acme/api")
		app := mountForge(t, f)
		p := servePeers(t)

		code, raw := asUser(t, app, http.MethodPost, "/v1/task/projects/api/issues", "hanzo", "alice",
			map[string]any{"title": "the ask", "description": "what it needs", "reply": asked})
		if code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("create = %d %s", code, raw)
		}
		if strings.Contains(string(raw), `"url":""`) || !strings.Contains(string(raw), "https://github.com/acme/api/issues/41") {
			t.Fatalf("the card does not say where the issue is read: %s", raw)
		}
		if !strings.Contains(string(raw), `"projectKey":"api"`) {
			t.Fatalf("a mirrored card keeps its board's key, so the next edit reaches it: %s", raw)
		}
		f.mu.Lock()
		for _, w := range f.writes {
			if strings.HasSuffix(w.path, "/issues") {
				t.Errorf("an issue was filed on the mirror: %+v", w)
			}
		}
		f.mu.Unlock()
		p.mu.Lock()
		if len(p.writes) != 1 {
			p.mu.Unlock()
			t.Fatalf("GitHub was asked %d times, want once", len(p.writes))
		}
		w := p.writes[0]
		p.mu.Unlock()
		if w.Owner != "acme" || w.Repo != "api" || w.Number != 0 || w.Title != "the ask" {
			t.Fatalf("the write = %+v", w)
		}
		if w.Channel != asked.Address() {
			t.Fatalf("the issue is bound to %q, want the thread it was asked in (%q)", w.Channel, asked.Address())
		}
		if !strings.Contains(w.Body, "what it needs") || !strings.Contains(w.Body, "`alice`") {
			t.Fatalf("the body must carry the ask and who it is for: %q", w.Body)
		}
		// A board key prefixed with the org's own name (e.g. "hanzo-api" when the
		// forge repository is "api") resolves to that repository when no prefixed
		// repository exists.
		code, raw = asUser(t, app, http.MethodPost, "/v1/task/projects/hanzo-api/issues", "hanzo", "alice",
			map[string]any{"title": "prefixed ask", "description": "body"})
		if code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("prefixed create = %d %s", code, raw)
		}
	})
}

// A repository GitHub alone holds is reached only when the caller's own GitHub
// view shows it; otherwise it is not there to file on.
func TestAnIssueOnARepositoryOnlyGitHubHolds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		app := mountForge(t, f)
		p := servePeers(t)
		head := map[string]string{"X-Org-Id": "hanzo", "X-User-Id": "u_alice", "X-User-Name": "alice"}
		call := func() string {
			return mcpCall(t, app, "tools/call", map[string]any{
				"name":      "post_task_projects_by_key_issues",
				"arguments": map[string]any{"key": "hanzo-inc/cloud", "title": "the ask"},
			}, head)
		}
		if out := call(); strings.Contains(out, `"isError":true`) {
			t.Fatalf("a repository the caller sees on GitHub was refused: %s", out)
		}
		p.mu.Lock()
		if len(p.writes) != 1 || p.writes[0].Owner != "hanzo-inc" || p.writes[0].Repo != "cloud" {
			t.Fatalf("the writes = %+v", p.writes)
		}
		p.seen = false
		p.mu.Unlock()
		if out := call(); !strings.Contains(out, "no repository hanzo-inc/cloud") {
			t.Fatalf("a repository the caller cannot see on GitHub must not be filed on: %s", out)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.writes) != 1 {
			t.Fatalf("a refused repository reached GitHub: %+v", p.writes)
		}
	})
}

// `agent` is the label and a run: the run is the caller's, on the board's
// repository, with the issue as its task, closing it, narrating in the thread
// the hand-off was asked in.
func TestHandingAnIssueToTheAgentStartsARunThatClosesIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		f.mirror("hanzoai", "api", "https://github.com/acme/api")
		app := mountForge(t, f)
		p := servePeers(t)

		code, raw := asUser(t, app, http.MethodPatch, "/v1/task/projects/api/issues/41", "hanzo", "alice",
			map[string]any{"assignee": "agent", "reply": asked})
		if code != http.StatusOK {
			t.Fatalf("hand off = %d %s", code, raw)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.writes) != 1 || len(p.writes[0].Labels) != 1 || p.writes[0].Labels[0] != "agent" || p.writes[0].Assignee != nil {
			t.Fatalf("the hand-off is the agent label and nobody assigned: %+v", p.writes)
		}
		if len(p.runs) != 1 {
			t.Fatalf("%d runs started, want one", len(p.runs))
		}
		run := p.runs[0]
		if run.Repo != "api" || run.Issue != "41" || run.Prompt != "the ask\n\nwhat it needs" || run.Reply != asked {
			t.Fatalf("the run = %+v", run)
		}
		if c := p.callers[0]; c.Org != "hanzo" || c.User != "u_alice" {
			t.Fatalf("the run was started as %+v, want the caller", c)
		}
	})
}

// With no thread of its own, a hand-off narrates where the issue is bound.
func TestAHandOffWithNoThreadNarratesWhereTheIssueIsBound(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		f.mirror("hanzoai", "api", "https://github.com/acme/api")
		app := mountForge(t, f)
		p := servePeers(t)
		p.channel = asked.Address()

		if code, raw := asUser(t, app, http.MethodPatch, "/v1/task/projects/api/issues/41", "hanzo", "alice",
			map[string]any{"assignee": "agent"}); code != http.StatusOK {
			t.Fatalf("hand off = %d %s", code, raw)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		want := client.Reply{Provider: asked.Provider, Channel: asked.Channel, Thread: asked.Thread}
		if len(p.runs) != 1 || p.runs[0].Reply != want {
			t.Fatalf("the run narrates at %+v, want the bound thread %+v", p.runs, want)
		}
	})
}

// On the forge, `me` is the caller's forge login, `agent` adds the label to the
// ones the issue has, and a comment is posted last.
func TestForgeWrites_MeAgentAndAComment(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		f.repo("hanzoai", "api", issue(7, "card", "open", "todo", "bug"))
		app := mountForge(t, f)
		p := servePeers(t)

		if code, raw := asUser(t, app, http.MethodPatch, "/v1/task/projects/api/issues/7", "hanzo", "alice",
			map[string]any{"assignee": "me", "comment": "on it"}); code != http.StatusOK {
			t.Fatalf("assign to me = %d %s", code, raw)
		}
		if code, raw := asUser(t, app, http.MethodPatch, "/v1/task/projects/api/issues/7", "hanzo", "alice",
			map[string]any{"assignee": "agent", "reply": asked}); code != http.StatusOK {
			t.Fatalf("hand off = %d %s", code, raw)
		}
		f.mu.Lock()
		var held, commented bool
		var labels []any
		for _, w := range f.writes {
			switch {
			case strings.HasSuffix(w.path, "/issues/7") && w.body["assignees"] != nil:
				got, _ := w.body["assignees"].([]any)
				held = len(got) == 1 && got[0] == "alice"
			case strings.HasSuffix(w.path, "/issues/7/comments"):
				commented = w.body["body"] == "on it"
			case strings.HasSuffix(w.path, "/issues/7/labels"):
				labels, _ = w.body["labels"].([]any)
			}
		}
		f.mu.Unlock()
		if !held {
			t.Error("`me` did not assign the caller's forge login")
		}
		if !commented {
			t.Error("the comment never reached the forge")
		}
		if len(labels) != 3 || labels[0] != "agent" {
			t.Errorf("the hand-off must add `agent` to the issue's own labels, got %v", labels)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.runs) != 1 || p.runs[0].Repo != "api" || p.runs[0].Issue != "7" || p.runs[0].Reply != asked {
			t.Fatalf("the forge hand-off run = %+v", p.runs)
		}
	})
}

// A project slug for an imported GitHub mirror (`cssprod-sroute` or
// `my-org-my-app`) resolves cleanly to the underlying forge repository
// (`cssprod_sroute` or `my-org_my-app`) across create, update, get, and board
// reads without returning 404 "no such board".
func TestMirroredSlugResolvesForgeUnderscoreBoard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		f.mirror("hanzoai", "cssprod_sroute", "https://github.com/cssprod/Sroute", issue(12, "mirror card", "open", "todo"))
		f.repo("hanzoai", "my-org_my-app", issue(3, "native card", "open", "backlog"))
		app := mountForge(t, f)
		p := servePeers(t)

		// 1. Create on hyphenated mirror slug routes to GitHub home with real forge key.
		code, raw := asUser(t, app, http.MethodPost, "/v1/task/projects/cssprod-sroute/issues", "hanzo", "alice",
			map[string]any{"title": "fix routing", "description": "from board"})
		if code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("create on cssprod-sroute = %d %s", code, raw)
		}
		if !strings.Contains(string(raw), `"projectKey":"cssprod_sroute"`) {
			t.Fatalf("expected projectKey cssprod_sroute, got %s", raw)
		}
		p.mu.Lock()
		if len(p.writes) != 1 || p.writes[0].Owner != "cssprod" || p.writes[0].Repo != "Sroute" {
			p.mu.Unlock()
			t.Fatalf("expected GitHub write to cssprod/Sroute, got %+v", p.writes)
		}
		p.mu.Unlock()

		// 2. Get project, board filter, and single issue by hyphenated slug all resolve.
		if code, raw := asUser(t, app, http.MethodGet, "/v1/task/projects/cssprod-sroute", "hanzo", "alice", nil); code != http.StatusOK || !strings.Contains(string(raw), `"key":"cssprod_sroute"`) {
			t.Fatalf("get project cssprod-sroute = %d %s", code, raw)
		}
		if code, raw := asUser(t, app, http.MethodGet, "/v1/task/board?key=cssprod-sroute", "hanzo", "alice", nil); code != http.StatusOK || !strings.Contains(string(raw), "mirror card") {
			t.Fatalf("board?key=cssprod-sroute = %d %s", code, raw)
		}
		if code, raw := asUser(t, app, http.MethodGet, "/v1/task/projects/cssprod-sroute/issues/12", "hanzo", "alice", nil); code != http.StatusOK || !strings.Contains(string(raw), "mirror card") {
			t.Fatalf("get issue cssprod-sroute#12 = %d %s", code, raw)
		}

		// 3. Multi-hyphen native forge repo (`my-org_my-app` addressed as `my-org-my-app`) creates & patches cleanly.
		if code, raw := asUser(t, app, http.MethodPost, "/v1/task/projects/my-org-my-app/issues", "hanzo", "alice",
			map[string]any{"title": "multi hyphen card"}); code != http.StatusCreated {
			t.Fatalf("create on my-org-my-app = %d %s", code, raw)
		}
		if code, raw := asUser(t, app, http.MethodPatch, "/v1/task/projects/my-org-my-app/issues/3", "hanzo", "alice",
			map[string]any{"status": "in_progress"}); code != http.StatusOK {
			t.Fatalf("patch on my-org-my-app#3 = %d %s", code, raw)
		}
	})
}
