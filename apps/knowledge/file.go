package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// file.go makes an object in an org's Hanzo S3 a workspace FILE: a record of
// what it is, the text read out of it, and that text indexed in three layers —
// so a chat can retrieve what a file says by reference, Drive can find a file by
// a phrase inside it, and an agent can walk a corpus the way a reader does.
//
// The bytes stay where the caller put them: an upload into one of the org's
// buckets (/v1/s3, presigned PUT or multipart), which is also what Drive lists.
// Registering the object here is the call after the upload lands:
//
//	POST   /v1/knowledge/files {bucket, key}      record it, state it on the bus, queue its ingest
//	GET    /v1/knowledge/files                    the org's files, with status
//	GET    /v1/knowledge/files/:id                one file
//	DELETE /v1/knowledge/files/:id                forget its index (the object stays)
//	GET    /v1/knowledge/files/:id/toc            its table of contents, each node summarized
//	GET    /v1/knowledge/files/:id/sections/:n    one section: its text, its links, its entities
//	GET    /v1/knowledge/files/:id/graph          what it links to and shares entities with
//	POST   /v1/knowledge/files/search             passages that match, across the org's files
//	POST   /v1/knowledge/files/retrieve           the ToC-first retrieval a chat answers from
//
// THE THREE LAYERS. The table of contents is the primary unit (after STAIR): a
// document's structure — HTML headings and landmarks, Markdown headings, a PDF's
// outline or its larger type, DOCX heading styles, a code file's symbols, a
// sheet's tabs — becomes a tree of sections, each with a one-line summary, and a
// long run of text with no structure is cut into synthetic sections. Passages are
// cut inside a section and never across one, each carrying its section's path;
// they are embedded and written to the full-text index. The graph joins
// documents, sections and entities (people, orgs, products, terms) by links,
// cross-references, citations and shared mentions, within the org.
//
// The ingest is a durable workflow in stages (ingest.go): extract → toc →
// passages → graph, after which the file is READY — its contents, passages and
// links all readable, its full text searchable — and then embed, which adds the
// passages' vectors while the file is already in use. Each stage is retried on
// its own and recorded on the file, so its status is honest while it runs.
//
// TENANCY is by construction. The org is the validated principal's, never a
// field. The bucket a caller names resolves to its physical name inside that org
// (object.BucketName). Every row lives in the org's own knowledge.db, and every
// full-text row in the org's own "files" index.

// Status values a file moves through: queued → indexing → ready. A file that is
// kept but cannot be read (a kind no stage reads, a size past the bound, no text
// in it) is stored, with the reason; a file whose ingest kept failing is failed.
// A forgotten file is forgetting until what the index holds of it is removed:
// no read answers for it (visible), and its record keeps the removal's cursor.
const (
	fileQueued     = "queued"
	fileIndexing   = "indexing"
	fileReady      = "ready"
	fileStoredAs   = "stored"
	fileFailed     = "failed"
	fileForgetting = "forgetting"
)

// visible is the condition every read of a file record holds: a file being
// forgotten is gone to every reader the moment it is forgotten.
const visible = `status<>'forgetting'`

// The stages, in order.
const (
	stageExtract  = "extract"
	stageToc      = "toc"
	stagePassages = "passages"
	stageEmbed    = "embed"
	stageGraph    = "graph"
)

var stages = []string{stageExtract, stageToc, stagePassages, stageGraph, stageEmbed}

// readyAfter is the stage after which a file is ready: everything it is read
// for is written, and only its vectors remain.
const readyAfter = stageGraph

// filesIndex is the full-text index (apps/index) a file's passages are written
// to, one row per passage.
const filesIndex = "files"

