package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// store.go is where the vector leg keeps its passages: ONE table in the org's own
// database (a SQLite file, {DataDir}/orgs/{org}/knowledge.db, or on hanzo-sql a
// schema of that name), opened through cloud.OrgStore like every other per-org
// store. There is no org column because the database IS the org — a query in one
// org's database cannot reach another org's rows, so tenancy is physical rather
// than a clause somebody has to remember.
//
// Its SQL is the dialect both engines share (sdk/dialect), and its tests
// run on both (internal/sqltest).
//
// A row is one passage of one document: which document (doctype, name), which
// passage (seq, and its byte span [lo, hi) of the text the document had when it
// was embedded), which model embedded it, and the vector as a vec.Encode blob.
// The document's project and owner ride on every row so a search applies its
// reach in the query, and its provider so a connector's disconnect takes its
// passages. Nothing is cited from here: a hit is answered from the document
// itself (present in retrieve.go).
//
// The row of marks named built is 1 once a build has synced every document the
// org holds (build.go), and built mirrors it.
//
// Every vector the store holds, a document's passages (chunks) and a file's
// (file_passages) alike, is in ONE space (internal/vec): kbIndex, recorded in
// vector_spaces by the first write and never changed in place. A write or a
// query of another model revision, dimension, normalization or task prefix is
// refused; reset, which POST /v1/knowledge/reindex runs, is the one way to
// another space.
type store struct {
	db    *sql.DB
	d     dialect.Dialect
	built atomic.Bool
}

// openStore is the open func cloud.OrgStore calls once per org file.
func openStore(db *sql.DB) (*store, error) {
	d := dialect.Of(db)
	ddl := `
CREATE TABLE IF NOT EXISTS chunks (
  doctype  TEXT    NOT NULL,
  name     TEXT    NOT NULL,
  seq      BIGINT  NOT NULL,
  lo       BIGINT  NOT NULL,
  hi       BIGINT  NOT NULL,
  model    TEXT    NOT NULL,
  vec      ` + d.Blob() + ` NOT NULL,
  project  TEXT    NOT NULL DEFAULT '',
  provider TEXT    NOT NULL DEFAULT '',
  owner    TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (doctype, name, seq)
);
CREATE INDEX IF NOT EXISTS ix_chunks_provider ON chunks(provider);
CREATE TABLE IF NOT EXISTS marks (
  name  TEXT   PRIMARY KEY,
  value BIGINT NOT NULL
);
` + vec.SpaceDDL + filesDDL(d)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	built := false
	if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("kb: migrate: %w", err)
		}
		if err := addFileColumns(ctx, d, tx); err != nil {
			return fmt.Errorf("kb: migrate files: %w", err)
		}
		if _, err := tx.ExecContext(ctx, finishedEarlier); err != nil {
			return fmt.Errorf("kb: migrate files: %w", err)
		}
		var err error
		if built, err = builtMark(ctx, d, tx); err != nil {
			return fmt.Errorf("kb: read build mark: %w", err)
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &store{db: db, d: d}
	s.built.Store(built)
	return s, nil
}

// builtMark reads whether a build has synced every document the org holds. A SQLite
// file an earlier build wrote kept the mark as the file's PRAGMA user_version, which is
// not a table and does not travel to hanzo-sql; it is read once, here, and carried into
// marks, so the build the file already had is not run again (it embeds every
// document).
func builtMark(ctx context.Context, d dialect.Dialect, tx *sql.Tx) (bool, error) {
	var v int
	err := tx.QueryRowContext(ctx, `SELECT value FROM marks WHERE name = $1`, builtName).Scan(&v)
	switch {
	case err == nil:
		return v >= 1, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	case d.Postgres():
		return false, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT user_version FROM pragma_user_version`).Scan(&v); err != nil {
		return false, err
	}
	if v < 1 {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO marks (name, value) VALUES ($1, 1) ON CONFLICT (name) DO NOTHING`, builtName)
	return err == nil, err
}

// builtName is the name of the mark a finished build leaves.
const builtName = "built"

