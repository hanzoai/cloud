package risk

// baseline.go — the ENTIRE cross-org surface, and the argument for why it is safe.
//
// Learning from an organisation's own first-party data is the moat. Learning
// across organisations is the thing that would destroy it, so the cross-org
// surface is exactly one table and it is AGGREGATE-ONLY:
//
//	hanzo.risk_baseline(bucket, subject_kind, dim, q10, q50, q90, orgs, n)
//
// There is NO org column. No subject, no identifier, no pseudonym, no hash of
// one. A row is three quantiles of one dim over one day, plus how many
// organisations and how many buckets went into them. That is not a redaction of
// per-tenant data — there is nothing to redact, because the shape cannot hold a
// tenant. The leak is UNCOMPUTABLE rather than merely disallowed, which is the
// same argument the qualified tenant key makes one layer down.
//
// Three further gates, because "aggregate" alone is not anonymity:
//
//   - k-ANONYMITY. A bucket only one organisation contributed to is that
//     organisation's data wearing a quantile's clothes. A bucket is published
//     only when at least [kAnonOrgs] distinct organisations and [kAnonRows]
//     buckets went into it, and the gate is enforced TWICE: in the statement's
//     HAVING (bound, never interpolated) and again on read, so a row written
//     before the gate existed is still refused.
//
//   - ONE ORGANISATION, ONE VOTE. Counting CONTRIBUTORS is not the same as
//     bounding WEIGHT, and only the second is anonymity. Twenty-five
//     organisations satisfy the floor while one of them supplies a million of the
//     million-and-twenty-four values, and then the published median IS that
//     organisation's median — its own distribution, republished under a name that
//     says it is everyone's. So each organisation is reduced to ONE number, its
//     own median for the day, BEFORE any quantile is taken over organisations.
//     Every contributor's share is then exactly 1/orgs, which the floor bounds at
//     1/[kAnonOrgs] = 4%: a dominant tenant cannot move the aggregate, and a
//     colluding group has to actually be a majority of the contributors.
//
//   - THE ANONYMOUS LANE CONTRIBUTES NOTHING. The reserved `$public` tenant is
//     not an organisation; [qualify] refuses it, so no rollup can ever write it
//     into hanzo.risk_feature and therefore no quantile can ever be moved by an
//     unauthenticated stranger. The exclusion is at the mint, not in a WHERE
//     clause — a filter is a place to forget, a refusal is not.
//
// The statements below are PACKAGE CONSTANTS composed only from [dims], which is
// code. Nothing a caller sends reaches them, as an identifier or otherwise.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"
)

// baselineTable is the network baseline. It is the only table in this package
// that is not read under a tenant, and its shape is why that is safe.
const baselineTable = "hanzo.risk_baseline"

