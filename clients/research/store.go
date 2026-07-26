package research

// store.go is the durable WRITE PLANE of Hanzo Research (HIP-0512 §"Two planes,
// named"): one per-org (per HIP-0302, physical-file-isolated) SQLite holding the
// research evidence records. This is the local source of truth; the Datastore
// projection (datastore.go) is a best-effort roll-up over it.
//
// RECORDS, NOT COLUMNS. Every record is a typed Go value stored as JSON in
// hanzoai/orm's fixed `_entities` table (id/kind/data/…), NOT a row in a
// hand-migrated per-field table. Adding a field to a record = adding a struct
// field, with ZERO schema change — so the "no such column" DDL-drift class (a
// CREATE INDEX referencing a column an older CREATE TABLE lacked, the outage) is
// STRUCTURALLY IMPOSSIBLE here: there are no per-field columns and no DDL on schema
// evolution. orm layers over the *sql.DB cloud.OrgDB already opened (cek-encrypted,
// single-writer, WAL) — orm manages the records, the caller owns the file — so
// encryption at rest and the HA file-durability plane are preserved.
//
// VERSIONED, NOT WRITE-ONCE. Every completed run is a versioned record. A
// correction does not mutate the prior record — it APPENDS a new version under a
// new content identity; the prior version is RETAINED (superseded), never deleted.
// Two orthogonal axes travel on every record:
//   - revision ∈ {original, corrected, retracted} — the producer-declared kind of
//     this version. `superseded` and `canonical` are DERIVED (a version is
//     canonical iff it is the latest-APPENDED non-retracted version of its stable
//     id; any earlier non-retracted version is superseded).
//   - status   ∈ {planning, running, complete, faulted, …} — the run's execution
//     state. faulted/failed runs are RETAINED — negative results are evidence.
//
// Version identity is content_hash (server-computed over the measurement AND its
// provenance — git sha/branch/dirty, lib versions): re-ingesting byte-identical
// content is a no-op (idempotent backfill), while a corrected number OR a run on a
// new commit / lib version is new content → a new RETAINED version. The dedup is
// orm.CreateIfAbsent keyed by the stable id <project>·<experiment-id>·<content_hash>
// (first-writer-wins), so a re-ingest never duplicates and a correction always
// retains. RETAINED is the full history (the truth); CANONICAL is the deterministic
// deduped view over it, so dedup never reads as loss.
//
// SEQ IS THE SUPERSESSION AUTHORITY (N2), NOT client ts. Each newly-appended
// version is stamped with a per-store monotone seq assigned INSIDE the ingest
// transaction (never a client value), so canonical = latest-APPEND-wins across
// every producer and un-forgeable by a client. A backfill importer stamps a
// wall-clock ts (~1.7e9) while an SDK live record sends none (ts=0); ordering by ts
// would sort an SDK correction/retraction BEFORE a backfilled experiment and it
// would never supersede. seq is assigned on append, so latest-append-wins is
// correct. ts is retained as display-only (measured-at).
//
// PROVENANCE is first-class + queryable (a field on the record, not buried in
// meta): project, git_sha / git_branch / git_dirty, and lib_versions.
//
// VISIBILITY + CONSENT are private-by-default and SEPARATE from upload: ingest
// always records visibility='private', trainable=false, publishable=false — public
// board visibility, training rights, and commons publication are each a distinct
// authorized grant (setGrant), never implied by uploading a run.
//
// Per-org stores are small, so the derived views (canonical, counts, aggregates)
// load a kind's records and fold them in memory — orm's JSON model has no per-field
// SQL index. If a kind ever grows large enough that a full scan hurts, the
// escalation is an orm-indexed filter for that kind.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
)

// The orm kinds this plane stores. The "research-" prefix keeps them disjoint from
// every other subsystem's kinds sharing the process-wide orm registry.
const (
	kindExp     = "research-experiment"
	kindAtt     = "research-attempt"
	kindArt     = "research-artifact"      // artifact metadata (small; the diary feed reads these)
	kindArtBlob = "research-artifact-blob" // artifact bytes, split out so listing never loads blobs
	kindMeta    = "research-meta"          // the per-store append clock
	seqID       = "seq"                    // the meta record's id
)

// init registers the record types with orm. Registration declares each kind to the
// one process-wide orm registry; the store drives reads/writes through the
// low-level orm.DB (explicit keys + Put/Get/Query/CreateIfAbsent).
func init() {
	orm.Register[expRecord](kindExp)
	orm.Register[attRecord](kindAtt)
	orm.Register[artRecord](kindArt)
	orm.Register[artBlob](kindArtBlob)
	orm.Register[metaRecord](kindMeta)
}

// ── record types (typed structs stored as JSON — adding a field needs NO DDL) ──

// expRecord is one versioned experiment run. content_hash is its version identity;
// seq is the server's append clock (canonical picks the latest). Provenance
// (git_*, lib_versions) is first-class. Canonical is DERIVED on read, so it is not
// stored; Endpoint is the SSRF-gated measurement arm, not evidence, so it is not
// stored either.
type expRecord struct {
	Project     string          `json:"project"`
	ID          string          `json:"id"`
	ContentHash string          `json:"content_hash"`
	Seq         int64           `json:"seq"`
	Revision    string          `json:"revision"`
	Status      string          `json:"status"`
	Visibility  string          `json:"visibility"`
	Trainable   bool            `json:"trainable"`
	Publishable bool            `json:"publishable"`
	Kind        string          `json:"kind"`
	Subject     string          `json:"subject"`
	Task        string          `json:"task"`
	Metric      string          `json:"metric"`
	Value       float64         `json:"value"`
	N           int             `json:"n"`
	NTotal      int             `json:"n_total"`
	CostUSD     float64         `json:"cost_usd"`
	Meta        json.RawMessage `json:"meta,omitempty"`
	GitSHA      string          `json:"git_sha"`
	GitBranch   string          `json:"git_branch"`
	GitDirty    bool            `json:"git_dirty"`
	LibVersions json.RawMessage `json:"lib_versions,omitempty"`
	TS          int64           `json:"ts"`
}

