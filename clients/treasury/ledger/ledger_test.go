package ledger

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// memStore is an in-memory ledger.Store used ONLY to prove the engine is
// store-agnostic: every domain rule below is exercised without SQLite, so the core
// is demonstrably liftable to hanzoai/finance on any backend. Tx holds a mutex so
// the balance guard is serialized exactly as the production single-connection
// SQLite adapter serializes it.
type memStore struct {
	mu      sync.Mutex
	entries []Entry
	byRef   map[string]Entry
	policy  Policy
}

func newMem() *memStore { return &memStore{byRef: map[string]Entry{}} }

func refKey(kind, program, ref string) string { return kind + "|" + program + "|" + ref }

func (m *memStore) balanceLocked(account string) int64 {
	var bal int64
	for _, e := range m.entries {
		for _, p := range e.Postings {
			if p.Account == account {
				bal += p.Amount
			}
		}
	}
	return bal
}

func (m *memStore) Balance(_ context.Context, account string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.balanceLocked(account), nil
}

func (m *memStore) BalancesWithPrefix(_ context.Context, prefix string) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for _, e := range m.entries {
		for _, p := range e.Postings {
			if strings.HasPrefix(p.Account, prefix) {
				out[p.Account] += p.Amount
			}
		}
	}
	return out, nil
}

func (m *memStore) Entries(_ context.Context, limit int) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, 0, len(m.entries))
	for i := len(m.entries) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.entries[i])
	}
	return out, nil
}

func (m *memStore) Policy(_ context.Context) (Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.policy, nil
}

func (m *memStore) SetPolicy(_ context.Context, p Policy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy = p
	return nil
}