// markBuilt records that a build has synced every document the org holds.
func (s *store) markBuilt(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO marks (name, value) VALUES ($1, 1)
ON CONFLICT (name) DO UPDATE SET value = excluded.value`, builtName); err != nil {
		return fmt.Errorf("kb: mark built: %w", err)
	}
	s.built.Store(true)
	return nil
}

// Close closes the org's file.
func (s *store) Close() error { return s.db.Close() }

// meta is what every passage carries of its document: the fields a search's
// reach and a connector's disconnect are applied on.
type meta struct{ project, provider, owner string }

// metaOf reads a document's meta out of its data.
func metaOf(data map[string]any) meta {
	return meta{project: str(data["project"]), provider: str(data["provider"]), owner: str(data["owner"])}
}

// kbIndex is the org's one vector index, over both tables of vectors. Held
// tells a store written before spaces were recorded from an empty one.
var kbIndex = vec.Index{
	Name: "knowledge",
	Held: `SELECT 1 WHERE EXISTS (SELECT 1 FROM chunks) OR EXISTS (SELECT 1 FROM file_passages WHERE vec IS NOT NULL)`,
}

// put makes spans the document's WHOLE set of passages, in one transaction: its
// previous passages go and these arrive together, so a re-save never leaves a
// passage of the old text behind and a reader never sees half of either. The
// vectors are of space sp (its dimension is theirs), which the org's index
// must have been created in, or is created in by this write; otherwise nothing
// is written and the refusal is a *vec.MixedError.
func (s *store) put(ctx context.Context, doctype, name string, m meta, sp vec.Space, spans []span, vecs [][]float32) error {
	if err := dialect.Text(doctype, name, sp.Model, m.project, m.provider, m.owner); err != nil {
		return fmt.Errorf("kb: write passage: %w", err)
	}
	if len(vecs) != len(spans) {
		return fmt.Errorf("kb: write passage: %d vectors for %d passages", len(vecs), len(spans))
	}
	sp, err := sp.Of(vecs...)
	if err != nil {
		return fmt.Errorf("kb: write passage: %w", err)
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("kb: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE doctype=$1 AND name=$2`, doctype, name); err != nil {
		return fmt.Errorf("kb: drop passages: %w", err)
	}
	if len(vecs) > 0 {
		if err := kbIndex.Claim(ctx, tx, sp); err != nil {
			return fmt.Errorf("kb: write passage: %w", err)
		}
	}
	for i, at := range spans {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunks (doctype,name,seq,lo,hi,model,vec,project,provider,owner)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			doctype, name, i, at.lo, at.hi, sp.Model, vec.Encode(vecs[i]),
			m.project, m.provider, m.owner); err != nil {
			return fmt.Errorf("kb: write passage: %w", err)
		}
	}
	return tx.Commit()
}

// drop removes a document's passages.
func (s *store) drop(ctx context.Context, doctype, name string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM chunks WHERE doctype=$1 AND name=$2`, doctype, name); err != nil {
		return fmt.Errorf("kb: drop passages: %w", err)
	}
	return nil
}

// dropProvider removes every passage a connector's documents contributed.
func (s *store) dropProvider(ctx context.Context, provider string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM chunks WHERE provider=$1`, provider); err != nil {
		return fmt.Errorf("kb: drop provider passages: %w", err)
	}
	return nil
}

// reset empties the org's vector index for a rebuild, in ONE transaction: every
// document passage, every file passage's vector, and the record of the space
// they were in. No reader sees vectors of the old space beside a new record,
// and the rebuild's first write creates the index anew in the space it embeds
// in.
//
// A file at rest (ready, no stage) is marked ready at stage embed, which is
// what a file whose vectors are still being written reads as, and answered for
// the caller to start; one the caller cannot start is started by the resume
// sweep, which reads that mark as work under way. A file already embedding is
// left to its run, which writes every passage that has no vector.
func (s *store) reset(ctx context.Context) ([]File, error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("kb: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM chunks`,
		`UPDATE file_passages SET vec=NULL, model='' WHERE vec IS NOT NULL OR model<>''`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("kb: clear vectors: %w", err)
		}
	}
	if err := kbIndex.Forget(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+fileCols+` FROM files WHERE status=$1 AND stage=''`, fileReady)
	if err != nil {
		return nil, fmt.Errorf("kb: read files: %w", err)
	}
	var files []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("kb: read file: %w", err)
		}
		files = append(files, f)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kb: read files: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE files SET stage=$1, error='', embedded=0, done=0, total=0, updated=$2
