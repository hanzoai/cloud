package kms_test

// Integration tests for the embedded KMS subsystem, exercised through the REAL
// orchestrator path (BuildDeps → the init()-registered App → the zip/Fiber
// stack), mirroring cmd/cloud/main_test.go. Requests run in-process via
// app.Fiber().Test — no listener, no external KMS, no PostgreSQL.
//
// SanitizeIdentity does not run in this harness (it is wired in serve.go, not
// UseAll), so a test simulates a validated principal by setting the same
// identity headers SanitizeIdentity would emit: X-Org-Id and X-User-IsAdmin. In
// production those are stripped from client input and re-issued only for a
// JWT-validated principal, so the org-scope gate is real; here we drive it
// directly. The boundary also MINTS the principal a typed op reads, and its
// holder is what the grant rule keys a value on, so kms.HeaderBoundary mints
// the one these headers describe (holder = X-User-Id) and harnessGrants names
// what each harness holder may open.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"

	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	// clients/kms: its init() registers the in-process KMS client factory
	// (RegisterKMSClientFactory) BuildDeps needs; it also exports Mount (the
	// subsystem spec below) + the *kms.Client type these tests assert on.
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// masterKeyB64 returns a fresh random 32-byte master key, base64-encoded as the
// operator would inject it via CLOUD_KMS_MASTER_KEY_REF.
func masterKeyB64(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// mountSpecs is the kms subsystem's composition-root entry, built locally so these
// tests mount exactly kms (the same spec apps.Wire() carries) without linking
// the whole bundle. cfg.Enable still gates it, exactly as in production.
func mountSpecs() []cloud.Plugin {
	return []cloud.Plugin{{Name: "kms", Use: kms.Use, OwnsHealth: true}}
}

// harnessGrants is every value the header-harness tests touch, granted to the
// holder the harness mints for each org ("u-<org>", do's X-User-Id). These tests
// measure addressing and isolation; who may open a value is measured in
// grant_rule_test.go. An org the grant document cannot name — one that is not
// already a storage label — is reached as SuperAdmin, the only way anyone
// reaches it.
const harnessGrants = `
grants:
- holder: u-hanzo
  org: hanzo
  read: &hanzo
  - {path: /, env: main, keys: [API_KEY, DB_URL]}
  - {path: /, env: prod, keys: [STRIPE_KEY]}
  - {path: /ci/deploy, env: default, keys: [TOKEN]}
  - {path: /iam-passwords, env: prod, keys: [Z_PASSWORD]}
  - {path: /iam-passwords, env: default, keys: [Z_PASSWORD]}
  write: *hanzo
- {holder: u-evil, org: evil, read: [{path: /, env: main, keys: [API_KEY]}]}
- {holder: u-maxpower, org: maxpower, read: [{path: /platform/api, env: default, keys: [DB_PASSWORD]}]}
- {holder: u-acme, org: acme, read: [{path: /platform/api, env: default, keys: [DB_PASSWORD]}]}
- {holder: u-acmecorp, org: acmecorp, read: &k [{path: /, env: default, keys: [K]}], write: *k}
- {holder: u-x, org: x, write: [{path: /, env: default, keys: [S]}]}
- {holder: u-xy, org: xy, write: [{path: /, env: default, keys: [S, VICT]}]}
- {holder: u-x-attacker, org: x-attacker, write: [{path: /, env: default, keys: [S]}]}
- {holder: u-victim, org: victim, write: [{path: /, env: default, keys: [REAL]}]}
- {holder: u-attacker, org: attacker, read: [{path: /, env: default, keys: [REAL, NOPE]}]}
`

// newApp wires BuildDeps + the canonical middleware + UseAll for the kms
// subsystem, exactly like main()'s path. Returns the app and the built deps (so
// tests can reach the in-process KMSClient directly). deps.Audit is this
// process's own chain, as Serve hands it over, so a SuperAdmin act is recorded.
func newApp(t *testing.T, cfg *cloud.Config) (*zip.App, cloud.Deps) {
	t.Helper()
	kms.UseGrants(t, harnessGrants)
	deps := cloud.BuildDeps(cfg)
	chain, err := audit.Open(t.TempDir(), "kms", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	deps.Audit = chain
	app := zip.New(zip.Config{Logger: luxlog.Default()})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(kms.HeaderBoundary)
	if err := cloud.UseAll(app, mountSpecs(), cfg, deps); err != nil {
		t.Fatalf("UseAll: %v", err)
	}
	return app, deps
}

func baseCfg(t *testing.T, masterKey string) *cloud.Config {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	return &cloud.Config{
		Brand:           "hanzo",
		Domain:          "api.hanzo.ai",
		IAMIssuer:       "https://hanzo.id",
		DataDir:         t.TempDir(),
		Enable:          []string{"kms"},
		KMSMasterKeyRef: masterKey,
	}
}

// TestHealthReadyWithMasterKey: with a master key configured, /v1/kms/health is
// 200 and reports ready. This is the real probe, not the generic liveness route.
func TestHealthReadyWithMasterKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))
		resp := do(t, app, "GET", "/v1/kms/health", "", "", false, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("GET /v1/kms/health = %d, want 200", resp.StatusCode)
		}
		body := decode(t, resp.Body)
		if body["ready"] != true {
			t.Errorf("health ready=%v, want true", body["ready"])
		}
		if body["signing"] != false {
			t.Errorf("health signing=%v, want false (no MPC configured)", body["signing"])
		}
	})
}

