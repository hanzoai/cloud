package kms_test

// Integration tests for the embedded KMS subsystem, exercised through the REAL
// orchestrator path (BuildDeps → the init()-registered MountSpec → the zip/Fiber
// stack), mirroring cmd/cloud/main_test.go. Requests run in-process via
// app.Fiber().Test — no listener, no external KMS, no PostgreSQL.
//
// SanitizeIdentity does not run in this harness (it is wired in serve.go, not
// MountAll), so a test simulates a validated principal by setting the same
// identity headers SanitizeIdentity would emit: X-Org-Id and X-User-IsAdmin. In
// production those are stripped from client input and re-issued only for a
// JWT-validated principal, so the org-scope gate is real; here we drive it
// directly.

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

	"github.com/hanzoai/cloud"
	// clients/kms: its init() registers the in-process KMS client factory
	// (RegisterKMSClientFactory) BuildDeps needs; it also exports Mount (the
	// subsystem spec below) + the *kms.Client type these tests assert on.
	"github.com/hanzoai/cloud/clients/kms"
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
func mountSpecs() []cloud.MountSpec {
	return []cloud.MountSpec{{Name: "kms", Mount: cloud.Typed(kms.Mount), OwnsHealth: true}}
}

// newApp wires BuildDeps + the canonical middleware + MountAll for the kms
// subsystem, exactly like main()'s path. Returns the app and the built deps (so
// tests can reach the in-process KMSClient directly).
func newApp(t *testing.T, cfg *cloud.Config) (*zip.App, cloud.Deps) {
	t.Helper()
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(deps.Logger))
	if err := cloud.MountAll(app, mountSpecs(), cfg, deps); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	return app, deps
}

func baseCfg(t *testing.T, masterKey string) *cloud.Config {
	t.Helper()
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
// cloud.KMSClient (deps.KMS) against a temp CLOUD_DATA_DIR.
func TestKMSClientRoundtrip(t *testing.T) {
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
	if _, err := kc.GetSecret(ctx, "does-not-exist"); !errors.Is(err, kms.ErrSecretNotFound) {
		t.Errorf("GetSecret(missing) err = %v, want ErrSecretNotFound", err)
	}
}

// TestSecretNotStoredInPlaintext: the on-disk bytes must NOT contain the
// plaintext — proof the AES-256-GCM Seal envelope is applied before storage
// (never store secrets in plaintext).
func TestSecretNotStoredInPlaintext(t *testing.T) {
	dir := t.TempDir()
	cfg := &cloud.Config{
		Brand: "hanzo", Domain: "api.hanzo.ai", IAMIssuer: "https://hanzo.id",
		DataDir: dir, Enable: []string{"kms"}, KMSMasterKeyRef: masterKeyB64(t),
	}
	_, deps := newApp(t, cfg)

	marker := []byte("PLAINTEXT-MARKER-should-never-hit-disk")
	if err := deps.KMS.PutSecret(context.Background(), "svc/DB_URL@main", marker); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	// Close so the KV flushes to disk, then scan the store files for the marker.
	if c, ok := deps.KMS.(*kms.Client); ok {
		if err := c.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}

	found := false
	root := filepath.Join(dir, "kms")
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
}

// TestSignFailsClosedNoMPC: Sign must fail closed with a clear, non-fabricated
// error when no MPC backend is configured. It must NEVER return a signature.
func TestSignFailsClosedNoMPC(t *testing.T) {
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
}

// TestRESTRoundtripOrgScoped: the /v1/kms REST surface upserts + reads a secret
// for the caller's own org (simulated validated principal), and rejects a
// cross-org read with 403 — the org-isolation boundary.
func TestRESTRoundtripOrgScoped(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	// hanzo caller stores a secret in its own org.
	body, _ := json.Marshal(map[string]string{"name": "API_KEY", "value": "hk-abc123", "env": "main"})
	resp := do(t, app, "POST", "/v1/kms/orgs/hanzo/secrets", "hanzo", string(body), false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("POST secret (own org) = %d, want 200: %s", resp.StatusCode, readAll(resp.Body))
	}

	// Same caller reads it back.
	resp = do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets/API_KEY?env=main", "hanzo", "", false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET secret (own org) = %d, want 200", resp.StatusCode)
	}
	if v, _ := decode(t, resp.Body)["value"].(string); v != "hk-abc123" {
		t.Errorf("GET secret value = %q, want hk-abc123", v)
	}

	// A different org (evil) may NOT read hanzo's secret.
	resp = do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets/API_KEY?env=main", "evil", "", false, nil)
	if resp.StatusCode != 403 {
		t.Errorf("cross-org GET = %d, want 403 (org isolation)", resp.StatusCode)
	}

	// A SuperAdmin may read any org.
	resp = do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets/API_KEY?env=main", "admin", "", true, nil)
	if resp.StatusCode != 200 {
		t.Errorf("admin cross-org GET = %d, want 200", resp.StatusCode)
	}

	// An unauthenticated caller (no org, not admin) is refused.
	resp = do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets/API_KEY?env=main", "", "", false, nil)
	if resp.StatusCode != 403 {
		t.Errorf("anonymous GET = %d, want 403", resp.StatusCode)
	}
}

// TestRESTSecretOpsFailClosedWithoutKey: without a master key, an authorized
// secret op is refused 503, never served insecurely.
func TestRESTSecretOpsFailClosedWithoutKey(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, "")) // no key
	// env is required on writes; supply it so the request reaches the
	// master-key gate this test exercises (rather than 400-ing on input).
	body, _ := json.Marshal(map[string]string{"name": "X", "value": "y", "env": "main"})
	resp := do(t, app, "POST", "/v1/kms/orgs/hanzo/secrets", "hanzo", string(body), false, nil)
	if resp.StatusCode != 503 {
		t.Fatalf("POST secret (no key) = %d, want 503 (fail-closed)", resp.StatusCode)
	}
}

// ── request helpers ────────────────────────────────────────────────────────────

// do drives an in-process request, setting the identity headers SanitizeIdentity
// would set for a validated principal (org, isAdmin). An empty org + admin=false
// simulates an unauthenticated caller.
func do(t *testing.T, app *zip.App, method, path, org, body string, admin bool, _ any) *http.Response {
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
	resp, err := app.Fiber().Test(req)
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
