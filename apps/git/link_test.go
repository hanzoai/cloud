package git

// A linked repository is read from its host through the cache and written to its
// host, and its host decides who may do either. Each path is proved with the real
// git CLI against the real server, and the host is a real git server over TLS,
// standing in for GitHub; what GitHub would answer about a person is a fake hub.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// hostToken is the installation token the fake hub mints and the host accepts.
const hostToken = "ghs_installation"

// host is a GitHub stand-in: bare repositories at <root>/<owner>/<name>.git
// over https, read with no credential unless private, and otherwise only with
// hostToken; a push always needs it.
type hostServer struct {
	url, root string
	mu        sync.Mutex
	private   map[string]bool // "owner/name"
	tokens    map[string]bool // passwords a Basic credential may carry
	sent      []string        // "<method> <path> <authorization>"
}

func serveHost(t *testing.T) *hostServer {
	t.Helper()
	h := &hostServer{root: t.TempDir(), private: map[string]bool{}, tokens: map[string]bool{hostToken: true}}
	backend := &cgi.Handler{
		Path: filepath.Join(gitOut(t, "", "--exec-path"), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + h.root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		h.mu.Lock()
		h.sent = append(h.sent, r.Method+" "+r.URL.Path+" "+auth)
		parts := strings.SplitN(strings.Trim(r.URL.Path, "/"), "/", 3)
		private := len(parts) >= 2 && h.private[parts[0]+"/"+strings.TrimSuffix(parts[1], ".git")]
		h.mu.Unlock()
		push := strings.HasSuffix(r.URL.Path, "/"+svcReceivePack) || r.URL.Query().Get("service") == svcReceivePack
		_, pass, _ := r.BasicAuth()
		h.mu.Lock()
		ok := h.tokens[pass]
		h.mu.Unlock()
		if (push || private) && !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h.url = srv.URL

	cert := filepath.Join(t.TempDir(), "host.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	// The host is this test's own loopback server, which outbound's client would
	// refuse to dial: the client is the server's, which trusts its certificate, and
	// the git CLI trusts it through trust.
	wasTrust, wasUpstream, wasBase := trust, upstream, githubBase
	trust = cert
	upstream = func(timeout time.Duration) *http.Client {
		c := srv.Client()
		c.Timeout = timeout
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}
	githubBase = srv.URL
	providers["127.0.0.1"] = "github"
	t.Cleanup(func() {
		trust, upstream, githubBase = wasTrust, wasUpstream, wasBase
		delete(providers, "127.0.0.1")
	})
	return h
}

// repo makes owner/name on the host with one commit, and answers its address
// and the commit.
func (h *hostServer) repo(t *testing.T, owner, name, content string, private bool) (string, string) {
	t.Helper()
	dir := filepath.Join(h.root, owner)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := gitSource(t, dir, name, content, "v1")
	gitRun(t, "", "--git-dir="+filepath.Join(dir, name+".git"), "config", "http.receivepack", "true")
	h.mu.Lock()
	h.private[owner+"/"+name] = private
	h.mu.Unlock()
	return h.url + "/" + owner + "/" + name + ".git", commit
}

// commit lands files on owner/name's branch on the host itself, as a push to
// GitHub that never passed through here.
func (h *hostServer) commit(t *testing.T, owner, name, branch string, files map[string]string) string {
	t.Helper()
	work := t.TempDir()
	gitRun(t, "", "clone", "-q", filepath.Join(h.root, owner, name+".git"), work)
	if branch != "main" {
		gitRun(t, work, "checkout", "-q", "-b", branch)
	}
	for p, c := range files {
		if err := os.MkdirAll(filepath.Join(work, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, work, p, c)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "-c", "user.email=t@hanzo.test", "-c", "user.name=t", "commit", "-q", "-m", "next")
	gitRun(t, work, "push", "-q", "origin", branch)
	return gitOut(t, work, "rev-parse", "HEAD")
}

// tip is owner/name's main on the host.
func (h *hostServer) tip(t *testing.T, owner, name string) string {
	t.Helper()
	return gitOut(t, "", "--git-dir="+filepath.Join(h.root, owner, name+".git"), "rev-parse", "main")
}

// fakeHub answers as GitHub would about the people in readers and writers, and
// records each question.
type fakeHub struct {
	mu      sync.Mutex
	known   map[string]bool // "owner/name" GitHub has; every name when nil
	readers map[string]bool
	writers map[string]bool
	refuse  int // the status a person outside readers gets; 404 when zero
	asked   []string
	revoked int
}

func (f *fakeHub) note(ctx context.Context, what string) string {
	user := zip.CallerOf(ctx).User
	f.mu.Lock()
	f.asked = append(f.asked, what+" "+user)
	f.mu.Unlock()
	return user
}

func (f *fakeHub) repo(ctx context.Context, owner, name string) (string, string, error) {
	if f.readers[f.note(ctx, "repo")] && (f.known == nil || f.known[owner+"/"+name]) {
		return owner, name, nil
	}
	status := f.refuse
	if status == 0 {
		status = http.StatusNotFound
	}
	return "", "", &zip.HTTPError{Status: status, Msg: "github repo: not found"}
}

func (f *fakeHub) read(ctx context.Context, _, _ string) (string, error) {
	f.note(ctx, "read")
	return hostToken, nil
}

func (f *fakeHub) write(ctx context.Context, _, _ string) (string, func(), error) {
	if !f.writers[f.note(ctx, "write")] {
		return "", nil, &zip.HTTPError{Status: http.StatusForbidden, Msg: "github run: connect your GitHub account"}
	}
	return hostToken, func() {
		f.mu.Lock()
		f.revoked++
		f.mu.Unlock()
	}, nil
}

func (f *fakeHub) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func hubs(t *testing.T, f *fakeHub) {
	t.Helper()
	was := asker
	asker = f
	t.Cleanup(func() { asker = was })
}

// as runs a JSON request as user of org, with claims.
func as(t *testing.T, app *zip.App, method, path, org, user string, claims map[string]string, body any) (int, []byte) {
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
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", user)
	for k, v := range claims {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// linkAs links org's repo name to source as user.
func linkAs(t *testing.T, app *zip.App, org, user, name, source string) repoView {
	t.Helper()
	code, body := as(t, app, http.MethodPost, "/v1/git/repos/"+name+"/link", org, user, nil, map[string]any{"source": source})
	if code != http.StatusOK {
		t.Fatalf("link %s/%s: %d %s", org, name, code, body)
	}
	settle(t)
	var v repoView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("link json: %v (%s)", err, body)
	}
	return v
}

// gitAs is the git CLI as user of org against this server.
func gitAs(org, user string, args ...string) []string {
	return append([]string{"-c", "http.extraHeader=X-Org-Id: " + org, "-c", "http.extraHeader=X-User-Id: " + user}, args...)
}

// cloneRepo clones <org>/<name> from base as org and returns the repo + its
// resolved HEAD commit hash.
func cloneRepo(t *testing.T, base, org, name string) (*gogit.Repository, plumbing.Hash) {
	t.Helper()
	asTenant(org)
	repo, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{
		URL: base + "/v1/git/" + org + "/" + name + ".git",
	})
	if err != nil {
		t.Fatalf("clone %s/%s: %v", org, name, err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head %s/%s: %v", org, name, err)
	}
	return repo, head.Hash()
}

// A LINKED REPOSITORY IS READ FROM ITS HOST: its arrival fills the cache once,
// then a clone, a tree and a file within the window answer the host's commit
// from it, the link names where the history is, and another org reads nothing.
func TestALinkedRepositoryIsReadFromItsHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		h := serveHost(t)
		src, commit := h.repo(t, "acme", "widgets", "# widgets\n", false)

		v := linkAs(t, app, "acme", "u_acme", "widgets", src)
		if v.Link == nil || v.Link.Provider != "github" || v.Link.Owner != "acme" || v.Link.Name != "widgets" || v.Link.CloneURL != src {
			t.Fatalf("link = %+v", v.Link)
		}
		if v.SSHURL != "git@127.0.0.1:acme/widgets.git" {
			t.Fatalf("a linked repository's SSH remote is its host's, got %q", v.SSHURL)
		}
		s := service(t)
		if n := s.State.cache.fetches.Load(); n != 1 {
			t.Fatalf("linking fetched %d times; its arrival reads the host once", n)
		}

		dst := filepath.Join(t.TempDir(), "clone")
		gitRun(t, "", gitAs("acme", "u_acme", "clone", "-q", base+"/v1/git/acme/widgets.git", dst)...)
		if got := gitOut(t, dst, "rev-parse", "HEAD"); got != commit {
			t.Fatalf("clone HEAD %s, want the host's %s", got, commit)
		}
		if got := gitOut(t, dst, "tag"); got != "v1" {
			t.Fatalf("tags %q, want the host's v1", got)
		}
		code, body := do(t, app, http.MethodGet, "/v1/git/repos/widgets/blob?path=README.md", "acme", nil)
		if code != http.StatusOK || !strings.Contains(string(body), "# widgets") {
			t.Fatalf("blob: %d %s", code, body)
		}
		code, body = do(t, app, http.MethodGet, "/v1/git/repos/widgets/tree", "acme", nil)
		if code != http.StatusOK || !strings.Contains(string(body), "README.md") {
			t.Fatalf("tree: %d %s", code, body)
		}
		if n := s.State.cache.fetches.Load(); n != 1 {
			t.Fatalf("a clone, a file and a tree within the window cost %d more fetches, want none", n-1)
		}

		asTenant("intruder")
		if _, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{URL: base + "/v1/git/acme/widgets.git"}); err == nil {
			t.Fatal("another org read acme's linked repository")
		}
		asTenant("acme")
	})
}

// THE CACHE FOLLOWS ITS HOST: within the window it answers what it fetched, and
// past it the host's new commit, with one more fetch.
func TestALinkedRepositoryFollowsItsHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h := serveHost(t)
		src, first := h.repo(t, "acme", "widgets", "# widgets\n", false)
		linkAs(t, app, "acme", "u_acme", "widgets", src)
		s := service(t)

		head := func() string {
			t.Helper()
			code, body := do(t, app, http.MethodGet, "/v1/git/repos/widgets/refs", "acme", nil)
			var refs refsJSON
			if code != http.StatusOK || json.Unmarshal(body, &refs) != nil || len(refs.Branches) != 1 {
				t.Fatalf("refs: %d %s", code, body)
			}
			return refs.Branches[0].SHA
		}
		if got := head(); got != first {
			t.Fatalf("main %s, want %s", got, first)
		}
		next := h.commit(t, "acme", "widgets", "main", map[string]string{"NEXT.md": "more\n"})
		if got := head(); got != first {
			t.Fatalf("within the window the cache answered %s, want what it fetched (%s)", got, first)
		}
		s.State.cache.mu.Lock()
		s.State.cache.ttl = 0
		s.State.cache.mu.Unlock()
		if got := head(); got != next {
			t.Fatalf("past the window main is %s, want the host's %s", got, next)
		}
		if n := s.State.cache.fetches.Load(); n != 2 {
			t.Fatalf("fetches = %d, want 2", n)
		}
	})
}

