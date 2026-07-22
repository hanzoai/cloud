package integrations

// connectors_test.go proves the per-USER connector plane (/v1/connectors)
// against the REAL store + REAL KMS (newKMS), with a scriptable in-package
// `fake` provider so the engine paths (device flow, throttle, single-flight
// refresh, cap, custody) are exercised with ZERO network:
//   - identity: every route requires a validated principal (exactly 403);
//     email-form and UUID-form users both pass verbatim;
//   - plane split: user-scoped providers are invisible/404 on /v1/integrations
//     and org providers 404 on /v1/connectors;
//   - device flow: pending → connected, server-side poll throttle, per-grant
//     single-flight, terminal outcomes burn the grant;
//   - refresh: skew-gated, single-flight with adopt-after-lock, deterministic
//     reseal (refresh first);
//   - custody: verify-before-store, (org,user) isolation, KMS-down fail-closed,
//     combined-path cap → 400, secrets never logged.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// Both user-id shapes the validUser rule admits verbatim (clients/link parity).
const (
	userUUID  = "7f3d9c2a-4b1e-4f60-9a2d-1c8e5b7a9d3f"
	userEmail = "z@zoo.ngo"
)

// ── scriptable fake provider (no HTTP) ─────────────────────────────────────────

// Swappable behavior vars; each test sets what it needs via reset(t). Package
// tests are serial (shared vars + t.Setenv) — no t.Parallel.
var (
	fakeStart   func(context.Context) (*DeviceStart, error)
	fakePoll    func(context.Context, Grant) (*DevicePoll, error)
	fakeVerify  func(context.Context, VerifyInput) (*ExchangeResult, error)
	fakeAdopt   func(context.Context, Bundle) (*ExchangeResult, error)
	fakeRefresh func(context.Context, string) (*ExchangeResult, error)
)

func init() {
	// NO Configured/Creds — registering proves the Mount scope-coherence path
	// accepts a fully capable user-scoped provider.
	register(&Provider{
		ID: "fake", Name: "Fake", Category: "Test", Scope: userScope,
		Secrets: []string{accessSecret, refreshSecret},
		Device: &Device{
			Start: func(ctx context.Context) (*DeviceStart, error) { return fakeStart(ctx) },
			Poll:  func(ctx context.Context, g Grant) (*DevicePoll, error) { return fakePoll(ctx, g) },
		},
		Verify:  func(ctx context.Context, in VerifyInput) (*ExchangeResult, error) { return fakeVerify(ctx, in) },
		Adopt:   func(ctx context.Context, b Bundle) (*ExchangeResult, error) { return fakeAdopt(ctx, b) },
		Refresh: func(ctx context.Context, r string) (*ExchangeResult, error) { return fakeRefresh(ctx, r) },
	})
}

// reset arms every fake func with a failing default (t.Errorf, goroutine-safe —
// handlers run off the test goroutine) so an unscripted provider call is a test
// failure, and restores the nil state on cleanup.
func reset(t *testing.T) {
	t.Helper()
	unscripted := func(name string) error {
		t.Errorf("fake provider %s called unscripted", name)
		return errors.New("unscripted " + name)
	}
	fakeStart = func(context.Context) (*DeviceStart, error) { return nil, unscripted("Start") }
	fakePoll = func(context.Context, Grant) (*DevicePoll, error) { return nil, unscripted("Poll") }
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) { return nil, unscripted("Verify") }
	fakeAdopt = func(context.Context, Bundle) (*ExchangeResult, error) { return nil, unscripted("Adopt") }
	fakeRefresh = func(context.Context, string) (*ExchangeResult, error) { return nil, unscripted("Refresh") }
	t.Cleanup(func() {
		fakeStart, fakePoll, fakeVerify, fakeAdopt, fakeRefresh = nil, nil, nil, nil, nil
	})
}

// ── request/route helpers ──────────────────────────────────────────────────────

func devStartPath(provider string) string { return "/v1/connectors/" + provider + "/device" }
func devPollPath(provider, flow string) string {
	return "/v1/connectors/" + provider + "/device/" + flow + "/poll"
}
func credPath(provider string) string { return "/v1/connectors/" + provider + "/credential" }
func tokenPath(id string) string      { return "/v1/connectors/" + id + "/token" }
func refreshPath(id string) string    { return "/v1/connectors/" + id + "/refresh" }
func connPath(id string) string       { return "/v1/connectors/" + id }

