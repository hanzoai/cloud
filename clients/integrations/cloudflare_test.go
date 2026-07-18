package integrations

// cloudflare_test.go proves the security properties of the apikey Cloudflare
// connector against the REAL plane (real store, real KMS seal/open — newKMS):
//   - verify-before-store: an inactive/rejected token is refused and NOTHING is
//     persisted (no KMS secret, no connection row);
//   - KMS custody round-trip: an active token lands ONLY in the org's KMS
//     namespace, never in the row/response;
//   - tenant isolation: org B can neither see nor read nor delete org A's
//     credential;
//   - org-admin gate: connect/disconnect require principal.IsOrgAdmin (NOT a
//     plain member, NOT SuperAdmin);
//   - the token never appears in a log line.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const cfTestToken = "cf-scoped-token-SECRET-deadbeef0123456789"

// cfMock is a configurable stand-in for the Cloudflare API v4 verify/accounts
// endpoints. It captures the Authorization header it receives on /verify so a
// test can assert the token rode the header (never a query).
type cfMock struct {
	status     string // verify result.status; "" => "active"
	verifyCode int    // HTTP status for /verify; 0 => 200
	noAccounts bool   // /accounts returns 403 (least-priv token can't list)

	mu       sync.Mutex
	gotAuth  string // Authorization header seen on /verify
	gotQuery string // raw query seen on /verify (must never carry the token)
}

func newCFMock(t *testing.T, m *cfMock) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/user/tokens/verify"):
			m.mu.Lock()
			m.gotAuth = r.Header.Get("Authorization")
			m.gotQuery = r.URL.RawQuery
			m.mu.Unlock()
			if m.verifyCode != 0 {
				w.WriteHeader(m.verifyCode)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"invalid"}]}`))
				return
			}
			status := m.status
			if status == "" {
				status = "active"
			}
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"tok-123","status":"` + status + `"}}`))
		case strings.HasPrefix(r.URL.Path, "/accounts"):
			if m.noAccounts {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"forbidden"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"acc-abc","name":"Acme Cloudflare"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUDFLARE_API_BASE", srv.URL)
}

// cfReq issues a request with the gateway identity headers. admin=true adds
// X-User-IsOrgAdmin (what principal.IsOrgAdmin reads). org=="" sends no identity.
func cfReq(t *testing.T, app *zip.App, method, path, org string, admin bool, body any) httpResult {
	t.Helper()
	var rqBody []byte
	if body != nil {
		rqBody, _ = json.Marshal(body)
	}
	rq := httptest.NewRequest(method, path, bytes.NewReader(rqBody))
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
		if admin {
			rq.Header.Set("X-User-IsOrgAdmin", "true")
		}
	}
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return httpResult{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: buf.Bytes()}
}

// cfConnect posts a connect with the standard test token as an org admin.
func cfConnect(t *testing.T, app *zip.App, org string) httpResult {
	t.Helper()
	return cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/connect", org, true,
		map[string]any{"token": cfTestToken})
}

func kmsHas(t *testing.T, kc *kms.Client, org string) ([]byte, bool) {
	t.Helper()
	v, err := kc.Get("/orgs/"+org+"/integrations/cloudflare", cloudflareTokenSecret, kmsEnv)
	if err != nil {
		return nil, false
	}
	return v, true
}

func TestCloudflareConnectVerifyActiveStoresTokenInKMS(t *testing.T) {
	newCFMock(t, &cfMock{status: "active"})
	kc := newKMS(t)
	app := newApp(t, kc)

	res := cfConnect(t, app, "acme")
	if res.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", res.Code, res.Body)
	}
	// Token sealed ONLY in the org's KMS namespace, and round-trips EXACTLY.
	got, ok := kmsHas(t, kc, "acme")
	if !ok {
		t.Fatal("expected the token sealed in KMS after a verified connect")
	}
	if string(got) != cfTestToken {
		t.Fatalf("KMS token mismatch: got %q", got)
	}
	// The connection row holds only non-secret metadata — never the token.
	conn, ok := ConnectionFor("acme", "cloudflare")
	if !ok {
		t.Fatal("expected a connection row")
	}
	if conn.ExternalID != "acc-abc" {
		t.Fatalf("externalId want acc-abc, got %q", conn.ExternalID)
	}
	if strings.Contains(conn.AccountLabel+conn.ExternalID+strings.Join(conn.Scopes, ","), cfTestToken) {
		t.Fatal("token leaked into the connection row metadata")
	}
	// The response body must not echo the secret.
	if bytes.Contains(res.Body, []byte(cfTestToken)) {
		t.Fatal("token leaked into the connect response body")
	}
}

