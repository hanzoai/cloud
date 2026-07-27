package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	// cek opens the store encrypted at rest (migrate-on-open + shred).
	"github.com/hanzoai/cloud/cek"
	// The ONE "sqlite" driver, kept registered for cek's no-key plaintext fallback.
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

// Store is the index database. ONE SQLite file ({DataDir}/index.db) holds
// every org's indexes and documents; tenant isolation is the `org` column,
// enforced on EVERY query — the same storage shape clients/ads and clients/crm
// use. MaxOpenConns(1) serializes writes against the single-writer file.
//
// Three tables, not one pair per index: a standalone Meilisearch mints a
// directory per index, but SQLite has no reason to. `org` and `uid` are ordinary
// columns, so a new index is a row, never DDL — which is what lets an untrusted
// uid be stored verbatim instead of sanitized into a table name.
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
// in every build lane and costs nothing but rows.
type Store struct {
	db *sql.DB
}

// migrateStore carries a store written under a previous name over to path.
//
// It moves the WHOLE family, not just the database: cek keeps the wrapped data
// key beside the file as "<path>.dek", so renaming the .db alone would leave the
// old key stranded, cek would mint a fresh one, and every existing document
// would be undecryptable — data loss that looks like an empty index. The -wal
// and -shm sidecars carry committed pages that have not been checkpointed yet,
// so they move too.
//
// One-time and idempotent: it acts only when the previous store exists and the
// current one does not. When BOTH exist it leaves them alone — merging two
// encrypted stores is not something to guess at.
func migrateStore(dir, previous, current string) error {
	old, cur := filepath.Join(dir, previous), filepath.Join(dir, current)
	if _, err := os.Stat(old); err != nil {
		return nil // fresh deployment, or already carried over
	}
	if _, err := os.Stat(cur); err == nil {
		return nil
	}
	for _, suffix := range []string{"", "-wal", "-shm", ".dek"} {
		src, dst := old+suffix, cur+suffix
		if _, err := os.Stat(src); err != nil {
			continue // not every sidecar exists at every moment
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("index: carry %s over to %s: %w", src, dst, err)
		}
	}
	return nil
}

func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the index registry, the document row store, and the inverted
// index over the extracted searchable text. Idempotent (IF NOT EXISTS).
//
// terms is ordered (org, uid, term, pk) so a prefix query reads one
// contiguous run of the primary key rather than scanning; the secondary index
// on (org, uid, pk) is what makes re-indexing one document cheap, since a
// replace deletes that document's terms before writing the new ones.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS indexes (
  org         TEXT NOT NULL,
  uid         TEXT NOT NULL,
  primary_key TEXT NOT NULL,
  filterable  TEXT NOT NULL DEFAULT '[]',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (org, uid)
);
CREATE TABLE IF NOT EXISTS docs (
  org TEXT NOT NULL,
  uid TEXT NOT NULL,
  pk  TEXT NOT NULL,
  usr TEXT NOT NULL DEFAULT '',
  doc TEXT NOT NULL,
  PRIMARY KEY (org, uid, pk)
);
CREATE INDEX IF NOT EXISTS ix_docs_usr ON docs(org, uid, usr);
CREATE TABLE IF NOT EXISTS terms (
  org  TEXT NOT NULL,
  uid  TEXT NOT NULL,
  term TEXT NOT NULL,
  pk   TEXT NOT NULL,
  PRIMARY KEY (org, uid, term, pk)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_terms_pk ON terms(org, uid, pk);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("index migrate: %w", err)
	}
	return s.adoptLegacyTables()
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
// emptied by the DROP, so a second boot has nothing left to adopt. INSERT OR
// IGNORE means rows already written under the current names win — the migration
// never overwrites live data with older rows.
func (s *Store) adoptLegacyTables() error {
	for previous, current := range legacyTables {
		var name string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, previous).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("index migrate: look for %s: %w", previous, err)
		}
		// The column lists are identical across the rename, so an unqualified
		// INSERT…SELECT is exact.
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO ` + current + ` SELECT * FROM ` + previous); err != nil {
			return fmt.Errorf("index migrate: adopt %s into %s: %w", previous, current, err)
		}
		if _, err := s.db.Exec(`DROP TABLE ` + previous); err != nil {
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
		`INSERT OR IGNORE INTO indexes(org, uid, primary_key, filterable, created_at, updated_at)
		 VALUES(?,?,?,?,?,?)`, org, uid, primaryKey, string(filt), now, now); err != nil {
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
		   FROM indexes i WHERE i.org=? ORDER BY i.created_at, i.uid`, org)
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
		idx.CreatedAt, idx.UpdatedAt = rfc3339(created), rfc3339(upded)
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
		   FROM indexes WHERE org=? AND uid=?`, org, uid).
		Scan(&idx.UID, &idx.PrimaryKey, &filt, &created, &upded)
	if errors.Is(err, sql.ErrNoRows) {
		return Index{}, errNoIndex
	}
	if err != nil {
		return Index{}, fmt.Errorf("index: read index: %w", err)
	}
	_ = json.Unmarshal([]byte(filt), &idx.FilterableAttributes)
	idx.CreatedAt = rfc3339(created)
	idx.UpdatedAt = rfc3339(upded)
	return idx, nil
}

// DropIndex removes an index and every document and term in it, in ONE
// transaction so an index can never survive as a registry row with no documents
// or as orphaned documents with no registry row.
func (s *Store) DropIndex(ctx context.Context, org, uid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM terms   WHERE org=? AND uid=?`,
		`DELETE FROM docs    WHERE org=? AND uid=?`,
		`DELETE FROM indexes WHERE org=? AND uid=?`,
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
		`UPDATE indexes SET filterable=?, updated_at=? WHERE org=? AND uid=?`,
		string(filt), time.Now().Unix(), org, uid)
	return err
}

