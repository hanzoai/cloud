package code

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// Store is one org's code index over a single database: a SQLite file
// ({DataDir}/orgs/{slug}/code.db), or on hanzo-sql the org's own schema. Because
// every org gets its OWN database, the org boundary is PHYSICAL — a query in one
// org's store can never reach another org's rows. Within it rows are scoped by
// repo. Its SQL is the dialect both engines share (sdk/dialect), and its
// tests run on both (internal/sqltest).
type Store struct {
	db *sql.DB
	d  dialect.Dialect
	// grams is the substring index of the chunks' code-tokenized text: FTS5 trigram
	// on SQLite, pg_trgm on PostgreSQL. Its key is a chunk's id.
	grams dialect.Grams
}

var errNotFound = errors.New("code: not found")

// codeIndex is the org's vectors as one index with one space (internal/vec):
// recorded by the first vector written, never changed in place, and asked by
// every write and every query. Held tells a store written before spaces were
// recorded from an empty one.
var codeIndex = vec.Index{Name: "code", Held: `SELECT 1 FROM vectors LIMIT 1`}

// openStore wraps an org DB — already opened + pragma'd by cloud.OrgDB — into
// the per-org code index store, running its migration. It is the open func the
// shared cloud.OrgStore cache calls once per org file.
func openStore(db *sql.DB) (*Store, error) {
	d := dialect.Of(db)
	grams, err := d.Grams("fts")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db, d: d, grams: grams}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the five index tiers this store composes, in one transaction under
// the store's lock. There is NO org column: the database IS the org. The substring
// index is keyed by chunks.id so a re-index of a file can delete exactly the stale
// rows. `path` is Bytewise because the tree lists files in the order of their bytes.
func (s *Store) migrate() error {
	d := s.d
	ddl := `
CREATE TABLE IF NOT EXISTS blobs (
  hash    TEXT PRIMARY KEY,
  content TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
  repo       TEXT NOT NULL,
  path       TEXT NOT NULL ` + d.Bytewise() + `,
  lang       TEXT NOT NULL,
  hash       TEXT NOT NULL,
  size       BIGINT NOT NULL,
  indexed_at BIGINT NOT NULL,
  PRIMARY KEY (repo, path)
);

CREATE TABLE IF NOT EXISTS symbols (
  id        ` + d.Identity() + `,
  repo      TEXT NOT NULL,
  path      TEXT NOT NULL,
  name      TEXT NOT NULL,
  kind      TEXT NOT NULL,
  line      BIGINT NOT NULL,
  end_line  BIGINT NOT NULL,
  signature TEXT NOT NULL DEFAULT '',
  scope     TEXT NOT NULL DEFAULT '',
  lang      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_symbols_repo_name ON symbols(repo, name);
CREATE INDEX IF NOT EXISTS ix_symbols_repo_path ON symbols(repo, path);

CREATE TABLE IF NOT EXISTS edges (
  id          ` + d.Identity() + `,
  repo        TEXT NOT NULL,
  name        TEXT NOT NULL,
  path        TEXT NOT NULL,
  line        BIGINT NOT NULL,
  from_symbol TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_edges_repo_name ON edges(repo, name);

CREATE TABLE IF NOT EXISTS chunks (
  id         ` + d.Identity() + `,
  repo       TEXT NOT NULL,
  path       TEXT NOT NULL,
  start_line BIGINT NOT NULL,
  end_line   BIGINT NOT NULL,
  symbol     TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT '',
  hash       TEXT NOT NULL,
  lang       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_chunks_repo_path ON chunks(repo, path);

CREATE TABLE IF NOT EXISTS vectors (
  chunk_id BIGINT PRIMARY KEY,
  dim      BIGINT NOT NULL,
  vec      ` + d.Blob() + ` NOT NULL
);
` + vec.SpaceDDL
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
		if !d.Postgres() {
			if err := scrubBlobs(ctx, tx); err != nil {
				return err
			}
		}
		return s.grams.Create(ctx, tx)
	}); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// scrubBlobs repairs pre-dialect SQLite blob rows whose content holds
