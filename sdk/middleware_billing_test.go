package cloud

// Tests for the zip-native billing gate. They drive real requests through the
// zip/fiber stack (app.Fiber().Test) against a fake commerce billing server, so
// the whole path is exercised end-to-end: BillingGate -> metering.Authorize over
// HTTP -> handler -> metering.Record over the plane. No mocks of the metering client
// itself — the fake commerce peer controls every outcome via the balance it returns.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

// fakeCommerce is the money peer: it answers the metering client's balance READ over
// HTTP and receives its usage DEBIT over the plane, which is the split the client makes.
// The balance status/body drives Authorize's outcome; debits are counted so tests can
// assert Record fired (or did not).
type fakeCommerce struct {
	balanceStatus int    // HTTP status for GET /v1/billing/balance (0 => 200).
	balanceBody   string // JSON body for the balance reply.

	debits planeDebits
}

func (f *fakeCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	f.debits.serve(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		status := f.balanceStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, f.balanceBody)
	})
	// NO /v1/billing/usage route, and its absence is the assertion: a debit that still
	// went over HTTP would 404 here rather than quietly counting.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeCommerce) usages() int32 { return f.debits.count() }

func mustClient(t *testing.T, baseURL string) *meter.Client {
	t.Helper()
	c, err := meter.New(meter.Config{BaseURL: baseURL})
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	return resp
}

// TestDefaultPrice proves the gate's price comes from the surface's DECLARATION and
// from nowhere else — that Metered and Free both charge nothing at the edge, that a
// health probe charges nothing even under a surface that IS priced, that a declared
// price is actually charged, and that a path no surface owns is Undeclared (charged
// nothing here, and refused by TestPriceDeclared before it can ship).
//
// The declarations are a FIXTURE, deliberately not the real Wire(): this asserts the
// mechanism (declaration → gate) and must therefore contain a positive price, which no
// real surface has. apps.TestEdgeChargesNothingToday is the other half — it asserts the
// REAL declarations, and it is where a change in what customers pay shows up.
func TestDefaultPrice(t *testing.T) {
	index(t, &Config{Enable: []string{"ai", "agent", "agents", "commerce", "o11y", "iam", "base", "probe"}},
		Plugin{Name: "ai", Price: Metered, Prefixes: []string{"/v1/ai", "/v1/tool"}},
		Plugin{Name: "agent", Price: Metered},
		Plugin{Name: "agents", Price: Metered},
		Plugin{Name: "commerce", Price: Free},
		Plugin{Name: "o11y", Price: Free},
		Plugin{Name: "iam", Price: Free},
		Plugin{Name: "base", Price: Free},
		Plugin{Name: "probe", Price: 7, Prefixes: []string{"/v1/probe"}},
	)
	cases := []struct {
		path string
		want int64
		why  string
	}{
		{"/v1/ai/chat/completions", 0, "ai declares Metered — the model plane's token meter owns the charge, so an edge charge double-bills"},
		{"/v1/ai/embeddings", 0, "same surface, same declaration"},
		{"/v1/tool/call", 0, "ai owns /v1/tool too; per-tool dispatch meters downstream"},
		{"/v1/commerce/billing/usage", 0, "commerce declares Free — it IS the pay path"},
		{"/v1/o11y/ingest", 0, "o11y declares Free — telemetry ingest is not user-billable"},
		{"/health", 0, "liveness probe"},
		{"/healthz", 0, "liveness probe"},
		{"/v1/iam/health", 0, "subsystem health suffix"},
		{"/v1/base/health", 0, "subsystem health suffix"},
		{"/v1/probe/health", 0, "a health probe is free even under a PRICED surface — a probe that 402s hides whether the process is up"},
		{"/v1/agent/chat", 0, "the orchestrator's round bills through its in-process completion"},
		{"/v1/agent/chat/presets", 0, "same surface — a read on it is free for the same reason"},
		{"/v1/agent/x/run", 0, "the agents subsystem meters its own per-run fee"},
		{"/v1/probe/thing", 7, "a declared price IS charged — without this case every zero above is unfalsifiable"},
		{"/v1/nobody/owns-this", 0, "no surface owns it: Undeclared charges nothing HERE and fails TestPriceDeclared instead"},
	}
	for _, tc := range cases {
		if got := priceForPath(t, tc.path); got != tc.want {
			t.Errorf("DefaultPrice(%q) = %d, want %d (%s)", tc.path, got, tc.want, tc.why)
		}
	}

	// A READ of the very same priced path costs nothing. This is the rule that made a
	// positive declaration possible at all: DefaultPrice priced by PATH alone, so a
	// surface declared at 7¢ charged 7¢ to list its own contents, and the only safe
	// declaration was Free — which is what all 95 surfaces chose. It is asserted
	// against the SAME path the row above charges for, so the two can never be
	// reconciled by weakening one of them.
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if got := priceForMethod(t, m, "/v1/probe/thing"); got != 0 {
			t.Errorf("DefaultPrice(%s /v1/probe/thing) = %d, want 0 — a read spends nothing, "+
				"so it costs nothing (Consumes)", m, got)
		}
	}
	// And the write still does, measured through the same helper — otherwise the
	// three zeros above are satisfied by a price that is broken for every method.
	if got := priceForMethod(t, http.MethodPost, "/v1/probe/thing"); got != 7 {
		t.Errorf("DefaultPrice(POST /v1/probe/thing) = %d, want 7", got)
	}
}

// priceForPath is priceForMethod on a WRITE. The table above asks what a surface
// COSTS, and only a consuming request costs anything (Consumes, price.go) — so a
// read would answer 0 for every row and make the whole table unfalsifiable.
func priceForPath(t *testing.T, p string) int64 {
	t.Helper()
	return priceForMethod(t, http.MethodPost, p)
}

// priceForMethod asks DefaultPrice what one call to method p costs.
//
// It used to route a real request through a one-shot handler and read the price
// inside it, because DefaultPrice took a *zip.Ctx and fiber recycles that context
// the moment the handler returns. A price is a fact about an operation, not about
// a request, so the whole apparatus — the app, the handler, the httptest request,
// the done channel guarding the recycle — is gone with the argument that needed it.
func priceForMethod(t *testing.T, method, p string) int64 {
	t.Helper()
	return DefaultPrice(method, p)
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
	if !strings.Contains(string(body), `"code":"`+want+`"`) {
		t.Errorf("body %q missing error code %q", string(body), want)
	}
}
