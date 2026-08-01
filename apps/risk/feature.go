package risk

// feature.go — THE ONLY DOOR to the event surface, and the place the tenant
// boundary is decided.
//
// Every org's events already land in one columnar store through one ingest door
// (POST /v1/event → the bus → one durable consumer per table): product events in
// event.event, captured failures in event.error, and every priced inference in
// hanzo.cloud_usage. This file turns that stream into a PER-ORG FEATURE SURFACE
// — hanzo.risk_feature — and reads it back for exactly one tenant at a time.
//
// THREE PROPERTIES, EACH LOAD-BEARING.
//
//  1. A read cannot be spelled without a tenant. [rows] takes a [tenant], which
//     has no exported constructor and is minted in exactly one place ([qualify],
//     reached only through [tenantOf] from the validated principal). There is no
//     path from this package to the store that does not carry one.
//
//  2. The tenant is the LEADING BOUND predicate of every statement. [featureWhere]
//     opens with `org = ?` and binds the key positionally; the window is bound
//     too; every column and aggregation a caller can name resolves through a
//     FIXED allowlist and is never interpolated. Nothing user-derived reaches a
//     statement as an identifier.
//
//  3. The key is `<brand>/<org>`, not the bare org. An org name is unique within
//     an issuer and not across issuers, so `acme` on hanzo.id and `acme` on
//     zoolabs.id are two unrelated organisations; keyed on the bare org they are
//     one set of rows and — because the same key is the salt every derived secret
//     hangs off — one vault. Qualifying at the key makes that collision
//     uncomputable rather than merely disallowed.
//
// The SOURCE planes are read with the BARE org, because that is the column they
// carry: event.event and hanzo.cloud_usage were written by the ingest door long
// before this plane existed and their tenant column is the IAM org slug. The
// qualification is applied on the way IN — the rollup writes the qualified key
// into hanzo.risk_feature — so this plane's own index is qualified end to end and
// the two halves of the key are never confused. [tenant.org] is the only way to
// reach the bare half and every use of it is a source-plane read.
//
// THE HOT PATH DOES NOT COME THROUGH HERE. Scoring reads in-memory rings
// (learn.go); this store is the BACKFILL that warms them. The warehouse is a
// single stateful pod that has taken the API down once already, and a decision
// that needs analytics up is a decision plane that fails when analytics does.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/brand"
	"github.com/zap-proto/zip"
)

// The store seam. Production is always the one warehouse client; a test
// substitutes these to drive the plane without standing a store up. Held as
// values on the same terms as apps/analytics holds warehouseReady/warehouseExec,
// so there is one convention for "the warehouse, replaceable in a test".
var (
	storeReady = datastore.Ready
	storeQuery = datastore.Query
	storeExec  = datastore.Exec
)

// ── the tenant key ───────────────────────────────────────────────────────────

// sep divides the tenant key's two halves. Brand ids are a closed set and none
// contains it, so the first one always ends the brand and the key reads back to
// the organisation it names.
const sep = "/"

// public is the reserved tenant the ingest door files CREDENTIAL-LESS writes
// under. It is not an organisation: nobody authenticated for it, its rows are a
// projection of whatever an unauthenticated stranger sent, and admitting it as a
// tenant would let that stranger move a real org's — or the network's —
// statistics. [qualify] refuses it, which is why no rollup and no baseline can
// ever reach it.
const public = "$public"

// tenant is the qualified tenant key, `<brand>/<org>`.
//
// It is UNEXPORTED and has no exported constructor, so no other package can
// produce one: a feature read is unspellable outside the mint. Within the
// package it is minted in exactly one place, [qualify].
type tenant string

// qualify builds a tenant key from the brand whose issuer vouched for the caller
// and the org that caller acts for. It is the ONE mint.
//
// The brand half is never taken from a header or a body — it is the deployment's
// own brand — for the same reason the org half is never taken from one: a caller
// that chooses which brand's tenant space its org lands in has chosen another
// brand's rows.
func qualify(brandID, org string) (tenant, error) {
	id := strings.ToLower(strings.TrimSpace(brandID))
	if id == "" || brand.For(id).ID != id {
		return "", fmt.Errorf("no brand is registered as %q, so nothing vouches for this tenant", brandID)
	}
	org = strings.TrimSpace(org)
	switch {
	case org == "":
		return "", fmt.Errorf("no org, so the request acts for no tenant")
	case org == public:
		return "", fmt.Errorf("%q is the anonymous lane, not an organisation", public)
	case strings.Contains(org, sep):
		return "", fmt.Errorf("org %q contains %q, so the tenant it names is not readable back", org, sep)
	}
	return tenant(id + sep + org), nil
}

