package storage

// Integration tests proving the per-org credit-drawdown gate is wired into the
// s3 data-plane guard via the ONE shared cloud.ResourceMeter: an unfunded org is
// refused 402 before S3 is touched, a funded op debits the CALLER org (product
// "s3", unit "op") once, a handler failure bills nothing, a free fee is un-gated,
// and unconfigured commerce is a no-op. The metering client's DEFAULT org is
// "hanzo", so every "billed acme" assertion also proves the debit targets the
// CALLER org, never the default — multitenancy through the guard.
//
// The guard is tested directly against a handler stub (guard takes any
// zip.Handler): the S3 backend is never dialed, so success/failure of the wrapped
// handler is deterministic and the billing contract is isolated from a live
// SeaweedFS. tenant() requires a validated principal (X-User-Id), so requests
// carry it exactly as SanitizeIdentity would in prod.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/commerce/metering"
	"github.com/hanzoai/cloud/clients/s3admin"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// billServer is a minimal commerce double: a fixed balance + records the
// X-Org-Id header and body of any usage debit (commerce reads X-Org-Id only).
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

// newBilledSvc builds an s3 svc with S3 admin credentials present (so guard does
// not 503 on Configured()) and a metering client pointed at commerceURL (default
// org "hanzo"; empty ⇒ !Enabled()).
func newBilledSvc(t *testing.T, commerceURL string) *svc {
	t.Helper()
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	t.Setenv("S3_ADMIN_ENDPOINT", "127.0.0.1:1")
	log := luxlog.New("module", "s3billtest")
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return &svc{
		admin: s3admin.New(),
		bill:  cloud.NewResourceMeter(cloud.Deps{Logger: log, Metering: m, Env: "mainnet"}, "s3"),
	}
}

// callGuard runs a guarded handler stub for org through the real guard. The stub
// records whether it ran and returns hErr (nil ⇒ success path).
func callGuard(t *testing.T, s *svc, org string, hErr error) (status int, ran *int32) {
	t.Helper()
	var calls int32
	h := s.guard(func(c *zip.Ctx) error {
		atomic.AddInt32(&calls, 1)
		if hErr != nil {
			return hErr
		}
		return c.NoContent(http.StatusNoContent)
	})
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/s3/op", h)
	req := httptest.NewRequest("POST", "/v1/s3/op", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org) // validated principal (tenant() gates on X-User-Id)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test guard: %v", err)
	}
	return resp.StatusCode, &calls
}

// Unfunded org → 402 insufficient_balance, the wrapped handler NEVER runs (no S3
// touched), nothing is debited.
func TestGuard_RefusesUnfundedOrg(t *testing.T) {
	bs := &billServer{available: 0}
	s := newBilledSvc(t, bs.start(t))

	status, ran := callGuard(t, s, "acme", nil)
	if status != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", status)
	}
	if atomic.LoadInt32(ran) != 0 {
		t.Fatalf("handler ran %d times for an unfunded org, want 0 (gate must precede S3)", atomic.LoadInt32(ran))
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a refused op, want 0", bs.debits())
	}
}

// Funded org → the op runs and the CALLER org (acme, not the default hanzo) is
// debited once with product "s3" / unit "op".
func TestGuard_AllowsAndDebitsCallerOrg(t *testing.T) {
	bs := &billServer{available: 100000}
	s := newBilledSvc(t, bs.start(t))

	status, ran := callGuard(t, s, "acme", nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	if atomic.LoadInt32(ran) != 1 {
		t.Fatalf("handler ran %d times, want 1", atomic.LoadInt32(ran))
	}
	if !waitFor(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("debits = %d, want 1 (a successful op must bill)", bs.debits())
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
	if u.Provider != "s3" {
		t.Fatalf("debit provider = %q, want %q", u.Provider, "s3")
	}
	if u.Model != "op" {
		t.Fatalf("debit model = %q, want %q", u.Model, "op")
	}
}

// A handler failure (e.g. the real S3 op errors) is authorized but NOT billed —
// mirrors the edge gate ("do not bill failed work").
func TestGuard_HandlerFailureNotBilled(t *testing.T) {
	bs := &billServer{available: 100000}
	s := newBilledSvc(t, bs.start(t))

	status, ran := callGuard(t, s, "acme", zip.Errorf(http.StatusBadGateway, "s3 down"))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if atomic.LoadInt32(ran) != 1 {
		t.Fatalf("handler ran %d times, want 1", atomic.LoadInt32(ran))
	}
	time.Sleep(50 * time.Millisecond) // give any (incorrect) async debit a chance
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a failed op, want 0", bs.debits())
	}
}

// A free fee (0) is un-gated: even at zero balance the op runs and nothing is
// debited.
func TestGuard_FreeFeeUngated(t *testing.T) {
	t.Setenv("CLOUD_S3_FEE_CENTS", "0")
	bs := &billServer{available: 0}
	s := newBilledSvc(t, bs.start(t))

	status, ran := callGuard(t, s, "acme", nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (free fee is un-gated)", status)
	}
	if atomic.LoadInt32(ran) != 1 {
		t.Fatalf("handler ran %d times, want 1", atomic.LoadInt32(ran))
	}
	time.Sleep(50 * time.Millisecond)
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a free fee, want 0", bs.debits())
	}
}

// Billing unconfigured (no commerce URL) → the gate is a no-op: the op runs and
// nothing is billed.
func TestGuard_BillingUnconfiguredNoop(t *testing.T) {
	s := newBilledSvc(t, "") // empty commerce URL ⇒ !Enabled()

	status, ran := callGuard(t, s, "acme", nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	if atomic.LoadInt32(ran) != 1 {
		t.Fatalf("handler ran %d times, want 1", atomic.LoadInt32(ran))
	}
}

// A request with no validated principal (no X-User-Id) is refused 403 before the
// gate — the tenant boundary precedes billing.
func TestGuard_NoPrincipalRefused(t *testing.T) {
	bs := &billServer{available: 100000}
	s := newBilledSvc(t, bs.start(t))

	status, ran := callGuard(t, s, "", nil)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	if atomic.LoadInt32(ran) != 0 {
		t.Fatalf("handler ran %d times without a principal, want 0", atomic.LoadInt32(ran))
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a refused (no-principal) op, want 0", bs.debits())
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
