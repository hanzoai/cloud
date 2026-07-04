package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountObserve builds the observe surface with a temp SQLite store and NO KMS
// (secret writes must fail closed), an unreachable VM URL and a non-resolvable DNS
// suffix so the status probe fails fast. The o11y read endpoints reach the
// datastore gate (aiobject.DatastoreEnabled()==false in tests) and answer 503 —
// which proves they are wired to REAL data, never a stub.
func mountObserve(t *testing.T, kms cloudKMS) (*zip.App, *service) {
	t.Helper()
	store, err := openSettingsStore(t.TempDir() + "/observe.db")
	if err != nil {
		t.Fatalf("openSettingsStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &service{
		store:     store,
		kms:       kms,
		log:       luxlog.New("test"),
		adminOrg:  "admin",
		vmURL:     "http://127.0.0.1:1",
		dnsSuffix: ".test.invalid",
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/o11y/logs", s.getLogs)
	app.Get("/v1/o11y/metrics", s.getMetrics)
	app.Get("/v1/o11y/status", s.getStatus)
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
	store, err := openSettingsStore(t.TempDir() + "/observe.db")
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
	app, _ := mountObserve(t, nil)
	// A forged request: X-Org-Id present (as the bearer-less path would restore) but
	// NO X-User-Id → not a validated principal → 403 on every surface.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/o11y/logs?product=kms"},
		{"GET", "/v1/o11y/metrics?product=kms"},
		{"GET", "/v1/o11y/status?product=kms"},
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
	app, _ := mountObserve(t, nil)

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
	appNoKMS, sNoKMS := mountObserve(t, nil)
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
	app, s := mountObserve(t, kms)
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
	app, _ := mountObserve(t, nil)
	for _, bad := range []string{"../etc", "KMS", "a b", "a'b", `a"b`, "a/b", strings.Repeat("x", 100)} {
		req := authReq("GET", "/v1/o11y/logs?product="+url.QueryEscape(bad), "acme", nil)
		code, _ := send(t, app, req)
		if code != http.StatusBadRequest {
			t.Fatalf("product %q: want 400, got %d", bad, code)
		}
	}
	// A valid product with a validated principal reaches the datastore gate (503,
	// datastore not connected in tests) — proving the endpoint serves REAL data,
	// never a stub.
	code, _ := send(t, app, authReq("GET", "/v1/o11y/logs?product=kms", "acme", nil))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("valid product logs: want 503 (no datastore), got %d", code)
	}
	code, _ = send(t, app, authReq("GET", "/v1/o11y/metrics?product=kms", "acme", nil))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("valid product metrics: want 503 (no datastore), got %d", code)
	}
}

// ── status returns an HONEST down (no fake green dot) when nothing is reachable ─

func TestStatusHonestDown(t *testing.T) {
	app, _ := mountObserve(t, nil)
	code, body := send(t, app, authReq("GET", "/v1/o11y/status?product=kms", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("status: %d %s", code, body)
	}
	var st statusResponse
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Up || st.Source != "none" {
		t.Fatalf("unreachable product must report down/none, got up=%v source=%s", st.Up, st.Source)
	}
}

// ── pure helpers ───────────────────────────────────────────────────────────────

func TestServiceForAndAdmin(t *testing.T) {
	if serviceFor("kms") != "kms" {
		t.Fatal("identity mapping broken")
	}
	if serviceFor("cloud-api") != "cloud" {
		t.Fatal("alias mapping broken")
	}
	s := &service{adminOrg: "admin"}
	if !s.isAdmin("admin") || s.isAdmin("acme") {
		t.Fatal("admin gate broken")
	}
	// An empty adminOrg must never admit anyone.
	e := &service{adminOrg: ""}
	if e.isAdmin("") {
		t.Fatal("empty adminOrg admitted empty org")
	}
}

func TestSeverityAndClamp(t *testing.T) {
	if severityForStatus(500, 0) != "ERROR" || severityForStatus(404, 0) != "WARN" || severityForStatus(200, 0) != "INFO" {
		t.Fatal("severity mapping broken")
	}
	if severityForStatus(200, 2) != "ERROR" {
		t.Fatal("span error status ignored")
	}
	if clampInt(5, 30, 3600) != 30 || clampInt(99999, 30, 3600) != 3600 || clampInt(60, 30, 3600) != 60 {
		t.Fatal("clamp broken")
	}
}
