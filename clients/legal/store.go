package legal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is returned when a record does not exist for the org. Every query is
// org-scoped, so another tenant's record is indistinguishable from a missing one.
var errNotFound = errors.New("legal: not found")

// Store persists legal templates (org overrides), generated documents, and filings.
// ONE cek-encrypted SQLite file ({DataDir}/legal.db) — a rendered contract carries
// names and terms, so the document body is sealed at rest. `org` scopes every table
// and every query. MaxOpenConns(1) serializes writes.
type Store struct {
	db *sql.DB
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

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS legal_template (
  org            TEXT NOT NULL,
  template_id    TEXT NOT NULL,
  version        INTEGER NOT NULL,
  category       TEXT NOT NULL,
  title          TEXT NOT NULL,
  counsel_review INTEGER NOT NULL DEFAULT 0,
  fields_json    TEXT NOT NULL DEFAULT '[]',
  body           TEXT NOT NULL,
  created_at     INTEGER NOT NULL,
  PRIMARY KEY (org, template_id, version)
);

CREATE TABLE IF NOT EXISTS legal_document (
  id               TEXT PRIMARY KEY,
  org              TEXT NOT NULL,
  template_id      TEXT NOT NULL,
  template_version INTEGER NOT NULL,
  category         TEXT NOT NULL,
  title            TEXT NOT NULL,
  content_type     TEXT NOT NULL DEFAULT 'text/markdown',
  body             TEXT NOT NULL,
  status           TEXT NOT NULL,
  esign_provider   TEXT NOT NULL DEFAULT '',
  esign_ref        TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  signed_at        INTEGER NOT NULL DEFAULT 0
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
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_legal_filing_org ON legal_filing(org, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("legal migrate: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---- template overrides (versioned) ----

// nextTemplateVersion returns the next version for an org's template id: one past the
// current max override version, or 2 (a builtin is version 1, so the first override
// is version 2 — versions never collide with the builtin).
func (s *Store) nextTemplateVersion(ctx context.Context, org, templateID string) (int, error) {
	var max sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM legal_template WHERE org=? AND template_id=?`, org, templateID).Scan(&max)
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
	version, err := s.nextTemplateVersion(ctx, org, t.ID)
	if err != nil {
		return Template{}, err
	}
	t.Version, t.Origin = version, "org"
	fieldsJSON, err := json.Marshal(t.Fields)
	if err != nil {
		return Template{}, fmt.Errorf("encode fields: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO legal_template (org, template_id, version, category, title, counsel_review, fields_json, body, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		org, t.ID, t.Version, string(t.Category), t.Title, boolInt(t.CounselReview), string(fieldsJSON), t.Body, nowUnix())
	if err != nil {
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
		   FROM legal_template WHERE org=? AND template_id=?
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
		   JOIN (SELECT template_id, MAX(version) v FROM legal_template WHERE org=? GROUP BY template_id) m
		     ON t.template_id=m.template_id AND t.version=m.v
		  WHERE t.org=?`, org, org)
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO legal_document (id, org, template_id, template_version, category, title, content_type, body, status, esign_provider, esign_ref, created_at, updated_at, signed_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Org, d.TemplateID, d.TemplateVersion, string(d.Category), d.Title, d.ContentType, d.Body,
		string(d.Status), d.EsignProvider, d.EsignRef, d.CreatedAt, d.UpdatedAt, d.SignedAt)
	if err != nil {
		return fmt.Errorf("create document: %w", err)
	}
	return nil
}

const docCols = `id, org, template_id, template_version, category, title, content_type, body, status, esign_provider, esign_ref, created_at, updated_at, signed_at`

func (s *Store) scanDoc(row interface{ Scan(...any) error }) (Document, error) {
	var d Document
	var category, status string
	if err := row.Scan(&d.ID, &d.Org, &d.TemplateID, &d.TemplateVersion, &category, &d.Title, &d.ContentType,
		&d.Body, &status, &d.EsignProvider, &d.EsignRef, &d.CreatedAt, &d.UpdatedAt, &d.SignedAt); err != nil {
		return Document{}, err
	}
	d.Category, d.Status = Category(category), DocStatus(status)
	return d, nil
}

func (s *Store) GetDocument(ctx context.Context, org, id string) (Document, error) {
	d, err := s.scanDoc(s.db.QueryRowContext(ctx, `SELECT `+docCols+` FROM legal_document WHERE org=? AND id=?`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, errNotFound
	}
	if err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return d, nil
}

func (s *Store) UpdateDocumentSign(ctx context.Context, org, id string, status DocStatus, provider, ref string, updatedAt, signedAt int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE legal_document SET status=?, esign_provider=?, esign_ref=?, updated_at=?, signed_at=? WHERE org=? AND id=?`,
		string(status), provider, ref, updatedAt, signedAt, org, id)
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
		`SELECT `+docCols+` FROM legal_document WHERE org=? ORDER BY created_at DESC LIMIT ?`, org, capLimit(limit))
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO legal_filing (id, org, document_ids, jurisdiction, provider, status, note, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Org, string(ids), f.Jurisdiction, f.Provider, string(f.Status), f.Note, f.CreatedAt, f.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create filing: %w", err)
	}
	return nil
}

func (s *Store) ListFilings(ctx context.Context, org string, limit int) ([]Filing, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, document_ids, jurisdiction, provider, status, note, created_at, updated_at
		   FROM legal_filing WHERE org=? ORDER BY created_at DESC LIMIT ?`, org, capLimit(limit))
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
