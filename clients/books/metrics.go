package books

// metrics.go — the DETERMINISTIC SaaS-metrics engine. It reads the SAME ledger the
// statements read (per-account debit/credit sums over a window) and derives the numbers a
// founder actually asks for: MRR, ARR, revenue, COGS, burn, gross margin, cash, deferred
// revenue, monthly burn, and runway. It is PURE arithmetic over the sums — no new writes,
// no float64, no heuristics — so the AI Ask brain (ask.go) narrates figures that are
// GROUND TRUTH, computed the one way the books define them. A metric is never guessed; it
// is the ledger, aggregated.
//
// FLOW vs STOCK. Revenue/COGS/burn/MRR are FLOW — movement over the (from, to] period,
// exactly the P&L window. Cash and deferred revenue are STOCK — a cumulative balance AS OF
// `to`, exactly the balance-sheet convention. Reading each from its natural window is what
// makes runway (a stock over a flow) correct.

import (
	"context"
	"time"
)

// Metrics is the deterministic SaaS-metrics snapshot over a reporting window. Every field
// is int64 CENTS except the two ratios/counters (basis points, months), matching the
// ledger's exact-integer money model. RunwayMonths == -1 is the INFINITE sentinel (the org
// is not burning cash), never a real month count.
type Metrics struct {
	From            string `json:"from,omitempty"`
	To              string `json:"to,omitempty"`
	Period          string `json:"period"`          // human label, e.g. "2026-07" or "all-time"
	Months          int    `json:"months"`          // window length used to normalize MRR / burn
	MRR             int64  `json:"mrr"`             // recurring revenue (4100) per month
	ARR             int64  `json:"arr"`             // MRR * 12
	Revenue         int64  `json:"revenue"`         // recognized revenue (all Income roots) over the period
	COGS            int64  `json:"cogs"`            // cost of goods (5000) over the period
	Burn            int64  `json:"burn"`            // total expense (all Expense roots) over the period
	GrossProfit     int64  `json:"grossProfit"`     // Revenue − COGS
	GrossMarginBps  int64  `json:"grossMarginBps"`  // GrossProfit / Revenue in basis points (7000 = 70%)
	NetIncome       int64  `json:"netIncome"`       // Revenue − Burn
	Cash            int64  `json:"cash"`            // Bank (1000) + Square clearing (1010) as of `to`
	DeferredRevenue int64  `json:"deferredRevenue"` // Customer Wallet (2000) liability as of `to`
	MonthlyBurn     int64  `json:"monthlyBurn"`     // net cash burned per month (>0 ⇒ losing cash)
	RunwayMonths    int64  `json:"runwayMonths"`    // Cash / MonthlyBurn; -1 = infinite (not burning)
}

// computeMetrics reads the two windows the engine needs — period movement over (from, to]
// for the flows, cumulative balance as of `to` for the stocks — and folds them into the
// deterministic snapshot. It is the ONE store-backed entry point; the arithmetic lives in
// the pure metricsFrom so it is unit-tested with a synthetic ledger and no DB.
func computeMetrics(ctx context.Context, s *store, from, to string) (Metrics, error) {
	period, err := s.sums(ctx, from, to) // posting_at > from AND <= to (P&L window)
	if err != nil {
		return Metrics{}, err
	}
	cumulative, err := s.sums(ctx, "", to) // posting_at <= to (balance-sheet window)
	if err != nil {
		return Metrics{}, err
	}
	return metricsFrom(period, cumulative, monthsBetween(from, to), from, to), nil
}

// metricsFrom is the PURE core: period sums drive the flows, cumulative sums the stocks,
// months normalizes the run-rates. It computes nothing the statements would not — Revenue
// is the P&L income total, Cash is the balance-sheet asset cash — so a figure the Ask brain
// narrates always ties back to a report the founder can open.
func metricsFrom(period, cumulative sums, months int, from, to string) Metrics {
	if months < 1 {
		months = 1
	}
	var revenue, burn, cogs int64
	for _, a := range chartOfAccounts {
		switch a.Type {
		case Income:
			revenue += incomeAmt(period, a.Number)
		case Expense:
			e := expenseAmt(period, a.Number)
			burn += e
			if a.Number == CloudCOGS {
				cogs += e
			}
		}
	}
	m := Metrics{
		From:            from,
		To:              to,
		Period:          periodLabel(to),
		Months:          months,
		MRR:             incomeAmt(period, MRR) / int64(months),
		Revenue:         revenue,
		COGS:            cogs,
		Burn:            burn,
		GrossProfit:     revenue - cogs,
		NetIncome:       revenue - burn,
		Cash:            assetAmt(cumulative, Bank) + assetAmt(cumulative, SquareClearing),
		DeferredRevenue: liabilityAmt(cumulative, CustomerWallet),
	}
	m.ARR = m.MRR * 12
	if revenue > 0 {
		m.GrossMarginBps = m.GrossProfit * 10000 / revenue
	}
	// Runway: only a NET LOSS burns cash. A profitable (or break-even) period consumes no
	// runway, so MonthlyBurn is 0 and runway is the INFINITE sentinel (-1). When burning,
	// runway is whole months of cash left; already-out-of-cash clamps to 0, never negative.
	if m.NetIncome < 0 {
		m.MonthlyBurn = (-m.NetIncome) / int64(months)
	}
	m.RunwayMonths = -1
	if m.MonthlyBurn > 0 {
		if m.Cash <= 0 {
			m.RunwayMonths = 0
		} else {
			m.RunwayMonths = m.Cash / m.MonthlyBurn
		}
	}
	return m
}

