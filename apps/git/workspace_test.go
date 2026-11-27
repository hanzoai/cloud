package git

// workspace_test.go — an org's codebases when its code lives in a workspace the
// platform made on the forge: setting it up for the person who opens it, listing
// and reading its repositories, and making a new one there.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountAs mounts the app behind a stand-in for the identity boundary that
// attests every request's X-User-Id as a principal of kind, holding its own
// token unless held is false (an IAM as() token).
func mountAs(t *testing.T, kind principal.Kind, held ...bool) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(zip.H(func(c *zip.Ctx) error {
		if u := c.Header("X-User-Id"); u != "" {
			holder := c.Header("X-Org-Id") + "/" + u
			if len(held) > 0 && !held[0] {
				holder = ""
			}
			principal.Mint(c, principal.Principal{Org: c.Header("X-Org-Id"), User: u, Subject: u, Kind: kind, Stable: true, Holder: holder})
		}
		return c.Next()
	}))
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_DOMAIN", "api.hanzo.test")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// doAs is do with extra headers, as the boundary would mint them.
func doAs(t *testing.T, app *zip.App, method, path, org string, head map[string]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	for k, v := range head {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// deskForge holds acme/site and acme/webby-ai_intel-hub, readable by the
// machine and by no person.
type deskForge struct {
	mu    sync.Mutex
	asked []string
	// gone is what a delete took away; the forge then answers it as absent.
	gone map[string]bool
}

func (f *deskForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.asked = append(f.asked, r.Method+" "+strings.TrimPrefix(r.URL.Path, forge.API)+" as "+r.Header.Get("Sudo"))
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "token machine" || r.Header.Get("Sudo") != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	site := map[string]any{"name": "site", "full_name": "acme/site", "description": "the site", "private": true,
		"default_branch": "main", "clone_url": "https://git.hanzo.test/acme/site.git", "ssh_url": "git@git.hanzo.test:acme/site.git",
		"size": 12, "updated_at": "2026-10-04T10:00:00Z"}
	hub := map[string]any{"name": "webby-ai_intel-hub", "full_name": "acme/webby-ai_intel-hub", "private": true,
		"default_branch": "main", "clone_url": "https://git.hanzo.test/acme/webby-ai_intel-hub.git", "updated_at": "2026-10-03T10:00:00Z"}
	switch path := strings.TrimPrefix(r.URL.Path, forge.API); {
	case path == "/orgs/acme/repos":
		w.Header().Set("X-Total-Count", "2")
		if r.URL.Query().Get("page") == "1" {
			_ = json.NewEncoder(w).Encode([]map[string]any{site, hub})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	case path == "/repos/acme/site":
		f.mu.Lock()
		gone := f.gone["site"]
		if r.Method == http.MethodDelete {
			if f.gone == nil {
				f.gone = map[string]bool{}
			}
			f.gone["site"] = true
		}
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodDelete && !gone:
			w.WriteHeader(http.StatusNoContent)
		case gone:
			w.WriteHeader(http.StatusNotFound)
		default:
			_ = json.NewEncoder(w).Encode(site)
		}
	case path == "/repos/acme/site/branches":
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "main"}, {"name": "agent/abc"}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *deskForge) read() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// desk is what a test sees of the stand-ins: which workspaces were asked about
// (and whether one was to be made), and what was made and enrolled.
type desk struct {
	mu       sync.Mutex
	asked    []string
	made     []string
	enrolled []string
	email    client.Email
	enrolErr error
}

// servesDesk stands the forge up and answers acme's workspace as made.
func servesDesk(t *testing.T) (*deskForge, *desk) {
	t.Helper()
	f := &deskForge{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	d := &desk{email: client.Email{Address: "josh@webby.example", Verified: true, Name: "Josh"}}
	wasWS, wasSrc, wasMake, wasEnrol, wasEmail := madeWorkspace, madeSource, madeRepo, madeEnrol, madeEmail
	madeWorkspace = func(org string, create bool) (forge.Workspace, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.asked = append(d.asked, org+" make="+strconv.FormatBool(create))
		if org == "acme" {
			return forge.Workspace{Owner: "acme", Made: true}, nil
		}
		return forge.Workspace{}, nil
	}
	madeSource = func(context.Context) (*forge.Client, error) { return forge.New(srv.URL, "machine") }
	madeRepo = func(_ context.Context, m *forge.Client, owner, name, description string) (forge.Repo, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if m.Actor() != "" {
			t.Errorf("a repository was made as %q", m.Actor())
		}
		d.made = append(d.made, owner+"/"+name+" "+description)
		return forge.Repo{Name: name, FullName: owner + "/" + name, Description: description, Private: true, Branch: "main",
			Clone: "https://git.hanzo.test/" + owner + "/" + name + ".git"}, nil
	}
	madeEnrol = func(_ context.Context, m *forge.Client, owner string, p forge.Person) (forge.Enrolled, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.enrolled = append(d.enrolled, owner+" "+p.Subject+" "+p.Address+" "+p.Name)
		if d.enrolErr != nil {
			return forge.Enrolled{}, d.enrolErr
		}
		return forge.Enrolled{Login: "josh", Made: true, Seated: true}, nil
	}
	madeEmail = func(context.Context) (*client.Email, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		e := d.email
		return &e, nil
	}
	t.Cleanup(func() {
		madeWorkspace, madeSource, madeRepo, madeEnrol, madeEmail = wasWS, wasSrc, wasMake, wasEnrol, wasEmail
	})
	return f, d
}

// OPENING THE WORKSPACE SETS IT UP FOR THE CALLER: the org's namespace is made
// when it has none, and the caller is enrolled there under their own subject
// and the verified address IAM holds — never anything the request sent.
func TestOpeningTheWorkspaceEnrolsTheCaller(t *testing.T) {
	app := mountAs(t, principal.Person)
	_, d := servesDesk(t)
	code, body := do(t, app, http.MethodPost, "/v1/git/workspace", "acme", map[string]any{"subject": "someone-else", "org": "hanzo"})
	if code != http.StatusOK {
		t.Fatalf("workspace: %d %s", code, body)
	}
	var got workspaceView
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got != (workspaceView{Owner: "acme", Made: true, Login: "josh"}) {
		t.Fatalf("workspace = %+v", got)
	}
	if strings.Join(d.asked, ",") != "acme make=true" {
		t.Fatalf("workspaces asked about: %v", d.asked)
	}
	if strings.Join(d.enrolled, ",") != "acme u_acme josh@webby.example Josh" {
		t.Fatalf("enrolled: %v", d.enrolled)
	}
}

// AN UNVERIFIED ADDRESS IS NOT AN IDENTITY: nothing is enrolled, and the
// refusal says what to do.
func TestAnUnverifiedAddressIsNotEnrolled(t *testing.T) {
	app := mountAs(t, principal.Person)
	_, d := servesDesk(t)
	d.email.Verified = false
	code, body := do(t, app, http.MethodPost, "/v1/git/workspace", "acme", nil)
	if code != http.StatusForbidden || !strings.Contains(string(body), "verify") {
		t.Fatalf("answered %d %s", code, body)
	}
	if len(d.enrolled) != 0 || len(d.asked) != 0 {
		t.Fatalf("enrolled %v, asked %v", d.enrolled, d.asked)
	}
}

// A SEAT IS A PERSON'S: an API key, an application, a request no boundary
// attested and a token acting for a person are refused before anything is made.
func TestOnlyAPersonTakesASeat(t *testing.T) {
	for i, kind := range []principal.Kind{principal.Key, principal.App, "", principal.Person} {
		app := mountAs(t, kind, i < 3) // the last is a person's token held by someone acting for them
		_, d := servesDesk(t)
		if code, body := do(t, app, http.MethodPost, "/v1/git/workspace", "acme", nil); code != http.StatusForbidden {
			t.Fatalf("%q answered %d %s", kind, code, body)
		}
		if len(d.enrolled) != 0 || len(d.asked) != 0 {
			t.Fatalf("%q: enrolled %v, asked %v", kind, d.enrolled, d.asked)
		}
	}
}

// A SUPERADMIN IN A CUSTOMER'S ORG READS ITS WORKSPACE AS IT IS: nothing is
// made, nobody is seated, and no repository is made there.
func TestSupportModeMakesNothing(t *testing.T) {
	app := mountAs(t, principal.Person)
	_, d := servesDesk(t)
	head := map[string]string{"X-User-IsAdmin": "true", "X-User-Owner": "admin"}
	code, body := doAs(t, app, http.MethodPost, "/v1/git/workspace", "acme", head)
	var got workspaceView
	_ = json.Unmarshal(body, &got)
	if code != http.StatusOK || got != (workspaceView{Owner: "acme", Made: true}) {
		t.Fatalf("answered %d %s", code, body)
	}
	if code, body := doAs(t, app, http.MethodPost, "/v1/git/workspace", "globex", head); code != http.StatusOK || strings.Contains(string(body), "login") {
		t.Fatalf("an org with none answered %d %s", code, body)
	}
	if strings.Join(d.asked, ",") != "acme make=false,globex make=false" || len(d.enrolled) != 0 {
		t.Fatalf("asked %v, enrolled %v", d.asked, d.enrolled)
	}
}

// AN ADDRESS ANOTHER SIGN-IN HOLDS ON THE FORGE IS A CONFLICT the person can
// read.
func TestAnAddressHeldElsewhereIsAConflict(t *testing.T) {
	app := mountAs(t, principal.Person)
	_, d := servesDesk(t)
	d.enrolErr = forge.ErrAddressHeld
	if code, body := do(t, app, http.MethodPost, "/v1/git/workspace", "acme", nil); code != http.StatusConflict {
		t.Fatalf("answered %d %s", code, body)
	}
}

// A SEAT THAT COULD NOT BE SET UP SAYS WHY, in a status the edge passes through.
// The edge replaces an origin's 502 with a page of its own, with no CORS header and
// none of the reason, which the browser seating its person read as "Failed to
// fetch". The forge refusing the account, and IAM not answering, each reach the
// person as what they are.
func TestASeatThatCouldNotBeSetUpSaysWhy(t *testing.T) {
	app := mountAs(t, principal.Person)
	_, d := servesDesk(t)
	d.enrolErr = errors.New("forge: make the account of u_acme: the login source is not held")
	code, body := do(t, app, http.MethodPost, "/v1/git/workspace", "acme", nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "the login source is not held") {
		t.Fatalf("a refused seat answered %d %s", code, body)
	}
	was := madeEmail
	madeEmail = func(context.Context) (*client.Email, error) { return nil, errors.New("iam: no answer") }
	t.Cleanup(func() { madeEmail = was })
	code, body = do(t, app, http.MethodPost, "/v1/git/workspace", "acme", nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "your account could not be read") {
		t.Fatalf("an unread account answered %d %s", code, body)
	}
}

// THE ESTATE'S WORKSPACE IS THE FORGE ACL'S: it is answered from the closed
// table, no record is asked about and nobody is enrolled.
func TestTheEstatesWorkspaceIsNotEnrolled(t *testing.T) {
	app := mountAs(t, principal.Person)
	_, d := servesDesk(t)
	code, body := do(t, app, http.MethodPost, "/v1/git/workspace", "hanzo", nil)
	var got workspaceView
	_ = json.Unmarshal(body, &got)
	if code != http.StatusOK || got != (workspaceView{Owner: "hanzoai"}) {
		t.Fatalf("answered %d %s", code, body)
	}
	if len(d.asked) != 0 || len(d.enrolled) != 0 {
		t.Fatalf("asked %v, enrolled %v", d.asked, d.enrolled)
	}
}

// THE ORG'S CODEBASES ARE ITS WORKSPACE'S REPOSITORIES, read as the machine in
// its own namespace, beside any repository this app holds for it.
func TestAWorkspaceOrgListsItsForgeRepositories(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, d := servesDesk(t)
		s := mounted.Load()
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if err := provision(s, context.Background(), st, Repo{ID: "repo_held", Org: "acme", Name: "held", DefaultBranch: "main", CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		code, body := do(t, app, http.MethodGet, "/v1/git/repos", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d %s", code, body)
		}
		var list repoList
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, r := range list.Data {
			names = append(names, r.Name)
		}
		if strings.Join(names, ",") != "site,webby-ai_intel-hub,held" {
			t.Fatalf("listed %v", names)
		}
		site := list.Data[0]
		if site.Org != "acme" || site.Description != "the site" || site.DefaultBranch != "main" || site.Public ||
			site.CloneURL != "https://git.hanzo.test/acme/site.git" || site.UpdatedAt != "2026-10-04T10:00:00Z" || site.SizeBytes != 12*1024 {
			t.Fatalf("site = %+v", site)
		}
		for _, a := range f.read() {
			if !strings.HasPrefix(a, "GET /orgs/acme/repos as ") {
				t.Fatalf("the forge was asked %s", a)
			}
		}
		if strings.Join(d.asked, ",") != "acme make=false" {
			t.Fatalf("workspaces asked about: %v", d.asked)
		}
	})
}

// AN ORG WITH NO WORKSPACE YET LISTS WHAT THIS APP HOLDS, and makes nothing.
func TestAnOrgWithNoWorkspaceListsWhatIsHeld(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, d := servesDesk(t)
		code, body := do(t, app, http.MethodGet, "/v1/git/repos", "globex", nil)
		if code != http.StatusOK || strings.TrimSpace(string(body)) != `{"data":[]}` {
			t.Fatalf("list: %d %s", code, body)
		}
		if len(f.read()) != 0 || strings.Join(d.asked, ",") != "globex make=false" {
			t.Fatalf("forge asked %v, workspaces %v", f.read(), d.asked)
		}
	})
}

// ONE CODEBASE, READ FROM THE WORKSPACE with its branches.
func TestAWorkspaceRepositoryIsReadWithItsBranches(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		servesDesk(t)
		code, body := do(t, app, http.MethodGet, "/v1/git/repos/site", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("get: %d %s", code, body)
		}
		var v repoView
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		if v.Name != "site" || strings.Join(v.Branches, ",") != "main,agent/abc" {
			t.Fatalf("repo = %+v", v)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/nope", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("a missing repository answered %d", code)
		}
	})
}