// qualified reports whether a key is one qualify would have produced. Derived
// from qualify rather than stated again, so there is one definition of the shape
// and a change to it cannot leave a validator behind.
func (t tenant) qualified() bool {
	b, org, found := strings.Cut(string(t), sep)
	if !found {
		return false
	}
	again, err := qualify(b, org)
	return err == nil && again == t
}

// brandOf is the key's brand half.
func (t tenant) brandOf() string {
	b, _, _ := strings.Cut(string(t), sep)
	return b
}

// org is the key's BARE org half — the slug the SOURCE planes carry in their own
// tenant column. It is the only way to reach it, so every source-plane read is
// visible as a call to this method.
func (t tenant) org() string {
	_, org, _ := strings.Cut(string(t), sep)
	return org
}

// tenantOf resolves the tenant a request acts for, from the VALIDATED principal
// and the deployment's own brand — never from a field, a query parameter or a
// header a caller can write.
//
// It fails closed off the HTTP path too: a caller with no validated principal has
// no org, so there is no key and no read.
func tenantOf(ctx context.Context, brandID string) (tenant, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("no validated principal")
	}
	t, err := qualify(brandID, org)
	if err != nil {
		return "", zip.ErrForbidden(err.Error())
	}
	return t, nil
}

// ── the feature surface ──────────────────────────────────────────────────────

// Subject kinds. A feature is meaningless without saying WHOSE, and each kind
// below is a column the event surface actually carries — not a category invented
// here and then read as empty.
const (
	// kindPerson is the identified end user across the product surface:
	// person_id, else distinct_id, else anonymous_id.
	kindPerson = "person"
	// kindSession is one session of that surface.
	kindSession = "session"
	// kindAccount is the org's own user in the metered plane
	// (hanzo.cloud_usage.user_id) — the subject whose spend velocity is what
	// pay-as-you-go abuse moves.
	kindAccount = "account"
)

// kinds is the closed set, in one place, so a rollup cannot write a kind a read
// cannot name.
var kinds = []string{kindPerson, kindSession, kindAccount}

// featureTable and baselineTable are this plane's own tables. Everything else
// this file touches belongs to another owner and is READ ONLY.
const (
	featureTable = "hanzo.risk_feature"
	sourceEvent  = "event.event"
	sourceError  = "event.error"
	sourceUsage  = "hanzo.cloud_usage"
)

