package books

import (
	"context"
	"strings"
	"testing"
)

// seedSaaSMonth posts one month of a real SaaS shape straight through the choke point:
// a paid deposit, recurring MRR recognition, usage recognition, a cloud COGS accrual, and a
// processor fee. The resulting ledger has known metrics the tests assert exactly.
//
//	deposit paid $1,000  →  Dr 1010 Square-clearing / Cr 2000 Customer Wallet
//	MRR recognized $300  →  Dr 2000 Customer Wallet  / Cr 4100 Recurring revenue
//	usage recognized $200 → Dr 2000 Customer Wallet  / Cr 4000 Usage revenue
//	COGS accrued $600    →  Dr 5000 Cloud COGS        / Cr 2300 Accrued infra
//	processor fee $50    →  Dr 5100 Processor fees     / Cr 1000 Bank
func seedSaaSMonth(t *testing.T, st *store) {
	t.Helper()
	mustPost(t, st, "dep-1", Leg{Account: SquareClearing, Debit: 100000}, Leg{Account: CustomerWallet, Credit: 100000})
	mustPost(t, st, "mrr-1", Leg{Account: CustomerWallet, Debit: 30000}, Leg{Account: MRR, Credit: 30000})
	mustPost(t, st, "use-1", Leg{Account: CustomerWallet, Debit: 20000}, Leg{Account: UsageRevenue, Credit: 20000})
	mustPost(t, st, "cogs-1", Leg{Account: CloudCOGS, Debit: 60000}, Leg{Account: AccruedInfra, Credit: 60000})
	mustPost(t, st, "fee-1", Leg{Account: ProcessorFees, Debit: 5000}, Leg{Account: Bank, Credit: 5000})
}

// TestMetricsFromSyntheticLedger locks the deterministic engine against a known ledger:
// MRR, ARR, revenue, COGS, burn, gross margin, cash, deferred revenue, monthly burn, and —
// the load-bearing one — RUNWAY. With $950 cash and $150/mo net burn, runway is 6 months.
func TestMetricsFromSyntheticLedger(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")
	seedSaaSMonth(t, st)

	m, err := computeMetrics(ctx, st, "", "")
	if err != nil {
		t.Fatalf("computeMetrics: %v", err)
	}
	if m.Months != 1 {
		t.Fatalf("all-time window must normalize to 1 month, got %d", m.Months)
	}
	if m.MRR != 30000 {
		t.Fatalf("MRR must be $300.00 (recurring 4100 / 1mo), got %d", m.MRR)
	}
	if m.ARR != 360000 {
		t.Fatalf("ARR must be MRR×12 = $3,600.00, got %d", m.ARR)
	}
	if m.Revenue != 50000 {
		t.Fatalf("revenue must be $500.00 (usage $200 + MRR $300), got %d", m.Revenue)
	}
	if m.COGS != 60000 {
		t.Fatalf("COGS must be $600.00 (5000), got %d", m.COGS)
	}
	if m.Burn != 65000 {
		t.Fatalf("burn must be $650.00 (COGS $600 + fee $50), got %d", m.Burn)
	}
	if m.GrossProfit != -10000 {
		t.Fatalf("gross profit must be revenue−COGS = -$100.00, got %d", m.GrossProfit)
	}
	if m.GrossMarginBps != -2000 {
		t.Fatalf("gross margin must be -2000 bps (-20%%), got %d", m.GrossMarginBps)
	}
	if m.NetIncome != -15000 {
		t.Fatalf("net income must be revenue−burn = -$150.00, got %d", m.NetIncome)
	}
	if m.Cash != 95000 {
		t.Fatalf("cash must be $950.00 (clearing $1000 − bank fee $50), got %d", m.Cash)
	}
	if m.DeferredRevenue != 50000 {
		t.Fatalf("deferred revenue must be $500.00 wallet liability, got %d", m.DeferredRevenue)
	}
	if m.MonthlyBurn != 15000 {
		t.Fatalf("monthly burn must be $150.00/mo, got %d", m.MonthlyBurn)
	}
	if m.RunwayMonths != 6 {
		t.Fatalf("runway must be $950 / $150 = 6 months, got %d", m.RunwayMonths)
	}
}

