package git

// push_ci_test.go holds what the file push may write and what CI does with it.
//
// The file push lands generated content: a builder's pages, an agent's edits. It
// may not write inside .git at all, it may change CI only for an org admin, and
// a workflow whose newest change came in through it is never run by the runner
// here, whoever pushed it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// member, orgAdmin and superAdmin are the three callers the file push tells
// apart, spelled as the claims the identity boundary mints.
var (
	member     = map[string]string{}
	orgAdmin   = map[string]string{"X-User-IsOrgAdmin": "true"}
	superAdmin = map[string]string{"X-User-IsAdmin": "true"}
)

// filePush posts files to the file push as a caller of org holding claims, and
// answers the status, the commit (on success) and the body.
func filePush(t *testing.T, app *zip.App, org string, claims map[string]string, repo string, files map[string]string) (int, string, string) {
	t.Helper()
	list := make([]map[string]string, 0, len(files))
	for p, c := range files {
		list = append(list, map[string]string{"path": p, "content": c})
	}
	b, _ := json.Marshal(map[string]any{"branch": "main", "message": "generated", "files": list})
	req := httptest.NewRequest(http.MethodPost, "/v1/git/repos/"+repo+"/push", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	for k, v := range claims {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var r pushResp
	_ = json.Unmarshal(body, &r)
	return resp.StatusCode, r.Commit, string(body)
}

// gitPush lands files on main through a real git push over smart HTTP, as the
// member u_<org>, and returns the commit.
func gitPush(t *testing.T, app *zip.App, org, repo string, files map[string]string) string {
	t.Helper()
	if code, out := do(t, app, http.MethodPost, "/v1/git/repos", org, map[string]any{"name": repo}); code != http.StatusCreated && code != http.StatusConflict {
		t.Fatalf("create %s: %d %s", repo, code, out)
	}
	url := liveServer(t, app) + "/v1/git/" + org + "/" + repo + ".git"
	asTenant(org)
	fs := memfs.New()
	local, err := gogit.Clone(memory.NewStorage(), fs, &gogit.CloneOptions{URL: url})
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		fs = memfs.New()
		if local, err = gogit.Init(memory.NewStorage(), fs); err == nil {
			_, err = local.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
		}
	}
	if err != nil {
		t.Fatalf("clone %s: %v", url, err)
	}
	wt, err := local.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for p, c := range files {
		if err := fs.MkdirAll(path.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := util.WriteFile(fs, p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	h, err := wt.Commit("ci", &gogit.CommitOptions{
		Author: &object.Signature{Name: "hanzo-dev", Email: "dev@hanzo.ai", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	head, err := local.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Push(&gogit.PushOptions{RemoteName: "origin",
		RefSpecs: []config.RefSpec{config.RefSpec(head.Name().String() + ":refs/heads/main")}}); err != nil {
		t.Fatalf("git push: %v", err)
	}
	return h.String()
}

// onMain lists the files on main.
func onMain(t *testing.T, org, repo string) []string {
	t.Helper()
	r, err := openRepository(context.Background(), mounted.Load(), Repo{Org: org, Name: repo})
	if err != nil {
		t.Fatal(err)
	}
	rev, _, err := r.Resolve(t.Context(), "main")
	if err != nil {
		return nil
	}
	var out []string
	if err := r.WalkText(t.Context(), rev, 0, func(p, _ string) error { out = append(out, p); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

// A BUILDER'S PUSH LANDS: pages, assets and dotfiles that are neither CI nor git.
func TestABuilderPushLands(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		code, commit, body := filePush(t, app, "acme", member, "site", map[string]string{
			"index.html":                  "<h1>hi</h1>",
			"assets/app.js":               "console.log(1)",
			".gitignore":                  "node_modules\n",
			".github/ISSUE_TEMPLATE.md":   "describe it",
			".hanzo/site.json":            "{}",
			".well-known/security.txt":    "Contact: security@hanzo.ai",
			"docs/.github/workflows/x.md": "a page about workflows, not one",
		})
		if code != http.StatusOK || commit == "" {
			t.Fatalf("a builder push was refused: %d %s", code, body)
		}
		if got := onMain(t, "acme", "site"); len(got) != 7 {
			t.Fatalf("main holds %v", got)
		}
	})
}

// A MEMBER CANNOT CHANGE CI THROUGH THE FILE PUSH: not in any directory a runner
// reads, in no spelling, and not by writing a file where the directory is, which
// deletes it. The refusal is a 403 that says why, and nothing lands.
func TestAMemberCannotChangeCI(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		if code, _, body := filePush(t, app, "acme", member, "site", map[string]string{"index.html": "v1"}); code != http.StatusOK {
			t.Fatalf("seed: %d %s", code, body)
		}
		for _, p := range []string{
			".hanzo/workflows/x.yml",
			".github/workflows/x.yml",
			".forgejo/workflows/x.yml",
			".gitea/workflows/x.yml",
			".GitHub/Workflows/x.yml",
			".hanzo/workflows",
			".github",
			".forgejo",
		} {
			code, _, body := filePush(t, app, "acme", member, "site", map[string]string{"index.html": "v2", p: "on: push"})
			if code != http.StatusForbidden || !strings.Contains(body, "only an org admin may push here") || !strings.Contains(body, p) {
				t.Errorf("%s: answered %d %s, want a 403 naming the path", p, code, body)
			}
		}
		if got := onMain(t, "acme", "site"); len(got) != 1 {
			t.Fatalf("a refused push landed: main holds %v", got)
		}
	})
}

// AN ADMIN MAY: an admin of the org, and a SuperAdmin.
func TestAnAdminMayChangeCI(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		for name, claims := range map[string]map[string]string{"org admin": orgAdmin, "SuperAdmin": superAdmin} {
			code, commit, body := filePush(t, app, "acme", claims, "site", map[string]string{".hanzo/workflows/ci.yml": "on: push # " + name})
			if code != http.StatusOK || commit == "" {
				t.Errorf("%s was refused: %d %s", name, code, body)
			}
		}
	})
}

// NOTHING IS WRITTEN INSIDE .git, by anyone, in any spelling a checkout reads as it.
func TestNothingIsWrittenInsideDotGit(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		for _, p := range []string{".git/config", "a/.git/hooks/pre-commit", ".GIT/config", ".git./config", ".git /config", "git~1/config"} {
			code, _, body := filePush(t, app, "acme", superAdmin, "site", map[string]string{p: "x"})
			if code != http.StatusBadRequest || !strings.Contains(body, "inside .git") {
				t.Errorf("%s: answered %d %s, want a 400", p, code, body)
			}
		}
	})
}

// A WORKFLOW THE FILE PUSH WROTE IS NEVER RUN BY ITSELF: not on the commit that
// wrote it, even an admin's, and not on the next builder push either. A change
// to it through git runs again.
func TestAFilePushedWorkflowDoesNotRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64"})
		s := mounted.Load()
		store, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		runs := func() int {
			t.Helper()
			drain(s, t.Context(), "acme")
			rs, err := store.Runs(t.Context(), "acme", "", "widgets", 10)
			if err != nil {
				t.Fatal(err)
			}
			owed, _ := store.Owed(t.Context(), 10)
			if len(owed) != 0 {
				t.Fatalf("a push is still owed: %+v", owed)
			}
			return len(rs)
		}

		if code, _, body := filePush(t, app, "acme", orgAdmin, "widgets", map[string]string{".hanzo/workflows/ci.yml": workflowFor("linux/amd64")}); code != http.StatusOK {
			t.Fatalf("admin push: %d %s", code, body)
		}
		if n := runs(); n != 0 {
			t.Fatalf("the commit that wrote a workflow through the file push ran it: %d runs", n)
		}
		if code, _, body := filePush(t, app, "acme", member, "widgets", map[string]string{"index.html": "<h1>hi</h1>"}); code != http.StatusOK {
			t.Fatalf("builder push: %d %s", code, body)
		}
		if n := runs(); n != 0 {
			t.Fatalf("the next builder push ran a workflow the file push wrote: %d runs", n)
		}
		gitPush(t, app, "acme", "widgets", map[string]string{"README.md": "# widgets"})
		if n := runs(); n != 0 {
			t.Fatalf("a git push that left it alone ran a workflow the file push wrote: %d runs", n)
		}

		commit := gitPush(t, app, "acme", "widgets", map[string]string{".hanzo/workflows/ci.yml": workflowFor("linux/amd64") + "# reviewed\n"})
		if n := runs(); n != 1 {
			t.Fatalf("a workflow changed through git did not run: %d runs", n)
		}
		if rs, _ := store.Runs(t.Context(), "acme", "", "widgets", 10); rs[0].Commit != commit {
			t.Fatalf("ran %s, want the git push %s", rs[0].Commit, commit)
		}
	})
}

