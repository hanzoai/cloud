package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountSettings builds the settings surface with a temp SQLite store and an
// optional KMS (nil ⇒ secret writes must fail closed, never plaintext).
func mountSettings(t *testing.T, kms cloudKMS) (*zip.App, *service) {
	t.Helper()
	store, err := openSettingsStore(t.TempDir() + "/settings.db")
	if err != nil {
		t.Fatalf("openSettingsStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &service{store: store, kms: kms, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/settings/:product", s.getSettings)
	app.Put("/v1/settings/:product", s.putSettings)
	return app, s
}

// cloudKMS is the interface subset the service holds (cloud.KMSClient). Declared
// here so the test's fakeKMS can satisfy it without importing the full alias.
type cloudKMS interface {
	GetSecret(ctx context.Context, ref string) ([]byte, error)
	PutSecret(ctx context.Context, ref string, value []byte) error
	Sign(ctx context.Context, keyRef string, payload []byte) ([]byte, error)
}

// fakeKMS is an in-memory KMS. It records what was put so a test can assert the
// secret VALUE went to KMS and never to SQLite.
type fakeKMS struct{ m map[string][]byte }

func newFakeKMS() *fakeKMS { return &fakeKMS{m: map[string][]byte{}} }

func (k *fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	v, ok := k.m[ref]
	if !ok {
		return nil, io.EOF
	}
	return v, nil
}
func (k *fakeKMS) PutSecret(_ context.Context, ref string, value []byte) error {
	k.m[ref] = append([]byte(nil), value...)
	return nil
}
func (k *fakeKMS) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) { return nil, nil }

// send drives a hand-built request (auth headers set by the caller).
func send(t *testing.T, app *zip.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// authReq builds a request with a VALIDATED principal (X-User-Id set, as
// SanitizeIdentity would from a verified token) for org.
func authReq(method, path, org string, body any) *http.Request {
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
	req.Header.Set("X-User-Id", "u_"+org)
	return req
}

// ── settings store: tenant isolation on the persisted CRUD ─────────────────────

func TestSettingsStoreIsolation(t *testing.T) {
	store, err := openSettingsStore(t.TempDir() + "/settings.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	if _, err := store.Put(ctx, Settings{Org: "acme", Product: "kms", Config: `{"retention":"30d"}`, SecretKeys: []string{"webhook"}, UpdatedAt: 100}); err != nil {
		t.Fatalf("put acme: %v", err)
	}
	got, err := store.Get(ctx, "acme", "kms")
	if err != nil {
		t.Fatalf("get acme: %v", err)
	}
	if got.Config != `{"retention":"30d"}` || len(got.SecretKeys) != 1 || got.SecretKeys[0] != "webhook" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	// A DIFFERENT org must NOT read acme's config for the same product.
	if _, err := store.Get(ctx, "evil", "kms"); err != errNotFound {
		t.Fatalf("cross-tenant read: want errNotFound, got %v", err)
	}
	// Same org, different product → not found (product-scoped).
	if _, err := store.Get(ctx, "acme", "iam"); err != errNotFound {
		t.Fatalf("cross-product read: want errNotFound, got %v", err)
	}
}

// ── HTTP: the principal gate fails closed on EVERY endpoint ────────────────────

func TestEndpointsRequireValidatedPrincipal(t *testing.T) {
	app, _ := mountSettings(t, nil)
	// A forged request: X-Org-Id present (as the bearer-less path would restore) but
	// NO X-User-Id → not a validated principal → 403 on every surface.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/settings/kms"},
		{"PUT", "/v1/settings/kms"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-Org-Id", "victim") // forged, no validated user
		code, _ := send(t, app, req)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s: want 403 for forged (no X-User-Id), got %d", tc.method, tc.path, code)
		}
	}
}

// ── HTTP: settings CRUD roundtrip + cross-tenant isolation ─────────────────────