// TestHealthFailClosedWithoutMasterKey: absent a master key the subsystem still
// mounts, but /v1/kms/health reports 503 (health-only mode) — never a silent
// insecure 200.
func TestHealthFailClosedWithoutMasterKey(t *testing.T) {
	cfg := baseCfg(t, "") // no master key
	app, _ := newApp(t, cfg)
	resp := do(t, app, "GET", "/v1/kms/health", "", "", false, nil)
	if resp.StatusCode != 503 {
		t.Fatalf("GET /v1/kms/health (no key) = %d, want 503", resp.StatusCode)
	}
	body := decode(t, resp.Body)
	if body["ready"] != false {
		t.Errorf("health ready=%v, want false", body["ready"])
	}
	if s, _ := body["error"].(string); !strings.Contains(s, "master key not configured") {
		t.Errorf("health error=%q, want it to name the missing master key", s)
	}
}

// TestKMSClientRoundtrip: a PutSecret→GetSecret roundtrip through the in-process
// cloud.Store (deps.KMS) against a temp CLOUD_DATA_DIR.
func TestKMSClientRoundtrip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		cfg := baseCfg(t, masterKeyB64(t))
		_, deps := newApp(t, cfg)

		kc := deps.KMS
		if kc == nil {
			t.Fatal("deps.KMS is nil; expected in-process client when kms enabled")
		}
		if _, ok := kc.(*kms.Client); !ok {
			t.Fatalf("deps.KMS is %T, want *kms.Client (in-process)", kc)
		}

		ctx := context.Background()
		const ref = "console-pk-hanzo"
		want := []byte("sk-super-secret-value-42")

		if err := kc.PutSecret(ctx, ref, want); err != nil {
			t.Fatalf("PutSecret: %v", err)
		}
		got, err := kc.GetSecret(ctx, ref)
		if err != nil {
			t.Fatalf("GetSecret: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("GetSecret = %q, want %q", got, want)
		}

		// A missing secret is a clean not-found, not a panic or a fabricated value.
		if _, err := kc.GetSecret(ctx, "does-not-exist"); !errors.Is(err, kms.ErrSecretNotFound) || !errors.Is(err, secret.ErrNone) {
			t.Errorf("GetSecret(missing) err = %v, want ErrSecretNotFound and cloud.ErrNoSecret", err)
		}
	})
}

