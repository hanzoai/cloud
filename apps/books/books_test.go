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

// fakeCost is the test double for the COGS cost seam: a per-transaction cost figure. A txn
// absent from the map reports ok=false (no real figure → no accrual), mirroring the live
// default until the cloud_usage projection is wired.
type fakeCost struct{ perTxn map[string]int64 }

func (f *fakeCost) usageCost(_ context.Context, _ string, _ bool, t commerceTxn) (int64, bool) {
	c, ok := f.perTxn[t.ID]
	return c, ok
}

// mustPost posts a balanced voucher straight through the choke point, failing the test on
// error — the terse way a report test seeds a known ledger state.
func mustPost(t *testing.T, st *store, id string, legs ...Leg) {
	t.Helper()
	ok, err := st.post(context.Background(), Voucher{
		SourceKind: "test", SourceID: id, PostingAt: "2026-07-01T00:00:00Z", Legs: legs,
	}, RoundOffAllowance)
	if err != nil || !ok {
		t.Fatalf("seed post %s: ok=%v err=%v", id, ok, err)
	}
}

// retainedOf returns the derived retained-earnings equity line from a balance sheet.
func retainedOf(bs BalanceSheet) int64 {
	for _, l := range bs.Equity {
		if l.Account == "" && l.Name == "Retained earnings" {
			return l.Amount
		}
	}
	return 0
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
	posted, err := ingestOrg(ctx, src, nil, st, "acme", false)
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
	posted, err = ingestOrg(ctx, src, nil, st, "acme", false)
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
	posted, err = ingestOrg(ctx, src, nil, st, "acme", false)
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
	if _, err := ingestOrg(ctx, src, nil, st, "acme", false); err != nil {
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
	if _, err := ingestOrg(ctx, src, nil, st, "acme", false); err != nil {
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

// TestZeroAmountSkipped proves a zero-amount row (no accounting weight) and an unknown type
// are SKIPPED — never posted as an empty or malformed voucher.
func TestZeroAmountSkipped(t *testing.T) {
	if _, ok := ruleFor(commerceTxn{ID: "zero-1", Type: "withdraw", Amount: 0}); ok {
		t.Fatalf("a zero-amount row must be skipped")
	}
	if _, ok := ruleFor(commerceTxn{ID: "huh-1", Type: "mystery", Amount: 100}); ok {
		t.Fatalf("an unknown type must be skipped")
	}
}

// TestReversalBooksOpposite proves a NEGATIVE amount is a REVERSAL, booking the exact
// opposite legs of the event it reverses — never abs()'d away. A negative deposit
// (chargeback) is Dr 2000 Customer Wallet / Cr 1010 Square-clearing; a negative usage
// (de-recognition) is Dr 4000 Usage revenue / Cr 2000 Customer Wallet.
func TestReversalBooksOpposite(t *testing.T) {
	dep, ok := ruleFor(commerceTxn{ID: "cb-1", Type: "deposit", Amount: -5000})
	if !ok {
		t.Fatalf("a negative deposit must book a reversal, not be skipped")
	}
	if len(dep.Legs) != 2 || legDebit(dep.Legs, CustomerWallet) != 5000 || legCredit(dep.Legs, SquareClearing) != 5000 {
		t.Fatalf("negative deposit must be Dr 2000 / Cr 1010, got %+v", dep.Legs)
	}
	use, ok := ruleFor(commerceTxn{ID: "dr-1", Type: "usage", Amount: -3000})
	if !ok {
		t.Fatalf("a negative usage must book a de-recognition, not be skipped")
	}
	if legDebit(use.Legs, UsageRevenue) != 3000 || legCredit(use.Legs, CustomerWallet) != 3000 {
		t.Fatalf("negative usage must be Dr 4000 / Cr 2000, got %+v", use.Legs)
	}
}

// TestUsageReversalDeRecognizes runs a full flow through the ledger: deposit → usage
// (recognition) → negative usage (de-recognition), and proves the wallet liability is
// RESTORED and revenue is de-recognized to zero — the books stay balanced throughout.
func TestUsageReversalDeRecognizes(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-1", Type: "deposit", Amount: 5000, CreatedAt: "2026-07-01T00:00:00Z"},
		{ID: "use-1", Type: "usage", Amount: 3000, CreatedAt: "2026-07-02T00:00:00Z"},
		{ID: "rev-1", Type: "usage", Amount: -3000, CreatedAt: "2026-07-03T00:00:00Z"},
	}}
	if _, err := ingestOrg(ctx, src, nil, st, "acme", false); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	tb, _ := trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("after de-recognition the books must balance: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	if _, wc := closingOf(tb, CustomerWallet); wc != 5000 {
		t.Fatalf("wallet liability must be RESTORED to $50.00 after de-recognition, got credit=%d", wc)
	}
	if _, revC := closingOf(tb, UsageRevenue); revC != 0 {
		t.Fatalf("revenue must be DE-RECOGNIZED to zero, got credit=%d", revC)
	}
}

// TestCOGSAccrual proves that when the cost seam reports a REAL figure, a usage recognition
// accrues matching cost of goods (Dr 5000 Cloud COGS / Cr 2300 Accrued infra payable) in a
// separate voucher — making the P&L a gross-margin statement (income − expense), not
// revenue-only — while the books stay balanced and revenue is NOT double-counted.
func TestCOGSAccrual(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-1", Type: "deposit", Amount: 10000, CreatedAt: "2026-07-01T00:00:00Z"},
		{ID: "use-1", Type: "usage", Amount: 4000, CreatedAt: "2026-07-02T00:00:00Z"},
	}}
	cost := &fakeCost{perTxn: map[string]int64{"use-1": 2500}}
	if _, err := ingestOrg(ctx, src, cost, st, "acme", false); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	tb, _ := trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("COGS-accrued books must balance: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	if cogsD, _ := closingOf(tb, CloudCOGS); cogsD != 2500 {
		t.Fatalf("usage must accrue $25.00 Cloud COGS debit, got debit=%d", cogsD)
	}
	if _, apC := closingOf(tb, AccruedInfra); apC != 2500 {
		t.Fatalf("COGS must credit Accrued infra payable $25.00, got credit=%d", apC)
	}
	// Revenue is unchanged — commerce remains the SOLE revenue source, never double-counted.
	if _, revC := closingOf(tb, UsageRevenue); revC != 4000 {
		t.Fatalf("revenue must stay $40.00 (not double-counted), got credit=%d", revC)
	}

	p, err := profitAndLoss(ctx, st, "", "")
	if err != nil {
		t.Fatalf("pnl: %v", err)
	}
	if p.TotalIncome != 4000 || p.TotalExpense != 2500 || p.NetIncome != 1500 {
		t.Fatalf("P&L must be income=4000 expense=2500 net=1500, got income=%d expense=%d net=%d", p.TotalIncome, p.TotalExpense, p.NetIncome)
	}
}