// baselineDDL declares the aggregate. Read the column list as the security
// argument: there is no place in it for a tenant.
//
// ReplacingMergeTree on (bucket, subject_kind, dim) so re-running a day
// converges on the newest computation of it rather than accumulating copies.
const baselineDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.risk_baseline (
		bucket        Date,
		subject_kind  LowCardinality(String),
		dim           LowCardinality(String),
		q10           Float64,
		q50           Float64,
		q90           Float64,
		orgs          UInt32,
		n             UInt64
	) ENGINE = ReplacingMergeTree()
	ORDER BY (bucket, subject_kind, dim)
	TTL bucket + INTERVAL 400 DAY`

// The k-anonymity floor.
//
// kAnonOrgs is the number of DISTINCT organisations a published bucket must
// contain. Twenty-five is chosen so that a quantile cannot be attributed even
// when an adversary controls several of the contributing organisations: to move
// a median you must outnumber the rest, and to READ one you must know every
// other contributor's value.
//
// kAnonRows is the number of underlying subject-days, which bounds the other
// direction: twenty-five organisations each contributing one subject is
// twenty-five readable numbers, not a distribution.
const (
	kAnonOrgs = 25
	kAnonRows = 1000
)

// maxShare is the largest share of a published band any single organisation can
// hold, and it is arithmetic rather than an aspiration: each organisation
// contributes exactly one value ([populate] reduces it to its own median before
// the quantile), so its share is 1/orgs and the floor bounds that at 1/kAnonOrgs.
// [TestBaseline_NoOrganisationCanDominate] holds the property this number states.
const maxShare = 1.0 / float64(kAnonOrgs)

// publishable is the k-anonymity gate as a PURE PREDICATE, used by both the
// statement that writes a bucket and the reader that returns one. One definition,
// two enforcement points: a row that predates the gate is refused on the way out.
func publishable(orgs uint32, n uint64) bool {
	return orgs >= kAnonOrgs && n >= kAnonRows
}

// populate is the ONE statement that writes the baseline, rendered per dim from
// the allowlist. Its placeholders bind, in order: the dim's published name, the
// window start, the window end, the organisation floor and the subject-day floor.
//
// THE OUTER QUANTILE IS INTERPOLATED AND THE INNER ONE IS EXACT, and the
// difference is the whole disclosure argument. `quantileExact` over k values
// SELECTS AN ELEMENT: bounding every contributor to one vote and then taking an
// exact quantile over exactly [kAnonOrgs] votes publishes one contributing
// organisation's own daily median, verbatim — a sharper leak than the domination
// the vote reduction removed, because twenty-four colluding organisations read
// the twenty-fifth's number exactly rather than merely moving it. `quantile` is
// ClickHouse's interpolating estimator: the published figure lies BETWEEN two
// organisations' values and is therefore nobody's. Inside one organisation there
// is nothing to disclose, so [vote] stays exact and stays modelled in Go.
//
// THE EXTREME LEVEL IS NOT PUBLISHED. At twenty-five votes a 99th percentile is
// the maximum however it is estimated, and a maximum is one organisation's value
// by definition. q10/q50/q90 are the levels a comparison actually reads.
//
// This is k-anonymity with bounded influence and it is NOT differential privacy:
// it bounds what one organisation can move and what one published figure can be
// attributed to, not what an adversary learns from many figures over time.
//
// THREE STAGES, and the middle one is the weight bound. The innermost reduces the
// surface to one value per SUBJECT per day. The middle reduces each ORGANISATION
// to one value — its own median over its own subjects — so every contributor
// enters the aggregate with weight one however many subjects it has. Only then is
// the quantile taken, over organisations. Without that middle stage the quantiles
// are a weighted average in which the largest tenant is the answer.
//
// `n` stays the count of underlying subject-days rather than the number of votes,
// because the floor it is checked against means "enough data", not "enough
// voters" — the voter floor is uniqExact(org), the other half of the HAVING.
//
// %s is filled ONLY from dim.Column — a package constant reached through
// [dimBy]. There is no path by which a caller's string becomes an identifier
// here, which [TestBaseline_StatementIsConstant] holds to.
func populate(d dim) string {
	return fmt.Sprintf(`INSERT INTO %s (bucket, subject_kind, dim, q10, q50, q90, orgs, n)
		SELECT b, subject_kind, ?, quantile(0.10)(x), quantile(0.50)(x),
		       quantile(0.90)(x), uniqExact(org), sum(subjects)
		FROM (
		  SELECT b, subject_kind, org, quantileExact(0.50)(sx) AS x, count() AS subjects
		  FROM (
		    SELECT toDate(bucket) AS b, subject_kind, org, subject, sum(%s) AS sx
		    FROM %s
		    WHERE bucket >= ? AND bucket < ?
		    GROUP BY b, subject_kind, org, subject
		  )
		  GROUP BY b, subject_kind, org
		)
		GROUP BY b, subject_kind
		HAVING uniqExact(org) >= ? AND sum(subjects) >= ?`,
		baselineTable, d.Column, featureTable)
}

// vote reduces one organisation's day to the ONE value it contributes to a
// published band: the median over its own subjects.
//
// It is the Go statement of the middle stage of [populate] — the reduction that
// makes every contributor's weight exactly one — and it exists so the property
// can be MEASURED ([TestBaseline_NoOrganisationCanDominate]) rather than only
// read off a SQL string. The two are held to each other by
// [TestBaseline_TheStatementVotesPerOrganisation], which fails if the SQL loses
// the stage this function models.
func vote(subjects []float64) float64 {
	if len(subjects) == 0 {
		return 0
	}
	xs := append([]float64(nil), subjects...)
	sort.Float64s(xs)
	// quantileExact(0.5) over n values takes the element at floor(0.5*n), which is
	// the upper of the two middles on an even count. Stated rather than assumed:
	// a model of a statement that rounds the other way is not a model of it.
	return xs[len(xs)/2]
}

// band is one published bucket of the network baseline: three quantiles of one
// dim on one day, and the two counts that prove the bucket is anonymous.
//
// The type is the argument. There is no field a tenant could be recovered from,
// and [TestBaseline_HasNoTenantColumn] reflects over it to keep it that way.
type band struct {
	Day  time.Time
	Kind string
	Dim  string
	Q10  float64
	Q50  float64
	Q90  float64
	Orgs uint32
	N    uint64
}

// recompute writes the baseline for a window. It takes NO tenant, and it cannot
// be given one: the statements read every organisation's contribution and emit
// only quantiles over them.
//
// It is a SCHEDULED job and there is no route to it. That is not an omission: a
// caller who could choose the window could choose one only their own
// organisation was active in, and read their own rows back out of the aggregate
// — which is the one way a k-anonymous table leaks.
func recompute(ctx context.Context, start, end time.Time) (int, error) {
	if !storeReady() {
		return 0, errStore
	}
	if err := ensure(ctx); err != nil {
		return 0, err
	}
	var wrote int
	for _, d := range dims {
		if err := storeExec(ctx, populate(d), d.Name, tsLiteral(start), tsLiteral(end), kAnonOrgs, kAnonRows); err != nil {
			return wrote, fmt.Errorf("risk: recompute baseline %q: %w", d.Name, err)
		}
		wrote++
	}
	return wrote, nil
}

// baseline reads the published bands for a window. It takes NO tenant — that is
// the whole point of the table — and it drops any band that does not satisfy
// [publishable], so a row written before the gate existed is still refused.
//
// A caller may narrow by dim, which resolves through the allowlist and is bound
// besides; naming an unknown dim is an error rather than a pass-through.
func baseline(ctx context.Context, dimName string, start, end time.Time) ([]band, error) {
	if !storeReady() {
		return nil, errStore
	}
	where := []string{"bucket >= ?", "bucket < ?"}
	args := []any{tsLiteral(start), tsLiteral(end)}
	if dimName != "" {
		if _, ok := dimBy[dimName]; !ok {
			return nil, fmt.Errorf("risk: unknown dim %q", dimName)
		}
		where = append(where, "dim = ?")
		args = append(args, dimName)
	}
	stmt := fmt.Sprintf(
		"SELECT bucket, subject_kind, dim, q10, q50, q90, orgs, n FROM %s WHERE %s ORDER BY bucket, subject_kind, dim LIMIT %d",
		baselineTable, strings.Join(where, " AND "), maxRows)
	raw, err := storeQuery(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("risk: read baseline: %w", err)
	}
	out := make([]band, 0, len(raw))
	for _, r := range raw {
		b := band{
			Day:  stamp(r["bucket"]),
			Kind: text(r["subject_kind"]),
			Dim:  text(r["dim"]),
		}
		b.Q10, _ = number(r["q10"])
		b.Q50, _ = number(r["q50"])
		b.Q90, _ = number(r["q90"])
		orgs, _ := number(r["orgs"])
		n, _ := number(r["n"])
		b.Orgs, b.N = uint32(orgs), uint64(n)
		if !publishable(b.Orgs, b.N) {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// ── the schedule ─────────────────────────────────────────────────────────────

// baselineEvery is how often the network baseline is recomputed, and
// baselineLag how far behind the present each run works.
//
// A day, one day back: the surface is bucketed by day, so recomputing the
// CURRENT day would publish a partial one and then publish it again — and a
// quantile over a partial day is a quantile over whoever happened to be awake.
// One full day behind is the earliest a day is complete in every timezone the
// fleet serves.
// baselineWait is how long a fresh process waits before its first run. A cold
// start has better things to do than a full-surface aggregation, and a pod in a
// restart loop must not turn that aggregation into a load test of the one
// warehouse the whole fleet reads.
const (
	baselineEvery = 24 * time.Hour
	baselineLag   = 48 * time.Hour
	baselineWait  = 5 * time.Minute
)

// schedule runs the baseline job for as long as ctx lives.
//
// It is idempotent by construction — the table replaces on (day, kind, dim) — so
// a replica that runs it while another already has converges rather than
// duplicating, and no election is needed to make it safe. A run that fails is
// logged and retried on the next tick: a stale baseline is a comparison nobody
// can make, never a wrong answer, because the reader drops what the floor
// refuses whatever is in the table.
func schedule(ctx context.Context, log luxlog.Logger) {
	first := time.NewTimer(baselineWait)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	t := time.NewTicker(baselineEvery)
	defer t.Stop()
	for {
		run(ctx, log)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// run recomputes yesterday's bands once.
func run(ctx context.Context, log luxlog.Logger) {
	if !storeReady() {
		return // an honest gap; the next tick tries again
	}
	end := time.Now().UTC().Add(-baselineLag).Truncate(24 * time.Hour)
	start := end.Add(-baselineEvery)
	n, err := recompute(ctx, start, end)
	if err != nil {
		log.Warn("network baseline not recomputed", "from", start, "to", end, "dims", n, "err", err)
		return
	}
	log.Info("network baseline recomputed", "from", start, "to", end, "dims", n,
		"orgs_floor", kAnonOrgs, "rows_floor", kAnonRows)
}
