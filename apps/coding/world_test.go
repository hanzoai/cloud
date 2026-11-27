package coding

// world_test.go — a run's admission, end to end.
//
// Every peer admission reaches is a real zip app on a real socket, and the forge
// is a real HTTP server the production client talks to, as in plane_test.go and
// authz_test.go. What this forge adds is MEMORY: it knows which repositories
// exist, because whether a name is free is the question under test, and it
// records which repositories it was asked to make and as whom.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// world is everything a run's admission can change, as the fakes saw it.
type world struct {
	mu       sync.Mutex
	host     string
	orgs     map[string]string           // forge namespace → visibility
	repos    map[string]*wrepo           // "<owner>/<name>" on the forge
	made     []string                    // "<owner>/<name> as <sudo>", each repository the forge was asked to create
	keys     []string                    // "<owner>/<name>", each repository a run key was registered on
	sudo     []string                    // "<method> <path> as <sudo>", every request the forge answered
	projects map[string]client.ProjectIn // "<org>/<slug>" → what the project store was asked for
	titles   []string                    // each session opened, by title
	sessions int
	ended    chan string   // each repository a run gave its key back on
	hold     chan struct{} // when set, a run's key is given back only once it closes
	blind    bool          // the project store answers "not held" for every slug, as a race would

	// The project store's record of made workspaces, and what it was asked.
	book  *shelf
	asked []string          // "<org> make=<bool>", each workspace question
	lie   *client.Workspace // when set, the store answers this whatever it holds
}

// wrepo is one repository on the stand-in forge.
type wrepo struct{ private, actions bool }

// shelf is the project store's workspace record, with the uniqueness the real
// one keeps.
type shelf struct {
	mu sync.Mutex
	m  map[string]string
}

func (b *shelf) Read(_ context.Context, org string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.m[org], nil
}

func (b *shelf) Write(_ context.Context, org, owner string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if have := b.m[org]; have != "" {
		return have, nil
	}
	for o, n := range b.m {
		if n == owner && o != org {
			return "", forge.ErrExists
		}
	}
	b.m[org] = owner
	return owner, nil
}

