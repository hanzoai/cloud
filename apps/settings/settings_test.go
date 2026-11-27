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
	"time"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	"github.com/hanzoai/cloud/internal/sqltest"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// compose installs what the program's composer installs — cloud.Bridge, once at
// the app root. A subsystem never installs its own, so a test app owes the same
// root install; without it every org-scoped op answers a 403 no production
// program would produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mountSettings builds the settings surface with a temp SQLite store and an
// optional KMS (nil ⇒ secret writes must fail closed, never plaintext).
func mountSettings(t *testing.T, kms cloudKMS) (*zip.App, *service) {
	t.Helper()
	store, err := openSettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("openSettingsStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &service{store: store, kms: kms, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	// The REAL registration, not a reconstruction of it: routes() is what Mount
	// calls, so a route or a middleware added there is exercised here too.
	routes(app, s)
	return app, s
}

// cloudKMS is the interface subset the service holds (cloud.Store). Declared
// here so the test's fakeKMS can satisfy it without importing the full alias.
type cloudKMS interface {
	GetSecret(ctx context.Context, ref string) ([]byte, error)
	PutSecret(ctx context.Context, ref string, value []byte) error
	DeleteSecret(ctx context.Context, ref string) error
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
func (k *fakeKMS) DeleteSecret(_ context.Context, ref string) error {
	delete(k.m, ref)
	return nil
}
func (k *fakeKMS) Sign(_ context.Context, _ string, _ []byte) ([]byte, error) { return nil, nil }

// send drives a hand-built request (auth headers set by the caller).
func send(t *testing.T, app *zip.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store, err := openSettingsStore(t.TempDir())
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
	})
}

// ── HTTP: the principal gate fails closed on EVERY endpoint ────────────────────

func TestEndpointsRequireValidatedPrincipal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountSettings(t, nil)
		// A forged request: X-Org-Id present (as the bearer-less path would restore) but
		// NO X-User-Id → not a validated principal → 401 on every surface.
		for _, tc := range []struct{ method, path string }{
			{"GET", "/v1/settings/kms"},
			{"PUT", "/v1/settings/kms"},
		} {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("X-Org-Id", "victim") // forged, no validated user
			code, _ := send(t, app, req)
			if code != http.StatusUnauthorized {
				t.Fatalf("%s %s: want 401 for forged (no X-User-Id), got %d", tc.method, tc.path, code)
			}
		}
	})
}

// ── HTTP: settings CRUD roundtrip + cross-tenant isolation ─────────────────────

func TestSettingsHTTPRoundtripAndIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
		var v View
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
	})
}

// ── HTTP: secrets never land in SQLite; fail closed without KMS ────────────────

func TestSecretsGoToKMSNotSQLite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// ── HTTP: product validation rejects traversal/injection at the boundary ───────

func TestProductValidation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
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

// ── cross-tenant admin API: a SuperAdmin manages ANOTHER org's config ──────────

// TestAdminCrossTenant drives the org-explicit AdminGet/AdminPut/AdminList the
// cloud admin surface calls. The org is an ARGUMENT, not the caller's own — the
// admit gate lives up in apps/admin, so here the invariant under test is that the
// org actually keys every read and write: one org's write is invisible to another,
// and a secret VALUE seals into the TARGET org's KMS namespace, never SQLite.
func TestAdminCrossTenant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		kms := newFakeKMS()
		_, s := mountSettings(t, kms)
		mounted = s
		t.Cleanup(func() { mounted = nil })
		ctx := context.Background()

		// A SuperAdmin writes acme's kms config, with a secret.
		if _, err := AdminPut(ctx, "acme", "kms", map[string]any{"retention": "30d"}, map[string]string{"webhook": "s3cr3t"}); err != nil {
			t.Fatalf("AdminPut acme: %v", err)
		}
		// The secret VALUE is in KMS under acme's namespace, never returned or stored raw.
		if got := kms.m["orgs/acme/settings/kms/webhook"]; string(got) != "s3cr3t" {
			t.Fatalf("secret not sealed to acme KMS ref: %q", got)
		}
		v, err := AdminGet(ctx, "acme", "kms")
		if err != nil {
			t.Fatalf("AdminGet acme: %v", err)
		}
		if !strings.Contains(string(v.Config), `"retention":"30d"`) {
			t.Fatalf("acme config not persisted: %s", v.Config)
		}
		if len(v.SecretKeys) != 1 || v.SecretKeys[0] != "webhook" {
			t.Fatalf("acme secret key not tracked: %v", v.SecretKeys)
		}

		// A DIFFERENT org the SuperAdmin also administers never sees acme's config.
		other, err := AdminGet(ctx, "beta", "kms")
		if err != nil {
			t.Fatalf("AdminGet beta: %v", err)
		}
		if strings.Contains(string(other.Config), "30d") {
			t.Fatalf("cross-tenant leak: beta saw acme's config: %s", other.Config)
		}

		// List names only the products the org actually configured.
		acmeList, err := AdminList(ctx, "acme")
		if err != nil {
			t.Fatalf("AdminList acme: %v", err)
		}
		if len(acmeList) != 1 || acmeList[0].Product != "kms" {
			t.Fatalf("AdminList acme unexpected: %+v", acmeList)
		}
		if betaList, err := AdminList(ctx, "beta"); err != nil || len(betaList) != 0 {
			t.Fatalf("AdminList beta: want empty, got %+v (err %v)", betaList, err)
		}

		// A malformed target org is refused before it can become a store key.
		if _, err := AdminGet(ctx, "bad org", "kms"); err == nil {
			t.Fatalf("AdminGet: expected refusal for malformed org")
		}
	})
}

