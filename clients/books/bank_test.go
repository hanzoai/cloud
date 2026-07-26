package books

import (
	"context"
	"testing"
)

// bankInflow / bankOutflow build a synthetic normalized BankTxn for the engine tests.
func bankTxn(id string, dir Direction, cents int64, at, merchant string) BankTxn {
	return BankTxn{
		Connector: "import", ExternalID: id, PostedAt: at, AmountCents: cents,
		Currency: "usd", Direction: dir, Merchant: merchant, Description: merchant,
	}
}

// revenueTouched reports whether any income account carries a balance — the property a
// reconciliation move and an unmatched inflow must BOTH leave false (no revenue invented).
func revenueTouched(tb TrialBalance) bool {
	for _, acct := range []string{UsageRevenue, MRR, ProductRevenue} {
		if d, c := closingOf(tb, acct); d != 0 || c != 0 {
			return true
		}
	}
	return false
}

// TestBankOutflowBooksExpense proves an OUTFLOW books Dr expense / Cr Bank, defaulting an
// uncategorized merchant to 5900, and leaves the trial balance balanced.
func TestBankOutflowBooksExpense(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	res, err := mapAndPost(ctx, st, bankTxn("out-1", Outflow, 2500, "2026-07-05T00:00:00Z", "Acme Widgets"))
	if err != nil {
		t.Fatalf("map outflow: %v", err)
	}
	if res.Status != statusPosted || !res.VoucherPosted {
		t.Fatalf("outflow must post an expense voucher, got %+v", res)
	}

	tb, _ := trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("outflow: NOT balanced: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	if ud, _ := closingOf(tb, UncategorizedExpense); ud != 2500 {
		t.Fatalf("uncategorized outflow must Dr 5900 $25.00, got debit=%d", ud)
	}
	if _, bc := closingOf(tb, Bank); bc != 2500 {
		t.Fatalf("outflow must Cr 1000 Bank $25.00, got credit=%d", bc)
	}
	if revenueTouched(tb) {
		t.Fatalf("an outflow must recognize NO revenue")
	}
}

// TestBankOutflowCategorizes proves a recognized merchant lands in a real expense category
// (AWS → Cloud/GPU COGS), not the uncategorized default.
func TestBankOutflowCategorizes(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	if _, err := mapAndPost(ctx, st, bankTxn("out-aws", Outflow, 9900, "2026-07-05T00:00:00Z", "AWS cloud")); err != nil {
		t.Fatalf("map outflow: %v", err)
	}
	tb, _ := trialBalance(ctx, st, "", "")
	if cd, _ := closingOf(tb, CloudCOGS); cd != 9900 {
		t.Fatalf("AWS outflow must Dr 5000 Cloud COGS $99.00, got debit=%d", cd)
	}
	if ud, _ := closingOf(tb, UncategorizedExpense); ud != 0 {
		t.Fatalf("a categorized outflow must NOT touch Uncategorized, got debit=%d", ud)
	}
}

// seedScanBill posts a scanned bill directly through the choke point: Dr expense / Cr 2001
// VendorPayable for the total, tagged (scan, id) with the vendor as its description — the
// accrual an outflow later settles.
func seedScanBill(t *testing.T, st *store, id, vendor, expense string, total int64, issuedAt string) {
	t.Helper()
	ok, err := st.post(context.Background(), Voucher{
		SourceKind: scanSourceKind, SourceID: id, PostingAt: issuedAt, Description: vendor,
		Legs: []Leg{{Account: expense, Debit: total}, {Account: VendorPayable, Credit: total}},
	}, RoundOffAllowance)
	if err != nil || !ok {
		t.Fatalf("seed scan bill %s: ok=%v err=%v", id, ok, err)
	}
}