WHERE status=$3 AND stage=''`, stageEmbed, time.Now().Unix(), fileReady); err != nil {
		return nil, fmt.Errorf("kb: mark files: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("kb: clear vectors: %w", err)
	}
	for i := range files {
		files[i].Stage = stageEmbed
	}
	return files, nil
}

// reachOf is who may see what, and within which scope: the rows a search is
// allowed to score. It is applied IN the query, so a passage outside it is never
// read — the k best are the k best of what the asker may see, not a filter over
// the k best of everything.
type reachOf struct {
	space    vec.Space // the query's: only the index's own space is comparable to it
	project  string    // "" for every project
	doctypes []string  // empty for every indexed doctype
	subject  string    // "" is the org itself, which reaches unowned documents only
}

// nearest scores every passage in reach against q and answers the k documents
// whose BEST passage scores highest, best first, each carrying that passage's
// span and score.
//
// It reads each row once and keeps one best passage per document, then keeps the
// k best documents with vec.Top — so it holds one entry per document in reach and
// k hits, never the vectors themselves. There is no scan cap to hide: every row
// in reach is scored, which is linear in the org's passages and is the cost of
// holding them in the org's own file rather than a vector database.
//
// A query of another space than the index's is refused (*vec.MixedError), never
// scored: the model is a served SKU whose upstream can change revision or shape,
// and a cosine across two spaces means nothing. Only rows of the query's model
// and dimension are read besides, which reaches only the passages of a store
// written before spaces were recorded, until its first write adopts it: a
// passage there of another model waits for its document's next save or a
// reindex, as it always has.
func (s *store) nearest(ctx context.Context, q []float32, r reachOf, k int) ([]hit, error) {
	qs, err := r.space.Of(q)
	if err != nil {
		return nil, fmt.Errorf("kb: query: %w", err)
	}
	if err := kbIndex.Admit(ctx, s.db, qs); err != nil {
		return nil, fmt.Errorf("kb: %w", err)
	}
	var bd dialect.Args
	where := []string{`model=` + bd.Mark(qs.Model), `length(vec)=` + bd.Mark(len(vec.Encode(q)))}
	if r.project != "" {
		where = append(where, `project=`+bd.Mark(r.project))
	}
	if len(r.doctypes) > 0 {
		marks := make([]string, len(r.doctypes))
		for i, d := range r.doctypes {
			marks[i] = bd.Mark(d)
		}
		where = append(where, `doctype IN (`+strings.Join(marks, ",")+`)`)
	}
	if r.subject == "" {
		where = append(where, `owner=''`)
	} else {
		where = append(where, `(owner='' OR owner=`+bd.Mark(r.subject)+`)`)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT doctype,name,seq,lo,hi,vec FROM chunks WHERE `+
			strings.Join(where, ` AND `), bd.Values()...)
	if err != nil {
		return nil, fmt.Errorf("kb: read passages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type best struct {
		h   hit
		seq int
	}
	docs := map[string]best{}
	for rows.Next() {
		var (
			b   best
			raw sql.RawBytes
		)
		if err := rows.Scan(&b.h.DocType, &b.h.Name, &b.seq, &b.h.lo, &b.h.hi, &raw); err != nil {
			return nil, fmt.Errorf("kb: read passage: %w", err)
		}
		v, err := vec.Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("kb: passage %s/%s#%d: %w", b.h.DocType, b.h.Name, b.seq, err)
		}
		if b.h.Score, err = vec.Cos(q, v); err != nil {
			return nil, fmt.Errorf("kb: passage %s/%s#%d: %w", b.h.DocType, b.h.Name, b.seq, err)
		}
		key := b.h.DocType + "/" + b.h.Name
		// The earlier passage wins a tie, so which passage a document is cited by
		// does not depend on the order rows came back in.
		if prev, ok := docs[key]; ok && (prev.h.Score > b.h.Score || prev.h.Score == b.h.Score && prev.seq < b.seq) {
			continue
		}
		docs[key] = b
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kb: read passages: %w", err)
	}
	top := vec.Top(func(yield func(string, float64) bool) {
		for key, b := range docs {
			if !yield(key, b.h.Score) {
				return
			}
		}
	}, k)
	out := make([]hit, len(top))
	for i, t := range top {
		out[i] = docs[t.Key].h
	}
	return out, nil
}