// TestMetricsRunwayInfinite proves a PROFITABLE period consumes no runway: monthly burn is
// zero and runway is the infinite sentinel (-1), never a spurious month count.
func TestMetricsRunwayInfinite(t *testing.T) {
	// Hand-built sums: $500 usage revenue, $100 COGS, $2000 cash, no other movement.
	period := sums{
		UsageRevenue: {0, 50000}, // credit-normal income
		CloudCOGS:    {10000, 0}, // debit-normal expense
	}
	cumulative := sums{
		Bank:         {200000, 0}, // $2000 cash
		UsageRevenue: {0, 50000},
		CloudCOGS:    {10000, 0},
	}
	m := metricsFrom(period, cumulative, 1, "", "")
	if m.NetIncome != 40000 {
		t.Fatalf("net income must be $400.00 profit, got %d", m.NetIncome)
	}
	if m.MonthlyBurn != 0 {
		t.Fatalf("a profitable period must have zero monthly burn, got %d", m.MonthlyBurn)
	}
	if m.RunwayMonths != -1 {
		t.Fatalf("a profitable period must have infinite runway (-1), got %d", m.RunwayMonths)
	}
}

// TestMonthsBetweenNormalizesRunRate proves a multi-month window normalizes MRR and burn to
// a per-month run-rate: a quarter of $900 recurring revenue is $300 MRR.
func TestMonthsBetweenNormalizesRunRate(t *testing.T) {
	from := "2026-01-01T00:00:00Z"
	to := "2026-04-01T00:00:00Z" // ~3 months
	if got := monthsBetween(from, to); got != 3 {
		t.Fatalf("Jan→Apr must be 3 months, got %d", got)
	}
	period := sums{MRR: {0, 90000}} // $900 recurring over the quarter
	m := metricsFrom(period, sums{}, monthsBetween(from, to), from, to)
	if m.MRR != 30000 {
		t.Fatalf("MRR must be $900/3mo = $300.00, got %d", m.MRR)
	}
}

// TestFormatUSD locks the shared money formatter: whole dollars drop the cents, fractional
// keep them, thousands are grouped, and a negative carries an outside sign.
func TestFormatUSD(t *testing.T) {
	cases := map[int64]string{
		0:       "$0",
		30000:   "$300",
		95000:   "$950",
		1234000: "$12,340",
		1234050: "$12,340.50",
		-10000:  "-$100",
		100:     "$1",
		5:       "$0.05",
	}
	for cents, want := range cases {
		if got := formatUSD(cents); got != want {
			t.Fatalf("formatUSD(%d) = %q, want %q", cents, got, want)
		}
	}
}

// TestAskRoutesToRealFigures proves the intent router answers with the REAL numbers from the
// engine and the correct sources — the Ask contract's grounding guarantee, with no model in
// the loop.
func TestAskRoutesToRealFigures(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")
	seedSaaSMonth(t, st)
	m, err := computeMetrics(ctx, st, "", "")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}

	// MRR question → MRR + ARR figures with exact values.
	r := buildAnswer("what is my MRR right now?", m)
	if v := figureValue(r, "MRR"); v != "$300" {
		t.Fatalf("MRR figure must be $300, got %q", v)
	}
	if v := figureValue(r, "ARR"); v != "$3,600" {
		t.Fatalf("ARR figure must be $3,600, got %q", v)
	}
	if !hasSource(r, "pnl") {
		t.Fatalf("MRR answer must cite the pnl source, got %v", r.Sources)
	}
	if !strings.Contains(r.Answer, "$300") {
		t.Fatalf("MRR answer must state the real $300 figure, got %q", r.Answer)
	}

	// Runway question → the real 6-month runway + cash + monthly burn.
	r = buildAnswer("how long is my runway?", m)
	if v := figureValue(r, "Runway"); v != "6 months" {
		t.Fatalf("runway figure must be '6 months', got %q", v)
	}
	if v := figureValue(r, "Cash"); v != "$950" {
		t.Fatalf("cash figure must be $950, got %q", v)
	}
	if !hasSource(r, "balance-sheet") {
		t.Fatalf("runway answer must cite the balance-sheet source, got %v", r.Sources)
	}

	// Revenue question → total recognized revenue.
	r = buildAnswer("how much revenue did we make?", m)
	if v := figureValue(r, "Revenue"); v != "$500" {
		t.Fatalf("revenue figure must be $500, got %q", v)
	}

	// Unknown question → the summary, still all real figures.
	r = buildAnswer("give me the state of the business", m)
	if figureValue(r, "Revenue") == "" || figureValue(r, "Cash") == "" {
		t.Fatalf("summary must carry real revenue + cash figures, got %+v", r.Figures)
	}
	if len(r.Followups) == 0 {
		t.Fatalf("every answer must offer sharp followups")
	}
}

