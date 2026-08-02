package risk

// feature.go is the ONLY file in this package that reads the warehouse. Every
// other file reaches a tenant's numbers through a function declared here, and
// every function declared here takes a minted Tenant — so "a feature read is
// tenant-scoped" is a property of the type signature, not of a reviewer's
// attention.
//
// TWO PLANES, AND THE SECOND IS WHY THE FIRST IS SAFE TO SHARE.
//
//	hanzo.risk_feature   per-tenant. `org` is the FIRST sort key, so a tenant read
//	                     is a prefix scan and not a filter, and the predicate is
//	                     bound rather than interpolated.
//	hanzo.risk_baseline  the network baseline. It has NO tenant column, no subject,
//	                     no id and no pseudonym — only quantiles over a k-anonymous
//	                     set of contributing orgs. There is no query against it that
//	                     returns one org's rows, because the rows do not exist. That
//	                     makes cross-tenant learning UNCOMPUTABLE rather than merely
//	                     disallowed, which is the only form of that boundary worth
//	                     having.
//
// THE HOT PATH DOES NOT COME THROUGH HERE. A payment-stage decision must answer
// inside the processor's authorization window, and the warehouse is a single
// StatefulSet pod that has taken api.hanzo.ai down once already. Scoring reads
// the in-memory velocity rings (constant time, fixed memory per key); this file
// is the BACKFILL that warms those rings and the read behind the dictionary and
// the search sandbox. A decide that needs analytics up is a payment plane that
// fails when analytics does.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
)

// featureTable is the per-tenant feature surface. It is a projection of the ONE
// event door (POST /v1/event -> bus -> event.event / event.error) plus the LLM
// spend ledger, reduced to the counts a risk decision reads and bucketed at five
// minutes, so a subject's recent shape is one prefix scan.
//
// org LEADS the sort key deliberately, unlike hanzo.cloud_usage's
// (timestamp, organization, ...): a per-tenant read must be a prefix scan.
const featureTable = "hanzo.risk_feature"

// baselineTable is the network baseline: quantiles per (bucket, subject kind,
// feature), over a k-anonymous set of contributing orgs. No tenant column
// exists, so no tenant row can be selected from it.
const baselineTable = "hanzo.risk_baseline"

const featureDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.risk_feature (
		org           String,
		subject_kind  LowCardinality(String),
		subject       String,
		bucket        DateTime,
		events        UInt32,
		sessions      UInt32,
		distincts     UInt32,
		errors        UInt32,
		spend_nano    Int64,
		tokens        UInt64,
		ips           UInt16,
		uas           UInt16,
		countries     UInt16,
		signups       UInt32,
		payments      UInt32,
		declines      UInt32,
		disputes      UInt32,
		payouts       UInt32
	) ENGINE = SummingMergeTree()
	ORDER BY (org, subject_kind, subject, bucket)
	TTL bucket + INTERVAL 400 DAY`

const baselineDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.risk_baseline (
		bucket        Date,
		subject_kind  LowCardinality(String),
		feature       LowCardinality(String),
		q10           Float64,
		q50           Float64,
		q90           Float64,
		q99           Float64,
		orgs          UInt32,
		n             UInt64
	) ENGINE = ReplacingMergeTree()
	ORDER BY (bucket, subject_kind, feature)
	TTL bucket + INTERVAL 400 DAY`

