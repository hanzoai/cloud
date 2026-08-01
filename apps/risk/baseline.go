package risk

// baseline.go — the ENTIRE cross-org surface, and the argument for why it is safe.
//
// Learning from an organisation's own first-party data is the moat. Learning
// across organisations is the thing that would destroy it, so the cross-org
// surface is exactly one table and it is AGGREGATE-ONLY:
//
//	hanzo.risk_baseline(bucket, subject_kind, dim, q10, q50, q90, q99, orgs, n)
//
// There is NO org column. No subject, no identifier, no pseudonym, no hash of
// one. A row is four quantiles of one dim over one day, plus how many
// organisations and how many buckets went into them. That is not a redaction of
// per-tenant data — there is nothing to redact, because the shape cannot hold a
// tenant. The leak is UNCOMPUTABLE rather than merely disallowed, which is the
// same argument the qualified tenant key makes one layer down.
//
// Two further gates, because "aggregate" alone is not anonymity:
//
//   - k-ANONYMITY. A bucket only one organisation contributed to is that
//     organisation's data wearing a quantile's clothes. A bucket is published
//     only when at least [kAnonOrgs] distinct organisations and [kAnonRows]
//     buckets went into it, and the gate is enforced TWICE: in the statement's
//     HAVING (bound, never interpolated) and again on read, so a row written
//     before the gate existed is still refused.
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
		q99           Float64,
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
// kAnonRows is the number of underlying buckets, which bounds the other
// direction: twenty-five organisations each contributing one bucket is
// twenty-five readable numbers, not a distribution.
const (
	kAnonOrgs = 25
	kAnonRows = 1000
)

// publishable is the k-anonymity gate as a PURE PREDICATE, used by both the
// statement that writes a bucket and the reader that returns one. One definition,
// two enforcement points: a row that predates the gate is refused on the way out.
func publishable(orgs uint32, n uint64) bool {
	return orgs >= kAnonOrgs && n >= kAnonRows
}

// populate is the ONE statement that writes the baseline, rendered per dim from
// the allowlist. Its placeholders bind, in order: the dim's published name, the
// window start, the window end, the organisation floor and the bucket floor.
//
// %s is filled ONLY from dim.Column — a package constant reached through
// [dimBy]. There is no path by which a caller's string becomes an identifier
// here, which [TestBaseline_StatementIsConstant] holds to.
func populate(d dim) string {
	return fmt.Sprintf(`INSERT INTO %s (bucket, subject_kind, dim, q10, q50, q90, q99, orgs, n)
		SELECT b, subject_kind, ?, quantileExact(0.10)(x), quantileExact(0.50)(x),
		       quantileExact(0.90)(x), quantileExact(0.99)(x), uniqExact(org), count()
		FROM (
		  SELECT toDate(bucket) AS b, subject_kind, org, sum(%s) AS x
		  FROM %s
		  WHERE bucket >= ? AND bucket < ?
		  GROUP BY b, subject_kind, org, subject
		)
		GROUP BY b, subject_kind
		HAVING uniqExact(org) >= ? AND count() >= ?`,
		baselineTable, d.Column, featureTable)
}

// band is one published bucket of the network baseline: four quantiles of one
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
	Q99  float64
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
		"SELECT bucket, subject_kind, dim, q10, q50, q90, q99, orgs, n FROM %s WHERE %s ORDER BY bucket, subject_kind, dim LIMIT %d",
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
		b.Q99, _ = number(r["q99"])
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
