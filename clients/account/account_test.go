package account

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeIAM is a stand-in for Hanzo IAM's /v1/iam/* management surface. It records
// the confidential-client Basic auth it received (so a test can assert the console
// authenticates as `hanzo-console`, NOT as the caller) and the exact `id` each
// privileged op targeted (so a test can prove the console only ever acts on the
// validated caller's own `<owner>/<name>`). It never needs a real cluster.
type fakeIAM struct {
	mu sync.Mutex

	// state
	keys map[string]string         // id → current hk- key ("" = none)
	orgs map[string]map[string]any // slug → org row (nil map = absent)
	user map[string]map[string]any // id → full user row (for the move)

	// captured
	gotAuth     string   // Authorization header on the last request
	mintedFor   []string // ids mint-user-keys was called with
	revokedFor  []string
	movedTo     map[string]string // id → new owner (from update-user)
	createdOrgs []map[string]any
	failAddOrg  bool // when true, add-organization answers status!=ok
	failMintKey bool
}

func newFakeIAM() *fakeIAM {
	return &fakeIAM{
		keys:    map[string]string{},
		orgs:    map[string]map[string]any{},
		user:    map[string]map[string]any{},
		movedTo: map[string]string{},
	}
}

func (f *fakeIAM) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	ok := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "msg": "", "data": data})
	}
	bad := func(w http.ResponseWriter, msg string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "msg": msg, "data": nil})
	}

	mux.HandleFunc("/v1/iam/get-user", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		id := r.URL.Query().Get("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		if row, present := f.user[id]; present {
			// A configured user row wins (used by the onboarding move).
			out := map[string]any{}
			for k, v := range row {
				out[k] = v
			}
			if key, has := f.keys[id]; has {
				out["accessKey"] = key
			}
			ok(w, out)
			return
		}
		// Otherwise synthesize a thin row carrying just the key state.
		ok(w, map[string]any{"accessKey": f.keys[id], "updatedTime": "2026-01-02T03:04:05Z"})
	})

	mux.HandleFunc("/v1/iam/mint-user-keys", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		id := r.URL.Query().Get("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mintedFor = append(f.mintedFor, id)
		if f.failMintKey {
			bad(w, "mint failed")
			return
		}
		key := "hk-" + strings.ReplaceAll(id, "/", "-") + "-SECRET"
		f.keys[id] = key
		ok(w, map[string]any{"accessKey": key})
	})

	mux.HandleFunc("/v1/iam/revoke-user-keys", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		id := r.URL.Query().Get("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.revokedFor = append(f.revokedFor, id)
		f.keys[id] = ""
		ok(w, map[string]any{})
	})

	mux.HandleFunc("/v1/iam/get-organization", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		id := r.URL.Query().Get("id") // admin/<slug>
		slug := id
		if i := strings.IndexByte(id, '/'); i >= 0 {
			slug = id[i+1:]
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if row, present := f.orgs[slug]; present && row != nil {
			ok(w, row)
			return
		}
		bad(w, "organization does not exist") // not-ok ⇒ getOrganization returns (nil,nil)
	})

	mux.HandleFunc("/v1/iam/add-organization", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		body, _ := io.ReadAll(r.Body)
		var row map[string]any
		_ = json.Unmarshal(body, &row)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failAddOrg {
			bad(w, "add-organization denied")
			return
		}
		f.createdOrgs = append(f.createdOrgs, row)
		if name, _ := row["name"].(string); name != "" {
			f.orgs[name] = row
		}
		ok(w, map[string]any{})
	})

	mux.HandleFunc("/v1/iam/update-user", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		id := r.URL.Query().Get("id")
		body, _ := io.ReadAll(r.Body)
		var row map[string]any
		_ = json.Unmarshal(body, &row)
		f.mu.Lock()
		defer f.mu.Unlock()
		if owner, _ := row["owner"].(string); owner != "" {
			f.movedTo[id] = owner
		}
		ok(w, map[string]any{})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeIAM) capture(r *http.Request) {
	f.mu.Lock()
	f.gotAuth = r.Header.Get("Authorization")
	f.mu.Unlock()
}

// mountApp mounts the account surface against the fake IAM at base, with the
// confidential client wired (unless creds are ""). Returns the app.
func mountApp(t *testing.T, base, clientID, clientSecret string) *zip.App {
	t.Helper()
	t.Setenv("IAM_URL", base)
	t.Setenv("IAM_MINT_CLIENT_ID", clientID)
	t.Setenv("IAM_MINT_CLIENT_SECRET", clientSecret)
	return mountBoth(t, "hanzo")
}

