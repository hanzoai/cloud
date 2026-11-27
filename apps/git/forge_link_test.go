package git

// A repository whose one home is the forge: read as the forge lets the person
// asking read it, filled under the deployment's own credential, pushed as the
// pusher's own Hanzo sign-in, and moved there from here with its refs proved.
// The forge's API is a stand-in that answers as each person; its git is the TLS
// host the GitHub tests use.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// forgeAPI answers GET /v1/repos/{owner}/{name} as the person a Sudo names:
// 200 for a repository they may read, 404 for any other.
type forgeAPI struct {
	mu      sync.Mutex
	readers map[string]map[string]bool // "owner/name" -> logins
}

func (f *forgeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rest, ok := strings.CutPrefix(r.URL.Path, forge.API+"/repos/")
	if !ok || r.Method != http.MethodGet || !f.readers[rest][r.Header.Get("Sudo")] {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	owner, name, _ := strings.Cut(rest, "/")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "full_name": owner + "/" + name, "private": true})
}

// serveForge stands the forge up: its git on h, its API answering as each
// person, the org acme's namespace there `acme`, and the deployment's
// credential the host's token.
func serveForge(t *testing.T) (*hostServer, *forgeAPI) {
	t.Helper()
	h := serveHost(t)
	providers["127.0.0.1"] = "forge"
	api := &forgeAPI{readers: map[string]map[string]bool{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	wasAs, wasFill, wasWs, wasReceive := forgeAs, forgeFill, madeWorkspace, forgeReceive
	t.Cleanup(func() { forgeAs, forgeFill, madeWorkspace, forgeReceive = wasAs, wasFill, wasWs, wasReceive })
	forgeAs = func(ctx context.Context) (*forge.Client, error) {
		c, err := forge.New(srv.URL, "machine")
		if err != nil {
			return nil, err
		}
		return c.As(zip.CallerOf(ctx).User), nil
	}
	forgeFill = func(context.Context) (gitCred, error) { return gitCred{User: "hanzo", Token: hostToken}, nil }
	madeWorkspace = func(org string, _ bool) (forge.Workspace, error) {
		return forge.Workspace{Owner: org, Made: true}, nil
	}
	forgeReceive = func(ctx context.Context, owner, name, _, branch string,
		move func(context.Context, string, gitCred) error) (string, error) {
		bare := filepath.Join(h.root, owner, name+".git")
		if _, err := os.Stat(bare); err == nil {
			return "", forge.ErrExists
		}
		gitRun(t, "", "init", "-q", "--bare", "-b", branch, bare)
		gitRun(t, "", "--git-dir="+bare, "config", "http.receivepack", "true")
		clone := h.url + "/" + owner + "/" + name + ".git"
		if err := move(ctx, clone, gitCred{User: "hanzo", Token: hostToken}); err != nil {
			_ = os.RemoveAll(bare)
			return "", err
		}
		return clone, nil
	}
	return h, api
}

func (f *forgeAPI) let(repo string, logins ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readers[repo] == nil {
		f.readers[repo] = map[string]bool{}
	}
	for _, l := range logins {
		f.readers[repo][l] = true
	}
}

// A FORGE LINK IS READ AS THE FORGE LETS THE PERSON ASKING: dave, whom the
// forge lets read it, reads through a cache filled under the deployment's
// credential; eve, whom it does not, reads nothing and is told to sign in there;
// the org itself reads its own namespace.
func TestAForgeLinkIsReadAsTheForgeLetsThePersonAsking(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h, api := serveForge(t)
		src, _ := h.repo(t, "acme", "app", "# app\n", true)
		api.let("acme/app", "dave")
		v := linkAs(t, app, "acme", "dave", "app", src)
		if v.Link == nil || v.Link.Provider != "forge" || v.Link.Owner != "acme" {
			t.Fatalf("link = %+v", v.Link)
		}
		code, body := as(t, app, http.MethodGet, "/v1/git/repos/app/blob?path=README.md", "acme", "dave", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), "# app") {
			t.Fatalf("dave: %d %s", code, body)
		}
		code, body = as(t, app, http.MethodGet, "/v1/git/repos/app/blob?path=README.md", "acme", "eve", nil, nil)
		if code != http.StatusNotFound || !strings.Contains(string(body), "sign in") {
			t.Fatalf("eve: %d %s", code, body)
		}
		if err := service(t).State.links.see(cloudFor("acme"), "acme", &link{Provider: "forge", Owner: "acme", Name: "app", CloneURL: src}); err != nil {
			t.Fatalf("the org itself was refused its own namespace: %v", err)
		}
		if err := service(t).State.links.see(cloudFor("acme"), "acme", &link{Provider: "forge", Owner: "other", Name: "app", CloneURL: h.url + "/other/app.git"}); err == nil {
			t.Fatal("the org itself read another namespace")
		}
	})
}

