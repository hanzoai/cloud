package admin

// Native SaaS business ANALYTICS (/v1/admin/analytics) — cohort retention, growth,
// churn, active-customers (DAU/WAU/MAU), revenue (MRR/ARPU) and usage over time, derived
// from REAL fleet data: IAM org `createdTime` (the signup cohort) + the commerce
// transaction ledger (usage = `withdraw` rows). Global-admin/org-scoped via GuardScoped.
//
// The fleet activity model + the continuous spend series live in clients/admin/core
// (core.FleetActivity / core.SpendSeries / core.CustActivity / core.SeriesPoint) because
// the revenue board reuses them — one implementation, DRY. This file holds only the
// analytics-specific derivation (growth/retention/churn/active/LTV) that folds over that
// shared model.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/zap-proto/zip"
)

// ── wire shapes (operator contract) ──────────────────────────────────────────

// retentionCohort is one row of the retention triangle: a signup cohort, its size, and
// the % of it still ACTIVE at each subsequent period (values[0] = the signup period
// itself). Percentages are 0..100.
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
	Signups             []core.SeriesPoint `json:"signups"`
	CumulativeCustomers []core.SeriesPoint `json:"cumulativeCustomers"`
	TotalCustomers      int                `json:"totalCustomers"`
	NewCustomers        int                `json:"newCustomers"`
	GrowthRatePct       float64            `json:"growthRatePct"`

	// Active customers — from the usage ledger.
	ActiveCustomers []core.SeriesPoint `json:"activeCustomers"`
	DAU             int                `json:"dau"`
	WAU             int                `json:"wau"`
	MAU             int                `json:"mau"`

	// Retention triangle — signup cohort × active period.
	Retention retentionGrid `json:"retention"`

	// Churn — logo churn (count) + rate.
	Churn        []core.SeriesPoint `json:"churn"`
	ChurnRatePct float64            `json:"churnRatePct"`

	// Revenue analytics.
	MRRCents  int64              `json:"mrrCents"`
	Revenue   []core.SeriesPoint `json:"revenue"`
	ARPUCents int64              `json:"arpuCents"`
	LTVCents  *int64             `json:"ltvCents"` // null until churn is observed
	NRRPct    *float64           `json:"nrrPct"`   // null — needs MRR history

	// Usage analytics.
	Usage        []core.SeriesPoint `json:"usage"`
	TopCustomers []analyticsSlice   `json:"topCustomers"`

	// Transparency: which metrics are backed by real data vs honest-empty.
	Computed map[string]bool     `json:"computed"`
	Sources  []core.SourceStatus `json:"sources"`
}

