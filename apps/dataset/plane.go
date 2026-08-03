package dataset

// plane.go is THE door to the store, and the place the tenant boundary is
// decided. Every statement this package can run is in this file, and every one
// of them opens with `org = ?`.
//
// THREE PROPERTIES, EACH LOAD-BEARING.
//
//  1. A read cannot be spelled without a tenant. Every function below takes a
//     [tenant.Key], which has no exported field, cannot be written as a literal
//     outside its own package and cannot be decoded from a request body. There is
//     no path from this package to the store that does not carry one.
//
//  2. The tenant is the LEADING, BOUND predicate of every statement, and it leads
//     the sort key and the partition expression of both tables this plane owns.
//     That is not decoration: a predicate that opens with the tenant is a prefix
//     scan, one that opens with a time bound is a filter over every tenant's rows,
//     and the second shape is both slower and — the half that matters — a
//     statement whose correctness rests on a term that is not first.
//
//  3. Nothing caller-derived is ever an identifier. Table names are package
//     constants, columns come from the [dims] allowlist, the kind comes from a
//     closed set, and everything else — the tenant key, the dataset name, the
//     window, the seed, the caps — BINDS. There is no fmt verb in this file that
//     takes a caller's string.
//
// WHAT THIS PLANE OWNS AND WHAT IT ONLY READS. It owns hanzo.risk_dataset and
// hanzo.risk_row outright and creates them idempotently, the way apps/datastore
// creates hanzo.cloud_usage. It only READS hanzo.risk_feature: that table has one
// writer and one DDL owner, and a second CREATE here would be a second definition
// with nothing making the two agree.
//
// NEITHER OWNED TABLE HAS A TTL, deliberately. A table TTL is a fleet-wide clock
// no tenant can hold longer and no tenant can shorten — the opposite of "only the
// retention plane decides expiry, per tenant". Disposal is DROP PARTITION on
// (org, dataset), which is per tenant by construction: the partition expression
// leads with the tenant, so a disposal cannot reach across one.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/tenant"
)

// store is the warehouse as this plane uses it: three calls and no more. It is an
// interface so a test drives the plane without standing a warehouse up, and so
// this package cannot reach the connection for anything else.
type store interface {
	Ready() bool
	Query(ctx context.Context, stmt string, args ...any) ([]map[string]any, error)
	Exec(ctx context.Context, stmt string, args ...any) error
}

// warehouse is the production store: cloud's one warehouse connection.
type warehouse struct{}

func (warehouse) Ready() bool { return datastore.Ready() }
func (warehouse) Query(ctx context.Context, stmt string, args ...any) ([]map[string]any, error) {
	return datastore.Query(ctx, stmt, args...)
}
func (warehouse) Exec(ctx context.Context, stmt string, args ...any) error {
	return datastore.Exec(ctx, stmt, args...)
}

// errStore is the honest gap, and it is ONE thing: the warehouse did not answer.
// A connection that is down and a statement that failed are the same fact to a
// caller — there is nothing to read and nothing to write — and collapsing them
// here is what lets [plane.gap] answer 503 for both without ever having to decide
// whether a driver's message is safe to show. Every store failure in this file
// wraps it; the driver's own text rides along for the log and goes no further.
//
// Never zeroes: an empty dataset list on an unreachable store reads exactly like
// a tenant that has declared none.
var errStore = errors.New("the dataset plane's store is not reachable")

// ── the tables ───────────────────────────────────────────────────────────────

const (
	manifestTable = "hanzo.risk_dataset"
	rowTable      = "hanzo.risk_row"
)

