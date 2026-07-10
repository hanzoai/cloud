package code

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Store is one org's code index over a single SQLite file
// ({DataDir}/orgs/{slug}/code.db). Because every org gets its OWN file, the
// tenant boundary is PHYSICAL — a query in one org's store can never reach
// another org's rows. Within the file rows are scoped by repo. MaxOpenConns(1)
// serializes writes against the file lock (the prompts/eval/projects discipline).
type Store struct {
	db *sql.DB
}

var errNotFound = errors.New("code: not found")

// openStore wraps a tenant DB — already opened + pragma'd by cloud.TenantDB — into
// the per-org code index store, running its migration. It is the open func the
// shared cloud.TenantStore cache calls once per org file.
func openStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the five index tiers this store composes. There is NO org
// column: the file IS the tenant. The FTS tables carry chunk_id UNINDEXED so a
// hit joins back to chunks, and their rowid is set to chunks.id so a re-index of
// a file can delete exactly the stale FTS rows.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS blobs (
  hash    TEXT PRIMARY KEY,
  content TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
  repo       TEXT NOT NULL,
  path       TEXT NOT NULL,
  lang       TEXT NOT NULL,
  hash       TEXT NOT NULL,
  size       INTEGER NOT NULL,
  indexed_at INTEGER NOT NULL,
  PRIMARY KEY (repo, path)
);

