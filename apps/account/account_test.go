package account

import (
	"cmp"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
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

	// keys is every key row, as IAM stores them, by "<owner>/<name>".
	keys map[string]*fakeKey
	// made orders the rows by creation, the order IAM lists them in (newest first).
	made int
	orgs map[string]map[string]any // slug → org row (nil map = absent)
	user map[string]map[string]any // id → full user row

	// captured
	gotAuth   string   // Authorization header on the last request
	mintedFor []string // the holder each key create named
	revoked   []string // "<owner>/<name>" of each key revoked
	edited    []string // "<owner>/<name>" of each key updated
	// rows is every row update-user was asked to write, whole.
	rows        []map[string]any
	createdOrgs []map[string]any
	failAddOrg  bool // when true, add-organization answers status!=ok
	failMintKey bool
	// failUpdateUser models an IAM that accepts the read but refuses the write —
	// the state where a profile photo's bytes have landed and the record pointing
	// at them has not.
	failUpdateUser bool
	// ignoreKeyType models an IAM that does not honour the publish scope: it mints
	// the secret key it always did.
	ignoreKeyType bool
	// failKeys makes every key read and write answer 500.
	failKeys bool
	// refuseKeys, when set, makes every key read and write answer that status with
	// IAM's own words, as IAM's typed refusals and its Guard answer.
	refuseKeys int
	// refuseKeyWrites limits refuseKeys to writes: the org's keys read, the
	// create, edit or revoke is refused.
	refuseKeyWrites bool
	// members is "<home>/<name>|<org>" for each membership.
	members map[string]bool

	// granted is every membership write, as {user, org, role}; roles holds the
	// standing role per "<user>|<org>". trail is the order of the membership and
	// org-removal calls, which is what a rollback's safety depends on.
	granted     []map[string]string
	roles       map[string]string
	deletedOrgs []string
	trail       []string
	// failMembership models IAM refusing the membership write (a 400, nothing
	// written). lostMembership models a write that lands while its answer does not
	// (a 502 from a hop in front of IAM). failRevoke refuses the undo.
	failMembership bool
	lostMembership bool
	failRevoke     bool
}

// fakeKey is one key row as IAM stores it, with the credential its holder
// presents beside it so a test can ask whether that credential still works.
type fakeKey struct {
	row    map[string]any
	secret string
}