// invalid UTF-8 or NUL bytes so PostgreSQL's text type accepts them on move.
func scrubBlobs(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT hash, content FROM blobs`)
	if err != nil {
		return err
	}
	type fix struct{ hash, content string }
	var fixes []fix
	for rows.Next() {
		var f fix
		if err := rows.Scan(&f.hash, &f.content); err != nil {
			_ = rows.Close()
			return err
		}
		if dialect.Text(f.content) != nil {
			f.content = strings.ToValidUTF8(strings.ReplaceAll(f.content, "\x00", ""), "\uFFFD")
			fixes = append(fixes, f)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, f := range fixes {
		if _, err := tx.ExecContext(ctx, `UPDATE blobs SET content=$1 WHERE hash=$2`, f.content, f.hash); err != nil {
			return err
		}
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
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM files WHERE repo=$1 AND path=$2`, repo, path).Scan(&h)
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
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM files WHERE repo=$1`, repo)
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

// bareRepo returns the repository name without an "owner/" prefix so queries
// against an org's store match whether the repo was indexed as "cloud" or
// "hanzoai/cloud".
func bareRepo(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 && i+1 < len(repo) {
		return repo[i+1:]
	}
	return repo
}

// repoCond builds a SQL predicate matching repo in either its given form, its
// bare name ("cloud"), or any "<owner>/<bare>" spelling ("hanzoai/cloud") inside
// the org's store. It binds three arguments, numbered from first, and they are
// case-folded for the LIKE as SQLite's LIKE folds them on its own.
func repoCond(col, repo string, first int) (string, []any) {
	bare := bareRepo(repo)
	n := strconv.Itoa(first)
	return `(` + col + `=$` + n + ` OR ` + col + `=$` + strconv.Itoa(first+1) + ` OR lower(` + col + `) LIKE lower($` + strconv.Itoa(first+2) + `) ESCAPE '\')`,
		[]any{repo, bare, "%/" + escapeLike(bare)}
}

