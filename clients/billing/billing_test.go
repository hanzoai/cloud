package billing

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeCommerce stands in for the commerce billing S2S surface. It records the
// Authorization header, the trusted X-Org-Id selector, and the exact query it
// received (so a test can prove the subject is pinned to the CALLER's own org and
// a client-forged subject/org never reaches commerce), and answers a canned body
// so a test can prove the handler passes commerce's body + status through verbatim.
type fakeCommerce struct {
	mu        sync.Mutex
	gotAuth   string
	gotOrg    string
	gotPath   string
	gotMethod string
	gotQuery  url.Values
	gotBody   []byte
	// canned response
	status int
	body   string
}

func (f *fakeCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.gotAuth = r.Header.Get("Authorization")
		f.gotOrg = r.Header.Get("X-Org-Id")
		f.gotPath = r.URL.Path
		f.gotMethod = r.Method
		f.gotQuery = r.URL.Query()
		f.gotBody = reqBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/usage", h)
	mux.HandleFunc("/v1/billing/balance", h)
	mux.HandleFunc("/v1/billing/gpu-eligibility", h)
	mux.HandleFunc("/v1/billing/gpu-charge", h)
	mux.HandleFunc("/v1/billing/portal/payment-methods", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mountApp mounts the billing surface against the fake commerce at base, with the
// service token wired (unless token is ""). base "" leaves commerce unconfigured.
func mountApp(t *testing.T, base, token string) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_COMMERCE_HTTP_URL", base)
	t.Setenv("COMMERCE_SERVICE_TOKEN", token)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// call drives a request. A non-empty user injects a VALIDATED principal (X-User-Id,
// which the gateway sets ONLY from a verified credential) with org as X-Org-Id.
func call(t *testing.T, app *zip.App, method, path, user, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
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

func TestUsage_ScopedToCallerOrg_PassesThroughVerbatim(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"user":"maxpower","count":1,"usage":[{"transactionId":"t1","amount":42,"metadata":{"model":"gpt-4o-mini"},"createdAt":"2026-07-01T00:00:00Z"}]}`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, http.MethodGet, "/v1/billing/usage", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("usage: want 200, got %d (%s)", code, body)
	}
	// The body is no longer byte-verbatim: usage() enriches each row's metadata with
	// the canonical product/agent dims. Assert the row survives enrichment intact.
	var got struct {
		Usage []struct {
			TransactionID string         `json:"transactionId"`
			Metadata      map[string]any `json:"metadata"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("usage body decode: %v (%s)", err, body)
	}
	if len(got.Usage) != 1 || got.Usage[0].TransactionID != "t1" || got.Usage[0].Metadata["model"] != "gpt-4o-mini" {
		t.Fatalf("usage row not preserved through enrichment: %s", body)
	}
	// commerce saw the caller's OWN org as both the S2S selector and the pinned subject.
	if f.gotOrg != "maxpower" {
		t.Fatalf("commerce X-Org-Id: want maxpower, got %q", f.gotOrg)
	}
	if got := f.gotQuery.Get("user"); got != "maxpower" {
		t.Fatalf("commerce subject: want user=maxpower, got %q", got)
	}
	if f.gotAuth != "Bearer svc-token" {
		t.Fatalf("commerce auth: want service token, got %q", f.gotAuth)
	}
	if f.gotPath != "/v1/billing/usage" {
		t.Fatalf("commerce path: want /v1/billing/usage, got %q", f.gotPath)
	}
}

func TestUsage_ClientCannotWidenScope(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"usage":[]}`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	// A malicious client forges every subject key + a foreign org in the query.
	code, _ := call(t, app, http.MethodGet,
		"/v1/billing/usage?user=victim&userId=victim&customerId=victim&org=other",
		"maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	// EVERY subject key is PINNED to the caller's own org (overwriting the forged values),
	// so no endpoint is left unfiltered; only the non-subject `org` selector is dropped.
	for _, k := range []string{"user", "userId", "customerId"} {
		if got := f.gotQuery.Get(k); got != "maxpower" {
			t.Fatalf("forged %q must be overwritten with the caller org: got %q", k, got)
		}
	}
	if f.gotQuery.Has("org") {
		t.Fatalf("client-forged org must NOT be forwarded to commerce (got %q)", f.gotQuery.Get("org"))
	}
	if f.gotOrg != "maxpower" {
		t.Fatalf("X-Org-Id must be the caller's own org, got %q", f.gotOrg)
	}
}

func TestUsage_PassthroughStartEnd(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"usage":[]}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, _ := call(t, app, http.MethodGet, "/v1/billing/usage?start=2026-07-01&end=2026-07-03", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	if f.gotQuery.Get("start") != "2026-07-01" || f.gotQuery.Get("end") != "2026-07-03" {
		t.Fatalf("start/end must pass through: got start=%q end=%q", f.gotQuery.Get("start"), f.gotQuery.Get("end"))
	}
	if f.gotQuery.Get("user") != "maxpower" {
		t.Fatalf("subject still pinned: got %q", f.gotQuery.Get("user"))
	}
}

func TestBalance_ScopedAndCurrency(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"balance":2046200,"holds":0,"available":2046200}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, body := call(t, app, http.MethodGet, "/v1/billing/balance?currency=usd", "maxpower/dave", "maxpower")
	if code != 200 || string(body) != f.body {
		t.Fatalf("balance: want 200 verbatim, got %d (%s)", code, body)
	}
	if f.gotPath != "/v1/billing/balance" || f.gotOrg != "maxpower" || f.gotQuery.Get("user") != "maxpower" {
		t.Fatalf("balance scope: path=%q org=%q user=%q", f.gotPath, f.gotOrg, f.gotQuery.Get("user"))
	}
	if f.gotQuery.Get("currency") != "usd" {
		t.Fatalf("currency must pass through: got %q", f.gotQuery.Get("currency"))
	}
}

func TestUsage_NoValidatedPrincipal_401_NeverTouchesCommerce(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"usage":[]}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	// A forged X-Org-Id with NO validated principal (no X-User-Id) must be refused
	// as 401 — never admin-gated, never served — and commerce must never be reached.
	code, _ := call(t, app, http.MethodGet, "/v1/billing/usage", "", "victim")
	if code != http.StatusUnauthorized {
		t.Fatalf("no principal: want 401, got %d", code)
	}
	if f.gotOrg != "" {
		t.Fatalf("commerce must NOT be reached on the unauthenticated path, saw org=%q", f.gotOrg)
	}
}

func TestUsage_CommerceUnconfigured_501(t *testing.T) {
	app := mountApp(t, "", "") // no commerce base/token
	code, _ := call(t, app, http.MethodGet, "/v1/billing/usage", "maxpower/dave", "maxpower")
	if code != http.StatusNotImplemented {
		t.Fatalf("unconfigured: want 501, got %d", code)
	}
}

func TestUsage_CommerceStatusForwarded(t *testing.T) {
	// A non-2xx from commerce (e.g. a real 500) is forwarded verbatim, not masked.
	f := &fakeCommerce{status: http.StatusInternalServerError, body: `{"error":"boom"}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, body := call(t, app, http.MethodGet, "/v1/billing/usage", "maxpower/dave", "maxpower")
	if code != http.StatusInternalServerError || !strings.Contains(string(body), "boom") {
		t.Fatalf("commerce status must pass through: got %d (%s)", code, body)
	}
}