// The lifecycle, as both a name and a rank.
//
// The rank IS the ReplacingMergeTree version column, which is what makes
// immutability structural rather than merely policed: ClickHouse keeps the row
// with the GREATEST version among duplicates, so no later write of a LOWER stage
// can displace a published version. The door refuses a second `ready` for one
// version; the engine refuses everything below it. Two layers, and the weaker one
// is not the only one.
//
// EXACTLY ONE STAGE OUTRANKS `ready`, and it is `disposed` — the tenant's own
// retention decision. That is the one write that must be able to supersede a
// publication, because the alternative is a plane that cannot honour a deletion.
// A published version is never REWRITTEN; it is only ever disposed of.
const (
	statusDeclared    = "declared"
	statusMaterialize = "materializing"
	statusRefused     = "refused"
	statusReady       = "ready"
	statusDisposed    = "disposed"
)

func rank(status string) uint8 {
	switch status {
	case statusDeclared:
		return 1
	case statusMaterialize:
		return 2
	case statusRefused:
		return 3
	case statusReady:
		return 4
	case statusDisposed:
		return 5
	}
	return 0
}

// manifestDDL is the version register: one row per (org, name, version), and the
// only place a version's status, spec, digest and counts live.
//
// PARTITION BY (org, name) so the tenant leads the partition expression exactly
// as it leads every predicate. ORDER BY (org, name, version) so a per-tenant read
// is a prefix scan. NO TTL — see the file note.
//
// It is bounded by the door, not by the engine: [maxNames] partitions per tenant
// and [maxVersions] rows in each. The register is metadata — a spec, a digest and
// a set of counts per version — so a tenant's whole register is kilobytes, which
// is what makes keeping a disposed dataset's record affordable.
const manifestDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.risk_dataset (
		org          String,
		name         String,
		version      UInt32,
		seq          UInt8,
		at           DateTime,
		by           String,
		status       LowCardinality(String),
		refusal      String,
		spec         String,
		source       String,
		digest       String,
		rows         UInt64,
		train        UInt64,
		val          UInt64,
		test         UInt64,
		subjects     UInt64,
		judged       UInt64,
		productive   UInt64,
		unproductive UInt64,
		horizon      UInt32,
		share        UInt16,
		truncated    UInt8
	) ENGINE = ReplacingMergeTree(seq)
	PARTITION BY (org, name)
	ORDER BY (org, name, version)`

// rowDDL is the dataset itself: the bytes a model names.
//
// ReplacingMergeTree with the whole row key in ORDER BY, so a retried insert
// batch converges instead of double-counting. NO TTL — see the file note.
//
// `disposition` is Int8 and every row this plane writes carries -1, unjudged. It
// is here rather than absent because a dataset's own statement about whether its
// rows are judged belongs with the rows; the label plane fills it in with an
// additive ALTER, which is the same way every other table in this warehouse has
// grown a column.
const rowDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.risk_row (
		org         String,
		dataset     String,
		version     UInt32,
		split       UInt8,
		kind        LowCardinality(String),
		subject     String,
		id          String,
		at          DateTime,
		point       Array(Float64),
		disposition Int8
	) ENGINE = ReplacingMergeTree()
	PARTITION BY (org, dataset)
	ORDER BY (org, dataset, version, split, at, id)`

// ensure creates this plane's own tables idempotently. Only SUCCESS latches in
// the caller, so a warehouse still connecting at boot is retried rather than
// poisoned.
func (p *plane) ensure(ctx context.Context) error {
	if !p.store.Ready() {
		return errStore
	}
	for _, stmt := range []string{`CREATE DATABASE IF NOT EXISTS hanzo`, manifestDDL, rowDDL} {
		if err := p.store.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("%w: ensure the schema: %v", errStore, err)
		}
	}
	return nil
}

// ── the manifest ─────────────────────────────────────────────────────────────

// entry is one version of one dataset, as the manifest holds it.
type entry struct {
	Name      string
	Version   int
	At        time.Time
	By        string
	Status    string
	Refusal   string
	Spec      record
	Source    string
	Digest    string
	Counts    counts
	Horizon   int
	Share     int
	Truncated bool
}