// TestBankOutflowSettlesScannedPayable proves the fix for the double-count finding: a bank
// OUTFLOW that pays a scanned bill SETTLES the accrued payable (Dr 2001 / Cr 1000) instead of
// re-booking the expense — so the spend is counted ONCE and the AP nets to zero, not twice
// with a dangling liability.
func TestBankOutflowSettlesScannedPayable(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// A scanned AWS bill accrues $99 of Cloud COGS against VendorPayable.
	seedScanBill(t, st, "scan:aws", "Amazon Web Services", CloudCOGS, 9900, "2026-07-01T00:00:00Z")

	// The bank pays it 20 days later — same amount, vendor named in the descriptor, in-window.
	res, err := mapAndPost(ctx, st, bankTxn("pay-aws", Outflow, 9900, "2026-07-21T00:00:00Z", "AMAZON WEB SERVICES"))
	if err != nil {
		t.Fatalf("map settling outflow: %v", err)
	}
	if res.Status != statusSettled || !res.VoucherPosted {
		t.Fatalf("an outflow paying a scanned bill must SETTLE it, got %+v", res)
	}

	tb, _ := trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("settlement: NOT balanced: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	// Expense booked ONCE ($99) — the accrual, not doubled by the payment.
	if cd, _ := closingOf(tb, CloudCOGS); cd != 9900 {
		t.Fatalf("Cloud COGS must be booked ONCE ($99.00), got debit=%d", cd)
	}
	// VendorPayable nets to zero (accrued then paid), no dangling AP.
	if d, c := closingOf(tb, VendorPayable); d != 0 || c != 0 {
		t.Fatalf("VendorPayable must net to ZERO after payment, got debit=%d credit=%d", d, c)
	}
	// Cash left the bank once.
	if _, bc := closingOf(tb, Bank); bc != 9900 {
		t.Fatalf("Bank must Cr the one $99.00 payment, got credit=%d", bc)
	}
}

// TestBankOutflowNoPayableMatchBooksExpense proves the vendor gate fails secure: a same-amount,
// in-window outflow whose descriptor does NOT name the scanned bill's vendor is booked as a
// FRESH expense (Dr expense / Cr Bank), leaving the unrelated payable OPEN — it never pays down
// a bill it cannot be shown to belong to.
func TestBankOutflowNoPayableMatchBooksExpense(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	seedScanBill(t, st, "scan:aws", "Amazon Web Services", CloudCOGS, 9900, "2026-07-01T00:00:00Z")

	// A $99 debit to an UNRELATED vendor — same amount, in-window, but not AWS.
	res, err := mapAndPost(ctx, st, bankTxn("pay-other", Outflow, 9900, "2026-07-10T00:00:00Z", "Staples"))
	if err != nil {
		t.Fatalf("map outflow: %v", err)
	}
	if res.Status != statusPosted {
		t.Fatalf("an unrelated outflow must book a fresh expense, got %+v", res)
	}
	tb, _ := trialBalance(ctx, st, "", "")
	// The AWS payable stays OPEN ($99 credit) — the unrelated debit did not consume it.
	if d, c := closingOf(tb, VendorPayable); c-d != 9900 {
		t.Fatalf("the AWS payable must stay OPEN ($99.00 credit), got debit=%d credit=%d", d, c)
	}
	// Cloud COGS carries only the scan accrual; the unrelated debit landed in Uncategorized.
	if cd, _ := closingOf(tb, CloudCOGS); cd != 9900 {
		t.Fatalf("Cloud COGS must hold only the scan accrual, got debit=%d", cd)
	}
	if ud, _ := closingOf(tb, UncategorizedExpense); ud != 9900 {
		t.Fatalf("the unrelated outflow must book a fresh Uncategorized expense, got debit=%d", ud)
	}
}

// TestBankInflowMatchedClearsSquare proves a bank deposit that MATCHES a prior Square
// capture CLEARS 1010 → 1000 (a reconciliation) and recognizes NO revenue — the
// double-count guard. It seeds the capture through the P0 spine (a commerce deposit books
// Dr 1010 / Cr 2000), then feeds the matching bank inflow.
func TestBankInflowMatchedClearsSquare(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// Seed a Square capture via the spine: deposit → Dr 1010 Square-clearing / Cr 2000.
	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-1", Type: "deposit", Amount: 10000, Currency: "usd", Notes: "card top-up", CreatedAt: "2026-07-01T10:00:00Z"},
	}}
	if _, err := ingestOrg(ctx, src, nil, st, "acme", false); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	// The cash settles to the bank two days later — same amount, within the window.
	res, err := mapAndPost(ctx, st, bankTxn("in-settle", Inflow, 10000, "2026-07-03T00:00:00Z", "SQUARE INC DEPOSIT"))
	if err != nil {
		t.Fatalf("map matched inflow: %v", err)
	}
	if res.Status != statusReconciled || !res.VoucherPosted || res.QuestionRaised {
		t.Fatalf("matched inflow must reconcile (no question), got %+v", res)
	}

	tb, _ := trialBalance(ctx, st, "", "")
	if !tb.Balanced {
		t.Fatalf("matched inflow: NOT balanced: debit=%d credit=%d", tb.TotalDebit, tb.TotalCredit)
	}
	// 1010 nets to zero (captured then cleared); 1000 holds the settled cash.
	if sd, sc := closingOf(tb, SquareClearing); sd != 0 || sc != 0 {
		t.Fatalf("Square-clearing must net to zero after settlement, got debit=%d credit=%d", sd, sc)
	}
	if bd, _ := closingOf(tb, Bank); bd != 10000 {
		t.Fatalf("Bank must hold the settled $100.00, got debit=%d", bd)
	}
	if revenueTouched(tb) {
		t.Fatalf("a settlement must recognize NO revenue (already recognized at usage)")
	}

	// No open question was raised for a cleanly matched inflow.
	if qs, _ := st.listQuestions(ctx); len(qs) != 0 {
		t.Fatalf("a matched inflow must raise NO question, got %d", len(qs))
	}
}