// as issues a request with an EXPLICIT user identity (req() derives u-<org>).
// Empty org/user sends no identity headers — the 403 leg.
func as(t *testing.T, app *zip.App, method, path, org, user string, body any) httpResult {
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
	}
	if user != "" {
		rq.Header.Set("X-User-Id", user)
	}
	// Generous timeout: fiber's default Test timeout is 1 s with FailOnTimeout,
	// and the single-flight tests intentionally hold a provider call (100-500 ms)
	// behind the flight lock — CI load must not turn that into a flake.
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return httpResult{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: buf.Bytes()}
}

func asOK(t *testing.T, app *zip.App, method, path, org, user string, body any) httpResult {
	t.Helper()
	res := as(t, app, method, path, org, user, body)
	if res.Code != http.StatusOK {
		t.Fatalf("%s %s want 200, got %d (%s)", method, path, res.Code, res.Body)
	}
	return res
}

// userHas reads the user KMS namespace directly through the production open path.
func userHas(t *testing.T, kc *kms.Client, org, user, provider, label, name string) ([]byte, error) {
	t.Helper()
	return kc.Get("/orgs/"+org+"/users/"+user+"/connectors/"+provider+"/"+label, name, kmsEnv)
}

// newLogApp mounts on a buffer-backed logger so tests can prove secrets never
// reach a log line (TestCloudflareTokenNeverLogged pattern).
func newLogApp(t *testing.T, kc *kms.Client) (*zip.App, *syncBuf) {
	t.Helper()
	logs := &syncBuf{}
	app := zip.New(zip.Config{Logger: luxlog.NewWriter(logs)})
	deps := cloud.Deps{Logger: luxlog.NewWriter(logs), DataDir: t.TempDir(), Domain: "api.hanzo.ai"}
	if kc != nil {
		deps.KMS = kc
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app, logs
}

func decode[T any](t *testing.T, res httpResult) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(res.Body, &v); err != nil {
		t.Fatalf("decode %T: %v (%s)", v, err, res.Body)
	}
	return v
}

// Wire shapes (the published /v1/connectors contract).
type startResp struct {
	Flow      string `json:"flow"`
	UserCode  string `json:"userCode"`
	VerifyURL string `json:"verifyUrl"`
	Interval  int64  `json:"interval"`
	ExpiresAt string `json:"expiresAt"`
}

type pollResp struct {
	Status    string    `json:"status"`
	Interval  int64     `json:"interval"`
	Connector *connView `json:"connector"`
}

type credResp struct {
	Connected bool      `json:"connected"`
	Connector *connView `json:"connector"`
}

type refreshResp struct {
	Refreshed bool      `json:"refreshed"`
	Connector *connView `json:"connector"`
}

type tokenResp struct {
	Token     string `json:"token"`
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	ExpiresAt string `json:"expiresAt"`
}

type listResp struct {
	Connectors []connView `json:"connectors"`
}

type cpListResp struct {
	Providers []connectorProviderView `json:"providers"`
}

func rewind(t *testing.T, flow string) {
	t.Helper()
	if err := mounted.State.store.TouchGrant(context.Background(), flow, 0); err != nil {
		t.Fatalf("rewind throttle: %v", err)
	}
}

// ── tests ──────────────────────────────────────────────────────────────────────

func TestConnectorsRequireIdentity(t *testing.T) {
	reset(t)
	app := newApp(t, newKMS(t))

	// Missing identity is EXACTLY 403 on every route: principal.Org fails at
	// Validated before validUser (or any body/param validation) can run.
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/connectors"},
		{http.MethodGet, "/v1/connectors/providers"},
		{http.MethodGet, tokenPath("fake:default")},
		{http.MethodPost, devStartPath("fake")},
		{http.MethodPost, devPollPath("fake", "deadbeef")},
		{http.MethodPost, credPath("fake")},
		{http.MethodPost, refreshPath("fake:default")},
		{http.MethodDelete, connPath("fake:default")},
	}
	for _, r := range routes {
		if res := as(t, app, r.method, r.path, "", "", nil); res.Code != http.StatusForbidden {
			t.Fatalf("%s %s without identity want exactly 403, got %d (%s)", r.method, r.path, res.Code, res.Body)
		}
	}

	// An email-form user is a valid identity everywhere (validUser admits it
	// verbatim) — the full surface answers 200, never 403/400.
	fakeStart = func(context.Context) (*DeviceStart, error) {
		return &DeviceStart{Code: "dev-secret", UserCode: "ABCD-1234", VerifyURL: "https://fake.test/device", Interval: 30, ExpiresAt: time.Now().Add(15 * time.Minute).Unix()}, nil
	}
	fakePoll = func(context.Context, Grant) (*DevicePoll, error) {
		return &DevicePoll{Status: pollPending}, nil
	}
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT", refreshSecret: "RT"}}, nil
	}
	fakeRefresh = func(context.Context, string) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT2", refreshSecret: "RT2"}, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
	}
	steps := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/connectors", nil},
		{http.MethodGet, "/v1/connectors/providers", nil},
		{http.MethodPost, credPath("fake"), map[string]any{"token": "tok"}},
		{http.MethodGet, tokenPath("fake:default"), nil},
		{http.MethodPost, refreshPath("fake:default"), nil},
	}
	for _, s := range steps {
		asOK(t, app, s.method, s.path, "acme", userEmail, s.body)
	}
	st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("fake"), "acme", userEmail, nil))
	asOK(t, app, http.MethodPost, devPollPath("fake", st.Flow), "acme", userEmail, nil)
	asOK(t, app, http.MethodDelete, connPath("fake:default"), "acme", userEmail, nil)
}