// attRecord is one versioned measured attempt on one item, keyed by (project,
// benchmark, item, model). A re-scored/re-run attempt is a new version; the prior
// is retained. response is the raw artifact (may carry personal/licensed material).
type attRecord struct {
	Project     string `json:"project"`
	Benchmark   string `json:"benchmark"`
	Item        string `json:"item"`
	Model       string `json:"model"`
	ContentHash string `json:"content_hash"`
	Seq         int64  `json:"seq"`
	Revision    string `json:"revision"`
	Status      string `json:"status"`
	Gold        string `json:"gold"`
	Answer      string `json:"answer"`
	Correct     bool   `json:"correct"`
	Response    string `json:"response"`
	Source      string `json:"source"`
	TS          int64  `json:"ts"`
}

// artRecord is one diary artifact's metadata, keyed by its server-derived sha256.
// The bytes live in a separate artBlob under a distinct id so the diary feed lists
// metadata without loading blobs.
type artRecord struct {
	SHA256         string          `json:"sha256"`
	Kind           string          `json:"kind"`
	Ref            string          `json:"ref"`
	RunID          string          `json:"run_id"`
	Project        string          `json:"project"`
	Visibility     string          `json:"visibility"`
	RetentionClass string          `json:"retention_class"`
	GitSHA         string          `json:"git_sha"`
	GitBranch      string          `json:"git_branch"`
	GitDirty       bool            `json:"git_dirty"`
	LibVersions    json.RawMessage `json:"lib_versions,omitempty"`
	TS             int64           `json:"ts"`
}

// artBlob is one artifact's stored bytes, hash-addressed by the same sha256 as its
// artRecord (under a distinct id keyspace). []byte serializes as base64 in the JSON.
type artBlob struct {
	Content []byte `json:"content"`
}

// metaRecord backs two singleton kindMeta records, keyed by distinct ids so they
// never interfere: id "seq" is the per-store monotone append clock (advanced inside
// the ingest tx once per newly-appended version, so canonical = latest-append-wins
// is server-authoritative and un-forgeable); id "legacy-migrated" is the one-time
// migration marker (its existence is the signal; the Seq value is unused there).
type metaRecord struct {
	Seq int64 `json:"seq"`
}

// ── store (orm over the caller-owned per-org *sql.DB) ─────────────────────────

// store is one org's research plane. db is orm's typed-record layer; raw is the
// *sql.DB cloud.OrgDB opened (cek-encrypted, single-writer, WAL) — orm borrows it,
// the store owns its Close (the OrgStore contract).
type store struct {
	db  orm.DB
	raw *sql.DB
}

// openStore layers orm's record model over the already-opened per-org *sql.DB. The
// seam signature is unchanged (cloud.OrgStore hands a pragma'd, cek-encrypted
// connection); orm's initSchema creates its `_entities` table if absent. It then
// carries any pre-orm raw-SQL evidence forward (migrateLegacy) so an existing store
// is never silently read as empty. A migration failure FAILS the open (fail-secure)
// rather than serving an empty board over real data.
func openStore(raw *sql.DB) (*store, error) {
	db, err := orm.AdaptSQLite(raw)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("research open store: %w", err)
	}
	s := &store{db: db, raw: raw}
	if err := s.migrateLegacy(context.Background()); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("research legacy migration: %w", err)
	}
	return s, nil
}

// Close closes the underlying connection the OrgStore opened. orm borrows the
// connection, so closing the store closes the file exactly once here.
func (s *store) Close() error { return s.raw.Close() }

// ── legacy migration (pre-orm raw-SQL tables → _entities) ─────────────────────

// legacyArt pairs an artifact's migrated metadata with its stored bytes.
type legacyArt struct {
	meta    artRecord
	content []byte
}