// A PUSH TO A FORGE LINK GOES AS THE PUSHER: their own Hanzo sign-in is the
// credential the forge takes, and nothing lands here. A push with no sign-in to
// carry (an API key) is refused and reaches nothing.
func TestAPushToAForgeLinkGoesAsThePusher(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		h, api := serveForge(t)
		src, _ := h.repo(t, "acme", "app", "# app\n", false)
		api.let("acme/app", "dave")
		h.mu.Lock()
		h.tokens["dave-iam"] = true
		h.mu.Unlock()
		linkAs(t, app, "acme", "dave", "app", src)
		url := base + "/v1/git/acme/app.git"

		work := filepath.Join(t.TempDir(), "work")
		gitRun(t, "", gitAs("acme", "dave", "clone", "-q", url, work)...)
		writeFile(t, work, "NEW.md", "as dave\n")
		gitRun(t, work, "add", "-A")
		gitRun(t, work, "-c", "user.email=t@hanzo.test", "-c", "user.name=t", "commit", "-q", "-m", "dave")
		pushed := gitOut(t, work, "rev-parse", "HEAD")
		if err := gitTry(t, work, gitAs("acme", "dave", "push", "-q", "origin", "main")...); err == nil {
			t.Fatal("a push with no sign-in to carry landed")
		}
		gitRun(t, work, append([]string{"-c", "http.extraHeader=Authorization: Bearer dave-iam"},
			gitAs("acme", "dave", "push", "-q", "origin", "main")...)...)
		if got := h.tip(t, "acme", "app"); got != pushed {
			t.Fatalf("the forge's main is %s, want %s", got, pushed)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		var asDave bool
		for _, s := range h.sent {
			asDave = asDave || (strings.Contains(s, svcReceivePack) && strings.HasSuffix(s, basicOf("hanzo", "dave-iam")))
		}
		if !asDave {
			t.Fatalf("the push did not reach the forge as dave: %v", h.sent)
		}
	})
}

// WHEN THE FORGE ANSWERS 401 TO A PERSON'S PUSH, cloud auto-provisions their forge
// identity from their IAM subject and verified email, admits hanzo members to the
// write team, and retries the push once.
func TestAPushToAForgeLinkAutoProvisionsOn401(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		h, api := serveForge(t)
		src, _ := h.repo(t, "acme", "app", "# app\n", false)
		api.let("acme/app", "dave")
		linkAs(t, app, "acme", "dave", "app", src)
		url := base + "/v1/git/acme/app.git"

		wasSrc, wasEmail := madeSource, madeEmail
		t.Cleanup(func() { madeSource, madeEmail = wasSrc, wasEmail })

		var provisioned bool
		// Mock forge client that succeeds on Provision
		forgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/admin/users") {
				provisioned = true
				h.mu.Lock()
				h.tokens["dave-iam"] = true // once provisioned, git server accepts token
				h.mu.Unlock()
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "login": "dave"})
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		}))
		t.Cleanup(forgeSrv.Close)

		madeSource = func(context.Context) (*forge.Client, error) {
			return forge.New(forgeSrv.URL, "machine")
		}
		madeEmail = func(context.Context) (*client.Email, error) {
			return &client.Email{Address: "dave@example.com", Verified: true, Name: "Dave"}, nil
		}

		work := filepath.Join(t.TempDir(), "work")
		gitRun(t, "", gitAs("acme", "dave", "clone", "-q", url, work)...)
		writeFile(t, work, "NEW.md", "retry works\n")
		gitRun(t, work, "add", "-A")
		gitRun(t, work, "-c", "user.email=dave@example.com", "-c", "user.name=dave", "commit", "-q", "-m", "retry")
		pushed := gitOut(t, work, "rev-parse", "HEAD")

		// Host initially does NOT hold dave-iam token (so first push attempt returns 401)
		err := gitTry(t, work, append([]string{"-c", "http.extraHeader=Authorization: Bearer dave-iam"},
			gitAs("acme", "dave", "push", "-q", "origin", "main")...)...)
		if err != nil {
			h.mu.Lock()
			sent := append([]string(nil), h.sent...)
			h.mu.Unlock()
			t.Fatalf("push failed: %v; provisioned=%v; sent=%v", err, provisioned, sent)
		}

		if !provisioned {
			t.Fatal("push succeeded without auto-provisioning")
		}
		if got := h.tip(t, "acme", "app"); got != pushed {
			t.Fatalf("the forge's main is %s, want %s", got, pushed)
		}
	})
}