// ensureTables creates both planes idempotently. cloud reads and writes rows on
// the event plane but does not own its DDL (o11y does); it DOES own these two,
// so they are created here the way apps/datastore/cloudusage.go creates its own —
// CREATE IF NOT EXISTS, plus additive ADD COLUMN IF NOT EXISTS migrations, so a
// fresh warehouse and a legacy one converge on the same shape.
func ensureTables(ctx context.Context) error {
	if !datastore.Ready() {
		return errWarehouse
	}
	for _, stmt := range []string{featureDDL, baselineDDL} {
		if err := datastore.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// featureColumns is the FIXED allowlist of columns a caller may name. Nothing
// user-derived is ever spelled into a statement: a caller names a column, this
// map answers whether that name is one of ours, and the map's own key is what
// reaches the SQL. A name that is not here is refused, never interpolated.
var featureColumns = map[string]string{
	"events":    "events",
	"sessions":  "sessions",
	"distincts": "distincts",
	"errors":    "errors",
	"spend":     "spend_nano",
	"tokens":    "tokens",
	"ips":       "ips",
	"uas":       "uas",
	"countries": "countries",
	"signups":   "signups",
	"payments":  "payments",
	"declines":  "declines",
	"disputes":  "disputes",
	"payouts":   "payouts",
}

// subjectKinds is the FIXED allowlist of aggregation axes. `pair` and `device`
// are the axes that surface several nominally unrelated customers acting as one,
// which is what multi-account abuse and account sharing look like from here.
var subjectKinds = map[string]bool{
	"account": true, "transaction": true, "session": true, "agent": true,
	"merchant": true, "payout": true, "user": true, "device": true,
	"ip": true, "pair": true,
}

// featureRow is one bucket of one subject's activity, as the reader materialises
// it. The tenant is NOT a field: a row is only ever produced by a read that was
// already scoped, so carrying the key back would be the one place it could be
// compared against the wrong thing.
type featureRow struct {
	bucket time.Time
	values map[string]float64
}

// riskWhere is the tenancy predicate. org LEADS and is BOUND; the window is
// bound; the subject kind has already passed the allowlist and is bound anyway.
// Nothing user-derived reaches the statement as an identifier.
//
// The shape is the one apps/analytics/query.go and o11y's eventsql use, which is
// the point: a third shape is a third place for the boundary to be got wrong.
func riskWhere(t Tenant, kind, subject string, start, end time.Time) (string, []any) {
	return "org = ? AND subject_kind = ? AND subject = ? AND bucket >= ? AND bucket < ?",
		[]any{t.String(), kind, subject, tsLiteral(start), tsLiteral(end)}
}

// tsLiteral renders a time the way the warehouse driver binds a DateTime. Same
// transport apps/analytics uses; the value is still a bound parameter.
func tsLiteral(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// errWarehouse is the honest gap: the warehouse is unreachable, so a read
// returns nothing AND says why. A reader that answered zero would be reporting
// "this subject did nothing", which is the one answer a risk surface must never
// invent.
var errWarehouse = fmt.Errorf("risk: the warehouse is unreachable, so this subject's history cannot be read")

// window reads one subject's buckets over [start,end).
//
// The caller supplies a Tenant, which it can only have obtained from a validated
// principal, and a kind that must be in the allowlist. The column list is built
// from featureColumns' own values, never from the caller's spelling.
func window(ctx context.Context, t Tenant, kind, subject string, start, end time.Time) ([]featureRow, error) {
	if !subjectKinds[kind] {
		return nil, fmt.Errorf("risk: %q is not a subject kind", kind)
	}
	if strings.TrimSpace(subject) == "" {
		return nil, fmt.Errorf("risk: no subject, so the window would name nobody")
	}
	if !datastore.Ready() {
		return nil, errWarehouse
	}
	where, args := riskWhere(t, kind, subject, start, end)

	// Ordered so the projection is stable across calls; the names come from our
	// own map, so this concatenation carries nothing the caller wrote.
	names := columnNames()
	sel := make([]string, 0, len(names)+1)
	sel = append(sel, "bucket")
	for _, n := range names {
		sel = append(sel, "sum("+featureColumns[n]+") AS "+n)
	}
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + featureTable +
		" WHERE " + where + " GROUP BY bucket ORDER BY bucket"

	rows, err := datastore.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]featureRow, 0, len(rows))
	for _, r := range rows {
		fr := featureRow{values: make(map[string]float64, len(names))}
		if b, ok := r["bucket"].(time.Time); ok {
			fr.bucket = b
		}
		for _, n := range names {
			fr.values[n] = num(r[n])
		}
		out = append(out, fr)
	}
	return out, nil
}

// columnNames returns the allowlist keys in a stable order.
func columnNames() []string {
	out := make([]string, 0, len(featureColumns))
	for n := range featureColumns {
		out = append(out, n)
	}
	sortStrings(out)
	return out
}

// ── the network baseline ────────────────────────────────────────────────────

// kAnonMin is how many DISTINCT orgs must contribute to a bucket before its
// quantiles may be published. Below it a quantile is close enough to one
// business's own numbers to be that business's numbers.
const kAnonMin = 25

// nMin is how many observations a published bucket needs. k orgs each
// contributing one row is k-anonymous and still statistically meaningless.
const nMin = 1000

// baselinePopulate is the WHOLE cross-org surface. It is never composed from
// request data, has no placeholder a caller could reach, and its projection
// carries no org, no subject, no id and no pseudonym — only quantiles and two
// counts. The HAVING clause is the k-anonymity gate and is part of the statement
// rather than a filter applied to its result, so a bucket below the threshold is
// never materialised at all.
//
// THE FLOOR IS SPELLED ONCE. It used to be a package constant with the numbers
// written into the SQL as literals AND declared again as kAnonMin/nMin above, so
// raising the constant raised only the read-side belt: the writer kept
// publishing below the intended floor and the reader silently discarded
// everything it wrote. Two spellings of one number is one number that will drift,
// and the direction it drifts in here is a privacy floor.
//
// The reserved `$public` tenant is excluded here as well as at the mint: an
// unauthenticated stranger writes into that lane, and a stranger who can move
// the network baseline can move every tenant's comparison against it.
var baselinePopulate = `
	INSERT INTO hanzo.risk_baseline (bucket, subject_kind, feature, q10, q50, q90, q99, orgs, n)
	SELECT
		toDate(bucket)                     AS bucket,
		subject_kind,
		'events'                           AS feature,
		quantileExact(0.10)(events)        AS q10,
		quantileExact(0.50)(events)        AS q50,
		quantileExact(0.90)(events)        AS q90,
		quantileExact(0.99)(events)        AS q99,
		uniqExact(org)                     AS orgs,
		count()                            AS n
	FROM hanzo.risk_feature
	WHERE org != '$public' AND org NOT LIKE '%/$public'
	  AND bucket >= toDateTime(toDate(now()) - 1) AND bucket < toDateTime(toDate(now()))
	GROUP BY bucket, subject_kind
	HAVING orgs >= ` + strconv.Itoa(kAnonMin) + ` AND n >= ` + strconv.Itoa(nMin)

// baselineRow is one published quantile bucket. Reflection over this type is
// part of the isolation proof: a field naming a tenant, a subject or a person
// would be a leak, and the test that asserts none exists reads THIS type and the
// DDL above rather than a comment.
type baselineRow struct {
	bucket  time.Time
	kind    string
	feature string
	q10     float64
	q50     float64
	q90     float64
	q99     float64
	orgs    uint64
	n       uint64
}

// publishBaseline recomputes yesterday's network quantiles. It is the ONLY
// writer of the baseline plane and it runs on a schedule, never on a request, so
// no caller can time it, steer it or observe its cost.
func publishBaseline(ctx context.Context) error {
	if !datastore.Ready() {
		return errWarehouse
	}
	return datastore.Exec(ctx, baselinePopulate)
}

// baseline reads the published quantiles for one subject kind. There is no
// tenant argument BECAUSE THERE IS NO TENANT COLUMN — every caller reads the
// same rows, which is what makes them safe to read at all.
func baseline(ctx context.Context, kind string, day time.Time) ([]baselineRow, error) {
	if !subjectKinds[kind] {
		return nil, fmt.Errorf("risk: %q is not a subject kind", kind)
	}
	if !datastore.Ready() {
		return nil, errWarehouse
	}
	rows, err := datastore.Query(ctx,
		"SELECT bucket, subject_kind, feature, q10, q50, q90, q99, orgs, n FROM "+baselineTable+
			" WHERE bucket = ? AND subject_kind = ? ORDER BY feature",
		day.UTC().Format("2006-01-02"), kind)
	if err != nil {
		return nil, err
	}
	out := make([]baselineRow, 0, len(rows))
	for _, r := range rows {
		br := baselineRow{
			kind: str(r["subject_kind"]), feature: str(r["feature"]),
			q10: num(r["q10"]), q50: num(r["q50"]), q90: num(r["q90"]), q99: num(r["q99"]),
			orgs: uint64(num(r["orgs"])), n: uint64(num(r["n"])),
		}
		if b, ok := r["bucket"].(time.Time); ok {
			br.bucket = b
		}
		// Belt on top of the HAVING: a row that predates the gate, or one an
		// operator inserted by hand, is dropped on read rather than trusted.
		if br.orgs < kAnonMin || br.n < nMin {
			continue
		}
		out = append(out, br)
	}
	return out, nil
}
