package cloud

// Tests for the zip-native billing gate. They drive real requests through the
// zip/fiber stack (app.Fiber().Test) against a fake commerce billing server, so
// the whole path is exercised end-to-end: BillingGate -> metering.Authorize over
// HTTP -> handler -> metering.Record. No mocks of the metering client itself —
// the fake commerce server controls every outcome via the balance it returns.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/zap-proto/zip"
)

// fakeCommerce answers the metering client's balance + usage calls. The balance
// status/body it returns drives Authorize's outcome; usage POSTs are counted so
// tests can assert Record fired (or did not).
type fakeCommerce struct {
	balanceStatus int    // HTTP status for GET /v1/billing/balance (0 => 200).
	balanceBody   string // JSON body for the balance reply.

	mu         sync.Mutex
	usageCount int32 // atomic: number of POST /v1/billing/usage calls.
	usageBody  []byte
}

func (f *fakeCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		status := f.balanceStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, f.balanceBody)
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.usageCount, 1)
		f.mu.Lock()
		f.usageBody, _ = io.ReadAll(r.Body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"transactionId":"tx_1","user":"hanzo/alice","amount":1,"currency":"usd","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeCommerce) usages() int32 { return atomic.LoadInt32(&f.usageCount) }

// newGateApp wires a minimal zip app with the billing gate in front of a single
// handler that flips `handlerRan` and returns 200. price forces a non-zero
// charge so the gate engages (DefaultPrice is tested separately).
func newGateApp(t *testing.T, m *metering.Client, handlerRan *atomic.Bool) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(BillingGate(m, func(c *zip.Ctx) int64 { return 5 }))
	app.Post("/v1/agent/run", func(c *zip.Ctx) error {
		handlerRan.Store(true)
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	return app
}

func mustClient(t *testing.T, baseURL string, failOpen bool) *metering.Client {
	t.Helper()
	c, err := metering.New(metering.Config{
		BaseURL:  baseURL,
		Token:    "svc-token",
		Org:      "hanzo",
		FailOpen: failOpen,
	})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return c
}

func doReq(t *testing.T, app *zip.App) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/run", nil)
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Id", "alice")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	return resp
}

// Authorize allows (balance positive) + price>0 → handler runs, 200, and a usage
// record is written to commerce.
func TestBillingGate_AllowsAndRecords(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":5000}`}
	srv := fc.server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL, false), &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run on an authorized request")
	}

	// Record is async (go m.Record). Poll briefly for the usage POST.
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("usage records = %d, want 1 (Record must fire after success)", fc.usages())
	}
}

// Authorize denies with ErrInsufficientBalance (available <= 0) → 402, handler
// NOT called, no usage recorded.
func TestBillingGate_InsufficientBalance402(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":0}`}
	srv := fc.server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL, false), &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", resp.StatusCode)
	}
	if ran.Load() {
		t.Fatal("handler ran despite insufficient balance — c.Next() must NOT be called")
	}
	if got := fc.usages(); got != 0 {
		t.Fatalf("usage records = %d, want 0 (denied request must not bill)", got)
	}
	assertErrorCode(t, resp, "insufficient_balance")
}

// Commerce unreachable/5xx + fail-closed (default) → 503, handler NOT called.
func TestBillingGate_BalanceUnknownFailClosed503(t *testing.T) {
	fc := &fakeCommerce{balanceStatus: http.StatusInternalServerError, balanceBody: `boom`}
	srv := fc.server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL, false /* fail-closed */), &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if ran.Load() {
		t.Fatal("handler ran on fail-closed balance-unknown — must be denied")
	}
	assertErrorCode(t, resp, "balance_unavailable")
}

// Commerce unreachable/5xx + fail-open → handler runs, 200 (availability over
// billing). Authorize returns nil in fail-open mode on a connectivity error.
func TestBillingGate_BalanceUnknownFailOpenPasses(t *testing.T) {
	fc := &fakeCommerce{balanceStatus: http.StatusInternalServerError, balanceBody: `boom`}
	srv := fc.server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL, true /* fail-open */), &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open allows on balance-unknown)", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run in fail-open mode")
	}
}

// An unconfigured metering client (no commerce URL) makes the gate a no-op: the
// handler always runs, nothing is billed.
func TestBillingGate_NoopWhenUnconfigured(t *testing.T) {
	m, err := metering.New(metering.Config{}) // no BaseURL => not Enabled().
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	if m.Enabled() {
		t.Fatal("client with empty BaseURL must not be Enabled()")
	}

	var ran atomic.Bool
	app := newGateApp(t, m, &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no-op gate)", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run with an unconfigured gate")
	}
}

// A nil metering client must also be a safe no-op (defensive: deps.Metering
// should never be nil, but the gate must not panic if it is).
func TestBillingGate_NilClientIsNoop(t *testing.T) {
	var ran atomic.Bool
	app := newGateApp(t, nil, &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (nil client no-op)", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run with a nil gate")
	}
}

// DefaultPrice must return 0 for self-metered and health paths (no gating, no
// double-billing) and a positive charge only for the generic agent edge.
func TestDefaultPrice(t *testing.T) {
	cases := []struct {
		path     string
		wantZero bool
		why      string
	}{
		{"/v1/ai/chat/completions", true, "ai self-meters LLM token costs — must not double-bill"},
		{"/v1/ai/embeddings", true, "ai subsystem self-meters"},
		{"/v1/commerce/billing/usage", true, "commerce bills its own usage"},
		{"/v1/o11y/ingest", true, "telemetry not user-billable at edge"},
		{"/v1/mcp/tools/call", true, "mcp meters per-tool downstream"},
		{"/health", true, "liveness probe"},
		{"/healthz", true, "liveness probe"},
		{"/v1/iam/health", true, "subsystem health suffix"},
		{"/v1/base/health", true, "subsystem health suffix"},
		{"/v1/agent/run", false, "legacy singular agent edge has no finer meter"},
		{"/v1/agents/list", true, "agents subsystem self-meters per-run fee — must not double-bill"},
		{"/v1/agents/x/run", true, "agent run self-metered by the agents subsystem"},
		{"/v1/unknown/thing", true, "unpriced path defaults to 0 (opt-in metering)"},
	}
	for _, tc := range cases {
		got := priceForPath(t, tc.path)
		if tc.wantZero && got != 0 {
			t.Errorf("DefaultPrice(%q) = %d, want 0 (%s)", tc.path, got, tc.why)
		}
		if !tc.wantZero && got <= 0 {
			t.Errorf("DefaultPrice(%q) = %d, want >0 (%s)", tc.path, got, tc.why)
		}
	}
}

// priceForPath routes a real request at path p through a one-shot handler that
// evaluates DefaultPrice against the genuine zip.Ctx and captures the result.
// The price is read INSIDE the handler (never after Test returns) because Fiber
// recycles its context once the handler completes.
func priceForPath(t *testing.T, p string) int64 {
	t.Helper()
	var got int64
	done := make(chan struct{})
	app := zip.New(zip.Config{})
	app.Use(func(c *zip.Ctx) error {
		got = DefaultPrice(c)
		close(done)
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	req := httptest.NewRequest(http.MethodGet, p, nil)
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("Test(%q): %v", p, err)
	}
	<-done
	return got
}

func waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func assertErrorCode(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// Cheap substring assert avoids coupling to map ordering in the JSON.
	if !containsSub(string(body), `"code":"`+want+`"`) {
		t.Errorf("body %q missing error code %q", string(body), want)
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