// deleted is whether the forge was asked, as the machine, to delete owner/name.
func (f *deskForge) deleted(full string) bool {
	for _, a := range f.read() {
		if a == "DELETE /repos/"+full+" as " {
			return true
		}
	}
	return false
}

// A WORKSPACE REPOSITORY IS DELETED ON THE FORGE, as the machine: it is what the
// org's list shows, so it is what a delete reaches. Gone, it reads as a 404 like
// any other, and a second delete is not a second success.
func TestAWorkspaceRepositoryIsDeletedOnTheForge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, _ := servesDesk(t)
		if code, body := do(t, app, http.MethodDelete, "/v1/git/repos/site", "acme", nil); code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", code, body)
		}
		if !f.deleted("acme/site") {
			t.Fatalf("the forge was asked %v", f.read())
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/site", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("a deleted repository read %d", code)
		}
		if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/site", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("a second delete answered %d", code)
		}
	})
}

// A NAME THE WORKSPACE DOES NOT HOLD IS A 404, and the forge is asked to delete nothing.
func TestAMissingWorkspaceRepositoryIsNotDeleted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, _ := servesDesk(t)
		if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/nope", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("a missing repository answered %d", code)
		}
		if f.deleted("acme/nope") {
			t.Fatalf("the forge was asked %v", f.read())
		}
	})
}

