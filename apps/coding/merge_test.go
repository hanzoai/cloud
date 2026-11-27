package coding

// merge_test.go — merging what a run proposed: as the person in the estate's
// namespaces, as the machine in a made one, only the run's own pull request, at
// the commit read, and never forced.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/forge"
	"github.com/zap-proto/zip"
)

// pullForge holds one repository, owner/site, with the run's pull request #7 and
// a fork's #9 from a branch of the same name. sudo is who may read it: a login
// in the estate's namespaces, "" (the machine) in a made one.
type pullForge struct {
	mu        sync.Mutex
	url       string
	owner     string
	sudo      string
	state     string // open, closed or merged
	mergeable bool
	refuse    bool // the forge refuses the merge itself: a rule on the base
	tip       string
	merges    []map[string]any // each merge the forge was asked for
	as        []string         // the Sudo of every request
}

func (f *pullForge) pull(n int, repo string) map[string]any {
	state, merged := f.state, f.state == "merged"
	if merged {
		state = "closed"
	}
	return map[string]any{
		"number": n, "html_url": f.url + "/" + repo + "/pulls/" + strconv.Itoa(n),
		"title": "Add a footer", "state": state, "merged": merged, "mergeable": f.mergeable,
		"head": map[string]any{"ref": runBranch, "sha": f.tip, "repo": map[string]any{"full_name": repo}},
		"base": map[string]any{"ref": "main"},
	}
}

func (f *pullForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	if login, ok := strings.CutPrefix(path, "/users/"); ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"login": login, "email": login + "@hanzo.ai"})
		return
	}
	f.as = append(f.as, r.Header.Get("Sudo"))
	if r.Header.Get("Sudo") != f.sudo {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	repo := "/repos/" + f.owner + "/site"
	switch {
	case path == repo:
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "site", "full_name": f.owner + "/site", "default_branch": "main"})
	case path == repo+"/pulls/7" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(f.pull(7, f.owner+"/site"))
	case path == repo+"/pulls/9" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(f.pull(9, "mallory/site"))
	case path == repo+"/pulls" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode([]map[string]any{f.pull(9, "mallory/site"), f.pull(7, f.owner+"/site")})
	case strings.HasSuffix(path, "/merge") && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.merges = append(f.merges, map[string]any{"path": path, "body": body})
		switch {
		case !f.mergeable || f.refuse:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Please try again later"})
		case body["head_commit_id"] != f.tip:
			w.WriteHeader(http.StatusConflict)
		default:
			f.state = "merged"
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// servesPull stands the forge up with the run on it, as the sessions app lets
// the caller see it.
func servesPull(t *testing.T, org, owner, sudo string) (*pullForge, map[string]agents.Session) {
	t.Helper()
	iamAddr = map[string]client.Email{"sub-zoe": verified("zoe@hanzo.ai")}
	f := &pullForge{owner: owner, sudo: sudo, state: "open", mergeable: true, tip: runTip}
	f.url = servesForge(t, f)
	sessions := map[string]agents.Session{
		runSession: {ID: runSession, Org: org, Kind: kindCoding, Repo: owner + "/site", Base: "main", Branch: runBranch},
	}
	was := visibleRun
	visibleRun = func(_ context.Context, id string) (agents.Session, error) {
		if x, ok := sessions[id]; ok {
			return x, nil
		}
		return agents.Session{}, zip.ErrNotFound("session not found")
	}
	t.Cleanup(func() { visibleRun = was })
	return f, sessions
}

// merges is what the forge was asked to merge.
func (f *pullForge) asked() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.merges...)
}

// IN THE ESTATE A MERGE IS THE PERSON'S: every request is sudoed as them, so the
// forge's own rules on the base decide, and what is merged is the run's own pull
// request at the commit read.
func TestMerge_InTheEstateIsThePersons(t *testing.T) {
	f, _ := servesPull(t, "hanzo", "hanzoai", "zoe")
	got, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: runSession})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got.Number != 7 || got.State != "merged" || got.Base != "main" {
		t.Fatalf("merged %+v", got)
	}
	asked := f.asked()
	if len(asked) != 1 || asked[0]["path"] != "/repos/hanzoai/site/pulls/7/merge" {
		t.Fatalf("the forge was asked %v", asked)
	}
	body := asked[0]["body"].(map[string]any)
	if body["Do"] != "merge" || body["head_commit_id"] != runTip || body["force_merge"] != nil {
		t.Fatalf("the merge asked %v", body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, who := range f.as {
		if who != "zoe" {
			t.Fatalf("a request was made as %q", who)
		}
	}
}

// IN A MADE WORKSPACE THE MACHINE MERGES, after the org's own record named the
// namespace, and nobody is sudoed.
func TestMerge_InAMadeWorkspaceIsTheMachines(t *testing.T) {
	f, _ := servesPull(t, "acme", "acme", "")
	project := zip.New(zip.Config{AppName: projectpeer.App, DisableStartupMessage: true})
	project.Post("/projects/workspace", func(ctx context.Context, _ *client.WorkspaceIn) (*client.Workspace, error) {
		if zip.CallerOf(ctx).Org != "acme" {
			return &client.Workspace{}, nil
		}
		return &client.Workspace{Owner: "acme", Made: true}, nil
	}, zip.WithOperationID(client.ProjectsWorkspace))
	go func() { _ = project.Listen(zip.SocketPath(projectpeer.App)) }()
	t.Cleanup(func() { _ = project.Shutdown() })
	waitListening(t, projectpeer.App)

	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "sub-zoe"})
	got, err := Merge(ctx, &client.CodingMergeIn{Session: runSession})
	if err != nil || got.State != "merged" {
		t.Fatalf("Merge = %+v, %v", got, err)
	}
	if asked := f.asked(); len(asked) != 1 || asked[0]["path"] != "/repos/acme/site/pulls/7/merge" {
		t.Fatalf("the forge was asked %v", asked)
	}
}