// newWorld stands the peers up. held is what the estate's hanzoai namespace
// holds before the test begins.
func newWorld(t *testing.T, held ...string) *world {
	t.Helper()
	w := &world{
		orgs: map[string]string{"hanzoai": "public"}, repos: map[string]*wrepo{},
		projects: map[string]client.ProjectIn{}, ended: make(chan string, 16), book: &shelf{m: map[string]string{}},
	}
	for _, h := range held {
		w.repos["hanzoai/"+h] = &wrepo{private: true}
	}

	srv := httptest.NewServer(http.HandlerFunc(w.forge))
	t.Cleanup(srv.Close)
	w.host = strings.TrimPrefix(srv.URL, "http://")
	// GitHub knows no repository here, so nothing is read from the real one.
	gh := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gh.Close)
	wasAPI := githubAPI
	githubAPI = gh.URL
	t.Cleanup(func() { githubAPI = wasAPI })
	t.Setenv("CLOUD_FORGE_HOST", srv.URL)
	forge.Invalidate()
	t.Cleanup(forge.Invalidate)

	t.Setenv("ZIP_RUNTIME_DIR", socketDir(t))
	kms := zip.New(zip.Config{AppName: "kms", DisableStartupMessage: true})
	kms.Post("/kms/get", func(_ context.Context, in *client.SecretIn) (*client.Secret, error) {
		switch in.Ref {
		case forge.TokenRef:
			return &client.Secret{Value: []byte("machine")}, nil
		case forge.HostKeyRef:
			return &client.Secret{Value: []byte("git.test ssh-ed25519 AAAAPINNED")}, nil
		}
		return nil, zip.ErrNotFound("no such secret")
	}, zip.WithOperationID(client.KMSGet))

	iam := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	iam.Post("/iam/email", func(context.Context, *struct{}) (*client.Email, error) {
		e := verified("zoe@hanzo.ai")
		return &e, nil
	}, zip.WithOperationID(client.IAMEmail))

	agent := zip.New(zip.Config{AppName: "agent", DisableStartupMessage: true})
	agent.Post("/agents/session/open", func(_ context.Context, in *client.SessionOpenIn) (*client.SessionOpened, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.sessions++
		w.titles = append(w.titles, in.Title)
		return &client.SessionOpened{SessionID: fmt.Sprintf("sess_%012x", w.sessions)}, nil
	}, zip.WithOperationID(client.AgentsSessionOpen))
	agent.Post("/agents/session/event", func(context.Context, *client.SessionEventIn) (*client.CodingAck, error) {
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsSessionEvent))
	agent.Post("/agents/session/close", func(context.Context, *client.SessionCloseIn) (*client.CodingAck, error) {
		w.mu.Lock()
		hold := w.hold
		w.mu.Unlock()
		if hold != nil {
			<-hold
		}
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsSessionClose))

	// The project store, down to the answers admission reads: whether the org
	// holds a slug, a 409 — never a renumbered success — for one it does, and
	// the org's workspace, made on first need by the real forge.Provision
	// against the stand-in forge.
	project := zip.New(zip.Config{AppName: "project", DisableStartupMessage: true})
	project.Post("/projects/ownership", func(ctx context.Context, in *client.OwnerIn) (*client.Ownership, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		_, mine := w.projects[zip.CallerOf(ctx).Org+"/"+in.IDOrSlug]
		return &client.Ownership{Mine: mine && !w.blind}, nil
	}, zip.WithOperationID(client.ProjectsOwnership))
	project.Post("/projects/create", func(ctx context.Context, in *client.ProjectIn) (*client.ProjectOut, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		key := zip.CallerOf(ctx).Org + "/" + in.Slug
		if _, ok := w.projects[key]; ok {
			return nil, zip.ErrConflict("project slug already exists in this org")
		}
		w.projects[key] = *in
		return &client.ProjectOut{ID: "proj_" + in.Slug, Slug: in.Slug}, nil
	}, zip.WithOperationID(client.ProjectsCreate))
	project.Post("/projects/workspace", func(ctx context.Context, in *client.WorkspaceIn) (*client.Workspace, error) {
		org := zip.CallerOf(ctx).Org
		w.mu.Lock()
		w.asked = append(w.asked, fmt.Sprintf("%s make=%v", org, in.Make))
		lie := w.lie
		w.mu.Unlock()
		if lie != nil {
			return lie, nil
		}
		var ws forge.Workspace
		var err error
		if in.Make {
			m, merr := forge.New(srv.URL, "machine")
			if merr != nil {
				return nil, merr
			}
			ws, err = forge.Provision(ctx, m.Machine(), w.book, org)
		} else {
			ws, err = forge.Find(ctx, w.book, org)
		}
		if err != nil {
			return nil, err
		}
		return &client.Workspace{Owner: ws.Owner, Made: ws.Made}, nil
	}, zip.WithOperationID(client.ProjectsWorkspace))

	plane.Bind()
	for name, app := range map[string]*zip.App{"kms": kms, "iam": iam, "agent": agent, "project": project} {
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
	return w
}