// A PRIVATE REPOSITORY IS READ AS GITHUB LETS THE CALLER: a person GitHub's view
// includes reads it, through a cache filled with the org's installation token;
// a person it does not is 404, a refusal is 403, and a caller with no org reads
// nothing. The token never leaves for the host's address over anything but TLS.
func TestAPrivateLinkIsReadAsGitHubLetsTheCaller(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		h := serveHost(t)
		f := &fakeHub{readers: map[string]bool{"dave": true}}
		hubs(t, f)
		src, commit := h.repo(t, "acme", "secret", "# secret\n", true)
		linkAs(t, app, "acme", "dave", "secret", src)

		code, body := as(t, app, http.MethodGet, "/v1/git/repos/secret/blob?path=README.md", "acme", "dave", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), "# secret") {
			t.Fatalf("dave: %d %s", code, body)
		}
		dst := filepath.Join(t.TempDir(), "clone")
		gitRun(t, "", gitAs("acme", "dave", "clone", "-q", base+"/v1/git/acme/secret.git", dst)...)
		if got := gitOut(t, dst, "rev-parse", "HEAD"); got != commit {
			t.Fatalf("dave's clone %s, want %s", got, commit)
		}

		code, body = as(t, app, http.MethodGet, "/v1/git/repos/secret/blob?path=README.md", "acme", "eve", nil, nil)
		if code != http.StatusNotFound || strings.Contains(string(body), "# secret") {
			t.Fatalf("eve, outside GitHub's view, answered %d %s", code, body)
		}
		f.refuse = http.StatusForbidden
		if code, body := as(t, app, http.MethodGet, "/v1/git/repos/secret/tree", "acme", "mallory", nil, nil); code != http.StatusForbidden {
			t.Fatalf("a refusal answered %d %s", code, body)
		}
		if code, _ := as(t, app, http.MethodGet, "/v1/git/acme/secret.git/info/refs?service=git-upload-pack", "", "", nil, nil); code != http.StatusUnauthorized && code != http.StatusNotFound {
			t.Fatalf("an anonymous fetch answered %d", code)
		}
		reads := 0
		for _, q := range f.questions() {
			if strings.HasPrefix(q, "read ") {
				reads++
			}
		}
		if reads != 1 {
			t.Fatalf("the org's read token was minted %d times, want once: %v", reads, f.questions())
		}
		if env := strings.Join(linkEnv("http://127.0.0.1/acme/secret.git", gitCred{User: "x-access-token", Token: hostToken}), " "); strings.Contains(env, "Authorization") {
			t.Fatalf("a token rode an http address: %s", env)
		}
		t.Setenv("GIT_MIRROR_TOKEN_GITHUB_COM", "machine")
		if env := strings.Join(linkEnv("https://github.com/acme/secret.git", gitCred{}), " "); strings.Contains(env, "Authorization") {
			t.Fatalf("a link was fetched with the deployment's own token: %s", env)
		}
	})
}

