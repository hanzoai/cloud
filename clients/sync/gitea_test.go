package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// gitea_test.go proves the git provider drives GITEA (the one git store) — the Gitea
// REST primitives behave, and a /v1/sync reconcile issues Gitea calls, never touching
// an embedded store. A stateful in-memory Gitea mock stands in for hanzo-git; the
// fast-forward transport core is proved separately against local repos.

// giteaMock is a minimal stateful Gitea REST server: it records the repos/orgs/mirrors
// a client creates so a test can ASSERT the provider acted on Gitea.
type giteaMock struct {
	mu       sync.Mutex
	token    string
	login    string
	orgs     map[string]bool
	repos    map[string]bool
	branches map[string]string       // "owner/repo/branch" -> sha
	mirrors  map[string][]pushMirror // "owner/repo" -> mirrors
	reqs     []string                // "METHOD /path" log
}

func newGiteaMock(t *testing.T) (*giteaMock, *gitea) {
	t.Helper()
	m := &giteaMock{
		token: "test-admin-token", login: "hanzo-admin",
		orgs: map[string]bool{}, repos: map[string]bool{},
		branches: map[string]string{}, mirrors: map[string][]pushMirror{},
	}
	srv := httptest.NewServer(m.mux(t))
	t.Cleanup(srv.Close)
	t.Setenv(giteaURLEnv, srv.URL)
	t.Setenv(giteaTokenEnv, m.token)
	gt, err := giteaFromEnv()
	if err != nil {
		t.Fatalf("giteaFromEnv: %v", err)
	}
	return m, gt
}

