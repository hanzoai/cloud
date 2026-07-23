package compliance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/idv"
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is returned when a record does not exist for the org. Handlers map it
// to 404 — and, because every query is org-scoped, a record that belongs to another
// tenant is indistinguishable from one that does not exist (no cross-tenant probe).
var errNotFound = errors.New("compliance: not found")

// Store persists compliance records. ONE SQLite file ({DataDir}/compliance.db),
// opened through cek so subject PII (name/email) is ENCRYPTED AT REST. Tenant
// isolation is a physical property: `org` is a column on every table and every read
// and write is filtered by it. MaxOpenConns(1) serializes writes.
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

// migrate creates the three tables. Idempotent. The org column carries the tenant on
// every row; the indexes serve the org-scoped list + the provider-ref callback lookup.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS compliance_subject (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  kind       TEXT NOT NULL,
  ref        TEXT NOT NULL DEFAULT '',
  email      TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_compliance_subject_org ON compliance_subject(org, created_at);

CREATE TABLE IF NOT EXISTS compliance_check (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  subject_id   TEXT NOT NULL,
  kind         TEXT NOT NULL,
  provider     TEXT NOT NULL,
  provider_ref TEXT NOT NULL DEFAULT '',
  verify_url   TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL,
  decided_by   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  decided_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_compliance_check_org ON compliance_check(org, created_at);
CREATE INDEX IF NOT EXISTS ix_compliance_check_ref ON compliance_check(org, provider_ref);
CREATE INDEX IF NOT EXISTS ix_compliance_check_provider_ref ON compliance_check(provider_ref);

CREATE TABLE IF NOT EXISTS compliance_accreditation (
  id              TEXT PRIMARY KEY,
  org             TEXT NOT NULL,
  subject_id      TEXT NOT NULL,
  method          TEXT NOT NULL,
  basis           TEXT NOT NULL,
  status          TEXT NOT NULL,
  evidence_doc_id TEXT NOT NULL DEFAULT '',
  reviewer_sub    TEXT NOT NULL DEFAULT '',
  note            TEXT NOT NULL DEFAULT '',
  expires_at      INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_compliance_acc_org ON compliance_accreditation(org, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("compliance migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ---- subjects ----

func (s *Store) CreateSubject(ctx context.Context, sub Subject) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO compliance_subject (id, org, kind, ref, email, name, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		sub.ID, sub.Org, string(sub.Kind), sub.Ref, sub.Email, sub.Name, sub.CreatedAt, sub.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create subject: %w", err)
	}
	return nil
}

func (s *Store) GetSubject(ctx context.Context, org, id string) (Subject, error) {
	var sub Subject
	var kind string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org, kind, ref, email, name, created_at, updated_at
		   FROM compliance_subject WHERE org=? AND id=?`, org, id).
		Scan(&sub.ID, &sub.Org, &kind, &sub.Ref, &sub.Email, &sub.Name, &sub.CreatedAt, &sub.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Subject{}, errNotFound
	}
	if err != nil {
		return Subject{}, fmt.Errorf("get subject: %w", err)
	}
	sub.Kind = SubjectKind(kind)
	return sub, nil
}

func (s *Store) ListSubjects(ctx context.Context, org string, limit int) ([]Subject, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, kind, ref, email, name, created_at, updated_at
		   FROM compliance_subject WHERE org=? ORDER BY created_at DESC LIMIT ?`, org, capLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list subjects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Subject
	for rows.Next() {
		var sub Subject
		var kind string
		if err := rows.Scan(&sub.ID, &sub.Org, &kind, &sub.Ref, &sub.Email, &sub.Name, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan subject: %w", err)
		}
		sub.Kind = SubjectKind(kind)
		out = append(out, sub)
	}
	return out, rows.Err()
}

// ---- checks ----

func (s *Store) CreateCheck(ctx context.Context, chk Check) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO compliance_check (id, org, subject_id, kind, provider, provider_ref, verify_url, status, decided_by, created_at, updated_at, decided_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		chk.ID, chk.Org, chk.SubjectID, string(chk.Kind), chk.Provider, chk.ProviderRef, chk.VerifyURL,
		string(chk.Status), chk.DecidedBy, chk.CreatedAt, chk.UpdatedAt, chk.DecidedAt)
	if err != nil {
		return fmt.Errorf("create check: %w", err)
	}
	return nil
}

func (s *Store) scanCheck(row interface{ Scan(...any) error }) (Check, error) {
	var chk Check
	var kind, status string
	if err := row.Scan(&chk.ID, &chk.Org, &chk.SubjectID, &kind, &chk.Provider, &chk.ProviderRef,
		&chk.VerifyURL, &status, &chk.DecidedBy, &chk.CreatedAt, &chk.UpdatedAt, &chk.DecidedAt); err != nil {
		return Check{}, err
	}
	chk.Kind = SubjectKind(kind)
	chk.Status = idv.Status(status)
	return chk, nil
}

const checkCols = `id, org, subject_id, kind, provider, provider_ref, verify_url, status, decided_by, created_at, updated_at, decided_at`

func (s *Store) GetCheck(ctx context.Context, org, id string) (Check, error) {
	chk, err := s.scanCheck(s.db.QueryRowContext(ctx,
		`SELECT `+checkCols+` FROM compliance_check WHERE org=? AND id=?`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Check{}, errNotFound
	}
	if err != nil {
		return Check{}, fmt.Errorf("get check: %w", err)
	}
	return chk, nil
}

// GetCheckByRef finds a check by its provider reference within the org — the lookup a
// provider webhook (correlated by provider ref) needs. Org-scoped so a webhook for
// one tenant can never touch another's record.
func (s *Store) GetCheckByRef(ctx context.Context, org, providerRef string) (Check, error) {
	chk, err := s.scanCheck(s.db.QueryRowContext(ctx,
		`SELECT `+checkCols+` FROM compliance_check WHERE org=? AND provider_ref=?`, org, providerRef))
	if errors.Is(err, sql.ErrNoRows) {
		return Check{}, errNotFound
	}
	if err != nil {
		return Check{}, fmt.Errorf("get check by ref: %w", err)
	}
	return chk, nil
}

// GetCheckByProviderRef finds a check by its provider reference across ALL orgs — the
// lookup a SIGNATURE-authenticated provider webhook needs, since an external provider
// carries no validated org. The provider reference is a cryptographically-unique
// token (128 random bits, minted per verification), so the owning org is resolved FROM
// the matched record: the webhook can only ever touch the one tenant that holds that
// reference, and a caller cannot probe another tenant by guessing (an unknown ref is a
// plain not-found). The org on the returned Check is authoritative for the update.
func (s *Store) GetCheckByProviderRef(ctx context.Context, providerRef string) (Check, error) {
	chk, err := s.scanCheck(s.db.QueryRowContext(ctx,
		`SELECT `+checkCols+` FROM compliance_check WHERE provider_ref=?`, providerRef))
	if errors.Is(err, sql.ErrNoRows) {
		return Check{}, errNotFound
	}
	if err != nil {
		return Check{}, fmt.Errorf("get check by provider ref: %w", err)
	}
	return chk, nil
}

// UpdateCheckStatus settles a check's status. Org-scoped in the WHERE clause so a
// caller can only ever move its own tenant's record.
func (s *Store) UpdateCheckStatus(ctx context.Context, org, id string, status idv.Status, decidedBy string, updatedAt, decidedAt int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE compliance_check SET status=?, decided_by=?, updated_at=?, decided_at=? WHERE org=? AND id=?`,
		string(status), decidedBy, updatedAt, decidedAt, org, id)
	if err != nil {
		return fmt.Errorf("update check: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) ListChecks(ctx context.Context, org string, limit int) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+checkCols+` FROM compliance_check WHERE org=? ORDER BY created_at DESC LIMIT ?`, org, capLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list checks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Check
	for rows.Next() {
		chk, err := s.scanCheck(rows)
		if err != nil {
			return nil, fmt.Errorf("scan check: %w", err)
		}
		out = append(out, chk)
	}
	return out, rows.Err()
}

// StatusCounts returns the per-status tally of checks for the org — the honest
// posture read (counts of provider-reported states), never a boolean "compliant".
func (s *Store) StatusCounts(ctx context.Context, org string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM compliance_check WHERE org=? GROUP BY status`, org)
	if err != nil {
		return nil, fmt.Errorf("status counts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		out[status] = n
	}
	return out, rows.Err()
}

// ---- accreditation ----

func (s *Store) CreateAccreditation(ctx context.Context, a Accreditation) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO compliance_accreditation (id, org, subject_id, method, basis, status, evidence_doc_id, reviewer_sub, note, expires_at, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.SubjectID, string(a.Method), string(a.Basis), string(a.Status),
		a.EvidenceDocID, a.ReviewerSub, a.Note, a.ExpiresAt, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create accreditation: %w", err)
	}
	return nil
}

const accCols = `id, org, subject_id, method, basis, status, evidence_doc_id, reviewer_sub, note, expires_at, created_at, updated_at`

func (s *Store) scanAcc(row interface{ Scan(...any) error }) (Accreditation, error) {
	var a Accreditation
	var method, basis, status string
	if err := row.Scan(&a.ID, &a.Org, &a.SubjectID, &method, &basis, &status,
		&a.EvidenceDocID, &a.ReviewerSub, &a.Note, &a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Accreditation{}, err
	}
	a.Method, a.Basis, a.Status = AccreditationMethod(method), AccreditationBasis(basis), AccreditationStatus(status)
	return a, nil
}

func (s *Store) GetAccreditation(ctx context.Context, org, id string) (Accreditation, error) {
	a, err := s.scanAcc(s.db.QueryRowContext(ctx,
		`SELECT `+accCols+` FROM compliance_accreditation WHERE org=? AND id=?`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Accreditation{}, errNotFound
	}
	if err != nil {
		return Accreditation{}, fmt.Errorf("get accreditation: %w", err)
	}
	return a, nil
}

// UpdateAccreditationDecision records a reviewer's decision on an accreditation
// record. Org-scoped WHERE — a reviewer can only decide within their own tenant.
func (s *Store) UpdateAccreditationDecision(ctx context.Context, org, id string, status AccreditationStatus, reviewerSub string, updatedAt int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE compliance_accreditation SET status=?, reviewer_sub=?, updated_at=? WHERE org=? AND id=?`,
		string(status), reviewerSub, updatedAt, org, id)
	if err != nil {
		return fmt.Errorf("update accreditation: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) ListAccreditation(ctx context.Context, org string, limit int) ([]Accreditation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accCols+` FROM compliance_accreditation WHERE org=? ORDER BY created_at DESC LIMIT ?`, org, capLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list accreditation: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Accreditation
	for rows.Next() {
		a, err := s.scanAcc(rows)
		if err != nil {
			return nil, fmt.Errorf("scan accreditation: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// capLimit bounds a list page: default 100, hard cap 1000, floor 1.
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