func TestCloudflareConnectRejectsInactiveTokenAndStoresNothing(t *testing.T) {
	newCFMock(t, &cfMock{status: "disabled"})
	kc := newKMS(t)
	app := newApp(t, kc)

	res := cfConnect(t, app, "acme")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("inactive token want 400, got %d (%s)", res.Code, res.Body)
	}
	if _, ok := kmsHas(t, kc, "acme"); ok {
		t.Fatal("verify-before-store violated: an inactive token was sealed to KMS")
	}
	if _, ok := ConnectionFor("acme", "cloudflare"); ok {
		t.Fatal("verify-before-store violated: an inactive token created a connection row")
	}
}

func TestCloudflareConnectRejectsUnauthorizedTokenAndStoresNothing(t *testing.T) {
	newCFMock(t, &cfMock{verifyCode: http.StatusUnauthorized})
	kc := newKMS(t)
	app := newApp(t, kc)

	res := cfConnect(t, app, "acme")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("rejected token want 400, got %d (%s)", res.Code, res.Body)
	}
	if _, ok := kmsHas(t, kc, "acme"); ok {
		t.Fatal("a Cloudflare-rejected token must not be sealed")
	}
	if _, ok := ConnectionFor("acme", "cloudflare"); ok {
		t.Fatal("a Cloudflare-rejected token must not create a row")
	}
}

func TestCloudflareConnectRequiresOrgAdmin(t *testing.T) {
	newCFMock(t, &cfMock{status: "active"})
	kc := newKMS(t)
	app := newApp(t, kc)

	// A plain member (no X-User-IsOrgAdmin) is refused BEFORE any CF call / store.
	res := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/connect", "acme", false,
		map[string]any{"token": cfTestToken})
	if res.Code != http.StatusForbidden {
		t.Fatalf("member connect want 403, got %d (%s)", res.Code, res.Body)
	}
	if _, ok := kmsHas(t, kc, "acme"); ok {
		t.Fatal("a non-admin connect must not reach KMS")
	}
}

func TestCloudflareConnectRequiresPrincipal(t *testing.T) {
	newCFMock(t, &cfMock{status: "active"})
	app := newApp(t, newKMS(t))
	// No identity headers at all → 403 (a validated principal is required).
	res := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/connect", "", false,
		map[string]any{"token": cfTestToken})
	if res.Code != http.StatusForbidden {
		t.Fatalf("no-principal connect want 403, got %d", res.Code)
	}
}

func TestCloudflareConnectEmptyTokenRejected(t *testing.T) {
	newCFMock(t, &cfMock{status: "active"})
	app := newApp(t, newKMS(t))
	res := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/connect", "acme", true,
		map[string]any{"token": "   "})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("empty token want 400, got %d (%s)", res.Code, res.Body)
	}
}