// TestDetectOutlierAndReversal proves the clarifying-questions detector surfaces an outlier
// usage charge and a de-recognition (sign anomaly), and stays SILENT on ordinary rows.
func TestDetectOutlierAndReversal(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")

	// A deposit funds the wallet so consumption never overdraws it (that is a separate
	// detector, tested below) — this test isolates the outlier + reversal signals.
	mustPostCommerce(t, st, "dep", Leg{Account: SquareClearing, Debit: 100000}, Leg{Account: CustomerWallet, Credit: 100000})
	// Five ordinary $10 usage recognitions (the norm), then one $500 outlier.
	mustPostCommerce(t, st, "u1", Leg{Account: CustomerWallet, Debit: 1000}, Leg{Account: UsageRevenue, Credit: 1000})
	mustPostCommerce(t, st, "u2", Leg{Account: CustomerWallet, Debit: 1000}, Leg{Account: UsageRevenue, Credit: 1000})
	mustPostCommerce(t, st, "u3", Leg{Account: CustomerWallet, Debit: 1000}, Leg{Account: UsageRevenue, Credit: 1000})
	mustPostCommerce(t, st, "u4", Leg{Account: CustomerWallet, Debit: 1200}, Leg{Account: UsageRevenue, Credit: 1200})
	mustPostCommerce(t, st, "big", Leg{Account: CustomerWallet, Debit: 50000}, Leg{Account: UsageRevenue, Credit: 50000})
	// A de-recognition: usage revenue reversed.
	mustPostCommerce(t, st, "rev", Leg{Account: UsageRevenue, Debit: 800}, Leg{Account: CustomerWallet, Credit: 800})

	rows, err := st.listGL(ctx, 1000)
	if err != nil {
		t.Fatalf("listGL: %v", err)
	}
	qs := detectQuestions(rows)

	if !hasQuestion(qs, "outlier", "big") {
		t.Fatalf("the $500 charge (vs ~$10 median) must be flagged an outlier, got %+v", qs)
	}
	if !hasQuestion(qs, "reversal", "rev") {
		t.Fatalf("the usage-revenue reversal must be flagged, got %+v", qs)
	}
	// The ordinary $10 charges must NOT be flagged — no busywork.
	if hasQuestion(qs, "outlier", "u1") || hasQuestion(qs, "outlier", "u2") {
		t.Fatalf("ordinary usage must not be flagged, got %+v", qs)
	}
	// Sharpest first: the $500 outlier leads.
	if len(qs) == 0 || qs[0].ID != "big" {
		t.Fatalf("largest anomaly must lead, got %+v", qs)
	}
}