// A MERGED PULL REQUEST ANSWERS AS IT IS, and nothing is asked of the forge.
func TestMerge_AMergedPullRequestAnswersAsItIs(t *testing.T) {
	f, _ := servesPull(t, "hanzo", "hanzoai", "zoe")
	f.state = "merged"
	got, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: runSession})
	if err != nil || got.State != "merged" || len(f.asked()) != 0 {
		t.Fatalf("Merge = %+v, %v; asked %v", got, err, f.asked())
	}
}

// NOTHING IS FORCED: a conflict and a closed pull request are 409 before the
// forge is asked, and a merge the forge refuses — a rule on the base, or a
// branch that moved after it was read — is 409 with the forge's reason.
func TestMerge_IsNeverForced(t *testing.T) {
	f, _ := servesPull(t, "hanzo", "hanzoai", "zoe")
	f.mergeable = false
	if _, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: runSession}); status(err) != http.StatusConflict {
		t.Fatalf("a conflict answered %v", err)
	}
	f.mergeable, f.state = true, "closed"
	if _, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: runSession}); status(err) != http.StatusConflict {
		t.Fatalf("a closed pull request answered %v", err)
	}
	if len(f.asked()) != 0 {
		t.Fatalf("the forge was asked to merge %v", f.asked())
	}
	f.state, f.refuse = "open", true
	if _, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: runSession}); status(err) != http.StatusConflict || f.state != "open" {
		t.Fatalf("a refused merge answered %v and left %s", err, f.state)
	}

	g, _ := servesPull(t, "hanzo", "hanzoai", "zoe")
	servesForge(t, &movingForge{pullForge: g})
	if _, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: runSession}); status(err) != http.StatusConflict {
		t.Fatalf("a branch that moved answered %v", err)
	}
	if g.state != "open" {
		t.Fatalf("a branch that moved was merged")
	}
}

// movingForge answers every read at one commit and every merge at another, as a
// forge does when a push lands between the two.
type movingForge struct{ *pullForge }

func (m *movingForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/merge") {
		m.mu.Lock()
		m.tip = "3333333333333333333333333333333333333333"
		m.mu.Unlock()
	}
	m.pullForge.ServeHTTP(w, r)
}

// ONLY THE RUN'S OWN PULL REQUEST IS MERGED: a session naming a fork's pull
// request from a branch of the run's name has nothing to merge, and a run the
// caller cannot see is not found — before the forge is asked to merge anything.
func TestMerge_OnlyTheRunsOwnPullRequest(t *testing.T) {
	f, sessions := servesPull(t, "hanzo", "hanzoai", "zoe")
	x := sessions[runSession]
	x.PR = f.url + "/hanzoai/site/pulls/9"
	sessions[runSession] = x
	if _, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: runSession}); status(err) != http.StatusConflict {
		t.Fatalf("a fork's pull request answered %v", err)
	}
	if _, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: "sess_not_yours"}); status(err) != http.StatusNotFound {
		t.Fatalf("another's run answered %v", err)
	}
	if len(f.asked()) != 0 {
		t.Fatalf("the forge was asked to merge %v", f.asked())
	}
}

// IN A MADE WORKSPACE EVERY RUN PROPOSES INTO THE DEFAULT BRANCH, even one that
// started from an earlier run's branch, so its proposal carries that run's work
// and one merge lands it. In the estate a proposal goes where it was asked.
func TestAFollowUpProposesIntoTheDefaultBranchInAMadeWorkspace(t *testing.T) {
	var (
		mu    sync.Mutex
		bases []string
	)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1")
		switch {
		case path == "/repos/acme/site" || path == "/repos/hanzoai/site":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "site", "default_branch": "trunk"})
		case strings.HasSuffix(path, "/pulls") && r.Method == http.MethodPost:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			mu.Lock()
			bases = append(bases, in["base"])
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"html_url": "https://git.test/pulls/1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	servesForge(t, h)
	ctx := context.Background()
	if _, err := propose(ctx, forge.Workspace{Owner: "acme", Made: true}, "", "site", "agent/aaa", "agent/bbb", "t", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := propose(ctx, forge.Workspace{Owner: "hanzoai"}, "zoe", "site", "agent/aaa", "agent/bbb", "t", "b"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(bases, " ") != "trunk agent/aaa" {
		t.Fatalf("proposed into %v", bases)
	}
}