// migrateLegacy carries a store's pre-orm evidence — the raw-SQL experiment /
// attempt / artifact tables the original store wrote in this same per-org file —
// forward into orm's _entities, ONCE, on open. Without it a store that predates the
// orm cutover reads as empty (the tables still hold the rows, but the orm reads look
// only at _entities), silently vanishing every logged run.
//
// It runs ONCE: a completed migration leaves a marker (a kindMeta record separate
// from the seq clock), so every later open short-circuits without re-scanning the
// legacy tables. It is also idempotent even without the marker — every row is
// CreateIfAbsent'd under the SAME stable id the ORM would mint, so a re-run never
// duplicates. Each version keeps its OLD seq value, so canonical/supersession is
// preserved EXACTLY — a corrected run stays canonical — and the append clock is
// advanced past every migrated seq so a later ingest supersedes correctly.
// content_hash, revision, status, visibility/consent, all provenance, and the
// artifact blobs are carried verbatim. Rows are read by COLUMN NAME (SELECT *), so a
// table left by ANY older schema — even the outage-era one missing provenance
// columns — migrates without referencing a column that may not exist: the migration
// itself can never hit "no such column". A greenfield store (no legacy tables) marks
// done immediately. A failure fails the open (fail-secure, via openStore).
func (s *store) migrateLegacy(ctx context.Context) error {
	done, err := s.legacyMigrated(ctx)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	present, err := s.legacyTables(ctx)
	if err != nil {
		return err
	}
	if len(present) == 0 {
		return s.markLegacyMigrated(ctx) // greenfield: nothing to carry
	}
	exps, atts, arts, err := s.readLegacy(ctx, present)
	if err != nil {
		return err
	}
	return s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		maxSeq, err := loadSeq(ctx, tx)
		if err != nil {
			return err
		}
		for i := range exps {
			r := exps[i]
			if _, err := tx.CreateIfAbsent(ctx, tx.NewKey(kindExp, expKeyID(r.Project, r.ID, r.ContentHash), 0, nil), &r); err != nil {
				return err
			}
			if r.Seq > maxSeq {
				maxSeq = r.Seq
			}
		}
		for i := range atts {
			r := atts[i]
			if _, err := tx.CreateIfAbsent(ctx, tx.NewKey(kindAtt, attKeyID(r.Project, r.Benchmark, r.Item, r.Model, r.ContentHash), 0, nil), &r); err != nil {
				return err
			}
			if r.Seq > maxSeq {
				maxSeq = r.Seq
			}
		}
		for i := range arts {
			m := arts[i].meta
			made, err := tx.CreateIfAbsent(ctx, tx.NewKey(kindArt, artKeyID(m.SHA256), 0, nil), &m)
			if err != nil {
				return err
			}
			if made {
				if _, err := tx.CreateIfAbsent(ctx, tx.NewKey(kindArtBlob, artBlobKeyID(m.SHA256), 0, nil), &artBlob{Content: arts[i].content}); err != nil {
					return err
				}
			}
		}
		// Never lower the clock: max(current, every migrated seq). A re-open where
		// later ingests already advanced it leaves it untouched.
		if err := putSeq(ctx, tx, maxSeq); err != nil {
			return err
		}
		// Mark done in the SAME tx: the marker commits iff the migration commits.
		return markLegacyMigratedTx(ctx, tx)
	})
}

// legacyMark is the id (in kindMeta, distinct from the seq clock's "seq") of the
// record whose existence records that the one-time legacy migration has completed.
const legacyMark = "legacy-migrated"

// legacyMigrated reports whether the one-time legacy migration has already run.
func (s *store) legacyMigrated(ctx context.Context) (bool, error) {
	var m metaRecord
	switch err := s.db.Get(ctx, s.db.NewKey(kindMeta, legacyMark, 0, nil), &m); {
	case err == nil:
		return true, nil
	case errors.Is(err, ormdb.ErrNoSuchEntity):
		return false, nil
	default:
		return false, err
	}
}

// markLegacyMigrated writes the completion marker (greenfield path, no tx).
func (s *store) markLegacyMigrated(ctx context.Context) error {
	_, err := s.db.Put(ctx, s.db.NewKey(kindMeta, legacyMark, 0, nil), &metaRecord{})
	return err
}

// markLegacyMigratedTx writes the completion marker inside the migration tx.
func markLegacyMigratedTx(ctx context.Context, tx orm.DB) error {
	_, err := tx.Put(ctx, tx.NewKey(kindMeta, legacyMark, 0, nil), &metaRecord{})
	return err
}

