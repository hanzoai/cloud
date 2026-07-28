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

	// state — key rows, as IAM stores them: (id, type) → the presented credential.
	// Two rows per user at most (one secret, one publishable), exactly like IAM's
	// (Owner, NameFor(scope)) identity.
	keys map[keyRef]string
	orgs map[string]map[string]any // slug → org row (nil map = absent)
	user map[string]map[string]any // id → full user row (for the move)

	// captured
	gotAuth     string   // Authorization header on the last request
	mintedFor   []string // ids mint-user-keys was called with
	mintedTypes []string // the `type` field each mint carried
	revokedFor  []string
	revokedType []string
	movedTo     map[string]string // id → new owner (from update-user)
	createdOrgs []map[string]any
	failAddOrg  bool // when true, add-organization answers status!=ok
	failMintKey bool
}

// keyRef identifies one key row the way IAM does: whose it is, and which class.
type keyRef struct{ id, typ string }

func newFakeIAM() *fakeIAM {
	return &fakeIAM{
		keys:    map[keyRef]string{},
		orgs:    map[string]map[string]any{},
		user:    map[string]map[string]any{},
		movedTo: map[string]string{},
	}
}

// keyType normalizes the mint/revoke `type` field the way IAM does: absent means
// secret. A fake that defaulted differently would hide the very bug under test.
func fakeKeyType(r *http.Request) string {
	if t := r.URL.Query().Get("type"); t != "" {
		return t
	}
	return "secret"
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
			ok(w, out)
			return
		}
		ok(w, map[string]any{"updatedTime": "2026-01-02T03:04:05Z"})
	})

	// The key LIST — IAM's owner-scoped key rows, MASKED (schema.Key.Mask blanks the
	// confidential half). The rows are what cloud must read: a mint writes a key row,
	// so a read of the USER row reports "no key" right after a successful mint.
	mux.HandleFunc("/v1/iam/keys", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		owner := r.URL.Query().Get("owner")
		f.mu.Lock()
		defer f.mu.Unlock()
		rows := []map[string]any{}
		for ref, key := range f.keys {
			gotOwner, user, _ := strings.Cut(ref.id, "/")
			if gotOwner != owner || key == "" {
				continue
			}
			row := map[string]any{
				"owner": owner, "name": "cloud-api", "user": user,
				"accessKey": key, "updatedTime": "2026-01-02T03:04:05Z",
			}
			if ref.typ == "publishable" {
				row["name"], row["scope"] = "publishable", "publish"
			}
			rows = append(rows, row)
		}
		ok(w, map[string]any{"keys": rows})
	})

	mux.HandleFunc("/v1/iam/mint-user-keys", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		id, typ := r.URL.Query().Get("id"), fakeKeyType(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mintedFor = append(f.mintedFor, id)
		f.mintedTypes = append(f.mintedTypes, typ)
		if f.failMintKey {
			bad(w, "mint failed")
			return
		}
		// The prefix IS the type: a publishable key is a pk-, a secret one an sk-.
		// Nothing downstream may have to ask which it got.
		key := "sk-" + strings.ReplaceAll(id, "/", "-") + "-SECRET"
		if typ == "publishable" {
			key = "pk-" + strings.ReplaceAll(id, "/", "-") + "-PUBLIC"
		}
		f.keys[keyRef{id, typ}] = key
		ok(w, map[string]any{"accessKey": key})
	})

	mux.HandleFunc("/v1/iam/revoke-user-keys", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		id, typ := r.URL.Query().Get("id"), fakeKeyType(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.revokedFor = append(f.revokedFor, id)
		f.revokedType = append(f.revokedType, typ)
		delete(f.keys, keyRef{id, typ})
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
		for _, path := range []string{"/v1/keys", "/v1/iam/keys"} {
			code, _ := call(t, app, m, path, "", "victim", "")
			if code != http.StatusForbidden {
				t.Fatalf("%s %s with forged org but no principal: want 403, got %d", m, path, code)
			}
		}
	}
	if len(f.mintedFor) != 0 || len(f.revokedFor) != 0 {
		t.Fatalf("IAM privileged op reached on the unauthenticated path: minted=%v revoked=%v", f.mintedFor, f.revokedFor)
	}
}

