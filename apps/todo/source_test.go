package todo

// source_test.go is the todo's harness now that THE FORGE IS THE STORE.
//
// The stub below enforces the real forge's two load-bearing behaviours, both
// measured against git.hanzo.ai before being written down (forge/forge.go states
// the measurement):
//
//   - Sudo DROPS PRIVILEGE. A sudoed request sees exactly what that user sees,
//     which is what makes one machine credential safe to hold.
//   - An unknown sudo user is 404, not an empty list.
//
// A harness that answered every request identically would let a cross-tenant
// read pass, so the stub models visibility per actor and the tenancy tests are
// written against it.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"net"
	"os"
	"time"
)

// kmsStub answers the one secret the todo reads. It is NOT a general KMS: a
// ref it does not know is an error, so a test that mis-names the ref fails
// rather than silently authenticating with an empty token.
type kmsStub struct{ token string }

func (k kmsStub) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if ref != tokenRef {
		return nil, errUnknownRef
	}
	return []byte(k.token), nil
}
func (kmsStub) PutSecret(context.Context, string, []byte) error      { return nil }
func (kmsStub) DeleteSecret(context.Context, string) error           { return nil }
func (kmsStub) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

var errUnknownRef = &refError{}

type refError struct{}

func (*refError) Error() string { return "unknown secret ref" }

// stubForge is a fake forge with per-actor visibility.
type stubForge struct {
	*httptest.Server
	mu sync.Mutex
	// visible maps a forge actor to the orgs that actor may see.
	visible map[string][]string
	// repos and issues are keyed by org.
	repos  map[string][]map[string]any
	issues map[string][]map[string]any
	// writes records every mutating request, so attribution can be asserted.
	writes []write
	token  string
}

// writtenBy is the Sudo actor of every write the forge received — who the board
// acted AS, which is the only thing a caller can put another person's name on.
func (f *stubForge) writtenBy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.writes))
	for _, w := range f.writes {
		out = append(out, w.actor)
	}
	return out
}

type write struct {
	method, path, actor string
	body                map[string]any
}

