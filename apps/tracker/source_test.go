package tracker

// source_test.go is the tracker's harness now that THE FORGE IS THE STORE.
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
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// kmsStub answers the one secret the tracker reads. It is NOT a general KMS: a
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
	// repos and issues are keyed by org; milestones by "org/repo".
	repos      map[string][]map[string]any
	issues     map[string][]map[string]any
	milestones map[string][]map[string]any
	// writes records every mutating request, so attribution can be asserted.
	writes []write
	token  string
}

type write struct {
	method, path, actor string
	body                map[string]any
}

func newForge(t *testing.T) *stubForge {
	t.Helper()
	f := &stubForge{
		visible:    map[string][]string{},
		repos:      map[string][]map[string]any{},
		issues:     map[string][]map[string]any{},
		milestones: map[string][]map[string]any{},
		token:      "forge-machine-token",
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if r.Header.Get("Authorization") != "token "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		actor := r.Header.Get("Sudo")
		orgs, known := f.visible[actor]
		if !known {
			w.WriteHeader(http.StatusNotFound) // the forge's answer for an unknown sudo user
			return
		}
		sees := func(org string) bool {
			for _, o := range orgs {
				if o == org {
					return true
				}
			}
			return false
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
				writeJSON(w, []any{})
				return
			}
			writeJSON(w, f.repos[org])
		case strings.HasSuffix(path, "/milestones"):
			seg := strings.Split(strings.TrimPrefix(path, "/repos/"), "/")
			if len(seg) < 2 || !sees(seg[0]) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, f.milestones[seg[0]+"/"+seg[1]])
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

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

// mountForge mounts the tracker against a stub forge and a stub KMS.
func mountForge(t *testing.T, f *stubForge) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_FORGE_HOST", f.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	err := Mount(app, cloud.Deps{
		Logger: luxlog.New("test"), DataDir: t.TempDir(),
		KMS: kmsStub{token: f.token},
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
	f.visible["alice"] = []string{"acme"}
	f.visible["mallory"] = []string{"umbrella"}
	f.repo("acme", "api", issue(1, "acme private work", "open", "todo"))
	f.repo("umbrella", "evil", issue(9, "umbrella secret", "open"))
	app := mountForge(t, f)

	t.Run("a member reads their own org", func(t *testing.T) {
		code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects/api/issues", "acme", "alice", nil)
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
		code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects/api/issues", "umbrella", "mallory", nil)
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
		code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects/api/issues", "acme", "mallory", nil)
		if code == http.StatusOK && strings.Contains(string(raw), "acme private work") {
			t.Fatalf("CROSS-TENANT READ via a named org: %s", raw)
		}
	})
}

// No validated principal ⇒ every route refuses, and none leaks a row.
func TestForgeTenancy_NoPrincipalRefusesEverything(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"acme"}
	f.repo("acme", "api", issue(1, "secret", "open"))
	app := mountForge(t, f)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/tracker/projects"},
		{http.MethodGet, "/v1/tracker/projects/api"},
		{http.MethodGet, "/v1/tracker/projects/api/issues"},
		{http.MethodGet, "/v1/tracker/milestones"},
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
	f.visible[""] = []string{"acme"} // if the surface sudoed as nobody, this would answer
	f.repo("acme", "api", issue(1, "secret", "open"))
	app := mountForge(t, f)

	// An org but no user id and no username ⇒ principal.Org already fails closed.
	code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects", "acme", "", nil)
	if code != http.StatusForbidden {
		t.Fatalf("no actor = %d %s, want 403", code, raw)
	}
}

// ── the org rollup ───────────────────────────────────────────────────────────

