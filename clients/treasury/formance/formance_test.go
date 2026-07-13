package formance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/clients/treasury/ledger"
)

// memPolicy is an in-memory ledger.PolicyStore for the adapter tests.
type memPolicy struct{ p ledger.Policy }

func (m *memPolicy) Policy(context.Context) (ledger.Policy, error) { return m.p, nil }
func (m *memPolicy) SetPolicy(_ context.Context, p ledger.Policy) error {
	m.p = p
	return nil
}

// fakeFormance is a minimal in-memory Formance Ledger v2 server: it enforces the two
// behaviours the adapter depends on — a duplicate `reference` is 409 CONFLICT
// (idempotency) and a transfer whose non-`world` source is underfunded is 400
// INSUFFICIENT_FUND (the overdraw guard). Enough to prove the adapter maps Formance's
// contract correctly, without a live Formance+Postgres.
type fakeFormance struct {
	mu       sync.Mutex
	balances map[string]int64
	refs     map[string]bool
	seq      int
}

func newFake() *fakeFormance {
	return &fakeFormance{balances: map[string]int64{}, refs: map[string]bool{}}
}

func (f *fakeFormance) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transactions"):
			f.postTransaction(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/aggregate/balances"):
			addr := r.URL.Query().Get("address")
			writeJSON(w, 200, map[string]any{"data": map[string]int64{"USD/2": f.balances[addr]}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/transactions"):
			writeJSON(w, 200, map[string]any{"cursor": map[string]any{"data": []any{}}})
		default:
			w.WriteHeader(404)
		}
	})
	return mux
}

func (f *fakeFormance) postTransaction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reference string `json:"reference"`
		Postings  []struct {
			Amount      int64  `json:"amount"`
			Source      string `json:"source"`
			Destination string `json:"destination"`
		} `json:"postings"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Reference != "" && f.refs[body.Reference] {
		writeJSON(w, http.StatusConflict, map[string]any{"errorCode": "CONFLICT"})
		return
	}
	p := body.Postings[0]
	if p.Source != "world" && f.balances[p.Source] < p.Amount {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "INSUFFICIENT_FUND"})
		return
	}
	f.balances[p.Source] -= p.Amount
	f.balances[p.Destination] += p.Amount
	if body.Reference != "" {
		f.refs[body.Reference] = true
	}
	f.seq++
	writeJSON(w, 200, map[string]any{"data": map[string]any{"id": f.seq, "reference": body.Reference}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newBackend(t *testing.T) (*Backend, *fakeFormance) {
	t.Helper()
	f := newFake()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return New(srv.URL, "hanzo-treasury", "", &memPolicy{}), f
}

// Accrue posts world→fund:reserve and is idempotent by the period reference.
func TestFormance_Accrue(t *testing.T) {
	ctx := context.Background()
	b, _ := newBackend(t)
	if _, err := b.SetPolicy(ctx, 1000, 1); err != nil { // 10%
		t.Fatal(err)
	}
	e, created, err := b.Accrue(ctx, "2026-07", 200_000, 10) // $2000 → $200 into fund
	if err != nil || !created {
		t.Fatalf("accrue: created=%v err=%v", created, err)
	}
	if e.Amount.Cents() != 20_000 {
		t.Fatalf("accrual = %d, want 20000", e.Amount.Cents())
	}
	if bal, _ := b.ReserveCents(ctx); bal != 20_000 {
		t.Fatalf("reserve = %d, want 20000", bal)
	}
	// Same period → Formance 409 CONFLICT → idempotent (created=false, no double).
	_, created2, err := b.Accrue(ctx, "2026-07", 200_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("re-accrue same period must be idempotent")
	}
	if bal, _ := b.ReserveCents(ctx); bal != 20_000 {
		t.Fatalf("reserve double-accrued: %d", bal)
	}
}

// DebitReserve is backed when the fund covers it and NOT backed (Formance
// INSUFFICIENT_FUND) when it does not — the overdraw guard, enforced by Formance.
func TestFormance_DebitReserve_GuardAndIdempotency(t *testing.T) {
	ctx := context.Background()
	b, _ := newBackend(t)
	_, _ = b.SetPolicy(ctx, 10000, 1) // 100%
	if _, _, err := b.Seed(ctx, "seed:1", "cap", 5_000, 1); err != nil {
		t.Fatal(err)
	}
	// Backed.
	_, backed, created, err := b.DebitReserve(ctx, "referral", "pay:1", "bonus", 3_000, 2)
	if err != nil || !backed || !created {
		t.Fatalf("debit backed: backed=%v created=%v err=%v", backed, created, err)
	}
	if bal, _ := b.ReserveCents(ctx); bal != 2_000 {
		t.Fatalf("reserve = %d, want 2000", bal)
	}
	// Overdraw → Formance 400 INSUFFICIENT_FUND → not backed, nothing posted.
	_, backed2, _, err := b.DebitReserve(ctx, "author", "pay:2", "royalty", 5_000, 3)
	if err != nil {
		t.Fatalf("overdraw should map to not-backed, not error: %v", err)
	}
	if backed2 {
		t.Fatal("overdraw must NOT be backed")
	}
	if bal, _ := b.ReserveCents(ctx); bal != 2_000 {
		t.Fatalf("blocked payout altered fund: %d", bal)
	}
	// Idempotent replay of the backed ref → 409 CONFLICT → backed, not created.
	_, backed3, created3, err := b.DebitReserve(ctx, "referral", "pay:1", "bonus", 3_000, 4)
	if err != nil || !backed3 || created3 {
		t.Fatalf("replay: backed=%v created=%v err=%v", backed3, created3, err)
	}
	if bal, _ := b.ReserveCents(ctx); bal != 2_000 {
		t.Fatalf("replay double-charged fund: %d", bal)
	}
}

// Snapshot rolls up the reserve + per-program payouts the same way the native
// backend does.
func TestFormance_Snapshot(t *testing.T) {
	ctx := context.Background()
	b, _ := newBackend(t)
	_, _ = b.SetPolicy(ctx, 5000, 1)
	_, _, _ = b.Seed(ctx, "seed:1", "cap", 10_000, 1)
	_, _, _, _ = b.DebitReserve(ctx, "referral", "p1", "", 4_000, 2)

	rep, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ReserveCents != 6_000 || rep.PaidCents != 4_000 || rep.AccruedCents != 10_000 {
		t.Fatalf("snapshot wrong: %+v", rep)
	}
	if rep.ByProgramCents["referral"] != 4_000 {
		t.Fatalf("by-program: %+v", rep.ByProgramCents)
	}
	if rep.Policy.RevenueShareBps != 5000 {
		t.Fatalf("policy bps = %d", rep.Policy.RevenueShareBps)
	}
}