// mountBoth mounts BOTH account subsystems (self-service + data bridges) on one app —
// exactly what production registers (account@48 then account-bridge@122), so a test
// exercises the full surface with the shared CSRF key. The caller sets the IAM env
// (IAM_URL / IAM_MINT_CLIENT_*) before calling.
func mountBoth(t *testing.T, brand string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), Brand: brand}
	if err := MountAccount(app, deps); err != nil {
		t.Fatalf("MountAccount: %v", err)
	}
	if err := MountBridge(app, deps); err != nil {
		t.Fatalf("MountBridge: %v", err)
	}
	return app
}

// callH drives a request with arbitrary VALIDATED-identity headers (the gateway sets
// these only from a verified credential). Mirrors `call` but lets a test inject
// X-User-Email / X-User-IsAdmin, which the ported routes read.
func callH(t *testing.T, app *zip.App, method, path string, headers map[string]string, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// call drives a request through the mounted app. When user is non-empty it injects
// a VALIDATED principal (X-User-Id set — the gateway sets this ONLY from a verified
// credential) with org as X-Org-Id. body is an optional JSON string.
func call(t *testing.T, app *zip.App, method, path, user, org, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// ── keys ──────────────────────────────────────────────────────────────────────

func TestKeys_RequireValidatedPrincipal(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// No X-User-Id → no validated principal → 403, and IAM is never touched, even if
	// a forged X-Org-Id is present (the bearer-less data path must not mint a key).
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		code, _ := call(t, app, m, "/v1/iam/keys", "", "victim", "")
		if code != http.StatusForbidden {
			t.Fatalf("%s /keys with forged org but no principal: want 403, got %d", m, code)
		}
	}
	if len(f.mintedFor) != 0 || len(f.revokedFor) != 0 {
		t.Fatalf("IAM privileged op reached on the unauthenticated path: minted=%v revoked=%v", f.mintedFor, f.revokedFor)
	}
}

func TestKeys_MintGetRevoke_ScopedToCaller(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// GET before mint → hasKey:false (authoritative IAM read, not the claim).
	code, body := call(t, app, http.MethodGet, "/v1/iam/keys", "alice", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("get pre-mint: want 200, got %d (%s)", code, body)
	}
	var st keyStatus
	mustJSON(t, body, &st)
	if st.HasKey {
		t.Fatalf("pre-mint hasKey should be false: %s", body)
	}

	// POST → mint; the key is returned ONCE, and IAM was targeted with the DERIVED
	// `<owner>/<name>` id — never a request value.
	code, body = call(t, app, http.MethodPost, "/v1/iam/keys", "alice", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("mint: want 200, got %d (%s)", code, body)
	}
	var minted struct {
		AccessKey string `json:"accessKey"`
	}
	mustJSON(t, body, &minted)
	if minted.AccessKey != "hk-acme-alice-SECRET" {
		t.Fatalf("mint returned wrong key: %q", minted.AccessKey)
	}
	if len(f.mintedFor) != 1 || f.mintedFor[0] != "acme/alice" {
		t.Fatalf("mint should target the derived id acme/alice, got %v", f.mintedFor)
	}

	// The confidential client authenticated as `hanzo-console` (Basic), NOT as the
	// caller — the whole point of the app-on-behalf boundary.
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("hanzo-console:s3cr3t"))
	if f.gotAuth != wantAuth {
		t.Fatalf("IAM auth: want confidential-client Basic, got %q", f.gotAuth)
	}

	// GET after mint → hasKey:true with the public prefix only (no secret material).
	code, body = call(t, app, http.MethodGet, "/v1/iam/keys", "alice", "acme", "")
	mustJSON(t, body, &st)
	if code != http.StatusOK || !st.HasKey || st.KeyPrefix != "hk-acme-ali" {
		t.Fatalf("get post-mint: want hasKey + 11-char prefix, got %d %s", code, body)
	}
	if strings.Contains(string(body), "SECRET") {
		t.Fatalf("GET /keys leaked the secret: %s", body)
	}

	// DELETE → revoke, targeting the same derived id.
	code, _ = call(t, app, http.MethodDelete, "/v1/iam/keys", "alice", "acme", "")
	if code != http.StatusOK || len(f.revokedFor) != 1 || f.revokedFor[0] != "acme/alice" {
		t.Fatalf("revoke: want 200 targeting acme/alice, got %d %v", code, f.revokedFor)
	}
}

