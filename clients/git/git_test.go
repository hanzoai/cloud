package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"github.com/valyala/fasthttp"
	luxlog "github.com/luxfi/log"
)

var testCfg = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

// asOrg carries the tenant identity a go-git client sends. Tests run
// sequentially, so a single guarded value + a globally-installed
// header-injecting http transport reproduces the gateway's X-Org-Id header on
// every git request without a per-call Client hook (the v5.19.1 Clone/Push
// options have no Client field).
var asOrg = struct {
	sync.Mutex
	org string
}{}

func init() {
	rt := &headerRT{base: http.DefaultTransport}
	c := githttp.NewClient(&http.Client{Transport: rt, Timeout: 30 * time.Second})
	client.InstallProtocol("http", c)
}

type headerRT struct{ base http.RoundTripper }

func (h *headerRT) RoundTrip(req *http.Request) (*http.Response, error) {
	asOrg.Lock()
	org := asOrg.org
	asOrg.Unlock()
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	return h.base.RoundTrip(req)
}

// asTenant sets the org every subsequent go-git client request carries.
func asTenant(org string) { asOrg.Lock(); asOrg.org = org; asOrg.Unlock() }

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Domain: "api.hanzo.test"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// do runs a control-plane JSON request through the Fiber test harness (mirrors
// clients/prompts/http_test.go).
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// liveServer serves the app over a real TCP listener (fasthttp) so an in-process
// go-git client can clone/push against the smart-HTTP endpoints.
func liveServer(t *testing.T, app *zip.App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })
	return "http://" + ln.Addr().String()
}

// TestControlPlaneCRUDAndIsolation proves repo create/list/get/delete and
// per-tenant isolation through the JSON control plane.
func TestControlPlaneCRUDAndIsolation(t *testing.T) {
	app := mountApp(t)

	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	code, body := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "widgets", "description": "the widget service"})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var created repoView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create json: %v (%s)", err, body)
	}
	if created.Org != "acme" || created.Name != "widgets" || created.DefaultBranch != "main" {
		t.Fatalf("unexpected repo view: %+v", created)
	}
	if created.CloneURL != "https://api.hanzo.test/v1/git/acme/widgets.git" {
		t.Fatalf("unexpected cloneUrl: %q", created.CloneURL)
	}
	if created.SizeBytes <= 0 {
		t.Fatalf("expected non-zero sizeBytes for a fresh bare repo, got %d", created.SizeBytes)
	}

	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "widgets"}); code != http.StatusConflict {
		t.Fatalf("duplicate create want 409, got %d", code)
	}

	code, body = do(t, app, http.MethodGet, "/v1/git/repos", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d", code)
	}
	var listed struct {
		Data []repoView `json:"data"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("list json: %v", err)
	}
	if len(listed.Data) != 1 || listed.Data[0].Name != "widgets" {
		t.Fatalf("acme should see [widgets], got %+v", listed.Data)
	}

	code, body = do(t, app, http.MethodGet, "/v1/git/repos", "beta", nil)
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 0 {
		t.Fatalf("beta must see zero repos, got %d %+v", code, listed.Data)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/widgets", "beta", nil); code != http.StatusNotFound {
		t.Fatalf("beta GET acme repo want 404, got %d", code)
	}

	code, body = do(t, app, http.MethodGet, "/v1/git/usage", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("usage want 200, got %d", code)
	}
	var usage usageView
	if err := json.Unmarshal(body, &usage); err != nil {
		t.Fatalf("usage json: %v (%s)", err, body)
	}
	if usage.Org != "acme" || len(usage.Repos) != 1 || usage.TotalBytes <= 0 {
		t.Fatalf("unexpected usage: %+v", usage)
	}

	if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/widgets", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/widgets", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("re-delete want 404, got %d", code)
	}
}

// TestInfoRefsAdvertisement proves the smart-HTTP ref advertisement for a fresh
// empty repo returns the git service header + a valid advertisement body.
func TestInfoRefsAdvertisement(t *testing.T) {
	app := mountApp(t)
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "adv"}); code != http.StatusCreated {
		t.Fatal("setup create failed")
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/git/acme/adv.git/info/refs?service=git-upload-pack", nil)
	req.Header.Set("X-Org-Id", "acme")
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("info/refs: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("info/refs want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-git-upload-pack-advertisement" {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("# service=git-upload-pack")) {
		t.Fatalf("advertisement missing service header: %q", body[:min(64, len(body))])
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/git/acme/adv.git/info/refs?service=bogus", nil)
	req.Header.Set("X-Org-Id", "acme")
	resp, _ = app.Fiber().Test(req, testCfg)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus service want 400, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestClonePushRoundTrip is the end-to-end proof: create a repo, clone it
// (empty), commit + push over smart-HTTP, then clone again in a fresh client
// and SEE the pushed commit. This exercises info/refs + receive-pack (push) +
// upload-pack (clone) against the billy-backed storer, entirely in-process.
func TestClonePushRoundTrip(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)

	if code, body := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "code"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	cloneURL := base + "/v1/git/acme/code.git"

	// 1) Clone the empty repo. An empty repo has no refs — go-git returns
	// ErrEmptyRemoteRepository; that is a successful, expected clone of an empty
	// repo (proves info/refs + upload-pack negotiation reach the storer).
	asTenant("acme")
	_, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{URL: cloneURL})
	if err != nil && err != transport.ErrEmptyRemoteRepository {
		t.Fatalf("clone empty repo: %v", err)
	}

	// 2) Build a working repo locally, commit, add the cloud as a remote, push.
	fs := memfs.New()
	local, err := gogit.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatalf("init local: %v", err)
	}
	wt, err := local.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	f, err := fs.Create("README.md")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := f.Write([]byte("# hanzo native git\n")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_ = f.Close()
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	commitHash, err := wt.Commit("first commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "hanzo-dev", Email: "dev@hanzo.ai", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := local.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{cloneURL}}); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	if err := local.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/master:refs/heads/main"},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// 3) Fresh clone — the pushed commit must be there.
	cloned, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{URL: cloneURL})
	if err != nil {
		t.Fatalf("re-clone: %v", err)
	}
	head, err := cloned.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Hash() != commitHash {
		t.Fatalf("cloned HEAD %s != pushed commit %s", head.Hash(), commitHash)
	}
	c, err := cloned.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit object: %v", err)
	}
	if c.Message != "first commit" {
		t.Fatalf("unexpected commit message: %q", c.Message)
	}

	// 4) The push must have been metered.
	_, ub := do(t, app, http.MethodGet, "/v1/git/usage", "acme", nil)
	var usage usageView
	if err := json.Unmarshal(ub, &usage); err != nil {
		t.Fatalf("usage json: %v", err)
	}
	if usage.TotalBytes <= 0 {
		t.Fatalf("expected metered bytes after push, got %d", usage.TotalBytes)
	}

	// 5) Cross-tenant guard: beta cannot clone acme's repo.
	asTenant("beta")
	if _, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{URL: cloneURL}); err == nil {
		t.Fatalf("beta clone of acme repo must fail, got nil error")
	}
	asTenant("acme")

	fmt.Printf("round-trip ok: pushed %s, re-cloned HEAD %s, metered %d bytes\n",
		commitHash, head.Hash(), usage.TotalBytes)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