// legacyTables reports which pre-orm tables are present in the file.
func (s *store) legacyTables(ctx context.Context) (map[string]bool, error) {
	rows, err := s.raw.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name IN ('experiment','attempt','artifact')`)
	if err != nil {
		return nil, fmt.Errorf("research legacy detect: %w", err)
	}
	defer func() { _ = rows.Close() }()
	present := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		present[n] = true
	}
	return present, rows.Err()
}

// readLegacy loads every row of the present pre-orm tables into the orm record
// shapes. Reads run BEFORE the migration transaction (each fully drains + closes its
// rows) so the single writer connection is free when the tx opens.
func (s *store) readLegacy(ctx context.Context, present map[string]bool) ([]expRecord, []attRecord, []legacyArt, error) {
	var exps []expRecord
	var atts []attRecord
	var arts []legacyArt
	if present["experiment"] {
		ms, err := s.scanLegacy(ctx, `SELECT * FROM experiment ORDER BY seq`)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("research legacy read experiment: %w", err)
		}
		for _, m := range ms {
			exps = append(exps, expRecord{
				Project: lstr(m, "project", "default"), ID: lstr(m, "id", ""),
				ContentHash: lstr(m, "content_hash", ""), Seq: lint(m, "seq"),
				Revision: revisionOf(lstr(m, "revision", "original")), Status: runStatus(lstr(m, "status", "complete")),
				Visibility: lstr(m, "visibility", "private"), Trainable: lbool(m, "trainable"), Publishable: lbool(m, "publishable"),
				Kind: lstr(m, "kind", ""), Subject: lstr(m, "subject", ""), Task: lstr(m, "task", ""), Metric: lstr(m, "metric", ""),
				Value: lfloat(m, "value"), N: int(lint(m, "n")), NTotal: int(lint(m, "n_total")), CostUSD: lfloat(m, "cost_usd"),
				Meta: ljson(m, "meta"), GitSHA: lstr(m, "git_sha", ""), GitBranch: lstr(m, "git_branch", ""),
				GitDirty: lbool(m, "git_dirty"), LibVersions: ljson(m, "lib_versions"), TS: lint(m, "ts"),
			})
		}
	}
	if present["attempt"] {
		ms, err := s.scanLegacy(ctx, `SELECT * FROM attempt ORDER BY seq`)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("research legacy read attempt: %w", err)
		}
		for _, m := range ms {
			atts = append(atts, attRecord{
				Project: lstr(m, "project", "default"), Benchmark: lstr(m, "benchmark", ""),
				Item: lstr(m, "item", ""), Model: lstr(m, "model", ""), ContentHash: lstr(m, "content_hash", ""),
				Seq: lint(m, "seq"), Revision: revisionOf(lstr(m, "revision", "original")), Status: runStatus(lstr(m, "status", "complete")),
				Gold: lstr(m, "gold", ""), Answer: lstr(m, "answer", ""), Correct: lbool(m, "correct"),
				Response: lstr(m, "response", ""), Source: sourceOf(lstr(m, "source", "")), TS: lint(m, "ts"),
			})
		}
	}
	if present["artifact"] {
		ms, err := s.scanLegacy(ctx, `SELECT * FROM artifact`)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("research legacy read artifact: %w", err)
		}
		for _, m := range ms {
			sha := lstr(m, "sha256", "")
			if sha == "" {
				continue
			}
			var content []byte
			if b, ok := m["content"].([]byte); ok {
				content = append([]byte(nil), b...) // copy: the scan buffer is reused
			}
			arts = append(arts, legacyArt{
				meta: artRecord{
					SHA256: sha, Kind: lstr(m, "kind", ""), Ref: lstr(m, "ref", ""),
					RunID: lstr(m, "run_id", ""), Project: lstr(m, "project", "default"),
					Visibility: lstr(m, "visibility", "private"), RetentionClass: lstr(m, "retention_class", "raw-artifact"),
					GitSHA: lstr(m, "git_sha", ""), GitBranch: lstr(m, "git_branch", ""),
					GitDirty: lbool(m, "git_dirty"), LibVersions: ljson(m, "lib_versions"), TS: lint(m, "ts"),
				},
				content: content,
			})
		}
	}
	return exps, atts, arts, nil
}

// scanLegacy runs a raw query and returns every row as a column-name→value map, so
// the caller reads by name and a column absent in an older schema simply yields a
// default (never a "no such column" on the query).
func (s *store) scanLegacy(ctx context.Context, query string) ([]map[string]any, error) {
	rows, err := s.raw.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// legacy value extractors: SQLite hands back int64 / float64 / string / []byte / nil,
// so each coerces the driver's dynamic type and defaults an absent (older-schema) or
// NULL column to the old table's column default.
func lstr(m map[string]any, k, def string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return def
	}
}

func lint(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func lbool(m map[string]any, k string) bool { return lint(m, k) != 0 }

func lfloat(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func ljson(m map[string]any, k string) json.RawMessage {
	return json.RawMessage(jsonObj(json.RawMessage(lstr(m, k, ""))))
}

// ── content hashes (version identity) — UNCHANGED from the raw-SQL store ───────

// hashExp is an experiment version's content identity: the REVISION, the
// substantive measurement, AND its PROVENANCE (git sha/branch/dirty, lib versions).
// revision is part of the identity so a RETRACTION of an otherwise identical run is
// a DISTINCT version (not a CreateIfAbsent no-op). Provenance is part of the
// identity so the SAME number on a different commit or lib version is a distinct
// RETAINED version. Meta and lib_versions are canonicalized (sorted-key,
// whitespace-free) so a re-serialization does not mint a spurious version.
// visibility/ts are NOT identity (a grant / a re-observation is not a new version).
func hashExp(e Experiment) string {
	return hashJoin(revisionOf(e.Revision), e.Kind, e.Subject, e.Task, e.Metric,
		strconv.FormatFloat(e.Value, 'g', -1, 64), strconv.Itoa(e.N), strconv.Itoa(e.NTotal),
		strconv.FormatFloat(e.CostUSD, 'g', -1, 64), runStatus(e.Status), canonJSON(e.Meta),
		e.GitSHA, e.GitBranch, strconv.Itoa(boolInt(e.GitDirty)), canonJSON(e.LibVersions))
}

// hashAtt is an attempt version's content identity: the revision + the measured
// artifact (gold, answer, correctness, raw response, source, status). A
// re-scored/re-run/retracted attempt is a new version; a byte-identical re-ingest
// is idempotent.
func hashAtt(a Attempt) string {
	return hashJoin(revisionOf(a.Revision), a.Gold, a.Answer, strconv.Itoa(boolInt(a.Correct)),
		a.Response, sourceOf(a.Source), runStatus(a.Status))
}

func hashJoin(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// orm's `_entities` keys entities by a GLOBALLY-unique id (its PRIMARY KEY spans
// every kind), so each kind carries a short id prefix to stay in its own keyspace —
// most importantly the artifact metadata (artKeyID) and its blob (artBlobKeyID),
// which share one sha256. For a run version, content_hash alone is not unique (it
// excludes project + stable id, so two different runs with identical measurements
// collide), so the id folds project + stable id + content hash through hashJoin
// (NUL-delimited, so a ':' inside a project/id segment can never make two distinct
// tuples collide).
func expKeyID(project, id, contentHash string) string {
	return "e:" + hashJoin(project, id, contentHash)
}

func attKeyID(project, benchmark, item, model, contentHash string) string {
	return "a:" + hashJoin(project, benchmark, item, model, contentHash)
}

func artKeyID(sha256Hex string) string     { return "r:" + sha256Hex }
func artBlobKeyID(sha256Hex string) string { return "b:" + sha256Hex }

// ── ingest (append versions, idempotent by content, seq-stamped) ──────────────

// ingest appends one batch — experiments and attempts under project — in a single
// transaction. project is the SERVER's value (principal.Project), stamped here; a
// payload cannot carry it. Visibility/consent are FORCED private/withheld — upload
// never grants them. Each NEW version is stamped with the next per-store seq
// (assigned here, inside the tx, never from the client) so canonical =
// latest-append-wins. A re-ingest of identical content adds 0 (CreateIfAbsent
// first-writer-wins). Returns how many NEW versions each kind gained.
func (s *store) ingest(ctx context.Context, project string, exps []Experiment, atts []Attempt) (expAdded, attAdded int, err error) {
	err = s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		// Reset on every attempt so a retried transaction never double-counts.
		expAdded, attAdded = 0, 0
		seq, e := loadSeq(ctx, tx)
		if e != nil {
			return e
		}
		for i := range exps {
			ex := exps[i]
			ch := hashExp(ex)
			next := seq + 1
			rec := expRecord{
				Project: project, ID: ex.ID, ContentHash: ch, Seq: next,
				Revision: revisionOf(ex.Revision), Status: runStatus(ex.Status),
				Visibility: "private", Trainable: false, Publishable: false,
				Kind: ex.Kind, Subject: ex.Subject, Task: ex.Task, Metric: ex.Metric,
				Value: ex.Value, N: ex.N, NTotal: ex.NTotal, CostUSD: ex.CostUSD,
				Meta: json.RawMessage(jsonObj(ex.Meta)), GitSHA: ex.GitSHA, GitBranch: ex.GitBranch,
				GitDirty: ex.GitDirty, LibVersions: json.RawMessage(jsonObj(ex.LibVersions)), TS: ex.TS,
			}
			created, e := tx.CreateIfAbsent(ctx, tx.NewKey(kindExp, expKeyID(project, ex.ID, ch), 0, nil), &rec)
			if e != nil {
				return fmt.Errorf("experiment insert: %w", e)
			}
			if created {
				seq = next
				expAdded++
			}
		}
		for i := range atts {
			a := atts[i]
			ch := hashAtt(a)
			next := seq + 1
			rec := attRecord{
				Project: project, Benchmark: a.Benchmark, Item: a.Item, Model: a.Model,
				ContentHash: ch, Seq: next, Revision: revisionOf(a.Revision), Status: runStatus(a.Status),
				Gold: a.Gold, Answer: a.Answer, Correct: a.Correct, Response: a.Response,
				Source: sourceOf(a.Source), TS: a.TS,
			}
			created, e := tx.CreateIfAbsent(ctx, tx.NewKey(kindAtt, attKeyID(project, a.Benchmark, a.Item, a.Model, ch), 0, nil), &rec)
			if e != nil {
				return fmt.Errorf("attempt insert: %w", e)
			}
			if created {
				seq = next
				attAdded++
			}
		}
		return putSeq(ctx, tx, seq)
	})
	if err != nil {
		return 0, 0, fmt.Errorf("research ingest: %w", err)
	}
	return expAdded, attAdded, nil
}

// loadSeq reads the per-store append clock; an absent clock (fresh store) is 0.
func loadSeq(ctx context.Context, tx orm.DB) (int64, error) {
	var m metaRecord
	err := tx.Get(ctx, tx.NewKey(kindMeta, seqID, 0, nil), &m)
	if err != nil {
		if errors.Is(err, ormdb.ErrNoSuchEntity) {
			return 0, nil
		}
		return 0, err
	}
	return m.Seq, nil
}

// putSeq persists the advanced append clock inside the ingest transaction.
func putSeq(ctx context.Context, tx orm.DB, seq int64) error {
	_, err := tx.Put(ctx, tx.NewKey(kindMeta, seqID, 0, nil), &metaRecord{Seq: seq})
	return err
}

// ── loaders + canonical folding (the ONE definition of "canonical") ───────────

// A version is canonical iff it is THE single latest-APPENDED version (max seq) of
// its stable id AND that latest version is not retracted. If the latest is a
// retraction, the id has NO canonical — WITHDRAWN (a retraction is honored, not
// ignored). Every version is retained regardless.
//
// SCALE NOTE: allExp/allAtt/allArt load a whole kind and fold it in memory (O(N) per
// request in the org's own history), which is right while a per-org corpus is small
// but degrades an org's own board at large history (org-bounded, never cross-tenant).
// The escalation, when a kind grows past comfortable in-memory folding, is a
// per-field orm-indexed Query.Filter (e.g. by project) plus a stored canonical flag
// maintained on write — not a return to hand-written per-column SQL.

func (s *store) allExp(ctx context.Context) ([]expRecord, error) {
	var recs []expRecord
	if _, err := s.db.Query(kindExp).GetAll(ctx, &recs); err != nil {
		return nil, fmt.Errorf("research load experiments: %w", err)
	}
	return recs, nil
}

func (s *store) allAtt(ctx context.Context) ([]attRecord, error) {
	var recs []attRecord
	if _, err := s.db.Query(kindAtt).GetAll(ctx, &recs); err != nil {
		return nil, fmt.Errorf("research load attempts: %w", err)
	}
	return recs, nil
}

func (s *store) allArt(ctx context.Context) ([]artRecord, error) {
	var recs []artRecord
	if _, err := s.db.Query(kindArt).GetAll(ctx, &recs); err != nil {
		return nil, fmt.Errorf("research load artifacts: %w", err)
	}
	return recs, nil
}

// canonicalExp folds experiment versions to the canonical version per stable id
// (project·id): the max-seq version, dropped if that latest version is retracted
// (withdrawn). The NUL separator can never collide two distinct (project, id) pairs.
func canonicalExp(recs []expRecord) map[string]expRecord {
	latest := map[string]expRecord{}
	for _, r := range recs {
		k := r.Project + "\x00" + r.ID
		if cur, ok := latest[k]; !ok || r.Seq > cur.Seq {
			latest[k] = r
		}
	}
	for k, r := range latest {
		if r.Revision == "retracted" {
			delete(latest, k)
		}
	}
	return latest
}

// canonicalAtt folds attempt versions to the canonical version per stable id
// (project·benchmark·item·model), same latest-append-wins / withdraw-on-retraction
// rule as canonicalExp.
func canonicalAtt(recs []attRecord) map[string]attRecord {
	latest := map[string]attRecord{}
	for _, r := range recs {
		k := r.Project + "\x00" + r.Benchmark + "\x00" + r.Item + "\x00" + r.Model
		if cur, ok := latest[k]; !ok || r.Seq > cur.Seq {
			latest[k] = r
		}
	}
	for k, r := range latest {
		if r.Revision == "retracted" {
			delete(latest, k)
		}
	}
	return latest
}

// answered reports whether a canonical version counts as an ANSWERED result — a
// completed status, not a fault. faulted/failed runs are RETAINED (counted in
// *_retained) but are not the answered headline. Mirrors the raw-SQL
// `status NOT IN ('faulted','failed')`.
func answered(status string) bool {
	return status != "faulted" && status != "failed"
}

// ── counts (retained = truth; canonical = deduped answered view) ──────────────

// Counts carries both axes for both kinds. retained is the full versioned history;
// canonical is the distinct-stable-id answered deduped view. retained ≥ canonical
// always, and the gap is superseded/faulted versions — never lost data.
type Counts struct {
	ExperimentsRetained  int `json:"experiments_retained"`
	ExperimentsCanonical int `json:"canonical_experiments"`
	AttemptsRetained     int `json:"attempts_retained"`
	AttemptsCanonical    int `json:"canonical_attempts"`
}

func (s *store) counts(ctx context.Context) (Counts, error) {
	exps, err := s.allExp(ctx)
	if err != nil {
		return Counts{}, err
	}
	atts, err := s.allAtt(ctx)
	if err != nil {
		return Counts{}, err
	}
	var c Counts
	c.ExperimentsRetained = len(exps)
	for _, r := range canonicalExp(exps) {
		if answered(r.Status) {
			c.ExperimentsCanonical++
		}
	}
	c.AttemptsRetained = len(atts)
	for _, r := range canonicalAtt(atts) {
		if answered(r.Status) {
			c.AttemptsCanonical++
		}
	}
	return c, nil
}

// listExperiments returns the CANONICAL experiments (latest non-retracted version
// per stable id) — whatever their status, so the board can render faults — with
// provenance, filtered by project ("" = every project in the org) and optionally
// kind, ordered deterministically by (project, id).
func (s *store) listExperiments(ctx context.Context, project, kind string) ([]Experiment, error) {
	exps, err := s.allExp(ctx)
	if err != nil {
		return nil, err
	}
	var out []Experiment
	for _, r := range canonicalExp(exps) {
		if project != "" && r.Project != project {
			continue
		}
		if kind != "" && r.Kind != kind {
			continue
		}
		out = append(out, r.toExperiment(true))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (r expRecord) toExperiment(canonical bool) Experiment {
	return Experiment{
		Project: r.Project, ID: r.ID, Revision: r.Revision, Status: r.Status,
		Canonical: canonical, Visibility: r.Visibility, Trainable: r.Trainable, Publishable: r.Publishable,
		Kind: r.Kind, Subject: r.Subject, Task: r.Task, Metric: r.Metric, Value: r.Value,
		N: r.N, NTotal: r.NTotal, CostUSD: r.CostUSD, Meta: r.Meta,
		GitSHA: r.GitSHA, GitBranch: r.GitBranch, GitDirty: r.GitDirty, LibVersions: r.LibVersions, TS: r.TS,
	}
}

// ── grants (visibility + consent — SEPARATE from upload) ──────────────────────

// setGrant records an authorized visibility/consent decision for a stable id's
// records — the SEPARATE authorization upload never implies. It updates EVERY
// retained version of the id (the decision is about the run, not one version). The
// load + updates run inside one transaction so a concurrent append cannot slip a
// new version past the grant. A nil pointer leaves that field unchanged. Returns
// versions touched. visibility/consent are NOT part of content identity, so an
// in-place update keeps each version's id (a Put, not a new version).
func (s *store) setGrant(ctx context.Context, project, id string, visibility *string, trainable, publishable *bool) (int, error) {
	if visibility == nil && trainable == nil && publishable == nil {
		return 0, nil
	}
	n := 0
	err := s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		n = 0
		var recs []expRecord
		if _, e := tx.Query(kindExp).GetAll(ctx, &recs); e != nil {
			return e
		}
		for i := range recs {
			r := recs[i]
			if r.Project != project || r.ID != id {
				continue
			}
			if visibility != nil {
				r.Visibility = *visibility
			}
			if trainable != nil {
				r.Trainable = *trainable
			}
			if publishable != nil {
				r.Publishable = *publishable
			}
			if _, e := tx.Put(ctx, tx.NewKey(kindExp, expKeyID(r.Project, r.ID, r.ContentHash), 0, nil), &r); e != nil {
				return e
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("research grant: %w", err)
	}
	return n, nil
}

// ── artifacts (the research diary — raw-artifact retention class) ─────────────

// putArtifact records one artifact idempotently by its server-derived sha256. The
// metadata and the bytes are written together in one transaction (so a metadata row
// can never outlive a missing blob), under distinct id keyspaces so listing the
// diary never loads blobs. project is the SERVER's value; visibility is FORCED
// private (a grant is separate). Returns 1 when a new artifact was recorded, 0 on a
// dup (same bytes → same hash → a no-op).
func (s *store) putArtifact(ctx context.Context, project string, a Artifact, content []byte) (int, error) {
	created := 0
	err := s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		created = 0
		rec := artRecord{
			SHA256: a.SHA256, Kind: a.Kind, Ref: a.Ref, RunID: a.RunID, Project: project,
			Visibility: "private", RetentionClass: "raw-artifact",
			GitSHA: a.GitSHA, GitBranch: a.GitBranch, GitDirty: a.GitDirty,
			LibVersions: json.RawMessage(jsonObj(a.LibVersions)), TS: a.TS,
		}
		madeMeta, e := tx.CreateIfAbsent(ctx, tx.NewKey(kindArt, artKeyID(a.SHA256), 0, nil), &rec)
		if e != nil {
			return e
		}
		if madeMeta {
			created = 1
			if _, e := tx.CreateIfAbsent(ctx, tx.NewKey(kindArtBlob, artBlobKeyID(a.SHA256), 0, nil), &artBlob{Content: content}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("research artifact insert: %w", err)
	}
	return created, nil
}

// artifactContent returns one artifact's stored bytes + kind, hash-addressed by
// sha256 and org-scoped (the file IS the org; project narrows within it). ok is
// false for an unknown hash or a project mismatch.
func (s *store) artifactContent(ctx context.Context, project, sha256Hex string) ([]byte, string, bool) {
	var meta artRecord
	if err := s.db.Get(ctx, s.db.NewKey(kindArt, artKeyID(sha256Hex), 0, nil), &meta); err != nil {
		return nil, "", false
	}
	if project != "" && meta.Project != project {
		return nil, "", false
	}
	var blob artBlob
	if err := s.db.Get(ctx, s.db.NewKey(kindArtBlob, artBlobKeyID(sha256Hex), 0, nil), &blob); err != nil {
		return nil, "", false
	}
	return blob.Content, meta.Kind, true
}

// listArtifacts returns the chronological diary feed newest-first, filtered by
// project, optional run_id, and an optional `since` unix-seconds lower bound.
// Bounded by limit.
func (s *store) listArtifacts(ctx context.Context, project, runID string, since int64, limit int) ([]Artifact, error) {
	recs, err := s.allArt(ctx)
	if err != nil {
		return nil, err
	}
	var out []Artifact
	for _, r := range recs {
		if project != "" && r.Project != project {
			continue
		}
		if runID != "" && r.RunID != runID {
			continue
		}
		if since > 0 && r.TS < since {
			continue
		}
		out = append(out, r.toArtifact())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS > out[j].TS
		}
		return out[i].SHA256 < out[j].SHA256
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r artRecord) toArtifact() Artifact {
	return Artifact{
		SHA256: r.SHA256, Kind: r.Kind, Ref: r.Ref, RunID: r.RunID, Project: r.Project,
		Visibility: r.Visibility, RetentionClass: r.RetentionClass,
		GitSHA: r.GitSHA, GitBranch: r.GitBranch, GitDirty: r.GitDirty, LibVersions: r.LibVersions, TS: r.TS,
	}
}

// setArtifactVisibility records the SEPARATE visibility grant for one artifact by
// sha256 — the same private-by-default rule as runs. The read-modify-write runs in
// one transaction (as setGrant does) so a concurrent grant cannot interleave between
// the read and the write. Returns rows touched (1 on a scoped match, 0 otherwise).
func (s *store) setArtifactVisibility(ctx context.Context, project, sha256Hex, visibility string) (int, error) {
	n := 0
	key := s.db.NewKey(kindArt, artKeyID(sha256Hex), 0, nil)
	err := s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		n = 0
		var meta artRecord
		if err := tx.Get(ctx, key, &meta); err != nil {
			if errors.Is(err, ormdb.ErrNoSuchEntity) {
				return nil
			}
			return err
		}
		if project != "" && meta.Project != project {
			return nil
		}
		meta.Visibility = visibility
		if _, err := tx.Put(ctx, key, &meta); err != nil {
			return err
		}
		n = 1
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("research artifact grant: %w", err)
	}
	return n, nil
}

// ── ops-board aggregates ──────────────────────────────────────────────────────

// ProjectSummary is one project's real totals — canonical (answered, deduped) plus
// *_retained (full versioned history) so dedup never reads as loss. Tokens are
// intentionally absent (the corpus carries no per-attempt token usage); cost_usd is
// the real spend.
type ProjectSummary struct {
	Project             string   `json:"project"`
	Experiments         int      `json:"experiments"` // canonical
	ExperimentsRetained int      `json:"experiments_retained"`
	Attempts            int      `json:"attempts"` // canonical
	AttemptsRetained    int      `json:"attempts_retained"`
	Models              int      `json:"models"`
	Benchmarks          int      `json:"benchmarks"`
	CostUSD             float64  `json:"cost_usd"`
	Kinds               []string `json:"kinds"`
}

func (s *store) projectSummaries(ctx context.Context) ([]ProjectSummary, error) {
	exps, err := s.allExp(ctx)
	if err != nil {
		return nil, err
	}
	atts, err := s.allAtt(ctx)
	if err != nil {
		return nil, err
	}

	byProj := map[string]*ProjectSummary{}
	kinds := map[string]map[string]bool{}
	models := map[string]map[string]bool{}
	benches := map[string]map[string]bool{}
	get := func(p string) *ProjectSummary {
		if byProj[p] == nil {
			byProj[p] = &ProjectSummary{Project: p}
			kinds[p] = map[string]bool{}
			models[p] = map[string]bool{}
			benches[p] = map[string]bool{}
		}
		return byProj[p]
	}

	// retained: all versions; kinds are DISTINCT over ALL retained experiments.
	for _, r := range exps {
		ps := get(r.Project)
		ps.ExperimentsRetained++
		kinds[r.Project][r.Kind] = true
	}
	for _, r := range atts {
		get(r.Project).AttemptsRetained++
	}
	// canonical answered: experiment count + cost.
	for _, r := range canonicalExp(exps) {
		if !answered(r.Status) {
			continue
		}
		ps := get(r.Project)
		ps.Experiments++
		ps.CostUSD += r.CostUSD
	}
	// canonical answered: attempt count + distinct models/benchmarks.
	for _, r := range canonicalAtt(atts) {
		if !answered(r.Status) {
			continue
		}
		ps := get(r.Project)
		ps.Attempts++
		models[r.Project][r.Model] = true
		benches[r.Project][r.Benchmark] = true
	}

	out := make([]ProjectSummary, 0, len(byProj))
	for p, ps := range byProj {
		ps.Kinds = sortedKeys(kinds[p])
		ps.Models = len(models[p])
		ps.Benchmarks = len(benches[p])
		out = append(out, *ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

// KindTotal is the per-kind slice of a totals aggregate (canonical experiments + cost).
type KindTotal struct {
	Kind        string  `json:"kind"`
	Experiments int     `json:"experiments"`
	CostUSD     float64 `json:"cost_usd"`
}

// Totals is the observatory's headline aggregate: canonical (answered) counts plus
// *_retained, and a per-kind breakdown. Deterministic from the store.
type Totals struct {
	Project             string      `json:"project,omitempty"`
	Projects            int         `json:"projects"`
	Experiments         int         `json:"experiments"` // canonical
	ExperimentsRetained int         `json:"experiments_retained"`
	Attempts            int         `json:"attempts"` // canonical
	AttemptsRetained    int         `json:"attempts_retained"`
	Models              int         `json:"models"`
	Benchmarks          int         `json:"benchmarks"`
	CostUSD             float64     `json:"cost_usd"`
	ByKind              []KindTotal `json:"by_kind"`
}

func (s *store) totals(ctx context.Context, project string) (Totals, error) {
	exps, err := s.allExp(ctx)
	if err != nil {
		return Totals{}, err
	}
	atts, err := s.allAtt(ctx)
	if err != nil {
		return Totals{}, err
	}
	if project != "" {
		exps = filterExp(exps, project)
		atts = filterAtt(atts, project)
	}

	var t Totals
	t.Project = project
	t.ExperimentsRetained = len(exps)
	t.AttemptsRetained = len(atts)

	projSet := map[string]bool{}
	kindSet := map[string]bool{}
	for _, r := range exps {
		projSet[r.Project] = true
		kindSet[r.Kind] = true
	}
	t.Projects = len(projSet)

	kindExpN := map[string]int{}
	kindCost := map[string]float64{}
	for _, r := range canonicalExp(exps) {
		if !answered(r.Status) {
			continue
		}
		t.Experiments++
		t.CostUSD += r.CostUSD
		kindExpN[r.Kind]++
		kindCost[r.Kind] += r.CostUSD
	}
	models := map[string]bool{}
	benches := map[string]bool{}
	for _, r := range canonicalAtt(atts) {
		if !answered(r.Status) {
			continue
		}
		t.Attempts++
		models[r.Model] = true
		benches[r.Benchmark] = true
	}
	t.Models = len(models)
	t.Benchmarks = len(benches)
	for _, k := range sortedKeys(kindSet) {
		t.ByKind = append(t.ByKind, KindTotal{Kind: k, Experiments: kindExpN[k], CostUSD: kindCost[k]})
	}
	return t, nil
}

func filterExp(recs []expRecord, project string) []expRecord {
	var out []expRecord
	for _, r := range recs {
		if r.Project == project {
			out = append(out, r)
		}
	}
	return out
}

func filterAtt(recs []attRecord, project string) []attRecord {
	var out []attRecord
	for _, r := range recs {
		if r.Project == project {
			out = append(out, r)
		}
	}
	return out
}

// ── small helpers ─────────────────────────────────────────────────────────────

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// jsonObj normalizes a JSON field to a compact object string; an absent or invalid
// value becomes "{}", so meta / lib_versions are always valid JSON a downstream
// query (SQLite json_extract, Datastore JSONExtract) can read. Shared with the
// warehouse roll-up (datastore.go).
func jsonObj(raw json.RawMessage) string {
	if len(raw) == 0 || !json.Valid(raw) {
		return "{}"
	}
	return string(raw)
}

// canonJSON renders a JSON value in a CANONICAL form (sorted keys, no whitespace)
// for content hashing, so a caller's re-serialization of the same object does not
// mint a spurious retained version. An unparseable value is hashed as its raw bytes
// (still deterministic); an absent value is empty.
func canonJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.Marshal(v) // encoding/json sorts map keys and strips whitespace
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// revisionOf normalizes a producer-declared version kind; empty is `original`.
// superseded and canonical are DERIVED, never stored, so they are not accepted here.
func revisionOf(r string) string {
	switch strings.TrimSpace(strings.ToLower(r)) {
	case "corrected":
		return "corrected"
	case "retracted":
		return "retracted"
	default:
		return "original"
	}
}

func sourceOf(s string) string {
	if s == "" {
		return "hanzo-measured"
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