// A PUSH TO A LINKED REPOSITORY GOES TO ITS HOST, as the person GitHub lets
// push, and nothing lands here: the host holds the commit, the cache reads it
// on the next read, no build fires here, and the token is revoked. A person
// GitHub does not let push is refused, and nothing reaches the host.
func TestAPushToALinkedRepositoryGoesToItsHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var mu sync.Mutex
		var events []cloud.GitPushEvent
		cloud.RegisterPushBuilder(func(_ context.Context, ev cloud.GitPushEvent) (int, error) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			return 0, nil
		})
		t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

		app := mountApp(t)
		base := liveServer(t, app)
		h := serveHost(t)
		f := &fakeHub{readers: map[string]bool{"dave": true, "eve": true}, writers: map[string]bool{"dave": true}}
		hubs(t, f)
		src, _ := h.repo(t, "acme", "widgets", "# widgets\n", false)
		linkAs(t, app, "acme", "dave", "widgets", src)
		url := base + "/v1/git/acme/widgets.git"

		work := filepath.Join(t.TempDir(), "work")
		gitRun(t, "", gitAs("acme", "dave", "clone", "-q", url, work)...)
		writeFile(t, work, "NEW.md", "pushed through\n")
		gitRun(t, work, "add", "-A")
		gitRun(t, work, "-c", "user.email=t@hanzo.test", "-c", "user.name=t", "commit", "-q", "-m", "through")
		pushed := gitOut(t, work, "rev-parse", "HEAD")

		if err := gitTry(t, work, gitAs("acme", "eve", "push", "-q", "origin", "main")...); err == nil {
			t.Fatal("eve, whom GitHub does not let push, pushed")
		}
		if got := h.tip(t, "acme", "widgets"); got == pushed {
			t.Fatal("a refused push reached the host")
		}

		gitRun(t, work, gitAs("acme", "dave", "push", "-q", "origin", "main")...)
		if got := h.tip(t, "acme", "widgets"); got != pushed {
			t.Fatalf("the host's main is %s, want the pushed %s", got, pushed)
		}
		s := service(t)
		if got := storeTip(t, s, "acme", "widgets", "main"); got == pushed {
			t.Fatal("the push landed here")
		}
		dst := filepath.Join(t.TempDir(), "again")
		gitRun(t, "", gitAs("acme", "dave", "clone", "-q", url, dst)...)
		if got := gitOut(t, dst, "rev-parse", "HEAD"); got != pushed {
			t.Fatalf("the next read answered %s, want the pushed %s", got, pushed)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(events) != 0 {
			t.Fatalf("a push to a linked repository fired builds here: %+v", events)
		}
		if f.revoked == 0 {
			t.Fatal("no write token was revoked")
		}
	})
}

