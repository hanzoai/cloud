package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/text"

	// locus.Use is the ONE opener (it moves the store off its file first, when asked): on SQLite the database is born encrypted under
	// the key cek derives from the process master and the system namespace, and comes
	// back with the single-connection cap already applied; on hanzo-sql it is the
	// store's schema.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// The ONE "sqlite" driver.
	_ "github.com/hanzoai/sqlite"
	"golang.org/x/text/unicode/norm"
)

// errNoIndex is returned when a uid has never been created for the org. The
// handler maps it to Meilisearch's index_not_found, which is load-bearing: the
// meilisearch JS client keys index auto-creation off exactly that code.
var errNoIndex = errors.New("index: index not found")

// Index is the per-org index descriptor — Meilisearch's index object.
type Index struct {
	UID                  string   `json:"uid"`
	PrimaryKey           string   `json:"primaryKey"`
	FilterableAttributes []string `json:"filterableAttributes,omitempty"`
	CreatedAt            string   `json:"createdAt"`
	UpdatedAt            string   `json:"updatedAt"`
}

// Store is the index database. ONE database — the deployment's own "index", a
// SQLite file or on hanzo-sql a schema — holds every org's indexes and documents;
// tenant isolation is the `org` column, enforced on EVERY query — the same storage
// shape clients/ads and clients/crm use. On SQLite the pool is one connection,
// which serializes writes against the single-writer file; on hanzo-sql the writers
// take the store's lock (Dialect.BeginWrite).
//
// Three tables, not one pair per index: a standalone Meilisearch mints a
// directory per index, but a relational store has no reason to. `org` and `uid`
// are ordinary columns, so a new index is a row, never DDL — which is what lets an
// untrusted uid be stored verbatim instead of sanitized into a table name.
//
// # Why the term table and not FTS5
//
// FTS5 is a compile-time module, and cloud links the SYSTEM SQLite so the
// SQLCipher codec is real: that library is built by the distro, ships
// ENABLE_FTS3 and HAS_CODEC, and has no fts5 module — the `sqlite_fts5` build
// tag only affects the vendored amalgamation, so it is inert here. An index that
// depended on FTS5 would open on a pure-Go build, pass its tests, and then fail
// to create a single table in the shipped binary.
//
// terms is an ordinary inverted index instead: one row per (document,
// term), keyed so a prefix query is an index range scan. It behaves identically
// in every build lane and on both engines, and costs nothing but rows, which is
// why it is not a Words index (sdk/dialect): the terms here are the folded,
// accent-stripped words of one tokenizer in this package, the same on either
// engine, and a range of them is a seek.
//
// `term` and `pk` are Bytewise: a prefix is the range [lo, hi) of the term, and
// documents page and tie-break in the order of their keys.
type Store struct {
	db *sql.DB
	d  dialect.Dialect
}

func openStore(dir string) (*Store, error) {
	s, err := locus.Use(context.Background(), nil, namespace.System(), "index", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, d: dialect.Of(db)}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open index store: %w", err)
	}
	return s, nil
}

// migrate creates the index registry, the document row store, and the inverted
// index over the extracted searchable text, in one transaction under the store's
// lock. Idempotent (IF NOT EXISTS).
//
// terms is ordered (org, uid, term, pk) so a prefix query reads one
// contiguous run of the primary key rather than scanning; the secondary index
// on (org, uid, pk) is what makes re-indexing one document cheap, since a
// replace deletes that document's terms before writing the new ones.
func (s *Store) migrate() error {
	d := s.d
	ddl := `
CREATE TABLE IF NOT EXISTS indexes (
  org         TEXT NOT NULL,
  uid         TEXT NOT NULL,
  primary_key TEXT NOT NULL,
  filterable  TEXT NOT NULL DEFAULT '[]',
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  PRIMARY KEY (org, uid)
);
CREATE TABLE IF NOT EXISTS docs (
  org TEXT NOT NULL,
  uid TEXT NOT NULL,
  pk  TEXT NOT NULL ` + d.Bytewise() + `,
  usr TEXT NOT NULL DEFAULT '',
  doc TEXT NOT NULL,
  PRIMARY KEY (org, uid, pk)
);
CREATE INDEX IF NOT EXISTS ix_docs_usr ON docs(org, uid, usr);
CREATE TABLE IF NOT EXISTS terms (
  org  TEXT NOT NULL,
  uid  TEXT NOT NULL,
  term TEXT NOT NULL ` + d.Bytewise() + `,
  pk   TEXT NOT NULL ` + d.Bytewise() + `,
  PRIMARY KEY (org, uid, term, pk)
) ` + d.Clustered() + `;
CREATE INDEX IF NOT EXISTS ix_terms_pk ON terms(org, uid, pk);
`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("index migrate: %w", err)
		}
		if err := s.adoptLegacyTables(ctx, tx); err != nil {
			return err
		}
		return s.repairTerms(ctx, tx)
	})
}

