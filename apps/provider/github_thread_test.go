package provider

// github_thread_test.go pins what an issue's thread hears and when it hears
// nothing: every event a person caused is one escaped line, the App's own writes
// are never said again, an issue labelled `agent` starts a run as the person who
// labelled it, and the write door files through the App, binds the thread and
// resolves `me` through IAM's link.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

func issueEv(action string) githubIssueEvent {
	ev := githubIssueEvent{Action: action, Sender: githubActor{ID: 42, Login: "vi", Type: "User"}}
	ev.Issue = githubIssue{Number: 7, Title: "the ask", Body: "what it needs"}
	ev.Repository.Name, ev.Repository.FullName = "widgets", "acme/widgets"
	ev.Installation.ID = 111
	return ev
}

func TestHeard(t *testing.T) {
	comment := issueEv("created")
	comment.Comment.ID = 9
	comment.Comment.Body = "looks like\nthe cache <!channel> [here](https://evil.example) & more"
	edited := comment
	edited.Action = "edited"
	assigned := issueEv("assigned")
	assigned.Assignee = githubActor{Login: "zed"}
	for name, c := range map[string]struct {
		ev   githubIssueEvent
		want string
	}{
		"a comment is one line, and none of it is markup": {comment,
			":speech_balloon: *vi* on `acme/widgets#7`: looks like the cache &lt;!channel&gt; [here] (https://evil.example) &amp; more"},
		"an edited comment is not news":   {edited, ""},
		"closed":                          {issueEv("closed"), ":white_check_mark: `acme/widgets#7` closed by *vi*"},
		"reopened":                        {issueEv("reopened"), ":leftwards_arrow_with_hook: `acme/widgets#7` reopened by *vi*"},
		"assigned":                        {assigned, ":bust_in_silhouette: `acme/widgets#7` assigned to *zed* by *vi*"},
		"an edit to the body is not news": {issueEv("edited"), ""},
		"opened is the thread's own news": {issueEv("opened"), ""},
	} {
		if got := heard(c.ev); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
}

func TestSelfIsTheAppAlone(t *testing.T) {
	t.Setenv(githubAppSlugEnv, "hanzo-platform")
	for a, want := range map[githubActor]bool{
		{Login: "hanzo-platform[bot]", Type: "Bot"}:  true,
		{Login: "dependabot[bot]", Type: "Bot"}:      false,
		{Login: "hanzo-platform[bot]", Type: "User"}: false,
		{Login: "vi", Type: "User"}:                  false,
	} {
		if self(a) != want {
			t.Errorf("self(%+v) = %v, want %v", a, !want, want)
		}
	}
	t.Setenv(githubAppSlugEnv, "")
	if !self(githubActor{Login: "anything[bot]", Type: "Bot"}) {
		t.Error("with no slug every bot is the App: dropping a line beats starting a loop")
	}
}

func TestCloses(t *testing.T) {
	got := closes("Closes #12, fixes #3 and Resolves: #12\nsee #4, reclose #5, closes acme/other#6")
	if !slices.Equal(got, []int{12, 3}) {
		t.Fatalf("closes = %v, want [12 3]", got)
	}
}

// agentPeer stands up the agent on the plane and records every run it is
// asked for, with the caller the plane carried it for.
type agentPeer struct {
	mu      sync.Mutex
	runs    []client.CodingStartIn
	callers []zip.Caller
}

func serveAgent(t *testing.T) *agentPeer {
	t.Helper()
	p := &agentPeer{}
	up(t, "agent", func(app *zip.App) {
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

// plane points this test's peers at a runtime dir short enough to hold a socket
// path; t.TempDir() embeds the test's name and overruns sun_path.
func socks(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "z")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Bind()
}

// up serves one peer on the plane.
func up(t *testing.T, name string, register func(*zip.App)) {
	t.Helper()
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

// An issue labelled `agent` by a person starts one run as that person, on the
// issue's repository, closing it and narrating in its thread; the App's own
// label coming back — the hand-off a thread already made — starts nothing.
func TestAnIssueLabelledForTheAgentStartsARunAsThePerson(t *testing.T) {
	t.Setenv(githubAppSlugEnv, "hanzo-platform")
	socks(t)
	_ = newApp(t, newKMS(t))
	p := serveAgent(t)
	if err := putUserLink(mounted, "acme", "github", "42", userLink{Subject: "u_vi"}); err != nil {
		t.Fatal(err)
	}
	ev := issueEv("labeled")
	ev.Label.Name = "agent"

	bot := ev
	bot.Sender = githubActor{ID: 1, Login: "hanzo-platform[bot]", Type: "Bot"}
	follow(mounted, "acme", bot, "slack:C1/1.2")
	follow(mounted, "acme", ev, "slack:C1/1.2")

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.runs) != 1 {
		t.Fatalf("%d runs, want exactly the person's one", len(p.runs))
	}
	run := p.runs[0]
	want := client.CodingStartIn{Repo: "github.com/acme/widgets", Prompt: "the ask\n\nwhat it needs", Issue: "7",
		Reply: client.Reply{Provider: "slack", Channel: "C1", Thread: "1.2"}}
	if !reflect.DeepEqual(run, want) {
		t.Fatalf("the run = %+v, want %+v", run, want)
	}
	if c := p.callers[0]; c.Org != "acme" || c.User != "u_vi" {
		t.Fatalf("the run was started as %+v, want acme/u_vi", c)
	}
}

// AN ISSUE'S REPOSITORY IS GITHUB'S. An org whose workspace on the forge is
// spelled like its GitHub org names the issue's repository by GitHub's address,
// so a workspace repository of the same name is never the one the run works on;
// the estate keeps the bare name its forge mirror is reached by.
func TestAnIssuesRepositoryIsNamedOnGitHub(t *testing.T) {
	for _, c := range []struct{ org, full, want string }{
		{"acme", "acme/widgets", "github.com/acme/widgets"},
		{"acme", "octocat/hello", "github.com/octocat/hello"},
		{"hanzo", "hanzoai/cloud", "hanzoai/cloud"},
	} {
		if got := issueRepo(c.org, c.full); got != c.want {
			t.Errorf("issueRepo(%q, %q) = %q, want %q", c.org, c.full, got, c.want)
		}
	}
}

// The write door files through the App, keys the board row by GitHub's own
// spelling, binds the thread, and makes `me` the GitHub account IAM linked to
// the caller.
func TestTheWriteDoorFilesBindsAndResolvesMe(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var (
			mu   sync.Mutex
			sent map[string]any
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/access_tokens"):
				_ = json.NewEncoder(w).Encode(map[string]any{
					"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				})
			case r.Method == http.MethodGet && r.URL.Path == "/user/4242":
				_, _ = io.WriteString(w, `{"login":"vi-gh"}`)
			case r.Method == http.MethodPost && r.URL.Path == "/repos/ACME/widgets/issues":
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				_ = json.Unmarshal(b, &sent)
				mu.Unlock()
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"number": 9, "title": "the ask", "state": "open", "html_url": "https://github.com/acme/widgets/issues/9",
					"repository_url": "http://" + r.Host + "/repos/acme/widgets",
					"assignees":      []map[string]any{{"login": "vi-gh"}},
				})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()
		withGithubApp(t, srv)
		socks(t)
		_ = newApp(t, newKMS(t))
		_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "github", Label: "acme", ExternalID: "111", AccountLabel: "acme"})
		up(t, "iam", func(app *zip.App) {
			app.Post("/iam/linked", func(ctx context.Context, in *client.LinkedIn) (*client.Linked, error) {
				if cloud.Who(ctx).User != "u_vi" {
					return &client.Linked{}, nil
				}
				return &client.Linked{Subject: "4242"}, nil
			}, zip.WithOperationID(client.IAMLinked))
		})
		var mirrored cloud.IssueUpsert
		cloud.RegisterIssueSink(func(_ context.Context, in cloud.IssueUpsert) (cloud.IssueUpsertResult, error) {
			mirrored = in
			return cloud.IssueUpsertResult{Created: true, Number: 1, Channel: in.Channel}, nil
		})
		t.Cleanup(func() { cloud.RegisterIssueSink(nil) })

		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u_vi"})
		mine := "me"
		got, err := planeGitHubIssue(ctx, &client.GitHubIssueIn{Owner: "ACME", Repo: "widgets", Title: "the ask",
			Assignee: &mine, Channel: "slack:C1/1.2"})
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		held, _ := sent["assignees"].([]any)
		mu.Unlock()
		if len(held) != 1 || held[0] != "vi-gh" {
			t.Fatalf("`me` reached GitHub as %v, want the linked login", sent["assignees"])
		}
		if mirrored.ExtRef != "github:acme/widgets#9" || mirrored.Channel != "slack:C1/1.2" || mirrored.Repo != "widgets" {
			t.Fatalf("the board row = %+v, want GitHub's spelling bound to the thread", mirrored)
		}
		if got.Owner != "acme" || got.Number != 9 || got.URL == "" || got.Channel != "slack:C1/1.2" || got.Assignee != "vi-gh" {
			t.Fatalf("the answer = %+v", got)
		}

		other := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u_nobody"})
		if _, err := planeGitHubIssue(other, &client.GitHubIssueIn{Owner: "acme", Repo: "widgets", Title: "x", Assignee: &mine}); err == nil ||
			!strings.Contains(err.Error(), "no GitHub linked") {
			t.Fatalf("a caller with no GitHub linked must be told so, got %v", err)
		}
	})
}
