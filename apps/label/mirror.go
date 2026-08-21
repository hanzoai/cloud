package label

// mirror.go is the DERIVED copy: the tenant's assertions in the columnar plane,
// so a dataset materialisation can join a hundred million feature rows to their
// labels in the warehouse instead of pulling them through a hundred thousand
// SQLite files.
//
// IT IS NOT THE RECORD. Every write lands in the tenant's own file first and is
// mirrored after; a mirror write that fails is reported on the response and the
// record stands. A caller that needs certainty reads /v1/label, which reads
// the file. Nothing here is ever the answer to "what did we assert" — only to
// "join this at scale".
//
// THREE THINGS THE TABLE GETS RIGHT, EACH FOR A STATED REASON.
//
//   - `org` LEADS the sort key and is the QUALIFIED key `<brand>/<org>`, matching
//     hanzo.risk_feature exactly. A per-tenant read is then a prefix scan rather
//     than a full-range filter (which is what hanzo.cloud_usage's
//     ORDER BY (timestamp, organization, …) costs it), and `acme` on hanzo.id
//     cannot answer `acme` on zoo.ngo's query.
//
//   - `seen` IS IN THE SORT KEY. A ReplacingMergeTree collapses rows that agree
//     on the whole sort key, so a key of (org, kind, subject, at, source, id)
//     alone would be correct only by accident and a key without `id` would
//     silently DELETE a source's earlier assertion the moment it corrected
//     itself — destroying the one property this plane exists to provide, that a
//     past observation instant still sees what it saw. With `seen` and `id` both
//     present, only a byte-identical redelivery collapses, which is exactly the
//     idempotence the record plane already gives.
//
//   - `knowable` IS A COLUMN, and it is the one the leakage predicate reads. The
//     first cut carried `seen` alone, so the warehouse could not apply the guard
//     at all: `seen` is the filer's claim and the derived instant — the later of
//     that claim and the server clock at the write — lived only in the record
//     plane. A materialiser joining here would have resolved under a rule the
//     record plane had already rejected, which is two answers to one question.
//
//   - `by` IS DOUBLE-QUOTED, here and in the record plane's SQLite, because BY is
//     a keyword in both dialects and both accept the SQL standard's quoted
//     identifier. It stays the SAME WORD on the wire, in the Go field and in both
//     columns rather than becoming three spellings of one fact — quoting is how a
//     dialect is accommodated, renaming is how a fact acquires aliases.
//
//   - NO TTL, and PARTITION BY MONTH. A table TTL is a fleet-wide clock no tenant
//     can hold a record past, which contradicts per-tenant retention outright, so
//     there is none: disposal is this plane's own per-tenant sweep.
//
//     The partition key is the event month, matching every other table in the
//     fleet (apps/usage, apps/samples, apps/links, apps/eval, apps/leaderboard all
//     partition toYYYYMM). It was `org` for one release, justified as making
//     disposal a DROP PARTITION — which purge() deliberately does not do, because
//     a retention boundary disposes of a PREFIX of a tenant's history and not all
//     of it. So the justification was never taken up, and what was left was the
//     only unbounded-cardinality partition key in the warehouse: one partition per
//     tenant, on a shared single-pod engine, with directory count, part metadata
//     and merge scheduling all growing with the tenant count. Tenant isolation
//     does not come from the partition and never did — it comes from `org` leading
//     the sort key and being a bound predicate on every statement.

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/tenant"
)

// columnar is the pair of statements that reach the derived copy: the write and
// the disposal. They travel TOGETHER as one value so the two halves are always
// aimed at the same world — a suite that could stub one and leave the other
// pointed at a real warehouse would be proving something nobody deploys.
type columnar struct {
	send  func(context.Context, tenant.Key, []Fact) error
	sweep func(context.Context, tenant.Key, []string) error
}

// warehouse is the real thing: the process's one warehouse connection.
var warehouse = columnar{send: mirror, sweep: purge}

const createDatabase = `CREATE DATABASE IF NOT EXISTS hanzo`