func TestConnectorPlanesDisjoint(t *testing.T) {
	app := newApp(t, newKMS(t))
	userIDs := []string{"fake", "openai", "anthropic", "github-copilot"}

	// User-scoped providers are invisible on the org list.
	var orgList struct {
		Providers []providerView `json:"providers"`
	}
	r := req(t, app, http.MethodGet, "/v1/integrations", "acme", nil)
	if err := json.Unmarshal(r.Body, &orgList); err != nil {
		t.Fatalf("org list: %v (%s)", err, r.Body)
	}
	for _, p := range orgList.Providers {
		if slices.Contains(userIDs, p.ID) {
			t.Fatalf("user-scoped provider %q leaked onto /v1/integrations", p.ID)
		}
	}
	// Every org-plane read AND write path 404s a user-scoped id — the write
	// paths (connect/verify/disconnect) are the dangerous ones.
	for _, id := range userIDs {
		for _, rt := range []struct{ method, path string }{
			{http.MethodGet, "/v1/integrations/" + id},
			{http.MethodPost, "/v1/integrations/" + id + "/connect"},
			{http.MethodPost, "/v1/integrations/" + id + "/verify"},
			{http.MethodPost, "/v1/integrations/" + id + "/disconnect"},
		} {
			if res := req(t, app, rt.method, rt.path, "acme", nil); res.Code != http.StatusNotFound {
				t.Fatalf("%s %s want 404, got %d (%s)", rt.method, rt.path, res.Code, res.Body)
			}
		}
	}
	// And an org provider is a 404 on the user plane.
	if res := as(t, app, http.MethodPost, credPath("cloudflare"), "acme", userUUID, map[string]any{"token": "x"}); res.Code != http.StatusNotFound {
		t.Fatalf("org provider on /v1/connectors want 404, got %d (%s)", res.Code, res.Body)
	}

	// The user provider cards carry capability-derived methods.
	pv := decode[cpListResp](t, asOK(t, app, http.MethodGet, "/v1/connectors/providers", "acme", userUUID, nil))
	got := map[string][]string{}
	for _, p := range pv.Providers {
		got[p.ID] = p.Methods
	}
	want := map[string][]string{
		"fake":           {"device", "oauth", "token"},
		"openai":         {"device", "oauth"},
		"anthropic":      {"token"},
		"github-copilot": {"device", "token"},
	}
	for id, methods := range want {
		if !slices.Equal(got[id], methods) {
			t.Fatalf("provider %s methods want %v, got %v", id, methods, got[id])
		}
	}
	// No org-scoped provider may surface on the user plane. Checked against the
	// registry's Scope directly (the real disjointness invariant), so adding a
	// user connector needs no edit here while an org provider leaking still fails.
	for id := range got {
		p, ok := registry[id]
		if !ok || p.Scope != userScope {
			t.Fatalf("non-user-scoped provider %q surfaced on /v1/connectors/providers", id)
		}
	}
}

