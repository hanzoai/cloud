package projects

// workspace_test.go — the record of each org's code workspace, the plane op a
// coding run asks it through, and the template fork that copies into it.
//
// The forge here answers as this deployment's does for the calls these paths
// make: a taken org name is 422, a taken repository is 409, a copy the forge
// could not finish keeps its row, and a repository reports whether its Actions
// run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/runs"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// workshop is a forge with memory, across every namespace.
type workshop struct {
	*httptest.Server
	mu     sync.Mutex
	token  string
	orgs   map[string]string // name → visibility
	repos  map[string]*shed  // owner/name
	writes []string          // "METHOD path" of every request that could change something
	copies []map[string]any  // every migrate body
	broken bool              // fail every copy after its row exists
}

type shed struct {
	private, actions bool
	source           string
	keys             []runKey
}

// runKey is a deploy key on a stand-in repository.
type runKey struct {
	ID      int64
	Title   string
	Created time.Time
}

func newWorkshop(t *testing.T) *workshop {
	t.Helper()
	w := &workshop{token: "forge-machine-token", orgs: map[string]string{}, repos: map[string]*shed{}}
	w.Server = httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(w.Server.Close)
	return w
}

func (w *workshop) serve(rw http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+w.token {
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, forge.API)
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.Method != http.MethodGet {
		w.writes = append(w.writes, r.Method+" "+path)
	}
	fail := func(code int, msg string) {
		rw.WriteHeader(code)
		_ = json.NewEncoder(rw).Encode(map[string]any{"message": msg})
	}
	repoJSON := func(key string) map[string]any {
		_, name, _ := strings.Cut(key, "/")
		p := w.repos[key]
		return map[string]any{"name": name, "full_name": key, "private": p.private, "has_actions": p.actions,
			"default_branch": "main", "ssh_url": "git@git.test:" + key + ".git",
			"permissions": map[string]any{"admin": true, "push": true, "pull": true}}
	}
	switch {
	case path == "/orgs" && r.Method == http.MethodPost:
		var in struct{ Username, Visibility string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if _, taken := w.orgs[in.Username]; taken {
			fail(http.StatusUnprocessableEntity, "user already exists")
			return
		}
		w.orgs[in.Username] = in.Visibility
		rw.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(rw).Encode(map[string]any{"username": in.Username, "visibility": in.Visibility})

	case strings.HasPrefix(path, "/orgs/"):
		owner, sub, _ := strings.Cut(strings.TrimPrefix(path, "/orgs/"), "/")
		vis, there := w.orgs[owner]
		switch {
		case !there:
			fail(http.StatusNotFound, "no org")
		case sub == "" && r.Method == http.MethodDelete:
			delete(w.orgs, owner)
			rw.WriteHeader(http.StatusNoContent)
		case sub == "":
			_ = json.NewEncoder(rw).Encode(map[string]any{"username": owner, "visibility": vis})
		case sub == "repos" && r.Method == http.MethodGet:
			var names []string
			for key := range w.repos {
				if strings.HasPrefix(key, owner+"/") {
					names = append(names, key)
				}
			}
			slices.Sort(names)
			rw.Header().Set("X-Total-Count", fmt.Sprint(len(names)))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if limit <= 0 {
				limit = len(names)
			}
			page = max(page, 1)
			list := []map[string]any{}
			for i := (page - 1) * limit; i < len(names) && i < page*limit; i++ {
				list = append(list, repoJSON(names[i]))
			}
			_ = json.NewEncoder(rw).Encode(list)
		case sub == "repos" && r.Method == http.MethodPost:
			var in struct {
				Name    string
				Private bool
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			key := owner + "/" + in.Name
			if _, taken := w.repos[key]; taken {
				fail(http.StatusConflict, "repository exists")
				return
			}
			w.repos[key] = &shed{private: in.Private, actions: true}
			rw.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(rw).Encode(repoJSON(key))
		default:
			fail(http.StatusNotFound, "no route")
		}

	case path == "/repos/migrate":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.copies = append(w.copies, in)
		key := fmt.Sprint(in["repo_owner"]) + "/" + fmt.Sprint(in["repo_name"])
		if _, taken := w.repos[key]; taken {
			fail(http.StatusConflict, "The repository with the same name already exists.")
			return
		}
		w.repos[key] = &shed{private: in["private"] == true, actions: true, source: fmt.Sprint(in["clone_addr"])}
		if w.broken {
			fail(http.StatusUnprocessableEntity, "Migration failed: fatal: could not read from remote.")
			return
		}
		rw.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(rw).Encode(repoJSON(key))

	case strings.HasPrefix(path, "/repos/"):
		parts := strings.SplitN(strings.TrimPrefix(path, "/repos/"), "/", 3)
		if len(parts) < 2 {
			fail(http.StatusNotFound, "no repo")
			return
		}
		key, sub := parts[0]+"/"+parts[1], ""
		if len(parts) == 3 {
			sub = parts[2]
		}
		p := w.repos[key]
		if p == nil {
			fail(http.StatusNotFound, "no repo")
			return
		}
		switch {
		case sub == "" && r.Method == http.MethodDelete:
			delete(w.repos, key)
			rw.WriteHeader(http.StatusNoContent)
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
			_ = json.NewEncoder(rw).Encode(repoJSON(key))
		case sub == "":
			_ = json.NewEncoder(rw).Encode(repoJSON(key))
		case sub == "teams":
			_ = json.NewEncoder(rw).Encode([]map[string]any{{"name": "Owners", "permission": "owner"}})
		case sub == "keys" && r.Method == http.MethodGet:
			out := []map[string]any{}
			if r.URL.Query().Get("page") == "1" {
				for _, k := range p.keys {
					out = append(out, map[string]any{"id": k.ID, "title": k.Title, "created_at": k.Created})
				}
			}
			_ = json.NewEncoder(rw).Encode(out)
		case strings.HasPrefix(sub, "keys/") && r.Method == http.MethodDelete:
			id, _ := strconv.ParseInt(strings.TrimPrefix(sub, "keys/"), 10, 64)
			p.keys = slices.DeleteFunc(p.keys, func(k runKey) bool { return k.ID == id })
			rw.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(sub, "collaborators"):
			_ = json.NewEncoder(rw).Encode([]any{})
		case sub == "branch_protections" && r.Method == http.MethodPost:
			rw.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(rw).Encode(map[string]any{})
		default:
			fail(http.StatusNotFound, "no route")
		}

	default:
		fail(http.StatusNotFound, "no route")
	}
}

// in reports, under the lock, what the forge holds and was asked.
func (w *workshop) in(f func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f()
}

// touched is every write that addressed the namespace owner.
func (w *workshop) touched(owner string) []string {
	var out []string
	w.in(func() {
		for _, s := range w.writes {
			_, p, _ := strings.Cut(s, " ")
			if p == "/orgs/"+owner || strings.HasPrefix(p, "/orgs/"+owner+"/") || strings.HasPrefix(p, "/repos/"+owner+"/") {
				out = append(out, s)
			}
		}
	})
	return out
}

// mountForking mounts the projects surface beside the stand-in forge, holding
// its machine credential in KMS, with the git app listening so a new project's
// catalogue copy settles.
func mountForking(t *testing.T) (*zip.App, *workshop) {
	t.Helper()
	listen(t)
	w := newWorkshop(t)
	t.Setenv("CLOUD_FORGE_HOST", w.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	// The pace is the deployment's, kept in the process's Coord: a test gets one of its
	// own, or the copies an earlier test took would be counted against this one.
	coordtest.Default(t, coord.Alone())
	if err := Use(app, cloud.Deps{KMS: vault{token: w.token}, MasterKey: testMaster}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app, w
}

func forked(t *testing.T, app *zip.App, org string, body map[string]any) projectsProject {
	t.Helper()
	code, out := do(t, app, http.MethodPost, "/v1/project/fork", org, body)
	if code != http.StatusCreated {
		t.Fatalf("fork %v as %s want 201, got %d (%s)", body, org, code, out)
	}
	var p projectsProject
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("fork json: %v (%s)", err, out)
	}
	return p
}

// A TEMPLATE FORK IS THE ORG'S OWN COPY. The org's workspace is made (private),
// the template's source is copied into it as a new private repository with
// Actions off — cloned as plain git, not mirrored — and the project builds from
// the copy, never from the template everybody forks.
func TestForkingATemplateCopiesItIntoTheOrgsWorkspace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		p := forked(t, app, "acme", map[string]any{"slug": "synapse"})

		host := strings.TrimPrefix(w.URL, "http://")
		if p.Repo.URL != "https://"+host+"/acme/synapse.git" {
			t.Fatalf("the project builds from %q, want the org's own copy", p.Repo.URL)
		}
		if p.Repo.Branch != "main" || p.ForkedFrom != "synapse" || p.Org != "acme" {
			t.Fatalf("project = %+v", p)
		}
		w.in(func() {
			if w.orgs["acme"] != "private" {
				t.Fatalf("the workspace is %q, want private", w.orgs["acme"])
			}
			c := w.repos["acme/synapse"]
			if c == nil || !c.private || c.actions || c.source != "https://github.com/hanzo-apps/synapse" {
				t.Fatalf("the copy is %+v", c)
			}
			if len(w.copies) != 1 || w.copies[0]["mirror"] != false || w.copies[0]["service"] != "git" {
				t.Fatalf("copies: %v", w.copies)
			}
		})
		if got, err := mounted.State.store.book().Read(t.Context(), "acme"); err != nil || got != "acme" {
			t.Fatalf("the record says %q, %v", got, err)
		}
	})
}