const labelDDL = `
CREATE TABLE IF NOT EXISTS hanzo.risk_label (
	org         String,
	kind        LowCardinality(String),
	subject     String,
	at          DateTime,
	seen        DateTime,
	knowable    DateTime,
	disposition Int8,
	source      LowCardinality(String),
	evidence    String,
	"by"        String,
	confidence  Float64,
	id          String
) ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMM(at)
ORDER BY (org, kind, subject, at, source, seen, id)`

// ready latches only on SUCCESS, so a warehouse still connecting at boot is
// retried on the next write rather than poisoned for the life of the process.
var ready atomic.Bool

// ensure creates the mirror table. Idempotent.
func ensure(ctx context.Context) error {
	if ready.Load() {
		return nil
	}
	if !datastore.Ready() {
		return fmt.Errorf("the warehouse is not connected")
	}
	if err := datastore.Exec(ctx, createDatabase); err != nil {
		return err
	}
	if err := datastore.Exec(ctx, labelDDL); err != nil {
		return err
	}
	ready.Store(true)
	return nil
}

// mirror copies a batch of already-recorded assertions into the columnar plane.
//
// The tenant key is bound, never interpolated, and it is the caller's MINTED key
// — the only value in this file that names a tenant, and one no request body can
// carry.
func mirror(ctx context.Context, t tenant.Key, facts []Fact) error {
	if len(facts) == 0 {
		return nil
	}
	if err := ensure(ctx); err != nil {
		return err
	}
	stmt, args := insert(t, facts)
	return datastore.Exec(ctx, stmt, args...)
}

// drain is how many assertions one delivery attempt carries. It is twice the
// write batch on purpose: a caller filing a full batch would otherwise fill the
// attempt with its own new rows and the backlog would never shrink, so half of
// every attempt is reserved for catching up.
const drain = 2 * maxAssert

// maxPending is how far the backlog is counted before the number saturates. It
// is the drain's own bound times a hundred: past that the answer is "a backlog
// to work through", which is the only decision the number informs.
const maxPending = 100 * drain

// deliver makes the derived copy catch up, and it is the whole repair mechanism.
//
// WITHOUT IT THE MIRROR LOSES ROWS PERMANENTLY, AND SILENTLY. A warehouse that is
// down for an hour takes no rows for that hour; the records are safe in the
// tenant's own file, so nothing is lost — but a redelivery of the same webhook
// resolves to `duplicate`, nothing new "lands", and the old code mirrored only
// what landed. The gap could therefore never be closed by any retry, and a
// warehouse-side training join would quietly be missing exactly the labels that
// arrived during the outage. A fraud label that is missing reads as an honest
// customer, which is the one error this plane exists to prevent.
//
// The mechanism is a cursor and nothing else: send everything past it in the
// (wrote, id) order, then move it to the last row sent. It is idempotent either
// way — the columnar table collapses a byte-identical row — so a re-send costs a
// merge and never a wrong answer, and a store that has delivered nothing simply
// re-delivers its whole history. That is why there is no migration for an
// existing tenant: it repairs itself on its next write.
//
// It reports what is still pending rather than looping: a bounded attempt that
// makes progress on every call is what keeps one tenant's ten-million-row
// catch-up from becoming every other tenant's latency. It reports what it SENT
// as well, because moving the cursor is a write to the tenant's file and the
// caller owes that file a ship before it acknowledges anything.
func deliver(ctx context.Context, t tenant.Key, st *store, c columnar) (sent, pending int, err error) {
	at, err := st.mark(ctx)
	if err != nil {
		return 0, 0, err
	}
	batch, err := st.undelivered(ctx, at, drain)
	if err != nil {
		return 0, 0, err
	}
	if len(batch) == 0 {
		return 0, 0, nil
	}
	if err := c.send(ctx, t, batch); err != nil {
		n, _ := st.pending(ctx, at, maxPending)
		return 0, n, err
	}
	to := cursor(batch[len(batch)-1].Seq)
	if err := st.advance(ctx, to); err != nil {
		return 0, 0, err
	}
	n, err := st.pending(ctx, to, maxPending)
	return len(batch), n, err
}