// manifestColumns is the fixed column list of every manifest write and read, in
// ONE order, so the writer's argument list and the reader's decoder cannot drift.
var manifestColumns = []string{
	"org", "name", "version", "seq", "at", "by", "status", "refusal",
	"spec", "source", "digest", "rows", "train", "val", "test", "subjects",
	"judged", "productive", "unproductive", "horizon", "share", "truncated",
}

// put writes manifest rows. It is the ONLY writer of hanzo.risk_dataset.
//
// The tenant is the first bound value AND the first component of the partition
// expression, so a row cannot be filed under a tenant other than the key that
// wrote it.
//
// It is variadic so a disposal — which marks every version of one dataset at once
// — writes ONE statement rather than a round trip per version, without there
// being a second writer that could bind the columns in a different order. Writing
// nothing is a no-op, never an INSERT with no values.
func (p *plane) put(ctx context.Context, k tenant.Key, es ...entry) error {
	if k.Zero() {
		return errNoTenant
	}
	if len(es) == 0 {
		return nil
	}
	if !p.store.Ready() {
		return errStore
	}
	group := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(manifestColumns)), ", ") + ")"
	args := make([]any, 0, len(es)*len(manifestColumns))
	for _, e := range es {
		args = append(args,
			k.String(), e.Name, uint32(e.Version), rank(e.Status), e.At.UTC(), e.By, e.Status, e.Refusal,
			e.Spec.canon(), e.Source, e.Digest,
			uint64(e.Counts.Rows), uint64(e.Counts.Train), uint64(e.Counts.Val), uint64(e.Counts.Test),
			uint64(e.Counts.Subjects), uint64(e.Counts.Judged), uint64(e.Counts.Productive), uint64(e.Counts.Unproductive),
			uint32(e.Horizon), uint16(e.Share), boolByte(e.Truncated))
	}
	stmt := "INSERT INTO " + manifestTable + " (" + strings.Join(manifestColumns, ", ") + ") VALUES " +
		strings.TrimSuffix(strings.Repeat(group+", ", len(es)), ", ")
	return p.store.Exec(ctx, stmt, args...)
}

// versions reads every version of one dataset, newest first.
//
// FINAL collapses the lifecycle rows to the current one per version. It is
// affordable here and only here: the predicate pins (org, name), which IS the
// partition expression, so FINAL merges the parts of one small partition rather
// than the table.
func (p *plane) versions(ctx context.Context, k tenant.Key, name string) ([]entry, error) {
	return p.read(ctx, k,
		"SELECT "+strings.Join(manifestColumns, ", ")+" FROM "+manifestTable+
			" FINAL WHERE org = ? AND name = ? ORDER BY version DESC LIMIT ?",
		k.String(), name, maxVersions)
}

// latest reads one dataset's newest version, or reports that there is none.
func (p *plane) latest(ctx context.Context, k tenant.Key, name string) (entry, bool, error) {
	es, err := p.versions(ctx, k, name)
	if err != nil || len(es) == 0 {
		return entry{}, false, err
	}
	return es[0], true, nil
}

// version reads one exact version.
func (p *plane) version(ctx context.Context, k tenant.Key, name string, v int) (entry, bool, error) {
	es, err := p.read(ctx, k,
		"SELECT "+strings.Join(manifestColumns, ", ")+" FROM "+manifestTable+
			" FINAL WHERE org = ? AND name = ? AND version = ?",
		k.String(), name, uint32(v))
	if err != nil {
		return entry{}, false, err
	}
	switch len(es) {
	case 0:
		return entry{}, false, nil
	case 1:
		return es[0], true, nil
	default:
		// FAIL SECURE. Two surviving rows for one version means the register is
		// not single-valued, and a version whose contents are ambiguous cannot be
		// the thing a model names. Answering with either one would make every
		// later citation of this version unfalsifiable.
		return entry{}, false, fmt.Errorf("dataset %q version %d is not single-valued in the register", name, v)
	}
}