// TWO ORGS, ONE TEMPLATE, TWO COPIES: neither org's project points at the other's
// repository or at the template's.
func TestTwoOrgsForkingOneTemplateGetTwoCopies(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		a := forked(t, app, "acme", map[string]any{"slug": "synapse"})
		g := forked(t, app, "globex", map[string]any{"slug": "synapse"})
		// Names are one namespace across orgs, so the second copy takes the next one.
		if a.Repo.URL == g.Repo.URL || !strings.HasSuffix(a.Repo.URL, "/acme/synapse.git") || !strings.HasSuffix(g.Repo.URL, "/globex/synapse-2.git") {
			t.Fatalf("acme builds from %q and globex from %q", a.Repo.URL, g.Repo.URL)
		}
		w.in(func() {
			if w.repos["acme/synapse"] == nil || w.repos["globex/synapse-2"] == nil {
				t.Fatalf("repositories: %v", w.repos)
			}
		})
	})
}

// A NAME THE WORKSPACE ALREADY HOLDS IS NEVER ADOPTED: the copy takes the next
// number and the repository that was there is not written.
func TestACopyNeverAdoptsARepositoryThatIsThere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		forked(t, app, "acme", map[string]any{"slug": "saas-landing", "target": "first"})
		w.in(func() { w.repos["acme/synapse"] = &shed{private: true} })
		mark := len(w.touched("acme"))

		p := forked(t, app, "acme", map[string]any{"slug": "synapse"})
		if !strings.HasSuffix(p.Repo.URL, "/acme/synapse-2.git") {
			t.Fatalf("the copy went to %q", p.Repo.URL)
		}
		for _, s := range w.touched("acme")[mark:] {
			if strings.Contains(s, "/repos/acme/synapse/") || strings.HasSuffix(s, "/repos/acme/synapse") {
				t.Fatalf("the repository that was there was written: %s", s)
			}
		}
	})
}