// TestBankInflowUnmatchedRaisesQuestion proves an inflow with NO matching Square capture is
// NOT guessed into revenue: it raises a clarifying question, books no voucher, and leaves
// every income account untouched.
func TestBankInflowUnmatchedRaisesQuestion(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	res, err := mapAndPost(ctx, st, bankTxn("in-mystery", Inflow, 4200, "2026-07-05T00:00:00Z", "WIRE FROM UNKNOWN"))
	if err != nil {
		t.Fatalf("map unmatched inflow: %v", err)
	}
	if res.Status != statusUnmatched || !res.QuestionRaised || res.VoucherPosted {
		t.Fatalf("unmatched inflow must raise a question and post no voucher, got %+v", res)
	}

	tb, _ := trialBalance(ctx, st, "", "")
	if revenueTouched(tb) {
		t.Fatalf("an unmatched inflow must NEVER guess revenue")
	}
	if _, bc := closingOf(tb, Bank); bc != 0 {
		t.Fatalf("an unmatched inflow must post no bank leg, got Bank credit=%d", bc)
	}
	if rows, _ := st.listGL(ctx, 100); len(rows) != 0 {
		t.Fatalf("an unmatched inflow must write NO gl rows, got %d", len(rows))
	}

	qs, err := st.listQuestions(ctx)
	if err != nil {
		t.Fatalf("list questions: %v", err)
	}
	if len(qs) != 1 || qs[0].ExternalID != "in-mystery" {
		t.Fatalf("exactly one clarifying question must exist for the inflow, got %+v", qs)
	}
	if un, _ := st.listUnreconciled(ctx); len(un) != 1 {
		t.Fatalf("the unmatched inflow must surface in the unreconciled queue, got %d", len(un))
	}
}