// ONLY THE ORG'S OWN WORKSPACE IS REACHED: an org with none deletes nothing there.
func TestAnotherOrgDeletesNothingInTheWorkspace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, _ := servesDesk(t)
		if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/site", "globex", nil); code != http.StatusNotFound {
			t.Fatalf("another org answered %d", code)
		}
		if f.deleted("acme/site") {
			t.Fatalf("the forge was asked %v", f.read())
		}
	})
}

// SUPPORT MODE DELETES NOTHING OF A CUSTOMER'S, as it makes nothing there.
func TestSupportModeDeletesNothing(t *testing.T) {
	app := mountAs(t, principal.Person)
	f, _ := servesDesk(t)
	head := map[string]string{"X-User-IsAdmin": "true", "X-User-Owner": "admin"}
	if code, body := doAs(t, app, http.MethodDelete, "/v1/git/repos/site", "acme", head); code != http.StatusForbidden {
		t.Fatalf("support mode answered %d %s", code, body)
	}
	if f.deleted("acme/site") {
		t.Fatalf("the forge was asked %v", f.read())
	}
}

// NEW MAKES THE REPOSITORY IN THE ORG'S WORKSPACE, as the machine, making the
// workspace first when the org has none; nothing is held here.
func TestNewMakesTheRepositoryInTheWorkspace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		_, d := servesDesk(t)
		code, body := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "shop", "description": "the shop"})
		if code != http.StatusCreated {
			t.Fatalf("create: %d %s", code, body)
		}
		var v repoView
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		if v.Name != "shop" || v.Org != "acme" || v.Description != "the shop" || v.CloneURL != "https://git.hanzo.test/acme/shop.git" {
			t.Fatalf("created %+v", v)
		}
		if strings.Join(d.made, ",") != "acme/shop the shop" || strings.Join(d.asked, ",") != "acme make=false" {
			t.Fatalf("made %v, asked %v", d.made, d.asked)
		}
		if _, held := findRepo(mounted.Load(), context.Background(), "acme", "shop"); held {
			t.Fatal("the repository was also made here")
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "open", "public": true}); code != http.StatusBadRequest {
			t.Fatalf("a public repository in a workspace answered %d", code)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "../x"}); code != http.StatusBadRequest {
			t.Fatalf("a bad name answered %d", code)
		}
		// An org with no workspace yet has one made first.
		d.asked = nil
		if code, body := do(t, app, http.MethodPost, "/v1/git/repos", "globex", map[string]any{"name": "shop"}); code != http.StatusBadGateway {
			t.Fatalf("a workspace that was not made answered %d %s", code, body)
		}
		if strings.Join(d.asked, ",") != "globex make=false,globex make=true" {
			t.Fatalf("asked %v", d.asked)
		}
	})
}