// A BUILDER PUSH STILL RUNS CI WRITTEN THROUGH GIT. What decides is where each
// workflow's newest change came from, not who made the commit being run.
func TestABuilderPushRunsCIWrittenThroughGit(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64"})
		pushWorkflow(t, app, "acme", "widgets", workflowFor("linux/amd64"))
		code, commit, body := filePush(t, app, "acme", member, "widgets", map[string]string{"index.html": "<h1>hi</h1>"})
		if code != http.StatusOK {
			t.Fatalf("builder push: %d %s", code, body)
		}
		s := mounted.Load()
		drain(s, t.Context(), "acme")
		store, _ := storeFor(s, "acme")
		rs, err := store.Runs(t.Context(), "acme", "", "widgets", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			if r.Commit == commit {
				return
			}
		}
		t.Fatalf("the builder push did not run the repository's own CI: %+v", rs)
	})
}

// A CALLER WITH NO REQUEST BEHIND IT CANNOT CHANGE CI, whatever it states. The op
// is reachable by name with no route (MCP, the plane), and there a claimed admin
// bit is the caller's own word, so authority is read only off a request.
func TestAStatedAdminCannotChangeCI(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		ctx := zip.WithCaller(t.Context(), zip.Caller{Org: "acme", User: "u_acme", Admin: true, OrgAdmin: true})
		_, err := ops{s: mounted.Load()}.pushFiles(ctx, &pushReq{Name: "site", Branch: "main",
			Files: []pushFile{{Path: ".hanzo/workflows/ci.yml", Content: "on: push"}}})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Fatalf("a stated admin with no request answered %v, want a 403", err)
		}
		if _, err := (ops{s: mounted.Load()}).pushFiles(ctx, &pushReq{Name: "site", Branch: "main",
			Files: []pushFile{{Path: "index.html", Content: "<h1>hi</h1>"}}}); err != nil {
			t.Fatalf("the same caller could not push a page: %v", err)
		}
	})
}