// TestSecretNotStoredInPlaintext: the on-disk bytes must NOT contain the
// plaintext — proof the AES-256-GCM Seal envelope is applied before storage
// (never store secrets in plaintext).
func TestSecretNotStoredInPlaintext(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		t.Setenv("CLOUD_DATA_DIR", dir)
		cfg := &cloud.Config{
			Brand: "hanzo", Domain: "api.hanzo.ai", IAMIssuer: "https://hanzo.id",
			DataDir: dir, Enable: []string{"kms"}, KMSMasterKeyRef: masterKeyB64(t),
		}
		_, deps := newApp(t, cfg)

		marker := []byte("PLAINTEXT-MARKER-should-never-hit-disk")
		if err := deps.KMS.PutSecret(context.Background(), "svc/DB_URL@main", marker); err != nil {
			t.Fatalf("PutSecret: %v", err)
		}
		// Close so the per-org SQLite files flush to disk, then scan every store file
		// under the data dir for the marker (per-org files live at {dir}/orgs/*/kms.db;
		// an org-less facade ref lands in {dir}/orgs/_platform/kms.db).
		if c, ok := deps.KMS.(*kms.Client); ok {
			if err := c.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
		}

		if locus.DSN() != "" {
			// On hanzo-sql there is no file to scan: read every column of every row the
			// store holds, and require that there is one so the scan is not vacuous.
			db, err := locus.Open(namespace.System(), "kms", dir)
			if err != nil {
				t.Fatalf("open the store: %v", err)
			}
			defer func() { _ = db.Close() }()
			rows, err := db.Query(`SELECT path, env, name, scheme, ciphertext, wrapped_dek, key_handle, policy_id FROM kms_secrets`)
			if err != nil {
				t.Fatalf("read the store: %v", err)
			}
			defer func() { _ = rows.Close() }()
			n := 0
			for rows.Next() {
				var path, env, name, scheme, handle, policy string
				var ct, dek []byte
				if err := rows.Scan(&path, &env, &name, &scheme, &ct, &dek, &handle, &policy); err != nil {
					t.Fatalf("scan: %v", err)
				}
				n++
				for _, col := range [][]byte{[]byte(path), []byte(env), []byte(name), []byte(scheme), ct, dek, []byte(handle), []byte(policy)} {
					if bytesContains(col, marker) {
						t.Fatal("plaintext marker found in the store — secret was NOT sealed before storage")
					}
				}
			}
			if n == 0 {
				t.Fatal("the store holds no row, so the scan proved nothing")
			}
			return
		}

		found := false
		root := dir
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			if bytesContains(b, marker) {
				found = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk store: %v", err)
		}
		if found {
			t.Fatal("plaintext marker found on disk — secret was NOT sealed before storage")
		}
	})
}

// TestSignFailsClosedNoMPC: Sign must fail closed with a clear, non-fabricated
// error when no MPC backend is configured. It must NEVER return a signature.
func TestSignFailsClosedNoMPC(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		cfg := baseCfg(t, masterKeyB64(t)) // master key present, but no MPC
		_, deps := newApp(t, cfg)

		sig, err := deps.KMS.Sign(context.Background(), "validator-1", []byte("payload"))
		if err == nil {
			t.Fatal("Sign returned nil error with no MPC configured — must fail closed")
		}
		if sig != nil {
			t.Fatalf("Sign returned a %d-byte signature with no MPC — must never fabricate", len(sig))
		}
		if !errors.Is(err, kms.ErrSignUnavailable) {
			t.Errorf("Sign err = %v, want ErrSignUnavailable", err)
		}
	})
}