// TestCOGSBackfillsPastRevenueCursor proves the cost accrual is DECOUPLED from the revenue
// cursor: a usage txn is first ingested with NO cost seam (revenue-only, exactly today's
// live noCost path), which advances the revenue cursor PAST that txn. When a real cost seam
// is wired on a LATER sync, the COGS voucher for that already-past usage still books — the
// matching principle backfills instead of being stranded revenue-only forever.
func TestCOGSBackfillsPastRevenueCursor(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-1", Type: "deposit", Amount: 10000, CreatedAt: "2026-07-01T00:00:00Z"},
		{ID: "use-1", Type: "usage", Amount: 4000, CreatedAt: "2026-07-02T00:00:00Z"},
	}}

	// Phase 1: ingest with NO cost seam — the live revenue-only path. Books revenue and
	// advances the revenue cursor past use-1; NO COGS accrues yet.
	if _, err := ingestOrg(ctx, src, nil, st, "acme", false); err != nil {
		t.Fatalf("ingest revenue-only: %v", err)
	}
	tb, _ := trialBalance(ctx, st, "", "")
	if cogsD, _ := closingOf(tb, CloudCOGS); cogsD != 0 {
		t.Fatalf("no cost seam must accrue ZERO COGS, got debit=%d", cogsD)
	}
	cur, err := st.cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cur.LastAt < "2026-07-02T00:00:00Z" {
		t.Fatalf("revenue cursor must have advanced past use-1, got %q", cur.LastAt)
	}

	// Phase 2: wire a REAL cost seam and re-sync. Even though the revenue cursor already sits
	// past use-1, the COGS pass re-scans it and books the matching accrual exactly once.
	cost := &fakeCost{perTxn: map[string]int64{"use-1": 2500}}
	posted, err := ingestOrg(ctx, src, cost, st, "acme", false)
	if err != nil {
		t.Fatalf("ingest with cost seam: %v", err)
	}
	if posted != 1 {
		t.Fatalf("backfill must post EXACTLY the 1 new COGS voucher (revenue idempotent), got %d", posted)
	}
	tb, _ = trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("backfilled books must balance: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	if cogsD, _ := closingOf(tb, CloudCOGS); cogsD != 2500 {
		t.Fatalf("backfill must accrue $25.00 Cloud COGS for the past usage, got debit=%d", cogsD)
	}
	if _, apC := closingOf(tb, AccruedInfra); apC != 2500 {
		t.Fatalf("backfill must credit Accrued infra payable $25.00, got credit=%d", apC)
	}
	// Revenue is unchanged — commerce stays the SOLE revenue source, never double-counted.
	if _, revC := closingOf(tb, UsageRevenue); revC != 4000 {
		t.Fatalf("revenue must stay $40.00 after backfill, got credit=%d", revC)
	}

	// Idempotent: a third sync with the same cost seam books nothing more.
	posted, err = ingestOrg(ctx, src, cost, st, "acme", false)
	if err != nil {
		t.Fatalf("third ingest: %v", err)
	}
	if posted != 0 {
		t.Fatalf("re-syncing an unchanged feed+seam must post 0 vouchers, got %d", posted)
	}
}