func TestKeys_MintGetRevoke_ScopedToCaller(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// GET before mint → an empty set (authoritative IAM read, not the claim).
	code, body := call(t, app, http.MethodGet, "/v1/keys", "alice", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("get pre-mint: want 200, got %d (%s)", code, body)
	}
	var st keyList
	mustJSON(t, body, &st)
	if len(st.Keys) != 0 {
		t.Fatalf("pre-mint key set should be empty: %s", body)
	}

	// POST → mint; the key is returned ONCE, and IAM was targeted with the DERIVED
	// `<owner>/<name>` id — never a request value.
	code, body = call(t, app, http.MethodPost, "/v1/keys", "alice", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("mint: want 200, got %d (%s)", code, body)
	}
	var minted struct {
		Type      string `json:"type"`
		Key       string `json:"key"`
		AccessKey string `json:"accessKey"`
	}
	mustJSON(t, body, &minted)
	if minted.Key != "sk-acme-alice-SECRET" || minted.AccessKey != minted.Key {
		t.Fatalf("mint returned wrong key: %+v", minted)
	}
	if minted.Type != "secret" {
		t.Fatalf("an unqualified mint must be a SECRET key, got %q", minted.Type)
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

	// GET after mint → the key is LISTED, by prefix only, with no secret material.
	// This is the round trip that was broken: the mint writes a key ROW and the read
	// looked at the USER row, so a freshly minted key never appeared.
	code, body = call(t, app, http.MethodGet, "/v1/keys", "alice", "acme", "")
	mustJSON(t, body, &st)
	if code != http.StatusOK || len(st.Keys) != 1 {
		t.Fatalf("get post-mint: want the minted key listed, got %d %s", code, body)
	}
	if st.Keys[0].Type != "secret" || st.Keys[0].Prefix != "sk-acme-ali" {
		t.Fatalf("get post-mint: want a secret key by prefix, got %+v", st.Keys[0])
	}
	if strings.Contains(string(body), "SECRET") {
		t.Fatalf("GET /v1/keys leaked the secret: %s", body)
	}

	// DELETE → revoke, targeting the same derived id, and the key stops being listed.
	code, _ = call(t, app, http.MethodDelete, "/v1/keys", "alice", "acme", "")
	if code != http.StatusOK || len(f.revokedFor) != 1 || f.revokedFor[0] != "acme/alice" {
		t.Fatalf("revoke: want 200 targeting acme/alice, got %d %v", code, f.revokedFor)
	}
	_, body = call(t, app, http.MethodGet, "/v1/keys", "alice", "acme", "")
	mustJSON(t, body, &st)
	if len(st.Keys) != 0 {
		t.Fatalf("a revoked key is still listed: %s", body)
	}
}

// The pk- fix at the door a caller actually uses: a publishable key is `type:
// publishable` on the ONE endpoint. Nothing minted one before — cloud had no pk-
// mint surface at all — so every product configured its own ingest credential and
// error reporting stayed on a separate DSN.
func TestKeys_PublishableTypeIsAFieldNotAnEndpoint(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := call(t, app, http.MethodPost, "/v1/keys", "alice", "acme", `{"type":"publishable"}`)
	if code != http.StatusOK {
		t.Fatalf("publishable mint: want 200, got %d (%s)", code, body)
	}
	var minted struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	mustJSON(t, body, &minted)
	if minted.Type != "publishable" || !strings.HasPrefix(minted.Key, "pk-") {
		t.Fatalf("want a pk- publishable key, got %+v", minted)
	}
	if len(f.mintedTypes) != 1 || f.mintedTypes[0] != "publishable" {
		t.Fatalf("the type must reach IAM as a field, got %v", f.mintedTypes)
	}

	// The type also rides as a query field — one contract, either spelling.
	if code, _ = call(t, app, http.MethodPost, "/v1/keys?type=publishable", "bob", "acme", ""); code != http.StatusOK {
		t.Fatalf("?type=publishable: want 200, got %d", code)
	}

	// A publishable key is LISTED WITH ITS FULL VALUE — it is public by construction
	// and useless to its holder if it cannot be read back.
	_, body = call(t, app, http.MethodGet, "/v1/keys", "alice", "acme", "")
	var st keyList
	mustJSON(t, body, &st)
	if len(st.Keys) != 1 || st.Keys[0].Type != "publishable" {
		t.Fatalf("want one publishable key listed, got %s", body)
	}
	if st.Keys[0].Key != "pk-acme-alice-PUBLIC" {
		t.Fatalf("a publishable key must list its full value, got %q", st.Keys[0].Key)
	}
}

// The two types are independent credentials: minting or revoking one must not touch
// the other. Rotating the key in a browser bundle cannot be allowed to sign the
// holder out of their own API.
func TestKeys_TypesAreIndependent(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	call(t, app, http.MethodPost, "/v1/keys", "alice", "acme", `{"type":"secret"}`)
	call(t, app, http.MethodPost, "/v1/keys", "alice", "acme", `{"type":"publishable"}`)

	var st keyList
	_, body := call(t, app, http.MethodGet, "/v1/keys", "alice", "acme", "")
	mustJSON(t, body, &st)
	if len(st.Keys) != 2 {
		t.Fatalf("a user holds one key per type; want 2 listed, got %s", body)
	}

	// Revoke ONLY the publishable one.
	if code, _ := call(t, app, http.MethodDelete, "/v1/keys?type=publishable", "alice", "acme", ""); code != http.StatusOK {
		t.Fatalf("scoped revoke: want 200, got %d", code)
	}
	if len(f.revokedType) != 1 || f.revokedType[0] != "publishable" {
		t.Fatalf("the revoke type must reach IAM, got %v", f.revokedType)
	}
	_, body = call(t, app, http.MethodGet, "/v1/keys", "alice", "acme", "")
	mustJSON(t, body, &st)
	if len(st.Keys) != 1 || st.Keys[0].Type != "secret" {
		t.Fatalf("revoking the publishable key must leave the secret key working, got %s", body)
	}
}

// An unrecognized type is REFUSED, never defaulted. Defaulting would hand a caller
// who asked for a browser-safe key a session-equivalent secret instead — the failure
// mode is a credential in the wrong place, so it has to be loud.
func TestKeys_UnknownTypeRefused(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	for _, typ := range []string{"public", "publishible", "sk", "SECRET"} {
		code, _ := call(t, app, http.MethodPost, "/v1/keys?type="+typ, "alice", "acme", "")
		if code != http.StatusBadRequest {
			t.Fatalf("POST type=%q: want 400, got %d", typ, code)
		}
		code, _ = call(t, app, http.MethodDelete, "/v1/keys?type="+typ, "alice", "acme", "")
		if code != http.StatusBadRequest {
			t.Fatalf("DELETE type=%q: want 400, got %d", typ, code)
		}
	}
	if len(f.mintedFor) != 0 || len(f.revokedFor) != 0 {
		t.Fatalf("a refused type must never reach IAM: minted=%v revoked=%v", f.mintedFor, f.revokedFor)
	}
}

// /v1/iam/keys is an ALIAS, not a second implementation: it answers identically and
// says on the wire that it is superseded (RFC 8594), naming /v1/keys.
func TestKeys_LegacyPathIsAThinDeprecatedAlias(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	req := httptest.NewRequest(http.MethodGet, "/v1/iam/keys", nil)
	req.Header.Set("X-User-Id", "alice")
	req.Header.Set("X-Org-Id", "acme")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("alias GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alias GET: want 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Deprecation") != "true" {
		t.Fatal("the superseded path must announce itself deprecated")
	}
	if !strings.Contains(resp.Header.Get("Link"), "/v1/keys") {
		t.Fatalf("the deprecation must NAME its replacement, got Link: %q", resp.Header.Get("Link"))
	}

	// And it is the SAME handler — a mint through the alias is a mint, with the type
	// field honored exactly as on the canonical path.
	code, body := call(t, app, http.MethodPost, "/v1/iam/keys?type=publishable", "alice", "acme", "")
	if code != http.StatusOK || !strings.Contains(string(body), "pk-") {
		t.Fatalf("alias POST must behave identically: %d %s", code, body)
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
	if minted.AccessKey != "sk-hanzo-z-SECRET" {
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
	var st keyList
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