// A REPOSITORY MOVES TO THE FORGE with its refs proved there, and is then a link
// read through the cache. Moving it twice, or over a forge repository of its
// name, is refused, and a member may not move one.
func TestARepositoryMovesToTheForge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h, api := serveForge(t)
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/probe/push", "acme", map[string]any{
			"branch": "trunk", "files": []map[string]any{{"path": "a.txt", "content": "ours\n"}},
		}); code != http.StatusOK {
			t.Fatalf("push: %d %s", code, b)
		}
		here := refTip(t, app, "acme", "probe", "trunk")

		if code, _ := as(t, app, http.MethodPost, "/v1/git/repos/probe/home", "acme", "u_acme", member, nil); code != http.StatusForbidden {
			t.Fatalf("a member moved a repository: %d", code)
		}
		code, body := as(t, app, http.MethodPost, "/v1/git/repos/probe/home", "acme", "u_acme", orgAdmin, nil)
		if code != http.StatusOK {
			t.Fatalf("home: %d %s", code, body)
		}
		var v repoView
		if err := json.Unmarshal(body, &v); err != nil || v.Link == nil || v.Link.Provider != "forge" || v.Link.Owner != "acme" {
			t.Fatalf("moved %s %v", body, err)
		}
		if got := gitOut(t, "", "--git-dir="+filepath.Join(h.root, "acme", "probe.git"), "rev-parse", "trunk"); got != here {
			t.Fatalf("the forge's trunk is %s, want %s", got, here)
		}
		if head := gitOut(t, "", "--git-dir="+filepath.Join(h.root, "acme", "probe.git"), "symbolic-ref", "HEAD"); head != "refs/heads/trunk" {
			t.Fatalf("the forge's default is %s, want trunk", head)
		}
		api.let("acme/probe", "u_acme")
		code, body = do(t, app, http.MethodGet, "/v1/git/repos/probe/blob?ref=trunk&path=a.txt", "acme", nil)
		if code != http.StatusOK || !strings.Contains(string(body), "ours") {
			t.Fatalf("read after the move: %d %s", code, body)
		}
		if code, _ := as(t, app, http.MethodPost, "/v1/git/repos/probe/home", "acme", "u_acme", orgAdmin, nil); code != http.StatusConflict {
			t.Fatalf("a second move answered %d", code)
		}

		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/taken/push", "acme", map[string]any{
			"files": []map[string]any{{"path": "a.txt", "content": "x\n"}},
		}); code != http.StatusOK {
			t.Fatalf("push: %d %s", code, b)
		}
		h.repo(t, "acme", "taken", "# theirs\n", false)
		if code, _ := as(t, app, http.MethodPost, "/v1/git/repos/taken/home", "acme", "u_acme", orgAdmin, nil); code != http.StatusConflict {
			t.Fatalf("a move over a forge repository of that name answered %d", code)
		}
	})
}

// cloudFor is a context on which the org itself asks, with no person.
func cloudFor(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org})
}

// basicOf is a Basic Authorization header's value.
func basicOf(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}