// TestDetectUncostedPartialCoverage proves the uncosted-usage detector is GATED on partial
// coverage: it fires only when SOME usage is costed and this one is not — and stays silent
// when nothing is costed at all (the live revenue-only path), so it never generates busywork.
func TestDetectUncostedPartialCoverage(t *testing.T) {
	ctx := context.Background()

	// Case 1: no COGS anywhere → no uncosted questions.
	bare := newBookStore(t, "books")
	mustPostCommerce(t, bare, "u1", Leg{Account: CustomerWallet, Debit: 4000}, Leg{Account: UsageRevenue, Credit: 4000})
	mustPostCommerce(t, bare, "u2", Leg{Account: CustomerWallet, Debit: 4000}, Leg{Account: UsageRevenue, Credit: 4000})
	rows, _ := bare.listGL(ctx, 1000)
	for _, q := range detectQuestions(rows) {
		if q.Kind == "uncosted" {
			t.Fatalf("with no COGS at all, uncosted must be silent, got %+v", q)
		}
	}

	// Case 2: u1 is costed, u2 is not → u2 flagged uncosted, u1 not.
	partial := newBookStore(t, "books-sandbox")
	mustPostCommerce(t, partial, "u1", Leg{Account: CustomerWallet, Debit: 4000}, Leg{Account: UsageRevenue, Credit: 4000})
	mustPostCommerce(t, partial, "u2", Leg{Account: CustomerWallet, Debit: 4000}, Leg{Account: UsageRevenue, Credit: 4000})
	mustPostKind(t, partial, "cogs_accrual", "u1", Leg{Account: CloudCOGS, Debit: 2500}, Leg{Account: AccruedInfra, Credit: 2500})
	rows, _ = partial.listGL(ctx, 1000)
	qs := detectQuestions(rows)
	if !hasQuestion(qs, "uncosted", "u2") {
		t.Fatalf("uncosted u2 must be flagged under partial coverage, got %+v", qs)
	}
	if hasQuestion(qs, "uncosted", "u1") {
		t.Fatalf("the costed u1 must NOT be flagged uncosted, got %+v", qs)
	}
}

// TestDetectCleanBooksNoBusywork proves a clean, fully-consistent ledger yields ZERO
// questions — the detector prompts only on real anomalies.
func TestDetectCleanBooksNoBusywork(t *testing.T) {
	ctx := context.Background()
	st := newBookStore(t, "books")
	// Even, costed usage: nothing unusual.
	mustPostCommerce(t, st, "u1", Leg{Account: CustomerWallet, Debit: 4000}, Leg{Account: UsageRevenue, Credit: 4000})
	mustPostCommerce(t, st, "u2", Leg{Account: CustomerWallet, Debit: 4000}, Leg{Account: UsageRevenue, Credit: 4000})
	mustPost(t, st, "dep-1", Leg{Account: SquareClearing, Debit: 100000}, Leg{Account: CustomerWallet, Credit: 100000})
	rows, _ := st.listGL(ctx, 1000)
	if qs := detectQuestions(rows); len(qs) != 0 {
		t.Fatalf("clean books must produce no questions, got %+v", qs)
	}
}

// ── test helpers ──

// mustPostCommerce posts a balanced voucher tagged as a commerce_txn (the source kind the
// usage detectors key on).
func mustPostCommerce(t *testing.T, st *store, id string, legs ...Leg) {
	t.Helper()
	mustPostKind(t, st, "commerce_txn", id, legs...)
}

func mustPostKind(t *testing.T, st *store, kind, id string, legs ...Leg) {
	t.Helper()
	ok, err := st.post(context.Background(), Voucher{
		SourceKind: kind, SourceID: id, PostingAt: "2026-07-10T00:00:00Z", Legs: legs,
	}, RoundOffAllowance)
	if err != nil || !ok {
		t.Fatalf("seed post %s/%s: ok=%v err=%v", kind, id, ok, err)
	}
}

func figureValue(r AskResponse, label string) string {
	for _, f := range r.Figures {
		if f.Label == label {
			return f.Value
		}
	}
	return ""
}

func hasSource(r AskResponse, src string) bool {
	for _, s := range r.Sources {
		if s == src {
			return true
		}
	}
	return false
}

func hasQuestion(qs []Question, kind, id string) bool {
	for _, q := range qs {
		if q.Kind == kind && q.ID == id {
			return true
		}
	}
	return false
}