// TestBankInflowCoincidentalAmountRaisesQuestion proves the fix for the false-match finding:
// a same-amount, in-window inflow that is NOT a processor settlement (an owner wire) must NOT
// consume the open capture and must NOT clear — it raises a question — and the untouched
// capture must still be clearable by the REAL settlement that arrives afterward.
func TestBankInflowCoincidentalAmountRaisesQuestion(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// Seed a $5.00 Square capture via the spine (deposit → Dr 1010 / Cr 2000).
	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-500", Type: "deposit", Amount: 500, Currency: "usd", Notes: "card top-up", CreatedAt: "2026-07-01T10:00:00Z"},
	}}
	if _, err := ingestOrg(ctx, src, nil, st, "acme", false); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	// A $5.00 OWNER WIRE lands two days later: same amount, in-window, but no processor
	// descriptor. It must raise a question, post no clearing voucher, and leave 1010 open.
	res, err := mapAndPost(ctx, st, bankTxn("owner-wire", Inflow, 500, "2026-07-03T00:00:00Z", "WIRE FROM OWNER"))
	if err != nil {
		t.Fatalf("map owner wire: %v", err)
	}
	if res.Status != statusUnmatched || !res.QuestionRaised || res.VoucherPosted {
		t.Fatalf("a coincidental same-amount non-settlement inflow must raise a question, got %+v", res)
	}
	tb, _ := trialBalance(ctx, st, "", "")
	if sd, sc := closingOf(tb, SquareClearing); sd-sc != 500 {
		t.Fatalf("the owner wire must leave the capture OPEN on 1010 ($5.00), got debit=%d credit=%d", sd, sc)
	}
	if revenueTouched(tb) {
		t.Fatalf("the owner wire must invent NO revenue")
	}

	// The REAL Square settlement arrives — the capacity the wire did NOT consume is still
	// open, so it clears cleanly and 1010 nets to zero.
	res, err = mapAndPost(ctx, st, bankTxn("square-settle", Inflow, 500, "2026-07-04T00:00:00Z", "SQUARE INC DEPOSIT"))
	if err != nil {
		t.Fatalf("map settlement: %v", err)
	}
	if res.Status != statusReconciled || !res.VoucherPosted || res.QuestionRaised {
		t.Fatalf("the real settlement must clear the still-open capture, got %+v", res)
	}
	tb, _ = trialBalance(ctx, st, "", "")
	if sd, sc := closingOf(tb, SquareClearing); sd != sc {
		t.Fatalf("1010 must net to zero after the real settlement clears, got debit=%d credit=%d", sd, sc)
	}
	if bd, _ := closingOf(tb, Bank); bd != 500 {
		t.Fatalf("Bank must hold the one settled $5.00 (not double), got debit=%d", bd)
	}
	if revenueTouched(tb) {
		t.Fatalf("neither the wire nor the settlement may invent revenue")
	}
}

// TestBankSecondSettlementNoOverClear proves per-capture consumption: once a capture has been
// cleared, a SECOND identical-amount processor inflow finds no open capture and raises a
// question instead of over-clearing 1010 (which would drive it negative and fabricate a
// second settlement of money that never had a second capture).
func TestBankSecondSettlementNoOverClear(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	src := &fakeSource{live: []commerceTxn{
		{ID: "dep-x", Type: "deposit", Amount: 800, Currency: "usd", Notes: "top-up", CreatedAt: "2026-07-01T10:00:00Z"},
	}}
	if _, err := ingestOrg(ctx, src, nil, st, "acme", false); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	r1, err := mapAndPost(ctx, st, bankTxn("settle-1", Inflow, 800, "2026-07-02T00:00:00Z", "SQUARE INC"))
	if err != nil || r1.Status != statusReconciled {
		t.Fatalf("first settlement must reconcile, got %+v err=%v", r1, err)
	}
	r2, err := mapAndPost(ctx, st, bankTxn("settle-2", Inflow, 800, "2026-07-03T00:00:00Z", "SQUARE INC"))
	if err != nil {
		t.Fatalf("map second settlement: %v", err)
	}
	if r2.Status != statusUnmatched || !r2.QuestionRaised || r2.VoucherPosted {
		t.Fatalf("a second settlement with no open capture must raise a question, got %+v", r2)
	}
	tb, _ := trialBalance(ctx, st, "", "")
	if sd, sc := closingOf(tb, SquareClearing); sd != sc {
		t.Fatalf("1010 must net to zero (one capture, one clear — no over-clear), got debit=%d credit=%d", sd, sc)
	}
	if bd, _ := closingOf(tb, Bank); bd != 800 {
		t.Fatalf("Bank must hold exactly the one $8.00 that was captured, got debit=%d", bd)
	}
}