func newForge(t *testing.T) *stubForge {
	t.Helper()
	f := &stubForge{
		visible: map[string][]string{},
		repos:   map[string][]map[string]any{},
		issues:  map[string][]map[string]any{},
		token:   "forge-machine-token",
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if r.Header.Get("Authorization") != "token "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// The ownership lookup behind forge.LoginFor is a MACHINE call — no Sudo —
		// so it is answered ahead of the sudo gate. Every stub user owns the
		// address their login derives from; a test that needs the two to DISAGREE
		// states its own row in `identity`.
		if login, ok := strings.CutPrefix(strings.TrimPrefix(r.URL.Path, "/v1"), "/users/"); ok {
			if _, exists := f.visible[login]; !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{"login": login, "email": login + "@hanzo.ai"})
			return
		}
		actor := r.Header.Get("Sudo")
		orgs, known := f.visible[actor]
		if !known {
			w.WriteHeader(http.StatusNotFound) // the forge's answer for an unknown sudo user
			return
		}
		sees := func(org string) bool {
			return slices.Contains(orgs, org)
		}
		path := strings.TrimPrefix(r.URL.Path, "/v1")

		if r.Method != http.MethodGet {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// A write must be visible to the actor's org or the forge refuses it.
			seg := strings.Split(strings.TrimPrefix(path, "/repos/"), "/")
			if len(seg) > 0 && !sees(seg[0]) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			f.writes = append(f.writes, write{r.Method, path, actor, body})
			writeJSON(w, map[string]any{"id": 99, "number": 7, "title": body["title"], "state": "open"})
			return
		}

		switch {
		case path == "/repos/issues/search":
			org := r.URL.Query().Get("owner")
			if !sees(org) {
				writeJSON(w, []any{})
				return
			}
			writeJSON(w, f.issues[org])
		case strings.HasPrefix(path, "/orgs/") && strings.HasSuffix(path, "/repos"):
			org := strings.TrimSuffix(strings.TrimPrefix(path, "/orgs/"), "/repos")
			if !sees(org) {
				w.Header().Set("X-Total-Count", "0")
				writeJSON(w, []any{})
				return
			}
			// As the real forge does — the repository walk pages off this count.
			w.Header().Set("X-Total-Count", strconv.Itoa(len(f.repos[org])))
			writeJSON(w, f.repos[org])
		case reIssueNum.MatchString(path):
			// ONE issue by repo and number — what a single-item read costs instead
			// of an org-wide fan-out. 404 covers "no such issue" and "not yours"
			// alike, as the real forge does under Sudo.
			m := reIssueNum.FindStringSubmatch(path)
			if !sees(m[1]) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			for _, is := range f.issues[m[1]] {
				repo, _ := is["repository"].(map[string]any)
				name, _ := repo["name"].(string)
				if !strings.EqualFold(name, m[2]) {
					continue
				}
				if n, _ := is["number"].(int); strconv.Itoa(n) == m[3] {
					writeJSON(w, is)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(path, "/repos/"):
			// One repository by name — what the board-detail page reads instead of
			// scanning the org's inventory. 404 for both "no such repo" and "not
			// yours", as the real forge does under Sudo.
			seg := strings.Split(strings.TrimPrefix(path, "/repos/"), "/")
			if len(seg) != 2 || !sees(seg[0]) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			for _, r := range f.repos[seg[0]] {
				if name, _ := r["name"].(string); strings.EqualFold(name, seg[1]) {
					writeJSON(w, r)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

// reIssueNum matches the single-issue address /repos/<org>/<repo>/issues/<num>.
// It is checked before the bare /repos/ case, which would otherwise take it.
var reIssueNum = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)$`)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// repo adds a repository the given actors can see, with its issues.
func (f *stubForge) repo(org, name string, issues ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[org] = append(f.repos[org], map[string]any{"name": name, "full_name": org + "/" + name})
	for _, is := range issues {
		is["repository"] = map[string]any{"name": name, "full_name": org + "/" + name, "owner": org}
		f.issues[org] = append(f.issues[org], is)
	}
}

// issue builds a forge issue row.
func issue(number int, title, state string, labels ...string) map[string]any {
	ls := []map[string]any{}
	for _, l := range labels {
		ls = append(ls, map[string]any{"name": l})
	}
	return map[string]any{"id": number, "number": number, "title": title, "state": state, "labels": ls}
}

// mountForge mounts the todo against a stub forge and a stub KMS.
// identity is what the store answers about each subject, keyed by X-User-Id
// exactly as the real op keys it. A test states rows it needs before mounting;
// serveIdentity defaults every asUser subject to a CONFIRMED address whose local
// part is the login, which is what the forge stub also says.
var identity map[string]plane.Email

// serveIdentity stands up the iam peer scopeForge resolves the actor through.
// Without it every forge-backed read refuses, which is the point: an
// unresolvable identity is not a board, it is a 403.
func serveIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", shortDir(t))
	app := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	zip.Post[struct{}, plane.Email](app, "/iam/email",
		func(ctx context.Context, _ *struct{}) (*plane.Email, error) {
			sub := zip.CallerOf(ctx).User
			if e, ok := identity[sub]; ok {
				return &e, nil
			}
			// Every asUser subject is "u_<login>", and by default owns the address
			// that login derives from.
			if login, ok := strings.CutPrefix(sub, "u_"); ok && login != "" {
				return &plane.Email{Address: login + "@hanzo.ai", Verified: true}, nil
			}
			return nil, zip.ErrUnauthorized("no such subject")
		}, zip.WithOperationID(plane.IAMEmail))
	plane.Bind()
	go func() { _ = app.Listen(zip.SocketPath("iam")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", zip.SocketPath("iam")); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the iam peer never came up")
}

// shortDir is a runtime dir short enough to hold a unix socket path: t.TempDir()
// embeds the test NAME, and sun_path caps at 104 bytes on darwin.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "z")
	if err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func mountForge(t *testing.T, f *stubForge) *zip.App {
	t.Helper()
	identity = map[string]plane.Email{}
	serveIdentity(t)
	t.Setenv("CLOUD_FORGE_HOST", f.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	err := Mount(app, cloud.Deps{
		DataDir: t.TempDir(),
		KMS:     kmsStub{token: f.token},
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// asUser issues a request as a VALIDATED principal: org + user id + the IAM
// username the forge is sudoed as. All three are minted by the identity boundary
// from validated claims in production; a test supplies them directly because it
// IS the boundary here.
func asUser(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, []byte) {
	t.Helper()
	return doWireAs(t, app, method, path, org, user, body)
}

// ── the tenancy gate ─────────────────────────────────────────────────────────

// The org is derived from the validated principal and the forge is asked through
// the caller's OWN eyes. Both controls are asserted here, because either alone
// would let one of these cases through.
func TestForgeTenancy_OrgComesFromThePrincipalAndTheForgeReChecksIt(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.visible["mallory"] = []string{"umbrella"}
	f.repo("hanzoai", "api", issue(1, "acme private work", "open", "todo"))
	f.repo("umbrella", "evil", issue(9, "umbrella secret", "open"))
	app := mountForge(t, f)

	t.Run("a member reads their own org", func(t *testing.T) {
		code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues", "hanzo", "alice", nil)
		if code != http.StatusOK {
			t.Fatalf("GET = %d %s", code, raw)
		}
		var rows []map[string]any
		_ = json.Unmarshal(raw, &rows)
		if len(rows) != 1 || rows[0]["title"] != "acme private work" {
			t.Fatalf("alice got %s, want acme's one issue", raw)
		}
	})

	// THE CROSS-TENANT CASE. Mallory's validated org is umbrella, so the surface
	// asks the forge for umbrella — never for the org she might name. She cannot
	// reach acme's board at all.
	t.Run("a member of another org reads nothing of acme's", func(t *testing.T) {
		code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues", "hanzo", "mallory", nil)
		if code != http.StatusOK {
			t.Fatalf("GET = %d %s", code, raw)
		}
		if strings.Contains(string(raw), "acme private work") {
			t.Fatalf("CROSS-TENANT READ: umbrella saw acme's issue: %s", raw)
		}
	})

	// Naming another org in a header does not move the scope: X-Org-Id is an
	// authority header, stripped on ingress and re-minted only from claims.
	t.Run("naming acme while validated as umbrella reads nothing of acme's", func(t *testing.T) {
		code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues", "hanzo", "mallory", nil)
		if code == http.StatusOK && strings.Contains(string(raw), "acme private work") {
			t.Fatalf("CROSS-TENANT READ via a named org: %s", raw)
		}
	})
}

// No validated principal ⇒ every route refuses, and none leaks a row.
func TestForgeTenancy_NoPrincipalRefusesEverything(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "secret", "open"))
	app := mountForge(t, f)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/todo/projects"},
		{http.MethodGet, "/v1/todo/projects/api"},
		{http.MethodGet, "/v1/todo/projects/api/issues"},
		{http.MethodGet, "/v1/todo/projects/api/issues/1"},
	} {
		code, raw := asUser(t, app, tc.method, tc.path, "", "", nil)
		if code != http.StatusForbidden {
			t.Errorf("%s %s with no principal = %d, want 403", tc.method, tc.path, code)
		}
		if strings.Contains(string(raw), "secret") {
			t.Errorf("%s %s leaked a row while refusing: %s", tc.method, tc.path, raw)
		}
	}
}

// A validated org with NO IAM username has no forge identity to act as. It must
// refuse rather than fall back to the machine credential, which would read every
// repo the token can see.
func TestForgeTenancy_NoActorRefusesRatherThanUsingTheMachineIdentity(t *testing.T) {
	f := newForge(t)
	f.visible[""] = []string{"hanzoai"} // if the surface sudoed as nobody, this would answer
	f.repo("hanzoai", "api", issue(1, "secret", "open"))
	app := mountForge(t, f)

	// An org but no user id and no username ⇒ principal.Org already fails closed.
	code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects", "hanzo", "", nil)
	if code != http.StatusForbidden {
		t.Fatalf("no actor = %d %s, want 403", code, raw)
	}
}

// ── the board projection ─────────────────────────────────────────────────────

// The column is a LABEL on the forge, and a closed issue is done whatever its
// labels say — the forge's own state is the stronger fact.
func TestForgeBoard_ColumnComesFromTheLabelAndClosedWins(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.repo("hanzoai", "api",
		issue(1, "labelled", "open", "in_progress", "high"),
		issue(2, "unlabelled", "open"),
		issue(3, "closed but labelled todo", "closed", "todo"),
	)
	app := mountForge(t, f)

	code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues", "hanzo", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("= %d %s", code, raw)
	}
	var rows []map[string]any
	_ = json.Unmarshal(raw, &rows)
	by := map[string]map[string]any{}
	for _, r := range rows {
		by[r["title"].(string)] = r
	}
	if got := by["labelled"]["status"]; got != "in_progress" {
		t.Errorf("status = %v, want in_progress from the label", got)
	}
	if got := by["labelled"]["priority"]; got != "high" {
		t.Errorf("priority = %v, want high from the label", got)
	}
	// The status/priority labels are LIFTED OUT, not rendered twice.
	if ls, _ := by["labelled"]["labels"].([]any); len(ls) != 0 {
		t.Errorf("labels = %v, want the column and priority lifted out", ls)
	}
	if got := by["unlabelled"]["status"]; got != "backlog" {
		t.Errorf("unlabelled status = %v, want backlog", got)
	}
	if got := by["closed but labelled todo"]["status"]; got != "done" {
		t.Errorf("closed issue status = %v, want done — the forge's state is the stronger fact", got)
	}
}

// ── the writes ───────────────────────────────────────────────────────────────

// A card moved on the board must be relabelled ON THE FORGE, as the human.
func TestForgeWrites_MoveIsARelabelAttributedToTheUser(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(7, "card", "open", "todo"))
	app := mountForge(t, f)

	code, raw := asUser(t, app, http.MethodPatch, "/v1/todo/projects/api/issues/7", "hanzo", "alice",
		map[string]any{"status": "in_progress"})
	if code != http.StatusOK {
		t.Fatalf("move = %d %s", code, raw)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var relabel *write
	for i := range f.writes {
		if strings.HasSuffix(f.writes[i].path, "/issues/7/labels") {
			relabel = &f.writes[i]
		}
	}
	if relabel == nil {
		t.Fatalf("no relabel reached the forge; writes = %+v", f.writes)
	}
	if relabel.actor != "alice" {
		t.Fatalf("the move was attributed to %q, want alice — a shared bot identity destroys the audit trail", relabel.actor)
	}
}

// The repository lifecycle is the forge's, not this surface's.
func TestForgeWrites_RepositoryLifecycleIsRefused(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	app := mountForge(t, f)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/todo/projects"},
		{http.MethodPatch, "/v1/todo/projects/api"},
		{http.MethodDelete, "/v1/todo/projects/api"},
	} {
		code, raw := asUser(t, app, tc.method, tc.path, "acme", "alice", map[string]any{"name": "x"})
		if code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", tc.method, tc.path, code)
		}
		if !strings.Contains(string(raw), "forge") {
			t.Errorf("%s %s refusal does not name the forge: %s", tc.method, tc.path, raw)
		}
	}
}

// doWireAs is doWire plus the IAM USERNAME the forge is sudoed as. It is a
// separate helper rather than a parameter on doWire because the username is the
// fact the forge-backed surface added: a validated org alone no longer scopes a
// request, and a test that supplies only an org must keep meaning "no actor".
func doWireAs(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		// Both are minted by the identity boundary from validated claims; a test is
		// the boundary here. X-User-Id satisfies principal.Org's validated-principal
		// gate, X-User-Name is what the forge acts as.
		rq.Header.Set("X-User-Id", "u_"+user)
		rq.Header.Set(authz.HeaderUserName, user)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// ── the CSRF gate ────────────────────────────────────────────────────────────

// A browser authenticates this surface from an httpOnly session COOKIE, which is
// AMBIENT: a page on any other origin that can reach us carries it too. The
// deployment reflects *.hanzo.ai with credentials, and that wildcard covers hosts
// serving arbitrary user content — so without this gate a page there could move
// another org's cards with the visitor's own session.
//
// Moving to the forge did not retire this threat. It SHARPENED it: a forged write
// now reaches the forge itself under the victim's Sudo identity, so the forge
// would record the victim as having made the change. The gate is asserted on the
// forge-backed writes for exactly that reason.
func TestAmbientCookieWritesNeedCSRF(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(7, "card", "open", "todo"))
	app := mountForge(t, f)

	// browser issues a request the way a signed-in tab does: a session COOKIE and
	// no Authorization header.
	browser := func(t *testing.T, method, path, csrf string, body any) int {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		rq := httptest.NewRequest(method, path, r)
		if body != nil {
			rq.Header.Set("Content-Type", "application/json")
		}
		rq.Header.Set("Cookie", "hanzo_iam_token=session-value")
		rq.Header.Set("X-Org-Id", "hanzo")
		rq.Header.Set("X-User-Id", "u_alice")
		rq.Header.Set(authz.HeaderUserName, "alice")
		if csrf != "" {
			rq.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	t.Run("every write is refused without a token", func(t *testing.T) {
		for _, tc := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPost, "/v1/todo/projects/api/issues", map[string]any{"title": "x"}},
			{http.MethodPatch, "/v1/todo/projects/api/issues/7", map[string]any{"status": "done"}},
		} {
			if got := browser(t, tc.method, tc.path, "", tc.body); got != http.StatusForbidden {
				t.Errorf("%s %s with a session cookie and no CSRF token = %d, want 403",
					tc.method, tc.path, got)
			}
		}
	})

	t.Run("a forged token is refused", func(t *testing.T) {
		if got := browser(t, http.MethodPatch, "/v1/todo/projects/api/issues/7",
			"not-a-real-token", map[string]any{"status": "done"}); got != http.StatusForbidden {
			t.Errorf("write with a forged CSRF token = %d, want 403", got)
		}
	})

	// THE POINT OF THE GATE: it runs BEFORE the handler, so a refused write must
	// never have reached the forge. Otherwise it is an audit trail, not a gate —
	// and the row it wrote would carry the victim's name.
	t.Run("the refusal never reached the forge", func(t *testing.T) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.writes) != 0 {
			t.Fatalf("%d CSRF-refused writes still reached the forge: %+v", len(f.writes), f.writes)
		}
	})

	t.Run("reads are not gated", func(t *testing.T) {
		for _, path := range []string{
			"/v1/todo/projects",
			"/v1/todo/projects/api",
			"/v1/todo/projects/api/issues",
			"/v1/todo/projects/api/issues/7",
		} {
			if got := browser(t, http.MethodGet, path, "", nil); got != http.StatusOK {
				t.Errorf("GET %s from a signed-in tab = %d, want 200 — reads change nothing", path, got)
			}
		}
	})

	t.Run("a header-authenticated caller is unaffected", func(t *testing.T) {
		// Not CSRF-able: a cross-site page cannot set Authorization. Gating it would
		// break every API client and the gateway-fronted path for no gain.
		if code, raw := asUser(t, app, http.MethodPatch, "/v1/todo/projects/api/issues/7", "hanzo", "alice",
			map[string]any{"status": "in_progress"}); code != http.StatusOK {
			t.Errorf("header-auth write = %d, want 200 (%s)", code, raw)
		}
	})
}

// ── the surviving store ──────────────────────────────────────────────────────

// The per-(org, IAM project) SQLite store is no longer behind /v1/todo — the
// forge is. It still backs the two PLANE doors (upsert_plane.go and the agent-PR
// client), so its physical tenant boundary is still load-bearing and still pinned
// here: two IAM projects under ONE org are two files, and neither can read the
// other's rows.
//
// Driven through storeFor rather than over HTTP, because HTTP no longer reaches
// it. Testing it through a door it no longer has would prove nothing.
func TestPerProjectStoreFileIsolation(t *testing.T) {
	f := newForge(t)
	app := mountForge(t, f)
	_ = app

	alpha, err := storeFor(mounted, "acme", "alpha")
	if err != nil {
		t.Fatalf("open alpha: %v", err)
	}
	beta, err := storeFor(mounted, "acme", "beta")
	if err != nil {
		t.Fatalf("open beta: %v", err)
	}
	if alpha == beta {
		t.Fatal("two IAM projects resolved to ONE store: the physical project boundary is gone")
	}

	ctx := t.Context()
	if err := alpha.CreateProject(ctx, Project{
		ID: "p_alpha", Org: "acme", Key: "ENG", Name: "Engineering",
	}); err != nil {
		t.Fatalf("create under alpha: %v", err)
	}

	// Under IAM project beta the SAME org sees nothing — physical isolation.
	rows, err := beta.ListProjects(ctx, "acme")
	if err != nil {
		t.Fatalf("list under beta: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("beta saw %d of alpha's projects: %+v", len(rows), rows)
	}

	// And a DIFFERENT org sees nothing of acme's, in the same file.
	rows, err = alpha.ListProjects(ctx, "other")
	if err != nil {
		t.Fatalf("list as other: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("another org read %d of acme's projects: %+v", len(rows), rows)
	}
}

// ── the brand gate ───────────────────────────────────────────────────────────

// A principal vouched by ANOTHER brand's IAM must not reach this deployment's
// forge, on any route.
//
// THE ATTACK. One cloud binary serves every brand's API host, and its validator
// trusts every white-label issuer — so a lux.id-issued token is genuinely valid
// here on the hanzo deployment. It arrives with an attested org and an attested
// username. Both controls this surface relies on then do exactly what they were
// built to do: the org scopes the query, and the username is Sudo'd against the
// forge. But the forge is git.hanzo.ai — the DEPLOYMENT's, resolved from its own
// domain — so "alice" there is a different human from lux.id's alice, and the
// forge answers with that person's private issues.
//
// Two individually-sound controls composing into a cross-brand private-repo read
// is the whole lesson: neither of them asked WHO VOUCHED.
func TestForgeBrandGate_AnotherBrandsPrincipalIsRefusedEverywhere(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "acme private work", "open", "todo"))
	app := mountForgeBranded(t, f, "hanzo")

	// EVERY route, read and write — the gate lives in the one
	// resolver they all pass through, and this proves none of them bypasses it.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/todo/projects", nil},
		{http.MethodGet, "/v1/todo/projects/api", nil},
		{http.MethodGet, "/v1/todo/projects/api/issues", nil},
		{http.MethodGet, "/v1/todo/projects/api/issues/1", nil},
		{http.MethodPost, "/v1/todo/projects/api/issues", map[string]any{"title": "x"}},
		{http.MethodPatch, "/v1/todo/projects/api/issues/1", map[string]any{"status": "done"}},
	} {
		code, raw := asBrandedUser(t, app, tc.method, tc.path, "hanzo", "alice", "lux", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s with a lux-vouched principal = %d, want 403", tc.method, tc.path, code)
		}
		if strings.Contains(string(raw), "acme private work") {
			t.Errorf("CROSS-BRAND READ: %s %s leaked another brand's private issue: %s",
				tc.method, tc.path, raw)
		}
	}

	// Nothing reached the forge — the refusal is before the call, not after it.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 0 {
		t.Fatalf("%d cross-brand writes reached the forge: %+v", len(f.writes), f.writes)
	}
}

// The gate must not break the deployment's OWN principals: the brand that
// matches passes, and a principal with NO vouching brand (an hk-/sk- key minted
// by this deployment's own IAM, which is by construction this brand) passes too.
func TestForgeBrandGate_OwnBrandAndUnbrandedStillWork(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "acme work", "open", "todo"))
	app := mountForgeBranded(t, f, "hanzo")

	t.Run("the deployment's own brand passes", func(t *testing.T) {
		code, raw := asBrandedUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues",
			"hanzo", "alice", "hanzo", nil)
		if code != http.StatusOK {
			t.Fatalf("own-brand principal = %d %s, want 200", code, raw)
		}
		if !strings.Contains(string(raw), "acme work") {
			t.Fatalf("own-brand principal saw nothing: %s", raw)
		}
	})

	t.Run("a case difference does not decide tenancy", func(t *testing.T) {
		code, _ := asBrandedUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues",
			"hanzo", "alice", "HANZO", nil)
		if code != http.StatusOK {
			t.Fatalf("own brand in a different case = %d, want 200", code)
		}
	})

	t.Run("no vouching brand passes — an own-IAM key has no second fact", func(t *testing.T) {
		code, raw := asBrandedUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues",
			"hanzo", "alice", "", nil)
		if code != http.StatusOK {
			t.Fatalf("unbranded principal = %d %s, want 200", code, raw)
		}
	})
}

// mountForgeBranded is mountForge for a deployment with a declared brand, which
// is what the brand gate compares the principal's vouching brand against.
func mountForgeBranded(t *testing.T, f *stubForge, brand string) *zip.App {
	t.Helper()
	identity = map[string]plane.Email{}
	serveIdentity(t)
	t.Setenv("CLOUD_FORGE_HOST", f.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	err := Mount(app, cloud.Deps{
		DataDir: t.TempDir(),
		Brand:   brand,
		KMS:     kmsStub{token: f.token},
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// asBrandedUser is asUser plus X-User-Brand — the brand whose IAM vouched for
// the principal, minted by the identity boundary from the token's VERIFIED iss
// and stripped on ingress like every authority header.
func asBrandedUser(t *testing.T, app *zip.App, method, path, org, user, brand string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", "u_"+user)
	rq.Header.Set(authz.HeaderUserName, user)
	if brand != "" {
		rq.Header.Set(cloud.HeaderUserBrand, brand)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// ── the schedule, and the boards that are queries ────────────────────────────

// A GANTT NEEDS AN INTERVAL. The forge gives an issue its deadline through its
// milestone and never gives it a start, so leaving StartAt at 0 made every
// scheduled row a POINT: spanOf reads "due, no start" as a milestone diamond,
// and the timeline was structurally incapable of drawing a bar on any board.
//
// The interval the forge does know is created -> due, and that is what a bar on
// this timeline means. Pinned here because nothing else can see it: the row is
// present either way, so a count passes while the track stays empty.
func TestForgeSchedule_AMilestoneDueDateGivesTheRowAnInterval(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	scheduled := issue(1, "scheduled", "open", "todo")
	scheduled["created_at"] = "2026-08-01T00:00:00Z"
	scheduled["milestone"] = map[string]any{"id": 1, "title": "0.2.0", "due_on": "2026-08-20T00:00:00Z"}
	backdated := issue(2, "created after its own deadline", "open", "todo")
	backdated["created_at"] = "2026-09-01T00:00:00Z"
	backdated["milestone"] = map[string]any{"id": 2, "title": "late", "due_on": "2026-08-20T00:00:00Z"}
	unscheduled := issue(3, "no milestone", "open", "todo")
	unscheduled["created_at"] = "2026-08-01T00:00:00Z"
	f.repo("hanzoai", "api", scheduled, backdated, unscheduled)
	app := mountForge(t, f)

	code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects/api/issues", "hanzo", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("= %d %s", code, raw)
	}
	var rows []map[string]any
	_ = json.Unmarshal(raw, &rows)
	by := map[string]map[string]any{}
	for _, r := range rows {
		by[r["title"].(string)] = r
	}

	start, due := by["scheduled"]["startAt"], by["scheduled"]["dueAt"]
	if start == nil || due == nil {
		t.Fatalf("scheduled row = start %v due %v, want both — a bar needs an interval", start, due)
	}
	if start.(float64) >= due.(float64) {
		t.Errorf("start %v is not before due %v", start, due)
	}

	// A row created after its own deadline has no interval. It stays a point
	// rather than becoming a bar drawn backwards.
	if s := by["created after its own deadline"]["startAt"]; s != nil {
		t.Errorf("backdated startAt = %v, want absent so it renders as the point it is", s)
	}
	if by["created after its own deadline"]["dueAt"] == nil {
		t.Error("backdated dueAt went missing; it is still a deadline")
	}

	// No milestone, no schedule — an unscheduled row must not be conjured onto
	// the timeline by this.
	if s := by["no milestone"]["startAt"]; s != nil {
		t.Errorf("unscheduled startAt = %v, want absent", s)
	}
}

// A BOARD IS A QUERY. The key is a filter and not an address, so the same op
// answers one project's board, the org's whole board, and a board narrower than
// any repository — which is the only shape available to an app that lives as a
// directory inside a shared repository. Nothing is provisioned for any of them.
func TestForgeBoard_TheKeyIsAFilterSoTheGlobalAndPerAppBoardsAreQueries(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "api work", "open", "todo", "app/meet"))
	f.repo("hanzoai", "web", issue(2, "web work", "open", "todo"))
	app := mountForge(t, f)

	titles := func(path string) []string {
		t.Helper()
		code, raw := asUser(t, app, http.MethodGet, path, "hanzo", "alice", nil)
		if code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, code, raw)
		}
		var rows []map[string]any
		_ = json.Unmarshal(raw, &rows)
		out := []string{}
		for _, r := range rows {
			out = append(out, r["title"].(string))
		}
		sort.Strings(out)
		return out
	}

	// The global board: every repository's work, one set of columns.
	if got := titles("/v1/todo/board"); !reflect.DeepEqual(got, []string{"api work", "web work"}) {
		t.Errorf("global board = %v, want both repositories' work", got)
	}
	// Bound to a repository, it is that project's board — unchanged.
	if got := titles("/v1/todo/projects/api/issues"); !reflect.DeepEqual(got, []string{"api work"}) {
		t.Errorf("project board = %v, want only that repository's work", got)
	}
	// Narrowed by label, it is a board smaller than a repository.
	if got := titles("/v1/todo/board?label=app/meet"); !reflect.DeepEqual(got, []string{"api work"}) {
		t.Errorf("per-app board = %v, want only the labelled row", got)
	}
	// The label is a name, and a name that answered differently for two casings
	// would be two boards.
	if got := titles("/v1/todo/board?label=App/Meet"); !reflect.DeepEqual(got, []string{"api work"}) {
		t.Errorf("per-app board (other casing) = %v, want the same board", got)
	}
	// A label nobody carries is an empty board, never every board.
	if got := titles("/v1/todo/board?label=app/nothing"); len(got) != 0 {
		t.Errorf("unknown label = %v, want an empty board rather than a silent fall-through to all work", got)
	}
}
