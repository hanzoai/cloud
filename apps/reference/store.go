package reference

// store.go is the durable baseline: two tables in the shared warehouse, and the
// ingest that fills them.
//
// THE TABLES HAVE NO TENANT COLUMN, AND THAT IS THE SECURITY ARGUMENT. Read the
// DDL below as the proof rather than as schema: there is nowhere in either shape
// for an organisation to go. A tenant's own allow and deny entries live
// somewhere else entirely — one SQLite file per organisation, opened through
// cloud.OrgNamespace (override.go) — so the two planes are not two rows in one
// table separated by a predicate, they are two different stores. A bug that
// leaked one into the other would have to open another organisation's file,
// which is the same isolation every other per-entity store in this binary
// already has. There is no `scope` column to get wrong.
//
// WHAT MAY GO IN HERE. Data someone else published under a licence we hold, and
// aggregates over fleet traffic that pass the k-anonymity floor in derive.go.
// Nothing else. reference_test.go holds both halves of that to a test.
//
// IDEMPOTENT BY CONTENT ADDRESS. A version IS the digest of the entries, so
// fetching the same set twice produces the same version, writes the same primary
// keys, and a ReplacingMergeTree collapses them. Re-running an ingest is not
// merely safe, it is a no-op that says so.
//
// RESUMABLE BY CURSOR, CORRECT BY MERGE. Entries land in sorted chunks and the
// manifest's `landed` counter advances after each one, so a run that dies at
// chunk k resumes at chunk k. The counter is an OPTIMISATION: because the
// version is content-addressed, a resumed run re-derives the identical sorted
// list, so re-writing a chunk that already landed produces the same rows the
// merge already deduplicates. The cursor saves the work; the primary key
// guarantees the answer.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
)

const (
	// sourceTable holds one row per (set, source, version): what was taken, from
	// where, under what terms, when, and how much of it landed.
	sourceTable = "hanzo.reference_source"
	// entryTable holds the members of every version.
	entryTable = "hanzo.reference_entry"
	// chunk is how many entries land per statement. Big enough that a 65,000-entry
	// list is a handful of round trips, small enough that a failure costs one.
	chunk = 5000
	// ddlTimeout bounds the idempotent table bootstrap.
	ddlTimeout = 10 * time.Second
)

// Statuses a version passes through. `ready` is the only one a snapshot builds
// from: a half-landed version must never answer, because a set that is missing
// its tail answers "not listed" for everything in it.
// `refused` is the third: a take that produced no answerable version, recorded so
// the failure is durable and the source's row shows why. Because only `ready` is
// read back (currentStatement), recording a refusal leaves the previous ready
// version current — which is the correct outcome for a publisher that stopped
// answering, and the one that ages out visibly instead of silently emptying a set.
const (
	statusIngest  = "ingesting"
	statusReady   = "ready"
	statusRefused = "refused"
)

const createDatabase = `CREATE DATABASE IF NOT EXISTS hanzo`

// createSource declares the version manifest. It carries NO TTL on purpose.
// Provenance is a record: a decision names the version it consulted, and that
// name has to keep resolving to a publisher, a licence and a date long after the
// bulk membership of that version has been pruned.
const createSource = `CREATE TABLE IF NOT EXISTS hanzo.reference_source (
  set      LowCardinality(String),
  source   LowCardinality(String),
  version  String,
  origin   String,
  terms    String,
  as_of    DateTime,
  fetched  DateTime,
  keys     UInt64,
  landed   UInt64,
  status   LowCardinality(String),
  refusal  String,
  at       DateTime
) ENGINE = ReplacingMergeTree(at)
ORDER BY (set, source, version)`

// createEntry declares the membership. Also no TTL: a table TTL is a clock
// nobody can hold, and a version's rows must live exactly as long as that
// version is current or one behind it. Superseded versions are pruned by the
// ingest that supersedes them, which is the only moment anything knows.
const createEntry = `CREATE TABLE IF NOT EXISTS hanzo.reference_entry (
  set     LowCardinality(String),
  source  LowCardinality(String),
  version String,
  key     String,
  value   Map(String, String),
  score   Float64,
  orgs    UInt32,
  n       UInt64,
  at      DateTime
) ENGINE = ReplacingMergeTree(at)
ORDER BY (set, source, version, key)`