func (m *memStore) Tx(_ context.Context, fn func(Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{m: m}
	if err := fn(tx); err != nil {
		return err // rollback: pending discarded
	}
	for _, e := range tx.pending {
		m.entries = append(m.entries, e)
		m.byRef[refKey(e.Kind, e.Program, e.Ref)] = e
	}
	return nil
}

type memTx struct {
	m       *memStore
	pending []Entry
}

func (t *memTx) EntryByRef(kind, program, ref string) (Entry, bool, error) {
	e, ok := t.m.byRef[refKey(kind, program, ref)]
	return e, ok, nil
}
func (t *memTx) Balance(account string) (int64, error) { return t.m.balanceLocked(account), nil }
func (t *memTx) Insert(e Entry, postings []Posting) error {
	e.Postings = postings
	t.pending = append(t.pending, e)
	return nil
}

// ── the double-entry invariant ───────────────────────────────────────────────

func TestValidateBalanced(t *testing.T) {
	if err := validateBalanced([]Posting{{"a", 100}, {"b", -100}}); err != nil {
		t.Fatalf("balanced entry rejected: %v", err)
	}
	if err := validateBalanced([]Posting{{"a", 100}, {"b", -99}}); err != ErrUnbalanced {
		t.Fatalf("unbalanced entry accepted, got %v", err)
	}
	if err := validateBalanced([]Posting{{"a", 0}}); err != ErrUnbalanced {
		t.Fatalf("single-leg entry accepted, got %v", err)
	}
}

// every entry the engine posts must itself balance to zero
func assertAllBalanced(t *testing.T, m *memStore) {
	t.Helper()
	for _, e := range m.entries {
		var sum int64
		for _, p := range e.Postings {
			sum += p.Amount
		}
		if sum != 0 {
			t.Fatalf("entry %s (%s) postings sum to %d, not 0", e.ID, e.Kind, sum)
		}
	}
}

// ── revenue-share accrual ────────────────────────────────────────────────────

func TestAccrue_RevenueShare(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	l := New(m)
	if _, err := l.SetPolicy(ctx, 500, 1); err != nil { // 5%
		t.Fatal(err)
	}
	e, created, err := l.Accrue(ctx, "2026-07", 100_000, 10) // $1000 revenue → $50 share
	if err != nil || !created {
		t.Fatalf("accrue: created=%v err=%v", created, err)
	}
	if e.AmountCents != 5_000 {
		t.Fatalf("share = %d, want 5000", e.AmountCents)
	}
	if bal, _ := l.ReserveCents(ctx); bal != 5_000 {
		t.Fatalf("reserve = %d, want 5000", bal)
	}
	if rev, _ := m.Balance(ctx, AccountRevenue); rev != -5_000 {
		t.Fatalf("revenue counter = %d, want -5000", rev)
	}
	assertAllBalanced(t, m)
}

func TestAccrue_IdempotentPerPeriod(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	l := New(m)
	_, _ = l.SetPolicy(ctx, 1000, 1) // 10%
	if _, c1, _ := l.Accrue(ctx, "2026-07", 100_000, 10); !c1 {
		t.Fatal("first accrue should create")
	}
	_, c2, err := l.Accrue(ctx, "2026-07", 100_000, 20) // same period, re-run
	if err != nil {
		t.Fatal(err)
	}
	if c2 {
		t.Fatal("second accrue for same period must be a no-op (idempotent)")
	}
	if bal, _ := l.ReserveCents(ctx); bal != 10_000 {
		t.Fatalf("reserve double-accrued: %d, want 10000", bal)
	}
}

func TestAccrue_ZeroShareNoop(t *testing.T) {
	ctx := context.Background()
	l := New(newMem()) // policy unset → 0%
	_, created, err := l.Accrue(ctx, "2026-07", 100_000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("0% policy must accrue nothing")
	}
}

// ── backed payout: the reserve guard ─────────────────────────────────────────

func fund(t *testing.T, l *Ledger, cents int64) {
	t.Helper()
	if _, _, err := l.Seed(context.Background(), "seed:test", "test capital", cents, 1); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestDebitReserve_BackedThenBlocked(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	l := New(m)
	fund(t, l, 5_000)

	// Backed: fund covers it.
	e, backed, created, err := l.DebitReserve(ctx, "referral", "ref:1", "referral bonus", 3_000, 10)
	if err != nil || !backed || !created {
		t.Fatalf("debit: backed=%v created=%v err=%v", backed, created, err)
	}
	if e.AmountCents != 3_000 {
		t.Fatalf("entry amount = %d", e.AmountCents)
	}
	if bal, _ := l.ReserveCents(ctx); bal != 2_000 {
		t.Fatalf("reserve after debit = %d, want 2000", bal)
	}
	if p, _ := m.Balance(ctx, PayoutAccount("referral")); p != 3_000 {
		t.Fatalf("payout:referral = %d, want 3000", p)
	}

	// Blocked: 5000 > remaining 2000 → nothing posted, fund intact.
	_, backed2, created2, err := l.DebitReserve(ctx, "author", "pay:99", "royalty", 5_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	if backed2 || created2 {
		t.Fatalf("insufficient reserve must NOT back the payout: backed=%v created=%v", backed2, created2)
	}
	if bal, _ := l.ReserveCents(ctx); bal != 2_000 {
		t.Fatalf("blocked payout altered the fund: %d, want 2000", bal)
	}
	assertAllBalanced(t, m)
}

func TestDebitReserve_AtMostOnce(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	l := New(m)
	fund(t, l, 10_000)

	_, b1, c1, err := l.DebitReserve(ctx, "affiliate", "pay:abc", "commission", 4_000, 10)
	if err != nil || !b1 || !c1 {
		t.Fatalf("first debit: %v %v %v", b1, c1, err)
	}
	// Same ref again (a retry) — must be backed but NOT create a second debit.
	_, b2, c2, err := l.DebitReserve(ctx, "affiliate", "pay:abc", "commission", 4_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !b2 || c2 {
		t.Fatalf("replay must be backed+not-created: backed=%v created=%v", b2, c2)
	}
	if bal, _ := l.ReserveCents(ctx); bal != 6_000 {
		t.Fatalf("fund charged twice for one ref: %d, want 6000", bal)
	}
}

func TestDebitReserve_NonPositive(t *testing.T) {
	l := New(newMem())
	if _, _, _, err := l.DebitReserve(context.Background(), "referral", "r", "m", 0, 1); err != ErrNonPositive {
		t.Fatalf("zero amount accepted: %v", err)
	}
}

// Concurrency: many debits racing a small fund can never overdraw it, and total
// paid never exceeds what was funded. Run with -race.
func TestDebitReserve_ConcurrentNoOverdraw(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	l := New(m)
	fund(t, l, 10_000) // only 10 payouts of 1000 can be backed

	const n = 50
	var wg sync.WaitGroup
	backedCount := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ref := "pay:" + string(rune('A'+i%26)) + itoa(i)
			_, backed, _, err := l.DebitReserve(ctx, "referral", ref, "race", 1_000, int64(i))
			if err != nil {
				t.Errorf("debit %d: %v", i, err)
				return
			}
			backedCount[i] = backed
		}(i)
	}
	wg.Wait()

	reserve, _ := l.ReserveCents(ctx)
	if reserve < 0 {
		t.Fatalf("FUND OVERDRAWN: reserve = %d", reserve)
	}
	paid := 0
	for _, b := range backedCount {
		if b {
			paid++
		}
	}
	if paid != 10 {
		t.Fatalf("backed %d payouts, want exactly 10 (fund was 10000/1000)", paid)
	}
	if reserve != 0 {
		t.Fatalf("reserve = %d, want 0 after 10 backed payouts", reserve)
	}
	assertAllBalanced(t, m)
}

// ── policy + snapshot ────────────────────────────────────────────────────────

func TestSetPolicy_Range(t *testing.T) {
	ctx := context.Background()
	l := New(newMem())
	if _, err := l.SetPolicy(ctx, -1, 1); err == nil {
		t.Fatal("negative bps accepted")
	}
	if _, err := l.SetPolicy(ctx, 10_001, 1); err == nil {
		t.Fatal(">100% bps accepted")
	}
	if _, err := l.SetPolicy(ctx, 10_000, 1); err != nil {
		t.Fatalf("100%% bps rejected: %v", err)
	}
}

func TestSnapshot_Reconciles(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	l := New(m)
	_, _ = l.SetPolicy(ctx, 2_000, 1)                                  // 20%
	_, _, _ = l.Accrue(ctx, "2026-07", 100_000, 10)                    // +20000 fund
	_, _, _, _ = l.DebitReserve(ctx, "referral", "r1", "", 5_000, 20)  // -5000
	_, _, _, _ = l.DebitReserve(ctx, "affiliate", "a1", "", 3_000, 30) // -3000

	rep, err := l.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.AccruedCents != 20_000 {
		t.Fatalf("accrued = %d, want 20000", rep.AccruedCents)
	}
	if rep.PaidCents != 8_000 {
		t.Fatalf("paid = %d, want 8000", rep.PaidCents)
	}
	if rep.ReserveCents != 12_000 {
		t.Fatalf("reserve = %d, want 12000", rep.ReserveCents)
	}
	// The reconciliation identity the whole design guarantees.
	if rep.ReserveCents != rep.AccruedCents-rep.PaidCents {
		t.Fatalf("reserve != accrued - paid: %d != %d - %d", rep.ReserveCents, rep.AccruedCents, rep.PaidCents)
	}
	if rep.ByProgramCents["referral"] != 5_000 || rep.ByProgramCents["affiliate"] != 3_000 {
		t.Fatalf("by-program wrong: %+v", rep.ByProgramCents)
	}
	if rep.Policy.RevenueShareBps != 2_000 {
		t.Fatalf("policy bps = %d", rep.Policy.RevenueShareBps)
	}
}

// itoa is a tiny stdlib-free int→string for the concurrency test refs.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