// fileContent returns the INDEXED content of one file: its symbol/definition
// chunks joined in line order. This is the material the search tiers hold, not a
// byte-verbatim file — inter-symbol lines (imports, package clause, bare consts a
// parser did not chunk) are absent. It is a fast "show me the code the index knows
// about" for context, NOT the source of record. The verbatim file (all bytes, and
// every historical version + blame) lives in the S3-backed git object plane
// (clients/git); route read_file/blame there when byte-fidelity matters. Returns
// ("","",nil) for an unknown (repo,path) so the handler can 404.
func (s *Store) fileContent(ctx context.Context, repo, path string) (string, string, error) {
	// lang comes from the files row; content from the chunk blobs.
	var lang string
	rc, rargs := repoCond("repo", repo, 1)
	err := s.db.QueryRowContext(ctx, `SELECT lang FROM files WHERE `+rc+` AND path=$4 LIMIT 1`, append(rargs, path)...).Scan(&lang)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("file lang: %w", err)
	}
	crc, crargs := repoCond("c.repo", repo, 1)
	rows, err := s.db.QueryContext(ctx,
		`SELECT b.content FROM chunks c JOIN blobs b ON b.hash=c.hash
		 WHERE `+crc+` AND c.path=$4 ORDER BY c.start_line`, append(crargs, path)...)
	if err != nil {
		return "", "", fmt.Errorf("file chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var part string
		if err := rows.Scan(&part); err != nil {
			return "", "", err
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(part)
	}
	return b.String(), lang, rows.Err()
}

// TreeEntry is one file in a repo's structure: its path, language, and how many
// top-level symbols it defines — enough for an agent to grasp module layout.
type TreeEntry struct {
	// Path is the file, relative to the repo root. The list is ordered by it, so a
	// reader can see module layout without sorting.
	Path string `json:"path"`
	// Lang is the language the indexer parsed the file as ("go", "python", …), or
	// empty when it recognised none — in which case Symbols is 0 because nothing was
	// extracted, not because the file declares nothing.
	Lang string `json:"lang"`
	// Symbols is how many top-level declarations the file defines. A file with none
	// is still listed: the file set is the authority here and the counts decorate it.
	Symbols int `json:"symbols"`
}

// tree returns the repo's file structure with per-file symbol counts, ordered by
// path — the get_repo_structure primitive. One query per tier, joined in Go: the
// files list is authoritative; symbol counts decorate it (a file with no symbols
// still appears).
func (s *Store) tree(ctx context.Context, repo string) ([]TreeEntry, error) {
	rc, rargs := repoCond("repo", repo, 1)
	rows, err := s.db.QueryContext(ctx, `SELECT path, lang FROM files WHERE `+rc+` ORDER BY path`, rargs...)
	if err != nil {
		return nil, fmt.Errorf("tree files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TreeEntry
	idx := map[string]int{}
	for rows.Next() {
		var e TreeEntry
		if err := rows.Scan(&e.Path, &e.Lang); err != nil {
			return nil, err
		}
		if _, dup := idx[e.Path]; dup {
			continue
		}
		idx[e.Path] = len(out)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Decorate with symbol counts in one pass.
	crows, err := s.db.QueryContext(ctx, `SELECT path, COUNT(*) FROM symbols WHERE `+rc+` GROUP BY path`, rargs...)
	if err != nil {
		return nil, fmt.Errorf("tree symbols: %w", err)
	}
	defer func() { _ = crows.Close() }()
	for crows.Next() {
		var p string
		var n int
		if err := crows.Scan(&p, &n); err != nil {
			return nil, err
		}
		if i, ok := idx[p]; ok {
			out[i].Symbols = n
		}
	}
	return out, crows.Err()
}

// deleteFile removes every artifact of one file across all tiers, in one
// transaction. Called before re-indexing a changed file and by prune.
func (s *Store) deleteFile(ctx context.Context, repo, path string) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.deleteFileTx(ctx, tx, repo, path); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) deleteFileTx(ctx context.Context, tx *sql.Tx, repo, path string) error {
	// Drop the substring rows first: their key is chunks.id, so gather chunk ids
	// before the chunk rows go.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE repo=$1 AND path=$2`, repo, path)
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
		if err := s.grams.Delete(ctx, tx, id); err != nil {
			return fmt.Errorf("delete fts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM vectors WHERE chunk_id=$1`, id); err != nil {
			return fmt.Errorf("delete vector: %w", err)
		}
	}
	for _, t := range []string{"chunks", "symbols", "edges", "files"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE repo=$1 AND path=$2`, repo, path); err != nil {
			return fmt.Errorf("delete %s: %w", t, err)
		}
	}
	return nil
}

// writeFile replaces one file's artifacts atomically: it deletes any prior rows,
// then inserts the file record, symbols, edges, and chunks (with their FTS rows
// and, when present, embeddings). vecs is aligned 1:1 with p.Chunks (a nil entry
// means "no embedding" — semantic disabled or embed failed for that chunk), and
// is of space sp, which the org's index must have been created in or is created
// in by this write. Vectors of another space write nothing at all: the refusal
// is a *vec.MixedError, and the caller writes the file again without them.
func (s *Store) writeFile(ctx context.Context, repo, path string, size int64, hash string, indexedAt int64, p Parsed, sp vec.Space, vecs [][]float32) error {
	var held [][]float32
	for i, v := range vecs {
		if i < len(p.Chunks) && len(v) > 0 {
			held = append(held, v)
		}
	}
	sp, err := sp.Of(held...)
	if err != nil {
		return fmt.Errorf("vectors of %s: %w", path, err)
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if len(held) > 0 {
		if err := codeIndex.Claim(ctx, tx, sp); err != nil {
			return fmt.Errorf("vectors of %s: %w", path, err)
		}
	}
	if err := s.deleteFileTx(ctx, tx, repo, path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO files (repo,path,lang,hash,size,indexed_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		repo, path, p.Lang, hash, size, indexedAt); err != nil {
		return fmt.Errorf("insert file: %w", err)
	}
	for _, sym := range p.Symbols {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO symbols (repo,path,name,kind,line,end_line,signature,scope,lang) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			repo, path, sym.Name, sym.Kind, sym.Line, sym.EndLine, sym.Signature, sym.Scope, p.Lang); err != nil {
			return fmt.Errorf("insert symbol: %w", err)
		}
	}
	for _, e := range p.Edges {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO edges (repo,name,path,line,from_symbol) VALUES ($1,$2,$3,$4,$5)`,
			repo, e.Name, path, e.Line, e.From); err != nil {
			return fmt.Errorf("insert edge: %w", err)
		}
	}
	for i, ch := range p.Chunks {
		chunkHash := sha256Hex(ch.Text)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO blobs (hash,content) VALUES ($1,$2) ON CONFLICT (hash) DO NOTHING`, chunkHash, ch.Text); err != nil {
			return fmt.Errorf("insert blob: %w", err)
		}
		var chunkID int64
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO chunks (repo,path,start_line,end_line,symbol,kind,hash,lang) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
			repo, path, ch.StartLine, ch.EndLine, ch.Symbol, ch.Kind, chunkHash, p.Lang).Scan(&chunkID); err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
		// The substring row is keyed by the chunk id so deleteFile can target it. Its
		// body is the code-tokenized text so both split terms (getUserName→get user
		// name) and the original identifiers are substring-searchable.
		if err := s.grams.Put(ctx, tx, chunkID, ftsBody(ch.Text)); err != nil {
			return fmt.Errorf("insert fts: %w", err)
		}
		if i < len(vecs) && len(vecs[i]) > 0 {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO vectors (chunk_id,dim,vec) VALUES ($1,$2,$3)`,
				chunkID, len(vecs[i]), vec.Encode(vecs[i])); err != nil {
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
	row := s.db.QueryRowContext(ctx, chunkSelect+` WHERE c.id=$1`, id)
	r, err := scanChunk(row)
	if errors.Is(err, sql.ErrNoRows) {
		return chunkRow{}, errNotFound
	}
	if err != nil {
		return chunkRow{}, fmt.Errorf("get chunk: %w", err)
	}
	return r, nil
}

// ftsSearch finds the chunks whose code-tokenized text holds the terms, as
// substrings, and returns them best first: every term when all is set, else any one.
// repo=="" searches every repo in the org's store.
//
// It ranks and caps inside a subquery on the index + chunks before joining blobs so a
// broad match never reads tens of thousands of blob texts just to discard all but the
// top limit.
func (s *Store) ftsSearch(ctx context.Context, repo string, terms []string, all bool, limit int) ([]chunkRow, error) {
	hits, args := s.grams.Hits(1, terms, all)
	sub := `SELECT h.hit AS id, h.score AS rank FROM (` + hits + `) h`
	if repo != "" {
		rc, rargs := repoCond("c.repo", repo, len(args)+1)
		sub += ` JOIN chunks c ON c.id=h.hit WHERE ` + rc
		args = append(args, rargs...)
	}
	args = append(args, limit)
	sub += ` ORDER BY rank, h.hit LIMIT $` + strconv.Itoa(len(args))
	q := chunkSelect + ` JOIN (` + sub + `) m ON m.id=c.id ORDER BY m.rank, c.id`
	return s.queryChunks(ctx, q, args...)
}

// scanChunks loads chunk rows for regex candidate scanning (repo-scoped, bounded).
func (s *Store) scanChunks(ctx context.Context, repo string, limit int) ([]chunkRow, error) {
	q := chunkSelect
	var args []any
	if repo != "" {
		rc, rargs := repoCond("c.repo", repo, 1)
		q += ` WHERE ` + rc
		args = append(args, rargs...)
	}
	args = append(args, limit)
	q += ` LIMIT $` + strconv.Itoa(len(args))
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
// case-insensitive substring match. Returns symbols with their defining chunk id
// when one exists.
func (s *Store) symbolSearch(ctx context.Context, repo, query string, limit int) ([]Symbol, error) {
	esc := escapeLike(query)
	prefix := esc + "%"
	sub := "%" + esc + "%"
	// $1 is the name as typed, $2 its prefix, $3 its substring; the ordering reads
	// them again. The name matches however its case is spelled, as it did when LIKE
	// folded ASCII case on its own.
	q := `SELECT repo,path,name,kind,line,end_line,signature,scope,lang FROM symbols WHERE `
	args := []any{query, prefix, sub}
	conds := []string{`(name=$1 OR lower(name)=lower($1) OR lower(name) LIKE lower($2) ESCAPE '\' OR lower(name) LIKE lower($3) ESCAPE '\')`}
	if repo != "" {
		rc, rargs := repoCond("repo", repo, len(args)+1)
		conds = append(conds, rc)
		args = append(args, rargs...)
	}
	q += strings.Join(conds, " AND ")
	// Exact matches rank ahead of case-insensitive exact, then prefix matches, then shorter names.
	args = append(args, limit)
	q += ` ORDER BY (name=$1) DESC, (lower(name)=lower($1)) DESC, (lower(name) LIKE lower($2) ESCAPE '\') DESC, length(name) ASC, name, path, line LIMIT $` + strconv.Itoa(len(args))
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
	q := `SELECT repo,path,name,kind,line,end_line,signature,scope,lang FROM symbols WHERE name=$1`
	args := []any{name}
	if repo != "" {
		rc, rargs := repoCond("repo", repo, 2)
		q += ` AND ` + rc
		args = append(args, rargs...)
	}
	q += ` ORDER BY length(name) ASC, path, line LIMIT 1`
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
	q := `SELECT DISTINCT name,path,line,from_symbol FROM edges WHERE name=$1 AND from_symbol!=''`
	args := []any{name}
	if repo != "" {
		rc, rargs := repoCond("repo", repo, 2)
		q += ` AND ` + rc
		args = append(args, rargs...)
	}
	args = append(args, limit)
	q += ` LIMIT $` + strconv.Itoa(len(args))
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
	q := `SELECT DISTINCT name FROM edges WHERE (from_symbol=$1 OR lower(from_symbol) LIKE lower($2) ESCAPE '\')`
	args := []any{from, "%." + escapeLike(from)}
	if repo != "" {
		rc, rargs := repoCond("repo", repo, 3)
		q += ` AND ` + rc
		args = append(args, rargs...)
	}
	args = append(args, limit)
	q += ` LIMIT $` + strconv.Itoa(len(args))
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
		chunkSelect+` WHERE c.repo=$1 AND c.path=$2 AND c.start_line<=$3 AND c.end_line>=$3 ORDER BY (c.end_line-c.start_line) ASC, c.id LIMIT 1`,
		sym.Repo, sym.Path, sym.Line)
	r, err := scanChunk(row)
	if err != nil {
		return chunkRow{}, false
	}
	r.Symbol = sym.Name
	r.Kind = sym.Kind
	return r, true
}

// hasVectors reports whether the store holds any vector embeddings (for repo when
// non-empty, or across the whole org store otherwise), so searchSemantic can skip
// the query-embedding network call when there are no vectors to compare against.
func (s *Store) hasVectors(ctx context.Context, repo string) bool {
	var id int64
	q := `SELECT chunk_id FROM vectors LIMIT 1`
	var args []any
	if repo != "" {
		rc, rargs := repoCond("c.repo", repo, 1)
		q = `SELECT v.chunk_id FROM vectors v JOIN chunks c ON c.id=v.chunk_id WHERE ` + rc + ` LIMIT 1`
		args = rargs
	}
	return s.db.QueryRowContext(ctx, q, args...).Scan(&id) == nil
}

// nearest scores q against the repo's vectors in Go and keeps the k chunks of
// highest positive similarity, best first. It streams rows into vec.Top, so it
// holds k hits and one vector at a time however many rows it reads.
//
// It reads at most max vectors, and says so: capped is true when rows remained
// past the cap, which means a chunk there was never scored. The answer is then
// the best of the first max, and a caller told nothing would read it as the best
// of the whole index.
//
// q is of space sp, and a query of another space than the index's is refused
// (*vec.MixedError), never scored. Only vectors of q's dimension are read
// besides, which reaches only a store written before spaces were recorded, until
// its first write adopts it: a vector there of another dimension is not
// comparable to q, and re-indexing its file replaces it.
func (s *Store) nearest(ctx context.Context, repo string, q []float32, sp vec.Space, k, max int) (top []vec.Hit[int64], capped bool, err error) {
	qs, err := sp.Of(q)
	if err != nil {
		return nil, false, fmt.Errorf("query vector: %w", err)
	}
	if err := codeIndex.Admit(ctx, s.db, qs); err != nil {
		return nil, false, err
	}
	query := `SELECT v.chunk_id,v.vec FROM vectors v WHERE v.dim=$1`
	args := []any{len(q)}
	if repo != "" {
		rc, rargs := repoCond("c.repo", repo, 2)
		query = `SELECT v.chunk_id,v.vec FROM vectors v JOIN chunks c ON c.id=v.chunk_id WHERE v.dim=$1 AND ` + rc
		args = append(args, rargs...)
	}
	args = append(args, max+1)
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY v.chunk_id LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, false, fmt.Errorf("read vectors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var read int
	top = vec.Top(func(yield func(int64, float64) bool) {
		for rows.Next() {
			if read == max {
				capped = true
				return
			}
			read++
			var (
				id  int64
				raw sql.RawBytes
			)
			if err = rows.Scan(&id, &raw); err != nil {
				return
			}
			v, derr := vec.Decode(raw)
			if derr != nil {
				err = fmt.Errorf("vector of chunk %d: %w", id, derr)
				return
			}
			sim, cerr := vec.Cos(q, v)
			if cerr != nil {
				err = fmt.Errorf("vector of chunk %d: %w", id, cerr)
				return
			}
			// A non-positive similarity is unrelated: it must not surface just
			// because the corpus is small (it would pollute fusion + context seeds).
			if sim > 0 && !yield(id, sim) {
				return
			}
		}
	}, k)
	if err != nil {
		return nil, false, err
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read vectors: %w", err)
	}
	return top, capped, nil
}

// chunkPage reads up to n of the org's chunks with an id above after, in id
// order, with the text each was cut as: every chunk, or with missing only those
// with no vector. A reindex walks the whole index with it, every repository.
func (s *Store) chunkPage(ctx context.Context, after int64, missing bool, n int) (ids []int64, texts []string, err error) {
	q := `SELECT c.id, b.content FROM chunks c JOIN blobs b ON b.hash=c.hash WHERE c.id > $1`
	if missing {
		q += ` AND NOT EXISTS (SELECT 1 FROM vectors v WHERE v.chunk_id=c.id)`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY c.id LIMIT $2`, after, n)
	if err != nil {
		return nil, nil, fmt.Errorf("read chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id   int64
			text string
		)
		if err := rows.Scan(&id, &text); err != nil {
			return nil, nil, fmt.Errorf("read chunk: %w", err)
		}
		ids, texts = append(ids, id), append(texts, text)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read chunks: %w", err)
	}
	return ids, texts, nil
}

// putVectors writes the vectors of chunks ids, of space sp (its dimension is
// theirs), and answers how many landed: a chunk an index pass deleted since it
// was read gets none. With fresh, every vector the index holds and its record go
// first, in the same transaction, and the index is created anew in sp: it is
// never without a record that a writer in another space could take first.
func (s *Store) putVectors(ctx context.Context, sp vec.Space, ids []int64, vecs [][]float32, fresh bool) (int, error) {
	if len(vecs) != len(ids) {
		return 0, fmt.Errorf("%d vectors for %d chunks", len(vecs), len(ids))
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if fresh {
		if err := clearVectorsTx(ctx, tx); err != nil {
			return 0, err
		}
	}
	if len(ids) > 0 {
		if err := codeIndex.Claim(ctx, tx, sp); err != nil {
			return 0, err
		}
	}
	var n int
	for i, id := range ids {
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM chunks WHERE id=$1`, id).Scan(&one); {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return 0, fmt.Errorf("read chunk %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO vectors (chunk_id,dim,vec) VALUES ($1,$2,$3)
ON CONFLICT (chunk_id) DO UPDATE SET dim=excluded.dim, vec=excluded.vec`,
			id, len(vecs[i]), vec.Encode(vecs[i])); err != nil {
			return 0, fmt.Errorf("write vector of chunk %d: %w", id, err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit vectors: %w", err)
	}
	return n, nil
}

// clearVectors removes every vector the index holds and its record, in one
// transaction, so the next write creates the index anew in its own space.
func (s *Store) clearVectors(ctx context.Context) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := clearVectorsTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func clearVectorsTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM vectors`); err != nil {
		return fmt.Errorf("clear vectors: %w", err)
	}
	return codeIndex.Forget(ctx, tx)
}

// dropOtherDimensions removes the vectors not of dims: rows a store kept from
// before it recorded its space, of a model no query of its space reads.
func (s *Store) dropOtherDimensions(ctx context.Context, dims int) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM vectors WHERE dim<>$1`, dims); err != nil {
		return fmt.Errorf("drop vectors of another dimension: %w", err)
	}
	return nil
}

// coverage answers how many chunks the org's index holds and how many of them
// have a vector, across every repository.
func (s *Store) coverage(ctx context.Context) (chunks, vectors int, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&chunks); err != nil {
		return 0, 0, fmt.Errorf("count chunks: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vectors v JOIN chunks c ON c.id=v.chunk_id`).Scan(&vectors); err != nil {
		return 0, 0, fmt.Errorf("count vectors: %w", err)
	}
	return chunks, vectors, nil
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
				rc, rargs := repoCond("c.repo", repo, 1)
				q = `SELECT COUNT(*) FROM vectors v JOIN chunks c ON c.id=v.chunk_id WHERE ` + rc
				args = rargs
			default:
				rc, rargs := repoCond("repo", repo, 1)
				q += ` WHERE ` + rc
				args = rargs
			}
		}
		_ = s.db.QueryRowContext(ctx, q, args...).Scan(&n)
		return n
	}
	return count("files"), count("symbols"), count("chunks"), count("vectors")
}