// A COPY THAT FAILS FAILS THE FORK, plainly, and leaves no project and no
// repository behind.
func TestAFailedCopyLeavesNothingBehind(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		w.in(func() { w.broken = true })
		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "acme", map[string]any{"slug": "synapse"})
		if code != http.StatusBadGateway || !strings.Contains(string(body), "could not copy the template") {
			t.Fatalf("a failed copy answered %d (%s)", code, body)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/project/synapse", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("a project was left behind: %d", code)
		}
		w.in(func() {
			if _, left := w.repos["acme/synapse"]; left {
				t.Fatal("the failed copy's repository was left behind")
			}
		})
	})
}

// A NAMED SLUG THE ORG HOLDS IS A 409 BEFORE ANYTHING IS COPIED.
func TestATakenSlugIsRefusedBeforeACopy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		var before int
		w.in(func() { before = len(w.copies) })
		if code, _ := do(t, app, http.MethodPost, "/v1/project/fork", "acme", map[string]any{"slug": "synapse", "target": "synapse"}); code != http.StatusConflict {
			t.Fatalf("a taken slug answered %d", code)
		}
		w.in(func() {
			if len(w.copies) != before {
				t.Fatalf("a refused fork copied: %v", w.copies[before:])
			}
		})
	})
}

// A LIVE PROJECT'S FORK IS UNCHANGED: it links the parent's repository and
// copies nothing, because copying another org's repository with the machine's
// authority would step over that org's own access rules.
func TestALiveForkCopiesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		ex := mkProject("hanzo", "example-kanban", "Example Kanban")
		ex.Status, ex.RepoURL, ex.RepoBranch = "live", "https://github.com/hanzo-templates/kanban-board", "main"
		if err := mounted.State.store.CreateProject(context.Background(), ex); err != nil {
			t.Fatalf("seed: %v", err)
		}
		p := forked(t, app, "acme", map[string]any{"slug": "example-kanban", "target": "my-board"})
		if p.Repo.URL != ex.RepoURL {
			t.Fatalf("a live fork builds from %q, want its parent's %q", p.Repo.URL, ex.RepoURL)
		}
		w.in(func() {
			if len(w.copies) != 0 {
				t.Fatalf("a live fork copied: %v", w.copies)
			}
			if _, made := w.orgs["acme"]; made {
				t.Fatal("a live fork made a workspace")
			}
		})
	})
}