// featureDDL is the per-org feature surface.
//
// `org` is FIRST in ORDER BY, deliberately unlike hanzo.cloud_usage's
// (timestamp, organization, …): a per-org read must be a PREFIX SCAN and not a
// filter over the whole store, because the read amplification of the second
// shape is what takes a single-pod warehouse down.
//
// SummingMergeTree, because every column is an additive count over a bucket and
// four source planes each contribute a DIFFERENT subset of the columns for the
// same (org, kind, subject, bucket) key — addition is what merges those four
// partial rows into one.
//
// It is emphatically NOT idempotent, and that is the reason the watermark below
// exists: re-inserting a window that already landed does not replace it, the two
// rows merge by addition and the organisation's own history doubles. Idempotency
// cannot come from the engine here, so it comes from [plane.roll] rolling each
// window exactly once.
const featureDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.risk_feature (
		org           String,
		subject_kind  LowCardinality(String),
		subject       String,
		bucket        DateTime,
		events        UInt32,
		sessions      UInt32,
		distincts     UInt32,
		paths         UInt32,
		errors        UInt32,
		calls         UInt32,
		failures      UInt32,
		tokens        UInt64,
		spend_nano    Int64,
		ips           UInt32
	) ENGINE = SummingMergeTree()
	ORDER BY (org, subject_kind, subject, bucket)
	TTL bucket + INTERVAL 400 DAY`

// featureMigrations bring an ALREADY-EXISTING table up to the current shape:
// CREATE TABLE IF NOT EXISTS is a no-op on a table created before a column was
// added, so each additive column also needs an idempotent ADD COLUMN IF NOT
// EXISTS. Applied after the CREATE so a fresh and a legacy table converge.
var featureMigrations = []string{
	`ALTER TABLE hanzo.risk_feature ADD COLUMN IF NOT EXISTS paths UInt32`,
	`ALTER TABLE hanzo.risk_feature ADD COLUMN IF NOT EXISTS calls UInt32`,
	`ALTER TABLE hanzo.risk_feature ADD COLUMN IF NOT EXISTS failures UInt32`,
	`ALTER TABLE hanzo.risk_feature ADD COLUMN IF NOT EXISTS tokens UInt64`,
	`ALTER TABLE hanzo.risk_feature ADD COLUMN IF NOT EXISTS spend_nano Int64`,
	`ALTER TABLE hanzo.risk_feature ADD COLUMN IF NOT EXISTS ips UInt32`,
}

// dim is one measurable column of the surface: the wire name a caller may use,
// the column it resolves to, and how to read the number.
//
// The Column is a PACKAGE CONSTANT reached only through [dimBy]. That is the
// allowlist: a caller names a dim, never a column, so no user-derived string is
// ever an identifier in a statement.
type dim struct {
	// Name is the dim as the API publishes it.
	Name string
	// Column is the risk_feature column it reads. Never user input.
	Column string
	// Unit is how to read the raw number, which is what turns a coordinate into a
	// sentence.
	Unit string
	// Source names the plane the column is rolled up from, so a dim that reads
	// zero everywhere can be traced to a plane the org does not use rather than
	// to a bug.
	Source string
}

// dims is the surface, in one place. The order is the order the dictionary
// publishes.
var dims = []dim{
	{Name: "events", Column: "events", Unit: "product events in the bucket", Source: sourceEvent},
	{Name: "sessions", Column: "sessions", Unit: "distinct sessions in the bucket", Source: sourceEvent},
	{Name: "distincts", Column: "distincts", Unit: "distinct identities in the bucket", Source: sourceEvent},
	{Name: "paths", Column: "paths", Unit: "distinct paths in the bucket", Source: sourceEvent},
	{Name: "errors", Column: "errors", Unit: "captured failures in the bucket", Source: sourceError},
	{Name: "calls", Column: "calls", Unit: "metered inference calls in the bucket", Source: sourceUsage},
	{Name: "failures", Column: "failures", Unit: "metered calls that did not succeed", Source: sourceUsage},
	{Name: "tokens", Column: "tokens", Unit: "tokens consumed in the bucket", Source: sourceUsage},
	{Name: "spend", Column: "spend_nano", Unit: "nano-USD of metered spend in the bucket", Source: sourceUsage},
	{Name: "ips", Column: "ips", Unit: "distinct client addresses in the bucket", Source: sourceUsage},
}

// dimBy is THE ALLOWLIST: the only way a name becomes a column.
var dimBy = func() map[string]dim {
	m := make(map[string]dim, len(dims))
	for _, d := range dims {
		m[d.Name] = d
	}
	return m
}()

// selectColumns is the projection every feature read takes, in one order, so the
// row decoder and the statement cannot drift.
var selectColumns = func() []string {
	out := make([]string, 0, len(dims))
	for _, d := range dims {
		out = append(out, d.Column)
	}
	return out
}()

// query is the CLOSED set of shapes a feature read can take. There is no free-text
// field: kind is checked against [kinds], subject is a BOUND value, the window is
// bound, and the limit is a validated int. A test enumerates every value this
// struct can hold and asserts the statement built from it.
type query struct {
	// kind narrows to one subject kind; empty reads every kind.
	kind string
	// subject narrows to one subject; empty reads every subject of the kind.
	subject string
	// start and end bound the window, half-open.
	start, end time.Time
	// limit bounds the rows returned. Zero takes the default.
	limit int
}

// maxRows bounds any single feature read. A warm that would scan a tenant's
// whole 400-day surface is a read the warehouse cannot afford; a bounded warm
// that reports what it covered is one it can.
const maxRows = 200_000

func (q query) rowLimit() int {
	if q.limit <= 0 || q.limit > maxRows {
		return maxRows
	}
	return q.limit
}

// featureWhere is THE predicate. org is the LEADING, BOUND term of every feature
// statement this package can build; the window is bound; the optional narrowers
// bind too. Nothing here is interpolated.
//
// The order is not cosmetic. `org` leads the table's sort key, so a predicate
// that opens with it is a prefix scan; one that opens with a time bound is a
// filter over every tenant's rows, which is both slower and — the part that
// matters — a statement whose correctness depends on a term that is not first.
func featureWhere(t tenant, q query) (string, []any) {
	where := []string{"org = ?"}
	args := []any{string(t)}
	if q.kind != "" {
		where = append(where, "subject_kind = ?")
		args = append(args, q.kind)
	}
	if q.subject != "" {
		where = append(where, "subject = ?")
		args = append(args, q.subject)
	}
	where = append(where, "bucket >= ?", "bucket < ?")
	args = append(args, tsLiteral(q.start), tsLiteral(q.end))
	return strings.Join(where, " AND "), args
}

// row is one bucket of one subject's surface.
type row struct {
	Kind    string
	Subject string
	Bucket  time.Time
	// Value is every dim of the bucket, keyed by the dim's published name. A dim
	// the org's surface does not carry is ABSENT rather than zero — the
	// difference between "no risk" and "no data" is the difference a model must
	// not be allowed to lose.
	Value map[string]float64
}

// rows reads one tenant's feature surface. It is the ONLY function in this
// package that builds a statement against a feature table, and it takes a
// [tenant] because a read without one has no meaning here.
func rows(ctx context.Context, t tenant, q query) ([]row, error) {
	if !t.qualified() {
		return nil, fmt.Errorf("risk: unqualified tenant key %q", string(t))
	}
	if q.kind != "" && !known(q.kind) {
		return nil, fmt.Errorf("risk: unknown subject kind %q", q.kind)
	}
	if !storeReady() {
		return nil, errStore
	}
	where, args := featureWhere(t, q)
	stmt := fmt.Sprintf(
		"SELECT subject_kind, subject, bucket, %s FROM %s WHERE %s GROUP BY subject_kind, subject, bucket ORDER BY bucket LIMIT %d",
		sumOf(selectColumns), featureTable, where, q.rowLimit())
	raw, err := storeQuery(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("risk: read feature surface: %w", err)
	}
	out := make([]row, 0, len(raw))
	for _, r := range raw {
		v := make(map[string]float64, len(dims))
		for _, d := range dims {
			if x, ok := number(r[d.Column]); ok {
				v[d.Name] = x
			}
		}
		out = append(out, row{
			Kind:    text(r["subject_kind"]),
			Subject: text(r["subject"]),
			Bucket:  stamp(r["bucket"]),
			Value:   v,
		})
	}
	return out, nil
}

// sumOf renders the projection. The columns are package constants from [dims],
// so the only thing composed into the statement is code.
func sumOf(cols []string) string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, "sum("+c+") AS "+c)
	}
	return strings.Join(out, ", ")
}

func known(kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// ── the rollup: source planes → the feature surface ──────────────────────────

// rollupStmt is one source plane's contribution, as a statement whose only
// user-derived values are BOUND: the qualified key it writes, the bare org it
// reads, and the window.
//
// The statements are package constants. There is no composition step and no
// place a caller's string could become an identifier, which is what makes the
// leak uncomputable rather than merely refused.
type rollupStmt struct {
	// Name identifies the contribution in a report.
	Name string
	// SQL is the constant statement. Its placeholders bind, in order: the
	// qualified tenant key (written), the bare org (read), the window start, the
	// window end, and the row cap.
	SQL string
}

// rollups is the closed set. Adding a source plane is adding a row here.
//
// Each reads ONE plane with `org = ?` (or `organization = ?`, the column that
// plane happens to carry) as its leading bound predicate and writes the
// QUALIFIED key, so the surface's own index is qualified even though the planes
// it is rolled up from are not.
var rollups = []rollupStmt{
	{
		Name: "person",
		SQL: `INSERT INTO hanzo.risk_feature (org, subject_kind, subject, bucket, events, sessions, distincts, paths)
		      SELECT ?, 'person', s, b, count(), uniqExact(session_id), uniqExact(distinct_id), uniqExact(path)
		      FROM (
		        SELECT if(person_id != '', person_id, if(distinct_id != '', distinct_id, anonymous_id)) AS s,
		               toStartOfFiveMinute(time) AS b, session_id, distinct_id, path
		        FROM event.event
		        WHERE org = ? AND time >= ? AND time < ?
		      )
		      WHERE s != ''
		      GROUP BY s, b
		      LIMIT ?`,
	},
	{
		Name: "session",
		SQL: `INSERT INTO hanzo.risk_feature (org, subject_kind, subject, bucket, events, sessions, distincts, paths)
		      SELECT ?, 'session', session_id, toStartOfFiveMinute(time), count(), 1, uniqExact(distinct_id), uniqExact(path)
		      FROM event.event
		      WHERE org = ? AND time >= ? AND time < ? AND session_id != ''
		      GROUP BY session_id, toStartOfFiveMinute(time)
		      LIMIT ?`,
	},
	{
		Name: "fault",
		SQL: `INSERT INTO hanzo.risk_feature (org, subject_kind, subject, bucket, errors)
		      SELECT ?, 'person', s, b, count()
		      FROM (
		        SELECT if(person_id != '', person_id, if(distinct_id != '', distinct_id, anonymous_id)) AS s,
		               toStartOfFiveMinute(time) AS b
		        FROM event.error
		        WHERE org = ? AND time >= ? AND time < ?
		      )
		      WHERE s != ''
		      GROUP BY s, b
		      LIMIT ?`,
	},
	{
		Name: "account",
		SQL: `INSERT INTO hanzo.risk_feature (org, subject_kind, subject, bucket, calls, failures, tokens, spend_nano, ips)
		      SELECT ?, 'account', user_id, toStartOfFiveMinute(timestamp),
		             count(), countIf(status != 'success'), sum(total_tokens), sum(billed_nano), uniqExact(client_ip)
		      FROM hanzo.cloud_usage
		      WHERE organization = ? AND timestamp >= ? AND timestamp < ? AND user_id != ''
		      GROUP BY user_id, toStartOfFiveMinute(timestamp)
		      LIMIT ?`,
	},
}

// errStore is the honest gap: the warehouse is not reachable, so there is no
// surface to read. Never zeroes — a model that learns from a fabricated absence
// of activity has learned that the tenant is quiet.
var errStore = fmt.Errorf("risk: the event surface is not reachable")

// ensure creates this plane's own tables idempotently. cloud READS the event
// planes and never creates them (their DDL owner is o11y); it owns
// hanzo.risk_feature and hanzo.risk_baseline outright, so it creates those the
// way apps/datastore creates hanzo.cloud_usage — CREATE IF NOT EXISTS plus
// additive ALTERs, so a fresh and a legacy warehouse converge.
func ensure(ctx context.Context) error {
	if !storeReady() {
		return errStore
	}
	for _, stmt := range append([]string{
		`CREATE DATABASE IF NOT EXISTS hanzo`,
		featureDDL,
		baselineDDL,
	}, featureMigrations...) {
		if err := storeExec(ctx, stmt); err != nil {
			return fmt.Errorf("risk: ensure schema: %w", err)
		}
	}
	return nil
}

// rollup folds ONE source plane's window into one tenant's feature surface. It is
// the ONLY writer of hanzo.risk_feature — one statement, one place, so the values
// that decide which tenant is read and which is written are bound in exactly one
// line of code.
//
// One plane and one window rather than all four and the whole backlog, because
// each plane advances under its OWN watermark ([plane.roll]): a deployment where
// event.error does not exist still gets its usage features, and its own watermark
// does not move.
func rollup(ctx context.Context, t tenant, r rollupStmt, start, end time.Time) error {
	if !t.qualified() {
		return fmt.Errorf("risk: unqualified tenant key %q", string(t))
	}
	if !storeReady() {
		return errStore
	}
	// The WRITE is the qualified key, the READ is the bare org. They are two
	// different values of one tenant and confusing them in either direction is a
	// cross-tenant read, which is why they are bound here and nowhere else.
	if err := storeExec(ctx, r.SQL, string(t), t.org(), tsLiteral(start), tsLiteral(end), maxRows); err != nil {
		return fmt.Errorf("risk: roll %q: %w", r.Name, err)
	}
	return nil
}

// ── the watermark: rolling each window exactly once ──────────────────────────
//
// [rollup] is the only writer of the feature surface and it is a plain INSERT
// into a SummingMergeTree, so running it twice over one window DOUBLES that
// organisation's history rather than converging on it. The surface therefore
// cannot be kept current by a timer alone: something has to remember how far each
// source plane has been folded, per tenant, across restarts.
//
// That is the watermark, and it lives on the tenant's OWN shelf — the same
// physically isolated file its model state does. One tenant's progress is not a
// row in a table another tenant's progress is also in.

// rolledDDL remembers how far each source plane has been folded into THIS
// tenant's feature surface. The tenant is in the key because a shelf file is
// named for the bare org slug and two brands' identically named organisations
// share one.
const rolledDDL = `CREATE TABLE IF NOT EXISTS rolled (
	tenant TEXT NOT NULL,
	plane  TEXT NOT NULL,
	upto   INTEGER NOT NULL,
	PRIMARY KEY (tenant, plane)
)`

// rollSpan is the widest window one rollup statement covers, so a tenant idle for
// a year is caught up in bounded steps rather than in one scan of a source plane.
//
// rollLag is how far behind the present a rollup stops. The source planes are
// written by an asynchronous consumer, so the newest minutes are not complete —
// folding them would roll up a partial window and, because the watermark then
// says it is done, never revisit it.
//
// rollFloor bounds how far back a tenant with no watermark starts. It is the warm
// window, because a fold exists to give a model the history the warm window reads
// and rolling up more would cost the warehouse a scan nothing reads back.
//
// rollSteps bounds how many windows ONE fold advances a plane through, and it is
// DERIVED from the two above rather than picked: a fold must be able to catch a
// brand-new tenant up across its whole floor, or its most recent — and most
// useful — window is the one that never lands. So the worst case is a tenant's
// first fold issuing four planes × rollSteps bounded statements, once, and one or
// two per fold from then on.
const (
	rollSpan  = 24 * time.Hour
	rollLag   = 10 * time.Minute
	rollFloor = warmWindow
	rollSteps = int(rollFloor/rollSpan) + 2
)

// featureBucket is the grain the surface is written at — every rollup statement
// buckets with toStartOfFiveMinute — and therefore the grain every roll window
// must be aligned to.
//
// A WINDOW THAT SPLITS A BUCKET IS WRONG, not merely inefficient, and the reason
// is the distinct columns. The additive ones (events, calls, tokens, spend) sum
// correctly across two partial inserts, which is what a SummingMergeTree is for.
// `distincts`, `paths`, `sessions` and `ips` are uniqExact over the window: split
// a bucket at 12:03 and a subject active in both halves contributes 1 twice, so
// the surface reports two distinct addresses where there was one. Every velocity
// and diversity feature reads those columns, so the inflation is not cosmetic —
// it is a model trained on numbers no traffic produced.
//
// Aligning also bounds the write rate for free: a second roll inside the same
// bucket finds the watermark already at the edge and issues no statement at all,
// so a surface read can bring itself current without becoming a write amplifier.
const featureBucket = 5 * time.Minute

// rolled reads how far a source plane has been folded for this tenant. A tenant
// with no watermark starts at the floor rather than at the epoch.
func (p *plane) rolled(t tenant, name string) (time.Time, error) {
	sh, err := p.for_(t)
	if err != nil {
		return time.Time{}, err
	}
	floor := p.now().UTC().Add(-rollFloor).Truncate(featureBucket)
	var upto int64
	err = sh.db.QueryRow(`SELECT upto FROM rolled WHERE tenant = ? AND plane = ?`, string(t), name).Scan(&upto)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return floor, nil
	case err != nil:
		return time.Time{}, fmt.Errorf("risk: read rollup watermark: %w", err)
	}
	if at := time.Unix(upto, 0).UTC(); at.After(floor) {
		return at, nil
	}
	return floor, nil
}

// mark advances a source plane's watermark for this tenant. It is written AFTER
// the insert it describes, so the failure mode of a crash between the two is
// re-rolling one window — a bounded over-count of at most rollSpan — rather than
// losing that window forever, which no later run would ever notice.
func (p *plane) mark(t tenant, name string, upto time.Time) error {
	sh, err := p.for_(t)
	if err != nil {
		return err
	}
	_, err = sh.db.Exec(
		`INSERT INTO rolled (tenant, plane, upto) VALUES (?, ?, ?)
		 ON CONFLICT(tenant, plane) DO UPDATE SET upto = excluded.upto`,
		string(t), name, upto.UTC().Unix())
	if err != nil {
		return fmt.Errorf("risk: save rollup watermark: %w", err)
	}
	return nil
}

// roll brings this tenant's feature surface up to date from its own source
// planes, and is the PRODUCTION CALLER of [rollup] — the thing that makes the
// per-org feature surface exist rather than merely be declarable.
//
// Each source plane advances independently under its own watermark, so a
// deployment where event.error does not exist still gets its usage features and
// its own watermark does not move. It returns how many windows it folded, which
// is what the fold report quotes.
func (p *plane) roll(ctx context.Context, t tenant) (int, error) {
	if !t.qualified() {
		return 0, fmt.Errorf("risk: unqualified tenant key %q", string(t))
	}
	if !storeReady() {
		return 0, errStore
	}
	if err := ensure(ctx); err != nil {
		return 0, err
	}
	// ONE AT A TIME for this tenant, for the same reason the warm is: the watermark
	// is what makes rolling idempotent, and two concurrent rolls both read it, both
	// insert and both advance it — after which the surface holds that window twice
	// and a SummingMergeTree will never converge back.
	res, err := p.resident(t)
	if err != nil {
		return 0, err
	}
	release, err := res.hold(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	// The edge is aligned DOWN to the surface's own grain, and every watermark is
	// therefore an aligned instant too, so no window this issues can ever split a
	// bucket. See [featureBucket].
	edge := p.now().UTC().Add(-rollLag).Truncate(featureBucket)
	var folded int
	var first error
	for _, r := range rollups {
		from, err := p.rolled(t, r.Name)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		for step := 0; step < rollSteps && from.Before(edge); step++ {
			if err := ctx.Err(); err != nil {
				return folded, err
			}
			to := from.Add(rollSpan)
			if to.After(edge) {
				to = edge
			}
			if err := rollup(ctx, t, r, from, to); err != nil {
				if first == nil {
					first = err
				}
				break // this plane stays where it was; the others are unaffected
			}
			if err := p.mark(t, r.Name, to); err != nil {
				if first == nil {
					first = err
				}
				break
			}
			folded++
			from = to
		}
	}
	return folded, first
}

// ── the dictionary: what THIS org's surface actually carries ─────────────────

// coverage is one dim as the tenant's own surface presents it: how many of its
// buckets carry the dim at all, and what the dim reads on average where it is
// present. A dim present in no bucket is BLIND — the model reads its neutral
// value there and a reviewer must be able to see that, because a feature blind
// on all traffic is not contributing whatever the inventory claims for it.
type coverage struct {
	Dim     dim
	Buckets int
	Present int
	Mean    float64
	Max     float64
}

// Blind reports whether the tenant's surface carries this dim at all.
func (c coverage) Blind() bool { return c.Present == 0 }

// dictionary reads the tenant's own surface and reports, per dim, what it
// carries. It is the honest half of the feature catalogue: the other half is the
// model's governed inventory, which is the same for every tenant.
func dictionary(ctx context.Context, t tenant, start, end time.Time) ([]coverage, error) {
	rs, err := rows(ctx, t, query{start: start, end: end})
	if err != nil {
		return nil, err
	}
	out := make([]coverage, 0, len(dims))
	for _, d := range dims {
		c := coverage{Dim: d, Buckets: len(rs)}
		var total float64
		for _, r := range rs {
			x, ok := r.Value[d.Name]
			if !ok {
				continue
			}
			c.Present++
			total += x
			if x > c.Max {
				c.Max = x
			}
		}
		if c.Present > 0 {
			c.Mean = total / float64(c.Present)
		}
		out = append(out, c)
	}
	return out, nil
}

// ordered puts a window of buckets in time order, oldest first. It is what the
// warm path replays into the rings and what the search replays as history, so
// both read the tenant's surface through the same reduction — a model learned
// out of order would fold a later window into an earlier reference.
func ordered(rs []row) []row {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Bucket.Before(rs[j].Bucket) })
	return rs
}

// ── decoding ─────────────────────────────────────────────────────────────────
//
// The warehouse decodes each column into its native scan type keyed by column
// name, so a reader coerces. These are the only coercions this package performs
// and they are total: an unreadable value is ABSENT, never zero.

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, finite(n)
	case float32:
		return float64(n), finite(float64(n))
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stamp(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	return time.Time{}
}

// tsLiteral binds a time as the warehouse's own DateTime literal — the proven
// transport apps/analytics binds windows with, so a bound window is a bound
// value and never an interpolated one.
func tsLiteral(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }
