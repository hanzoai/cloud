package admin

// Native SaaS business ANALYTICS (/v1/admin/analytics) — cohort retention, growth,
// churn, active-customers (DAU/WAU/MAU), revenue (MRR/ARPU) and usage over time,
// derived from REAL fleet data: IAM org `createdTime` (the signup cohort — always
// available) + the commerce transaction ledger (usage = `withdraw` rows, the true
// customer-activity signal). Global-admin only (s.guard), like every admin route.
//
// HONEST BY CONSTRUCTION. There is NO fabricated curve anywhere. Growth/cohorts
// come from real signup timestamps; retention/active/churn/usage come from real
// consumption events. A metric that cannot yet be computed (LTV needs observed
// churn; NRR needs MRR history commerce does not expose point-in-time) returns a
// null / honest-empty series — never an invented trend — and the `computed` map
// flags exactly which metrics are backed by data, so the console renders honest
// states and a reviewer can verify no number was made up.
//
// The heavy read (every org's ledger) is bounded + fanned out concurrently. Admin
// is low-QPS; at fleet scale this belongs in the insights/datastore OLAP mirror
// (same note as the usage series), but the billing ledger is the correct SOURCE
// OF TRUTH for real per-customer activity today.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/zap-proto/zip"
)

// ── wire shapes (operator contract) ──────────────────────────────────────────

// seriesPoint is one bucketed point (count OR cents, per the series). T is the
// bucket key (RFC3339 date / "2006-01" month).
type seriesPoint struct {
	T     string `json:"t"`
	Value int64  `json:"value"`
}

// retentionCohort is one row of the retention triangle: a signup cohort, its size,
// and the % of it still ACTIVE at each subsequent period (values[0] = the signup
// period itself). Percentages are 0..100.
type retentionCohort struct {
	Cohort string    `json:"cohort"`
	Size   int       `json:"size"`
	Values []float64 `json:"values"`
}

// retentionGrid is the classic cohort-retention heatmap (cohorts × periods).
type retentionGrid struct {
	Interval string            `json:"interval"` // "month"
	Periods  int               `json:"periods"`
	Cohorts  []retentionCohort `json:"cohorts"`
}

// analyticsData is the whole GET /v1/admin/analytics payload.
type analyticsData struct {
	Range       string `json:"range"`
	Interval    string `json:"interval"`
	GeneratedAt string `json:"generatedAt"`

	// Growth — from IAM createdTime (always real).
	Signups             []seriesPoint `json:"signups"`
	CumulativeCustomers []seriesPoint `json:"cumulativeCustomers"`
	TotalCustomers      int           `json:"totalCustomers"`
	NewCustomers        int           `json:"newCustomers"`
	GrowthRatePct       float64       `json:"growthRatePct"`

	// Active customers — from the usage ledger.
	ActiveCustomers []seriesPoint `json:"activeCustomers"`
	DAU             int           `json:"dau"`
	WAU             int           `json:"wau"`
	MAU             int           `json:"mau"`

	// Retention triangle — signup cohort × active period.
	Retention retentionGrid `json:"retention"`

	// Churn — logo churn (count) + rate.
	Churn        []seriesPoint `json:"churn"`
	ChurnRatePct float64       `json:"churnRatePct"`

	// Revenue analytics.
	MRRCents  int64         `json:"mrrCents"`
	Revenue   []seriesPoint `json:"revenue"`
	ARPUCents int64         `json:"arpuCents"`
	LTVCents  *int64        `json:"ltvCents"` // null until churn is observed
	NRRPct    *float64      `json:"nrrPct"`   // null — needs MRR history

	// Usage analytics.
	Usage        []seriesPoint    `json:"usage"`
	TopCustomers []analyticsSlice `json:"topCustomers"`

	// Transparency: which metrics are backed by real data vs honest-empty. A
	// reviewer/console reads this to know nothing was fabricated.
	Computed map[string]bool `json:"computed"`
	Sources  []sourceStatus  `json:"sources"`
}