// TestKeys_DirectBearerPath_MintsByUsernameNotUUID is the regression guard for the
// cloud-direct hk- mint 502. On the in-binary direct-Bearer path SanitizeIdentity
// stamps X-User-Id = the JWT subject (a UUID) and, distinctly, X-User-Name = the IAM
// username. The user-key ops must target <owner>/<username> ("hanzo/z"), NOT
// <owner>/<uuid> — which failed IAM's GetOwnerAndNameFromId user lookup ("password
// or code is incorrect", surfaced as 502). The gateway path (no X-User-Name;
// X-User-Id == username) must be UNCHANGED (keyID falls back to owner/name).
func TestKeys_DirectBearerPath_MintsByUsernameNotUUID(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	const uuid = "2d4d67ab-30f1-474e-b81f-f60461852259"
	req := httptest.NewRequest(http.MethodPost, "/v1/iam/keys", nil)
	req.Header.Set("X-User-Id", uuid)  // direct-path stamp: the subject UUID
	req.Header.Set("X-User-Name", "z") // direct-path stamp: the IAM username
	req.Header.Set("X-Org-Id", "hanzo")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("direct-path mint: want 200, got %d (%s)", resp.StatusCode, b)
	}
	if len(f.mintedFor) != 1 || f.mintedFor[0] != "hanzo/z" {
		t.Fatalf("direct-path mint must target hanzo/z (username), never hanzo/<uuid>: got %v", f.mintedFor)
	}
	var minted struct {
		AccessKey string `json:"accessKey"`
	}
	mustJSON(t, b, &minted)
	if minted.AccessKey != "hk-hanzo-z-SECRET" {
		t.Fatalf("direct-path mint returned wrong key: %q", minted.AccessKey)
	}
}

func TestKeys_NotConfigured_501(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "", "") // confidential client unwired
	code, body := call(t, app, http.MethodPost, "/v1/iam/keys", "alice", "acme", "")
	if code != http.StatusNotImplemented {
		t.Fatalf("unconfigured mint: want 501, got %d (%s)", code, body)
	}
}

func TestKeys_MintUpstreamFailure_502(t *testing.T) {
	f := newFakeIAM()
	f.failMintKey = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	code, body := call(t, app, http.MethodPost, "/v1/iam/keys", "alice", "acme", "")
	if code != http.StatusBadGateway {
		t.Fatalf("mint upstream failure: want 502, got %d (%s)", code, body)
	}
}

// ── onboard ─────────────────────────────────────────────────────────────────