// TestAdminPutSecretFailsClosedNoKMS proves the cross-tenant write inherits the
// self-service custody rule: a secret with no KMS configured is refused whole, so a
// plaintext secret can never reach SQLite by the admin path either.
func TestAdminPutSecretFailsClosedNoKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := mountSettings(t, nil) // nil KMS
		mounted = s
		t.Cleanup(func() { mounted = nil })
		if _, err := AdminPut(context.Background(), "acme", "kms", nil, map[string]string{"webhook": "s3cr3t"}); err == nil {
			t.Fatalf("expected fail-closed refusal with no KMS")
		}
	})
}

// A pair is created once and rewritten after: the second Put changes the config, the
// secret key names and updated_at, and keeps the created_at the first one gave it —
// in the Settings it answers and in the row a later read finds, on either engine.
func TestPutKeepsWhenAPairWasCreated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store, err := openSettingsStore(t.TempDir())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = store.Close() }()
		ctx := context.Background()

		first, err := store.Put(ctx, Settings{Org: "acme", Product: "kms", Config: `{"a":1}`, UpdatedAt: 100})
		if err != nil || first.CreatedAt != 100 || first.UpdatedAt != 100 {
			t.Fatalf("first put = %+v, %v", first, err)
		}
		second, err := store.Put(ctx, Settings{Org: "acme", Product: "kms", Config: `{"a":2}`, SecretKeys: []string{"token"}, UpdatedAt: 250})
		if err != nil || second.CreatedAt != 100 || second.UpdatedAt != 250 {
			t.Fatalf("second put = %+v, %v", second, err)
		}
		got, err := store.Get(ctx, "acme", "kms")
		if err != nil || got.CreatedAt != 100 || got.UpdatedAt != 250 || got.Config != `{"a":2}` || len(got.SecretKeys) != 1 || got.SecretKeys[0] != "token" {
			t.Fatalf("read back = %+v, %v", got, err)
		}
		// Another product of the same org is its own pair.
		other, err := store.Put(ctx, Settings{Org: "acme", Product: "iam", Config: `{}`, UpdatedAt: 300})
		if err != nil || other.CreatedAt != 300 {
			t.Fatalf("other product = %+v, %v", other, err)
		}
	})
}

// An org's data-handling policy is read once from its privacy, platform-console and
// models settings: zero data retention by any of the switches ZDREnabled has always
// read, and the region its data must stay in by dataResidency (hanzo-inc/cloud#801).
// An org that states neither requires neither, and an unmounted store requires nothing.
func TestPrivacyStatesRetentionAndResidency(t *testing.T) {
	ctx := context.Background()
	if zdr, region := Privacy(ctx, "acme"); zdr || region != "" {
		t.Fatalf("unmounted: zdr %v region %q", zdr, region)
	}
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := mountSettings(t, nil)
		mounted = s
		t.Cleanup(func() { mounted = nil })
		if _, err := AdminPut(ctx, "acme", "privacy", map[string]any{"retentionDays": "0", "dataResidency": " EU "}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := AdminPut(ctx, "beta", "models", map[string]any{"enforceZdrModelsOnly": true}, nil); err != nil {
			t.Fatal(err)
		}
		if zdr, region := Privacy(ctx, "acme"); !zdr || region != "eu" {
			t.Errorf("acme: zdr %v region %q", zdr, region)
		}
		if zdr, region := Privacy(ctx, "beta"); !zdr || region != "" {
			t.Errorf("beta: zdr %v region %q", zdr, region)
		}
		if zdr, region := Privacy(ctx, "gamma"); zdr || region != "" {
			t.Errorf("gamma: zdr %v region %q", zdr, region)
		}
		if !ZDREnabled(ctx, "acme") || ZDREnabled(ctx, "gamma") {
			t.Error("ZDREnabled reads the same policy")
		}
	})
}
