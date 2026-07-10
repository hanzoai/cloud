package functions

// Integration tests proving the per-org credit-drawdown gate is wired into the
// REAL invoke path via the ONE shared cloud.ResourceMeter: an unfunded org is
// refused 402 before any sandbox compute runs, a funded org runs and its OWN org
// ledger is debited (product "functions", unit "invoke"), a sandbox transport
// failure bills nothing (no billable compute), a free fee is un-gated, and an
// unconfigured commerce is a no-op. The metering client's DEFAULT org is "hanzo",
// so every "billed acme" assertion also proves the debit targets the CALLER org,
// never the default — multitenancy end-to-end through the handler.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// billServer is a minimal commerce double: it returns a fixed balance and records
// the X-Org-Id header + body of any usage debit (commerce reads X-Org-Id only).
type billServer struct {
	available int64

	mu        sync.Mutex
	usageOrg  string
	usageBody []byte
	usages    int32
}

func (b *billServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&b.usages, 1)
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.usageOrg, b.usageBody = r.Header.Get("X-Org-Id"), body
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"transactionId":"tx_1","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billServer) debits() int32 { return atomic.LoadInt32(&b.usages) }
func (b *billServer) lastDebit() (string, []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usageOrg, b.usageBody
}

// sandbox is a code-executor double speaking the LibreChat /exec contract. It
// counts invocations and returns a fixed stdout so a funded invoke succeeds.
type sandbox struct {
	calls int32
}

func (s *sandbox) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/exec", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.calls, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"stdout":"ok","stderr":""}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}
func (s *sandbox) ran() int32 { return atomic.LoadInt32(&s.calls) }

// newBilledSvc builds a functions service with a store, an exec client pointed at
// execUpstream (empty ⇒ unconfigured), and a metering client pointed at
// commerceURL (default org "hanzo"; empty ⇒ !Enabled()).
func newBilledSvc(t *testing.T, commerceURL, execUpstream string) *cloud.Service[state] {
	t.Helper()
	log := luxlog.New("module", "fnbilltest")
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Logger: log, Metering: m, Env: "mainnet"}, "functions"),
		State: state{
			stores: cloud.NewTenantStore(t.TempDir(), "functions", openStore),
			exec:   &execClient{upstream: execUpstream, apiKey: "k", http: &http.Client{}},
		},
	}
}

// seedFn inserts a ready function directly into the org's per-org store — the
// SAME file the invoke handler resolves through the shared cache, so it sees it.
func seedFn(t *testing.T, s *cloud.Service[state], org, name string) {
	t.Helper()
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("seed fn store: %v", err)
	}
	if _, err := store.Upsert(context.Background(), Function{
		Org: org, Name: name, Runtime: "python", Code: "print(1)", TimeoutSec: 30, MemoryLimit: "256Mi", Status: "ready",
	}); err != nil {
		t.Fatalf("seed fn: %v", err)
	}
}

// fireInvoke fires POST /v1/functions/:name/invoke for org through the real handler.
func fireInvoke(t *testing.T, s *cloud.Service[state], org, name string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/functions/:name/invoke", cloud.Handle(s, invoke))
	req := httptest.NewRequest("POST", "/v1/functions/"+name+"/invoke", bytes.NewReader([]byte(`{"input":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (tenant() gates on it)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test invoke: %v", err)
	}
	return resp
}

// Unfunded org → 402 insufficient_balance, sandbox NEVER called, nothing debited.
func TestInvoke_RefusesUnfundedOrg(t *testing.T) {
	sb := &sandbox{}
	bs := &billServer{available: 0}
	s := newBilledSvc(t, bs.start(t), sb.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusPaymentRequired {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 402", resp.StatusCode, body)
	}
	if sb.ran() != 0 {
		t.Fatalf("sandbox ran %d times for an unfunded org, want 0 (gate must precede compute)", sb.ran())
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a refused invoke, want 0", bs.debits())
	}
}

// Funded org → 200, sandbox runs, and the CALLER org (acme, not the client
// default hanzo) is debited once with product "functions" / unit "invoke".
func TestInvoke_AllowsAndDebitsCallerOrg(t *testing.T) {
	sb := &sandbox{}
	bs := &billServer{available: 100000}
	s := newBilledSvc(t, bs.start(t), sb.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if sb.ran() != 1 {
		t.Fatalf("sandbox ran %d times, want 1", sb.ran())
	}
	if !waitFor(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("debits = %d, want 1 (a successful invoke must bill)", bs.debits())
	}
	org, body := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller %q (never the default 'hanzo')", org, "acme")
	}
	var u struct {
		User     string `json:"user"`
		Amount   int64  `json:"amount"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	_ = json.Unmarshal(body, &u)
	if u.User != "acme" {
		t.Fatalf("debit user = %q, want caller org %q", u.User, "acme")
	}
	if u.Amount != cloud.DefaultResourceFeeCents {
		t.Fatalf("debit amount = %d, want default fee %d", u.Amount, cloud.DefaultResourceFeeCents)
	}
	if u.Provider != "functions" {
		t.Fatalf("debit provider = %q, want %q", u.Provider, "functions")
	}
	if u.Model != "invoke" {
		t.Fatalf("debit model = %q, want %q", u.Model, "invoke")
	}
}

// A sandbox transport failure (unreachable executor) is authorized but runs NO
// billable compute → nothing is debited (no free-usage, and no charge for work
// that never happened).
func TestInvoke_TransportFailureNotBilled(t *testing.T) {
	bs := &billServer{available: 100000}
	// execUpstream points at a dead address so run() returns a transport error.
	s := newBilledSvc(t, bs.start(t), "http://127.0.0.1:1")
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 502 (transport failure)", resp.StatusCode, body)
	}
	time.Sleep(50 * time.Millisecond) // give any (incorrect) async debit a chance
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a sandbox transport failure, want 0", bs.debits())
	}
}

// Free fee (0) is un-gated: even at zero balance the invoke runs and nothing is
// debited.
func TestInvoke_FreeFeeUngated(t *testing.T) {
	t.Setenv("CLOUD_FUNCTION_FEE_CENTS", "0")
	sb := &sandbox{}
	bs := &billServer{available: 0}
	s := newBilledSvc(t, bs.start(t), sb.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200 (free fee is un-gated)", resp.StatusCode, body)
	}
	if sb.ran() != 1 {
		t.Fatalf("sandbox ran %d times, want 1", sb.ran())
	}
	time.Sleep(50 * time.Millisecond)
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a free fee, want 0", bs.debits())
	}
}

// Billing unconfigured (no commerce URL) → the gate is a no-op: invoke works and
// nothing is billed.
func TestInvoke_BillingUnconfiguredNoop(t *testing.T) {
	sb := &sandbox{}
	s := newBilledSvc(t, "", sb.start(t)) // empty commerce URL ⇒ !Enabled()
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if sb.ran() != 1 {
		t.Fatalf("sandbox ran %d times, want 1", sb.ran())
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}