// repairTerms re-derives the terms of every document holding a term that is not
// valid UTF-8. A file written before clip cut on a rune boundary holds such terms
// (a term cut mid-rune), SQLite keeps them, and PostgreSQL's text refuses them, so
// the store's move to the server failed on the first. Terms are derived data: the
// document is the truth, so its terms are written again from it. Only a file can
// hold one, so the server's copy is not scanned.
func (s *Store) repairTerms(ctx context.Context, tx *sql.Tx) error {
	if s.d.Postgres() {
		return nil
	}
	type key struct{ org, uid, pk string }
	bad := map[key]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT org, uid, term, pk FROM terms`)
	if err != nil {
		return fmt.Errorf("index migrate: read terms: %w", err)
	}
	for rows.Next() {
		var org, uid, term, pk string
		if err := rows.Scan(&org, &uid, &term, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("index migrate: read terms: %w", err)
		}
		if !utf8.ValidString(term) {
			bad[key{org, uid, pk}] = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("index migrate: read terms: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for k := range bad {
		if _, err := tx.ExecContext(ctx, `DELETE FROM terms WHERE org=$1 AND uid=$2 AND pk=$3`, k.org, k.uid, k.pk); err != nil {
			return fmt.Errorf("index migrate: drop terms: %w", err)
		}
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT doc FROM docs WHERE org=$1 AND uid=$2 AND pk=$3`, k.org, k.uid, k.pk).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue // the terms of a document that is gone
		}
		if err != nil {
			return fmt.Errorf("index migrate: read document: %w", err)
		}
		var d map[string]any
		if json.Unmarshal([]byte(raw), &d) != nil {
			continue // a document nothing can read has no terms to derive
		}
		for _, term := range tokenize(searchableText(d)) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO terms(org, uid, term, pk) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, k.org, k.uid, term, k.pk); err != nil {
				return fmt.Errorf("index migrate: derive terms: %w", err)
			}
		}
	}
	return nil
}

// legacyTables maps a table's previous name to its current one. Moving the store
// FILE is not enough when the tables inside it were renamed too: the rows would
// still be there, under names nothing queries, and the index would read as empty
// while every document sat intact one identifier away.
var legacyTables = map[string]string{
	"search_indexes": "indexes",
	"search_docs":    "docs",
	"search_terms":   "terms",
}