// TestRESTRoundtripOrgScoped: the /v1/kms REST surface upserts + reads a secret
// for the caller's own org (simulated validated principal), and hides another
// org's secret behind a 404 — the org-isolation boundary.
//
// WHY 404 AND NOT 403. The org used to be a path segment that had to equal the
// caller's, so a mismatch was a FORBIDDEN request for a nameable resource. The org
// is now read from the validated principal and is UNSPELLABLE in a URL
// (mount.go reqOrg), and it is folded into a per-org store partition. A caller
// therefore cannot form a request for another org's secret at all: the request it
// does form resolves inside its OWN namespace, where the record is simply absent.
// NOT-FOUND is the correct — and strictly stronger — signal: 403 conceded that the
// resource existed and merely refused it, which is an existence oracle across
// tenants (pinned closed by TestVector4_NoCrossOrgExistenceOracle). Every
// assertion below reads the BODY as well as the status, because after the reshape
// both tenants spell the identical URL and a status alone cannot distinguish
// "refused" from "served the caller its own record".
func TestRESTRoundtripOrgScoped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

		// hanzo caller stores a secret in its own org.
		body, _ := json.Marshal(map[string]string{"name": "API_KEY", "value": "sk-abc123", "env": "main"})
		resp := do(t, app, "POST", "/v1/kms/secrets", "hanzo", string(body), false, asOrgAdmin)
		if resp.StatusCode != 200 {
			t.Fatalf("POST secret (own org) = %d, want 200: %s", resp.StatusCode, readAll(resp.Body))
		}

		// Same caller reads it back.
		resp = do(t, app, "GET", "/v1/kms/secrets/API_KEY?env=main", "hanzo", "", false, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("GET secret (own org) = %d, want 200", resp.StatusCode)
		}
		if v, _ := decode(t, resp.Body)["value"].(string); v != "sk-abc123" {
			t.Errorf("GET secret value = %q, want sk-abc123", v)
		}

		// A different org (evil) sends the IDENTICAL request and gets not-found: its
		// org resolves it into evil's own namespace, which holds nothing. The body
		// check is the isolation assertion — the status only says "no record here".
		resp = do(t, app, "GET", "/v1/kms/secrets/API_KEY?env=main", "evil", "", false, nil)
		if resp.StatusCode != 404 {
			t.Errorf("cross-org GET = %d, want 404 (org unspellable in URL ⇒ resolves in the "+
				"caller's own namespace ⇒ not-found, which hides existence rather than confirming it)", resp.StatusCode)
		}
		if b := readAll(resp.Body); strings.Contains(b, "sk-abc123") {
			t.Fatalf("LEAK: cross-org GET returned hanzo's secret: %s", b)
		}

		// A SuperAdmin gets the SAME answer here, and that is the whole point: this
		// harness omits SanitizeIdentity, so `admin`+isAdmin is just an org header, and
		// there is no URL in which an admin can name another org.
		//
		// DECISION PENDING — the admin cross-org READ was not deleted, it MOVED. This
		// line used to assert 200 via URL traversal (/v1/kms/orgs/{org}/…), a route
		// that no longer exists. The capability survives on the identity instead:
		// SanitizeIdentity's SuperAdmin arm honors X-Org-Id as the effective org
		// (middleware_identity.go `effOrg = cliOrg`), gated on membership of the
		// reserved admin org AND !isMachinePrincipal. That claim-bound org-switch is
		// pinned end-to-end, WITH the switched-into tenant's plaintext asserted, by
		// TestRedIso_C_AdminCrossOrg. Flagged for z: keep the org-switch as the one
		// admin impersonation path (current behavior), or remove platform sudo from
		// KMS entirely? Until that is answered this asserts what the code does.
		resp = do(t, app, "GET", "/v1/kms/secrets/API_KEY?env=main", "admin", "", true, nil)
		if resp.StatusCode != 404 {
			t.Errorf("admin cross-org GET = %d, want 404 (no URL names an org, admin or not; "+
				"the retained admin path is the claim-bound org-switch — see TestRedIso_C_AdminCrossOrg)", resp.StatusCode)
		}
		if b := readAll(resp.Body); strings.Contains(b, "sk-abc123") {
			t.Fatalf("LEAK: admin URL-traversal GET returned hanzo's secret: %s", b)
		}

		// An unauthenticated caller (no principal) is refused BEFORE the store is
		// touched — 403, distinct from the 404 above, because the failure is the
		// credential, not the coordinate.
		resp = do(t, app, "GET", "/v1/kms/secrets/API_KEY?env=main", "", "", false, nil)
		if resp.StatusCode != 401 {
			t.Errorf("anonymous GET = %d, want 401", resp.StatusCode)
		}
		if b := readAll(resp.Body); strings.Contains(b, "sk-abc123") {
			t.Fatalf("LEAK: anonymous GET returned hanzo's secret: %s", b)
		}
	})
}

// TestRESTSecretOpsFailClosedWithoutKey: without a master key, an authorized
// secret op is refused 503, never served insecurely.
func TestRESTSecretOpsFailClosedWithoutKey(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, "")) // no key
	// env is required on writes; supply it so the request reaches the
	// master-key gate this test exercises (rather than 400-ing on input).
	body, _ := json.Marshal(map[string]string{"name": "X", "value": "y", "env": "main"})
	resp := do(t, app, "POST", "/v1/kms/secrets", "hanzo", string(body), false, asOrgAdmin)
	if resp.StatusCode != 503 {
		t.Fatalf("POST secret (no key) = %d, want 503 (fail-closed)", resp.StatusCode)
	}
}