func newFakeIAM() *fakeIAM {
	return &fakeIAM{
		keys:    map[string]*fakeKey{},
		members: map[string]bool{},
		orgs:    map[string]map[string]any{},
		user:    map[string]map[string]any{},
		roles:   map[string]string{},
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

	// One record, answered the way IAM's typed reads answer: the resource itself,
	// and a 404 when it is not there.
	resource := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	missing := func(w http.ResponseWriter, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": http.StatusNotFound, "error": msg})
	}

	// GET /v1/iam/users/{owner}/{name} — the only address IAM reads a user at. The
	// natural key is the path; the retired /v1/iam/users/get is not served, so a
	// client that regresses to it gets the 404 the running service gives.
	mux.HandleFunc("GET /v1/iam/users/{owner}/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		owner, name := r.PathValue("owner"), r.PathValue("name")
		// A confidential client may not read an account in the reserved admin org:
		// IAM's policy keeps every reserved-owner user row SuperAdmin-only.
		if owner == adminOrg {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":403,"error":"forbidden"}`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		out := map[string]any{"owner": owner, "name": name, "updatedTime": "2026-01-02T03:04:05Z"}
		// A configured user row wins (used by the onboarding move).
		maps.Copy(out, f.user[owner+"/"+name])
		resource(w, out)
	})

	f.serveKeys(mux, ok, bad, resource, missing)

	// GET /v1/iam/organizations/{owner}/{name}: the org itself, or a 404, which is
	// the one answer getOrganization reads as "the name is free".
	mux.HandleFunc("GET /v1/iam/organizations/{owner}/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		if row, present := f.orgs[r.PathValue("name")]; present && row != nil && r.PathValue("owner") == adminOrg {
			resource(w, row)
			return
		}
		missing(w, "organization not found")
	})

	mux.HandleFunc("/v1/iam/organizations", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		body, _ := io.ReadAll(r.Body)
		var row map[string]any
		_ = json.Unmarshal(body, &row)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failAddOrg {
			bad(w, "create organization denied")
			return
		}
		f.createdOrgs = append(f.createdOrgs, row)
		if name, _ := row["name"].(string); name != "" {
			f.orgs[name] = row
		}
		ok(w, map[string]any{})
	})

	// DELETE /v1/iam/organizations/{owner}/{name}: a typed op, so the answer is the
	// resource itself, and 404 when the row is not there.
	mux.HandleFunc("DELETE /v1/iam/organizations/{owner}/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		name := r.PathValue("name")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.trail = append(f.trail, "delete-org "+name)
		if _, present := f.orgs[name]; !present || r.PathValue("owner") != adminOrg {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"error":"organization not found"}`))
			return
		}
		delete(f.orgs, name)
		f.deletedOrgs = append(f.deletedOrgs, name)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"affected":true}`))
	})

	// The membership relation, as IAM serves it: POST ensures one (a repeat is a
	// no-op that never lowers a role), and the verb-shaped delete revokes one. Both
	// refuse with the error envelope under a 400.
	mux.HandleFunc("POST /v1/iam/memberships", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.trail = append(f.trail, "ensure "+in["user"]+"|"+in["org"])
		if in["user"] == "" || in["org"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			bad(w, "user and org are required")
			return
		}
		if f.failMembership {
			w.WriteHeader(http.StatusBadRequest)
			bad(w, "auth:Unauthorized operation")
			return
		}
		if strings.HasPrefix(in["user"], adminOrg+"/") {
			w.WriteHeader(http.StatusBadRequest)
			bad(w, "only a SuperAdmin may change an account in the admin organization")
			return
		}
		f.granted = append(f.granted, in)
		pair := in["user"] + "|" + in["org"]
		added := !f.members[pair]
		if added {
			f.members[pair] = true
			f.roles[pair] = in["role"]
		}
		if f.lostMembership {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream timed out"))
			return
		}
		ok(w, added)
	})
	mux.HandleFunc("POST /v1/iam/delete-membership", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		pair := in["user"] + "|" + in["org"]
		f.trail = append(f.trail, "revoke "+pair)
		if strings.HasPrefix(in["user"], adminOrg+"/") {
			w.WriteHeader(http.StatusBadRequest)
			bad(w, "only a SuperAdmin may change an account in the admin organization")
			return
		}
		if f.failRevoke {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream timed out"))
			return
		}
		// IAM never removes an organization's last owner.
		if f.roles[pair] == "owner" {
			owners := 0
			for p, role := range f.roles {
				if role == "owner" && strings.HasSuffix(p, "|"+in["org"]) {
					owners++
				}
			}
			if owners <= 1 {
				w.WriteHeader(http.StatusBadRequest)
				bad(w, "this is the organization's last owner; name another owner first")
				return
			}
		}
		removed := f.members[pair]
		delete(f.members, pair)
		delete(f.roles, pair)
		ok(w, removed)
	})

	// PUT /v1/iam/users/{owner}/{name}: the row travels NESTED under `user` and is
	// written at the address in the URL, so a client that sends the bare row, or
	// names a different row in the body than in the URL, is refused here.
	mux.HandleFunc("PUT /v1/iam/users/{owner}/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		body, _ := io.ReadAll(r.Body)
		var in struct {
			User map[string]any `json:"user"`
		}
		_ = json.Unmarshal(body, &in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if in.User == nil || in.User["owner"] != r.PathValue("owner") || in.User["name"] != r.PathValue("name") {
			w.WriteHeader(http.StatusBadRequest)
			bad(w, "the row must be written at its own address")
			return
		}
		if f.failUpdateUser {
			w.WriteHeader(http.StatusBadRequest)
			bad(w, "update refused")
			return
		}
		f.rows = append(f.rows, in.User)
		resource(w, in.User)
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
	return mount(t, "hanzo")
}

// compose installs what a host installs. A subsystem never installs cloud.Bridge:
// the program's composer owns it — serve.go at the root of the fused host, the
// plugin constructor for a plugin program. In a test the test is the composer, so
// it owes the same install; skipping it drives a program where every org-scoped op
// answers 403 for a reason production callers never see.
func compose(app *zip.App) {
	app.Use(zip.H(standIn))
	app.Use(cloud.Bridge())
}

// standIn stands in for the identity boundary (cloud.SanitizeIdentity): it attests
// the person the injected identity headers name, holding an access token, as the
// boundary does for a verified one. A caller with no home org holds nothing, as a
// token with no `orgs` does. The real boundary, over real IAM tokens, is driven in
// onboard_iam_test.go.
func standIn(c *zip.Ctx) error {
	user := c.Header("X-User-Id")
	if user == "" {
		principal.Mint(c, principal.Principal{})
		return c.Continue()
	}
	p := principal.Principal{Org: c.Header("X-Org-Id"), User: user, Subject: user, Kind: principal.Person}
	if home := cmp.Or(c.Header("X-User-Owner"), c.Header("X-Org-Id")); home != "" {
		p.Holder = home + "/" + cmp.Or(c.Header("X-User-Name"), user)
	}
	principal.Mint(c, p)
	return c.Continue()
}

// mount mounts the account subsystem on a bare app — exactly what production
// registers (account@48). The caller sets the IAM env (IAM_URL / IAM_MINT_CLIENT_*)
// before calling.
func mount(t *testing.T, brand string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_BRAND", brand)
	deps := cloud.Deps{}
	if err := Use(app, deps); err != nil {
		t.Fatalf("Use: %v", err)
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// ── onboard ─────────────────────────────────────────────────────────────────

// An org is created beside its creator and never by moving them. Whether they
// act from the signup org or a team they belong to, they keep the account they
// signed in with — no user row is written — and own the new org by membership,
// holding its default key.
func TestOnboard_CreatesAnOrgItsCreatorOwns(t *testing.T) {
	for _, tc := range []struct{ who, user, org, holder string }{
		{"a signup in the brand org", "dave", "hanzo", "hanzo/dave"},
		{"a member of a team", "alice", "acme", "acme/alice"},
	} {
		t.Run(tc.who, func(t *testing.T) {
			f := newFakeIAM()
			f.user[tc.holder] = map[string]any{"owner": tc.org, "name": tc.user, "type": "normal-user"}
			app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

			code, body := call(t, app, http.MethodPost, "/v1/account/orgs", tc.user, tc.org, `{"name":"Acme Rockets"}`)
			if code != http.StatusOK {
				t.Fatalf("want 200, got %d (%s)", code, body)
			}
			var resp onboardResp
			mustJSON(t, body, &resp)
			if resp.Org != "acme-rockets" {
				t.Fatalf("onboard result wrong: %+v", resp)
			}
			if len(f.createdOrgs) != 1 {
				t.Fatalf("want 1 org created, got %d", len(f.createdOrgs))
			}
			if owner, _ := f.createdOrgs[0]["owner"].(string); owner != "admin" {
				t.Fatalf("created org must be owned by admin, got %q", owner)
			}
			if role := f.roles[tc.holder+"|acme-rockets"]; role != "owner" {
				t.Fatalf("%s holds %q in acme-rockets, want owner (roles %v)", tc.holder, role, f.roles)
			}
			if len(f.rows) != 0 {
				t.Fatalf("creating an org wrote a user row, which is how a creator was moved: %v", f.rows)
			}
			k := f.keys["acme-rockets/default"]
			if k == nil || k.row["user"] != tc.holder || k.row["displayName"] != "Default" {
				t.Fatalf("default key = %v, want acme-rockets/default \"Default\" held by %s", k, tc.holder)
			}
			if resp.AccessKey != k.row["accessKey"] || resp.AccessSecret == "" || !f.works(resp.AccessSecret) {
				t.Fatalf("the response must carry the minted key once: %+v", resp)
			}
		})
	}
}

// A key IAM will not mint leaves the org standing, owned, and the response without
// a credential: an error here would send the creator back to a name that is theirs.
func TestOnboard_AKeyThatDoesNotMintLeavesTheOrg(t *testing.T) {
	f := newFakeIAM()
	f.failMintKey = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "dave", "hanzo", `{"name":"Acme Rockets"}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "acme-rockets" || resp.AccessKey != "" || resp.AccessSecret != "" {
		t.Fatalf("resp = %+v, want the org and no credential", resp)
	}
	if f.roles["hanzo/dave|acme-rockets"] != "owner" {
		t.Fatalf("the creator does not own the org: %v", f.roles)
	}
}

func TestOnboard_ReservedAndTaken(t *testing.T) {
	f := newFakeIAM()
	f.orgs["taken"] = map[string]any{"owner": "admin", "name": "taken"}
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// A reserved brand/system name is a 400 (policy), before any IAM create.
	code, _ := call(t, app, http.MethodPost, "/v1/account/orgs", "alice", "acme", `{"name":"Hanzo"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("reserved name: want 400, got %d", code)
	}
	// An explicit name that's taken is an honest 409.
	code, _ = call(t, app, http.MethodPost, "/v1/account/orgs", "alice", "acme", `{"name":"Taken"}`)
	if code != http.StatusConflict {
		t.Fatalf("taken name: want 409, got %d", code)
	}
	if len(f.createdOrgs) != 0 {
		t.Fatalf("no org should be created for reserved/taken names, got %d", len(f.createdOrgs))
	}
}

func TestOnboard_Personal_AutoSuffixesOnCollision(t *testing.T) {
	f := newFakeIAM()
	f.user["hanzo/dave"] = map[string]any{"owner": "hanzo", "name": "dave"}
	f.orgs["dave"] = map[string]any{"owner": "admin", "name": "dave"} // base personal slug already taken
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// personal:true with the base slug taken auto-suffixes to dave-2.
	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "dave", "hanzo", `{"personal":true}`)
	if code != http.StatusOK {
		t.Fatalf("personal onboard: want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "dave-2" {
		t.Fatalf("personal collision should auto-suffix to dave-2, got %q", resp.Org)
	}
}

// A personal org is the caller's own space from wherever they act: a team member
// gets one beside the team, owned by them, and stays in the team.
func TestOnboard_PersonalBesideATeam(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	code, body := call(t, app, http.MethodPost, "/v1/account/orgs", "alice", "acme", `{"personal":true}`)
	if code != http.StatusOK {
		t.Fatalf("personal beside a team: want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "alice" || len(f.createdOrgs) != 1 || f.createdOrgs[0]["isPersonal"] != true {
		t.Fatalf("resp = %+v, orgs = %v, want the personal org alice", resp, f.createdOrgs)
	}
	if f.roles["acme/alice|alice"] != "owner" || len(f.rows) != 0 {
		t.Fatalf("roles = %v, rows = %v: want alice to own it and stay in acme", f.roles, f.rows)
	}
}

func TestOnboard_Unauthenticated_403(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	code, _ := call(t, app, http.MethodPost, "/v1/account/orgs", "", "", `{"name":"x"}`)
	if code != http.StatusForbidden {
		t.Fatalf("unauth onboard: want 403, got %d", code)
	}
}

// ── ownership: account claims nothing under /v1/iam ──────────────────────────

// TestAccountClaimsNothingUnderIAM replaces TestIAMKeysBeatsWildcard, which proved
// the opposite fact and proved it about a topology that is gone.
//
// account used to register /v1/iam/keys (three methods) and /v1/iam/onboard inside
// IAM's own prefix, in front of the /v1/iam/* wildcard iam was relayed behind, and
// that test pinned fiber's most-specific-wins as the thing keeping the two apart.
// It was already a fiction in production: api.hanzo.ai routes /v1/iam/* to IAM, so
// none of those four registrations ever answered there — measurably, that address
// returns IAM's own {"status":401,"error":"authentication required"} from
// server: zip, with no Deprecation header and no x-api-version.
//
// iam is COMPOSED now, so both would be EXACT routes at one address and the winner
// would be registration order rather than specificity — silent, and zip refuses a
// duplicate at build instead. The keys aliases are deleted and onboard is
// POST /v1/account/orgs, both named for the resource under the capability that
// serves them — the same rule that moved the key surface off /v1/iam/keys in the
// first place. This test is the ratchet on that: nothing account registers may sit
// under a prefix another app owns.
func TestAccountClaimsNothingUnderIAM(t *testing.T) {
	f := newFakeIAM()
	t.Setenv("IAM_URL", f.server(t).URL)
	t.Setenv("IAM_MINT_CLIENT_ID", "hanzo-console")
	t.Setenv("IAM_MINT_CLIENT_SECRET", "s3cr3t")
	t.Setenv("IAM_SERVICE_TOKEN", "svc")

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_BRAND", "hanzo")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	for _, r := range app.Fiber().GetRoutes(true) {
		if strings.HasPrefix(r.Path, "/v1/iam") || strings.HasPrefix(r.Path, "/login/oauth") {
			t.Errorf("account registers %s %s, inside a prefix iam owns — zip refuses "+
				"the duplicate at build and the whole subsystem fail-closes to 503",
				r.Method, r.Path)
		}
	}

	// The two operations that moved are alive at their own addresses.
	if code, body := call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", ""); code != http.StatusOK {
		t.Fatalf("GET /v1/account/keys: want 200, got %d (%s)", code, body)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/account/orgs", "dave", "hanzo", `{"name":"Acme Rockets"}`); code != http.StatusOK {
		t.Fatalf("POST /v1/account/orgs: want 200, got %d", code)
	}
}

func mustJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %T: %v (%s)", v, err, body)
	}
}
