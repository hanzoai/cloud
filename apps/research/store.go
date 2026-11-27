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
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"

	"github.com/hanzoai/cloud/sdk/dialect"
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
	ReceivedAt  int64           `json:"received_at"`
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
	Tags        []string        `json:"tags,omitempty"`
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
	ReceivedAt  int64  `json:"received_at"`
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
	SHA256           string          `json:"sha256"`
	ContentSHA256    string          `json:"content_sha256,omitempty"`
	ArtifactID       string          `json:"artifact_id,omitempty"`
	MediaType        string          `json:"media_type,omitempty"`
	Canonicalization string          `json:"canonicalization,omitempty"`
	UncompressedSize int64           `json:"uncompressed_size,omitempty"`
	Kind             string          `json:"kind"`
	Ref              string          `json:"ref"`
	RunID            string          `json:"run_id"`
	Project          string          `json:"project"`
	Visibility       string          `json:"visibility"`
	RetentionClass   string          `json:"retention_class"`
	GitSHA           string          `json:"git_sha"`
	GitBranch        string          `json:"git_branch"`
	GitDirty         bool            `json:"git_dirty"`
	LibVersions      json.RawMessage `json:"lib_versions,omitempty"`
	ReceivedAt       int64           `json:"received_at"`
	TS               int64           `json:"ts"`
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
// client signature is unchanged (cloud.OrgStore hands a pragma'd, cek-encrypted
// connection); orm's initSchema creates its `_entities` table if absent. It then
// carries any pre-orm raw-SQL evidence forward (migrateLegacy) so an existing store
// is never silently read as empty. A migration failure FAILS the open (fail-secure)
// rather than serving an empty board over real data.
func openStore(raw *sql.DB) (*store, error) {
	db, err := orm.AdaptSQL(raw)
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
		return s.dropLegacyTables(ctx)
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
	if err := s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
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
	}); err != nil {
		return err
	}
	return s.dropLegacyTables(ctx)
}