// analyticsSlice is a labelled magnitude (top customers by usage cents).
type analyticsSlice struct {
	Label string `json:"label"`
	Value int64  `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

// ── handler ──────────────────────────────────────────────────────────────────

func analytics(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	now := time.Now().UTC()
	rangeStr := normalizeRange(c.Query("range"))
	since, interval, _ := rangeWindow(rangeStr, now)

	var sources []core.SourceStatus

	// Scoped fan-in: a SuperAdmin gets every org (all-orgs SaaS analytics); an org admin
	// gets ONLY their own subtree — the ONE tenant-scope predicate (core.ScopedOrgs).
	orgs, err := core.ScopedOrgs(s, ctx, c, cr)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	sources = append(sources, core.SrcOf("iam", nil, len(orgs), now.Format(time.RFC3339)))

	acts, ledgerOK := core.FleetActivity(s, ctx, orgs)
	ledgerRows := 0
	for _, a := range acts {
		ledgerRows += len(a.Usage)
	}
	var ledgerErr error
	if !ledgerOK {
		ledgerErr = core.ErrPartialRevenue // partial ledger read — mark degraded
	}
	sources = append(sources, core.SrcOf("commerce-ledger", ledgerErr, ledgerRows, now.Format(time.RFC3339)))

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
	return core.OK(c, data)
}

// analyticsInput is everything computeAnalytics needs — no I/O, so the whole SaaS
// analytics derivation is unit-testable without a network.
type analyticsInput struct {
	acts     []core.CustActivity
	mrrCents int64
	now      time.Time
	since    time.Time
	interval string
	rangeStr string
	ledgerOK bool // the ledger read yielded real usage rows
}

// computeAnalytics is the PURE derivation of every analytics metric from the real
// activity model. Growth is always computed (signup timestamps); the ledger-backed
// metrics compute from real usage when present and degrade to honest empty/zero when the
// fleet has no usage yet — never a fabricated curve. `computed` flags each.
func computeAnalytics(in analyticsInput) analyticsData {
	buckets := core.EnumerateBuckets(in.since, in.now, in.interval)

	// ── Growth (IAM createdTime — always real) ──
	signups := make([]core.SeriesPoint, len(buckets))
	newCount := 0
	total := 0
	for i, b := range buckets {
		signups[i] = core.SeriesPoint{T: b}
	}
	idx := core.IndexOf(buckets)
	for _, a := range in.acts {
		if !a.HasCreated {
			continue
		}
		total++
		if !a.Created.Before(in.since) {
			newCount++
		}
		if i, ok := idx[core.BucketKeyOf(a.Created, in.interval)]; ok {
			signups[i].Value++
		}
	}
	// Cumulative customers across the SAME buckets (all-time count at each bucket end).
	cumulative := make([]core.SeriesPoint, len(buckets))
	for i, b := range buckets {
		end := bucketEnd(b, in.interval)
		n := 0
		for _, a := range in.acts {
			if a.HasCreated && !a.Created.After(end) {
				n++
			}
		}
		cumulative[i] = core.SeriesPoint{T: b, Value: int64(n)}
	}
	growthRate := priorWindowGrowth(in.acts, in.since, in.now)

	// ── Active customers + usage (ledger-backed) ──
	usage := core.SpendSeries(in.acts, in.since, in.now, in.interval)
	active := make([]core.SeriesPoint, len(buckets))
	for i, b := range buckets {
		active[i] = core.SeriesPoint{T: b}
	}
	for _, a := range in.acts {
		// active in a bucket = at least one usage event in it
		seen := map[string]bool{}
		for _, p := range a.Usage {
			if p.T.Before(in.since) || p.T.After(in.now) {
				continue
			}
			seen[core.BucketKeyOf(p.T, in.interval)] = true
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
		// LTV ≈ ARPU / monthly churn rate — computed ONLY when real churn is observed.
		v := int64(float64(arpu) / (churnRate / 100.0))
		ltv = &v
	}

	// ── Top customers by usage ──
	top := topCustomersByUsage(in.acts, 10)

	// Revenue series = realized usage revenue per bucket (same as usage cents for a
	// pay-as-you-go fleet; distinct field so the console can theme it as revenue).
	revenue := make([]core.SeriesPoint, len(usage))
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

// computeRetention builds the cohort × period retention triangle from real signup months
// and usage months. retention[c][k] = fraction of cohort c ACTIVE in month c+k. Cohorts
// are capped to the last `maxCohorts` months; a cohort with no signups is omitted. Values
// are 0..100.
func computeRetention(acts []core.CustActivity, now time.Time, maxCohorts int) retentionGrid {
	// Group customers by signup month.
	byCohort := map[string][]core.CustActivity{}
	for _, a := range acts {
		if !a.HasCreated {
			continue
		}
		k := core.MonthKey(a.Created)
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

	nowMonth := core.MonthKey(now)
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
				if m.ActiveIn(month, "month") {
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

// computeChurn derives monthly LOGO churn: a customer counts as churned in month M if
// they were active in M-1 but NOT in M. The rate is the average monthly churn over the
// observed window. Returns honest zeros when there is no usage history.
func computeChurn(acts []core.CustActivity, now time.Time, months int) ([]core.SeriesPoint, float64) {
	// Build the last `months` month keys ending at now.
	keys := lastMonths(now, months)
	series := make([]core.SeriesPoint, len(keys))
	var churnedTotal, baseTotal int
	for i, m := range keys {
		series[i] = core.SeriesPoint{T: m}
		if i == 0 {
			continue // no prior month to compare
		}
		prev := keys[i-1]
		churned := 0
		base := 0
		for _, a := range acts {
			wasActive := a.ActiveIn(prev, "month")
			if wasActive {
				base++
				if !a.ActiveIn(m, "month") {
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

// topCustomersByUsage returns the top-N customers by total usage cents (desc).
func topCustomersByUsage(acts []core.CustActivity, n int) []analyticsSlice {
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

// priorWindowGrowth is the signup growth vs the immediately-preceding window: the %
// change in new signups this window vs last. 0 when the prior window had none.
func priorWindowGrowth(acts []core.CustActivity, since, now time.Time) float64 {
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
func activeWithin(acts []core.CustActivity, cut time.Time) int {
	n := 0
	for _, a := range acts {
		if a.ActiveSince(cut) {
			n++
		}
	}
	return n
}

// fleetMRR sums each org's active-subscription MRR concurrently.
func fleetMRR(s *cloud.Service[core.State], ctx context.Context, orgs []iam.Org) int64 {
	vals := make([]int64, len(orgs))
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			if pl, err := s.State.Commerce.Plan(ctx, o.Name); err == nil {
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

// ── analytics-specific month arithmetic (the shared bucket keys live in core) ──

// bucketEnd returns the inclusive end instant of a bucket key (for the cumulative count).
// A day/week/month key advances one unit; the end is one nanosecond before.
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
		out = append(out, core.MonthKey(now.AddDate(0, -i, 0)))
	}
	return out
}

func pct(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return (float64(part) / float64(whole)) * 100
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