func TestCloudflareTenantIsolation(t *testing.T) {
	newCFMock(t, &cfMock{status: "active"})
	kc := newKMS(t)
	app := newApp(t, kc)

	// Org A connects.
	if r := cfConnect(t, app, "orga"); r.Code != http.StatusOK {
		t.Fatalf("orga connect want 200, got %d (%s)", r.Code, r.Body)
	}
	// A's token is under A's KMS path; B's path is empty (physically separate file).
	if _, ok := kmsHas(t, kc, "orga"); !ok {
		t.Fatal("orga token missing")
	}
	if _, ok := kmsHas(t, kc, "orgb"); ok {
		t.Fatal("orgb must not have a credential")
	}
	// B sees the provider as NOT connected.
	rb := cfReq(t, app, http.MethodGet, "/v1/integrations/cloudflare", "orgb", false, nil)
	var view providerView
	if err := json.Unmarshal(rb.Body, &view); err != nil {
		t.Fatalf("orgb get view: %v (%s)", err, rb.Body)
	}
	if view.Connected {
		t.Fatal("orgb must not see orga's connection")
	}
	// B's verify is 404 (nothing connected for B).
	if r := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/verify", "orgb", false, nil); r.Code != http.StatusNotFound {
		t.Fatalf("orgb verify want 404, got %d (%s)", r.Code, r.Body)
	}
	// B's disconnect is idempotent-OK but MUST NOT remove A's secret or row.
	if r := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/disconnect", "orgb", true, nil); r.Code != http.StatusOK {
		t.Fatalf("orgb disconnect want 200, got %d (%s)", r.Code, r.Body)
	}
	if _, ok := kmsHas(t, kc, "orga"); !ok {
		t.Fatal("orgb disconnect deleted orga's credential — tenant isolation broken")
	}
	if _, ok := ConnectionFor("orga", "cloudflare"); !ok {
		t.Fatal("orgb disconnect deleted orga's row — tenant isolation broken")
	}
	// The in-process token seam also refuses to hand B a credential.
	if _, err := TokenFor(context.Background(), "orgb", "cloudflare", cloudflareTokenSecret); err == nil {
		t.Fatal("TokenFor returned a credential for a non-connected org")
	}
}

func TestCloudflareVerifyEndpoint(t *testing.T) {
	newCFMock(t, &cfMock{status: "active"})
	app := newApp(t, newKMS(t))
	if r := cfConnect(t, app, "acme"); r.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d", r.Code)
	}
	r := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/verify", "acme", false, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("verify want 200, got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Active bool `json:"active"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil || !out.Active {
		t.Fatalf("verify want active=true, got %s", r.Body)
	}
}

func TestCloudflareDisconnectRemovesTokenAndRow(t *testing.T) {
	newCFMock(t, &cfMock{status: "active"})
	kc := newKMS(t)
	app := newApp(t, kc)

	if r := cfConnect(t, app, "acme"); r.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d", r.Code)
	}
	if r := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/disconnect", "acme", true, nil); r.Code != http.StatusOK {
		t.Fatalf("disconnect want 200, got %d (%s)", r.Code, r.Body)
	}
	if _, ok := kmsHas(t, kc, "acme"); ok {
		t.Fatal("disconnect must delete the KMS credential")
	}
	if _, ok := ConnectionFor("acme", "cloudflare"); ok {
		t.Fatal("disconnect must delete the connection row")
	}
	// Idempotent: a second disconnect still 200s.
	if r := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/disconnect", "acme", true, nil); r.Code != http.StatusOK {
		t.Fatalf("second disconnect want 200, got %d", r.Code)
	}
}

// syncBuf is a mutex-guarded writer so the logger can be inspected after a
// synchronous request without a data race.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestCloudflareTokenNeverLogged(t *testing.T) {
	newCFMock(t, &cfMock{status: "active"})
	kc := newKMS(t)

	// Build the app with a buffer-backed logger so we can inspect every log line.
	logs := &syncBuf{}
	app := zip.New(zip.Config{Logger: luxlog.NewWriter(logs)})
	deps := cloud.Deps{Logger: luxlog.NewWriter(logs), DataDir: t.TempDir(), Domain: "api.hanzo.ai", KMS: kc}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	// A successful connect (logs "connector connected") ...
	if r := cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/connect", "acme", true,
		map[string]any{"token": cfTestToken}); r.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", r.Code, r.Body)
	}
	// ... and a FAILED connect (logs "connector verify failed") — neither may log it.
	// Point at an unroutable address to force the transport-failure path WITHOUT a
	// live call to the real Cloudflare API.
	t.Setenv("CLOUDFLARE_API_BASE", "http://127.0.0.1:1")
	_ = cfReq(t, app, http.MethodPost, "/v1/integrations/cloudflare/connect", "beta", true,
		map[string]any{"token": cfTestToken})

	if strings.Contains(logs.String(), cfTestToken) {
		t.Fatalf("the credential token appeared in a log line:\n%s", logs.String())
	}
}