// The warehouse as VALUES, on the same terms as apps/analytics/warehouse.go:
// production is always the ONE datastore client, and a test substitutes them to
// drive the durable half — the version manifest, the resume cursor, the prune —
// without standing up a store. They are the only door this package reaches the
// warehouse through, so there is one place to substitute and no second path that
// could stay real while these are faked.
var (
	storeReady = datastore.Ready
	storeQuery = datastore.Query
	storeExec  = datastore.Exec
)

// tableMu guards the lazy bootstrap. Only SUCCESS is latched: a failed DDL
// leaves the flag false so the next call retries. sync.Once is wrong here — it
// would cache the failure forever, and the warehouse connects asynchronously so
// the first attempt usually happens before it is up.
var (
	tableMu    sync.Mutex
	tableReady bool
)

// ensure creates the database and both tables exactly once successfully.
func ensure(ctx context.Context) error {
	tableMu.Lock()
	defer tableMu.Unlock()
	if tableReady {
		return nil
	}
	if !storeReady() {
		return fmt.Errorf("reference: the warehouse is not connected")
	}
	for _, stmt := range []string{createDatabase, createSource, createEntry} {
		if err := storeExec(ctx, stmt); err != nil {
			return fmt.Errorf("reference: bootstrap: %w", err)
		}
	}
	tableReady = true
	return nil
}

// version is one taken version of one source: the manifest row.
type version struct {
	Set     string
	Source  string
	Version string
	Origin  string
	Terms   string
	AsOf    time.Time
	Fetched time.Time
	Keys    uint64
	Landed  uint64
	Status  string
	Refusal string
}

// ready reports whether this version may be answered from.
func (v version) ready() bool { return v.Status == statusReady && v.Landed >= v.Keys }

// current reads the newest ready version of every source, one row each.
//
// LIMIT 1 BY is what keeps this bounded as history accumulates: without it the
// read grows with every version ever taken, which is the read amplification a
// single-pod warehouse cannot afford.
const currentStatement = `SELECT set, source, version, origin, terms, as_of, fetched, keys, landed, status, refusal
FROM ` + sourceTable + ` FINAL
WHERE status = ?
ORDER BY set, source, fetched DESC
LIMIT 1 BY set, source`

func current(ctx context.Context) ([]version, error) {
	if err := ensure(ctx); err != nil {
		return nil, err
	}
	rows, err := storeQuery(ctx, currentStatement, statusReady)
	if err != nil {
		return nil, fmt.Errorf("reference: read versions: %w", err)
	}
	out := make([]version, 0, len(rows))
	for _, r := range rows {
		out = append(out, asVersion(r))
	}
	return out, nil
}

// taken reads one specific version's manifest row, which is how a resumed ingest
// learns where it stopped.
const takenStatement = `SELECT set, source, version, origin, terms, as_of, fetched, keys, landed, status, refusal
FROM ` + sourceTable + ` FINAL
WHERE set = ? AND source = ? AND version = ?`

func taken(ctx context.Context, set, source, ver string) (version, bool, error) {
	rows, err := storeQuery(ctx, takenStatement, set, source, ver)
	if err != nil {
		return version{}, false, fmt.Errorf("reference: read version: %w", err)
	}
	if len(rows) == 0 {
		return version{}, false, nil
	}
	return asVersion(rows[0]), true, nil
}

func asVersion(r map[string]any) version {
	return version{
		Set:     text(r["set"]),
		Source:  text(r["source"]),
		Version: text(r["version"]),
		Origin:  text(r["origin"]),
		Terms:   text(r["terms"]),
		AsOf:    when(r["as_of"]),
		Fetched: when(r["fetched"]),
		Keys:    count64(r["keys"]),
		Landed:  count64(r["landed"]),
		Status:  text(r["status"]),
		Refusal: text(r["refusal"]),
	}
}

