package sync

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// stand_test.go builds the two git hosts these tests need, out of REAL git.
//
// There is no fake for the thing under test. The whole property — that a
// divergence is REFUSED rather than forced — is a property of `git push` talking
// to a `git receive-pack`, so a stub that returned "rejected" when we expected it
// would prove only that we can write a stub. Both ends here are actual bare
// repositories served by git-http-backend, and every assertion about a ref is a
// read of that repository afterwards.
//
// The forge stand additionally answers the handful of REST calls this client makes
// (create, read, list, set default branch) on the same origin, because the forge
// client derives the git address from its own API base — so serving them apart
// would test an arrangement production does not have.

// ── running git ──────────────────────────────────────────────────────────────

// git runs a git command and fails the test on a non-zero exit. It runs under
// the same config isolation the production path uses, so a developer's global
// gitconfig cannot change what a test proves.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// tip reads a bare repository's ref, or "" when it has none.
func tip(t *testing.T, bare, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+bare, "rev-parse", "--verify", "--quiet", ref)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ── a git host ───────────────────────────────────────────────────────────────

// host is a set of bare repositories served over smart-HTTP, with a tally of the
// OBJECT TRANSFERS it answered — which is what an advance costs and what a
// short-circuited one must not spend.
//
// Under protocol v2 a ref advertisement is itself a POST to git-upload-pack
// (command=ls-refs), so counting by path would count the cheap read as if it
// were a pack. The command in the body is what tells them apart, and it is the
// only thing this reads out of a request.
type host struct {
	t    *testing.T
	root string
	URL  string

	mu      sync.Mutex
	moved   int               // fetches + pushes: the transfers
	authFor map[string]string // git path → the Authorization header it carried
}

// bare is the on-disk path of one repository on this host.
func (h *host) bare(owner, name string) string {
	return filepath.Join(h.root, owner, name+".git")
}

// init creates an empty bare repository that accepts anonymous pushes.
func (h *host) init(owner, name string) string {
	h.t.Helper()
	dir := h.bare(owner, name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		h.t.Fatal(err)
	}
	git(h.t, "", "init", "--bare", "--quiet", "--initial-branch=main", dir)
	// git-http-backend serves receive-pack only where the repository says so.
	git(h.t, "", "--git-dir="+dir, "config", "http.receivepack", "true")
	return dir
}

// seed builds a repository with one commit on main and publishes it here,
// returning a work tree the test can add more commits to.
func (h *host) seed(owner, name, content string) *tree {
	h.t.Helper()
	dir := h.init(owner, name)
	w := h.t.TempDir()
	git(h.t, w, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(w, "f"), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
	git(h.t, w, "add", "-A")
	git(h.t, w, "commit", "--quiet", "-m", "seed")
	git(h.t, w, "remote", "add", "origin", dir)
	git(h.t, w, "push", "--quiet", "origin", "main")
	return &tree{t: h.t, dir: w, bare: dir}
}

// counts reports the object transfers answered so far.
func (h *host) counts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.moved
}

// auth returns the Authorization header seen on the most recent git request
// whose path contains want.
func (h *host) auth(want string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p, v := range h.authFor {
		if strings.Contains(p, want) {
			return v
		}
	}
	return ""
}

// gitHandler serves the bare repos under root, tallying pack requests and
// recording what credential each carried.
func (h *host) gitHandler() http.Handler {
	execPath := git(h.t, "", "--exec-path")
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + h.root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		moved := strings.HasSuffix(r.URL.Path, "/git-receive-pack")
		if strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			// Read the command out and put the body back, so the backend still sees
			// the request it would have seen.
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			rest, _ := io.ReadAll(r.Body)
			body = append(body, rest...)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			moved = !bytes.Contains(body, []byte("command=ls-refs"))
		}
		h.mu.Lock()
		if moved {
			h.moved++
		}
		if h.authFor == nil {
			h.authFor = map[string]string{}
		}
		h.authFor[r.URL.Path] = r.Header.Get("Authorization")
		h.mu.Unlock()
		backend.ServeHTTP(w, r)
	})
}

// tree is a work tree pointed at a repository on a host.
type tree struct {
	t    *testing.T
	dir  string
	bare string
}