// THE ESTATE FORKS AS IT ALWAYS HAS: the template's own repository, no copy, no
// namespace made for the org. The project's own source is published to
// hanzo-community in the background (visibility.go), which makes THAT org when it is
// the first to publish, so the test names the estate's and not "any org".
func TestTheEstateForksAsBefore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		p := forked(t, app, "hanzo", map[string]any{"slug": "synapse"})
		if p.Repo.URL != "https://github.com/hanzo-apps/synapse" {
			t.Fatalf("the estate's fork builds from %q", p.Repo.URL)
		}
		w.in(func() {
			if _, made := w.orgs["hanzo"]; made || len(w.copies) != 0 {
				t.Fatalf("the estate's fork made %v / %v", w.copies, w.orgs)
			}
		})
	})
}

// THE WORKSPACE IS THE CALLER'S ORG'S, and only the caller's: the op takes no
// org, reads it off the call, makes it only when asked, and answers the estate
// from its table without the forge.
func TestTheWorkspaceIsTheCallersOrgs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, w := mountForking(t)
		ask := func(org string, create bool) (*client.Workspace, error) {
			ctx := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "u_" + org})
			return planeWorkspace(ctx, &client.WorkspaceIn{Make: create})
		}
		if got, err := ask("acme", false); err != nil || *got != (client.Workspace{}) {
			t.Fatalf("an org with none, read = %+v, %v", got, err)
		}
		if got, err := ask("acme", true); err != nil || *got != (client.Workspace{Owner: "acme", Made: true}) {
			t.Fatalf("made = %+v, %v", got, err)
		}
		if got, err := ask("globex", false); err != nil || *got != (client.Workspace{}) {
			t.Fatalf("another org read acme's: %+v, %v", got, err)
		}
		if got, err := ask("acme", false); err != nil || got.Owner != "acme" {
			t.Fatalf("acme reads %+v, %v", got, err)
		}
		var before int
		w.in(func() { before = len(w.writes) })
		if got, err := ask("hanzo", true); err != nil || *got != (client.Workspace{Owner: "hanzoai"}) {
			t.Fatalf("the estate = %+v, %v", got, err)
		}
		w.in(func() {
			if len(w.writes) != before {
				t.Fatalf("the estate's workspace wrote to the forge: %v", w.writes[before:])
			}
		})
		for _, org := range []string{"", "a/b", ".."} {
			if _, err := ask(org, true); err == nil {
				t.Fatalf("org %q was answered", org)
			}
		}
	})
}