func TestDeviceFlow(t *testing.T) {
	reset(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	fakeStart = func(context.Context) (*DeviceStart, error) {
		return &DeviceStart{Code: "dev-secret", UserCode: "ABCD-1234", VerifyURL: "https://fake.test/device", Interval: 1, ExpiresAt: time.Now().Add(15 * time.Minute).Unix()}, nil
	}
	res := asOK(t, app, http.MethodPost, devStartPath("fake"), "acme", userUUID, nil)
	st := decode[startResp](t, res)
	if st.Flow == "" || st.UserCode != "ABCD-1234" || st.VerifyURL != "https://fake.test/device" || st.Interval != 1 || st.ExpiresAt == "" {
		t.Fatalf("start response incomplete: %+v", st)
	}
	// The provider device code is secret-adjacent and NEVER leaves the server.
	if bytes.Contains(res.Body, []byte("dev-secret")) {
		t.Fatalf("device code leaked into the start response: %s", res.Body)
	}

	fakePoll = func(context.Context, Grant) (*DevicePoll, error) {
		return &DevicePoll{Status: pollPending}, nil
	}
	p1 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("fake", st.Flow), "acme", userUUID, nil))
	if p1.Status != "pending" || p1.Interval != 1 {
		t.Fatalf("first poll want pending/1, got %+v", p1)
	}

	rewind(t, st.Flow)
	var mu sync.Mutex
	var gotCode string
	fakePoll = func(_ context.Context, g Grant) (*DevicePoll, error) {
		mu.Lock()
		gotCode = g.Code
		mu.Unlock()
		return &DevicePoll{Status: pollDone, Result: &ExchangeResult{
			Tokens:       map[string]string{accessSecret: "AT1", refreshSecret: "RT1"},
			ExternalID:   "ext-1",
			AccountLabel: "Fake Acct",
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		}}, nil
	}
	p2 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("fake", st.Flow), "acme", userUUID, nil))
	if p2.Status != "connected" || p2.Connector == nil || p2.Connector.ID != "fake:default" {
		t.Fatalf("done poll want connected fake:default, got %+v", p2)
	}
	mu.Lock()
	if gotCode != "dev-secret" {
		t.Fatalf("provider poll must receive the stored device code, got %q", gotCode)
	}
	mu.Unlock()

	// The verified pair is sealed in the USER KMS namespace, values exact.
	if v, err := userHas(t, kc, "acme", userUUID, "fake", "default", accessSecret); err != nil || string(v) != "AT1" {
		t.Fatalf("access custody: %q err=%v", v, err)
	}
	if v, err := userHas(t, kc, "acme", userUUID, "fake", "default", refreshSecret); err != nil || string(v) != "RT1" {
		t.Fatalf("refresh custody: %q err=%v", v, err)
	}
	// The grant is burned by the terminal outcome.
	if res := as(t, app, http.MethodPost, devPollPath("fake", st.Flow), "acme", userUUID, nil); res.Code != http.StatusNotFound {
		t.Fatalf("poll after done want 404, got %d (%s)", res.Code, res.Body)
	}
}

func TestDevicePollThrottle(t *testing.T) {
	reset(t)
	app := newApp(t, newKMS(t))

	fakeStart = func(context.Context) (*DeviceStart, error) {
		return &DeviceStart{Code: "c", UserCode: "U-1", VerifyURL: "https://fake.test/device", Interval: 30, ExpiresAt: time.Now().Add(15 * time.Minute).Unix()}, nil
	}
	var polls atomic.Int64
	fakePoll = func(context.Context, Grant) (*DevicePoll, error) {
		polls.Add(1)
		return &DevicePoll{Status: pollPending}, nil
	}
	st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("fake"), "acme", userUUID, nil))

	p1 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("fake", st.Flow), "acme", userUUID, nil))
	if p1.Status != "pending" || polls.Load() != 1 {
		t.Fatalf("first poll want upstream pending, got %+v (polls=%d)", p1, polls.Load())
	}
	// Inside the cadence window the server answers pending WITHOUT an upstream
	// call (RFC-8628 server obligation — the client_ids are shared across tenants).
	p2 := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("fake", st.Flow), "acme", userUUID, nil))
	if p2.Status != "pending" || polls.Load() != 1 {
		t.Fatalf("throttled poll must not call the provider: %+v (polls=%d)", p2, polls.Load())
	}
}