func TestSettingsHTTPRoundtripAndIsolation(t *testing.T) {
	app, _ := mountSettings(t, nil)

	// acme writes non-secret config.
	code, body := send(t, app, authReq("PUT", "/v1/settings/kms", "acme", map[string]any{
		"config": map[string]any{"retention": "30d", "enabled": true},
	}))
	if code != http.StatusOK {
		t.Fatalf("put: %d %s", code, body)
	}

	// acme reads it back.
	code, body = send(t, app, authReq("GET", "/v1/settings/kms", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("get acme: %d %s", code, body)
	}
	var v settingsView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(string(v.Config), `"retention":"30d"`) {
		t.Fatalf("acme config not persisted: %s", v.Config)
	}

	// A different org gets its OWN (empty) config, NEVER acme's.
	code, body = send(t, app, authReq("GET", "/v1/settings/kms", "other", nil))
	if code != http.StatusOK {
		t.Fatalf("get other: %d", code)
	}
	if strings.Contains(string(body), "30d") {
		t.Fatalf("cross-tenant leak: other org saw acme's config: %s", body)
	}
}

// ── HTTP: secrets never land in SQLite; fail closed without KMS ────────────────

func TestSecretsGoToKMSNotSQLite(t *testing.T) {
	// Without KMS, a secret write MUST fail closed (never plaintext to SQLite).
	appNoKMS, sNoKMS := mountSettings(t, nil)
	code, _ := send(t, appNoKMS, authReq("PUT", "/v1/settings/kms", "acme", map[string]any{
		"secrets": map[string]string{"webhook": "s3cr3t-value"},
	}))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("secret write w/o KMS: want 503, got %d", code)
	}
	if _, err := sNoKMS.store.Get(context.Background(), "acme", "kms"); err != errNotFound {
		t.Fatalf("failed secret write must not persist anything, got %v", err)
	}

	// With KMS, the secret VALUE goes to KMS; SQLite holds only the key NAME.
	kms := newFakeKMS()
	app, s := mountSettings(t, kms)
	code, body := send(t, app, authReq("PUT", "/v1/settings/kms", "acme", map[string]any{
		"config":  map[string]any{"retention": "7d"},
		"secrets": map[string]string{"webhook": "s3cr3t-value"},
	}))
	if code != http.StatusOK {
		t.Fatalf("put w/ KMS: %d %s", code, body)
	}
	// The secret value is in KMS at the org/product-scoped ref.
	ref := secretRef("acme", "kms", "webhook")
	if got, _ := kms.GetSecret(context.Background(), ref); string(got) != "s3cr3t-value" {
		t.Fatalf("secret not in KMS at %s: %q", ref, got)
	}
	// The SQLite row must NOT contain the plaintext secret anywhere.
	st, err := s.store.Get(context.Background(), "acme", "kms")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(st.Config, "s3cr3t-value") {
		t.Fatalf("PLAINTEXT SECRET IN SQLITE config: %s", st.Config)
	}
	if len(st.SecretKeys) != 1 || st.SecretKeys[0] != "webhook" {
		t.Fatalf("secret key name not tracked: %+v", st.SecretKeys)
	}
	// The read view exposes the key name but NEVER the value.
	code, body = send(t, app, authReq("GET", "/v1/settings/kms", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("get: %d", code)
	}
	if strings.Contains(string(body), "s3cr3t-value") {
		t.Fatalf("secret VALUE leaked in GET response: %s", body)
	}
}

// ── HTTP: product validation rejects traversal/injection at the boundary ───────

func TestProductValidation(t *testing.T) {
	app, _ := mountSettings(t, nil)
	for _, bad := range []string{"../etc", "KMS", "a b", "a'b", `a"b`, "a/b", strings.Repeat("x", 100)} {
		// URL-encode the product so it reaches the :product handler rather than being
		// swallowed by path routing; the handler must reject it 400.
		req := authReq("GET", "/v1/settings/"+percentEscape(bad), "acme", nil)
		code, _ := send(t, app, req)
		if code != http.StatusBadRequest && code != http.StatusNotFound {
			// A slash-bearing slug can 404 at the router before the handler; anything
			// else must be a boundary 400. Either way it must NEVER 200/persist.
			t.Fatalf("product %q: want 400 (or 404 for slash), got %d", bad, code)
		}
	}
}

// percentEscape encodes a path segment so injection payloads reach the handler.
func percentEscape(s string) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteByte(r)
		default:
			b.WriteByte('%')
			const hex = "0123456789ABCDEF"
			b.WriteByte(hex[r>>4])
			b.WriteByte(hex[r&0xf])
		}
	}
	return b.String()
}