// ONE NAMESPACE PER ORG, ONE ORG PER NAMESPACE, first write wins.
func TestTheBookKeepsOneNamespacePerOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, _ = mountForking(t)
		b := mounted.State.store.book()
		ctx := t.Context()
		if got, err := b.Write(ctx, "acme", "acme"); err != nil || got != "acme" {
			t.Fatalf("first write = %q, %v", got, err)
		}
		if got, err := b.Write(ctx, "acme", "acme-2"); err != nil || got != "acme" {
			t.Fatalf("a second write for acme = %q, %v; the first must stand", got, err)
		}
		if _, err := b.Write(ctx, "globex", "acme"); !errors.Is(err, forge.ErrExists) {
			t.Fatalf("globex took acme's namespace: %v", err)
		}
		if got, err := b.Read(ctx, "globex"); err != nil || got != "" {
			t.Fatalf("globex reads %q, %v", got, err)
		}
	})
}

// TAKING TEMPLATES IS PACED PER ORG: past the rate a fork is refused plainly,
// before any workspace or copy is made, and another org is not held back by it.
func TestTemplateCopiesArePacedPerOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		for i := range copyRate {
			forked(t, app, "acme", map[string]any{"slug": "synapse", "target": fmt.Sprintf("copy-%d", i)})
		}
		var before int
		w.in(func() { before = len(w.copies) })
		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "acme", map[string]any{"slug": "synapse", "target": "one-too-many"})
		if code != http.StatusTooManyRequests || !strings.Contains(string(body), "the most it may") {
			t.Fatalf("a fork past the pace answered %d (%s)", code, body)
		}
		w.in(func() {
			if len(w.copies) != before {
				t.Fatalf("a paced-out fork copied: %v", w.copies[before:])
			}
		})
		forked(t, app, "globex", map[string]any{"slug": "synapse"})
	})
}

// THE PACE FORGETS WHAT IS OLDER THAN ITS WINDOW.
func TestPaceForgetsOldCopies(t *testing.T) {
	p := &pace{pods: coord.Alone(), rate: copyRate, window: 200 * time.Millisecond}
	ctx := t.Context()
	for i := range copyRate {
		if !p.take(ctx, "acme") {
			t.Fatalf("copy %d was refused inside the rate", i)
		}
	}
	if p.take(ctx, "acme") {
		t.Fatal("a copy past the rate was allowed")
	}
	time.Sleep(250 * time.Millisecond)
	if !p.take(ctx, "acme") {
		t.Fatal("a copy after the window was refused")
	}
}

// THE PACE IS THE DEPLOYMENT'S: an org that has taken its rate through one replica
// is refused by the next, and another org is not held back by it. Three replicas
// each counting for themselves would let an org take the rate three times over.
//
// It runs on the in-process KV; the KV driver's own tests (internal/driver/kv) hold
// the same counts to a real server.
//
// MUTATION: keep the copies in the replica again (a slice of times under a mutex)
// and each replica admits its own rate.
func TestPaceIsTheDeploymentsAcrossReplicas(t *testing.T) {
	t.Run("in-process KV", func(t *testing.T) { sharePace(t, coordtest.Memory(t)) })
}

func sharePace(t *testing.T, pod func(id string) *coord.Coord) {
	a := &pace{pods: pod("cloud-0"), rate: copyRate, window: copyWindow}
	b := &pace{pods: pod("cloud-1"), rate: copyRate, window: copyWindow}
	ctx := t.Context()
	for i := range copyRate {
		replica := a
		if i%2 == 1 {
			replica = b
		}
		if !replica.take(ctx, "acme") {
			t.Fatalf("copy %d was refused inside the rate", i)
		}
	}
	if a.take(ctx, "acme") || b.take(ctx, "acme") {
		t.Fatal("a copy past the rate was allowed on a replica that had taken none of it")
	}
	if !b.take(ctx, "globex") {
		t.Fatal("another org was held back by acme's copies")
	}
}

// A FULL WORKSPACE REFUSES A TEMPLATE PLAINLY and leaves no project behind.
func TestAFullWorkspaceRefusesACopy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		w.in(func() {
			for i := range 100 {
				w.repos[fmt.Sprintf("acme/r%d", i)] = &shed{private: true}
			}
		})
		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "acme", map[string]any{"slug": "prism"})
		if code != http.StatusConflict || !strings.Contains(string(body), "holds as many repositories as it may") {
			t.Fatalf("a fork into a full workspace answered %d (%s)", code, body)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/project/prism", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("a refused fork left a project: %d", code)
		}
	})
}