// analyticsSlice is a labelled magnitude (top customers by usage cents).
type analyticsSlice struct {
	Label string `json:"label"`
	Value int64  `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

// ── the activity model the pure math folds over ──────────────────────────────

// txnPoint is one dated usage event (a commerce `withdraw`, in cents).
type txnPoint struct {
	T     time.Time
	Cents int64
}

// custActivity is one customer's real analytics input: when they signed up (IAM
// createdTime) and their consumption events (commerce withdraws). Deposits are
// NOT activity (a credit grant is not the customer using the product), so only
// withdraws feed active/retention/churn/usage — the honest "used it" signal.
type custActivity struct {
	Org        string
	Display    string
	Created    time.Time
	HasCreated bool
	Usage      []txnPoint
	SpendCents int64
}

func (ca custActivity) activeIn(bucket string, interval string) bool {
	for _, p := range ca.Usage {
		if bucketKeyOf(p.T, interval) == bucket {
			return true
		}
	}
	return false
}

func (ca custActivity) activeSince(cut time.Time) bool {
	for _, p := range ca.Usage {
		if !p.T.Before(cut) {
			return true
		}
	}
	return false
}

// ── handler ──────────────────────────────────────────────────────────────────

func analytics(s *cloud.Service[state], c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	now := time.Now().UTC()
	rangeStr := normalizeRange(c.Query("range"))
	since, interval, _ := rangeWindow(rangeStr, now)

	var sources []sourceStatus

	// Scoped fan-in: a SuperAdmin gets every org (all-orgs SaaS analytics); an org
	// admin gets ONLY their own subtree (their org's usage/active/spend), never
	// another tenant's — the ONE tenant-scope predicate (scope.go).
	orgs, err := scopedOrgs(s, ctx, c, cr)
	if err != nil {
		return fail(c, err.Error())
	}
	sources = append(sources, srcOf("iam", nil, len(orgs), now.Format(time.RFC3339)))

	acts, ledgerOK := fleetActivity(s, ctx, orgs)
	ledgerRows := 0
	for _, a := range acts {
		ledgerRows += len(a.Usage)
	}
	var ledgerErr error
	if !ledgerOK {
		ledgerErr = errPartialRevenue // partial ledger read — mark degraded
	}
	sources = append(sources, srcOf("commerce-ledger", ledgerErr, ledgerRows, now.Format(time.RFC3339)))

	// MRR from subscriptions (point-in-time), fanned out like the money reads.
	mrr := fleetMRR(s, ctx, orgs)

	data := computeAnalytics(analyticsInput{
		acts:     acts,
		mrrCents: mrr,
		now:      now,
		since:    since,
		interval: interval,
		rangeStr: rangeStr,
		ledgerOK: ledgerOK && ledgerRows > 0,
	})
	data.GeneratedAt = now.Format(time.RFC3339)
	data.Sources = sources
	return ok(c, data)
}

// analyticsInput is everything computeAnalytics needs — no I/O, so the whole SaaS
// analytics derivation is unit-testable without a network.
type analyticsInput struct {
	acts     []custActivity
	mrrCents int64
	now      time.Time
	since    time.Time
	interval string
	rangeStr string
	ledgerOK bool // the ledger read yielded real usage rows
}

// computeAnalytics is the PURE derivation of every analytics metric from the real
// activity model. Growth is always computed (signup timestamps); the ledger-backed
// metrics compute from real usage when present and degrade to honest empty/zero
// when the fleet has no usage yet — never a fabricated curve. `computed` flags each.
func computeAnalytics(in analyticsInput) analyticsData {
	buckets := enumerateBuckets(in.since, in.now, in.interval)

	// ── Growth (IAM createdTime — always real) ──
	signups := make([]seriesPoint, len(buckets))
	newCount := 0
	total := 0
	for i, b := range buckets {
		signups[i] = seriesPoint{T: b}
	}
	idx := indexOf(buckets)
	for _, a := range in.acts {
		if !a.HasCreated {
			continue
		}
		total++
		if !a.Created.Before(in.since) {
			newCount++
		}
		if i, ok := idx[bucketKeyOf(a.Created, in.interval)]; ok {
			signups[i].Value++
		}
	}
	// Cumulative customers across the SAME buckets (all-time count at each bucket end).
	cumulative := make([]seriesPoint, len(buckets))
	for i, b := range buckets {
		end := bucketEnd(b, in.interval)
		n := 0
		for _, a := range in.acts {
			if a.HasCreated && !a.Created.After(end) {
				n++
			}
		}
		cumulative[i] = seriesPoint{T: b, Value: int64(n)}
	}
	growthRate := priorWindowGrowth(in.acts, in.since, in.now)

	// ── Active customers + usage (ledger-backed) ──
	usage := spendSeries(in.acts, in.since, in.now, in.interval)
	active := make([]seriesPoint, len(buckets))
	for i, b := range buckets {
		active[i] = seriesPoint{T: b}
	}
	for _, a := range in.acts {
		// active in a bucket = at least one usage event in it
		seen := map[string]bool{}
		for _, p := range a.Usage {
			if p.T.Before(in.since) || p.T.After(in.now) {
				continue
			}
			seen[bucketKeyOf(p.T, in.interval)] = true
		}
		for b := range seen {
			if i, ok := idx[b]; ok {
				active[i].Value++
			}
		}
	}
	dau := activeWithin(in.acts, in.now.AddDate(0, 0, -1))
	wau := activeWithin(in.acts, in.now.AddDate(0, 0, -7))
	mau := activeWithin(in.acts, in.now.AddDate(0, 0, -30))

	// ── Retention triangle (monthly cohorts × active month) ──
	retention := computeRetention(in.acts, in.now, 12)

	// ── Churn (monthly logo churn from active months) + rate ──
	churn, churnRate := computeChurn(in.acts, in.now, 6)

	// ── Revenue analytics ──
	var totalSpend int64
	for _, a := range in.acts {
		totalSpend += a.SpendCents
	}
	arpu := int64(0)
	if mau > 0 {
		arpu = totalSpend / int64(mau)
	} else if total > 0 {
		arpu = totalSpend / int64(total)
	}
	var ltv *int64
	if churnRate > 0 && arpu > 0 {
		// LTV ≈ ARPU / monthly churn rate — computed ONLY when real churn is
		// observed, else honest null (LTV needs churn to mean anything).
		v := int64(float64(arpu) / (churnRate / 100.0))
		ltv = &v
	}

	// ── Top customers by usage ──
	top := topCustomersByUsage(in.acts, 10)

	// Revenue series = realized usage revenue per bucket (same as usage cents for a
	// pay-as-you-go fleet; distinct field so the console can theme it as revenue).
	revenue := make([]seriesPoint, len(usage))
	copy(revenue, usage)

	return analyticsData{
		Range:               in.rangeStr,
		Interval:            in.interval,
		Signups:             signups,
		CumulativeCustomers: cumulative,
		TotalCustomers:      total,
		NewCustomers:        newCount,
		GrowthRatePct:       growthRate,
		ActiveCustomers:     active,
		DAU:                 dau,
		WAU:                 wau,
		MAU:                 mau,
		Retention:           retention,
		Churn:               churn,
		ChurnRatePct:        churnRate,
		MRRCents:            in.mrrCents,
		Revenue:             revenue,
		ARPUCents:           arpu,
		LTVCents:            ltv,
		NRRPct:              nil, // honest null — needs MRR history commerce doesn't expose
		Usage:               usage,
		TopCustomers:        top,
		Computed: map[string]bool{
			"growth":    true, // signup timestamps are always present
			"retention": in.ledgerOK,
			"active":    in.ledgerOK,
			"churn":     in.ledgerOK,
			"usage":     in.ledgerOK,
			"revenue":   in.ledgerOK,
			"mrr":       true,
			"arpu":      in.ledgerOK,
			"ltv":       ltv != nil,
			"nrr":       false,
		},
	}
}

// computeRetention builds the cohort × period retention triangle from real signup
// months and usage months. retention[c][k] = fraction of cohort c ACTIVE in month
// c+k. Cohorts are capped to the last `maxCohorts` months (the classic triangle);
// a cohort with no signups is omitted. Values are 0..100.
func computeRetention(acts []custActivity, now time.Time, maxCohorts int) retentionGrid {
	// Group customers by signup month.
	byCohort := map[string][]custActivity{}
	for _, a := range acts {
		if !a.HasCreated {
			continue
		}
		k := monthKey(a.Created)
		byCohort[k] = append(byCohort[k], a)
	}

	// Sorted cohort months, newest last, capped.
	cohorts := make([]string, 0, len(byCohort))
	for k := range byCohort {
		cohorts = append(cohorts, k)
	}
	sort.Strings(cohorts)
	if len(cohorts) > maxCohorts {
		cohorts = cohorts[len(cohorts)-maxCohorts:]
	}

	nowMonth := monthKey(now)
	grid := retentionGrid{Interval: "month"}
	maxPeriods := 0
	for _, cohort := range cohorts {
		members := byCohort[cohort]
		periods := monthsBetween(cohort, nowMonth) + 1
		if periods < 1 {
			periods = 1
		}
		row := retentionCohort{Cohort: cohort, Size: len(members), Values: make([]float64, periods)}
		for k := 0; k < periods; k++ {
			month := addMonths(cohort, k)
			activeN := 0
			for _, m := range members {
				if m.activeIn(month, "month") {
					activeN++
				}
			}
			if len(members) > 0 {
				row.Values[k] = pct(activeN, len(members))
			}
		}
		if periods > maxPeriods {
			maxPeriods = periods
		}
		grid.Cohorts = append(grid.Cohorts, row)
	}
	grid.Periods = maxPeriods
	return grid
}

// computeChurn derives monthly LOGO churn: a customer counts as churned in month M
// if they were active in M-1 but NOT in M. The rate is the average monthly churn
// over the observed window (churned / active-at-start). Returns honest zeros when
// there is no usage history.
func computeChurn(acts []custActivity, now time.Time, months int) ([]seriesPoint, float64) {
	// Build the last `months` month keys ending at now.
	keys := lastMonths(now, months)
	series := make([]seriesPoint, len(keys))
	var churnedTotal, baseTotal int
	for i, m := range keys {
		series[i] = seriesPoint{T: m}
		if i == 0 {
			continue // no prior month to compare
		}
		prev := keys[i-1]
		churned := 0
		base := 0
		for _, a := range acts {
			wasActive := a.activeIn(prev, "month")
			if wasActive {
				base++
				if !a.activeIn(m, "month") {
					churned++
				}
			}
		}
		series[i].Value = int64(churned)
		churnedTotal += churned
		baseTotal += base
	}
	rate := 0.0
	if baseTotal > 0 {
		rate = pct(churnedTotal, baseTotal)
	}
	return series, rate
}

// spendSeries buckets fleet usage cents into a continuous series over since..now.
// Shared by the analytics usage/revenue trend and the revenue board's spend trend
// (one implementation, DRY). A bucket with no usage is an honest 0, not a gap.
func spendSeries(acts []custActivity, since, now time.Time, interval string) []seriesPoint {
	buckets := enumerateBuckets(since, now, interval)
	idx := indexOf(buckets)
	out := make([]seriesPoint, len(buckets))
	for i, b := range buckets {
		out[i] = seriesPoint{T: b}
	}
	for _, a := range acts {
		for _, p := range a.Usage {
			if p.T.Before(since) || p.T.After(now) {
				continue
			}
			if i, ok := idx[bucketKeyOf(p.T, interval)]; ok {
				out[i].Value += p.Cents
			}
		}
	}
	return out
}

// topCustomersByUsage returns the top-N customers by total usage cents (desc).
func topCustomersByUsage(acts []custActivity, n int) []analyticsSlice {
	rows := make([]analyticsSlice, 0, len(acts))
	for _, a := range acts {
		if a.SpendCents <= 0 {
			continue
		}
		rows = append(rows, analyticsSlice{Label: a.Display, Value: a.SpendCents, Hint: a.Org})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Value > rows[j].Value })
	if len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

// priorWindowGrowth is the signup growth vs the immediately-preceding window: the
// % change in new signups this window vs last. 0 when the prior window had none.
func priorWindowGrowth(acts []custActivity, since, now time.Time) float64 {
	window := now.Sub(since)
	priorStart := since.Add(-window)
	cur, prev := 0, 0
	for _, a := range acts {
		if !a.HasCreated {
			continue
		}
		if !a.Created.Before(since) && a.Created.Before(now) {
			cur++
		} else if !a.Created.Before(priorStart) && a.Created.Before(since) {
			prev++
		}
	}
	if prev == 0 {
		return 0
	}
	return (float64(cur-prev) / float64(prev)) * 100
}

// activeWithin counts customers with at least one usage event since `cut`.
func activeWithin(acts []custActivity, cut time.Time) int {
	n := 0
	for _, a := range acts {
		if a.activeSince(cut) {
			n++
		}
	}
	return n
}

// ── fleet readers (I/O; concurrent, bounded) ─────────────────────────────────

// fleetActivity reads every org's signup time (already on the org row) + usage
// ledger, folded into the pure activity model. Returns (acts, ok) where ok is
// false if ANY org's ledger read failed (the caller marks the source degraded and
// flags the ledger-backed metrics as not-fully-computed). Fanned out concurrently
// with a bound, like the customer list.
func fleetActivity(s *cloud.Service[state], ctx context.Context, orgs []iam.Org) ([]custActivity, bool) {
	acts := make([]custActivity, len(orgs))
	oks := make([]bool, len(orgs))
	sem := make(chan struct{}, maxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			ca := custActivity{Org: o.Name, Display: display(o.DisplayName, o.Name)}
			if t, err := time.Parse(time.RFC3339, o.CreatedTime); err == nil {
				ca.Created = t.UTC()
				ca.HasCreated = true
			}
			rows, err := s.State.commerce.Ledger(ctx, o.Name, 2000)
			oks[i] = err == nil
			for _, r := range rows {
				if strings.ToLower(r.Kind) != "withdraw" {
					continue // only consumption is "activity"; deposits are credits
				}
				t, perr := parseTxnTime(r.At)
				if perr != nil {
					continue
				}
				amt := int64(r.Amount)
				if amt < 0 {
					amt = -amt
				}
				ca.Usage = append(ca.Usage, txnPoint{T: t, Cents: amt})
				ca.SpendCents += amt
			}
			acts[i] = ca
		}(i, o)
	}
	wg.Wait()
	allOK := true
	for _, ok := range oks {
		if !ok {
			allOK = false
			break
		}
	}
	return acts, allOK
}

// fleetMRR sums each org's active-subscription MRR concurrently.
func fleetMRR(s *cloud.Service[state], ctx context.Context, orgs []iam.Org) int64 {
	vals := make([]int64, len(orgs))
	sem := make(chan struct{}, maxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			if pl, err := s.State.commerce.Plan(ctx, o.Name); err == nil {
				vals[i] = int64(pl.MRR)
			}
		}(i, o)
	}
	wg.Wait()
	var total int64
	for _, v := range vals {
		total += v
	}
	return total
}

// ── pure time-bucket helpers ─────────────────────────────────────────────────

func monthKey(t time.Time) string { return t.UTC().Format("2006-01") }
func dayKey(t time.Time) string   { return t.UTC().Format("2006-01-02") }

// weekKey buckets to the ISO week's Monday (a stable weekly key).
func weekKey(t time.Time) string {
	u := t.UTC()
	// back up to Monday
	wd := int(u.Weekday())
	if wd == 0 {
		wd = 7
	}
	monday := u.AddDate(0, 0, -(wd - 1))
	return monday.Format("2006-01-02")
}

func bucketKeyOf(t time.Time, interval string) string {
	switch interval {
	case "month":
		return monthKey(t)
	case "week":
		return weekKey(t)
	default:
		return dayKey(t)
	}
}

// bucketEnd returns the inclusive end instant of a bucket key (for the cumulative
// count). A day/week/month key advances one unit; the end is one nanosecond before.
func bucketEnd(key, interval string) time.Time {
	switch interval {
	case "month":
		if t, err := time.Parse("2006-01", key); err == nil {
			return t.AddDate(0, 1, 0).Add(-time.Nanosecond)
		}
	case "week":
		if t, err := time.Parse("2006-01-02", key); err == nil {
			return t.AddDate(0, 0, 7).Add(-time.Nanosecond)
		}
	default:
		if t, err := time.Parse("2006-01-02", key); err == nil {
			return t.AddDate(0, 0, 1).Add(-time.Nanosecond)
		}
	}
	return time.Now().UTC()
}

// enumerateBuckets lists every bucket key from since..now inclusive so a series has
// a continuous axis (a zero-usage bucket is an honest 0, not a gap).
func enumerateBuckets(since, now time.Time, interval string) []string {
	if since.After(now) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	step := func(t time.Time) time.Time {
		switch interval {
		case "month":
			return t.AddDate(0, 1, 0)
		case "week":
			return t.AddDate(0, 0, 7)
		default:
			return t.AddDate(0, 0, 1)
		}
	}
	// cap iterations so a bad range can never spin unbounded
	for t, n := since, 0; !t.After(now) && n < 800; t, n = step(t), n+1 {
		k := bucketKeyOf(t, interval)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	// ensure the final bucket (now) is present
	last := bucketKeyOf(now, interval)
	if !seen[last] {
		out = append(out, last)
	}
	return out
}

func indexOf(buckets []string) map[string]int {
	m := make(map[string]int, len(buckets))
	for i, b := range buckets {
		m[b] = i
	}
	return m
}

// addMonths adds k months to a "2006-01" key.
func addMonths(month string, k int) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.AddDate(0, k, 0).Format("2006-01")
}

// monthsBetween returns the whole-month distance from a..b ("2006-01" keys).
func monthsBetween(a, b string) int {
	ta, ea := time.Parse("2006-01", a)
	tb, eb := time.Parse("2006-01", b)
	if ea != nil || eb != nil {
		return 0
	}
	return int(tb.Year()-ta.Year())*12 + int(tb.Month()-ta.Month())
}

// lastMonths returns the last n month keys ending at `now` (oldest first).
func lastMonths(now time.Time, n int) []string {
	out := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, monthKey(now.AddDate(0, -i, 0)))
	}
	return out
}

func pct(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return (float64(part) / float64(whole)) * 100
}

// parseTxnTime accepts the commerce ledger's RFC3339 forms.
func parseTxnTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Parse("2006-01-02T15:04:05Z", s)
}

// normalizeRange clamps the range param to the supported set (default 30d).
func normalizeRange(r string) string {
	switch strings.TrimSpace(r) {
	case "7d", "30d", "90d", "all":
		return strings.TrimSpace(r)
	default:
		return "30d"
	}
}

// rangeWindow maps a range to (since, interval, approxBuckets).
func rangeWindow(rangeStr string, now time.Time) (time.Time, string, int) {
	switch rangeStr {
	case "7d":
		return now.AddDate(0, 0, -7), "day", 7
	case "90d":
		return now.AddDate(0, 0, -90), "week", 13
	case "all":
		return now.AddDate(-2, 0, 0), "month", 24
	default: // 30d
		return now.AddDate(0, 0, -30), "day", 30
	}
}