// A CLIENT-LESS PUSH TO A LINKED REPOSITORY GOES TO ITS HOST too, built on the
// host's tip.
func TestAClientlessPushToALinkedRepositoryGoesToItsHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h := serveHost(t)
		f := &fakeHub{readers: map[string]bool{"u_acme": true}, writers: map[string]bool{"u_acme": true}}
		hubs(t, f)
		src, first := h.repo(t, "acme", "site", "# site\n", false)
		linkAs(t, app, "acme", "u_acme", "site", src)

		code, out, body := filePush(t, app, "acme", member, "site", map[string]string{"index.html": "<h1>hi</h1>"})
		if code != http.StatusOK {
			t.Fatalf("push: %d %s", code, body)
		}
		if got := h.tip(t, "acme", "site"); got != out {
			t.Fatalf("the host's main is %s, want the pushed %s", got, out)
		}
		if parent := gitOut(t, "", "--git-dir="+filepath.Join(h.root, "acme", "site.git"), "rev-parse", out+"^"); parent != first {
			t.Fatalf("the commit's parent is %s, want the host's tip %s", parent, first)
		}
		code, raw := do(t, app, http.MethodGet, "/v1/git/repos/site/blob?path=index.html", "acme", nil)
		var blob blobJSON
		if code != http.StatusOK || json.Unmarshal(raw, &blob) != nil || blob.Content != "<h1>hi</h1>" {
			t.Fatalf("the next read: %d %s", code, raw)
		}
	})
}