// adoptLegacyTables carries rows out of a previous schema's tables and drops
// them. Idempotent: a table that is not there is skipped, and one that is gets
// emptied by the DROP, so a second boot has nothing left to adopt. ON CONFLICT DO
// NOTHING means rows already written under the current names win — the migration
// never overwrites live data with older rows. It runs in the migration's
// transaction, so a store is never half adopted.
func (s *Store) adoptLegacyTables(ctx context.Context, tx *sql.Tx) error {
	for previous, current := range legacyTables {
		has, err := s.d.HasTable(ctx, tx, previous)
		if err != nil {
			return fmt.Errorf("index migrate: look for %s: %w", previous, err)
		}
		if !has {
			continue
		}
		// The column lists are identical across the rename, so an unqualified
		// INSERT…SELECT is exact. WHERE TRUE ends the SELECT so SQLite does not
		// read the ON CONFLICT as the end of a join.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+current+` SELECT * FROM `+previous+` WHERE TRUE ON CONFLICT DO NOTHING`); err != nil {
			return fmt.Errorf("index migrate: adopt %s into %s: %w", previous, current, err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+previous); err != nil {
			return fmt.Errorf("index migrate: drop %s: %w", previous, err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping proves the store is readable, so the health route can fail closed.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ---- indexes --------------------------------------------------------------

// EnsureIndex creates the index for (org, uid) if absent and returns it. An
// empty primaryKey defaults to "id", matching Meilisearch. Idempotent: an
// existing index keeps its original primary key, because changing it would
// silently orphan every document already stored under the old one.
func (s *Store) EnsureIndex(ctx context.Context, org, uid, primaryKey string) (Index, error) {
	// An empty uid is refused rather than stored: it is not addressable through
	// the API, so its documents would be invisible and its registry row could not
	// be dropped by name. The handlers already reject one, so reaching here means
	// a caller inside the process got it wrong.
	if strings.TrimSpace(uid) == "" {
		return Index{}, fmt.Errorf("index: index uid is required")
	}
	if err := dialect.Text(org, uid, primaryKey); err != nil {
		return Index{}, fmt.Errorf("index: an index's name: %w", err)
	}
	if idx, err := s.Index(ctx, org, uid); err == nil {
		return idx, nil
	} else if !errors.Is(err, errNoIndex) {
		return Index{}, err
	}
	if primaryKey == "" {
		primaryKey = "id"
	}
	now := time.Now().Unix()
	// `user` is filterable by default: it is the attribute chat filters
	// conversations and messages by, and the only one the filter grammar below
	// understands.
	filt, _ := json.Marshal([]string{"user"})
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO indexes(org, uid, primary_key, filterable, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (org, uid) DO NOTHING`, org, uid, primaryKey, string(filt), now, now); err != nil {
		return Index{}, fmt.Errorf("index: create index: %w", err)
	}
	return s.Index(ctx, org, uid)
}

// Indexes lists an org's indexes in creation order, newest last, with the
// document count each one holds. It is the org's whole search surface in one
// answer — and the only way to see an index whose uid you do not already know.
func (s *Store) Indexes(ctx context.Context, org string) ([]Index, []int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT i.uid, i.primary_key, i.filterable, i.created_at, i.updated_at,
		        (SELECT COUNT(*) FROM docs d WHERE d.org=i.org AND d.uid=i.uid)
		   FROM indexes i WHERE i.org=$1 ORDER BY i.created_at, i.uid`, org)
	if err != nil {
		return nil, nil, fmt.Errorf("index: list indexes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out, counts := []Index{}, []int{}
	for rows.Next() {
		var (
			idx            Index
			filt           string
			created, upded int64
			n              int
		)
		if err := rows.Scan(&idx.UID, &idx.PrimaryKey, &filt, &created, &upded, &n); err != nil {
			return nil, nil, fmt.Errorf("index: scan index: %w", err)
		}
		_ = json.Unmarshal([]byte(filt), &idx.FilterableAttributes)
		idx.CreatedAt, idx.UpdatedAt = text.Unix(created), text.Unix(upded)
		out, counts = append(out, idx), append(counts, n)
	}
	return out, counts, rows.Err()
}

// Index reads one index descriptor, or errNoIndex.
func (s *Store) Index(ctx context.Context, org, uid string) (Index, error) {
	var (
		idx            Index
		filt           string
		created, upded int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT uid, primary_key, filterable, created_at, updated_at
		   FROM indexes WHERE org=$1 AND uid=$2`, org, uid).
		Scan(&idx.UID, &idx.PrimaryKey, &filt, &created, &upded)
	if errors.Is(err, sql.ErrNoRows) {
		return Index{}, errNoIndex
	}
	if err != nil {
		return Index{}, fmt.Errorf("index: read index: %w", err)
	}
	_ = json.Unmarshal([]byte(filt), &idx.FilterableAttributes)
	idx.CreatedAt = text.Unix(created)
	idx.UpdatedAt = text.Unix(upded)
	return idx, nil
}

// DropIndex removes an index and every document and term in it, in ONE
// transaction so an index can never survive as a registry row with no documents
// or as orphaned documents with no registry row.
func (s *Store) DropIndex(ctx context.Context, org, uid string) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM terms   WHERE org=$1 AND uid=$2`,
		`DELETE FROM docs    WHERE org=$1 AND uid=$2`,
		`DELETE FROM indexes WHERE org=$1 AND uid=$2`,
	} {
		if _, err := tx.ExecContext(ctx, q, org, uid); err != nil {
			return fmt.Errorf("index: drop index %q: %w", uid, err)
		}
	}
	return tx.Commit()
}

// SetFilterable replaces the filterable attribute list for an index.
func (s *Store) SetFilterable(ctx context.Context, org, uid string, attrs []string) error {
	filt, _ := json.Marshal(attrs)
	_, err := s.db.ExecContext(ctx,
		`UPDATE indexes SET filterable=$1, updated_at=$2 WHERE org=$3 AND uid=$4`,
		string(filt), time.Now().Unix(), org, uid)
	return err
}

// ---- documents ------------------------------------------------------------

// Upsert adds or replaces documents in an index. Documents whose primary key is
// missing or empty are skipped, as Meilisearch skips documents it cannot key.
// The whole batch is one transaction, so a failure mid-batch leaves the index
// exactly as it was.
//
// The batch's terms are written in key order, all documents together: terms is
// one b-tree keyed by term, and a document's own terms land all over it, so
// writing them document by document touches a page per term. In order, one page
// takes the run of rows that fall on it — what makes a large file's passages a
// write the index can keep up with.
func (s *Store) Upsert(ctx context.Context, org, uid, primaryKey string, docs []map[string]any) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// A key given twice in one batch is the last document given: the earlier one is
	// replaced before it is read, as it was when each was written in turn, and a
	// statement that upserts one row twice is an error on PostgreSQL.
	type entry struct{ pk, usr, raw string }
	var entries []entry
	at := map[string]int{}
	type row struct{ term, pk string }
	var rows []row
	for _, d := range docs {
		pk, usr := stringify(d[primaryKey]), stringify(d["user"])
		// A key or a user that PostgreSQL cannot keep (a NUL, bytes that are not UTF-8)
		// is a document that cannot be keyed, and is skipped as an empty key is.
		if pk == "" || dialect.Text(pk, usr) != nil {
			continue
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return fmt.Errorf("index: encode document %q: %w", pk, err)
		}
		e := entry{pk, usr, string(raw)}
		if i, ok := at[pk]; ok {
			entries[i] = e
			rows = slices.DeleteFunc(rows, func(r row) bool { return r.pk == pk })
		} else {
			at[pk] = len(entries)
			entries = append(entries, e)
		}
		for _, term := range tokenize(searchableText(d)) {
			rows = append(rows, row{term, pk})
		}
	}
	// The documents go in a statement to a chunk, and so do the deletions of their
	// old terms: a round trip each is what a network between the store and the
	// process makes slow. Drop the previous terms before writing the new ones, so a
	// replaced document can never keep matching text it no longer contains.
	for chunk := range slices.Chunk(entries, docsPerStatement) {
		args := []any{org, uid}
		var values strings.Builder
		for i, e := range chunk {
			if i > 0 {
				values.WriteByte(',')
			}
			args = append(args, e.pk, e.usr, e.raw)
			n := len(args)
			values.WriteString(`($1,$2,$` + strconv.Itoa(n-2) + `,$` + strconv.Itoa(n-1) + `,$` + strconv.Itoa(n) + `)`)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO docs(org, uid, pk, usr, doc) VALUES `+values.String()+`
			 ON CONFLICT(org, uid, pk) DO UPDATE SET usr=excluded.usr, doc=excluded.doc`, args...); err != nil {
			return fmt.Errorf("index: store documents: %w", err)
		}
		pks := make([]any, 0, len(chunk))
		for _, e := range chunk {
			pks = append(pks, e.pk)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM terms WHERE org=$1 AND uid=$2 AND pk IN (`+dialect.Marks(3, len(pks))+`)`,
			append([]any{org, uid}, pks...)...); err != nil {
			return fmt.Errorf("index: reindex documents: %w", err)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].term != rows[j].term {
			return rows[i].term < rows[j].term
		}
		return rows[i].pk < rows[j].pk
	})
	for chunk := range slices.Chunk(rows, termsPerStatement) {
		args := []any{org, uid}
		var values strings.Builder
		for i, r := range chunk {
			if i > 0 {
				values.WriteByte(',')
			}
			args = append(args, r.term, r.pk)
			values.WriteString(`($1,$2,$` + strconv.Itoa(len(args)-1) + `,$` + strconv.Itoa(len(args)) + `)`)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO terms(org, uid, term, pk) VALUES `+values.String()+` ON CONFLICT DO NOTHING`, args...); err != nil {
			return fmt.Errorf("index: index documents: %w", err)
		}
	}
	// A batch this size has made the statistics of both tables out of date, and the
	// range reads of search plan from them.
	if len(rows) >= analyzeAt {
		if err := s.d.Analyze(ctx, tx, "docs", "terms"); err != nil {
			return fmt.Errorf("index: analyze: %w", err)
		}
	}
	return tx.Commit()
}

// analyzeAt is how many terms a batch writes before the planner's statistics are
// refreshed in its transaction (Dialect.Analyze).
const analyzeAt = 5000

// termsPerStatement is how many rows one INSERT of terms carries: a round trip for
// every term of a large file's passages is what a network between the store and the
// process makes slow, and four values a row keeps a statement far under either
// engine's limit on parameters.
const termsPerStatement = 500

// docsPerStatement is the same for documents, three values a row.
const docsPerStatement = 200

// PKs lists an index's primary keys. Reconcile needs the KEYS and not the
// documents to work out what a full-corpus swap must prune; reading the doc
// column to throw it away would pull a whole corpus into memory for a set
// difference.
func (s *Store) PKs(ctx context.Context, org, uid string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pk FROM docs WHERE org=$1 AND uid=$2`, org, uid)
	if err != nil {
		return nil, fmt.Errorf("index: list keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}

// Document reads one stored document verbatim.
func (s *Store) Document(ctx context.Context, org, uid, pk string) (json.RawMessage, error) {
	var doc string
	err := s.db.QueryRowContext(ctx,
		`SELECT doc FROM docs WHERE org=$1 AND uid=$2 AND pk=$3`, org, uid, pk).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("index: read document: %w", err)
	}
	return json.RawMessage(doc), nil
}

// Documents pages an index in primary-key order and reports the total.
func (s *Store) Documents(ctx context.Context, org, uid string, limit, offset int) ([]json.RawMessage, int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT doc FROM docs WHERE org=$1 AND uid=$2 ORDER BY pk LIMIT $3 OFFSET $4`,
		org, uid, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("index: list documents: %w", err)
	}
	out, err := scanDocs(rows)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM docs WHERE org=$1 AND uid=$2`, org, uid).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("index: count documents: %w", err)
	}
	return out, total, nil
}

// Delete removes documents by primary key from both the row store and the FTS
// index. Unknown keys are a no-op, as they are in Meilisearch.
func (s *Store) Delete(ctx context.Context, org, uid string, pks []string) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, pk := range pks {
		if dialect.Text(pk) != nil {
			continue // no such key can be stored, so there is none to delete
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM docs WHERE org=$1 AND uid=$2 AND pk=$3`, org, uid, pk); err != nil {
			return fmt.Errorf("index: delete document %q: %w", pk, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM terms WHERE org=$1 AND uid=$2 AND pk=$3`, org, uid, pk); err != nil {
			return fmt.Errorf("index: deindex document %q: %w", pk, err)
		}
	}
	return tx.Commit()
}

// ---- search ---------------------------------------------------------------

// Search returns the documents matching q, optionally narrowed to a set of
// users. Query terms are OR-joined prefixes, and a document matching MORE of
// them ranks higher — the only ranking signal an inverted index gives for free,
// and enough to put "kubernetes migration" above "kubernetes" for that query.
// Ties break on primary key so paging is stable. An empty q with no users lists
// the index, matching Meilisearch's placeholder search.
func (s *Store) Search(ctx context.Context, org, uid, q string, users []string, limit, offset int) ([]json.RawMessage, error) {
	ranges, err := s.rare(ctx, org, uid, prefixRanges(q))
	if err != nil {
		return nil, err
	}
	query, args := s.searchSQL(org, uid, ranges, users, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("index: query: %w", err)
	}
	return scanDocs(rows)
}

// commonAt is how many index rows a query word may hold and still be read: a
// word past it is in so much of the org's index that it ranks nothing above
// anything and costs a scan of every document it is in.
const commonAt = 20000

// rare keeps the query words that say something: a word fewer than commonAt rows
// hold. A log file's name sits in the path of every one of its passages, so a
// question that names the file read all of them to rank none. Each word is
// weighed by a count that stops at the bound; a query of nothing but common
// words keeps them all.
func (s *Store) rare(ctx context.Context, org, uid string, ranges []prefixRange) ([]prefixRange, error) {
	var keep []prefixRange
	for _, r := range ranges {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM (SELECT 1 FROM terms WHERE org=$1 AND uid=$2 AND term >= $3 AND term < $4 LIMIT $5) w`,
			org, uid, r.lo, r.hi, commonAt+1).Scan(&n); err != nil {
			return nil, fmt.Errorf("index: weigh query: %w", err)
		}
		if n <= commonAt {
			keep = append(keep, r)
		}
	}
	if len(keep) == 0 {
		return ranges, nil
	}
	return keep, nil
}

// searchSQL is Search's statement and its arguments, over the query words'
// ranges. $1 is the org and $2 the uid wherever they appear.
func (s *Store) searchSQL(org, uid string, ranges []prefixRange, users []string, limit, offset int) (string, []any) {
	args := []any{org, uid}
	// The ranges come first in the text, and so first in the arguments; the users
	// are numbered after them, wherever the text puts them.
	var branches []string
	for i, r := range ranges {
		args = append(args, r.lo, r.hi)
		branches = append(branches, `SELECT pk, `+strconv.Itoa(i)+` AS q FROM terms WHERE org=$1 AND uid=$2 AND term >= $`+strconv.Itoa(len(args)-1)+` AND term < $`+strconv.Itoa(len(args)))
	}
	var users_ string
	if len(users) > 0 {
		users_ = ` AND d.usr IN (` + dialect.Marks(len(args)+1, len(users)) + `)`
		for _, u := range users {
			args = append(args, u)
		}
	}
	args = append(args, limit, offset)
	page := ` LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	if len(ranges) == 0 {
		return `SELECT d.doc FROM docs d WHERE d.org=$1 AND d.uid=$2` + users_ + ` ORDER BY d.pk` + page, args
	}
	// One range scan of terms per query word, each its own branch of a UNION
	// ALL so each reads the primary key's range (org, uid, term) and nothing
	// else. As one WHERE with the ranges OR-ed together, SQLite read every term
	// the index holds for the org and tested each one.
	//
	// The matches drive the join (Dialect.Join fixes the order on SQLite) and each
	// fetches its document by primary key. Joined the other way, SQLite walked every
	// document the org's index holds and probed the matches for each, so even a
	// word nothing contains cost seconds in a workspace of large files.
	//
	// A document ranks by how many of the QUERY's words it holds —
	// Meilisearch's first rule, "words" — not by how many of its own terms
	// matched: a log line holds dozens of terms that begin "09", and counting
	// each one put every timestamp-dense line ahead of the one line that named
	// what was asked for.
	return `SELECT d.doc FROM (SELECT pk, COUNT(DISTINCT q) AS matched FROM (` + strings.Join(branches, " UNION ALL ") + `) u GROUP BY pk) m
		` + s.d.Join() + ` docs d ON d.org=$1 AND d.uid=$2 AND d.pk=m.pk
		WHERE TRUE` + users_ + ` ORDER BY m.matched DESC, m.pk` + page, args
}

func scanDocs(rows *sql.Rows) ([]json.RawMessage, error) {
	defer func() { _ = rows.Close() }()
	out := []json.RawMessage{}
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, fmt.Errorf("index: scan document: %w", err)
		}
		out = append(out, json.RawMessage(doc))
	}
	return out, rows.Err()
}

// ---- text extraction ------------------------------------------------------

// maxIndexedText caps the searchable text taken from one document so a single
// oversized field cannot dominate the index.
const maxIndexedText = 64 << 10

// searchableText flattens a document into the text that gets indexed: `title`
// and `text` first (the fields chat searches — a conversation's title, a
// message's flattened content), then every other string leaf so a document is
// findable by any human-readable field it carries.
func searchableText(d map[string]any) string {
	var b strings.Builder
	seen := map[string]bool{}
	write := func(v any) {
		s := stringify(v)
		if s == "" || b.Len()+len(s) > maxIndexedText {
			return
		}
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, k := range []string{"title", "text"} {
		if v, ok := d[k]; ok {
			seen[k] = true
			write(v)
		}
	}
	for k, v := range d {
		if seen[k] {
			continue
		}
		if s, ok := v.(string); ok {
			write(s)
		}
	}
	return strings.TrimSpace(b.String())
}

// tokenize splits text into the terms stored in the inverted index: runs of
// letters, digits and underscore, lowercased and stripped of diacritics so
// "Café" and "cafe" are the same term. Terms longer than maxTerm are truncated
// rather than dropped, keeping them findable by their prefix.
func tokenize(text string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 16)
	for _, f := range strings.FieldsFunc(fold(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		f = clip(f)
		if f == "" {
			continue
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// clip cuts a term to maxTerm bytes on a rune boundary. Cutting at a byte offset
// splits a multi-byte rune and leaves a term that is not UTF-8, which PostgreSQL's
// text refuses. A term holding any other invalid byte is made valid, never kept.
func clip(f string) string {
	if len(f) > maxTerm {
		cut := maxTerm
		for cut > 0 && !utf8.RuneStart(f[cut]) {
			cut--
		}
		f = f[:cut]
	}
	if !utf8.ValidString(f) {
		f = strings.ToValidUTF8(f, "")
	}
	return f
}

// maxTerm bounds a stored term. Longer text is still findable by its prefix.
const maxTerm = 64

// prefixRange is the half-open key range [lo, hi) covering every term starting
// with a prefix, so a prefix query is an index range scan rather than a LIKE
// over the whole table.
type prefixRange struct{ lo, hi string }

// prefixRanges turns free text into one range per query term. It returns nil
// when the query has no usable terms, which callers read as "no text filter" —
// never as "match nothing".
func prefixRanges(q string) []prefixRange {
	terms := tokenize(q)
	if len(terms) == 0 {
		return nil
	}
	out := make([]prefixRange, 0, len(terms))
	for _, t := range terms {
		if hi := successor(t); hi != "" {
			out = append(out, prefixRange{lo: t, hi: hi})
		}
	}
	return out
}

// successor returns the smallest string greater than every string beginning
// with s: s with its final rune incremented. Terms are folded to letters,
// digits and underscore, so the incremented rune is always representable.
func successor(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	r[len(r)-1]++
	return string(r)
}

// fold lowercases text and drops combining marks, so an accented term matches
// its unaccented spelling — the job FTS5's `remove_diacritics 2` tokenizer did.
func fold(s string) string {
	s = strings.ToLower(s)
	if isASCII(s) {
		return s // the common path: nothing to decompose
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// ---- filters --------------------------------------------------------------

var (
	userEqRe = regexp.MustCompile(`(?i)user\s*=\s*"([^"]*)"|user\s*=\s*'([^']*)'`)
	userInRe = regexp.MustCompile(`(?i)user\s+IN\s*\[([^\]]*)\]`)
)

// ParseUserFilter extracts the user id(s) from a Meilisearch filter expression.
// Chat only ever sends `user = "<id>"`, but the array form and `user IN [...]`
// are accepted too. An expression naming no user yields nil, which callers read
// as "no user filter" — never as "match nothing".
func ParseUserFilter(f any) []string {
	switch v := f.(type) {
	case string:
		return userFilter(v)
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, userFilter(s)...)
			}
		}
		return out
	}
	return nil
}

func userFilter(s string) []string {
	if m := userInRe.FindStringSubmatch(s); m != nil {
		var out []string
		for p := range strings.SplitSeq(m[1], ",") {
			if p = strings.Trim(strings.TrimSpace(p), `"'`); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	var out []string
	for _, m := range userEqRe.FindAllStringSubmatch(s, -1) {
		if m[1] != "" {
			out = append(out, m[1])
		} else if m[2] != "" {
			out = append(out, m[2])
		}
	}
	return out
}

// stringify renders a JSON scalar as the text used for a primary key, the user
// filter, or the search index. Numbers keep their exact decimal form so an
// integer key round-trips as "42", never "4.2e+01".
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