// A process that cannot ask the processes executing runs (no shared coord) cannot tell
// a run that is working from one that is gone, and withdraws nothing.
func TestReclaimWithdrawsNothingItCannotCheck(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, w := mountForking(t)
		ctx := t.Context()
		if _, err := mounted.State.store.book().Write(ctx, "acme", "acme"); err != nil {
			t.Fatal(err)
		}
		w.in(func() {
			w.orgs["acme"] = "private"
			w.repos["acme/site"] = &shed{private: true, keys: []runKey{
				{ID: 1, Title: "hanzo-run-sess_old", Created: time.Now().Add(-3 * time.Hour)},
			}}
		})
		reclaim(mounted, ctx)
		w.in(func() {
			if got := w.repos["acme/site"].keys; len(got) != 1 {
				t.Fatalf("a key was withdrawn with no way to ask who is working: %v", got)
			}
		})
	})
}

// RUN KEYS A KILLED PROCESS LEFT IN A MADE WORKSPACE ARE WITHDRAWN when the
// store mounts: only the run path's own keys, only those of runs no process is
// executing (a run may work for hours, and the pod that mounts may be booting beside
// pods that are still up), only in namespaces the record names and the estate does
// not keep.
func TestReclaimWithdrawsLeftoverRunKeysInMadeWorkspaces(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, w := mountForking(t)
		ctx := t.Context()
		// This pod, and another that executes one run: the process that executes runs
		// answers what it holds.
		pod := coordtest.Memory(t)
		mounted.Coord = pod("cloud-0")
		defer pod("cloud-1").Serve(runs.Held, func(context.Context, string, []byte) ([]byte, bool) {
			return runs.Answer([]string{"sess_working"})
		})()
		b := mounted.State.store.book()
		for org, owner := range map[string]string{"acme": "acme", "globex": "globex-2"} {
			if _, err := b.Write(ctx, org, owner); err != nil {
				t.Fatalf("record %s: %v", org, err)
			}
		}
		// A record naming a kept name, as a hand edit might have left: never swept.
		if _, err := mounted.State.store.db.Exec(`INSERT INTO workspaces (org, owner, created_at) VALUES ('mallory', 'zenlm', 1)`); err != nil {
			t.Fatalf("seed: %v", err)
		}
		old, fresh := time.Now().Add(-3*time.Hour), time.Now()
		w.in(func() {
			for _, o := range []string{"acme", "globex-2", "zenlm"} {
				w.orgs[o] = "private"
			}
			w.repos["acme/site"] = &shed{private: true, keys: []runKey{
				{ID: 1, Title: "hanzo-run-sess_old", Created: old},
				{ID: 2, Title: "hanzo-run-sess_live", Created: fresh},
				{ID: 3, Title: "deploy from ci", Created: old},
				{ID: 6, Title: "hanzo-run-sess_working", Created: old},
			}}
			w.repos["globex-2/app"] = &shed{private: true, keys: []runKey{{ID: 4, Title: "hanzo-run-sess_gone", Created: old}}}
			w.repos["zenlm/model"] = &shed{private: true, keys: []runKey{{ID: 5, Title: "hanzo-run-sess_x", Created: old}}}
		})

		reclaim(mounted, ctx)

		w.in(func() {
			ids := func(key string) []int64 {
				var out []int64
				for _, k := range w.repos[key].keys {
					out = append(out, k.ID)
				}
				return out
			}
			if got := ids("acme/site"); fmt.Sprint(got) != "[2 3 6]" {
				t.Fatalf("acme/site keeps %v, want the keys of the run starting and the run working on another pod, and the person's", got)
			}
			if got := ids("globex-2/app"); len(got) != 0 {
				t.Fatalf("globex-2/app keeps %v", got)
			}
			if got := ids("zenlm/model"); fmt.Sprint(got) != "[5]" {
				t.Fatalf("a namespace the estate keeps was swept: %v", got)
			}
		})
	})
}