// A LINK NEVER HIDES A HISTORY HELD HERE: a repository with refs of its own is
// refused, and its refs stay as they are.
func TestALinkNeverHidesAHistoryHeldHere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h := serveHost(t)
		src, _ := h.repo(t, "acme", "native", "# theirs\n", false)
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/native/push", "acme", map[string]any{
			"files": []map[string]any{{"path": "a.txt", "content": "ours\n"}},
		}); code != http.StatusOK {
			t.Fatalf("push: %d %s", code, b)
		}
		before := refTip(t, app, "acme", "native", "main")
		code, body := do(t, app, http.MethodPost, "/v1/git/repos/native/link", "acme", map[string]any{"source": src})
		if code != http.StatusConflict {
			t.Fatalf("linking a repository with its own history answered %d %s", code, body)
		}
		if after := refTip(t, app, "acme", "native", "main"); after != before {
			t.Fatalf("main moved: %s -> %s", before, after)
		}
	})
}

// THE SWEEP LINKS EACH REPOSITORY TO ITS NAMESAKE where GitHub lets the caller
// read one, lists the rest with GitHub's answer, never touches a history held
// here, and is an org admin's alone.
func TestTheSweepLinksNamesakesAndGuessesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h := serveHost(t)
		f := &fakeHub{readers: map[string]bool{"u_acme": true}, known: map[string]bool{"acme/open": true, "acme/closed": true}}
		hubs(t, f)
		h.repo(t, "acme", "open", "# open\n", false)
		h.repo(t, "acme", "closed", "# closed\n", true)
		s := service(t)
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"open", "closed", "gone"} {
			if err := st.Create(context.Background(), Repo{ID: "repo_" + name, Org: "acme", Name: name, DefaultBranch: "main", CreatedAt: 1, UpdatedAt: 1}); err != nil {
				t.Fatal(err)
			}
		}
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/native/push", "acme", map[string]any{
			"files": []map[string]any{{"path": "a.txt", "content": "ours\n"}},
		}); code != http.StatusOK {
			t.Fatalf("push: %d %s", code, b)
		}

		if code, _ := as(t, app, http.MethodPost, "/v1/git/link", "acme", "u_acme", member, map[string]any{"owner": "acme"}); code != http.StatusForbidden {
			t.Fatalf("a member swept: %d", code)
		}
		code, body := as(t, app, http.MethodPost, "/v1/git/link", "acme", "u_acme", orgAdmin, map[string]any{"owner": "acme"})
		if code != http.StatusOK {
			t.Fatalf("sweep: %d %s", code, body)
		}
		settle(t)
		var out linked
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		names := func(vs []repoView) (n []string) {
			for _, v := range vs {
				n = append(n, v.Name)
			}
			return n
		}
		if got := strings.Join(names(out.Linked), ","); got != "closed,open" {
			t.Fatalf("linked %s, want closed,open", got)
		}
		if len(out.Unmatched) != 1 || out.Unmatched[0].Name != "gone" || out.Unmatched[0].Reason == "" {
			t.Fatalf("unmatched %+v, want gone with GitHub's answer", out.Unmatched)
		}
		if strings.Join(out.Held, ",") != "native" {
			t.Fatalf("held %v, want native", out.Held)
		}
		r, err := st.Get(context.Background(), "acme", "", "gone")
		if err != nil || r.Origin != "" {
			t.Fatalf("an unmatched repository was linked: %+v %v", r, err)
		}
		if code, body := do(t, app, http.MethodGet, "/v1/git/repos/closed/blob?path=README.md", "acme", nil); code != http.StatusOK || !strings.Contains(string(body), "# closed") {
			t.Fatalf("a swept private repository: %d %s", code, body)
		}
	})
}