func TestDevicePollConcurrent(t *testing.T) {
	reset(t)
	app := newApp(t, newKMS(t))

	fakeStart = func(context.Context) (*DeviceStart, error) {
		return &DeviceStart{Code: "c", UserCode: "U-1", VerifyURL: "https://fake.test/device", Interval: 1, ExpiresAt: time.Now().Add(15 * time.Minute).Unix()}, nil
	}
	var polls atomic.Int64
	fakePoll = func(context.Context, Grant) (*DevicePoll, error) {
		polls.Add(1)
		// Long enough that BOTH handlers read the live grant before the first
		// flight finalizes it — the second must reach the adopt-after-lock path.
		time.Sleep(500 * time.Millisecond)
		return &DevicePoll{Status: pollDone, Result: &ExchangeResult{
			Tokens: map[string]string{accessSecret: "CAT", refreshSecret: "CRT"},
		}}, nil
	}
	st := decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("fake"), "acme", userUUID, nil))

	// Two concurrent polls of ONE flow: the flight serializes them, the second
	// adopts the finalized connector — the device code is spent exactly once.
	results := make([]httpResult, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = as(t, app, http.MethodPost, devPollPath("fake", st.Flow), "acme", userUUID, nil)
		}(i)
	}
	wg.Wait()
	for i, res := range results {
		if res.Code != http.StatusOK {
			t.Fatalf("concurrent poll %d want 200, got %d (%s)", i, res.Code, res.Body)
		}
		if p := decode[pollResp](t, res); p.Status != "connected" {
			t.Fatalf("concurrent poll %d want connected, got %+v", i, p)
		}
	}
	if polls.Load() != 1 {
		t.Fatalf("device code must be spent exactly once, provider polled %d times", polls.Load())
	}
}

func TestDeviceTerminal(t *testing.T) {
	reset(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	start := func(expiresAt int64) string {
		t.Helper()
		fakeStart = func(context.Context) (*DeviceStart, error) {
			return &DeviceStart{Code: "c", UserCode: "U-1", VerifyURL: "https://fake.test/device", Interval: 1, ExpiresAt: expiresAt}, nil
		}
		return decode[startResp](t, asOK(t, app, http.MethodPost, devStartPath("fake"), "acme", userUUID, nil)).Flow
	}
	kmsEmpty := func() {
		t.Helper()
		if _, err := userHas(t, kc, "acme", userUUID, "fake", "default", accessSecret); err == nil {
			t.Fatal("terminal outcome must store nothing in KMS")
		}
	}

	for _, tc := range []struct{ provider, wire string }{
		{pollDenied, "denied"},
		{pollExpired, "expired"},
	} {
		flow := start(time.Now().Add(15 * time.Minute).Unix())
		fakePoll = func(context.Context, Grant) (*DevicePoll, error) {
			return &DevicePoll{Status: tc.provider}, nil
		}
		if p := decode[pollResp](t, asOK(t, app, http.MethodPost, devPollPath("fake", flow), "acme", userUUID, nil)); p.Status != tc.wire {
			t.Fatalf("terminal want %q, got %+v", tc.wire, p)
		}
		// Terminal outcomes DELETE the grant — the flow is unknown afterwards.
		if res := as(t, app, http.MethodPost, devPollPath("fake", flow), "acme", userUUID, nil); res.Code != http.StatusNotFound {
			t.Fatalf("poll after %s want 404, got %d", tc.wire, res.Code)
		}
		kmsEmpty()
	}

	// Read-time TTL: an expired grant is indistinguishable from an unknown one,
	// and the provider is never polled (reset default would fail the test).
	flow := start(time.Now().Add(-time.Minute).Unix())
	fakePoll = func(context.Context, Grant) (*DevicePoll, error) {
		t.Error("provider polled for an expired grant")
		return nil, errors.New("expired grant polled")
	}
	if res := as(t, app, http.MethodPost, devPollPath("fake", flow), "acme", userUUID, nil); res.Code != http.StatusNotFound {
		t.Fatalf("poll of expired grant want 404, got %d (%s)", res.Code, res.Body)
	}
	kmsEmpty()
}

func TestCredentialLabelAndID(t *testing.T) {
	reset(t)
	app := newApp(t, newKMS(t))
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT"}}, nil
	}

	// Explicit label rides into the connector id; empty label means "default".
	cr := decode[credResp](t, asOK(t, app, http.MethodPost, credPath("fake"), "acme", userEmail, map[string]any{"token": "tok", "label": "work"}))
	if !cr.Connected || cr.Connector == nil || cr.Connector.ID != "fake:work" {
		t.Fatalf("labeled connect want fake:work, got %+v", cr)
	}
	asOK(t, app, http.MethodPost, credPath("fake"), "acme", userEmail, map[string]any{"token": "tok"})
	list := decode[listResp](t, asOK(t, app, http.MethodGet, "/v1/connectors", "acme", userEmail, nil))
	ids := make([]string, 0, len(list.Connectors))
	for _, c := range list.Connectors {
		ids = append(ids, c.ID)
	}
	for _, want := range []string{"fake:work", "fake:default"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("list must carry %s, got %v", want, ids)
		}
	}

	// Bad labels are refused before any provider call.
	for _, label := range []string{"bad/label", strings.Repeat("l", 65), ".."} {
		if res := as(t, app, http.MethodPost, credPath("fake"), "acme", userEmail, map[string]any{"token": "tok", "label": label}); res.Code != http.StatusBadRequest {
			t.Fatalf("label %q want 400, got %d (%s)", label, res.Code, res.Body)
		}
	}
	// Malformed connector ids are 400 on every id-taking route.
	for _, id := range []string{"noseparator", ":x", "x:"} {
		for _, rt := range []struct{ method, path string }{
			{http.MethodGet, tokenPath(id)},
			{http.MethodPost, refreshPath(id)},
			{http.MethodDelete, connPath(id)},
		} {
			if res := as(t, app, rt.method, rt.path, "acme", userEmail, nil); res.Code != http.StatusBadRequest {
				t.Fatalf("%s %s want 400, got %d (%s)", rt.method, rt.path, res.Code, res.Body)
			}
		}
	}
}