// filesDDL is the file index in the org's knowledge.db, created beside the KB
// passages (store.go openStore). There is no org column: the database is the org.
func filesDDL(d dialect.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS files (
  id       TEXT    PRIMARY KEY,
  bucket   TEXT    NOT NULL,
  key      TEXT    NOT NULL,
  name     TEXT    NOT NULL,
  type     TEXT    NOT NULL DEFAULT '',
  size     BIGINT  NOT NULL DEFAULT 0,
  etag     TEXT    NOT NULL DEFAULT '',
  project  TEXT    NOT NULL DEFAULT '',
  parent   TEXT    NOT NULL DEFAULT '',
  status   TEXT    NOT NULL,
  stage    TEXT    NOT NULL DEFAULT '',
  error    TEXT    NOT NULL DEFAULT '',
  chars    BIGINT  NOT NULL DEFAULT 0,
  clipped  BIGINT  NOT NULL DEFAULT 0,
  sections BIGINT  NOT NULL DEFAULT 0,
  passages BIGINT  NOT NULL DEFAULT 0,
  embedded BIGINT  NOT NULL DEFAULT 0,
  done     BIGINT  NOT NULL DEFAULT 0,
  total    BIGINT  NOT NULL DEFAULT 0,
  note     TEXT    NOT NULL DEFAULT '',
  org      TEXT    NOT NULL DEFAULT '',
  uploader TEXT    NOT NULL DEFAULT '',
  payer    TEXT    NOT NULL DEFAULT '',
  created  BIGINT  NOT NULL,
  updated  BIGINT  NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ix_files_object ON files(bucket, key);
CREATE TABLE IF NOT EXISTS sections (
  file      TEXT    NOT NULL,
  id        BIGINT  NOT NULL,
  parent    BIGINT  NOT NULL,
  level     BIGINT  NOT NULL,
  title     TEXT    NOT NULL,
  anchor    TEXT    NOT NULL DEFAULT '',
  lo        BIGINT  NOT NULL,
  hi        BIGINT  NOT NULL,
  body      TEXT    NOT NULL,
  summary   TEXT    NOT NULL DEFAULT '',
  synthetic BIGINT  NOT NULL DEFAULT 0,
  PRIMARY KEY (file, id)
);
CREATE TABLE IF NOT EXISTS file_passages (
  file    TEXT    NOT NULL,
  seq     BIGINT  NOT NULL,
  section BIGINT  NOT NULL,
  part    BIGINT  NOT NULL,
  lo      BIGINT  NOT NULL,
  hi      BIGINT  NOT NULL,
  model   TEXT    NOT NULL DEFAULT '',
  vec     ` + d.Blob() + `,
  PRIMARY KEY (file, seq)
);
CREATE INDEX IF NOT EXISTS ix_file_passages_section ON file_passages(file, section);
CREATE TABLE IF NOT EXISTS entities (
  id   TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mentions (
  entity  TEXT    NOT NULL,
  file    TEXT    NOT NULL,
  section BIGINT  NOT NULL,
  PRIMARY KEY (entity, file, section)
);
CREATE INDEX IF NOT EXISTS ix_mentions_file ON mentions(file, section);
CREATE TABLE IF NOT EXISTS links (
  file           TEXT    NOT NULL,
  section        BIGINT  NOT NULL,
  kind           TEXT    NOT NULL,
  target_file    TEXT    NOT NULL,
  target_section BIGINT  NOT NULL,
  label          TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (file, section, kind, target_file, target_section, label)
);
CREATE INDEX IF NOT EXISTS ix_links_target ON links(target_file, target_section);
CREATE INDEX IF NOT EXISTS ix_links_label ON links(label) WHERE target_file = '';
`
}

// File is a workspace file: an object in one of the org's buckets, and what
// knowledge holds of it.
type File struct {
	// ID names the file in its org. It is derived from the bucket and key, so
	// registering the same object twice answers the same file.
	ID string `json:"id"`
	// Name is the object's file name, the last segment of its key.
	Name string `json:"name"`
	// Type is the object's media type as the store holds it, or the one its
	// name implies when the store holds only the generic default.
	Type string `json:"type"`
	// Size is the object's length in bytes, as the store reports it.
	Size int64 `json:"size"`
	// Bucket is the org bucket the object is in, by the friendly name
	// /v1/s3/buckets lists.
	Bucket string `json:"bucket"`
	// Key is the object's key in that bucket. GET
	// /v1/s3/buckets/{bucket}/objects/{key} answers a signed download URL for it.
	Key string `json:"key"`
	// Project is the project scope it is indexed under. Absent for the org's
	// own files.
	Project string `json:"project,omitempty"`
	// Parent is the id of the archive this file was unpacked from. Absent for a
	// file uploaded on its own.
	Parent string `json:"parent,omitempty"`
	// Status is queued, indexing, ready, stored (kept but not indexed — Error
	// says why) or failed. A ready file's contents, passages and links are all
	// readable and its full text is searched; its vectors may still be filling
	// in (Stage embed), which adds search by meaning as it goes.
	Status string `json:"status"`
	// Stage is the ingest stage the file is in: extract, toc, passages or graph
	// while indexing, embed while a ready file's vectors are written. Absent once
	// the ingest is done.
	Stage string `json:"stage,omitempty"`
	// Error is why a stored or failed file was not indexed — or, on a ready
	// file, why it is searched by its words alone — in words a person can act
	// on. Absent otherwise.
	Error string `json:"error,omitempty"`
	// Chars is the length of the text read out of the file, in bytes.
	Chars int `json:"chars,omitempty"`
	// Clipped is true when only the file's beginning is indexed: its text ran
	// past the org's bound or the room the index has. Note says how far.
	Clipped bool `json:"clipped,omitempty"`
	// Sections is how many nodes its table of contents has, the document's own
	// root included.
	Sections int `json:"sections,omitempty"`
	// Passages is how many passages its text was cut into.
	Passages int `json:"passages,omitempty"`
	// Embedded is how many of those passages carry a vector — Passages once the
	// embed stage is done, unless Note says the file is embedded in part.
	Embedded int `json:"embedded,omitempty"`
	// Done is how far the running stage has come, of Total: bytes of the file
	// read (extract), sections summarized (toc), cut into passages (passages)
	// and linked (graph), passages embedded (embed). Absent between stages and
	// once the ingest is done.
	Done int64 `json:"done,omitempty"`
	// Total is what the running stage has to do in all, in Done's units.
	Total int64 `json:"total,omitempty"`
	// Note says in words where the file is indexed less than whole and why: its
	// text past the org's bound or the room the index has, its passages past
	// the bound on vectors. Absent when the whole file is indexed every way.
	Note string `json:"note,omitempty"`
	// Created is when the file was first registered, in unix seconds.
	Created int64 `json:"created"`
	// Updated is when its record last changed, in unix seconds.
	Updated int64 `json:"updated"`

	etag string
	// org is the org the file was registered by, spelled as the run that ingests
	// it is told: the store is named by its folded form, which a run starting
	// the file again (resume) cannot unfold. Empty on a record written before
	// records carried it.
	org string
	// uploader is the subject the identity boundary vouched for when the file
	// was registered (principal.SubjectFrom): the one person, beside an admin,
	// who may change or forget it (actor.manages). A file unpacked from an
	// archive is its archive's uploader's. Empty on a record written before
	// records carried it, and for a caller that names no subject: such a file
	// is nobody's, so only an admin manages it. It never moves while the file
	// is live; a file registered again after it was forgotten is its new
	// registrant's.
	uploader string
	// payer is the account that pays for the file's ingest — its model calls and
	// embeddings — written the way IAM writes the billing_account claim
	// (account.Account.String). It is the registrant's, read where the file was
	// registered (cloud.PayerOf), because the ingest runs long after that request
	// is gone and must not fall back to the org: in the signup org the org's own
	// account is the platform's pool. Empty on a record written before records
	// carried it; see [File.wallet].
	payer string
}

// wallet is the account the file's ingest is charged to. A record written before
// it carried a payer was charged to its org's own account, and that is what it
// keeps everywhere the org pools; in the signup org such a record names nobody, and
// its model calls are refused rather than billed to the platform.
func (f File) wallet(org string) account.Account {
	if p := account.Parse(f.payer); !p.Zero() {
		return p
	}
	return account.Payer(account.Credential{Owner: org})
}

// fileID names an object in its org: the same bucket and key always answer the
// same id, so a re-upload of one key is a new version of one file.
func fileID(bucket, key string) string {
	sum := sha256.Sum256([]byte(bucket + "\x00" + key))
	return "f" + hex.EncodeToString(sum[:])[:20]
}

const fileCols = `id,bucket,key,name,type,size,etag,project,parent,status,stage,error,chars,clipped,sections,passages,embedded,done,total,note,created,updated,org,uploader,payer`

// fileColumns are the columns a files table made before them gains when its
// store opens (openStore), so an org's store reads the same whichever build
// made it.
var fileColumns = [][2]string{
	{"done", "done BIGINT NOT NULL DEFAULT 0"},
	{"total", "total BIGINT NOT NULL DEFAULT 0"},
	{"note", "note TEXT NOT NULL DEFAULT ''"},
	{"org", "org TEXT NOT NULL DEFAULT ''"},
	{"uploader", "uploader TEXT NOT NULL DEFAULT ''"},
	{"payer", "payer TEXT NOT NULL DEFAULT ''"},
}

// finishedEarlier rewrites the records an earlier build finished into the form
// this one reads as finished. That build ran graph last and marked a finished
// file ready AT it, so its done files read ready with stage graph; this one
// runs graph before embed and marks a finished file ready with no stage, and a
// ready file with a stage is one whose vectors are still being written. Read
// that way, a file the earlier build finished claims work nothing will ever
// do. This build never writes ready with stage graph — graph runs while a file
// is indexing — so the rewrite reaches only those records.
const finishedEarlier = `UPDATE files SET stage='' WHERE status='ready' AND stage='graph'`

// underway reads the files whose record says work is under way, forgetting
// ones included: the records the resume sweep (ingest.go) weighs.
func (s *store) underway(ctx context.Context) ([]File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+fileCols+` FROM files
WHERE status IN ('queued','indexing','forgetting') OR (status='ready' AND stage<>'')`)
	if err != nil {
		return nil, fmt.Errorf("kb: read files under way: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, fmt.Errorf("kb: read file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// addFileColumns adds the fileColumns a files table lacks.
func addFileColumns(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	for _, c := range fileColumns {
		have, err := d.HasColumn(ctx, tx, "files", c[0])
		if err != nil {
			return err
		}
		if !have {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE files ADD COLUMN `+c[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanFile(row interface{ Scan(...any) error }) (File, error) {
	var (
		f       File
		clipped int
	)
	err := row.Scan(&f.ID, &f.Bucket, &f.Key, &f.Name, &f.Type, &f.Size, &f.etag, &f.Project, &f.Parent,
		&f.Status, &f.Stage, &f.Error, &f.Chars, &clipped, &f.Sections, &f.Passages, &f.Embedded, &f.Done, &f.Total, &f.Note, &f.Created, &f.Updated, &f.org, &f.uploader, &f.payer)
	f.Clipped = clipped != 0
	return f, err
}

// file reads one file's record; sql.ErrNoRows when the org holds no such file.
func (s *store) file(ctx context.Context, id string) (File, error) {
	return scanFile(s.db.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE id=$1 AND `+visible, id))
}

// fileAt reads the file at an object, if the org has registered it.
func (s *store) fileAt(ctx context.Context, bucket, key string) (File, error) {
	return scanFile(s.db.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE bucket=$1 AND key=$2 AND `+visible, bucket, key))
}

// files reads the org's files, newest first, optionally those in one bucket.
func (s *store) files(ctx context.Context, bucket string, limit int) ([]File, error) {
	var a dialect.Args
	q := `SELECT ` + fileCols + ` FROM files WHERE ` + visible
	if bucket != "" {
		q += ` AND bucket=` + a.Mark(bucket)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY updated DESC LIMIT `+a.Mark(limit), a.Values()...)
	if err != nil {
		return nil, fmt.Errorf("kb: read files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, fmt.Errorf("kb: read file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// queue records a file as waiting for its ingest: a new file, or a new version
// of one, which starts again from extract. A live record keeps its uploader and its
// payer; a forgotten one takes the new registrant's.
func (s *store) queue(ctx context.Context, f File) error {
	if err := dialect.Text(f.ID, f.Bucket, f.Key, f.Name, f.Type, f.etag, f.Project, f.Parent, f.org, f.uploader, f.payer); err != nil {
		return fmt.Errorf("kb: queue file: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO files (id,bucket,key,name,type,size,etag,project,parent,status,stage,error,created,updated,org,uploader,payer)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'','',$11,$12,$13,$14,$15)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, type=excluded.type, size=excluded.size,
  etag=excluded.etag, project=excluded.project, parent=excluded.parent, status=excluded.status, org=excluded.org,
  uploader=CASE WHEN files.status='forgetting' THEN excluded.uploader ELSE files.uploader END,
  payer=CASE WHEN files.status='forgetting' OR files.payer='' THEN excluded.payer ELSE files.payer END,
  stage='', error='', chars=0, clipped=0, sections=0, passages=0, embedded=0, done=0, total=0, note='',
  updated=excluded.updated`,
		f.ID, f.Bucket, f.Key, f.Name, f.Type, f.Size, f.etag, f.Project, f.Parent, fileQueued, f.Created, f.Updated, f.org, f.uploader, f.payer)
	if err != nil {
		return fmt.Errorf("kb: queue file: %w", err)
	}
	return nil
}

// mark moves a version of a file to a status and stage, with its reason. A new
// stage starts its progress at nothing; a stage marked again (a retry) keeps
// it, since extract resumes from it. A mark for a version the record no longer
// holds changes nothing: a newer upload has its own ingest, and an older one
// finishing late must not overwrite it.
func (s *store) mark(ctx context.Context, id, etag, status, stage, reason string) error {
	// A reason is whatever error ended the stage, from anywhere; the text it is kept as
	// is what a column can hold.
	reason = strings.ToValidUTF8(strings.ReplaceAll(reason, "\x00", ""), "\uFFFD")
	_, err := s.db.ExecContext(ctx, `UPDATE files SET
  done=CASE WHEN stage=$1 THEN done ELSE 0 END, total=CASE WHEN stage=$1 THEN total ELSE 0 END,
  status=$2, stage=$1, error=$3, updated=$4 WHERE id=$5 AND etag=$6`,
		stage, status, reason, time.Now().Unix(), id, etag)
	if err != nil {
		return fmt.Errorf("kb: mark file: %w", err)
	}
	return nil
}

// progress records how far the running stage of a version has come.
// errSuperseded when the record no longer holds that version.
func (s *store) progress(ctx context.Context, id, etag string, done, total int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE files SET done=$1, total=$2, updated=$3 WHERE id=$4 AND etag=$5`, done, total, time.Now().Unix(), id, etag)
	if err != nil {
		return fmt.Errorf("kb: record progress: %w", err)
	}
	return held(res)
}

// errSuperseded is a write for a version of a file its record no longer holds:
// the file was forgotten, or a newer upload replaced it. The stage writing it
// stops there, so nothing is written for a file that is gone.
var errSuperseded = errors.New("kb: the file no longer holds this version")

// held turns an update that matched no record into errSuperseded.
func held(res sql.Result) error {
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return errSuperseded
	}
	return nil
}

// holds reports whether the record still holds a version: asked before a write
// to the shared full-text index, which no transaction here can roll back.
func (s *store) holds(ctx context.Context, id, etag string) error {
	var one int
	switch err := s.db.QueryRowContext(ctx, `SELECT 1 FROM files WHERE id=$1 AND etag=$2`, id, etag).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return errSuperseded
	case err != nil:
		return fmt.Errorf("kb: read file: %w", err)
	}
	return nil
}

// note records where a version is indexed less than whole, replacing what the
// same stage said before: a sentence a stage owns begins with its prefix
// ("Indexed", "Searched by meaning"), and the others stand.
func (s *store) note(ctx context.Context, id, etag, prefix, text string) error {
	var cur string
	if err := s.db.QueryRowContext(ctx, `SELECT note FROM files WHERE id=$1 AND etag=$2`, id, etag).Scan(&cur); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("kb: read note: %w", err)
	}
	var keep []string
	for _, line := range strings.Split(cur, "\n") {
		if line != "" && !strings.HasPrefix(line, prefix) {
			keep = append(keep, line)
		}
	}
	if text != "" {
		keep = append(keep, text)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE files SET note=$1, updated=$2 WHERE id=$3 AND etag=$4`, strings.Join(keep, "\n"), time.Now().Unix(), id, etag)
	if err != nil {
		return fmt.Errorf("kb: record note: %w", err)
	}
	return nil
}

// count records one of a file's measures (chars, clipped, sections, passages,
// embedded) for a version.
func (s *store) count(ctx context.Context, id, etag, col string, n int) error {
	switch col {
	case "chars", "clipped", "sections", "passages", "embedded":
	default:
		return fmt.Errorf("kb: no file measure %q", col)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE files SET `+col+`=$1, updated=$2 WHERE id=$3 AND etag=$4`, n, time.Now().Unix(), id, etag)
	if err != nil {
		return fmt.Errorf("kb: count %s: %w", col, err)
	}
	return held(res)
}

// forget removes everything the index holds of a file — its sections,
// passages, mentions and links — and its full-text rows, leaving its record.
func (s *store) forget(ctx context.Context, org, id string) error {
	n, err := s.span(ctx, id)
	if err != nil {
		return err
	}
	if err := unlex(ctx, org, id, 0, n); err != nil {
		return err
	}
	return s.unrow(ctx, id)
}

// span is one past a file's last passage: the keys its full-text rows are under.
func (s *store) span(ctx context.Context, id string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq)+1,0) FROM file_passages WHERE file=$1`, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("kb: count passages: %w", err)
	}
	return n, nil
}

// unrow deletes a file's rows here: its passages, sections, mentions and links,
// and the links into it, which lead nowhere once it is gone.
func (s *store) unrow(ctx context.Context, id string) error {
	for _, q := range unrows {
		if _, err := s.db.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("kb: forget file: %w", err)
		}
	}
	return nil
}

var unrows = []string{
	`DELETE FROM file_passages WHERE file=$1`,
	`DELETE FROM sections WHERE file=$1`,
	`DELETE FROM mentions WHERE file=$1`,
	`DELETE FROM links WHERE file=$1`,
	`UPDATE links SET target_file='', target_section=0 WHERE target_file=$1`,
}

// forgetBatch is how many passages' full-text rows one removal carries: one
// transaction in the index every org shares, so other writers take turns.
const forgetBatch = 500

// unlex removes the full-text rows of a file's passages lo up to hi.
func unlex(ctx context.Context, org, id string, lo, hi int) error {
	for ; lo < hi; lo += forgetBatch {
		keys := make([]string, 0, forgetBatch)
		for seq := lo; seq < min(hi, lo+forgetBatch); seq++ {
			keys = append(keys, passageKey(id, seq))
		}
		if err := lexicalErr(lexical.Remove(ctx, org, filesIndex, keys...)); err != nil {
			return fmt.Errorf("kb: drop full-text rows: %w", err)
		}
	}
	return nil
}

// release forgets a file to every reader at once: its record turns forgetting
// (no read answers for it, a running ingest stops at its next write, the text
// it counted against the org's bound is free), and nothing links to it or from
// it. Its full-text rows, passages and sections are removed after, by unindex,
// whose cursor is the record's done of total. sql.ErrNoRows when the org holds
// no such file to forget, or no longer holds it as uploader's: the record the
// caller was admitted against (forgetFile).
func (s *store) release(ctx context.Context, id, uploader string) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("kb: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE files SET status=$1, stage='', error='', note='', etag='',
  chars=0, clipped=0, sections=0, passages=0, embedded=0, done=0,
  total=(SELECT COALESCE(MAX(seq)+1,0) FROM file_passages WHERE file=$2), updated=$3
  WHERE id=$2 AND uploader=$4 AND `+visible, fileForgetting, id, time.Now().Unix(), uploader)
	if err != nil {
		return fmt.Errorf("kb: forget file: %w", err)
	}
	if held(res) != nil {
		return sql.ErrNoRows
	}
	for _, q := range unrows[2:] {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("kb: forget file: %w", err)
		}
	}
	return tx.Commit()
}

// passageKey is a passage's identity in the full-text index.
func passageKey(file string, seq int) string { return file + "#" + strconv.Itoa(seq) }

// ---- the surface ----

// fileIn is POST /v1/knowledge/files: which object to make a file. There is no
// org field — the org is the validated tenant — and no name, type or size: those
// are read off the object the store holds, never taken from the caller.
type fileIn struct {
	// Bucket is one of the org's buckets, by the friendly name /v1/s3/buckets
	// lists. Required.
	Bucket string `json:"bucket"`
	// Key is the object's key in that bucket, as the upload wrote it. Required.
	Key string `json:"key"`
	// Project indexes the file under one project scope. Empty is the org's own.
	Project string `json:"project,omitempty"`
}

// fileRef is a file addressed by its id.
type fileRef struct {
	// ID is the file's id, as POST /v1/knowledge/files answered it.
	ID string `json:"id"`
}

// filesIn is GET /v1/knowledge/files.
type filesIn struct {
	// Bucket lists the files in one of the org's buckets. Empty lists them all.
	Bucket string `json:"-" url:"bucket"`
	// Limit bounds the list, as a decimal. Default 100, maximum 1000.
	Limit string `json:"-" url:"limit"`
}

// filesOut is the org's files, newest first.
type filesOut struct {
	// Files are the org's files, most recently changed first.
	Files []File `json:"files"`
}

// forgotten is what DELETE /v1/knowledge/files/{id} answers.
type forgotten struct {
	// ID is the file whose index was removed.
	ID string `json:"id"`
}

// RegisterFile makes an object in one of the caller's org buckets a workspace
// file. Upload the bytes first — POST /v1/s3/buckets/{bucket}/objects mints a
// presigned PUT, POST /v1/s3/buckets/{bucket}/uploads starts a multipart upload
// for a large one — then register the key here. The object's name, type and
// size are read from the store. The file is recorded as queued, a file_stored
// event is stated on the org's event plane, and a durable ingest is queued that
// extracts its text and structure, summarizes its table of contents, cuts and
// embeds its passages and links it into the org's graph. An archive (.zip) is
// unpacked and every file inside becomes a workspace file of its own. Poll GET
// /v1/knowledge/files/{id} for status. Registering an object again answers the
// same file, and re-indexes it only when the object changed or its last ingest
// failed.
//
// Registering a changed object again changes its file, which only the person
// who registered it or an admin of the org may do: anyone else is refused 403
// and the file keeps its version. Registering an unchanged object is a read.
//
// Example: {"bucket": "hanzo", "key": "chat/report.pdf"}
func (o ops) registerFile(ctx context.Context, in *fileIn) (*File, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	bucket := strings.TrimSpace(in.Bucket)
	if !object.Friendly(bucket) {
		return nil, zip.ErrBadRequest("bucket must be one of the org's buckets, by name")
	}
	key, ok := object.Key(in.Key)
	if !ok {
		return nil, zip.ErrBadRequest("key is required and must be a clean object path")
	}
	x := idx
	if x == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "knowledge is not mounted")
	}
	return x.register(ctx, org, bucket, key, strings.TrimSpace(in.Project), "", actorOf(ctx, org))
}

// register records the object at bucket/key as a file, as by, and queues its
// ingest. parent names the archive it was unpacked from, if any. A file the org
// already holds is changed only by someone who manages it; a new one is by's.
func (x *indexer) register(ctx context.Context, org, bucket, key, project, parent string, by actor) (*File, error) {
	obj, err := objects.stat(ctx, org, bucket, key)
	if err != nil {
		return nil, err
	}
	st, err := x.storeFor(org)
	if err != nil {
		return nil, err
	}
	id := fileID(bucket, key)
	now := time.Now().Unix()
	prev, err := st.file(ctx, id)
	switch {
	case err == nil && prev.etag == obj.etag && prev.Status != fileFailed:
		return &prev, nil // this version is indexed, or on its way
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	f := File{
		ID: id, Name: path.Base(key), Type: typeOf(obj.typ, key), Size: obj.size,
		Bucket: bucket, Key: key, Project: project, Parent: parent,
		Status: fileQueued, Created: now, Updated: now, etag: obj.etag, org: org, uploader: by.subject,
		payer: by.payer.String(),
	}
	if err == nil {
		if err := by.manages(prev); err != nil {
			return nil, err
		}
		f.Created, f.uploader = prev.Created, prev.uploader
	}
	if err := st.queue(ctx, f); err != nil {
		return nil, err
	}
	announce(org, fileStored, f)
	x.enqueue(org, f, "")
	return &f, nil
}

// GetFile answers one of the caller's org files: what it is, where its bytes
// are, and how far its ingest has got. 404 for an id the org holds no file for.
func (o ops) getFile(ctx context.Context, in *fileRef) (*File, error) {
	org, st, err := tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	f, err := st.file(ctx, strings.TrimSpace(in.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, zip.ErrNotFound("no such file")
	}
	if err != nil {
		return nil, err
	}
	if !readsFile(ctx, org, f) {
		return nil, zip.ErrNotFound("no such file")
	}
	return &f, nil
}

// readsFile reports whether the caller may READ a file. In a customer's own org
// every member reads the org's files, as they always did. In a held org (hanzo,
// where every self-serve customer is a member and strangers to each other) a
// file is its uploader's: the uploader reads it, a SuperAdmin reads any, and one
// with no uploader is a SuperAdmin's. The file-based search and retrieve run
// through the same predicate (inReach), and the vector search at
// /v1/knowledge/search is already keyed per person (ownerFilter).
func readsFile(ctx context.Context, org string, f File) bool {
	if !authz.IsHeldOrg(org) {
		return true
	}
	if principal.SuperFrom(ctx) {
		return true
	}
	sub, ok := principal.SubjectFrom(ctx)
	return ok && strings.TrimSpace(sub) != "" && f.uploader == strings.TrimSpace(sub)
}

// ListFiles answers the caller's org files, most recently changed first, each
// with its status and stage — optionally only those in one bucket, which is how
// Drive shows a folder's files with their index state.
func (o ops) listFiles(ctx context.Context, in *filesIn) (*filesOut, error) {
	_, st, err := tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := strconv.Atoi(strings.TrimSpace(in.Limit))
	if err != nil || limit <= 0 || limit > 1000 {
		limit = 100
	}
	fs, err := st.files(ctx, strings.TrimSpace(in.Bucket), limit)
	if err != nil {
		return nil, err
	}
	org, _, _ := tenantStore(ctx)
	kept := fs[:0]
	for _, f := range fs {
		if readsFile(ctx, org, f) {
			kept = append(kept, f)
		}
	}
	return &filesOut{Files: kept}, nil
}

// ForgetFile removes one of the caller's org files from the index — its table of
// contents, passages, full-text rows, mentions and links — and its record. The
// file is gone from every read when this answers: its listing, its contents,
// search and retrieval. What the index held of it is removed after, a batch at
// a time, so forgetting a file of any size answers at once. The object itself
// stays in its bucket; delete it there. Drive calls this when it deletes a file.
//
// Only the person who registered the file or an admin of the org may forget it;
// anyone else in the org, who can still read it, is refused 403. A file
// registered before uploaders were recorded is forgotten by an admin alone.
func (o ops) forgetFile(ctx context.Context, in *fileRef) (*forgotten, error) {
	org, st, err := tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	f, err := st.file(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, zip.ErrNotFound("no such file")
	}
	if err != nil {
		return nil, err
	}
	if err := actorOf(ctx, org).manages(f); err != nil {
		return nil, err
	}
	if err := st.release(ctx, id, f.uploader); errors.Is(err, sql.ErrNoRows) {
		return nil, zip.ErrNotFound("no such file")
	} else if err != nil {
		return nil, err
	}
	idx.erase(org, id)
	return &forgotten{ID: id}, nil
}

// actor is who changes a file: the subject the identity boundary vouched for,
// and whether they administer the org the file is in.
type actor struct {
	// subject is the caller as a file is keyed on (principal.SubjectFrom), or ""
	// for a caller that names none, who owns nothing.
	subject string
	// payer is the account the caller's registrations are charged to.
	payer account.Account
	// admin is a SuperAdmin, or an admin of the org the call acts in.
	admin bool
}

// actorOf reads the caller a typed op was handed, which is the same whichever
// way it came in — REST, MCP's tools/call, the call plane — since every one of
// them dispatches to the op with that context.
//
// The org-admin bit is authority only in the org it was minted for, so it counts
// only where the caller's org IS org, compared whole: a caller who administers
// another org and is re-pointed here (principal.WithActing) holds none. In a held
// org (authz.IsHeldOrg — hanzo, where every self-serve signup is a member) an
// admin of the org is one more stranger to what its members uploaded, as to
// their agents, sessions and machines: there the uploader and a SuperAdmin
// manage a file, and one with no uploader is a SuperAdmin's alone.
func actorOf(ctx context.Context, org string) actor {
	who := cloud.Who(ctx)
	a := actor{admin: principal.SuperFrom(ctx) || (who.OrgAdmin && who.Org == org && !authz.IsHeldOrg(org))}
	if sub, ok := principal.SubjectFrom(ctx); ok {
		a.subject = strings.TrimSpace(sub)
	}
	a.payer = cloud.PayerOf(ctx).Wallet
	return a
}

// manages refuses an actor who may not change or forget f: only its uploader or
// an admin may. A file with no uploader is nobody's, so only an admin manages it.
// The refusal is 403 rather than 404 because the file is the org's and every
// member reads it — reindex refuses a member the same way.
func (a actor) manages(f File) error {
	if a.admin || (a.subject != "" && a.subject == f.uploader) {
		return nil
	}
	return zip.ErrForbidden("only the person who uploaded this file, or an admin of the org, may change or forget it")
}

// tenantStore resolves the caller's org and opens its store: the one opening
// every file op makes.
func tenantStore(ctx context.Context) (string, *store, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", nil, err
	}
	x := idx
	if x == nil {
		return "", nil, zip.Errorf(http.StatusServiceUnavailable, "knowledge is not mounted")
	}
	st, err := x.storeFor(org)
	if err != nil {
		return "", nil, err
	}
	return org, st, nil
}

// typeOf is an object's media type: the store's, unless the store holds only
// the generic default, in which case the one its name implies.
func typeOf(stored, key string) string {
	t := strings.TrimSpace(stored)
	if t != "" && t != "application/octet-stream" && t != "binary/octet-stream" {
		return t
	}
	if byName := mime.TypeByExtension(strings.ToLower(path.Ext(key))); byName != "" {
		return byName
	}
	if t != "" {
		return t
	}
	return "application/octet-stream"
}

// ---- the bus ----

// The names a file is filed under on the event plane: stored when it is
// registered, indexed when its ingest ends ready.
const (
	fileStored  = "file_stored"
	fileIndexed = "file_indexed"
)

// emitBudget bounds one statement on a detached goroutine, waking a cold event
// child included.
const emitBudget = 5 * time.Second

// emitting caps the statements in flight; past it one is dropped, not queued.
var emitting = make(chan struct{}, 32)

// capture is the one plane call, a variable so a test sees what leaves.
var capture = eventpeer.EventCapture

// announce files a file's moment on the org's own event plane: the same write core
// POST /v1/event reaches, so every consumer of the bus (analytics, webhooks, the
// org's own lenses) sees files arrive and land. Detached and bounded — the record
// is already written, and a lost statement is a missing row, not a lost file.
// The name is not sent: the plane is a store every lens reads.
func announce(org, name string, f File) {
	select {
	case emitting <- struct{}{}:
	default:
		return
	}
	call, lg := capture, logger()
	go func() {
		defer func() { <-emitting }()
		defer func() {
			if r := recover(); r != nil {
				lg.Error("stating a file panicked", "err", r)
			}
		}()
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), emitBudget)
		defer cancel()
		_, err := call(ctx, &client.EventIn{
			Name:    name,
			Product: "knowledge",
			Attributes: []client.Signal{
				{Name: "file", Value: f.ID},
				{Name: "type", Value: f.Type},
				{Name: "size", Value: strconv.FormatInt(f.Size, 10)},
			},
		})
		if err != nil && !errors.Is(err, plane.ErrNoPeer) {
			lg.Warn("stating a file failed", "org", org, "file", f.ID, "event", name, "err", err)
		}
	}()
}