// MetricsResponse is the GET /v1/books/metrics payload: the raw deterministic snapshot
// (int64 cents, the canonical numbers) PLUS the same figures already formatted through the
// ONE money formatter (formatUSD), so a consumer surfaces books' numbers in books' format
// and never re-derives either. It is the single grounded read the unified /v1/ask advisor
// composes — every figure is the ledger, aggregated, never a guess.
type MetricsResponse struct {
	Metrics
	Figures []Figure `json:"figures"`
}

// metricsFigures renders the COMPLETE metric snapshot as formatted figures, in the order a
// founder reads a one-line business summary. It reuses the shared formatters (formatUSD,
// marginPct, runwayValue) so a figure here is byte-identical to the same figure anywhere in
// the books surface — books owns both the number and its format.
func metricsFigures(m Metrics) []Figure {
	p := m.Period
	fig := func(label, value string) Figure { return Figure{Label: label, Value: value, Period: p} }
	return []Figure{
		fig("Revenue", formatUSD(m.Revenue)),
		fig("MRR", formatUSD(m.MRR)),
		fig("ARR", formatUSD(m.ARR)),
		fig("Gross margin", marginPct(m)),
		fig("Gross profit", formatUSD(m.GrossProfit)),
		fig("COGS", formatUSD(m.COGS)),
		fig("Burn", formatUSD(m.Burn)),
		fig("Net income", formatUSD(m.NetIncome)),
		fig("Cash", formatUSD(m.Cash)),
		fig("Deferred revenue", formatUSD(m.DeferredRevenue)),
		fig("Runway", runwayValue(m)),
	}
}

// net is an account's signed net (debit − credit) over a sums window. The four *Amt helpers
// place it on the account's NATURAL display sign, exactly as the statements do: assets and
// expenses are debit-normal (shown as net), income and liabilities credit-normal (flipped).
func net(m sums, acct string) int64 { dc := m[acct]; return dc[0] - dc[1] }

func incomeAmt(m sums, acct string) int64    { return -net(m, acct) }
func expenseAmt(m sums, acct string) int64   { return net(m, acct) }
func assetAmt(m sums, acct string) int64     { return net(m, acct) }
func liabilityAmt(m sums, acct string) int64 { return -net(m, acct) }

// monthsBetween is the window length in whole months (≥1), used to turn period revenue into
// a monthly run-rate and period loss into a monthly burn. An open or unparseable window
// (all-time, or a bare date) is treated as ONE reporting period — the deterministic default
// the Ask brain and tests rely on, so a single month of data reads as MRR == that month's
// recurring revenue.
func monthsBetween(from, to string) int {
	f, ferr := time.Parse(time.RFC3339, from)
	t, terr := time.Parse(time.RFC3339, to)
	if ferr != nil || terr != nil || !t.After(f) {
		return 1
	}
	days := t.Sub(f).Hours() / 24
	months := int((days + 15.0) / 30.4375) // round to nearest month
	if months < 1 {
		return 1
	}
	return months
}

// periodLabel is the human period tag for a figure: the YYYY-MM of `to`, or "all-time" when
// `to` is open/unparseable. It is presentation only — the metric window is [from, to].
func periodLabel(to string) string {
	if t, err := time.Parse(time.RFC3339, to); err == nil {
		return t.Format("2006-01")
	}
	return "all-time"
}

// formatUSD renders exact cents as a grouped USD string, dropping a whole-dollar ".00" so a
// figure reads "$12,340" but a fractional one keeps its cents "$12,340.50". A negative
// amount carries the sign OUTSIDE the dollar sign ("-$100"). It is the ONE money formatter
// the Ask brain and the clarifying-questions detector share, so every surfaced figure reads
// identically.
func formatUSD(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	dollars := cents / 100
	rem := cents % 100
	s := "$" + group(dollars)
	if rem != 0 {
		s += "." + twoDigits(rem)
	}
	if neg {
		return "-" + s
	}
	return s
}

// group renders a non-negative integer with thousands separators (1234567 → "1,234,567").
func group(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	var out []byte
	for i := 0; i < len(digits); i++ {
		if i > 0 && i%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, digits[i])
	}
	// digits are least-significant-first; reverse into place.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func twoDigits(n int64) string {
	return string([]byte{byte('0' + n/10), byte('0' + n%10)})
}