// THE ESTATE, AND A DEPLOYMENT WITHOUT THE PROJECT APP, MAKE IT HERE.
func TestTheEstateMakesItHere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		_, d := servesDesk(t)
		if code, body := do(t, app, http.MethodPost, "/v1/git/repos", "hanzo", map[string]any{"name": "svc"}); code != http.StatusCreated {
			t.Fatalf("estate create: %d %s", code, body)
		}
		madeWorkspace = func(string, bool) (forge.Workspace, error) { return forge.Workspace{}, plane.ErrNoPeer }
		if code, body := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "svc"}); code != http.StatusCreated {
			t.Fatalf("no-peer create: %d %s", code, body)
		}
		if len(d.made) != 0 {
			t.Fatalf("made on the forge: %v", d.made)
		}
		for _, org := range []string{"hanzo", "acme"} {
			if _, held := findRepo(mounted.Load(), context.Background(), org, "svc"); !held {
				t.Fatalf("%s/svc is not held here", org)
			}
		}
	})
}

// A WORKSPACE THAT CANNOT BE READ IS SAID, not answered as an empty list.
func TestAnUnreadableWorkspaceIsSaid(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		servesDesk(t)
		madeWorkspace = func(string, bool) (forge.Workspace, error) { return forge.Workspace{}, errors.New("store down") }
		if code, _ := do(t, app, http.MethodGet, "/v1/git/repos", "acme", nil); code != http.StatusBadGateway {
			t.Fatalf("answered %d", code)
		}
	})
}