func TestConnectorCap(t *testing.T) {
	reset(t)
	app := newApp(t, newKMS(t))
	var verifies atomic.Int64
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		verifies.Add(1)
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT"}}, nil
	}

	for i := range maxConnectors {
		asOK(t, app, http.MethodPost, credPath("fake"), "acme", userUUID, map[string]any{"token": "tok", "label": "l" + strings.Repeat("x", i)})
	}
	if verifies.Load() != maxConnectors {
		t.Fatalf("want %d verifies, got %d", maxConnectors, verifies.Load())
	}
	// A NEW label past the cap is refused BEFORE the provider is called.
	res := as(t, app, http.MethodPost, credPath("fake"), "acme", userUUID, map[string]any{"token": "tok", "label": "overflow"})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body, []byte("too many connectors")) {
		t.Fatalf("cap want 400 too-many, got %d (%s)", res.Code, res.Body)
	}
	if verifies.Load() != maxConnectors {
		t.Fatalf("cap must precede provider work: %d verifies", verifies.Load())
	}
	// Reconnecting an EXISTING label is always allowed (device flows / re-auth
	// must never dead-end at the cap).
	asOK(t, app, http.MethodPost, credPath("fake"), "acme", userUUID, map[string]any{"token": "tok", "label": "l"})
	if verifies.Load() != maxConnectors+1 {
		t.Fatalf("reconnect must reach the provider: %d verifies", verifies.Load())
	}
}

func TestCredentialVerifyFailStoresNothing(t *testing.T) {
	reset(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	nothing := func() {
		t.Helper()
		if _, err := userHas(t, kc, "acme", userEmail, "fake", "default", accessSecret); err == nil {
			t.Fatal("verify-before-store violated: a rejected credential was sealed")
		}
		list := decode[listResp](t, asOK(t, app, http.MethodGet, "/v1/connectors", "acme", userEmail, nil))
		if len(list.Connectors) != 0 {
			t.Fatalf("verify-before-store violated: a rejected credential created a row: %+v", list.Connectors)
		}
	}

	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return nil, errors.New("provider rejected the token")
	}
	res := as(t, app, http.MethodPost, credPath("fake"), "acme", userEmail, map[string]any{"token": "bad"})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body, []byte("credential verification failed")) {
		t.Fatalf("failed verify want 400, got %d (%s)", res.Code, res.Body)
	}
	nothing()

	fakeAdopt = func(context.Context, Bundle) (*ExchangeResult, error) {
		return nil, errors.New("provider rejected the bundle")
	}
	if res := as(t, app, http.MethodPost, credPath("fake"), "acme", userEmail, map[string]any{"oauth": map[string]any{"access": "a", "refresh": "r"}}); res.Code != http.StatusBadRequest {
		t.Fatalf("failed adopt want 400, got %d (%s)", res.Code, res.Body)
	}
	nothing()

	// No oauth bundle + empty token never reaches a provider (reset defaults
	// would fail the test if it did).
	reset(t)
	if res := as(t, app, http.MethodPost, credPath("fake"), "acme", userEmail, map[string]any{}); res.Code != http.StatusBadRequest {
		t.Fatalf("empty intake want 400, got %d (%s)", res.Code, res.Body)
	}
	nothing()
}