// commit adds a commit and publishes it, returning the new tip.
func (w *tree) commit(content string) string {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.dir, "f"), []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
	git(w.t, w.dir, "add", "-A")
	git(w.t, w.dir, "commit", "--quiet", "-m", "change "+content)
	git(w.t, w.dir, "push", "--quiet", "origin", "main")
	return git(w.t, w.dir, "rev-parse", "HEAD")
}

// branch publishes a new branch at the current tip.
func (w *tree) branch(name string) string {
	w.t.Helper()
	git(w.t, w.dir, "push", "--quiet", "origin", "HEAD:refs/heads/"+name)
	return git(w.t, w.dir, "rev-parse", "HEAD")
}

// tag publishes a lightweight tag at the current tip.
func (w *tree) tag(name string) string {
	w.t.Helper()
	git(w.t, w.dir, "tag", "-f", name)
	git(w.t, w.dir, "push", "--quiet", "--force", "origin", "refs/tags/"+name)
	return git(w.t, w.dir, "rev-parse", name)
}

// newHost serves an empty git host and returns it.
func newHost(t *testing.T) *host {
	t.Helper()
	h := &host{t: t, root: t.TempDir()}
	srv := httptest.NewServer(h.gitHandler())
	t.Cleanup(srv.Close)
	h.URL = srv.URL
	return h
}

// ── the stand-in forge ───────────────────────────────────────────────────────

// forgeToken is the machine credential the stand accepts. A test asserts it
// arrives on the git push too, which is the only proof that the credential
// really travels by env-injected header and not by luck.
const forgeToken = "stand-machine-token"

// stand is a git host that ALSO answers the forge REST calls this client makes, on
// the same origin — create a repository, read one, list them, point HEAD, set
// its Actions unit, and state a branch rule.
type stand struct {
	*host
	owner string

	rulesMu     sync.Mutex
	orgs        map[string]string         // org → visibility
	rules       map[string]map[string]any // repo → the last branch rule asked for
	actions     map[string]bool           // repo → has_actions as last set; absent is the forge's default, on
	mirrors     map[string]bool           // owner/repo → true when repository is a pull mirror
	mirrorSyncs []string                  // owner/repo synced via POST /repos/{owner}/{repo}/mirror-sync
}

// markMirror marks owner/repo on this stand as a pull mirror (read-only to git
// pushes and branch edits, refreshed via POST /repos/{owner}/{repo}/mirror-sync).
func (s *stand) markMirror(owner, name string) {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	if s.mirrors == nil {
		s.mirrors = map[string]bool{}
	}
	s.mirrors[owner+"/"+name] = true
}

func (s *stand) isMirror(owner, name string) bool {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	return s.mirrors[owner+"/"+name]
}

func (s *stand) syncedMirrors() []string {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	return append([]string(nil), s.mirrorSyncs...)
}

// rule returns the branch-protection rule the client asked this stand to write on
// repo, or nil if it asked for none.
func (s *stand) rule(repo string) map[string]any {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	return s.rules[repo]
}

// hasActions reports whether repo's Actions unit is on. A repository nobody set
// it on carries the forge's default, which is on.
func (s *stand) hasActions(repo string) bool {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	on, set := s.actions[repo]
	return !set || on
}

// newStand serves a forge stand-in and points the deployment at it.
func newStand(t *testing.T, owner string) *stand {
	t.Helper()
	s := &stand{host: &host{t: t, root: t.TempDir()}, owner: owner}
	gitSide := s.gitHandler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			s.rest(w, r)
			return
		}
		gitSide.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	t.Setenv("CLOUD_FORGE_HOST", srv.URL)
	// The upstream stand and this one both live on loopback, which the SSRF gate
	// refuses.
	admitHosts(t, "127.0.0.1")
	return s
}

// admitHosts lets hosts past the SSRF gate for one test; every other host is
// still judged by outbound.Host.
func admitHosts(t *testing.T, hosts ...string) {
	t.Helper()
	was := admit
	admit = func(ctx context.Context, host string) error {
		if slices.Contains(hosts, host) {
			return nil
		}
		return was(ctx, host)
	}
	t.Cleanup(func() { admit = was })
}