// ── request helpers ────────────────────────────────────────────────────────────

// do drives an in-process request, setting the identity headers SanitizeIdentity
// would set for a validated principal (org, isAdmin). An empty org + admin=false
// simulates an unauthenticated caller.
// asOrgAdmin is the authority a WRITE to one's own org carries: admin OF THAT ORG.
// Deliberately NOT platform sudo — the two admin scopes stay apart, so a fixture that
// seeds its own tenant never borrows cross-tenant authority to do it.
var asOrgAdmin = map[string]string{"X-User-IsOrgAdmin": "true"}

// do issues a request as a principal assembled from its arguments: org membership
// from org, platform sudo from admin, and anything further from extra — which is how
// a caller presents the org-admin authority the mutating routes require (asOrgAdmin).
//
// These requests are injected BEHIND the identity boundary, so the headers stand as
// written; that is what makes this a faithful test of the GATE. The boundary's own
// job — that no client copy of any of these names survives ingress — is a different
// property, asserted where it lives (cloud's identity contract).
func do(t *testing.T, app *zip.App, method, path, org, body string, admin bool, extra map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if admin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func decode(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	b, _ := io.ReadAll(r)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode json %q: %v", string(b), err)
	}
	return m
}

func readAll(r io.Reader) string { b, _ := io.ReadAll(r); return string(b) }

func bytesContains(haystack, needle []byte) bool {
	return strings.Contains(string(haystack), string(needle))
}

// TestASecretHasOneAddressHoweverItIsSpelled stores a secret under a SUB-PATH and
// reads it back the two ways a caller can spell that address.
//
// The router hands a captured segment over exactly as it arrived — it decodes
// nothing — while every client cut from this API percent-encodes a path
// parameter: Go's url.PathEscape, Python's quote(safe="") and JS's
// encodeURIComponent all render "ci/deploy/TOKEN" as "ci%2Fdeploy%2FTOKEN". So a
// secret filed under a sub-path answered the console and 404'd every SDK, and the
// 404 read as "no such secret" rather than as "you cannot spell this".
func TestASecretHasOneAddressHoweverItIsSpelled(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))
		body, _ := json.Marshal(map[string]string{
			"name": "TOKEN", "path": "ci/deploy", "value": "s3cret", "env": "default",
		})
		if r := do(t, app, "POST", "/v1/kms/secrets", "hanzo", string(body), false, asOrgAdmin); r.StatusCode != http.StatusOK {
			t.Fatalf("store: %d %s", r.StatusCode, readAll(r.Body))
		}
		for _, spelling := range []string{
			"/v1/kms/secrets/ci/deploy/TOKEN?env=default",
			"/v1/kms/secrets/ci%2Fdeploy%2FTOKEN?env=default",
		} {
			if r := do(t, app, "GET", spelling, "hanzo", "", false, nil); r.StatusCode != http.StatusOK {
				t.Errorf("GET %s = %d, want 200 — the two spellings address one secret", spelling, r.StatusCode)
			}
		}
	})
}

// TestAnEncodedTraversalIsStillRefused is the half that decoding could have cost
// and does not.
//
// Decoding happens BEFORE the split and the validators run AFTER it, so an
// encoded "../" becomes a real "../" and is then refused by the same rule that
// refuses a plain one. Decoding widens what a caller may SPELL, never what a
// caller may REACH — and on the credential broker that is the property worth a
// test of its own rather than a sentence.
func TestAnEncodedTraversalIsStillRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))
		for _, escape := range []string{
			"/v1/kms/secrets/ci%2F..%2F..%2Fother%2FTOKEN?env=default",
			"/v1/kms/secrets/%2E%2E%2F%2E%2E%2FTOKEN?env=default",
		} {
			if r := do(t, app, "GET", escape, "hanzo", "", false, nil); r.StatusCode != http.StatusBadRequest {
				t.Errorf("GET %s = %d, want 400 — an encoded traversal must be refused by the same "+
					"rule as a plain one", escape, r.StatusCode)
			}
		}
	})
}