func TestOnboard_FirstRun_CreatesAndMoves(t *testing.T) {
	f := newFakeIAM()
	// The zero-org user (empty X-Org-Id ⇒ bare id "dave") has a real user row so the
	// move can re-submit it.
	f.user["dave"] = map[string]any{"owner": "", "name": "dave", "type": "normal-user"}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// First-run: the caller has NO org (empty X-Org-Id) but IS validated. onboard
	// must allow it (requireOwner=false), create the org, and MOVE the user in.
	code, body := call(t, app, http.MethodPost, "/v1/iam/onboard", "dave", "", `{"name":"Acme Rockets"}`)
	if code != http.StatusOK {
		t.Fatalf("first-run onboard: want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "acme-rockets" || resp.Additional {
		t.Fatalf("onboard result wrong: %+v", resp)
	}
	if len(f.createdOrgs) != 1 {
		t.Fatalf("want 1 org created, got %d", len(f.createdOrgs))
	}
	if owner, _ := f.createdOrgs[0]["owner"].(string); owner != "admin" {
		t.Fatalf("created org must be owned by admin, got %q", owner)
	}
	if f.movedTo["dave"] != "acme-rockets" {
		t.Fatalf("first-run must move the user into the new org, movedTo=%v", f.movedTo)
	}
}

func TestOnboard_Additional_CreatesWithoutMoving(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// The caller ALREADY has an org. onboard must create the new org but NOT move
	// them (a move would strip their owner + orphan their current org).
	code, body := call(t, app, http.MethodPost, "/v1/iam/onboard", "alice", "acme", `{"name":"Side Project"}`)
	if code != http.StatusOK {
		t.Fatalf("additional onboard: want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "side-project" || !resp.Additional {
		t.Fatalf("additional onboard result wrong: %+v", resp)
	}
	if len(f.movedTo) != 0 {
		t.Fatalf("additional onboard must NOT move the user, movedTo=%v", f.movedTo)
	}
}

func TestOnboard_ReservedAndTaken(t *testing.T) {
	f := newFakeIAM()
	f.orgs["taken"] = map[string]any{"owner": "admin", "name": "taken"}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// A reserved brand/system name is a 400 (policy), before any IAM create.
	code, _ := call(t, app, http.MethodPost, "/v1/iam/onboard", "alice", "acme", `{"name":"Hanzo"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("reserved name: want 400, got %d", code)
	}
	// An explicit name that's taken is an honest 409.
	code, _ = call(t, app, http.MethodPost, "/v1/iam/onboard", "alice", "acme", `{"name":"Taken"}`)
	if code != http.StatusConflict {
		t.Fatalf("taken name: want 409, got %d", code)
	}
	if len(f.createdOrgs) != 0 {
		t.Fatalf("no org should be created for reserved/taken names, got %d", len(f.createdOrgs))
	}
}

func TestOnboard_Personal_AutoSuffixesOnCollision(t *testing.T) {
	f := newFakeIAM()
	f.user["dave"] = map[string]any{"owner": "", "name": "dave"}
	f.orgs["dave"] = map[string]any{"owner": "admin", "name": "dave"} // base personal slug already taken
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// personal:true (zero-org user) with the base slug taken → auto-suffix to dave-2,
	// first-run move.
	code, body := call(t, app, http.MethodPost, "/v1/iam/onboard", "dave", "", `{"personal":true}`)
	if code != http.StatusOK {
		t.Fatalf("personal onboard: want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "dave-2" {
		t.Fatalf("personal collision should auto-suffix to dave-2, got %q", resp.Org)
	}
}

func TestOnboard_PersonalWhenAlreadyOrged_409(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	// A user WITH an org asking for a personal org is meaningless → 409.
	code, _ := call(t, app, http.MethodPost, "/v1/iam/onboard", "alice", "acme", `{"personal":true}`)
	if code != http.StatusConflict {
		t.Fatalf("personal-while-orged: want 409, got %d", code)
	}
}

func TestOnboard_Unauthenticated_403(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	code, _ := call(t, app, http.MethodPost, "/v1/iam/onboard", "", "", `{"name":"x"}`)
	if code != http.StatusForbidden {
		t.Fatalf("unauth onboard: want 403, got %d", code)
	}
}

// ── route ordering: the native /v1/iam surface beats clients/iam's wildcard ───

// TestIAMKeysBeatsWildcard proves the ACTUAL route-match precedence: with the account
// self-service routes mounted FIRST (order 48) and clients/iam's /v1/iam/* WILDCARD
// mounted AFTER (order 50) — the exact production mount order — a request to /v1/iam/keys
// reaches the NATIVE handler, not the wildcard. A path the native surface does NOT own
// still falls through to the wildcard, proving it is really mounted and only the specific
// route shadows it.
func TestIAMKeysBeatsWildcard(t *testing.T) {
	f := newFakeIAM()
	t.Setenv("IAM_URL", f.server(t).URL)
	t.Setenv("IAM_MINT_CLIENT_ID", "hanzo-console")
	t.Setenv("IAM_MINT_CLIENT_SECRET", "s3cr3t")

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo"}
	// account (order 48) mounts its SPECIFIC /v1/iam/keys + /v1/iam/onboard FIRST.
	if err := MountAccount(app, deps); err != nil {
		t.Fatalf("MountAccount: %v", err)
	}
	// clients/iam (order 50) mounts its /v1/iam/* WILDCARD AFTER — the exact prod order.
	const sentinel = 599
	app.All("/v1/iam/*", func(c *zip.Ctx) error {
		return c.JSON(sentinel, map[string]string{"handler": "iam-wildcard"})
	})

	// GET /v1/iam/keys must hit the NATIVE handler (keyStatus 200), never the wildcard.
	code, body := call(t, app, http.MethodGet, "/v1/iam/keys", "alice", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("/v1/iam/keys must hit the native handler (200), got %d (%s) — wildcard shadowed it", code, body)
	}
	if strings.Contains(string(body), "iam-wildcard") {
		t.Fatalf("/v1/iam/keys reached the wildcard, not the native handler: %s", body)
	}
	var st keyStatus
	mustJSON(t, body, &st) // native response shape

	// POST /v1/iam/keys (mint) must ALSO hit the native handler and target the derived id.
	code, body = call(t, app, http.MethodPost, "/v1/iam/keys", "alice", "acme", "")
	if code != http.StatusOK || strings.Contains(string(body), "iam-wildcard") {
		t.Fatalf("POST /v1/iam/keys must mint via the native handler, got %d (%s)", code, body)
	}
	if len(f.mintedFor) != 1 || f.mintedFor[0] != "acme/alice" {
		t.Fatalf("native mint must target acme/alice, got %v", f.mintedFor)
	}

	// /v1/iam/onboard is likewise native (not the wildcard).
	code, _ = call(t, app, http.MethodPost, "/v1/iam/onboard", "dave", "", `{"name":"Acme Rockets"}`)
	if code == sentinel {
		t.Fatalf("/v1/iam/onboard reached the wildcard (%d) — the native handler must win", sentinel)
	}

	// A path the native surface does NOT own falls through to the wildcard (proof it IS
	// mounted and only the specific /v1/iam/keys + /v1/iam/onboard routes shadow it).
	code, _ = call(t, app, http.MethodGet, "/v1/iam/oauth/token", "alice", "acme", "")
	if code != sentinel {
		t.Fatalf("/v1/iam/oauth/token must reach the /v1/iam/* wildcard (%d), got %d", sentinel, code)
	}
}

func mustJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %T: %v (%s)", v, err, body)
	}
}