// dropLegacyTables removes the pre-orm raw-SQL tables once their rows have been
// carried into _entities, so a later move to hanzo-sql sees only _entities.
func (s *store) dropLegacyTables(ctx context.Context) error {
	for _, name := range []string{"experiment", "attempt", "artifact"} {
		if _, err := s.raw.ExecContext(ctx, `DROP TABLE IF EXISTS `+name); err != nil {
			return fmt.Errorf("research drop legacy %s: %w", name, err)
		}
	}
	return nil
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

// legacyTables reports which pre-orm tables are present in the store. Only a SQLite
// file written before the orm cutover has any; a store on hanzo-sql never did, so it
// reports none and marks itself migrated.
func (s *store) legacyTables(ctx context.Context) (map[string]bool, error) {
	d := dialect.Of(s.raw)
	present := map[string]bool{}
	for _, name := range []string{"experiment", "attempt", "artifact"} {
		has, err := d.HasTable(ctx, s.raw, name)
		if err != nil {
			return nil, fmt.Errorf("research legacy detect: %w", err)
		}
		if has {
			present[name] = true
		}
	}
	return present, nil
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
// canonTags trims, deduplicates, and sorts tags lexicographically so re-sending
// the same tag set in a different order never mints a spurious version.
func canonTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func hashExp(e Experiment) string {
	parts := []string{
		revisionOf(e.Revision), e.Kind, e.Subject, e.Task, e.Metric,
		strconv.FormatFloat(e.Value, 'g', -1, 64), strconv.Itoa(e.N), strconv.Itoa(e.NTotal),
		strconv.FormatFloat(e.CostUSD, 'g', -1, 64), runStatus(e.Status), canonJSON(e.Meta),
		e.GitSHA, e.GitBranch, strconv.Itoa(boolInt(e.GitDirty)), canonJSON(e.LibVersions),
	}
	if tags := canonTags(e.Tags); len(tags) > 0 {
		parts = append(parts, strings.Join(tags, "\x1f"))
	}
	return hashJoin(parts...)
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
	now := time.Now().Unix()
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
			ts := ex.TS
			if ts == 0 {
				ts = now
			}
			rec := expRecord{
				Project: project, ID: ex.ID, ContentHash: ch, Seq: next, ReceivedAt: now,
				Revision: revisionOf(ex.Revision), Status: runStatus(ex.Status),
				Visibility: "private", Trainable: false, Publishable: false,
				Kind: ex.Kind, Subject: ex.Subject, Task: ex.Task, Metric: ex.Metric,
				Value: ex.Value, N: ex.N, NTotal: ex.NTotal, CostUSD: ex.CostUSD,
				Meta: json.RawMessage(jsonObj(ex.Meta)), GitSHA: ex.GitSHA, GitBranch: ex.GitBranch,
				GitDirty: ex.GitDirty, LibVersions: json.RawMessage(jsonObj(ex.LibVersions)),
				Tags: canonTags(ex.Tags), TS: ts,
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
			ts := a.TS
			if ts == 0 {
				ts = now
			}
			rec := attRecord{
				Project: project, Benchmark: a.Benchmark, Item: a.Item, Model: a.Model,
				ContentHash: ch, Seq: next, ReceivedAt: now,
				Revision: revisionOf(a.Revision), Status: runStatus(a.Status),
				Gold: a.Gold, Answer: a.Answer, Correct: a.Correct, Response: a.Response,
				Source: sourceOf(a.Source), TS: ts,
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

// An experiment's stable id is (project, id); an attempt's is (project,
// benchmark, item, model). Both fold through the ONE canonical definition
// below them (the `version` interface), because the two hand-written copies
// these replaced were identical line for line — which is how a third would have
// drifted from the other two. The NUL separator can never collide two distinct
// tuples.
func (r expRecord) stable() string { return r.Project + "\x00" + r.ID }
func (r expRecord) key() string    { return expKeyID(r.Project, r.ID, r.ContentHash) }
func (r expRecord) clock() int64   { return r.Seq }
func (r expRecord) gone() bool     { return r.Revision == "retracted" }

func (r attRecord) stable() string {
	return r.Project + "\x00" + r.Benchmark + "\x00" + r.Item + "\x00" + r.Model
}
func (r attRecord) key() string {
	return attKeyID(r.Project, r.Benchmark, r.Item, r.Model, r.ContentHash)
}
func (r attRecord) clock() int64 { return r.Seq }
func (r attRecord) gone() bool   { return r.Revision == "retracted" }

func canonicalExp(recs []expRecord) map[string]expRecord { return canonical(recs) }
func canonicalAtt(recs []attRecord) map[string]attRecord { return canonical(recs) }

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
func (s *store) listExperiments(ctx context.Context, project, kind string, tags ...string) ([]Experiment, error) {
	exps, err := s.allExp(ctx)
	if err != nil {
		return nil, err
	}
	var wantTag string
	if len(tags) > 0 {
		wantTag = strings.TrimSpace(tags[0])
	}
	var out []Experiment
	for _, r := range canonicalExp(exps) {
		if project != "" && r.Project != project {
			continue
		}
		if kind != "" && r.Kind != kind {
			continue
		}
		if wantTag != "" && !slices.Contains(r.Tags, wantTag) {
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

// experimentVersions returns every retained version of an experiment stable id,
// ordered ascending by seq so pre-registration (`planning`/`running`) precedes
// completion (`complete`). When explicitProj is empty and only one project in the
// org holds id, that project is selected; otherwise fallbackProj disambiguates.
func (s *store) experimentVersions(ctx context.Context, explicitProj, fallbackProj, id string) (string, []ExperimentVersion, error) {
	exps, err := s.allExp(ctx)
	if err != nil {
		return "", nil, err
	}
	canonByStable := canonicalExp(exps)

	targetProj := strings.TrimSpace(explicitProj)
	if targetProj == "" {
		projs := map[string]bool{}
		for _, r := range exps {
			if r.ID == id {
				projs[r.Project] = true
			}
		}
		switch {
		case len(projs) == 1:
			for p := range projs {
				targetProj = p
			}
		case projs[fallbackProj]:
			targetProj = fallbackProj
		default:
			targetProj = fallbackProj
		}
	}

	var matched []expRecord
	for _, r := range exps {
		if r.ID != id {
			continue
		}
		if targetProj != "" && r.Project != targetProj {
			continue
		}
		matched = append(matched, r)
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Seq < matched[j].Seq
	})

	out := make([]ExperimentVersion, 0, len(matched))
	for _, r := range matched {
		canon, hasCanon := canonByStable[r.stable()]
		isCanon := hasCanon && canon.Seq == r.Seq && canon.ContentHash == r.ContentHash
		out = append(out, ExperimentVersion{
			Seq:         r.Seq,
			ReceivedAt:  r.ReceivedAt,
			TS:          r.TS,
			ContentHash: r.ContentHash,
			Revision:    r.Revision,
			Status:      r.Status,
			Canonical:   isCanon,
			Value:       r.Value,
			N:           r.N,
			Meta:        shown(r.Meta),
			GitSHA:      r.GitSHA,
			Tags:        r.Tags,
		})
	}
	return targetProj, out, nil
}

func (r expRecord) toExperiment(canonical bool) Experiment {
	return Experiment{
		Project: r.Project, ID: r.ID, Seq: r.Seq, ContentHash: r.ContentHash, ReceivedAt: r.ReceivedAt,
		Revision: r.Revision, Status: r.Status,
		Canonical: canonical, Visibility: r.Visibility, Trainable: r.Trainable, Publishable: r.Publishable,
		Kind: r.Kind, Subject: r.Subject, Task: r.Task, Metric: r.Metric, Value: r.Value,
		N: r.N, NTotal: r.NTotal, CostUSD: r.CostUSD, Meta: shown(r.Meta),
		GitSHA: r.GitSHA, GitBranch: r.GitBranch, GitDirty: r.GitDirty,
		LibVersions: shown(r.LibVersions), Tags: r.Tags, TS: r.TS,
	}
}

// ── grants (visibility + consent — SEPARATE from upload) ──────────────────────

// setGrant records an authorized visibility/consent decision for a stable id's
// records — the SEPARATE authorization upload never implies. It updates EVERY
// retained version of the id (the decision is about the record, not one version)
// of the one kind the grant names: a run is BOTH measurement kinds that carry
// consent, the experiment rows and the run records that share the id; a decision,
// an evaluation or evidence (decision.go) takes visibility alone, and only from a
// person (errNotPerson otherwise). An empty kind is the one kind holding the id,
// and an id held by more than one kind is refused (ambiguous), so a grant never
// moves a record it did not name: a run holds the id in any project, as the
// thread kinds do, though a grant moves only its project's runs. The load +
// updates run inside one transaction so a concurrent append cannot slip a new
// version past the grant. A nil pointer leaves that field unchanged. Returns
// versions touched. visibility/consent are NOT part of content identity, so an
// in-place update keeps each version's id (a Put, not a new version).
func (s *store) setGrant(ctx context.Context, project, kind, id string, visibility *string, trainable, publishable *bool, person bool) (int, error) {
	if visibility == nil && trainable == nil && publishable == nil {
		return 0, nil
	}
	n := 0
	var refused error
	err := s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		n, refused = 0, nil
		var exps []expRecord
		if _, e := tx.Query(kindExp).GetAll(ctx, &exps); e != nil {
			return e
		}
		var runs []runRecord
		if _, e := tx.Query(kindRun).GetAll(ctx, &runs); e != nil {
			return e
		}
		held := []string{}
		if slices.ContainsFunc(exps, func(r expRecord) bool { return r.ID == id }) || slices.ContainsFunc(runs, func(r runRecord) bool { return r.ID == id }) {
			held = append(held, "run")
		}
		exps = slices.DeleteFunc(exps, func(r expRecord) bool { return r.Project != project || r.ID != id })
		runs = slices.DeleteFunc(runs, func(r runRecord) bool { return r.Project != project || r.ID != id })
		thread, e := threadHolding(ctx, tx, id)
		if e != nil {
			return e
		}
		for _, k := range threadKinds {
			if thread[k] > 0 {
				held = append(held, k)
			}
		}
		k := kind
		if k == "" {
			if len(held) > 1 {
				refused = ambiguous{id: id, kinds: held}
				return refused
			}
			if len(held) == 0 {
				return nil
			}
			k = held[0]
		}
		if k != "run" {
			if !person {
				refused = errNotPerson
				return refused
			}
			if visibility == nil {
				return nil
			}
			n, e = grantThread(ctx, tx, k, id, *visibility)
			return e
		}
		for i := range exps {
			r := exps[i]
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
		for i := range runs {
			r := runs[i]
			if visibility != nil {
				r.Visibility = *visibility
			}
			if trainable != nil {
				r.Trainable = *trainable
			}
			if publishable != nil {
				r.Publishable = *publishable
			}
			if _, e := tx.Put(ctx, tx.NewKey(kindRun, r.key(), 0, nil), &r); e != nil {
				return e
			}
			n++
		}
		return nil
	})
	if refused != nil {
		return 0, refused
	}
	if err != nil {
		return 0, fmt.Errorf("research grant: %w", err)
	}
	return n, nil
}

// errNotPerson refuses a grant of a decision thread kind to a caller that is not
// a person signed in as themselves (principal.Signed): publishing a decision, an
// evaluation or evidence is a person's act.
var errNotPerson = errors.New("research: a thread grant is a person's")

// ambiguous is a grant naming no kind whose id more than one kind holds.
type ambiguous struct {
	id    string
	kinds []string
}

func (a ambiguous) Error() string {
	return fmt.Sprintf("%q names records of more than one kind (%s): a grant names the kind it is for", a.id, strings.Join(a.kinds, ", "))
}

// ── artifacts (the research diary — raw-artifact retention class) ─────────────

// putArtifact records one artifact idempotently by its server-derived sha256. The
// metadata and the bytes are written together in one transaction (so a metadata row
// can never outlive a missing blob), under distinct id keyspaces so listing the
// diary never loads blobs. project is the SERVER's value; visibility is FORCED
// private (a grant is separate). Returns 1 when a new artifact was recorded, 0 on a
// dup (same bytes → same hash → a no-op).
func (s *store) putArtifact(ctx context.Context, project string, a ResearchArtifact, content []byte) (int, artRecord, error) {
	created := 0
	now := time.Now().Unix()
	var stored artRecord
	err := s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		created = 0
		ts := a.TS
		if ts == 0 {
			ts = now
		}
		uncomp := a.UncompressedSize
		if uncomp <= 0 {
			uncomp = int64(len(content))
		}
		rec := artRecord{
			SHA256:           a.SHA256,
			ContentSHA256:    cmp.Or(a.ContentSHA256, a.SHA256),
			ArtifactID:       a.ArtifactID,
			MediaType:        a.MediaType,
			Canonicalization: a.Canonicalization,
			UncompressedSize: uncomp,
			Kind:             a.Kind,
			Ref:              a.Ref,
			RunID:            a.RunID,
			Project:          project,
			Visibility:       "private",
			RetentionClass:   "raw-artifact",
			GitSHA:           a.GitSHA,
			GitBranch:        a.GitBranch,
			GitDirty:         a.GitDirty,
			LibVersions:      json.RawMessage(jsonObj(a.LibVersions)),
			ReceivedAt:       now,
			TS:               ts,
		}
		key := tx.NewKey(kindArt, artKeyID(a.SHA256), 0, nil)
		madeMeta, e := tx.CreateIfAbsent(ctx, key, &rec)
		if e != nil {
			return e
		}
		if madeMeta {
			created = 1
			stored = rec
			if _, e := tx.CreateIfAbsent(ctx, tx.NewKey(kindArtBlob, artBlobKeyID(a.SHA256), 0, nil), &artBlob{Content: content}); e != nil {
				return e
			}
			return nil
		}
		var existing artRecord
		if e := tx.Get(ctx, key, &existing); e == nil {
			stored = existing
		} else {
			stored = rec
		}
		return nil
	})
	if err != nil {
		return 0, artRecord{}, fmt.Errorf("research artifact insert: %w", err)
	}
	return created, stored, nil
}

// artifactContent returns one artifact's stored bytes + kind, hash-addressed by
// sha256 and org-scoped (the file IS the org; project narrows within it). ok is
// false for an unknown hash or a project mismatch.
func (s *store) artifactContent(ctx context.Context, project, sha256Hex string) ([]byte, string, bool) {
	blob, meta, ok := s.artifactBlob(ctx, project, sha256Hex)
	if !ok {
		return nil, "", false
	}
	return blob, meta.Kind, true
}

// artifactBlob returns one artifact's stored bytes and full metadata record.
func (s *store) artifactBlob(ctx context.Context, project, sha256Hex string) ([]byte, artRecord, bool) {
	var meta artRecord
	if err := s.db.Get(ctx, s.db.NewKey(kindArt, artKeyID(sha256Hex), 0, nil), &meta); err != nil {
		return nil, artRecord{}, false
	}
	if project != "" && meta.Project != project {
		return nil, artRecord{}, false
	}
	var blob artBlob
	if err := s.db.Get(ctx, s.db.NewKey(kindArtBlob, artBlobKeyID(sha256Hex), 0, nil), &blob); err != nil {
		return nil, artRecord{}, false
	}
	return blob.Content, meta, true
}

// listArtifacts returns the chronological diary feed newest-first, filtered by
// project, optional run_id, and an optional `since` unix-seconds lower bound.
// Bounded by limit.
func (s *store) listArtifacts(ctx context.Context, project, runID string, since int64, limit int) ([]ResearchArtifact, error) {
	recs, err := s.allArt(ctx)
	if err != nil {
		return nil, err
	}
	var out []ResearchArtifact
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

func (r artRecord) toArtifact() ResearchArtifact {
	contentSHA := cmp.Or(r.ContentSHA256, r.SHA256)
	return ResearchArtifact{
		SHA256:           r.SHA256,
		ContentSHA256:    contentSHA,
		ArtifactID:       r.ArtifactID,
		MediaType:        r.MediaType,
		Canonicalization: r.Canonicalization,
		UncompressedSize: r.UncompressedSize,
		Kind:             r.Kind,
		Ref:              r.Ref,
		RunID:            r.RunID,
		Project:          r.Project,
		Visibility:       r.Visibility,
		RetentionClass:   r.RetentionClass,
		GitSHA:           r.GitSHA,
		GitBranch:        r.GitBranch,
		GitDirty:         r.GitDirty,
		LibVersions:      shown(r.LibVersions),
		ReceivedAt:       r.ReceivedAt,
		TS:               r.TS,
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
	// Project is the project these totals cover. Rows are one per project the org
	// has evidence under, sorted by this.
	Project string `json:"project"`
	// Experiments counts the CANONICAL answered runs: one per stable id, the
	// latest non-retracted version, faulted and failed left out.
	Experiments int `json:"experiments"`
	// ExperimentsRetained counts every run version the store holds for the
	// project — superseded and retracted included. Retained is always ≥
	// canonical, and the gap is history rather than loss.
	ExperimentsRetained int `json:"experiments_retained"`
	// Attempts counts the CANONICAL answered attempts, one per (benchmark, item,
	// model).
	Attempts int `json:"attempts"`
	// AttemptsRetained counts every attempt version held, on the same terms as
	// ExperimentsRetained.
	AttemptsRetained int `json:"attempts_retained"`
	// Models is how many DISTINCT models appear across the canonical answered
	// attempts.
	Models int `json:"models"`
	// Benchmarks is how many DISTINCT benchmarks those same attempts cover.
	Benchmarks int `json:"benchmarks"`
	// CostUSD is US DOLLARS (not cents) summed over the canonical answered runs,
	// so it is what the deduped view cost and not what the history cost.
	CostUSD float64 `json:"cost_usd"`
	// Kinds are the discriminators seen in the project, sorted, over ALL retained
	// versions — so a kind stays listed after its runs are superseded or
	// retracted, even when it contributes nothing to Experiments.
	Kinds []string `json:"kinds"`
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
	// Kind is the discriminator this row aggregates: benchmark, kernel-perf,
	// training, ablation, policy-eval, ab. A kind appears as soon as any RETAINED
	// version carries it, so a kind whose every run was retracted or faulted is
	// listed with zero experiments rather than dropped.
	Kind string `json:"kind"`
	// Experiments counts the canonical answered runs of this kind — the same
	// predicate as the headline count, sliced.
	Experiments int `json:"experiments"`
	// CostUSD is US DOLLARS (not cents) summed over those same runs.
	CostUSD float64 `json:"cost_usd"`
}

// ResearchTotals is the observatory's headline aggregate: canonical (answered) counts plus
// *_retained, and a per-kind breakdown. Deterministic from the store.
//
// The name carries its own product because the SCHEMA namespace is FLAT: zip keys a
// component schema on the Go type's bare name (typeName drops the package), so the
// fleet compose refuses two apps that mean different things by one name. It was
// `Totals`, and apps/admin publishes a `Totals` of its own about volumes and
// clusters — the compose named the collision the moment this type became an op's Out.
type ResearchTotals struct {
	// Project echoes the project the aggregate was narrowed to. Absent means the
	// figures below span the whole org.
	Project string `json:"project,omitempty"`
	// Projects is how many DISTINCT projects the counted evidence spans, so it is
	// 1 whenever Project narrows to one.
	Projects int `json:"projects"`
	// Experiments counts the CANONICAL answered runs: one per stable id, the
	// latest non-retracted version, faulted and failed left out.
	Experiments int `json:"experiments"`
	// ExperimentsRetained counts every run version held — superseded and
	// retracted included. Retained is always ≥ canonical, and the gap is history
	// rather than loss, which is why both travel together.
	ExperimentsRetained int `json:"experiments_retained"`
	// Attempts counts the CANONICAL answered attempts, one per (benchmark, item,
	// model).
	Attempts int `json:"attempts"`
	// AttemptsRetained counts every attempt version held, on the same terms as
	// ExperimentsRetained.
	AttemptsRetained int `json:"attempts_retained"`
	// Models is how many DISTINCT models appear across the canonical answered
	// attempts.
	Models int `json:"models"`
	// Benchmarks is how many DISTINCT benchmarks those same attempts cover.
	Benchmarks int `json:"benchmarks"`
	// CostUSD is US DOLLARS (not cents) summed over the canonical answered runs.
	CostUSD float64 `json:"cost_usd"`
	// ByKind is the same headline split per discriminator, ordered by kind.
	ByKind []KindTotal `json:"by_kind"`
}

func (s *store) totals(ctx context.Context, project string) (ResearchTotals, error) {
	exps, err := s.allExp(ctx)
	if err != nil {
		return ResearchTotals{}, err
	}
	atts, err := s.allAtt(ctx)
	if err != nil {
		return ResearchTotals{}, err
	}
	if project != "" {
		exps = filterExp(exps, project)
		atts = filterAtt(atts, project)
	}

	var t ResearchTotals
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

// shown is a stored JSON object as a reader sees it: the canonical form, whatever
// the engine kept. SQLite hands back the bytes the caller wrote; PostgreSQL's jsonb
// hands back its own spacing and key order. A response must not depend on which
// engine the store runs on.
func shown(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(canonJSON(raw))
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

// ── the experiment record: benchmark · run · study · paper ────────────────────
//
// record.go says what these four are. This half is how they are STORED, and the
// answer is: exactly like the two kinds above them. Same per-org SQLite, same orm
// `_entities` (so a new field is a struct field and never a migration), same
// content-hash version identity, same server-assigned append clock deciding
// supersession, same private-by-default consent. There is no second store, and
// the machinery below is written ONCE over every kind rather than five times.

const (
	kindBench = "research-benchmark"
	kindRun   = "research-run"
	kindStudy = "research-study"
	kindPaper = "research-paper"
	// The decision thread (decision.go): evidence, the DDT decisions that name it,
	// the evaluations that read it, and the actions and commitments that follow.
	kindEvidence   = "research-evidence"
	kindDecision   = "research-decision"
	kindEvaluation = "research-evaluation"
	kindAction     = "research-action"
	kindCommitment = "research-commitment"
)

func init() {
	orm.Register[benchRecord](kindBench)
	orm.Register[runRecord](kindRun)
	orm.Register[studyRecord](kindStudy)
	orm.Register[paperRecord](kindPaper)
	orm.Register[evidenceRecord](kindEvidence)
	orm.Register[decisionRecord](kindDecision)
	orm.Register[evaluationRecord](kindEvaluation)
	orm.Register[actionRecord](kindAction)
	orm.Register[commitmentRecord](kindCommitment)
}

// version is what every record kind in this plane has in common, and the only
// thing the shared machinery needs of one: which stable id its versions accrue
// under, which entity this version is, when it was appended, and whether it
// withdraws the id. Writing it once is why canonical has ONE definition rather
// than one per kind — the two hand-written copies this replaced were identical,
// which is how a third would have drifted.
type version interface {
	stable() string // the id versions accrue under, NUL-joined so no pair collides
	key() string    // this version's orm entity id
	clock() int64   // the append clock this version was stamped with
	gone() bool     // whether this version retracts the id
}

// canonical folds versions to the canonical one per stable id: the max-clock
// version, dropped when that latest version is a retraction (the id is
// WITHDRAWN — a retraction is honored, not ignored). Every version stays
// retained regardless.
func canonical[T version](recs []T) map[string]T {
	latest := map[string]T{}
	for _, r := range recs {
		if cur, ok := latest[r.stable()]; !ok || r.clock() > cur.clock() {
			latest[r.stable()] = r
		}
	}
	for k, r := range latest {
		if r.gone() {
			delete(latest, k)
		}
	}
	return latest
}

// loadAll reads every version of one kind. Same in-memory fold as the scale note
// above: right while a per-org corpus is small, escalating to an orm-indexed
// filter if one ever is not.
func loadAll[T any](ctx context.Context, s *store, kind string) ([]T, error) {
	var recs []T
	if _, err := s.db.Query(kind).GetAll(ctx, &recs); err != nil {
		return nil, fmt.Errorf("research load %s: %w", kind, err)
	}
	return recs, nil
}

// putAll appends one batch of versions of one kind in a single transaction,
// idempotent by the content hash each record already carries and stamped with
// the next per-store seq — the same append clock the experiment ingest advances,
// so supersession is one order across every kind in the file. Returns which
// records were NEW versions, by index; a byte-identical re-send appends none.
func putAll[T version](ctx context.Context, s *store, kind string, recs []T, stamp func(*T, int64)) ([]bool, error) {
	var made []bool
	err := s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		var e error
		made, e = appendTx(ctx, tx, kind, recs, stamp) // per attempt, so a retried transaction never double-counts
		return e
	})
	if err != nil {
		return nil, fmt.Errorf("research put %s: %w", kind, err)
	}
	return made, nil
}

// appendTx is putAll's append inside a transaction the caller holds, for a write
// that reads what it decides on in the same transaction: each record not already
// held under its key is stamped with the next seq and created. Returns which
// records were NEW versions, by index.
func appendTx[T version](ctx context.Context, tx orm.DB, kind string, recs []T, stamp func(*T, int64)) ([]bool, error) {
	made := make([]bool, len(recs))
	seq, err := loadSeq(ctx, tx)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		r := recs[i]
		next := seq + 1
		stamp(&r, next)
		created, err := tx.CreateIfAbsent(ctx, tx.NewKey(kind, r.key(), 0, nil), &r)
		if err != nil {
			return nil, fmt.Errorf("%s insert: %w", kind, err)
		}
		if created {
			seq = next
			made[i] = true
		}
	}
	return made, putSeq(ctx, tx, seq)
}

// tally counts the new versions putAll reports.
func tally(made []bool, err error) (int, error) {
	n := 0
	for _, m := range made {
		if m {
			n++
		}
	}
	return n, err
}

// ── stored shapes ─────────────────────────────────────────────────────────────
//
// Each mirrors its wire type minus what is DERIVED (canonical, completion) and
// plus what the plane owns (content_hash, seq). Consent rides on the run alone:
// a run is a measurement and carries the same private-by-default grant an
// experiment does; a study and a paper carry no measurement, so forcing a
// consent decision onto them would be a field nobody could answer.

type benchRecord struct {
	Project     string   `json:"project"`
	ID          string   `json:"id"`
	ContentHash string   `json:"content_hash"`
	Seq         int64    `json:"seq"`
	Revision    string   `json:"revision"`
	Title       string   `json:"title"`
	Version     string   `json:"version"`
	Dataset     string   `json:"dataset"`
	License     string   `json:"license"`
	Origin      string   `json:"origin"`
	Splits      []Split  `json:"splits"`
	Metric      string   `json:"metric"`
	Metrics     []string `json:"metrics"`
	Citation    string   `json:"citation"`
	Notes       string   `json:"notes"`
	When        string   `json:"when"`
}

func (r benchRecord) stable() string { return r.Project + "\x00" + r.ID }
func (r benchRecord) key() string    { return "k:" + hashJoin(r.Project, r.ID, r.ContentHash) }
func (r benchRecord) clock() int64   { return r.Seq }
func (r benchRecord) gone() bool     { return r.Revision == "retracted" }

type runRecord struct {
	Project       string    `json:"project"`
	ID            string    `json:"id"`
	ContentHash   string    `json:"content_hash"`
	Seq           int64     `json:"seq"`
	Revision      string    `json:"revision"`
	Benchmark     string    `json:"benchmark"`
	Split         string    `json:"split"`
	System        string    `json:"system"`
	Version       string    `json:"version"`
	Baseline      bool      `json:"baseline"`
	Study         string    `json:"study"`
	Embedder      string    `json:"embedder"`
	Reader        string    `json:"reader"`
	K             *int      `json:"k"`
	Temperature   *float64  `json:"temperature"`
	MaxTokens     *int      `json:"max_tokens"`
	Prompt        string    `json:"prompt"`
	PromptDigest  string    `json:"prompt_digest"`
	DatasetDigest string    `json:"dataset_digest"`
	StoreDigest   string    `json:"store_digest"`
	FactsDigest   string    `json:"facts_digest"`
	Commit        string    `json:"commit"`
	Questions     *int      `json:"questions"`
	Answered      *int      `json:"answered"`
	When          string    `json:"when"`
	Ended         string    `json:"ended"`
	By            string    `json:"by"`
	Measures      []Measure `json:"measures"`
	Notes         string    `json:"notes"`
	Visibility    string    `json:"visibility"`
	Trainable     bool      `json:"trainable"`
	Publishable   bool      `json:"publishable"`
}

func (r runRecord) stable() string { return r.Project + "\x00" + r.ID }
func (r runRecord) key() string    { return "u:" + hashJoin(r.Project, r.ID, r.ContentHash) }
func (r runRecord) clock() int64   { return r.Seq }
func (r runRecord) gone() bool     { return r.Revision == "retracted" }

type studyRecord struct {
	Improvement *ImprovementPolicy `json:"improvement,omitempty"`
	Project     string             `json:"project"`
	ID          string             `json:"id"`
	ContentHash string             `json:"content_hash"`
	Seq         int64              `json:"seq"`
	Revision    string             `json:"revision"`
	Title       string             `json:"title"`
	Question    string             `json:"question"`
	Finding     string             `json:"finding"`
	When        string             `json:"when"`
}

func (r studyRecord) stable() string { return r.Project + "\x00" + r.ID }
func (r studyRecord) key() string    { return "s:" + hashJoin(r.Project, r.ID, r.ContentHash) }
func (r studyRecord) clock() int64   { return r.Seq }
func (r studyRecord) gone() bool     { return r.Revision == "retracted" }

type paperRecord struct {
	Project     string   `json:"project"`
	ID          string   `json:"id"`
	ContentHash string   `json:"content_hash"`
	Seq         int64    `json:"seq"`
	Revision    string   `json:"revision"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	Venue       string   `json:"venue"`
	DOI         string   `json:"doi"`
	URL         string   `json:"url"`
	Status      string   `json:"status"`
	Cites       []string `json:"cites"`
	Study       string   `json:"study"`
	When        string   `json:"when"`
}

func (r paperRecord) stable() string { return r.Project + "\x00" + r.ID }
func (r paperRecord) key() string    { return "p:" + hashJoin(r.Project, r.ID, r.ContentHash) }
func (r paperRecord) clock() int64   { return r.Seq }
func (r paperRecord) gone() bool     { return r.Revision == "retracted" }

// ── content identity ──────────────────────────────────────────────────────────
//
// Each version's identity is its revision plus everything SUBSTANTIVE about it.
// Re-sending byte-identical content appends nothing; changing any of it appends a
// RETAINED version beside the old one. Visibility and consent are never part of
// it — a grant is a decision about a record, not a new version of it — and
// neither is any derived field.

func hashBench(b ResearchBenchmark) string {
	return hashJoin(revisionOf(b.Revision), b.Title, b.Version, b.Dataset, b.License,
		b.Origin, marshal(b.Splits), b.Metric, marshal(b.Metrics), b.Citation, b.Notes, b.When)
}

// hashRun covers the whole execution — every knob, every digest, the commit, the
// completion counts and the numbers. A run re-recorded with its end stamp filled
// in is therefore a CORRECTION that supersedes, not a duplicate that is dropped,
// which is the path a run first published from a checkpoint is fixed by.
func hashRun(r Run) string {
	return hashJoin(revisionOf(r.Revision), r.Benchmark, r.Split, r.System, r.Version,
		strconv.Itoa(boolInt(r.Baseline)), r.Study, r.Embedder, r.Reader,
		itoa(r.K), ftoa(r.Temperature), itoa(r.MaxTokens),
		r.Prompt, r.PromptDigest, r.DatasetDigest, r.StoreDigest, r.FactsDigest, r.Commit,
		itoa(r.Questions), itoa(r.Answered), r.When, r.Ended, r.By,
		marshal(r.Measures), r.Notes)
}

func hashStudy(s Study) string {
	// Preserve the identity of pre-loop studies when no policy is supplied.
	fields := []string{revisionOf(s.Revision), s.Title, s.Question, s.Finding, s.When}
	if s.Improvement != nil {
		fields = append(fields, marshal(s.Improvement))
	}
	return hashJoin(fields...)
}

func hashPaper(p Paper) string {
	return hashJoin(revisionOf(p.Revision), p.Title, marshal(p.Authors), p.Venue,
		p.DOI, p.URL, p.Status, marshal(p.Cites), p.Study, p.When)
}

// marshal renders a value deterministically for hashing. Struct field order is
// fixed by the type and slice order is the producer's, so re-sending the same
// value hashes the same; re-ORDERING a list is a different value and says so.
func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ── writes ────────────────────────────────────────────────────────────────────

// putBenchmarks appends benchmark definitions. project is the SERVER's value.
func (s *store) putBenchmarks(ctx context.Context, project string, in []ResearchBenchmark) (int, error) {
	recs := make([]benchRecord, len(in))
	for i, b := range in {
		recs[i] = benchRecord{
			Project: project, ID: b.ID, ContentHash: hashBench(b), Revision: revisionOf(b.Revision),
			Title: b.Title, Version: b.Version, Dataset: b.Dataset, License: b.License,
			Origin: b.Origin, Splits: b.Splits, Metric: b.Metric, Metrics: b.Metrics,
			Citation: b.Citation, Notes: b.Notes, When: b.When,
		}
	}
	return tally(putAll(ctx, s, kindBench, recs, func(r *benchRecord, seq int64) { r.Seq = seq }))
}

// putRuns appends run versions. project is the SERVER's value and consent is
// FORCED private/withheld — recording a run grants nothing, exactly as ingesting
// an experiment does not.
func (s *store) putRuns(ctx context.Context, project string, in []Run) (int, error) {
	recs := make([]runRecord, len(in))
	for i, r := range in {
		recs[i] = runRecord{
			Project: project, ID: r.ID, ContentHash: hashRun(r), Revision: revisionOf(r.Revision),
			Benchmark: r.Benchmark, Split: r.Split, System: r.System, Version: r.Version,
			Baseline: r.Baseline, Study: r.Study, Embedder: r.Embedder, Reader: r.Reader,
			K: r.K, Temperature: r.Temperature, MaxTokens: r.MaxTokens,
			Prompt: r.Prompt, PromptDigest: r.PromptDigest, DatasetDigest: r.DatasetDigest,
			StoreDigest: r.StoreDigest, FactsDigest: r.FactsDigest, Commit: r.Commit,
			Questions: r.Questions, Answered: r.Answered, When: r.When, Ended: r.Ended, By: r.By,
			Measures: r.Measures, Notes: r.Notes,
			Visibility: "private", Trainable: false, Publishable: false,
		}
	}
	return tally(putAll(ctx, s, kindRun, recs, func(r *runRecord, seq int64) { r.Seq = seq }))
}

// putStudies appends study versions. project is the SERVER's value.
func (s *store) putStudies(ctx context.Context, project string, in []Study) (int, error) {
	recs := make([]studyRecord, len(in))
	for i, st := range in {
		recs[i] = studyRecord{
			Improvement: st.Improvement,
			Project:     project, ID: st.ID, ContentHash: hashStudy(st), Revision: revisionOf(st.Revision),
			Title: st.Title, Question: st.Question, Finding: st.Finding, When: st.When,
		}
	}
	return tally(putAll(ctx, s, kindStudy, recs, func(r *studyRecord, seq int64) { r.Seq = seq }))
}

// putPapers appends paper versions. project is the SERVER's value.
func (s *store) putPapers(ctx context.Context, project string, in []Paper) (int, error) {
	recs := make([]paperRecord, len(in))
	for i, p := range in {
		recs[i] = paperRecord{
			Project: project, ID: p.ID, ContentHash: hashPaper(p), Revision: revisionOf(p.Revision),
			Title: p.Title, Authors: p.Authors, Venue: p.Venue, DOI: p.DOI, URL: p.URL,
			Status: p.Status, Cites: p.Cites, Study: p.Study, When: p.When,
		}
	}
	return tally(putAll(ctx, s, kindPaper, recs, func(r *paperRecord, seq int64) { r.Seq = seq }))
}

// ── reads ─────────────────────────────────────────────────────────────────────

// benchmarks returns the canonical benchmark definitions, filtered by project
// ("" = every project in the org) and optionally one id, ordered by (project, id).
func (s *store) benchmarks(ctx context.Context, project, id string) ([]ResearchBenchmark, error) {
	recs, err := loadAll[benchRecord](ctx, s, kindBench)
	if err != nil {
		return nil, err
	}
	var out []ResearchBenchmark
	for _, r := range canonical(recs) {
		if (project != "" && r.Project != project) || (id != "" && r.ID != id) {
			continue
		}
		out = append(out, ResearchBenchmark{
			Project: r.Project, ID: r.ID, Title: r.Title, Version: r.Version,
			Dataset: r.Dataset, License: r.License, Origin: r.Origin, Splits: r.Splits,
			Metric: r.Metric, Metrics: r.Metrics, Citation: r.Citation, Notes: r.Notes,
			Revision: r.Revision, Canonical: true, When: r.When,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// runFilter is how a run listing is narrowed. Every field is optional and an
// empty one narrows nothing; `Completion` is checked against the DERIVED value,
// so `?completion=partial` answers the question this record was built for —
// which published runs never finished. IDs and Benchmarks are sets: a run
// passes when it carries any one of them.
type runFilter struct {
	Project    string
	IDs        []string
	Key        string
	Benchmarks []string
	System     string
	Study      string
	Completion string
	Since      string
	Until      string
}

// runs returns the canonical runs matching a filter, each with its derived
// completion, ordered by (project, id).
func (s *store) runs(ctx context.Context, f runFilter) ([]Run, error) {
	recs, err := loadAll[runRecord](ctx, s, kindRun)
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, r := range canonical(recs) {
		if run := r.toRun(); f.keeps(run) {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// keeps reports whether a run passes the filter.
func (f runFilter) keeps(run Run) bool {
	switch {
	case f.Project != "" && run.Project != f.Project:
	case len(f.IDs) > 0 && !slices.Contains(f.IDs, run.ID):
	case f.Key != "" && run.Key != f.Key:
	case len(f.Benchmarks) > 0 && !slices.Contains(f.Benchmarks, run.Benchmark):
	case f.System != "" && run.System != f.System:
	case f.Study != "" && run.Study != f.Study:
	case f.Completion != "" && run.Completion != f.Completion:
	case !within(run.When, f.Since, f.Until):
	default:
		return true
	}
	return false
}

// run returns one canonical run by (project, id), or ok=false when the id has no
// canonical version — never recorded, or withdrawn by a retraction.
func (s *store) run(ctx context.Context, project, id string) (Run, bool, error) {
	rs, err := s.runs(ctx, runFilter{Project: project, IDs: []string{id}})
	if err != nil || len(rs) == 0 {
		return Run{}, false, err
	}
	return rs[0], true, nil
}

// toRun is a stored version as the wire reads it, with what is derived on read —
// completion and key — computed from the stored fields. Neither is stored, so a
// row written before either existed reads with both, and a change to how one is
// derived reaches every row at once.
func (r runRecord) toRun() Run {
	run := Run{
		Project: r.Project, ID: r.ID, Benchmark: r.Benchmark, Split: r.Split,
		System: r.System, Version: r.Version, Baseline: r.Baseline, Study: r.Study,
		Embedder: r.Embedder, Reader: r.Reader, K: r.K, Temperature: r.Temperature,
		MaxTokens: r.MaxTokens, Prompt: r.Prompt, PromptDigest: r.PromptDigest,
		DatasetDigest: r.DatasetDigest, StoreDigest: r.StoreDigest, FactsDigest: r.FactsDigest,
		Commit: r.Commit, Questions: r.Questions, Answered: r.Answered,
		When: r.When, Ended: r.Ended, By: r.By,
		Completion: completionOf(r.Questions, r.Answered, r.Ended),
		Measures:   r.Measures, Notes: r.Notes,
		Revision: r.Revision, Canonical: true,
		Visibility: r.Visibility, Trainable: r.Trainable, Publishable: r.Publishable,
	}
	run.Key = keyOf(run)
	return run
}

// studies returns the canonical studies, filtered by project and optionally one id.
func (s *store) studies(ctx context.Context, project, id string) ([]Study, error) {
	recs, err := loadAll[studyRecord](ctx, s, kindStudy)
	if err != nil {
		return nil, err
	}
	var out []Study
	for _, r := range canonical(recs) {
		if (project != "" && r.Project != project) || (id != "" && r.ID != id) {
			continue
		}
		out = append(out, Study{
			Improvement: r.Improvement,
			Project:     r.Project, ID: r.ID, Title: r.Title, Question: r.Question,
			Finding: r.Finding, Revision: r.Revision, Canonical: true, When: r.When,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// papers returns the canonical papers, filtered by project, optionally one id,
// the study they report, or a run id they CITE — which is the read that answers
// "what did we publish off this run".
func (s *store) papers(ctx context.Context, project, id, study, cites string) ([]Paper, error) {
	recs, err := loadAll[paperRecord](ctx, s, kindPaper)
	if err != nil {
		return nil, err
	}
	var out []Paper
	for _, r := range canonical(recs) {
		switch {
		case project != "" && r.Project != project:
		case id != "" && r.ID != id:
		case study != "" && r.Study != study:
		case cites != "" && !has(r.Cites, cites):
		default:
			out = append(out, Paper{
				Project: r.Project, ID: r.ID, Title: r.Title, Authors: r.Authors,
				Venue: r.Venue, DOI: r.DOI, URL: r.URL, Status: r.Status,
				Cites: r.Cites, Study: r.Study, Revision: r.Revision, Canonical: true, When: r.When,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func has(xs []string, v string) bool {
	return slices.Contains(xs, v)
}