// Milestones are repo-scoped on the forge; the org view is a server-side
// fan-out, and each row must name the repo it came from.
func TestForgeMilestones_OrgRollupFansOutServerSide(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"acme"}
	f.repo("acme", "api")
	f.repo("acme", "web")
	f.milestones["acme/api"] = []map[string]any{{"id": 1, "title": "v1", "state": "open", "open_issues": 3}}
	f.milestones["acme/web"] = []map[string]any{{"id": 2, "title": "launch", "state": "open", "open_issues": 5}}
	app := mountForge(t, f)

	code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/milestones", "acme", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("GET milestones = %d %s", code, raw)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("not an array: %s", raw)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d milestones, want 2 across the org: %s", len(rows), raw)
	}
	for _, m := range rows {
		if m["repo"] == "" || m["repo"] == nil {
			t.Fatalf("milestone %v does not name its repo", m)
		}
	}
}

// An empty rollup is [] and never null.
func TestForgeMilestones_EmptyIsAnArray(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"acme"}
	app := mountForge(t, f)
	code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/milestones", "acme", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("= %d %s", code, raw)
	}
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("empty rollup = %s, want []", raw)
	}
}

// ── the board projection ─────────────────────────────────────────────────────

// The column is a LABEL on the forge, and a closed issue is done whatever its
// labels say — the forge's own state is the stronger fact.
func TestForgeBoard_ColumnComesFromTheLabelAndClosedWins(t *testing.T) {
	f := newForge(t)
	f.visible["alice"] = []string{"acme"}
	f.repo("acme", "api",
		issue(1, "labelled", "open", "in_progress", "high"),
		issue(2, "unlabelled", "open"),
		issue(3, "closed but labelled todo", "closed", "todo"),
	)
	app := mountForge(t, f)

	code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects/api/issues", "acme", "alice", nil)
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
	f.visible["alice"] = []string{"acme"}
	f.repo("acme", "api", issue(7, "card", "open", "todo"))
	app := mountForge(t, f)

	code, raw := asUser(t, app, http.MethodPatch, "/v1/tracker/projects/api/issues/7", "acme", "alice",
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
	f.visible["alice"] = []string{"acme"}
	app := mountForge(t, f)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/tracker/projects"},
		{http.MethodPatch, "/v1/tracker/projects/api"},
		{http.MethodDelete, "/v1/tracker/projects/api"},
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
	f.visible["alice"] = []string{"acme"}
	f.repo("acme", "api", issue(7, "card", "open", "todo"))
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
		rq.Header.Set("X-Org-Id", "acme")
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
			{http.MethodPost, "/v1/tracker/projects/api/issues", map[string]any{"title": "x"}},
			{http.MethodPatch, "/v1/tracker/projects/api/issues/7", map[string]any{"status": "done"}},
		} {
			if got := browser(t, tc.method, tc.path, "", tc.body); got != http.StatusForbidden {
				t.Errorf("%s %s with a session cookie and no CSRF token = %d, want 403",
					tc.method, tc.path, got)
			}
		}
	})

	t.Run("a forged token is refused", func(t *testing.T) {
		if got := browser(t, http.MethodPatch, "/v1/tracker/projects/api/issues/7",
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
			"/v1/tracker/projects",
			"/v1/tracker/projects/api",
			"/v1/tracker/projects/api/issues",
			"/v1/tracker/milestones",
		} {
			if got := browser(t, http.MethodGet, path, "", nil); got != http.StatusOK {
				t.Errorf("GET %s from a signed-in tab = %d, want 200 — reads change nothing", path, got)
			}
		}
	})

	t.Run("a header-authenticated caller is unaffected", func(t *testing.T) {
		// Not CSRF-able: a cross-site page cannot set Authorization. Gating it would
		// break every API client and the gateway-fronted path for no gain.
		if code, raw := asUser(t, app, http.MethodPatch, "/v1/tracker/projects/api/issues/7", "acme", "alice",
			map[string]any{"status": "in_progress"}); code != http.StatusOK {
			t.Errorf("header-auth write = %d, want 200 (%s)", code, raw)
		}
	})
}

// ── the surviving store ──────────────────────────────────────────────────────

// The per-(org, IAM project) SQLite store is no longer behind /v1/tracker — the
// forge is. It still backs the two PLANE doors (upsert_plane.go and the agent-PR
// seam), so its physical tenant boundary is still load-bearing and still pinned
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