func (m *giteaMock) mux(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			m.mu.Lock()
			m.reqs = append(m.reqs, r.Method+" "+r.URL.Path)
			m.mu.Unlock()
			if r.Header.Get("Authorization") != "token "+m.token {
				t.Errorf("missing/short auth header: %q", r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/user", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"login": m.login})
	}))
	mux.HandleFunc("GET /api/v1/orgs/{org}", authed(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.orgs[r.PathValue("org")] {
			writeJSON(w, 200, map[string]string{"username": r.PathValue("org")})
			return
		}
		w.WriteHeader(404)
	}))
	mux.HandleFunc("POST /api/v1/orgs", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.orgs[body.Username] = true
		m.mu.Unlock()
		writeJSON(w, 201, map[string]string{"username": body.Username})
	}))
	mux.HandleFunc("POST /api/v1/orgs/{owner}/repos", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.repos[r.PathValue("owner")+"/"+body.Name] = true
		m.mu.Unlock()
		writeJSON(w, 201, map[string]string{"name": body.Name})
	}))
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/branches/{branch}", authed(func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("owner") + "/" + r.PathValue("repo") + "/" + r.PathValue("branch")
		m.mu.Lock()
		sha, ok := m.branches[key]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, map[string]any{"commit": map[string]string{"id": sha}})
	}))
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/push_mirrors", authed(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		list := m.mirrors[r.PathValue("owner")+"/"+r.PathValue("repo")]
		m.mu.Unlock()
		if list == nil {
			list = []pushMirror{}
		}
		writeJSON(w, 200, list)
	}))
	mux.HandleFunc("POST /api/v1/repos/{owner}/{repo}/push_mirrors", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RemoteAddress string `json:"remote_address"`
			SyncOnCommit  bool   `json:"sync_on_commit"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.SyncOnCommit {
			t.Errorf("push_mirror POST missing sync_on_commit")
		}
		key := r.PathValue("owner") + "/" + r.PathValue("repo")
		m.mu.Lock()
		m.mirrors[key] = append(m.mirrors[key], pushMirror{RemoteName: "m1", RemoteAddress: body.RemoteAddress})
		m.mu.Unlock()
		writeJSON(w, 201, map[string]string{"remote_name": "m1"})
	}))
	mux.HandleFunc("DELETE /api/v1/repos/{owner}/{repo}/push_mirrors/{name}", authed(func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("owner") + "/" + r.PathValue("repo")
		m.mu.Lock()
		kept := m.mirrors[key][:0]
		for _, pm := range m.mirrors[key] {
			if pm.RemoteName != r.PathValue("name") {
				kept = append(kept, pm)
			}
		}
		m.mirrors[key] = kept
		m.mu.Unlock()
		w.WriteHeader(204)
	}))
	// Least-specific repo GET registered last (ServeMux prefers the specific ones above).
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}", authed(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		ok := m.repos[r.PathValue("owner")+"/"+r.PathValue("repo")]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, map[string]string{"name": r.PathValue("repo")})
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *giteaMock) mirrorCount(owner, repo string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.mirrors[owner+"/"+repo])
}

func TestGitea_EnsureRepoCreatesOrgAndRepo(t *testing.T) {
	m, gt := newGiteaMock(t)
	ctx := context.Background()
	if err := gt.ensureRepo(ctx, "acme", "widgets"); err != nil {
		t.Fatalf("ensureRepo: %v", err)
	}
	m.mu.Lock()
	gotOrg, gotRepo := m.orgs["acme"], m.repos["acme/widgets"]
	m.mu.Unlock()
	if !gotOrg || !gotRepo {
		t.Fatalf("ensureRepo did not create org+repo on gitea (org=%v repo=%v)", gotOrg, gotRepo)
	}
	// Idempotent: a second call is a no-op (repo already present → single GET, no POST).
	before := len(m.reqs)
	if err := gt.ensureRepo(ctx, "acme", "widgets"); err != nil {
		t.Fatalf("ensureRepo (2nd): %v", err)
	}
	m.mu.Lock()
	added := m.reqs[before:]
	m.mu.Unlock()
	for _, r := range added {
		if r[:4] == "POST" {
			t.Fatalf("idempotent ensureRepo issued a POST: %v", added)
		}
	}
}

func TestGitea_PushMirrorLifecycle(t *testing.T) {
	m, gt := newGiteaMock(t)
	ctx := context.Background()
	target := "https://github.com/acme/widgets.git"

	if err := gt.ensurePushMirror(ctx, "acme", "widgets", target, "ghs_token"); err != nil {
		t.Fatalf("ensurePushMirror: %v", err)
	}
	if n := m.mirrorCount("acme", "widgets"); n != 1 {
		t.Fatalf("after ensure: mirror count = %d, want 1", n)
	}
	// Idempotent: ensuring the SAME target again does not add a second mirror.
	if err := gt.ensurePushMirror(ctx, "acme", "widgets", target, "ghs_token"); err != nil {
		t.Fatalf("ensurePushMirror (2nd): %v", err)
	}
	if n := m.mirrorCount("acme", "widgets"); n != 1 {
		t.Fatalf("idempotent ensure added a mirror: count = %d, want 1", n)
	}
	// Teardown removes it (matched by host+path, credential-agnostic).
	if err := gt.removePushMirror(ctx, "acme", "widgets", target); err != nil {
		t.Fatalf("removePushMirror: %v", err)
	}
	if n := m.mirrorCount("acme", "widgets"); n != 0 {
		t.Fatalf("after remove: mirror count = %d, want 0", n)
	}
}

func TestGitea_BranchSHAAndWhoami(t *testing.T) {
	m, gt := newGiteaMock(t)
	ctx := context.Background()
	m.mu.Lock()
	m.branches["acme/widgets/main"] = "deadbeef"
	m.mu.Unlock()

	sha, ok, err := gt.branchSHA(ctx, "acme", "widgets", "main")
	if err != nil || !ok || sha != "deadbeef" {
		t.Fatalf("branchSHA = (%q,%v,%v), want (deadbeef,true,nil)", sha, ok, err)
	}
	if _, ok, _ := gt.branchSHA(ctx, "acme", "widgets", "absent"); ok {
		t.Fatalf("branchSHA for a missing branch reported ok")
	}
	login, err := gt.whoami(ctx)
	if err != nil || login != "hanzo-admin" {
		t.Fatalf("whoami = (%q,%v), want (hanzo-admin,nil)", login, err)
	}
	if gt.login != "hanzo-admin" {
		t.Fatalf("whoami did not cache the login")
	}
}

// TestReconcile_PushOnly_ActsOnGitea proves a /v1/sync reconcile drives Gitea (creates
// the repo + registers a push-mirror), not any embedded store. Push-only + a GitLab
// source keeps it credential-free (gitToken mints nothing), so the whole reconcile is
// pure Gitea REST — the load-bearing "acts on Gitea" verification.
func TestReconcile_PushOnly_ActsOnGitea(t *testing.T) {
	m, _ := newGiteaMock(t)
	sy := Sync{
		Org: "acme", Kind: "git", Direction: dirPush, Actor: "hanzo-sync",
		Source: Endpoint{Provider: provGitLab, Locator: "https://gitlab.com/acme/widgets.git"},
		Target: Endpoint{Provider: provNative, Locator: "widgets"},
	}
	ev := Event{Manual: true, Provider: provGitLab, Org: "acme"}

	changed, err := gitProvider{}.Reconcile(context.Background(), sy, ev)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !changed {
		t.Fatalf("push-only reconcile reported no change")
	}
	m.mu.Lock()
	repoOK := m.repos["acme/widgets"]
	mirrors := m.mirrors["acme/widgets"]
	m.mu.Unlock()
	if !repoOK {
		t.Fatalf("reconcile did not create the repo on gitea")
	}
	if len(mirrors) != 1 || !sameRemote(mirrors[0].RemoteAddress, sy.Source.Locator) {
		t.Fatalf("reconcile did not register the gitea push-mirror to the source: %+v", mirrors)
	}
}

// TestReconcile_FailsClosedWithoutGitea proves the provider refuses (never silently
// no-ops) when the Gitea store is unconfigured.
func TestReconcile_FailsClosedWithoutGitea(t *testing.T) {
	t.Setenv(giteaTokenEnv, "")
	sy := Sync{
		Org: "acme", Kind: "git", Direction: dirPush,
		Source: Endpoint{Provider: provGitLab, Locator: "https://gitlab.com/acme/widgets.git"},
		Target: Endpoint{Provider: provNative, Locator: "widgets"},
	}
	if _, err := (gitProvider{}).Reconcile(context.Background(), sy, Event{Manual: true}); err == nil {
		t.Fatalf("reconcile without a gitea store must fail closed, got nil error")
	}
}

func TestSameRemote(t *testing.T) {
	cases := [][2]string{
		{"https://github.com/acme/widgets.git", "https://x-access-token:tok@github.com/acme/widgets"},
		{"https://GitHub.com/Acme/Widgets", "https://github.com/acme/widgets.git"},
	}
	for _, c := range cases {
		if !sameRemote(c[0], c[1]) {
			t.Errorf("sameRemote(%q,%q) = false, want true", c[0], c[1])
		}
	}
	if sameRemote("https://github.com/acme/widgets", "https://github.com/acme/other") {
		t.Errorf("sameRemote matched different repos")
	}
}

// TestFetchThenPush_FastForwardGuard proves the inbound transport core: a fast-forward
// push advances Gitea, an up-to-date push is a no-op, and an unrelated (non-fast-forward)
// history is a CONFLICT that leaves the destination untouched (the split-brain guard).
// Uses local repos over the git file transport, so it needs the git binary.
func TestFetchThenPush_FastForwardGuard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary required for the file:// transport")
	}
	ctx := context.Background()
	dir := t.TempDir()
	src := initRepoWithCommit(t, filepath.Join(dir, "src"), "a")   // source @ A
	dst := filepath.Join(dir, "dst.git")                           // empty bare destination
	if _, err := gogit.PlainInit(dst, true); err != nil {
		t.Fatalf("init dst: %v", err)
	}
	fetch := gitconfig.RefSpec("+refs/heads/master:refs/heads/master")
	push := gitconfig.RefSpec("refs/heads/master:refs/heads/master")

	// 1) fresh branch → destination advances.
	changed, err := fetchThenPush(ctx, src, nil, dst, nil, fetch, push)
	if err != nil || !changed {
		t.Fatalf("first push = (%v,%v), want (true,nil)", changed, err)
	}
	srcHead := headOf(t, src, "master")
	if got := headOf(t, dst, "master"); got != srcHead {
		t.Fatalf("dst head = %q, want %q", got, srcHead)
	}
	// 2) same push again → up to date, no change.
	if changed, err := fetchThenPush(ctx, src, nil, dst, nil, fetch, push); err != nil || changed {
		t.Fatalf("idempotent push = (%v,%v), want (false,nil)", changed, err)
	}
	// 3) an UNRELATED source history → non-fast-forward → conflict, dst preserved.
	other := initRepoWithCommit(t, filepath.Join(dir, "other"), "x")
	changed, err = fetchThenPush(ctx, other, nil, dst, nil, fetch, push)
	if err != nil {
		t.Fatalf("conflicting push errored (want swallowed): %v", err)
	}
	if changed {
		t.Fatalf("conflicting push reported a change")
	}
	if got := headOf(t, dst, "master"); got != srcHead {
		t.Fatalf("dst head after conflict = %q, want preserved %q", got, srcHead)
	}
}

// initRepoWithCommit creates a non-bare repo at path with a single commit on master and
// returns the path (a file-transport source).
func initRepoWithCommit(t *testing.T, path, msg string) string {
	t.Helper()
	r, err := gogit.PlainInit(path, false)
	if err != nil {
		t.Fatalf("init %s: %v", path, err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "f.txt"), []byte(msg), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("f.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return path
}

func headOf(t *testing.T, path, branch string) string {
	t.Helper()
	r, err := gogit.PlainOpen(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	ref, err := r.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		t.Fatalf("ref %s: %v", branch, err)
	}
	return ref.Hash().String()
}