// A LINK ARRIVES AT THE CODE INDEX AND THE SKILLS, as a first push would, and so
// does each push its host reports to its default branch. A feature branch is
// read on the next read and indexed nowhere, and a push another org's
// installation reports reaches none of this org's repositories. The code and
// tool planes are stand-ins under their real app names and op ids.
func TestALinkArrivesAtTheIndexAndTheSkills(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir, err := os.MkdirTemp("", "gitl") // a unix socket path is capped near 104 bytes
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("ZIP_RUNTIME_DIR", dir)
		plane.Unbind()
		cloud.ResetPlane()
		t.Cleanup(func() {
			cloud.ResetPlane()
			plane.Unbind()
			_ = os.RemoveAll(dir)
		})
		app := mountApp(t)

		var mu sync.Mutex
		var indexed []client.IndexIn
		var skills []client.SkillsIn
		cloud.Plane().Post("/code/index", func(ctx context.Context, in *client.IndexIn) (*client.Indexed, error) {
			mu.Lock()
			defer mu.Unlock()
			indexed = append(indexed, *in)
			return &client.Indexed{}, nil
		}, zip.WithOperationID(client.CodeIndex))
		cloud.Plane().Post("/code/graph", func(context.Context, *client.Tree) (*client.Restated, error) {
			return &client.Restated{}, nil
		}, zip.WithOperationID(client.CodeGraph))
		cloud.Plane().Post("/tools/skills", func(ctx context.Context, in *client.SkillsIn) (*client.Skills, error) {
			mu.Lock()
			defer mu.Unlock()
			if cloud.Who(ctx).Org == "acme" {
				skills = append(skills, *in)
			}
			return &client.Skills{}, nil
		}, zip.WithOperationID(client.ToolsSkills))
		for _, name := range []string{"code", "tool"} {
			stop, err := cloud.ServePlane(name, nil)
			if err != nil {
				t.Fatalf("serve %s plane: %v", name, err)
			}
			t.Cleanup(func() { _ = stop() })
		}

		h := serveHost(t)
		src, _ := h.repo(t, "acme", "widgets", "# widgets\n", false)
		first := h.commit(t, "acme", "widgets", "main", map[string]string{
			"main.go": "package main\n", ".agents/skills/deploy/SKILL.md": "---\nname: deploy\n---\nship it\n",
		})
		linkAs(t, app, "acme", "u_acme", "widgets", src)

		files := func(in client.IndexIn) string {
			var p []string
			for _, f := range in.Files {
				p = append(p, f.Path)
			}
			return strings.Join(p, ",")
		}
		mu.Lock()
		if len(indexed) != 1 || indexed[0].Org != "acme" || indexed[0].Repo != "widgets" || !strings.Contains(files(indexed[0]), "main.go") {
			t.Fatalf("the link's arrival indexed %+v, want acme/widgets with main.go", indexed)
		}
		if len(skills) != 1 || skills[0].Source != "widgets" || len(skills[0].Files) != 1 || skills[0].Files[0].Path != ".agents/skills/deploy/SKILL.md" {
			t.Fatalf("the link's arrival gave the skills %+v", skills)
		}
		mu.Unlock()

		next := h.commit(t, "acme", "widgets", "main", map[string]string{"lib.go": "package main\n"})
		out, err := planeLanded(cloud.For(context.Background(), "acme"), &client.LandedIn{CloneURL: src, Ref: "refs/heads/main", Before: first, After: next})
		if err != nil || strings.Join(out.Repos, ",") != "widgets" {
			t.Fatalf("landed on main: %+v %v", out, err)
		}
		settle(t)
		mu.Lock()
		if len(indexed) != 2 || !strings.Contains(files(indexed[1]), "lib.go") {
			t.Fatalf("a push to main indexed %+v, want the pushed tree", indexed)
		}
		mu.Unlock()

		feature := h.commit(t, "acme", "widgets", "feature", map[string]string{"wip.go": "package main\n"})
		if out, err := planeLanded(cloud.For(context.Background(), "acme"), &client.LandedIn{CloneURL: src, Ref: "refs/heads/feature", After: feature}); err != nil || len(out.Repos) != 1 {
			t.Fatalf("landed on a feature branch: %+v %v", out, err)
		}
		if out, err := planeLanded(cloud.For(context.Background(), "beta"), &client.LandedIn{CloneURL: src, Ref: "refs/heads/main", After: next}); err != nil || len(out.Repos) != 0 {
			t.Fatalf("another org's push reached %+v %v", out, err)
		}
		settle(t)
		mu.Lock()
		defer mu.Unlock()
		if len(indexed) != 2 || len(skills) != 2 {
			t.Fatalf("a feature branch or another org's push was indexed: %d indexes, %d skill reads", len(indexed), len(skills))
		}
		code, body := do(t, app, http.MethodGet, "/v1/git/repos/widgets/refs", "acme", nil)
		if code != http.StatusOK || !strings.Contains(string(body), feature) {
			t.Fatalf("the next read did not see the feature branch: %d %s", code, body)
		}
	})
}