// rest answers the forge endpoints this client uses. Every one of them checks the
// machine credential, so a call that lost it fails here rather than silently
// succeeding against an open stub.
func (s *stand) rest(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+forgeToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	seg := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/"), "/"), "/")
	switch {
	// POST /v1/orgs — create org.
	case r.Method == http.MethodPost && len(seg) == 1 && seg[0] == "orgs":
		var in struct {
			Username   string `json:"username"`
			Visibility string `json:"visibility"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.rulesMu.Lock()
		if s.orgs == nil {
			s.orgs = map[string]string{}
		}
		if _, taken := s.orgs[in.Username]; taken {
			s.rulesMu.Unlock()
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		vis := in.Visibility
		if vis == "" {
			vis = "private"
		}
		s.orgs[in.Username] = vis
		s.rulesMu.Unlock()
		w.WriteHeader(http.StatusCreated)
		s.reply(w, map[string]any{"username": in.Username, "visibility": vis})
	// GET /v1/orgs/{owner} — read org.
	case r.Method == http.MethodGet && len(seg) == 2 && seg[0] == "orgs":
		s.rulesMu.Lock()
		vis, ok := s.orgs[seg[1]]
		s.rulesMu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.reply(w, map[string]any{"username": seg[1], "visibility": vis})
	// POST /v1/repos/{owner}/{repo}/mirror-sync — trigger pull mirror refresh.
	case r.Method == http.MethodPost && len(seg) == 4 && seg[0] == "repos" && seg[3] == "mirror-sync":
		dir := s.bare(seg[1], seg[2])
		if _, err := os.Stat(dir); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !s.isMirror(seg[1], seg[2]) {
			http.Error(w, "repository is not a mirror", http.StatusForbidden)
			return
		}
		s.rulesMu.Lock()
		s.mirrorSyncs = append(s.mirrorSyncs, seg[1]+"/"+seg[2])
		s.rulesMu.Unlock()
		w.WriteHeader(http.StatusOK)
	// GET /v1/repos/{owner}/{repo}/branches/{branch} — read branch tip.
	case r.Method == http.MethodGet && len(seg) == 5 && seg[0] == "repos" && seg[3] == "branches":
		dir := s.bare(seg[1], seg[2])
		if _, err := os.Stat(dir); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		branch, _ := url.PathUnescape(seg[4])
		sha := tip(s.t, dir, "refs/heads/"+branch)
		if sha == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.reply(w, map[string]any{"name": branch, "commit": map[string]any{"id": sha}})
	// POST /v1/repos/{owner}/{repo}/branch_protections — state a branch rule.
	//
	// The rule is REMEMBERED rather than enforced: this stand serves real
	// git-http-backend, which knows nothing of the forge's pre-receive, so what a
	// test can prove here is that the client asked for the right rule. That it is
	// the rule the forge acts on is the forge's own contract (protect.go).
	case r.Method == http.MethodPost && len(seg) == 4 && seg[0] == "repos" && seg[3] == "branch_protections":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.rulesMu.Lock()
		if s.rules == nil {
			s.rules = map[string]map[string]any{}
		}
		s.rules[seg[2]] = in
		s.rulesMu.Unlock()
		s.reply(w, in)
	// GET /v1/repos/{owner}/{repo}/branch_protections — read the rules back.
	//
	// The client reads before it writes, because the forge answers a create for a
	// rule it already holds with 403, the status it also uses for a refusal. A
	// stand that only accepted the write let every sync of an existing repository
	// fail here on a 404 instead.
	case r.Method == http.MethodGet && len(seg) == 4 && seg[0] == "repos" && seg[3] == "branch_protections":
		s.rulesMu.Lock()
		held := s.rules[seg[2]]
		s.rulesMu.Unlock()
		out := []map[string]any{}
		if held != nil {
			out = append(out, held)
		}
		s.reply(w, out)
	// PATCH /v1/repos/{owner}/{repo}/branch_protections/{rule} — correct one in
	// place, which is what the client does with a rule that is present and weaker.
	case r.Method == http.MethodPatch && len(seg) == 5 && seg[0] == "repos" && seg[3] == "branch_protections":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.rulesMu.Lock()
		if s.rules == nil {
			s.rules = map[string]map[string]any{}
		}
		held := s.rules[seg[2]]
		if held == nil {
			held = map[string]any{}
		}
		name, _ := url.PathUnescape(seg[4])
		held["rule_name"] = name
		for k, v := range in {
			held[k] = v
		}
		s.rules[seg[2]] = held
		s.rulesMu.Unlock()
		s.reply(w, held)
	// POST /v1/orgs/{owner}/repos — create.
	case r.Method == http.MethodPost && len(seg) == 3 && seg[0] == "orgs" && seg[2] == "repos":
		var in struct {
			Name     string `json:"name"`
			AutoInit bool   `json:"auto_init"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.AutoInit {
			// The stand refuses what production must never ask for: a repository
			// born with a commit is a repository born in conflict with its upstream.
			http.Error(w, "auto_init would seed a commit the upstream does not have", http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(s.bare(seg[1], in.Name)); err == nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		s.init(seg[1], in.Name)
		s.reply(w, s.repoJSON(seg[1], in.Name))
	// GET /v1/orgs/{owner}/repos — list.
	case r.Method == http.MethodGet && len(seg) == 3 && seg[0] == "orgs" && seg[2] == "repos":
		var out []any
		names, _ := filepath.Glob(filepath.Join(s.root, seg[1], "*.git"))
		for _, n := range names {
			out = append(out, s.repoJSON(seg[1], strings.TrimSuffix(filepath.Base(n), ".git")))
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(len(out)))
		s.reply(w, out)
	// GET|PATCH /v1/repos/{owner}/{repo}
	case len(seg) == 3 && seg[0] == "repos":
		dir := s.bare(seg[1], seg[2])
		if _, err := os.Stat(dir); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPatch {
			if s.isMirror(seg[1], seg[2]) {
				http.Error(w, "Git Repository is a mirror.", http.StatusForbidden)
				return
			}
			// Like the forge, a PATCH changes only the fields it carries.
			var in struct {
				Default *string `json:"default_branch"`
				Actions *bool   `json:"has_actions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Default != nil {
				if tip(s.t, dir, "refs/heads/"+*in.Default) == "" {
					http.Error(w, "no such branch", http.StatusUnprocessableEntity)
					return
				}
				git(s.t, "", "--git-dir="+dir, "symbolic-ref", "HEAD", "refs/heads/"+*in.Default)
			}
			if in.Actions != nil {
				s.rulesMu.Lock()
				if s.actions == nil {
					s.actions = map[string]bool{}
				}
				s.actions[seg[2]] = *in.Actions
				s.rulesMu.Unlock()
			}
		}
		s.reply(w, s.repoJSON(seg[1], seg[2]))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *stand) repoJSON(owner, name string) map[string]any {
	head := ""
	dir := s.bare(owner, name)
	cmd := exec.Command("git", "--git-dir="+dir, "symbolic-ref", "--short", "HEAD")
	if out, err := cmd.Output(); err == nil {
		head = strings.TrimSpace(string(out))
	}
	return map[string]any{
		"name": name, "full_name": owner + "/" + name,
		"default_branch": head, "private": true, "mirror": s.isMirror(owner, name),
		"permissions": map[string]any{"admin": true, "push": true, "pull": true},
		"ssh_url":     "ssh://git@stand/" + owner + "/" + name + ".git",
	}
}

func (s *stand) reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ── the deployment under test ────────────────────────────────────────────────

// vault is the KMS a test mounts: it holds the forge machine credential and
// nothing else, so a code path reaching for another secret fails loudly.
type vault struct{}

func (vault) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if ref == "orgs/hanzo/deploy/FORGE_TRACKER_TOKEN@prod" {
		return []byte(forgeToken), nil
	}
	return nil, fmt.Errorf("no secret at %s", ref)
}
func (vault) PutSecret(context.Context, string, []byte) error      { return nil }
func (vault) DeleteSecret(context.Context, string) error           { return nil }
func (vault) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

// mountForge mounts the sync app with a KMS that holds the forge credential —
// the arrangement every git test here runs under.
func mountForge(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_DOMAIN", "api.hanzo.ai")
	if err := Use(app, cloud.Deps{
		KMS: vault{},
	}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// basic is the Authorization header value the forge credential should arrive as.
func basic() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+forgeToken))
}