// names lists every dataset this tenant has, ONE ROW EACH: its newest version.
//
// `LIMIT 1 BY name` is what makes this an O(names) read instead of an
// O(names × versions) one. Reading every version and keeping the first per name
// in Go would answer the same question and cost a tenant's whole register — 64
// names by 256 versions, each carrying a spec to decode — on an op that is a free
// read anyone can loop. A bound that holds only because the caller discards most
// of what it asked for is not a bound.
func (p *plane) names(ctx context.Context, k tenant.Key) ([]entry, error) {
	return p.read(ctx, k,
		"SELECT "+strings.Join(manifestColumns, ", ")+" FROM "+manifestTable+
			" FINAL WHERE org = ? ORDER BY name, version DESC LIMIT 1 BY name LIMIT ?",
		k.String(), maxNames)
}

// read runs a manifest statement and decodes it. It is the ONE decoder, so the
// column order above is read in exactly one place.
//
// It REFUSES a row whose org is not the caller's. That predicate is already in
// every statement here; asserting it again on the way out is the cheap half of
// defence in depth, and it is the half that survives someone editing a WHERE
// clause. A refusal, not a filter: a store that returned another tenant's row is
// a store this plane must stop reading, not one to quietly clean up after.
func (p *plane) read(ctx context.Context, k tenant.Key, stmt string, args ...any) ([]entry, error) {
	if k.Zero() {
		return nil, errNoTenant
	}
	if !p.store.Ready() {
		return nil, errStore
	}
	raw, err := p.store.Query(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: read the register: %v", errStore, err)
	}
	out := make([]entry, 0, len(raw))
	for _, r := range raw {
		if text(r["org"]) != k.String() {
			return nil, fmt.Errorf("dataset: the register returned a row belonging to another tenant")
		}
		e := entry{
			Name:    text(r["name"]),
			Version: int(number(r["version"])),
			At:      when(r["at"]),
			By:      text(r["by"]),
			Status:  text(r["status"]),
			Refusal: text(r["refusal"]),
			Source:  text(r["source"]),
			Digest:  text(r["digest"]),
			Counts: counts{
				Rows: int(number(r["rows"])), Train: int(number(r["train"])),
				Val: int(number(r["val"])), Test: int(number(r["test"])),
				Subjects: int(number(r["subjects"])), Judged: int(number(r["judged"])),
				Productive: int(number(r["productive"])), Unproductive: int(number(r["unproductive"])),
			},
			Horizon:   int(number(r["horizon"])),
			Share:     int(number(r["share"])),
			Truncated: number(r["truncated"]) != 0,
		}
		if e.Spec, err = decode(text(r["spec"])); err != nil {
			return nil, fmt.Errorf("dataset %q version %d: %w", e.Name, e.Version, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// ── the rows ─────────────────────────────────────────────────────────────────

// rowColumns is the fixed column list of every row write and read.
var rowColumns = []string{"org", "dataset", "version", "split", "kind", "subject", "id", "at", "point", "disposition"}

// unjudged is the disposition of every row this plane writes. A dataset with no
// label plane behind it is entirely unjudged, and saying so is what lets a model
// plane refuse to rank rather than invent a winner.
const unjudged int8 = -1

// insertRows writes one batch. Batching is a bound, not an optimisation: the cap
// is enforced before this is reached, and the batch size keeps one statement from
// carrying two hundred thousand value groups.
func (p *plane) insertRows(ctx context.Context, k tenant.Key, name string, version int, rows []row) error {
	if k.Zero() {
		return errNoTenant
	}
	if !p.store.Ready() {
		return errStore
	}
	group := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(rowColumns)), ", ") + ")"
	for start := 0; start < len(rows); start += batch {
		end := min(start+batch, len(rows))
		chunk := rows[start:end]
		args := make([]any, 0, len(chunk)*len(rowColumns))
		for _, r := range chunk {
			args = append(args, k.String(), name, uint32(version), r.Split, r.Kind, r.Subject, r.ID, r.At.UTC(), r.Point, unjudged)
		}
		stmt := "INSERT INTO " + rowTable + " (" + strings.Join(rowColumns, ", ") + ") VALUES " +
			strings.TrimSuffix(strings.Repeat(group+", ", len(chunk)), ", ")
		if err := p.store.Exec(ctx, stmt, args...); err != nil {
			return fmt.Errorf("%w: write rows: %v", errStore, err)
		}
	}
	return nil
}

// batch is how many rows go in one statement. Two thousand value groups is a
// statement of a few hundred kilobytes, which the store takes comfortably and
// which keeps a failed batch small enough to be worth retrying.
const batch = 2_000

// rows reads a materialised version back, bounded and in the stored order.
//
// The tenant leads the predicate and the partition expression, so a version name
// belonging to another tenant selects nothing — not a filtered set, an absent
// one.
func (p *plane) rows(ctx context.Context, k tenant.Key, name string, version, offset, limit int, split string) ([]row, error) {
	if k.Zero() {
		return nil, errNoTenant
	}
	if !p.store.Ready() {
		return nil, errStore
	}
	where := "org = ? AND dataset = ? AND version = ?"
	args := []any{k.String(), name, uint32(version)}
	if split != "" {
		s, ok := splitOf(split)
		if !ok {
			return nil, fmt.Errorf("dataset: %q is not a split", split)
		}
		where += " AND split = ?"
		args = append(args, s)
	}
	args = append(args, uint64(limit), uint64(offset))
	raw, err := p.store.Query(ctx,
		"SELECT org, split, kind, subject, id, at, point FROM "+rowTable+
			" WHERE "+where+" ORDER BY id LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, fmt.Errorf("%w: read rows: %v", errStore, err)
	}
	out := make([]row, 0, len(raw))
	for _, r := range raw {
		if text(r["org"]) != k.String() {
			return nil, fmt.Errorf("dataset: the row plane returned a row belonging to another tenant")
		}
		out = append(out, row{
			fact: fact{
				Kind:    text(r["kind"]),
				Subject: text(r["subject"]),
				At:      when(r["at"]),
				Point:   floats(r["point"]),
			},
			ID:    text(r["id"]),
			Split: uint8(number(r["split"])),
		})
	}
	return out, nil
}

// splitOf resolves a split name. The names are a closed set and the value bound
// is the small integer, never the caller's text.
func splitOf(s string) (uint8, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "train":
		return train, true
	case "val":
		return val, true
	case "test":
		return test, true
	}
	return 0, false
}

func splitName(s uint8) string {
	switch s {
	case train:
		return "train"
	case val:
		return "val"
	default:
		return "test"
	}
}

// ── disposal ─────────────────────────────────────────────────────────────────

// dispose removes one tenant's one dataset: the BYTES go, and the register is
// MARKED with what went.
//
// THE BYTES ARE DROPPED, NOT DELETED ROW BY ROW. The rows table is partitioned by
// (org, dataset), so the tenant is the first component of the thing being
// dropped: a disposal is per tenant by construction and cannot be spelled
// cross-tenant. That is why there is no table TTL here — a TTL is a clock the
// tenant does not hold, and this plane's rule is that only the tenant's own
// retention decision expires its records.
//
// THE REGISTER IS NOT DROPPED, and that is a correction of an earlier design
// rather than a hesitation. Dropping it reset the version counter: after
// disposing `orders`, the next declaration was version 1 again, so a model citing
// `orders v3` could be pointed at rows the first v3 never contained, and nothing
// anywhere recorded that the first v3 had existed. A citation that is ambiguous
// across time is not a citation. So the register keeps ONE row per version,
// marked `disposed` — which outranks `ready` in the engine's version column, so
// the mark supersedes the publication rather than racing it — and the next
// declaration continues from the number the dataset reached.
//
// What is kept is a RECORD, not the data: the rows carrying subjects and
// coordinates are gone; the register holds the name, the number, the spec, the
// digest and who disposed of it when. That is what a retention obligation asks a
// plane to be able to answer with, and the answer to "prove you deleted it" is
// not silence.
//
// ORDER: bytes first, mark second. If the mark fails, the register still names
// versions whose bytes are gone — which every read then reports as an
// unreproducible dataset — and a repeat disposal completes it. The other order
// would leave bytes that no register names, which nothing would ever report.
func (p *plane) dispose(ctx context.Context, k tenant.Key, name string, es []entry, by string) error {
	if k.Zero() {
		return errNoTenant
	}
	if !p.store.Ready() {
		return errStore
	}
	if err := p.store.Exec(ctx, "ALTER TABLE "+rowTable+" DROP PARTITION (?, ?)", k.String(), name); err != nil {
		return fmt.Errorf("%w: dispose rows: %v", errStore, err)
	}
	marks := make([]entry, 0, len(es))
	at := time.Now().UTC()
	for _, e := range es {
		if e.Status == statusDisposed {
			// Already marked. Leaving it alone keeps the disposal instant on the
			// record the instant the disposal happened, so a repeat call — which is
			// how a failed byte-drop is retried — cannot rewrite history.
			continue
		}
		e.Status = statusDisposed
		e.Refusal = ""
		e.At = at
		e.By = by
		marks = append(marks, e)
	}
	if err := p.put(ctx, k, marks...); err != nil {
		return fmt.Errorf("%w: record the disposal: %v", errStore, err)
	}
	return nil
}

// ── the source ───────────────────────────────────────────────────────────────

// census is what the window holds, measured before anything is read out of it. It
// is how the membership share is chosen, and it is half of the lineage
// fingerprint.
type census struct {
	Rows     int
	Subjects int
	First    time.Time
	Last     time.Time
}

// sourceWhere is THE source predicate: org first and bound, then the kind, then
// the window. The order follows the source table's own sort key
// (org, subject_kind, subject, bucket), so the read is a prefix scan.
func sourceWhere(k tenant.Key, s spec, until time.Time) (string, []any) {
	where := []string{"org = ?"}
	args := []any{k.String()}
	if s.Kind != "" {
		where = append(where, "subject_kind = ?")
		args = append(args, s.Kind)
	}
	where = append(where, "bucket >= ?", "bucket < ?")
	args = append(args, literal(s.From), literal(until))
	return strings.Join(where, " AND "), args
}

// census measures the window: how many distinct (kind, subject, bucket) rows it
// would produce, how many subjects those belong to, and its true extent.
//
// It counts the GROUPED cardinality rather than raw rows, because the source is a
// SummingMergeTree whose unmerged parts hold several physical rows per key — a
// raw count() would over-report and the share would sample harder than it needs.
//
// It is the plane's most expensive statement — an EXACT distinct-count, not an
// estimate, over a window that may be 400 days wide — which is why it takes a
// [scan] and not a key: the admission is the parameter, so an op that wanted to
// run this for free would have to obtain one, and [plane.admit] is the only
// place one exists.
func (p *plane) census(ctx context.Context, a scan, s spec, until time.Time) (census, error) {
	k := a.k
	if k.Zero() {
		return census{}, errNoTenant
	}
	if !p.store.Ready() {
		return census{}, errStore
	}
	where, args := sourceWhere(k, s, until)
	raw, err := p.store.Query(ctx,
		"SELECT uniqExact((subject_kind, subject, bucket)) AS rows, uniqExact((subject_kind, subject)) AS subjects,"+
			" min(bucket) AS first, max(bucket) AS last FROM "+sourceTable+" WHERE "+where, args...)
	if err != nil {
		return census{}, fmt.Errorf("%w: measure the source: %v", errStore, err)
	}
	if len(raw) == 0 {
		return census{}, nil
	}
	return census{
		Rows:     int(number(raw[0]["rows"])),
		Subjects: int(number(raw[0]["subjects"])),
		First:    when(raw[0]["first"]),
		Last:     when(raw[0]["last"]),
	}, nil
}

// facts reads the window, bounded, sampled and ordered.
//
// The membership sample is `cityHash64(seed, subject) % 1000 < share`. It is
// applied to the SUBJECT and not to the row, so a subject is either wholly in or
// wholly out and no split can be handed half of one. The seed binds, so the same
// seed selects the same subjects however many times this runs, and a different
// seed selects a different sample — which is what makes a capped dataset
// reproducible instead of "whatever came back first".
//
// The order is the source's own sort key, so the LIMIT truncates at a subject
// boundary this package can find and drop ([trim]).
func (p *plane) facts(ctx context.Context, a scan, s spec, until time.Time, share int) ([]fact, bool, error) {
	k := a.k
	if k.Zero() {
		return nil, false, errNoTenant
	}
	if !p.store.Ready() {
		return nil, false, errStore
	}
	where, args := sourceWhere(k, s, until)
	if share < shareDenominator {
		where += fmt.Sprintf(" AND cityHash64(?, subject) %% %d < ?", shareDenominator)
		args = append(args, s.Seed, uint32(share))
	}
	limit := s.Rows
	args = append(args, uint64(limit))

	sel := make([]string, 0, len(s.Dims))
	for _, c := range s.columns() {
		sel = append(sel, "sum("+c+") AS "+c)
	}
	raw, err := p.store.Query(ctx,
		"SELECT subject_kind, subject, bucket, "+strings.Join(sel, ", ")+
			" FROM "+sourceTable+" WHERE "+where+
			" GROUP BY subject_kind, subject, bucket"+
			" ORDER BY subject_kind, subject, bucket LIMIT ?", args...)
	if err != nil {
		return nil, false, fmt.Errorf("%w: read the source: %v", errStore, err)
	}
	cols := s.columns()
	out := make([]fact, 0, len(raw))
	for _, r := range raw {
		f := fact{
			Kind:    text(r["subject_kind"]),
			Subject: text(r["subject"]),
			At:      when(r["bucket"]),
			Point:   make([]float64, len(cols)),
		}
		for i, c := range cols {
			f.Point[i] = number(r[c])
		}
		out = append(out, f)
	}
	return out, len(raw) >= limit, nil
}

// retention reads the SOURCE table's own TTL from the store's catalogue.
//
// It is read rather than asserted. If the source's retention is shorter than a
// dataset's window, that dataset can never be re-derived from source, and every
// lineage claim about it is false — so the fact has to come from the store, and a
// store that will not answer produces an empty string rather than a comfortable
// default.
func (p *plane) retention(ctx context.Context) string {
	if !p.store.Ready() {
		return ""
	}
	db, tbl, _ := strings.Cut(sourceTable, ".")
	raw, err := p.store.Query(ctx,
		"SELECT engine_full FROM system.tables WHERE database = ? AND name = ?", db, tbl)
	if err != nil || len(raw) == 0 {
		return ""
	}
	full := text(raw[0]["engine_full"])
	i := strings.Index(full, "TTL ")
	if i < 0 {
		return ""
	}
	ttl := full[i:]
	if j := strings.Index(ttl, "\nSETTINGS"); j > 0 {
		ttl = ttl[:j]
	}
	return strings.TrimSpace(ttl)
}

// ── decoding ─────────────────────────────────────────────────────────────────
//
// The store decodes each column into its native scan type keyed by column name,
// so a reader coerces. These are the only coercions this package performs and
// they are total: an unreadable value reads as its zero, and every caller of
// [number] is a count whose zero is the honest answer for "absent".

func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int8:
		return float64(n)
	case int16:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint8:
		return float64(n)
	case uint16:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	default:
		return 0
	}
}

func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func when(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	return time.Time{}
}

func floats(v any) []float64 {
	switch a := v.(type) {
	case []float64:
		return a
	case []float32:
		out := make([]float64, len(a))
		for i, f := range a {
			out[i] = float64(f)
		}
		return out
	}
	return nil
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