// TestBankIdempotent proves re-mapping the SAME (connector, external_id) posts nothing the
// second time — no double-post — across outflow and unmatched-inflow paths, and the trial
// balance is unchanged.
func TestBankIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	out := bankTxn("dup-out", Outflow, 3300, "2026-07-05T00:00:00Z", "Office supplies")
	if _, err := mapAndPost(ctx, st, out); err != nil {
		t.Fatalf("first outflow: %v", err)
	}
	in := bankTxn("dup-in", Inflow, 700, "2026-07-06T00:00:00Z", "Unknown deposit")
	if _, err := mapAndPost(ctx, st, in); err != nil {
		t.Fatalf("first inflow: %v", err)
	}

	tb1, _ := trialBalance(ctx, st, "", "")
	rows1, _ := st.listGL(ctx, 1000)
	q1, _ := st.listQuestions(ctx)

	// Re-map the identical rows — must be idempotent no-ops.
	res, err := mapAndPost(ctx, st, out)
	if err != nil || !res.Skipped {
		t.Fatalf("re-mapping an outflow must skip, got %+v err=%v", res, err)
	}
	res, err = mapAndPost(ctx, st, in)
	if err != nil || !res.Skipped {
		t.Fatalf("re-mapping an inflow must skip, got %+v err=%v", res, err)
	}

	tb2, _ := trialBalance(ctx, st, "", "")
	rows2, _ := st.listGL(ctx, 1000)
	q2, _ := st.listQuestions(ctx)

	if tb2.TotalDebit != tb1.TotalDebit || tb2.TotalCredit != tb1.TotalCredit {
		t.Fatalf("idempotent re-map changed the books: %d/%d vs %d/%d",
			tb2.TotalDebit, tb2.TotalCredit, tb1.TotalDebit, tb1.TotalCredit)
	}
	if len(rows2) != len(rows1) {
		t.Fatalf("idempotent re-map changed gl row count: %d vs %d", len(rows2), len(rows1))
	}
	if len(q2) != len(q1) {
		t.Fatalf("idempotent re-map changed question count: %d vs %d", len(q2), len(q1))
	}
	if !tb2.Balanced {
		t.Fatalf("books must stay balanced through idempotent re-map")
	}
}

// TestBankTransferNoPL proves an own-account transfer records for audit but posts NO GL
// entry (no profit-and-loss effect) — the books stay flat.
func TestBankTransferNoPL(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	res, err := mapAndPost(ctx, st, bankTxn("xfer-1", Transfer, 50000, "2026-07-05T00:00:00Z", "Transfer to savings"))
	if err != nil {
		t.Fatalf("map transfer: %v", err)
	}
	if res.Status != statusTransfer || res.VoucherPosted || res.QuestionRaised {
		t.Fatalf("a transfer must record with no voucher/question, got %+v", res)
	}
	if rows, _ := st.listGL(ctx, 100); len(rows) != 0 {
		t.Fatalf("a transfer must write NO gl rows, got %d", len(rows))
	}
	if txns, _ := st.listBankTxns(ctx, 100); len(txns) != 1 || txns[0].Status != statusTransfer {
		t.Fatalf("the transfer must be recorded for audit, got %+v", txns)
	}
}

// TestParseCents locks exact decimal-string → int64 cents parsing with no float64: signs,
// separators, currency symbols, padding, and rejection of sub-cent precision.
func TestParseCents(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		bad  bool
	}{
		{in: "12.34", want: 1234},
		{in: "-1,203.50", want: -120350},
		{in: "$5", want: 500},
		{in: "0.09", want: 9},
		{in: "1000", want: 100000},
		{in: ".5", want: 50},
		{in: "12.345", bad: true}, // sub-cent precision rejected, never rounded
		{in: "abc", bad: true},
		{in: "", bad: true},
		// Overflow must ERROR, never silently wrap to a negative or zero magnitude — the
		// exact-int64-cents guarantee has no back door for a malformed/malicious PDF.
		{in: "92233720368547758.08", bad: true},  // wrapped to a negative before the guard
		{in: "184467440737095516.16", bad: true}, // wrapped to zero before the guard
		{in: "99999999999999999999", bad: true},  // digit run overflows int64 in parseDigits
	}
	for _, c := range cases {
		got, err := parseCents(c.in)
		if c.bad {
			if err == nil {
				t.Fatalf("parseCents(%q) must error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseCents(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("parseCents(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestBankValidation proves a structurally invalid row (non-positive amount, missing key)
// is rejected before it can post.
func TestBankValidation(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	for _, bad := range []BankTxn{
		{Connector: "import", ExternalID: "z", PostedAt: "2026-07-05T00:00:00Z", AmountCents: 0, Direction: Outflow},
		{Connector: "import", ExternalID: "", PostedAt: "2026-07-05T00:00:00Z", AmountCents: 100, Direction: Outflow},
		{Connector: "", ExternalID: "z", PostedAt: "2026-07-05T00:00:00Z", AmountCents: 100, Direction: Outflow},
	} {
		if _, err := mapAndPost(ctx, st, bad); err == nil {
			t.Fatalf("invalid txn must be rejected: %+v", bad)
		}
	}
}
