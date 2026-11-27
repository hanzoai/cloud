package triage

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/zap-proto/zip"
)

// world stands in for GitHub and the agents app: the two peers triage talks to, on
// real sockets, holding what they were asked.
type world struct {
	mu        sync.Mutex
	issues    map[int]*client.GitHubIssue
	comments  map[int][]string
	pulls     map[int]*client.GitHubPull
	starts    []client.CodingStartIn
	callers   []zip.Caller
	merges    []client.GitHubMergeIn
	stops     []client.SessionMatchIn
	holds     bool // the release holds the fix
	asked     int  // how many times a release was asked about
	nextIssue int
}

func serve(t *testing.T, name string, register func(*zip.App)) {
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

func newWorld(t *testing.T) *world {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	w := &world{issues: map[int]*client.GitHubIssue{}, comments: map[int][]string{}, pulls: map[int]*client.GitHubPull{}, nextIssue: 40}
	serve(t, "provider", func(app *zip.App) {
		app.Post("/integrations/github/issue", func(ctx context.Context, in *client.GitHubIssueIn) (*client.GitHubIssue, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			is := w.issues[in.Number]
			if in.Number == 0 {
				w.nextIssue++
				is = &client.GitHubIssue{Owner: in.Owner, Repo: in.Repo, Number: w.nextIssue, State: "open"}
				w.issues[is.Number] = is
			}
			if is == nil {
				return nil, zip.ErrNotFound("no such issue")
			}
			if in.Title != "" {
				is.Title = in.Title
			}
			if in.Body != "" {
				is.Body = in.Body
			}
			if in.State != "" {
				is.State = in.State
			}
			is.Labels = append(is.Labels, in.Labels...)
			if in.Comment != "" {
				w.comments[is.Number] = append(w.comments[is.Number], in.Comment)
			}
			out := *is
			return &out, nil
		}, zip.WithOperationID(client.IntegrationsGitHubIssue))
		app.Post("/integrations/github/pull", func(ctx context.Context, in *client.GitHubPullIn) (*client.GitHubPull, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if pr := w.pulls[in.Number]; pr != nil {
				out := *pr
				return &out, nil
			}
			return nil, zip.ErrNotFound("no such pull request")
		}, zip.WithOperationID(client.IntegrationsGitHubPull))
		app.Post("/integrations/github/merge", func(ctx context.Context, in *client.GitHubMergeIn) (*client.GitHubMerged, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.merges = append(w.merges, *in)
			return &client.GitHubMerged{Sha: "merge-" + in.Sha}, nil
		}, zip.WithOperationID(client.IntegrationsGitHubMerge))
		app.Post("/integrations/github/contains", func(ctx context.Context, in *client.GitHubContainsIn) (*client.GitHubContains, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.asked++
			return &client.GitHubContains{Contains: w.holds}, nil
		}, zip.WithOperationID(client.IntegrationsGitHubContains))
	})
	serve(t, "agent", func(app *zip.App) {
		app.Post("/agents/coding/start", func(ctx context.Context, in *client.CodingStartIn) (*client.CodingStarted, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.starts = append(w.starts, *in)
			w.callers = append(w.callers, cloud.Who(ctx))
			n := len(w.starts)
			return &client.CodingStarted{SessionID: "sess_" + string(rune('0'+n)), Branch: "agent/" + in.Branch}, nil
		}, zip.WithOperationID(client.AgentsCodingStart))
		app.Post("/agents/sessions/stop", func(ctx context.Context, in *client.SessionMatchIn) (*client.SessionCount, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.stops = append(w.stops, *in)
			return &client.SessionCount{}, nil
		}, zip.WithOperationID(client.AgentsSessionsStop))
	})
	return w
}

func (w *world) started() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.starts)
}

func (w *world) said(issue int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.comments[issue]...)
}

// loop is a triage over a ledger on each engine, with a clock the test moves.
type loop struct {
	*state
	w   *world
	now time.Time
	// changed, when set, is how many files GitHub says the next pull request changes.
	changed int
}

func newLoop(t *testing.T, cfg Config) *loop {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// A subtest's loop starts from an empty ledger: on PostgreSQL the engine's tests
	// share one database, so the tables are emptied here.
	for _, table := range []string{"triage_config", "triage_faults", "triage_work"} {
		if _, err := store.db.Exec("DELETE FROM " + table); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	l := &loop{w: newWorld(t), now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	l.state = &state{store: store, log: cloud.NewBase(cloud.Deps{}, "triage").Log, now: func() time.Time { return l.now }}
	return l
}

func each(t *testing.T, body func(t *testing.T)) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) { body(t) })
}

// cloudFault is a fault the platform stated about itself, as the plane hands it over.
func cloudFault(fp, route string, at time.Time) event.Fault {
	return event.Fault{
		Org: "hanzo", Issue: fp, Product: "cloud", Service: "agents", Path: route, Time: at, Message: "row 41 is gone",
		Attributes: map[string]string{"fault.class": "server", "fault.code": "internal", "fault.op": "GET " + route, "release": "r1", "fault.request": "req-" + fp},
	}
}

// repoOf is the one repository the tests route to.
func repoOf(auto, merge bool) Repo {
	return Repo{Org: "hanzo", Repo: "hanzo-inc/cloud", Match: []Match{{Product: "cloud"}}, Auto: auto, Merge: merge, Concurrent: 1}
}

func config(auto, merge bool) Config {
	return Config{
		Enabled: true, Actor: "admin/hanzo-autofix", Label: "auto",
		Threshold: map[string]Limit{"server": {Count: 3, Minutes: 60}},
		Runs:      Runs{Seconds: 600, PerDay: 5, Attempts: 2},
		Repos:     []Repo{repoOf(auto, merge)},
	}
}
