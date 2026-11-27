package legal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/tenure"
	// locus.Open is the ONE opener: with CLOUD_SQL_DSN set the database is a schema
	// on hanzo-sql, and otherwise it is born encrypted under the key cek derives from
	// the process master and the system namespace, with the single-connection cap
	// already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// errNotFound is returned when a record does not exist for the org. Every query is
// org-scoped, so another tenant's record is indistinguishable from a missing one.
var errNotFound = errors.New("legal: not found")

// Store persists legal templates (org overrides), generated documents, and filings.
// ONE database — the deployment's own "legal", an encrypted SQLite file or a schema on
// hanzo-sql — because a rendered contract carries names and terms. `org` scopes every
// table and every query. Its SQL is the dialect both engines share (sdk/dialect),
// and its tests run on both (internal/sqltest).
//
// The rendered contract (legal_document.body) and a filing's note are SEALED columns
// (internal/org, field.go) on both engines, bound to the row's (org, id): the body names
// the parties and their terms, the note is a reviewer's free text about a filing. No
// WHERE or ORDER BY reads either. Left plain: ids, the category and status vocabularies,
// the template's title, the e-sign provider and its reference, who signed (an IAM user
// id), and the times. An org's template override (legal_template.body) is tenant-authored
// boilerplate with no party in it (a party enters at render) and is the same class as a
// prompt: it stays plain until the owner decides on tenant content.
type Store struct {
	db     *sql.DB
	d      dialect.Dialect
	fields *tenure.Fields
}

// subsystem names the store: the system namespace's "legal". The database and the sealer
// are both made from it, so a column's key and its schema cannot name two things.
const subsystem = "legal"

var (
	docKey, docSealed       = []string{"org", "id"}, []string{"body"}
	filingKey, filingSealed = []string{"org", "id"}, []string{"note"}
)

// openStore opens the database and the sealed columns' keys. master is
// cloud.Deps.MasterKey; with none the store does not open, so no contract is ever
// written in the clear because a key was missing.
func openStore(dir string, master []byte) (*Store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("legal: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("legal: %w", err)
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, d: dialect.Of(db), fields: fields}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
			return nil, err
		}
		// A SQLite file from before the columns were sealed holds them in plaintext. Seal
		// them in place, before any read, so the store never reads a value it did not
		// write and cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed, and
		// Upgrade leaves them alone. It is a walk in small transactions of its own, after
		// the schema has committed.
		for _, t := range []struct {
			table       string
			key, sealed []string
		}{
			{"legal_document", docKey, docSealed},
			{"legal_filing", filingKey, filingSealed},
		} {
			if _, err := s.fields.Upgrade(context.Background(), db, t.table, t.key, t.sealed); err != nil {
				return nil, fmt.Errorf("legal migrate: %w", err)
			}
		}
		return s, nil
	})
}