func TestRefreshSingleFlight(t *testing.T) {
	reset(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	// Seed a connector whose access token dies INSIDE the refreshSkew window.
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{
			Tokens:    map[string]string{accessSecret: "A1", refreshSecret: "R1"},
			ExpiresAt: time.Now().Add(30 * time.Second).Unix(),
		}, nil
	}
	asOK(t, app, http.MethodPost, credPath("fake"), "acme", userUUID, map[string]any{"token": "tok"})

	var refreshes atomic.Int64
	fakeRefresh = func(_ context.Context, r string) (*ExchangeResult, error) {
		refreshes.Add(1)
		if r != "R1" {
			t.Errorf("refresh must use the custodied token, got %q", r)
		}
		time.Sleep(100 * time.Millisecond)
		return &ExchangeResult{
			Tokens:    map[string]string{accessSecret: "A2", refreshSecret: "R2"},
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}, nil
	}
	// Two concurrent token reads: ONE provider refresh; the second flight adopts
	// the rotation (a second refresh call would spend the rotated token).
	results := make([]httpResult, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = as(t, app, http.MethodGet, tokenPath("fake:default"), "acme", userUUID, nil)
		}(i)
	}
	wg.Wait()
	for i, res := range results {
		if res.Code != http.StatusOK {
			t.Fatalf("concurrent token %d want 200, got %d (%s)", i, res.Code, res.Body)
		}
		if tk := decode[tokenResp](t, res); tk.Token != "A2" {
			t.Fatalf("concurrent token %d want rotated A2, got %+v", i, tk)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refresh must single-flight: %d provider calls", refreshes.Load())
	}
	// The rotation was RESEALED (refresh-first deterministic order).
	if v, err := userHas(t, kc, "acme", userUUID, "fake", "default", refreshSecret); err != nil || string(v) != "R2" {
		t.Fatalf("rotated refresh custody: %q err=%v", v, err)
	}
}

func TestRefreshSkew(t *testing.T) {
	reset(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{
			Tokens:    map[string]string{accessSecret: "A1", refreshSecret: "R1"},
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}, nil
	}
	asOK(t, app, http.MethodPost, credPath("fake"), "acme", userUUID, map[string]any{"token": "tok"})

	var refreshes atomic.Int64
	fakeRefresh = func(context.Context, string) (*ExchangeResult, error) {
		refreshes.Add(1)
		return &ExchangeResult{
			Tokens:    map[string]string{accessSecret: "A2", refreshSecret: "R2"},
			ExpiresAt: time.Now().Add(2 * time.Hour).Unix(),
		}, nil
	}
	// Outside the skew window a token read never refreshes.
	if tk := decode[tokenResp](t, asOK(t, app, http.MethodGet, tokenPath("fake:default"), "acme", userUUID, nil)); tk.Token != "A1" {
		t.Fatalf("fresh token read want stored A1, got %+v", tk)
	}
	if refreshes.Load() != 0 {
		t.Fatalf("fresh token must not refresh: %d calls", refreshes.Load())
	}
	// POST /refresh forces a rotation regardless of expiry.
	rr := decode[refreshResp](t, asOK(t, app, http.MethodPost, refreshPath("fake:default"), "acme", userUUID, nil))
	if !rr.Refreshed || rr.Connector == nil || refreshes.Load() != 1 {
		t.Fatalf("force refresh want one rotation, got %+v (calls=%d)", rr, refreshes.Load())
	}
	exp, err := time.Parse(time.RFC3339, rr.Connector.ExpiresAt)
	if err != nil || time.Until(exp) < 90*time.Minute {
		t.Fatalf("refresh must advance expiresAt: %q err=%v", rr.Connector.ExpiresAt, err)
	}
}