// TestPnLIsIncomeMinusExpense proves the P&L rolls Income and Expense roots into
// income − expense on a synthetic set, with credit-nature income shown positive.
func TestPnLIsIncomeMinusExpense(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// Recognize $90 revenue, accrue $30 COGS and $5 processor fee via direct balanced posts.
	mustPost(t, st, "r1", Leg{Account: CustomerWallet, Debit: 9000}, Leg{Account: UsageRevenue, Credit: 9000})
	mustPost(t, st, "c1", Leg{Account: CloudCOGS, Debit: 3000}, Leg{Account: AccruedInfra, Credit: 3000})
	mustPost(t, st, "f1", Leg{Account: ProcessorFees, Debit: 500}, Leg{Account: Bank, Credit: 500})

	p, err := profitAndLoss(ctx, st, "", "")
	if err != nil {
		t.Fatalf("pnl: %v", err)
	}
	if p.TotalIncome != 9000 {
		t.Fatalf("income must be $90.00, got %d", p.TotalIncome)
	}
	if p.TotalExpense != 3500 {
		t.Fatalf("expense must be $35.00 (COGS + fees), got %d", p.TotalExpense)
	}
	if p.NetIncome != 5500 {
		t.Fatalf("net income must be income−expense = $55.00, got %d", p.NetIncome)
	}
}

// TestBalanceSheetBalances proves Assets = Liabilities + Equity after a real revenue-and-
// cost flow: the period's net income is folded into equity as retained earnings so the
// accounting equation closes exactly.
func TestBalanceSheetBalances(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-1", Type: "deposit", Amount: 10000, CreatedAt: "2026-07-01T00:00:00Z"},
		{ID: "use-1", Type: "usage", Amount: 4000, CreatedAt: "2026-07-02T00:00:00Z"},
	}}
	cost := &fakeCost{perTxn: map[string]int64{"use-1": 2500}}
	if _, err := ingestOrg(ctx, src, cost, st, "acme", false); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	bs, err := balanceSheet(ctx, st, "")
	if err != nil {
		t.Fatalf("balance sheet: %v", err)
	}
	if !bs.Balanced {
		t.Fatalf("Assets must equal Liabilities + Equity: A=%d L=%d E=%d", bs.TotalAssets, bs.TotalLiabilities, bs.TotalEquity)
	}
	if bs.TotalAssets != bs.TotalLiabilities+bs.TotalEquity {
		t.Fatalf("equation broken: A=%d L+E=%d", bs.TotalAssets, bs.TotalLiabilities+bs.TotalEquity)
	}
	// Retained earnings = recognized revenue − accrued cost = $40 − $25 = $15.
	if re := retainedOf(bs); re != 1500 {
		t.Fatalf("retained earnings must be net income $15.00, got %d", re)
	}
}

// TestExportPackage proves the export bundles all four statements and they agree: the trial
// balance and balance sheet both balance, and the package P&L matches a standalone P&L.
func TestExportPackage(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-1", Type: "deposit", Amount: 8000, CreatedAt: "2026-07-01T00:00:00Z"},
		{ID: "use-1", Type: "usage", Amount: 3000, CreatedAt: "2026-07-02T00:00:00Z"},
	}}
	cost := &fakeCost{perTxn: map[string]int64{"use-1": 1800}}
	if _, err := ingestOrg(ctx, src, cost, st, "acme", false); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	pkg, err := financialPackage(ctx, st, "acme", "", "", 5000)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if pkg.Org != "acme" {
		t.Fatalf("package must carry the org, got %q", pkg.Org)
	}
	if !pkg.TrialBalance.Balanced {
		t.Fatalf("package trial balance must balance")
	}
	if !pkg.BalanceSheet.Balanced {
		t.Fatalf("package balance sheet must balance")
	}
	if pkg.PnL.NetIncome != 1200 {
		t.Fatalf("package P&L net must be $30.00 − $18.00 = $12.00, got %d", pkg.PnL.NetIncome)
	}
	if len(pkg.GL) == 0 {
		t.Fatalf("package must carry the GL detail behind the statements")
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
	if _, err := ingestOrg(ctx, src, nil, live, "acme", false); err != nil {
		t.Fatalf("ingest live: %v", err)
	}
	if _, err := ingestOrg(ctx, src, nil, sandbox, "acme", true); err != nil {
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