// insert builds the batch write. Separate from mirror so the statement and its
// bindings are inspectable without a warehouse — the tenant key leading every
// row is the property worth pinning, and it is not one a test can see through
// a connection that is down.
func insert(t tenant.Key, facts []Fact) (string, []any) {
	const width = 12
	values := make([]string, 0, len(facts))
	args := make([]any, 0, len(facts)*width)
	for _, f := range facts {
		values = append(values, "(?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			t.String(), string(f.Kind), f.Subject, f.At, f.Seen, f.Knowable,
			f.Disposition.code(), string(f.Source), f.Evidence, f.By, f.Confidence, f.ID)
	}
	return `
INSERT INTO hanzo.risk_label
(org, kind, subject, at, seen, knowable, disposition, source, evidence, "by", confidence, id)
VALUES ` + strings.Join(values, ","), args
}

// purge is the columnar half of the retention sweep: the named records, in one
// tenant's partition, gone.
//
// TWO BOUND PREDICATES, AND BOTH ARE LOAD-BEARING. `org` alone would let a
// disposal reach a neighbour if a key were ever mis-minted; `id` alone would let
// one tenant's boundary delete another tenant's row that happens to carry the
// same content digest — which is not hypothetical, because the digest is over
// the assertion's content and two tenants CAN assert identical facts about
// identically-named subjects. Together they name exactly this tenant's rows.
//
// It is a lightweight DELETE rather than a DROP PARTITION because a retention
// boundary disposes of a PREFIX of a tenant's history, not all of it.
// CHUNKED, and the number is measured rather than chosen. A DELETE is not an
// INSERT: the driver streams an insert's rows as a native block, but it renders a
// delete's bindings into the statement TEXT, where the engine's max_query_size
// (256 KiB by default) applies. A sweep naming this package's own maxDispose of
// ten thousand 64-character digests builds a 262 KB statement and is refused with
// code 62 — so a tenant with more than a few thousand expired records could never
// dispose of anything at all, which is a retention plane that has stopped working
// and does not say so.
//
// A partial sweep is safe to retry: the columnar copy is disposed of before the
// record, so a chunk that failed leaves rows the record still names and the next
// call identifies and deletes them again. Retrying a DELETE that already ran
// costs nothing.
func purge(ctx context.Context, t tenant.Key, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := ensure(ctx); err != nil {
		return err
	}
	for chunk := range chunks(ids, purgeChunk) {
		stmt, args := deletion(t, chunk)
		if err := datastore.Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

// purgeChunk is how many ids one disposal statement names. A digest renders to
// 66 bytes with its quotes and separator, so two thousand is about 132 KB —
// half the engine's default ceiling, which leaves room for a ceiling that is
// lowered rather than a statement that is grown.
const purgeChunk = 2000

// deletion builds the disposal. Separate from purge for the same reason insert
// is separate from mirror: the two bound predicates are the whole safety
// argument and a test must be able to read them.
func deletion(t tenant.Key, ids []string) (string, []any) {
	holes := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, t.String())
	for i, id := range ids {
		holes[i] = "?"
		args = append(args, id)
	}
	return `DELETE FROM hanzo.risk_label WHERE org = ? AND id IN (` + strings.Join(holes, ",") + `)`, args
}

// Order is the precedence rule rendered as a Datastore ordering tuple, for the
// dataset materialiser to resolve labels IN the warehouse — `argMin(disposition,
// `+label.Order()+`)` picks the same winner the Go comparator would.
//
// It is GENERATED from the same precedence map stronger() reads, so a source
// added to the vocabulary appears in both orderings and cannot be forgotten in
// one. That is the only defence available against a rule with two homes:
// orderCoversEverySource pins it, and mirrorOrderTest pins the rendering.
//
// The terms match stronger() one for one — rank ascending, knowable DESCENDING
// (so negated), confidence descending (negated), id ascending.
func Order() string {
	var b strings.Builder
	b.WriteString("(transform(source, [")
	names := make([]string, 0, len(precedence))
	ranks := make([]string, 0, len(precedence))
	for _, s := range sources() {
		r, _ := rank(s)
		names = append(names, "'"+string(s)+"'")
		ranks = append(ranks, fmt.Sprint(r))
	}
	b.WriteString(strings.Join(names, ","))
	b.WriteString("], [")
	b.WriteString(strings.Join(ranks, ","))
	// An unknown source sorts LAST, matching rank()'s fallback: an unrecognised
	// claim must never win.
	//
	// toInt64 before negating: toUnixTimestamp is UNSIGNED, and negating an
	// unsigned is the kind of promotion that is right by accident until the day
	// it wraps.
	fmt.Fprintf(&b, "], %d), -toInt64(toUnixTimestamp(knowable)), -confidence, id)", len(precedence))
	return b.String()
}

// ResolvedSQL is the warehouse-side read the dataset materialiser joins against:
// one row per judged event, resolved at that event's OWN as-of instant, over one
// tenant.
//
// The horizon arithmetic is in SQL rather than in a loop over rows, because the
// alternative is shipping every assertion to the app to filter it — the read
// amplification a single-pod warehouse cannot afford. Two predicates carry the
// whole leakage rule and both are per row, not per batch:
//
//	knowable <= at + h   an assertion is visible only at the event's own as-of
//	at + h <= now        the event has matured and may be admitted at all
//
// The first reads `knowable` — the SERVER-DERIVED instant, the later of the
// filer's `seen` and the clock at the write — and it is the same column and the
// same comparison visible() makes in Go. A predicate over `seen` alone would
// hold the warehouse to a weaker rule than the record plane, and a materialiser
// would then train on rows the resolve op refuses to return.
//
// `org` is the LEADING bound predicate and the table's first sort term, so a
// tenant read is a prefix scan. Nothing a caller sends becomes statement text.
//
// ONE argMin OVER A TUPLE, NOT ONE PER COLUMN, for two reasons and the second is
// the one that matters. The engine refuses the per-column form outright — an
// alias `AS source` shadows the column the ordering expression reads, and the
// second argMin then finds an aggregate inside its own argument (ILLEGAL_
// AGGREGATION, code 184). And even where it parsed, three independent argMins
// over one ordering tuple could each answer from a different row on a tie, so
// "the winner" would be a chimera assembled from two assertions. Picking the
// whole payload once makes that unrepresentable: every projected field comes
// from the row that won.
//
// The six placeholders bind, in order:
//
//	org      the qualified tenant key `<brand>/<org>`
//	from,to  the event window, half-open
//	h        the maturity horizon IN SECONDS (DateTime + Int adds seconds)
//	h        the same horizon, for the maturity predicate
//	now      the materialisation instant
//
// The projection carries the winner's whole provenance — `source`, `id`,
// `knowable` and `confidence` beside the disposition — so a training row and an
// adverse action can both name what judged them without a second read.
// `knowable` and not `seen`: the instant published beside a resolved label has to
// be the one the guard actually applied, or nobody can check the answer. `contested` is
// true when the visible assertions hold more than one disposition, matching
// Resolved.Contested exactly: two sources that agree are corroboration.
//
// It is exported and unused inside this package by design: it is the CONTRACT
// the dataset plane joins against, published beside the table it reads so the
// two cannot drift.
func ResolvedSQL() string {
	return `
SELECT kind, subject, at,
       tupleElement(won, 1) AS disposition,
       tupleElement(won, 2) AS source,
       tupleElement(won, 3) AS id,
       tupleElement(won, 4) AS knowable,
       tupleElement(won, 5) AS confidence,
       contested
FROM (
  SELECT kind, subject, at,
         argMin((disposition, source, id, knowable, confidence), ` + Order() + `) AS won,
         countDistinct(disposition) > 1 AS contested
  FROM hanzo.risk_label
  WHERE org = ?
    AND at >= ? AND at < ?
    AND knowable <= at + ?
    AND at + ? <= ?
  GROUP BY kind, subject, at
)`
}