func TestTokenIsolation(t *testing.T) {
	reset(t)
	kc := newKMS(t)
	app := newApp(t, kc)
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT", refreshSecret: "RT"}}, nil
	}
	asOK(t, app, http.MethodPost, credPath("fake"), "acme", userEmail, map[string]any{"token": "tok"})

	// Another user of the SAME org and the same user in ANOTHER org both see
	// "no row" — 404, never a token.
	if res := as(t, app, http.MethodGet, tokenPath("fake:default"), "acme", userUUID, nil); res.Code != http.StatusNotFound {
		t.Fatalf("same-org other-user token want 404, got %d (%s)", res.Code, res.Body)
	}
	if res := as(t, app, http.MethodGet, tokenPath("fake:default"), "globex", userEmail, nil); res.Code != http.StatusNotFound {
		t.Fatalf("other-org token want 404, got %d (%s)", res.Code, res.Body)
	}
	if _, err := userHas(t, kc, "acme", userUUID, "fake", "default", accessSecret); err == nil {
		t.Fatal("another user's KMS namespace must be empty")
	}
	if _, err := userHas(t, kc, "globex", userEmail, "fake", "default", accessSecret); err == nil {
		t.Fatal("another org's KMS namespace must be empty")
	}
	// The owner still reads its own token.
	if tk := decode[tokenResp](t, asOK(t, app, http.MethodGet, tokenPath("fake:default"), "acme", userEmail, nil)); tk.Token != "AT" {
		t.Fatalf("owner token want AT, got %+v", tk)
	}
}

func TestDropIdempotent(t *testing.T) {
	reset(t)
	kc := newKMS(t)
	app := newApp(t, kc)
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT", refreshSecret: "RT"}}, nil
	}
	asOK(t, app, http.MethodPost, credPath("fake"), "acme", userUUID, map[string]any{"token": "tok"})

	for i := range 2 {
		res := asOK(t, app, http.MethodDelete, connPath("fake:default"), "acme", userUUID, nil)
		var out struct {
			Disconnected bool `json:"disconnected"`
		}
		if err := json.Unmarshal(res.Body, &out); err != nil || !out.Disconnected {
			t.Fatalf("drop %d want disconnected:true, got %s", i, res.Body)
		}
		if _, err := userHas(t, kc, "acme", userUUID, "fake", "default", accessSecret); err == nil {
			t.Fatal("drop must delete the custodied secrets")
		}
	}
}

func TestKMSNotReady(t *testing.T) {
	reset(t)
	app := newApp(t, nil)
	// token/refresh check custody readiness AFTER row lookup — seed a row
	// directly so the 503 (not a 404) is what the caller sees.
	if err := mounted.State.store.UpsertConnector(context.Background(), Connector{
		Org: "acme", User: userUUID, Provider: "fake", Label: "default",
	}); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	for _, rt := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, devStartPath("fake"), nil},
		{http.MethodPost, credPath("fake"), map[string]any{"token": "tok"}},
		{http.MethodGet, tokenPath("fake:default"), nil},
		{http.MethodPost, refreshPath("fake:default"), nil},
	} {
		if res := as(t, app, rt.method, rt.path, "acme", userUUID, rt.body); res.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s with KMS down want 503, got %d (%s)", rt.method, rt.path, res.Code, res.Body)
		}
	}
}

func TestUserPathCap(t *testing.T) {
	reset(t)
	app := newApp(t, newKMS(t))
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT"}}, nil
	}
	// Org (63), user (128) and label (64) are each individually valid, but the
	// COMBINED KMS path exceeds the 253-byte cap — a 400, never a 503.
	org := strings.Repeat("o", 63)
	user := strings.Repeat("u", 128)
	label := strings.Repeat("l", 64)
	res := as(t, app, http.MethodPost, credPath("fake"), org, user, map[string]any{"token": "tok", "label": label})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body, []byte("too long")) {
		t.Fatalf("oversized custody path want 400 too-long, got %d (%s)", res.Code, res.Body)
	}
}

func TestRefreshFailureNotLogged(t *testing.T) {
	reset(t)
	const (
		secretAccess  = "vault-access-SECRET-A1"
		secretRefresh = "vault-refresh-SECRET-R1"
	)
	kc := newKMS(t)
	app, logs := newLogApp(t, kc)

	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{
			Tokens:    map[string]string{accessSecret: secretAccess, refreshSecret: secretRefresh},
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}, nil
	}
	asOK(t, app, http.MethodPost, credPath("fake"), "acme", userUUID, map[string]any{"token": "tok"})

	fakeRefresh = func(context.Context, string) (*ExchangeResult, error) {
		return nil, errors.New("upstream refused the rotation")
	}
	res := as(t, app, http.MethodPost, refreshPath("fake:default"), "acme", userUUID, nil)
	if res.Code != http.StatusBadGateway {
		t.Fatalf("failed refresh want 502, got %d (%s)", res.Code, res.Body)
	}
	for _, secret := range []string{secretAccess, secretRefresh} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("a custodied token appeared in a log line:\n%s", logs.String())
		}
		if bytes.Contains(res.Body, []byte(secret)) {
			t.Fatalf("a custodied token appeared in the error response: %s", res.Body)
		}
	}
}
