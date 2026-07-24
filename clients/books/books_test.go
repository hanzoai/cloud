package books

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
)

// fakeSource is the test double for the commerce posting source: a fixed set of
// transactions per ledger (live vs sandbox), so the ingestion pipeline is exercised with
// no network and no commerce.
type fakeSource struct {
	live    []commerceTxn
	sandbox []commerceTxn
}

func (f *fakeSource) transactions(_ context.Context, _ string, sandbox bool) ([]commerceTxn, error) {
	if sandbox {
		return f.sandbox, nil
	}
	return f.live, nil
}

// closingOf returns an account's closing debit/credit columns from a trial balance (0,0
// if the account did not move).
func closingOf(tb TrialBalance, account string) (debit, credit int64) {
	for _, r := range tb.Rows {
		if r.Account == account {
			return r.ClosingDebit, r.ClosingCredit
		}
	}
	return 0, 0
}

func newBookStore(t *testing.T, subsystem string) *store {
	t.Helper()
	stores := cloud.NewOrgStore[*store](t.TempDir(), subsystem, openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	st, err := stores.For("acme", "")
	if err != nil {
		t.Fatalf("open books store: %v", err)
	}
	return st
}

// TestBooksRevenueRecognition feeds a deposit then a usage event through the ingestion
// pipeline (which posts via the Post() choke point) and asserts the two properties the
// revenue books MUST hold:
//
//	(a) the Trial Balance balances — total debit == total credit; and
//	(b) the Customer Wallet is a LIABILITY (credit) balance after a deposit, and DECREASES
//	    on usage while Usage revenue INCREASES (the revenue-recognition moment).
func TestBooksRevenueRecognition(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "txn-deposit-1", Type: "deposit", Amount: 10000, Currency: "usd", Notes: "top-up", CreatedAt: "2026-07-01T10:00:00Z"},
	}}

	// Phase 1: deposit only. The prepaid credit is a liability, NOT income yet.
	posted, err := ingestOrg(ctx, src, st, "acme", false)
	if err != nil {
		t.Fatalf("ingest deposit: %v", err)
	}
	if posted != 1 {
		t.Fatalf("deposit should post exactly 1 voucher, got %d", posted)
	}

	tb, err := trialBalance(ctx, st, "", "")
	if err != nil {
		t.Fatalf("trial balance after deposit: %v", err)
	}
	if !tb.Balanced {
		t.Fatalf("after deposit: NOT balanced: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	wd, wc := closingOf(tb, CustomerWallet)
	if wd != 0 || wc != 10000 {
		t.Fatalf("after deposit: Customer Wallet must be a $100.00 CREDIT (liability), got debit=%d credit=%d", wd, wc)
	}
	if _, revC := closingOf(tb, UsageRevenue); revC != 0 {
		t.Fatalf("after deposit: NO revenue must be recognized yet, got usage revenue credit=%d", revC)
	}
	sqD, _ := closingOf(tb, SquareClearing)
	if sqD != 10000 {
		t.Fatalf("after deposit: Square-clearing must be a $100.00 DEBIT (asset), got debit=%d", sqD)
	}

	// Phase 2: add a usage event. Deposit re-appears in the feed but must NOT re-post
	// (idempotency), and usage RECOGNIZES revenue by consuming the wallet.
	src.live = append(src.live, commerceTxn{
		ID: "txn-usage-1", Type: "withdraw", Amount: 3000, Currency: "usd", Tags: "ai:tokens", CreatedAt: "2026-07-02T12:00:00Z",
	})
	posted, err = ingestOrg(ctx, src, st, "acme", false)
	if err != nil {
		t.Fatalf("ingest usage: %v", err)
	}
	if posted != 1 {
		t.Fatalf("second ingest must post ONLY the new usage voucher (deposit idempotent), got %d", posted)
	}

	tb, err = trialBalance(ctx, st, "", "")
	if err != nil {
		t.Fatalf("trial balance after usage: %v", err)
	}
	if !tb.Balanced {
		t.Fatalf("after usage: NOT balanced: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	wd, wc = closingOf(tb, CustomerWallet)
	if wd != 0 || wc != 7000 {
		t.Fatalf("after usage: Customer Wallet credit must DECREASE to $70.00, got debit=%d credit=%d", wd, wc)
	}
	_, revC := closingOf(tb, UsageRevenue)
	if revC != 3000 {
		t.Fatalf("after usage: Usage revenue must INCREASE to $30.00 credit (recognition), got credit=%d", revC)
	}

	// Idempotency: a third ingest of the identical feed posts nothing and leaves the books
	// unchanged.
	posted, err = ingestOrg(ctx, src, st, "acme", false)
	if err != nil {
		t.Fatalf("third ingest: %v", err)
	}
	if posted != 0 {
		t.Fatalf("re-ingesting an unchanged feed must post 0 vouchers, got %d", posted)
	}
	tb2, _ := trialBalance(ctx, st, "", "")
	if tb2.TotalDebit != tb.TotalDebit || tb2.TotalCredit != tb.TotalCredit {
		t.Fatalf("idempotent re-ingest changed the books: %+v vs %+v", tb2, tb)
	}
}

// TestPostBalanceEnforced proves the choke point rejects an unbalanced voucher beyond the
// round-off allowance and accepts one within it (booking the difference to round-off).
func TestPostBalanceEnforced(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// A 100-cent imbalance is far beyond the round-off allowance → rejected, nothing written.
	_, err := st.post(ctx, Voucher{
		SourceKind: "test", SourceID: "bad-1", PostingAt: "2026-07-01T00:00:00Z",
		Legs: []Leg{{Account: SquareClearing, Debit: 10000}, {Account: CustomerWallet, Credit: 9900}},
	}, RoundOffAllowance)
	if err == nil {
		t.Fatalf("an unbalanced voucher (100c gap) MUST be rejected")
	}
	if rows, _ := st.listGL(ctx, 100); len(rows) != 0 {
		t.Fatalf("a rejected voucher must write NO gl rows, got %d", len(rows))
	}

	// A 1-cent gap is within the allowance → posted, with a round-off leg that balances it.
	posted, err := st.post(ctx, Voucher{
		SourceKind: "test", SourceID: "roundoff-1", PostingAt: "2026-07-01T00:00:00Z",
		Legs: []Leg{{Account: SquareClearing, Debit: 10000}, {Account: CustomerWallet, Credit: 9999}},
	}, RoundOffAllowance)
	if err != nil || !posted {
		t.Fatalf("a within-allowance voucher must post: posted=%v err=%v", posted, err)
	}
	tb, _ := trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("round-off must leave the books balanced: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	if rd, rc := closingOf(tb, RoundOff); rd != 0 || rc != 1 {
		t.Fatalf("the 1-cent difference must land on Round-off as a credit, got debit=%d credit=%d", rd, rc)
	}
}

// TestTrialBalanceDetectsImbalance proves the trial balance is a real DETECTOR, not a
// mirror of post()'s guarantee. It writes a lone $5.00 debit with NO offsetting credit
// straight into gl_entry — the exact shape post() structurally forbids — then asserts the
// report reports Balanced==false and surfaces the precise debit−credit gap. Only a direct
// write can exercise this; a ledger built through post() can never be unbalanced.
func TestTrialBalanceDetectsImbalance(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO voucher (source_kind, source_id, posting_at) VALUES ('test','imbalance-1','2026-07-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO gl_entry (voucher_id, posting_at, account, debit, credit, source_kind, source_id)
		 VALUES (1, '2026-07-01T00:00:00Z', ?, 500, 0, 'test', 'imbalance-1')`, SquareClearing); err != nil {
		t.Fatalf("seed gl_entry: %v", err)
	}

	tb, err := trialBalance(ctx, st, "", "")
	if err != nil {
		t.Fatalf("trial balance: %v", err)
	}
	if tb.Balanced {
		t.Fatalf("an unbalanced ledger MUST report Balanced=false")
	}
	if tb.TotalDebit-tb.TotalCredit != 500 {
		t.Fatalf("detector must surface the exact 500c gap, got debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
}

// TestPlaceNetBySign locks the sign-only placement: a positive net lands on the debit
// column, a negative net on the credit column (magnitude, other column zero) — regardless
// of any account type, since type no longer participates.
func TestPlaceNetBySign(t *testing.T) {
	var d, c int64
	placeNet(&d, &c, 700)
	if d != 700 || c != 0 {
		t.Fatalf("positive net must be a debit balance, got debit=%d credit=%d", d, c)
	}
	d, c = 0, 0
	placeNet(&d, &c, -400)
	if d != 0 || c != 400 {
		t.Fatalf("negative net must be a credit balance, got debit=%d credit=%d", d, c)
	}
}

// TestGrantBooksAsPromoExpense proves a FREE grant:* credit is booked as a promotional
// EXPENSE (Dr 5200 Promo credit / Cr 2000 Customer Wallet), never as processor cash — so
// the books never invent a Square-clearing asset for money no card was ever charged.
func TestGrantBooksAsPromoExpense(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "grant-1", Type: "deposit", Amount: 500, Currency: "usd", Tags: "grant:starter", CreatedAt: "2026-07-01T00:00:00Z"},
	}}
	if _, err := ingestOrg(ctx, src, st, "acme", false); err != nil {
		t.Fatalf("ingest grant: %v", err)
	}

	tb, _ := trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("grant voucher must balance")
	}
	if promoD, _ := closingOf(tb, PromoCredit); promoD != 500 {
		t.Fatalf("grant must Dr Promo credit $5.00, got debit=%d", promoD)
	}
	if sqD, _ := closingOf(tb, SquareClearing); sqD != 0 {
		t.Fatalf("a free grant must NOT invent Square-clearing cash, got debit=%d", sqD)
	}
	if _, wc := closingOf(tb, CustomerWallet); wc != 500 {
		t.Fatalf("grant must Cr Customer Wallet liability $5.00, got credit=%d", wc)
	}
}

// TestRefundReturnsFundsNotRevenue proves a refund draws the wallet liability down against
// CASH (Dr 2000 Customer Wallet / Cr 1000 Bank), never recognizing revenue on a return of
// unspent prepaid funds.
func TestRefundReturnsFundsNotRevenue(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-1", Type: "deposit", Amount: 5000, Currency: "usd", CreatedAt: "2026-07-01T00:00:00Z"},
		{ID: "ref-1", Type: "refund", Amount: 2000, Currency: "usd", CreatedAt: "2026-07-02T00:00:00Z"},
	}}
	if _, err := ingestOrg(ctx, src, st, "acme", false); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	tb, _ := trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("refund voucher must balance")
	}
	if _, wc := closingOf(tb, CustomerWallet); wc != 3000 {
		t.Fatalf("wallet liability must draw down to $30.00 after refund, got credit=%d", wc)
	}
	if _, bankC := closingOf(tb, Bank); bankC != 2000 {
		t.Fatalf("refund must Cr Bank $20.00 (cash out), got credit=%d", bankC)
	}
	if _, revC := closingOf(tb, UsageRevenue); revC != 0 {
		t.Fatalf("a refund must recognize NO revenue, got usage revenue credit=%d", revC)
	}
}

// TestNegativeAmountSkipped proves a mis-signed (non-positive) commerce row is SKIPPED,
// never abs()'d into a fabricated posting — commerce is a magnitude ledger.
func TestNegativeAmountSkipped(t *testing.T) {
	if _, ok := ruleFor(commerceTxn{ID: "neg-1", Type: "deposit", Amount: -5000}); ok {
		t.Fatalf("a negative-amount row must be skipped, not booked")
	}
	if _, ok := ruleFor(commerceTxn{ID: "zero-1", Type: "withdraw", Amount: 0}); ok {
		t.Fatalf("a zero-amount row must be skipped")
	}
}

// TestSandboxSegregation proves a sandbox (X-Hanzo-Test) feed posts to a PHYSICALLY
// separate ledger and never pollutes live revenue.
func TestSandboxSegregation(t *testing.T) {
	ctx := context.Background()
	live := newBookStore(t, "books")
	sandbox := newBookStore(t, "books-sandbox")

	src := &fakeSource{
		live:    []commerceTxn{{ID: "live-dep", Type: "deposit", Amount: 5000, CreatedAt: "2026-07-01T00:00:00Z"}},
		sandbox: []commerceTxn{{ID: "test-dep", Type: "deposit", Amount: 999999, CreatedAt: "2026-07-01T00:00:00Z"}},
	}
	if _, err := ingestOrg(ctx, src, live, "acme", false); err != nil {
		t.Fatalf("ingest live: %v", err)
	}
	if _, err := ingestOrg(ctx, src, sandbox, "acme", true); err != nil {
		t.Fatalf("ingest sandbox: %v", err)
	}

	tbLive, _ := trialBalance(ctx, live, "", "")
	if _, wc := closingOf(tbLive, CustomerWallet); wc != 5000 {
		t.Fatalf("live wallet must reflect ONLY the live $50.00 deposit, got credit=%d", wc)
	}
	tbSandbox, _ := trialBalance(ctx, sandbox, "", "")
	if _, wc := closingOf(tbSandbox, CustomerWallet); wc != 999999 {
		t.Fatalf("sandbox wallet must hold the test deposit, got credit=%d", wc)
	}
}