// ---- documents ------------------------------------------------------------

// Upsert adds or replaces documents in an index. Documents whose primary key is
// missing or empty are skipped, as Meilisearch skips documents it cannot key.
// The whole batch is one transaction, so a failure mid-batch leaves the index
// exactly as it was.
func (s *Store) Upsert(ctx context.Context, org, uid, primaryKey string, docs []map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, d := range docs {
		pk := stringify(d[primaryKey])
		if pk == "" {
			continue
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return fmt.Errorf("index: encode document %q: %w", pk, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO docs(org, uid, pk, usr, doc) VALUES(?,?,?,?,?)
			 ON CONFLICT(org, uid, pk) DO UPDATE SET usr=excluded.usr, doc=excluded.doc`,
			org, uid, pk, stringify(d["user"]), string(raw)); err != nil {
			return fmt.Errorf("index: store document %q: %w", pk, err)
		}
		// Drop the previous terms before writing the new ones, so a replaced
		// document can never keep matching text it no longer contains.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM terms WHERE org=? AND uid=? AND pk=?`, org, uid, pk); err != nil {
			return fmt.Errorf("index: reindex document %q: %w", pk, err)
		}
		for _, term := range tokenize(searchableText(d)) {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO terms(org, uid, term, pk) VALUES(?,?,?,?)`,
				org, uid, term, pk); err != nil {
				return fmt.Errorf("index: index document %q: %w", pk, err)
			}
		}
	}
	return tx.Commit()
}

// Document reads one stored document verbatim.
func (s *Store) Document(ctx context.Context, org, uid, pk string) (json.RawMessage, error) {
	var doc string
	err := s.db.QueryRowContext(ctx,
		`SELECT doc FROM docs WHERE org=? AND uid=? AND pk=?`, org, uid, pk).Scan(&doc)
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
		`SELECT doc FROM docs WHERE org=? AND uid=? ORDER BY pk LIMIT ? OFFSET ?`,
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
		`SELECT COUNT(*) FROM docs WHERE org=? AND uid=?`, org, uid).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("index: count documents: %w", err)
	}
	return out, total, nil
}

// Delete removes documents by primary key from both the row store and the FTS
// index. Unknown keys are a no-op, as they are in Meilisearch.
func (s *Store) Delete(ctx context.Context, org, uid string, pks []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, pk := range pks {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM docs WHERE org=? AND uid=? AND pk=?`, org, uid, pk); err != nil {
			return fmt.Errorf("index: delete document %q: %w", pk, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM terms WHERE org=? AND uid=? AND pk=?`, org, uid, pk); err != nil {
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
	args := []any{org, uid}
	where := []string{`d.org=?`, `d.uid=?`}
	join, order := "", `ORDER BY d.pk`

	if ranges := prefixRanges(q); len(ranges) > 0 {
		// One range scan of terms per query term, unioned by the GROUP BY.
		// The subquery is joined rather than used as IN (…) so its match count is
		// available to ORDER BY.
		clauses := make([]string, len(ranges))
		matchArgs := []any{org, uid}
		for i, r := range ranges {
			clauses[i] = `(term >= ? AND term < ?)`
			matchArgs = append(matchArgs, r.lo, r.hi)
		}
		join = `JOIN (SELECT pk, COUNT(*) AS matched FROM terms
		               WHERE org=? AND uid=? AND (` + strings.Join(clauses, " OR ") + `)
		               GROUP BY pk) m ON m.pk = d.pk`
		// The join's parameters bind BEFORE the outer WHERE's, matching SQL order.
		args = append(matchArgs, args...)
		order = `ORDER BY m.matched DESC, d.pk`
	}
	if len(users) > 0 {
		ph := make([]string, len(users))
		for i, u := range users {
			ph[i] = "?"
			args = append(args, u)
		}
		where = append(where, `d.usr IN (`+strings.Join(ph, ",")+`)`)
	}
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx,
		`SELECT d.doc FROM docs d `+join+` WHERE `+strings.Join(where, " AND ")+
			` `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("index: query: %w", err)
	}
	return scanDocs(rows)
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
		if len(f) > maxTerm {
			f = f[:maxTerm]
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
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
		for _, p := range strings.Split(m[1], ",") {
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

// ---- helpers --------------------------------------------------------------

func rfc3339(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.RFC3339) }

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