CREATE TABLE IF NOT EXISTS symbols (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  repo      TEXT NOT NULL,
  path      TEXT NOT NULL,
  name      TEXT NOT NULL,
  kind      TEXT NOT NULL,
  line      INTEGER NOT NULL,
  end_line  INTEGER NOT NULL,
  signature TEXT NOT NULL DEFAULT '',
  scope     TEXT NOT NULL DEFAULT '',
  lang      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_symbols_repo_name ON symbols(repo, name);
CREATE INDEX IF NOT EXISTS ix_symbols_repo_path ON symbols(repo, path);

CREATE TABLE IF NOT EXISTS edges (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  repo        TEXT NOT NULL,
  name        TEXT NOT NULL,
  path        TEXT NOT NULL,
  line        INTEGER NOT NULL,
  from_symbol TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_edges_repo_name ON edges(repo, name);

CREATE TABLE IF NOT EXISTS chunks (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  repo       TEXT NOT NULL,
  path       TEXT NOT NULL,
  start_line INTEGER NOT NULL,
  end_line   INTEGER NOT NULL,
  symbol     TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT '',
  hash       TEXT NOT NULL,
  lang       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_chunks_repo_path ON chunks(repo, path);

CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(body, chunk_id UNINDEXED, tokenize='trigram');

CREATE TABLE IF NOT EXISTS vectors (
  chunk_id INTEGER PRIMARY KEY,
  dim      INTEGER NOT NULL,
  vec      BLOB NOT NULL
);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ── file lifecycle (incremental indexing) ────────────────────────────────────

// fileHash returns the recorded content hash for (repo,path), or "" if the file
// has never been indexed. The indexer skips a file whose hash is unchanged.
func (s *Store) fileHash(ctx context.Context, repo, path string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM files WHERE repo=? AND path=?`, repo, path).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("file hash: %w", err)
	}
	return h, nil
}

// listFilePaths returns every indexed path under repo (for prune).
func (s *Store) listFilePaths(ctx context.Context, repo string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM files WHERE repo=?`, repo)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// deleteFile removes every artifact of one file across all tiers, in one
// transaction. Called before re-indexing a changed file and by prune.
func (s *Store) deleteFile(ctx context.Context, repo, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := deleteFileTx(ctx, tx, repo, path); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteFileTx(ctx context.Context, tx *sql.Tx, repo, path string) error {
	// Drop FTS rows first: their rowid == chunks.id, so gather chunk ids before
	// the chunk rows go.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE repo=? AND path=?`, repo, path)
	if err != nil {
		return fmt.Errorf("chunk ids: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM fts WHERE rowid=?`, id); err != nil {
			return fmt.Errorf("delete fts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM vectors WHERE chunk_id=?`, id); err != nil {
			return fmt.Errorf("delete vector: %w", err)
		}
	}
	for _, t := range []string{"chunks", "symbols", "edges", "files"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE repo=? AND path=?`, repo, path); err != nil {
			return fmt.Errorf("delete %s: %w", t, err)
		}
	}
	return nil
}

// writeFile replaces one file's artifacts atomically: it deletes any prior rows,
// then inserts the file record, symbols, edges, and chunks (with their FTS rows
// and, when present, embeddings). vecs is aligned 1:1 with p.Chunks (a nil entry
// means "no embedding" — semantic disabled or embed failed for that chunk).
func (s *Store) writeFile(ctx context.Context, repo, path string, size int64, hash string, indexedAt int64, p Parsed, vecs [][]float32) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := deleteFileTx(ctx, tx, repo, path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO files (repo,path,lang,hash,size,indexed_at) VALUES (?,?,?,?,?,?)`,
		repo, path, p.Lang, hash, size, indexedAt); err != nil {
		return fmt.Errorf("insert file: %w", err)
	}
	for _, sym := range p.Symbols {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO symbols (repo,path,name,kind,line,end_line,signature,scope,lang) VALUES (?,?,?,?,?,?,?,?,?)`,
			repo, path, sym.Name, sym.Kind, sym.Line, sym.EndLine, sym.Signature, sym.Scope, p.Lang); err != nil {
			return fmt.Errorf("insert symbol: %w", err)
		}
	}
	for _, e := range p.Edges {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO edges (repo,name,path,line,from_symbol) VALUES (?,?,?,?,?)`,
			repo, e.Name, path, e.Line, e.From); err != nil {
			return fmt.Errorf("insert edge: %w", err)
		}
	}
	for i, ch := range p.Chunks {
		chunkHash := sha256Hex(ch.Text)
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO blobs (hash,content) VALUES (?,?)`, chunkHash, ch.Text); err != nil {
			return fmt.Errorf("insert blob: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO chunks (repo,path,start_line,end_line,symbol,kind,hash,lang) VALUES (?,?,?,?,?,?,?,?)`,
			repo, path, ch.StartLine, ch.EndLine, ch.Symbol, ch.Kind, chunkHash, p.Lang)
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
		chunkID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("chunk id: %w", err)
		}
		// FTS row: rowid pinned to chunk id so deleteFile can target it. body is the
		// code-tokenized text so both split terms (getUserName→get user name) and the
		// original identifiers are substring-searchable via the trigram tokenizer.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO fts (rowid,body,chunk_id) VALUES (?,?,?)`,
			chunkID, ftsBody(ch.Text), chunkID); err != nil {
			return fmt.Errorf("insert fts: %w", err)
		}
		if i < len(vecs) && len(vecs[i]) > 0 {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO vectors (chunk_id,dim,vec) VALUES (?,?,?)`,
				chunkID, len(vecs[i]), encodeVec(vecs[i])); err != nil {
				return fmt.Errorf("insert vector: %w", err)
			}
		}
	}
	return tx.Commit()
}

// ── retrieval reads ──────────────────────────────────────────────────────────

// chunkRow is a chunk joined with its raw text, the unit every tier returns.
type chunkRow struct {
	ID        int64
	Repo      string
	Path      string
	StartLine int
	EndLine   int
	Symbol    string
	Kind      string
	Lang      string
	Text      string
	Score     float64
}

const chunkSelect = `SELECT c.id,c.repo,c.path,c.start_line,c.end_line,c.symbol,c.kind,c.lang,b.content
FROM chunks c JOIN blobs b ON b.hash=c.hash`

func scanChunk(sc interface{ Scan(...any) error }) (chunkRow, error) {
	var r chunkRow
	err := sc.Scan(&r.ID, &r.Repo, &r.Path, &r.StartLine, &r.EndLine, &r.Symbol, &r.Kind, &r.Lang, &r.Text)
	return r, err
}

// getChunk loads one chunk by id (context packing follows symbol/edge references
// back to their defining chunk).
func (s *Store) getChunk(ctx context.Context, id int64) (chunkRow, error) {
	row := s.db.QueryRowContext(ctx, chunkSelect+` WHERE c.id=?`, id)
	r, err := scanChunk(row)
	if errors.Is(err, sql.ErrNoRows) {
		return chunkRow{}, errNotFound
	}
	if err != nil {
		return chunkRow{}, fmt.Errorf("get chunk: %w", err)
	}
	return r, nil
}

// ftsSearch runs the trigram MATCH and returns chunk rows ordered by bm25 (best
// first). repo=="" searches every repo in the org's store.
func (s *Store) ftsSearch(ctx context.Context, repo, match string, limit int) ([]chunkRow, error) {
	q := chunkSelect + ` JOIN fts ON fts.rowid=c.id WHERE fts MATCH ?`
	args := []any{match}
	if repo != "" {
		q += ` AND c.repo=?`
		args = append(args, repo)
	}
	q += ` ORDER BY bm25(fts) LIMIT ?`
	args = append(args, limit)
	return s.queryChunks(ctx, q, args...)
}

// scanChunks loads chunk rows for regex candidate scanning (repo-scoped, bounded).
func (s *Store) scanChunks(ctx context.Context, repo string, limit int) ([]chunkRow, error) {
	q := chunkSelect
	var args []any
	if repo != "" {
		q += ` WHERE c.repo=?`
		args = append(args, repo)
	}
	q += ` LIMIT ?`
	args = append(args, limit)
	return s.queryChunks(ctx, q, args...)
}

func (s *Store) queryChunks(ctx context.Context, q string, args ...any) ([]chunkRow, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []chunkRow
	for rows.Next() {
		r, err := scanChunk(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// symbolSearch resolves go-to-symbol: exact name first, then prefix, then a
// code-token match (so "user name" finds getUserName). Returns symbols with their
// defining chunk id when one exists.
func (s *Store) symbolSearch(ctx context.Context, repo, query string, limit int) ([]Symbol, error) {
	like := escapeLike(query) + "%"
	q := `SELECT repo,path,name,kind,line,end_line,signature,scope,lang FROM symbols WHERE `
	var conds []string
	var args []any
	conds = append(conds, `(name=? OR name LIKE ? ESCAPE '\')`)
	args = append(args, query, like)
	if repo != "" {
		conds = append(conds, `repo=?`)
		args = append(args, repo)
	}
	q += strings.Join(conds, " AND ")
	// Exact matches rank ahead of prefix matches; then shorter names (closer match).
	q += ` ORDER BY (name=?) DESC, length(name) ASC LIMIT ?`
	args = append(args, query, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("symbol search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Symbol
	for rows.Next() {
		var sym Symbol
		if err := rows.Scan(&sym.Repo, &sym.Path, &sym.Name, &sym.Kind, &sym.Line, &sym.EndLine, &sym.Signature, &sym.Scope, &sym.Lang); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}

// lookupSymbol returns the single best definition for an exact symbol name — used
// by context packing to attach a referenced symbol's def.
func (s *Store) lookupSymbol(ctx context.Context, repo, name string) (Symbol, bool) {
	q := `SELECT repo,path,name,kind,line,end_line,signature,scope,lang FROM symbols WHERE name=?`
	args := []any{name}
	if repo != "" {
		q += ` AND repo=?`
		args = append(args, repo)
	}
	q += ` ORDER BY length(name) ASC LIMIT 1`
	var sym Symbol
	err := s.db.QueryRowContext(ctx, q, args...).Scan(
		&sym.Repo, &sym.Path, &sym.Name, &sym.Kind, &sym.Line, &sym.EndLine, &sym.Signature, &sym.Scope, &sym.Lang)
	if err != nil {
		return Symbol{}, false
	}
	return sym, true
}

// callersOf returns the distinct enclosing symbols that reference name (the "key
// callers" a context bundle attaches). Bounded by limit.
func (s *Store) callersOf(ctx context.Context, repo, name string, limit int) ([]Edge, error) {
	q := `SELECT DISTINCT name,path,line,from_symbol FROM edges WHERE name=? AND from_symbol!=''`
	args := []any{name}
	if repo != "" {
		q += ` AND repo=?`
		args = append(args, repo)
	}
	q += ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("callers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.Name, &e.Path, &e.Line, &e.From); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// calleesOf returns the distinct symbol names that `from` references (the edges
// OUT of a symbol), so context packing can attach the definitions a match calls.
func (s *Store) calleesOf(ctx context.Context, repo, from string, limit int) ([]string, error) {
	// A seed's symbol is the bare name ("Hello") but a Go method's edges store the
	// qualified caller ("Greeter.Hello"), so match either the exact name or a
	// "<scope>.<name>" suffix.
	q := `SELECT DISTINCT name FROM edges WHERE (from_symbol=? OR from_symbol LIKE ? ESCAPE '\')`
	args := []any{from, "%." + escapeLike(from)}
	if repo != "" {
		q += ` AND repo=?`
		args = append(args, repo)
	}
	q += ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("callees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// chunkForSymbol finds the chunk that contains a symbol's definition line, so a
// symbol hit fuses on the same key as a lexical/semantic hit for the same code.
func (s *Store) chunkForSymbol(ctx context.Context, sym Symbol) (chunkRow, bool) {
	row := s.db.QueryRowContext(ctx,
		chunkSelect+` WHERE c.repo=? AND c.path=? AND c.start_line<=? AND c.end_line>=? ORDER BY (c.end_line-c.start_line) ASC LIMIT 1`,
		sym.Repo, sym.Path, sym.Line, sym.Line)
	r, err := scanChunk(row)
	if err != nil {
		return chunkRow{}, false
	}
	r.Symbol = sym.Name
	r.Kind = sym.Kind
	return r, true
}

// loadVectors streams every (chunk_id, vec) for repo so the semantic tier can
// brute-force cosine in Go. Bounded by max to cap memory; codebase-scale KNN over
// float32 is a few ms and needs no ANN index for the MVP. The vectors table is a
// schema-compatible seam for a future sqlite-vec `vec0` KNN.
func (s *Store) loadVectors(ctx context.Context, repo string, max int) ([]int64, [][]float32, error) {
	q := `SELECT v.chunk_id,v.vec FROM vectors v`
	var args []any
	if repo != "" {
		q += ` JOIN chunks c ON c.id=v.chunk_id WHERE c.repo=?`
		args = append(args, repo)
	}
	q += ` LIMIT ?`
	args = append(args, max)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("load vectors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	var vecs [][]float32
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		vecs = append(vecs, decodeVec(raw))
	}
	return ids, vecs, rows.Err()
}

// stats reports the store's footprint for the index response.
func (s *Store) stats(ctx context.Context, repo string) (files, symbols, chunks, vectors int) {
	count := func(table string) int {
		var n int
		q := `SELECT COUNT(*) FROM ` + table
		var args []any
		if repo != "" {
			switch table {
			case "vectors":
				q = `SELECT COUNT(*) FROM vectors v JOIN chunks c ON c.id=v.chunk_id WHERE c.repo=?`
			default:
				q += ` WHERE repo=?`
			}
			args = append(args, repo)
		}
		_ = s.db.QueryRowContext(ctx, q, args...).Scan(&n)
		return n
	}
	return count("files"), count("symbols"), count("chunks"), count("vectors")
}

// ── vector codec + cosine ────────────────────────────────────────────────────

func encodeVec(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// cosine returns the cosine similarity of two equal-length vectors, or 0 when
// either is degenerate / mismatched (never NaN into a ranking).
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