// forge answers as Forgejo does for the calls admission makes: an org or a
// repository is there only once it has been made, a taken org name is 422, a run
// key is minted on a repository that is there, and the default branch refuses
// that key.
func (w *world) forge(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.Header.Get("Authorization") != "token machine" {
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, forge.API)
	sudo := r.Header.Get("Sudo")
	w.sudo = append(w.sudo, r.Method+" "+path+" as "+sudo)
	repo := func(key string) map[string]any {
		_, name, _ := strings.Cut(key, "/")
		p := w.repos[key]
		return map[string]any{
			"name": name, "full_name": key, "default_branch": "main", "private": p.private, "has_actions": p.actions,
			"ssh_url": "git@git.test:" + key + ".git", "permissions": map[string]any{"pull": true, "push": true},
		}
	}
	if login, ok := strings.CutPrefix(path, "/users/"); ok {
		_ = json.NewEncoder(rw).Encode(map[string]any{"login": login, "email": login + "@hanzo.ai"})
		return
	}
	if path == "/orgs" && r.Method == http.MethodPost {
		var in struct{ Username, Visibility string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if _, taken := w.orgs[in.Username]; taken {
			rw.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(rw).Encode(map[string]any{"message": "user already exists"})
			return
		}
		w.orgs[in.Username] = in.Visibility
		rw.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(rw).Encode(map[string]any{"username": in.Username, "visibility": in.Visibility})
		return
	}
	if rest, ok := strings.CutPrefix(path, "/orgs/"); ok {
		owner, sub, _ := strings.Cut(rest, "/")
		vis, there := w.orgs[owner]
		switch {
		case !there:
			rw.WriteHeader(http.StatusNotFound)
		case sub == "" && r.Method == http.MethodDelete:
			delete(w.orgs, owner)
			rw.WriteHeader(http.StatusNoContent)
		case sub == "":
			_ = json.NewEncoder(rw).Encode(map[string]any{"username": owner, "visibility": vis})
		case sub == "repos" && r.Method == http.MethodPost:
			var in struct {
				Name    string
				Private bool
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			key := owner + "/" + in.Name
			if _, taken := w.repos[key]; taken {
				rw.WriteHeader(http.StatusConflict)
				return
			}
			w.repos[key] = &wrepo{private: in.Private, actions: true}
			w.made = append(w.made, key+" as "+sudo)
			rw.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(rw).Encode(repo(key))
		case sub == "repos":
			var list []map[string]any
			for key := range w.repos {
				if strings.HasPrefix(key, owner+"/") {
					list = append(list, repo(key))
				}
			}
			rw.Header().Set("X-Total-Count", fmt.Sprint(len(list)))
			_ = json.NewEncoder(rw).Encode(list)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
		return
	}
	rest, ok := strings.CutPrefix(path, "/repos/")
	parts := strings.SplitN(rest, "/", 3)
	if !ok || len(parts) < 2 {
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	key, sub := parts[0]+"/"+parts[1], ""
	if len(parts) == 3 {
		sub = parts[2]
	}
	p := w.repos[key]
	if p == nil {
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	switch {
	case sub == "" && r.Method == http.MethodPatch:
		var in struct {
			Actions *bool `json:"has_actions"`
			Private *bool `json:"private"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Actions != nil {
			p.actions = *in.Actions
		}
		if in.Private != nil {
			p.private = *in.Private
		}
		_ = json.NewEncoder(rw).Encode(repo(key))
	case sub == "" && r.Method == http.MethodDelete:
		delete(w.repos, key)
		rw.WriteHeader(http.StatusNoContent)
	case sub == "":
		_ = json.NewEncoder(rw).Encode(repo(key))
	case sub == "teams":
		_ = json.NewEncoder(rw).Encode([]map[string]any{{"name": "owners", "permission": "owner"}})
	case strings.HasPrefix(sub, "collaborators"), sub == "keys" && r.Method == http.MethodGet:
		_ = json.NewEncoder(rw).Encode([]any{})
	case sub == "keys" && r.Method == http.MethodPost:
		w.keys = append(w.keys, key)
		rw.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(rw).Encode(map[string]any{"id": 7})
	case sub == "branch_protections" && r.Method == http.MethodPost:
		rw.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(rw).Encode(map[string]any{})
	case sub == "branch_protections/main":
		_ = json.NewEncoder(rw).Encode(map[string]any{"rule_name": "main", "enable_push": false, "enable_force_push": false})
	case strings.HasPrefix(sub, "keys/") && r.Method == http.MethodDelete:
		if w.hold != nil {
			w.mu.Unlock()
			<-w.hold
			w.mu.Lock()
		}
		w.ended <- key
		rw.WriteHeader(http.StatusNoContent)
	default:
		rw.WriteHeader(http.StatusNotFound)
	}
}

// start admits one run as zoe in the hanzo org, and waits for it to give its key
// back and release its pool slot before starting another run. The run itself
// fails at once, since nothing here is a sandbox, and that is
// not what is under test: admission is.
//
// The log is not the test's. The engine is the process's one Dispatcher and keeps
// the log it was first built with, so a t.Log handed to it outlives its test.
func (w *world) start(t *testing.T, in client.CodingStartIn) (Accepted, error) {
	t.Helper()
	return w.startIn(t, "hanzo", in)
}

// startIn is start as zoe in org.
func (w *world) startIn(t *testing.T, org string, in client.CodingStartIn) (Accepted, error) {
	t.Helper()
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "sub-zoe"})
	acc, err := Start(ctx, org, "sub-zoe", in, func(string, ...any) {})
	if err != nil {
		return acc, err
	}
	select {
	case <-w.ended:
	case <-time.After(20 * time.Second):
		t.Fatalf("the run for %s never gave its key back", acc.SessionID)
	}
	// Withdrawal happens before the deferred pool release. Waiting only for the
	// forge request races the next admission against the previous run's slot.
	if f := find(org, client.CodingSteerIn{SessionID: acc.SessionID}); f != nil {
		select {
		case <-f.done:
		case <-time.After(20 * time.Second):
			t.Fatalf("the run for %s never released its slot", acc.SessionID)
		}
	}
	return acc, nil
}

// A PROMPT ALONE RUNS IN AN EMPTY WORKSPACE: nothing is made on the forge or in
// the project store, no branch is named, and a follow-up starts from the same
// input — no repository — in the same sandbox.
func TestAPromptAloneRunsInAnEmptyWorkspace(t *testing.T) {
	w := newWorld(t)
	hold := make(chan struct{})
	w.hold = hold
	acc, err := Start(asCaller("sub-zoe"), "hanzo", "sub-zoe", client.CodingStartIn{Task: "Build a todo app"}, func(string, ...any) {})
	if err != nil {
		close(hold)
		t.Fatalf("refused: %v", err)
	}
	if acc.Repo != "" || acc.Branch != "" {
		close(hold)
		t.Fatalf("admitted as %+v, want no repository and no branch", acc)
	}
	flights.Lock()
	f := flights.m[acc.SessionID]
	flights.Unlock()
	close(hold)
	if f != nil {
		<-f.done
	}
	if f == nil || f.in.Repo != "" || f.in.Prompt != "Build a todo app" {
		t.Fatalf("a follow-up would start from %+v", f)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.projects) != 0 || len(w.made) != 0 || len(w.keys) != 0 {
		t.Fatalf("a run on no repository made %v, %v and keys %v", w.projects, w.made, w.keys)
	}
}

// A RUN GIVEN A REPO IS THE RUN IT ALWAYS WAS: no project, nothing made on the
// forge, the repo echoed back as given. `task` is the word it is asked in here.
func TestARunGivenARepoMakesNothingNew(t *testing.T) {
	w := newWorld(t, "api")
	acc, err := w.start(t, client.CodingStartIn{Repo: "api", Task: "fix the flake"})
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if acc.Repo != "api" {
		t.Fatalf("admitted as %+v, want repo api", acc)
	}
	if len(w.projects) != 0 || len(w.made) != 0 {
		t.Fatalf("a run on a repo made %v and %v", w.projects, w.made)
	}
	if fmt.Sprint(w.titles) != "[api: fix the flake]" {
		t.Fatalf("the session is not about the task: %v", w.titles)
	}
}

// PROMPT AND TASK ARE ONE FIELD. The same words under either name open the same
// run, and a prompt given beside a task is the one that is used.
func TestPromptAndTaskAreOneField(t *testing.T) {
	w := newWorld(t, "api")
	for _, in := range []client.CodingStartIn{
		{Repo: "api", Prompt: "add a health check"},
		{Repo: "api", Task: "add a health check"},
		{Repo: "api", Prompt: "add a health check", Task: "something else"},
	} {
		if _, err := w.start(t, in); err != nil {
			t.Fatalf("%+v was refused: %v", in, err)
		}
	}
	want := "[api: add a health check api: add a health check api: add a health check]"
	if fmt.Sprint(w.titles) != want {
		t.Fatalf("the three asks opened %v, want one run three times", w.titles)
	}
}

// NO TASK IS REFUSED, AND THE REFUSAL SAYS WHICH FIELD — before anything is made.
func TestARunWithNothingToDoIsRefused(t *testing.T) {
	w := newWorld(t, "api")
	for _, in := range []client.CodingStartIn{{}, {Repo: "api"}, {Prompt: "  ", Task: "\n"}} {
		_, err := w.start(t, in)
		if err == nil || !strings.Contains(err.Error(), "`prompt` (or `task`) is required") {
			t.Fatalf("%+v: refused with %v, want it to name prompt and task", in, err)
		}
	}
	if len(w.projects) != 0 || len(w.titles) != 0 {
		t.Fatalf("a run with no task made %v and opened %v", w.projects, w.titles)
	}
}

// A RUN ON THE ORG'S OWN MACHINE WITH NO REPO MAKES NOTHING: no repository or
// project is made for it, since it works in an empty directory on the machine
// (#779, machine_test.go). One that asks for what only a repository gives, a
// pull request, is refused for want of the repo.
func TestAMachineRunWithNoRepoMakesNothing(t *testing.T) {
	w := newWorld(t)
	const target = "tgt_0123456789abcdef0123456789abcdef"
	_, err := w.start(t, client.CodingStartIn{Prompt: "Build a todo app", TargetID: target, Pull: true})
	if err == nil || !strings.Contains(err.Error(), "needs the repo it works in to open a pull request") {
		t.Fatalf("refused with %v, want it to ask for the repo", err)
	}
	// This world links no machine, so the plain run is refused at the machine's
	// gate, never for its repo.
	_, err = w.start(t, client.CodingStartIn{Prompt: "Build a todo app", TargetID: target})
	if err == nil || strings.Contains(err.Error(), "needs the repo") {
		t.Fatalf("refused with %v, want the machine asked and no repo", err)
	}
	if len(w.projects) != 0 {
		t.Fatalf("a run on a machine with no repo made %v", w.projects)
	}
}