// A TAG AND A BRANCH OF ONE NAME BOTH ARRIVE. A fetch that followed tags by short
// name once resolved a tag `2017` onto a branch `2017` and failed the whole
// repository; the fill fetches every branch and every tag as itself.
func TestATagNamedLikeABranchIsFilled(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h := serveHost(t)
		src, _ := h.repo(t, "acme", "demos", "# demos\n", false)
		work := t.TempDir()
		gitRun(t, "", "clone", "-q", filepath.Join(h.root, "acme", "demos.git"), work)
		gitRun(t, work, "checkout", "-q", "-b", "2017")
		writeFile(t, work, "y.txt", "2017\n")
		gitRun(t, work, "add", "-A")
		gitRun(t, work, "-c", "user.email=t@hanzo.test", "-c", "user.name=t", "commit", "-q", "-m", "2017")
		gitRun(t, work, "-c", "user.email=t@hanzo.test", "-c", "user.name=t", "tag", "-a", "2017", "-m", "2017")
		gitRun(t, work, "push", "-q", "origin", "refs/heads/2017:refs/heads/2017", "refs/tags/2017:refs/tags/2017")
		linkAs(t, app, "acme", "u_acme", "demos", src)

		code, body := do(t, app, http.MethodGet, "/v1/git/repos/demos/refs", "acme", nil)
		var refs refsJSON
		if code != http.StatusOK || json.Unmarshal(body, &refs) != nil {
			t.Fatalf("refs: %d %s", code, body)
		}
		var branch, tag bool
		for _, b := range refs.Branches {
			branch = branch || b.Name == "2017"
		}
		for _, g := range refs.Tags {
			tag = tag || g.Name == "2017"
		}
		if !branch || !tag {
			t.Fatalf("branch 2017 %v, tag 2017 %v: %s", branch, tag, body)
		}
	})
}