// callBody drives a request with a JSON body (for POST gpu-charge).
func callBody(t *testing.T, app *zip.App, method, path, user, org, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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

func TestGPUEligibility_ScopedAndPassthrough(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"user":"maxpower","eligible":false,"reason":"card_required","prepaidAvailable":0,"cardOnFile":false,"requiredCents":24000}`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, http.MethodGet,
		"/v1/billing/gpu-eligibility?amountCents=24000&minPrepaidCents=24000&currency=usd",
		"maxpower/dave", "maxpower")
	if code != 200 || string(body) != f.body {
		t.Fatalf("gpu-eligibility: want 200 verbatim, got %d (%s)", code, body)
	}
	if f.gotPath != "/v1/billing/gpu-eligibility" {
		t.Fatalf("commerce path: want /v1/billing/gpu-eligibility, got %q", f.gotPath)
	}
	// The launch gate's amount + 24h floor + currency reach commerce; the subject is the
	// caller's OWN org on BOTH the S2S selector and the pinned query subject.
	if f.gotQuery.Get("amountCents") != "24000" || f.gotQuery.Get("minPrepaidCents") != "24000" || f.gotQuery.Get("currency") != "usd" {
		t.Fatalf("launch params must pass through: got %v", f.gotQuery)
	}
	if f.gotOrg != "maxpower" || f.gotQuery.Get("user") != "maxpower" {
		t.Fatalf("gpu-eligibility scope: org=%q user=%q", f.gotOrg, f.gotQuery.Get("user"))
	}
	if f.gotAuth != "Bearer svc-token" {
		t.Fatalf("commerce auth: want service token, got %q", f.gotAuth)
	}
}

func TestGPUEligibility_ClientCannotWidenScope(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"eligible":true,"reason":"ok"}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	// A forged subject in the query must be overwritten with the caller's own org.
	code, _ := call(t, app, http.MethodGet,
		"/v1/billing/gpu-eligibility?user=victim&amountCents=100&org=other",
		"maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	if f.gotQuery.Get("user") != "maxpower" {
		t.Fatalf("forged user must be overwritten with the caller org: got %q", f.gotQuery.Get("user"))
	}
	if f.gotQuery.Has("org") {
		t.Fatalf("client-forged org must NOT reach commerce, got %q", f.gotQuery.Get("org"))
	}
	if f.gotOrg != "maxpower" {
		t.Fatalf("X-Org-Id must be the caller's own org, got %q", f.gotOrg)
	}
}

func TestPaymentMethods_ProxiesPortal_Scoped(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `[{"id":"pm_1","brand":"visa","last4":"4242","isDefault":true}]`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	// The console requests the same-origin /v1/billing/payment-methods (mounted on cloud);
	// cloud proxies it to commerce's admin-group PORTAL read.
	code, body := call(t, app, http.MethodGet, "/v1/billing/payment-methods", "maxpower/dave", "maxpower")
	if code != 200 || string(body) != f.body {
		t.Fatalf("payment-methods: want 200 verbatim, got %d (%s)", code, body)
	}
	if f.gotPath != "/v1/billing/portal/payment-methods" {
		t.Fatalf("commerce path: want /v1/billing/portal/payment-methods, got %q", f.gotPath)
	}
	// PortalPaymentMethods 400s without a customerId; the proxy pins it (and user/userId)
	// to the caller's own org, so the list is scoped to the caller's cards — never widenable.
	if f.gotOrg != "maxpower" || f.gotQuery.Get("customerId") != "maxpower" || f.gotQuery.Get("user") != "maxpower" {
		t.Fatalf("payment-methods scope: org=%q customerId=%q user=%q", f.gotOrg, f.gotQuery.Get("customerId"), f.gotQuery.Get("user"))
	}
}

func TestGPUCharge_PinsSubjectInBody_ForwardsStatus(t *testing.T) {
	// commerce answers 402 card_required — the money verdict must be forwarded verbatim.
	f := &fakeCommerce{status: 402, body: `{"error":{"code":"card_required","message":"Add a card on file before launching a GPU"}}`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	// A malicious client tries to charge ANOTHER tenant's wallet via the body subject.
	code, body := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge",
		"maxpower/dave", "maxpower",
		`{"user":"victim","userId":"victim","customerId":"victim","amountCents":24000,"currency":"usd","tag":"gpu-h100"}`)
	if code != 402 || string(body) != f.body {
		t.Fatalf("gpu-charge: want 402 verbatim, got %d (%s)", code, body)
	}
	if f.gotMethod != http.MethodPost || f.gotPath != "/v1/billing/gpu-charge" {
		t.Fatalf("commerce call: want POST /v1/billing/gpu-charge, got %s %q", f.gotMethod, f.gotPath)
	}
	if f.gotOrg != "maxpower" {
		t.Fatalf("X-Org-Id must be the caller's own org, got %q", f.gotOrg)
	}
	// The body subject is PINNED to the caller's org; the charge params survive verbatim.
	var got map[string]any
	if err := json.Unmarshal(f.gotBody, &got); err != nil {
		t.Fatalf("commerce body not JSON: %v (%s)", err, f.gotBody)
	}
	for _, k := range []string{"user", "userId", "customerId"} {
		if got[k] != "maxpower" {
			t.Fatalf("body %q must be pinned to caller org, got %v (full: %s)", k, got[k], f.gotBody)
		}
	}
	if got["amountCents"] != float64(24000) || got["currency"] != "usd" || got["tag"] != "gpu-h100" {
		t.Fatalf("charge params must survive: got %v", got)
	}
}

func TestGPUCharge_NoPrincipal_401_NeverTouchesCommerce(t *testing.T) {
	f := &fakeCommerce{status: 201, body: `{}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	// A forged X-Org-Id with NO validated principal must be refused 401, commerce untouched.
	code, _ := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "", "victim", `{"amountCents":100}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("no principal: want 401, got %d", code)
	}
	if f.gotOrg != "" {
		t.Fatalf("commerce must NOT be reached on the unauthenticated path, saw org=%q", f.gotOrg)
	}
}

func TestGPUCharge_Unconfigured_501(t *testing.T) {
	app := mountApp(t, "", "") // no commerce base/token
	code, _ := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "maxpower/dave", "maxpower", `{"amountCents":100}`)
	if code != http.StatusNotImplemented {
		t.Fatalf("unconfigured: want 501, got %d", code)
	}
}

func TestPinSubjectBody(t *testing.T) {
	// Forged subject overwritten; charge params preserved.
	out := pinSubjectBody([]byte(`{"user":"victim","amountCents":24000,"tag":"gpu"}`), "maxpower")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if m["user"] != "maxpower" || m["userId"] != "maxpower" || m["customerId"] != "maxpower" {
		t.Fatalf("subject not pinned: %v", m)
	}
	if m["amountCents"] != float64(24000) || m["tag"] != "gpu" {
		t.Fatalf("params not preserved: %v", m)
	}
	// Empty body → a fresh object carrying ONLY the pinned subject (never omitted).
	out = pinSubjectBody(nil, "maxpower")
	m = nil
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("empty-body result not JSON: %v", err)
	}
	if m["user"] != "maxpower" {
		t.Fatalf("empty body must still pin the subject: %v", m)
	}
}