func (s *Store) migrate(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS legal_template (
  org            TEXT NOT NULL,
  template_id    TEXT NOT NULL,
  version        BIGINT NOT NULL,
  category       TEXT NOT NULL,
  title          TEXT NOT NULL,
  counsel_review BIGINT NOT NULL DEFAULT 0,
  fields_json    TEXT NOT NULL DEFAULT '[]',
  body           TEXT NOT NULL,
  created_at     BIGINT NOT NULL,
  PRIMARY KEY (org, template_id, version)
);

CREATE TABLE IF NOT EXISTS legal_document (
  id               TEXT PRIMARY KEY,
  org              TEXT NOT NULL,
  template_id      TEXT NOT NULL,
  template_version BIGINT NOT NULL,
  category         TEXT NOT NULL,
  title            TEXT NOT NULL,
  content_type     TEXT NOT NULL DEFAULT 'text/markdown',
  body             TEXT NOT NULL,
  status           TEXT NOT NULL,
  esign_provider   TEXT NOT NULL DEFAULT '',
  esign_ref        TEXT NOT NULL DEFAULT '',
  created_at       BIGINT NOT NULL,
  updated_at       BIGINT NOT NULL,
  signed_at        BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_legal_document_org ON legal_document(org, created_at);

CREATE TABLE IF NOT EXISTS legal_filing (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  document_ids  TEXT NOT NULL DEFAULT '[]',
  jurisdiction  TEXT NOT NULL DEFAULT '',
  provider      TEXT NOT NULL,
  status        TEXT NOT NULL,
  note          TEXT NOT NULL DEFAULT '',
  created_at    BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_legal_filing_org ON legal_filing(org, created_at);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("legal migrate: %w", err)
	}
	// signed_by is who recorded a signature complete. It came after the table, so a
	// file that predates it gains it here.
	have, err := s.d.HasColumn(ctx, tx, "legal_document", "signed_by")
	if err != nil {
		return fmt.Errorf("legal migrate: %w", err)
	}
	if !have {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE legal_document ADD COLUMN signed_by TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("legal migrate: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---- template overrides (versioned) ----

// nextTemplateVersion returns the next version for an org's template id: one past the
// current max override version, or 2 (a builtin is version 1, so the first override
// is version 2 — versions never collide with the builtin).
func nextTemplateVersion(ctx context.Context, tx *sql.Tx, org, templateID string) (int, error) {
	var max sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT MAX(version) FROM legal_template WHERE org=$1 AND template_id=$2`, org, templateID).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("next version: %w", err)
	}
	if !max.Valid {
		return 2, nil
	}
	return int(max.Int64) + 1, nil
}

// SaveTemplateOverride persists a new version of an org's template and returns it.
func (s *Store) SaveTemplateOverride(ctx context.Context, org string, t Template) (Template, error) {
	fieldsJSON, err := json.Marshal(t.Fields)
	if err != nil {
		return Template{}, fmt.Errorf("encode fields: %w", err)
	}
	if err := dialect.Text(org, t.ID, string(t.Category), t.Title, string(fieldsJSON), t.Body); err != nil {
		return Template{}, fmt.Errorf("save template override: %w", err)
	}
	// The next version is read and then written, so two saves cannot take the same.
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return Template{}, fmt.Errorf("save template override: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	version, err := nextTemplateVersion(ctx, tx, org, t.ID)
	if err != nil {
		return Template{}, err
	}
	t.Version, t.Origin = version, "org"
	_, err = tx.ExecContext(ctx,
		`INSERT INTO legal_template (org, template_id, version, category, title, counsel_review, fields_json, body, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		org, t.ID, t.Version, string(t.Category), t.Title, boolInt(t.CounselReview), string(fieldsJSON), t.Body, nowUnix())
	if err != nil {
		return Template{}, fmt.Errorf("save template override: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Template{}, fmt.Errorf("save template override: %w", err)
	}
	return t, nil
}

// latestOverride returns the org's latest override for a template id, or false.
func (s *Store) latestOverride(ctx context.Context, org, templateID string) (Template, bool, error) {
	var t Template
	var category, fieldsJSON string
	var counsel int
	err := s.db.QueryRowContext(ctx,
		`SELECT template_id, version, category, title, counsel_review, fields_json, body
		   FROM legal_template WHERE org=$1 AND template_id=$2
		   ORDER BY version DESC LIMIT 1`, org, templateID).
		Scan(&t.ID, &t.Version, &category, &t.Title, &counsel, &fieldsJSON, &t.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, false, nil
	}
	if err != nil {
		return Template{}, false, fmt.Errorf("latest override: %w", err)
	}
	t.Category, t.CounselReview, t.Origin = Category(category), counsel != 0, "org"
	_ = json.Unmarshal([]byte(fieldsJSON), &t.Fields)
	return t, true, nil
}

// ResolveTemplate returns the ACTIVE template for an org: its latest override if any,
// else the built-in. errNotFound when neither exists.
func (s *Store) ResolveTemplate(ctx context.Context, org, templateID string) (Template, error) {
	if t, ok, err := s.latestOverride(ctx, org, templateID); err != nil {
		return Template{}, err
	} else if ok {
		return t, nil
	}
	if t, ok := builtin(templateID); ok {
		return t, nil
	}
	return Template{}, errNotFound
}

// ResolveCatalog returns the org's active catalog: every built-in, with the org's
// overrides applied, plus any org-only templates.
func (s *Store) ResolveCatalog(ctx context.Context, org string) ([]Template, error) {
	catalog := map[string]Template{}
	for _, t := range Builtins() {
		catalog[t.ID] = t
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.template_id, t.version, t.category, t.title, t.counsel_review, t.fields_json, t.body
		   FROM legal_template t
		   JOIN (SELECT template_id, MAX(version) v FROM legal_template WHERE org=$1 GROUP BY template_id) m
		     ON t.template_id=m.template_id AND t.version=m.v
		  WHERE t.org=$1`, org)
	if err != nil {
		return nil, fmt.Errorf("resolve catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t Template
		var category, fieldsJSON string
		var counsel int
		if err := rows.Scan(&t.ID, &t.Version, &category, &t.Title, &counsel, &fieldsJSON, &t.Body); err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		t.Category, t.CounselReview, t.Origin = Category(category), counsel != 0, "org"
		_ = json.Unmarshal([]byte(fieldsJSON), &t.Fields)
		catalog[t.ID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(catalog))
	for _, t := range catalog {
		out = append(out, t)
	}
	return out, nil
}

// ---- documents ----

func (s *Store) CreateDocument(ctx context.Context, d Document) error {
	if err := dialect.Text(d.ID, d.Org, d.TemplateID, string(d.Category), d.Title, d.ContentType, d.Body,
		string(d.Status), d.EsignProvider, d.EsignRef, d.SignedBy); err != nil {
		return fmt.Errorf("create document: %w", err)
	}
	r := s.fields.Row("legal_document", d.Org, d.ID)
	body := r.Seal("body", d.Body)
	if err := r.Err(); err != nil {
		return fmt.Errorf("create document: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO legal_document (id, org, template_id, template_version, category, title, content_type, body, status, esign_provider, esign_ref, created_at, updated_at, signed_at, signed_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		d.ID, d.Org, d.TemplateID, d.TemplateVersion, string(d.Category), d.Title, d.ContentType, body,
		string(d.Status), d.EsignProvider, d.EsignRef, d.CreatedAt, d.UpdatedAt, d.SignedAt, d.SignedBy)
	if err != nil {
		return fmt.Errorf("create document: %w", err)
	}
	return nil
}

const docCols = `id, org, template_id, template_version, category, title, content_type, body, status, esign_provider, esign_ref, created_at, updated_at, signed_at, signed_by`

func (s *Store) scanDoc(row interface{ Scan(...any) error }) (Document, error) {
	var d Document
	var category, status string
	if err := row.Scan(&d.ID, &d.Org, &d.TemplateID, &d.TemplateVersion, &category, &d.Title, &d.ContentType,
		&d.Body, &status, &d.EsignProvider, &d.EsignRef, &d.CreatedAt, &d.UpdatedAt, &d.SignedAt, &d.SignedBy); err != nil {
		return Document{}, err
	}
	d.Category, d.Status = Category(category), DocStatus(status)
	// The row key is read off the row itself, so a ciphertext carried to another document
	// or tenant fails here rather than answering with someone else's contract.
	r := s.fields.Row("legal_document", d.Org, d.ID)
	d.Body = r.Open("body", d.Body)
	if err := r.Err(); err != nil {
		return Document{}, err
	}
	return d, nil
}

func (s *Store) GetDocument(ctx context.Context, org, id string) (Document, error) {
	d, err := s.scanDoc(s.db.QueryRowContext(ctx, `SELECT `+docCols+` FROM legal_document WHERE org=$1 AND id=$2`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, errNotFound
	}
	if err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return d, nil
}

// UpdateDocumentSign moves a document's signature state. signedBy is the validated
// IAM user who recorded it complete — empty for every move but completion.
func (s *Store) UpdateDocumentSign(ctx context.Context, org, id string, status DocStatus, provider, ref string, updatedAt, signedAt int64, signedBy string) error {
	if err := dialect.Text(string(status), provider, ref, signedBy); err != nil {
		return fmt.Errorf("update document: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE legal_document SET status=$1, esign_provider=$2, esign_ref=$3, updated_at=$4, signed_at=$5, signed_by=$6 WHERE org=$7 AND id=$8`,
		string(status), provider, ref, updatedAt, signedAt, signedBy, org, id)
	if err != nil {
		return fmt.Errorf("update document: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) ListDocuments(ctx context.Context, org string, limit int) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+docCols+` FROM legal_document WHERE org=$1 ORDER BY created_at DESC LIMIT $2`, org, capLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Document
	for rows.Next() {
		d, err := s.scanDoc(rows)
		if err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- filings ----

func (s *Store) CreateFiling(ctx context.Context, f Filing) error {
	ids, _ := json.Marshal(f.DocumentIDs)
	if err := dialect.Text(f.ID, f.Org, string(ids), f.Jurisdiction, f.Provider, string(f.Status), f.Note); err != nil {
		return fmt.Errorf("create filing: %w", err)
	}
	r := s.fields.Row("legal_filing", f.Org, f.ID)
	note := r.Seal("note", f.Note)
	if err := r.Err(); err != nil {
		return fmt.Errorf("create filing: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO legal_filing (id, org, document_ids, jurisdiction, provider, status, note, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		f.ID, f.Org, string(ids), f.Jurisdiction, f.Provider, string(f.Status), note, f.CreatedAt, f.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create filing: %w", err)
	}
	return nil
}

func (s *Store) ListFilings(ctx context.Context, org string, limit int) ([]Filing, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, document_ids, jurisdiction, provider, status, note, created_at, updated_at
		   FROM legal_filing WHERE org=$1 ORDER BY created_at DESC LIMIT $2`, org, capLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list filings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Filing
	for rows.Next() {
		var f Filing
		var ids, status string
		if err := rows.Scan(&f.ID, &f.Org, &ids, &f.Jurisdiction, &f.Provider, &status, &f.Note, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan filing: %w", err)
		}
		f.Status = FilingStatus(status)
		_ = json.Unmarshal([]byte(ids), &f.DocumentIDs)
		r := s.fields.Row("legal_filing", f.Org, f.ID)
		f.Note = r.Open("note", f.Note)
		if err := r.Err(); err != nil {
			return nil, fmt.Errorf("scan filing: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func capLimit(limit int) int {
	switch {
	case limit <= 0:
		return 100
	case limit > 1000:
		return 1000
	default:
		return limit
	}
}