const markStatement = `INSERT INTO ` + sourceTable +
	` (set, source, version, origin, terms, as_of, fetched, keys, landed, status, refusal, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// mark writes the manifest row. ReplacingMergeTree(at) on (set, source,
// version) means the newest write of one version wins, so a status change is an
// insert rather than an update — the plane has one way to write a row.
func mark(ctx context.Context, v version, at time.Time) error {
	return storeExec(ctx, markStatement,
		v.Set, v.Source, v.Version, v.Origin, v.Terms,
		v.AsOf.UTC(), v.Fetched.UTC(), v.Keys, v.Landed, v.Status, v.Refusal, at.UTC())
}

// landed is the outcome of an ingest: which version is current now, and whether
// this run actually changed anything.
type landing struct {
	Version   string
	Keys      int
	Wrote     int
	Unchanged bool
	Resumed   bool
}

// ingest lands one source's entries as a version, idempotently and resumably.
//
// The order of operations is the design: content-address FIRST, so a re-run of
// an unchanged source is answered before a single row is written; then mark the
// version `ingesting` so a crash leaves a visible half-version rather than a
// silent gap; then land the chunks, advancing the cursor; then mark `ready`,
// which is the moment the version becomes answerable; then prune what it
// superseded.
func ingest(ctx context.Context, set string, src Source, entries []Entry, asOf, now time.Time) (landing, error) {
	if err := ensure(ctx); err != nil {
		return landing{}, err
	}
	sorted := order(entries)
	ver := digest(sorted)

	prior, found, err := taken(ctx, set, src.Name, ver)
	if err != nil {
		return landing{}, err
	}
	if found && prior.ready() {
		// The same set, already landed. Re-stamp the manifest so a refresh that
		// changed nothing is distinguishable from a refresh that did not run — the
		// distinction luxfi/aml pkg/screen's Fitness.Digest exists to make — and
		// write not one entry row.
		prior.Fetched = now
		prior.AsOf = asOf
		if err := mark(ctx, prior, now); err != nil {
			return landing{}, err
		}
		return landing{Version: ver, Keys: len(sorted), Unchanged: true}, nil
	}

	from := 0
	resumed := false
	if found && prior.Landed > 0 && prior.Landed < uint64(len(sorted)) {
		from = int(prior.Landed)
		resumed = true
	}

	v := version{
		Set: set, Source: src.Name, Version: ver,
		Origin: src.Origin, Terms: src.Terms,
		AsOf: asOf, Fetched: now,
		Keys: uint64(len(sorted)), Landed: uint64(from), Status: statusIngest,
	}
	if err := mark(ctx, v, now); err != nil {
		return landing{}, err
	}

	wrote := 0
	for i := from; i < len(sorted); i += chunk {
		end := min(i+chunk, len(sorted))
		if err := land(ctx, set, src.Name, ver, sorted[i:end], now); err != nil {
			return landing{}, err
		}
		wrote += end - i
		v.Landed = uint64(end)
		if err := mark(ctx, v, now); err != nil {
			return landing{}, err
		}
	}

	v.Status = statusReady
	v.Landed = uint64(len(sorted))
	if err := mark(ctx, v, now); err != nil {
		return landing{}, err
	}
	return landing{Version: ver, Keys: len(sorted), Wrote: wrote, Resumed: resumed}, nil
}

// insert builds one chunk's statement and its bound arguments. PURE, so the one
// place a value could become part of a statement instead of a parameter is
// testable without a warehouse: the only interpolation is the repeated
// placeholder tuple, generated from the chunk length, and it carries no input.
func insert(set, source, ver string, entries []Entry, at time.Time) (string, []any) {
	if len(entries) == 0 {
		return "", nil
	}
	const cols = "(set, source, version, key, value, score, orgs, n, at)"
	tuples := make([]string, 0, len(entries))
	args := make([]any, 0, len(entries)*9)
	for _, e := range entries {
		tuples = append(tuples, "(?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args, set, source, ver, e.Key, e.Value, e.Score, e.Orgs, e.N, at.UTC())
	}
	return "INSERT INTO " + entryTable + " " + cols + " VALUES " + strings.Join(tuples, ", "), args
}

// land writes one chunk.
func land(ctx context.Context, set, source, ver string, entries []Entry, at time.Time) error {
	stmt, args := insert(set, source, ver, entries, at)
	if stmt == "" {
		return nil
	}
	if err := storeExec(ctx, stmt, args...); err != nil {
		return fmt.Errorf("reference: land %s/%s: %w", set, source, err)
	}
	return nil
}

const readStatement = `SELECT key, value, score, orgs, n
FROM ` + entryTable + ` FINAL
WHERE set = ? AND source = ? AND version = ?
ORDER BY key
LIMIT ` + maxReadLiteral

// maxReadLiteral is maxRead as the literal the statement carries. A LIMIT cannot
// bind, so it is spelled once here and nothing a caller sends reaches it.
const maxReadLiteral = "200000"

// read materialises one version's entries.
func read(ctx context.Context, set, source, ver string) ([]Entry, error) {
	rows, err := storeQuery(ctx, readStatement, set, source, ver)
	if err != nil {
		return nil, fmt.Errorf("reference: read %s/%s: %w", set, source, err)
	}
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, Entry{
			Key:   text(r["key"]),
			Value: pairs(r["value"]),
			Score: number(r["score"]),
			Orgs:  count32(r["orgs"]),
			N:     count64(r["n"]),
		})
	}
	return out, nil
}

const pruneStatement = `ALTER TABLE ` + entryTable +
	` DELETE WHERE set = ? AND source = ? AND version != ? AND version != ?`

// prune drops the membership of every version but the two named.
//
// Non-fatal by construction. A failed prune leaves history, which costs storage
// and nothing else; treating it as an ingest failure would turn a housekeeping
// problem into a freshness one.
func prune(ctx context.Context, set, source, keep, alsoKeep string) error {
	return storeExec(ctx, pruneStatement, set, source, keep, alsoKeep)
}

// dropper is prune as a value, so what a take supersedes can be decided and
// tested without a warehouse to delete from.
type dropper func(ctx context.Context, set, source, keep, alsoKeep string) error

// sweepOld drops the membership of every version of every source but the CURRENT
// one and the one it REPLACED.
//
// The previous version is kept deliberately, and `was` is why this function
// exists rather than a loop at the call site: the current snapshot knows what is
// current now and cannot know what was current a moment ago, so the pair has to
// be carried in. A call site that passed the current version for both spared
// exactly one version — the statement's two placeholders bound to the same
// string — and quietly deleted the rows behind every citation taken in the
// window before a refresh, which is the audit answer this whole plane is sold on.
//
// A source taken for the first time has no previous version; `was` carries no
// entry for it, `alsoKeep` is the empty string, and a version that is not the
// empty string spares nothing extra — which is correct, there is nothing extra.
func sweepOld(ctx context.Context, drop dropper, set string, was map[string]held, now []version) error {
	var first error
	for _, v := range now {
		if err := drop(ctx, set, v.Source, v.Version, was[v.Source].Version); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// held is what this plane holds for one source at one instant: the version and
// how many members it carries. It is captured BEFORE a take so the take can be
// compared with what it replaces — the previous version is what prune spares
// (sweepOld) and the previous size is what the swing gate measures against
// (swung).
type held struct {
	Version string
	Keys    uint64
}

// holding reads what a snapshot currently holds, per source.
func holding(s *snap) map[string]held {
	out := map[string]held{}
	if s == nil {
		return out
	}
	for _, v := range s.took {
		out[v.Source] = held{Version: v.Version, Keys: v.Keys}
	}
	return out
}

// swing is how far a source's size may move in ONE take before the take is
// refused and the previous version is left standing.
//
// The empty case was already an error and the truncation case already an error,
// and between them sat the dangerous one: a publisher serving a valid, parseable
// list at a tenth or ten times its previous size. Both directions are refused
// because both are silent. A list that shrank answers "not listed" for everything
// it lost and reads exactly like a clean world; a list that exploded puts members
// nobody vetted into the baseline every organisation's decisions read.
//
// Four rather than two: published lists do move, and a bound that fires on
// ordinary growth is a bound an operator learns to force through.
const swing = 4

// swung reports whether a take moved a source's size past [swing], in either
// direction. A source taken for the first time has nothing to compare against and
// is never refused.
func swung(was, now uint64) bool {
	if was == 0 {
		return false
	}
	return now*swing < was || now > was*swing
}

// order sorts entries by key. It is the canonical order the digest is taken over
// and the order chunks land in, so a resumed run and a fresh run agree on which
// entry is the nth.
func order(entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// keys returns a value map's keys in sorted order, so the digest of an entry
// does not depend on Go's map iteration.
func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// text, when, number and pairs read a warehouse column into the type this
// package wants, tolerating whichever concrete type the driver chose.
func text(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case *string:
		if s == nil {
			return ""
		}
		return *s
	default:
		return ""
	}
}

func when(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t.UTC()
	case *time.Time:
		if t == nil {
			return time.Time{}
		}
		return t.UTC()
	default:
		return time.Time{}
	}
}

func number(v any) float64 {
	switch f := v.(type) {
	case float64:
		return f
	case float32:
		return float64(f)
	default:
		return float64(count64(v))
	}
}

func pairs(v any) map[string]string {
	switch m := v.(type) {
	case map[string]string:
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = text(val)
		}
		return out
	default:
		return map[string]string{}
	}
}
