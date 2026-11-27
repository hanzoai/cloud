package compliance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/idv"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is returned when a record does not exist for the org. Handlers map it
// to 404 — and, because every query is org-scoped, a record that belongs to another
// tenant is indistinguishable from one that does not exist (no cross-tenant probe).
var errNotFound = errors.New("compliance: not found")

// subsystem names the store: the system namespace's "compliance". The database and the
// sealer are both made from it, so a column's key and its schema cannot name two things.
const subsystem = "compliance"

// The columns this store SEALS (internal/org, field.go), each with the primary key the sealed
// value is bound to. A sealed column is what the SQLCipher file used to protect for
// free: it is ciphertext on hanzo-sql too, and no WHERE or ORDER BY reads one.
//
//	compliance_subject.ref, .email, .name   the party under verification: who they are
//	compliance_check.verify_url             the provider's hosted flow: whoever holds the
//	                                        link can complete the verification as the party
//	compliance_accreditation.note           a reviewer's free text about a party
//
// Left plain, because a predicate, an index or a join reads them or because they name no
// one: ids (id, org, subject_id, provider_ref, evidence_doc_id, reviewer_sub, decided_by),
// the enums and the timestamps. provider_ref is looked up by equality (the signed
// provider webhook names a check by it) and is not a credential: the webhook is
// authenticated by its HMAC signature, and the reference only says which check to re-read.
var (
	subjectKey, subjectSealed = []string{"org", "id"}, []string{"ref", "email", "name"}
	checkKey, checkSealed     = []string{"org", "id"}, []string{"verify_url"}
	accKey, accSealed         = []string{"org", "id"}, []string{"note"}
)

// Store persists compliance records. ONE database — the system namespace's
// "compliance", a schema on hanzo-sql or a SQLite file born encrypted. Subject PII
// (name/email) is ENCRYPTED AT REST on both: the file is SQLCipher, and the columns
// above are sealed values on either engine, so the same bytes cross from one to the
// other. Tenant isolation is a physical property: `org` is a column on every table and
// every read and write is filtered by it. Its SQL is the dialect both engines share
// (sdk/dialect); on SQLite MaxOpenConns(1) serializes writes.
type Store struct {
	db     *sql.DB
	fields *tenure.Fields
}

// openStore opens the database and the sealed columns' keys for the system namespace's
// "compliance". master is cloud.Deps.MasterKey; with none the store does not open, so no
// column is ever written in the clear because a key was missing.
func openStore(dir string, master []byte) (*Store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("compliance: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("compliance: %w", err)
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, fields: fields}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
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
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
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
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL,
  decided_at   BIGINT NOT NULL DEFAULT 0
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
  expires_at      BIGINT NOT NULL DEFAULT 0,
  created_at      BIGINT NOT NULL,
  updated_at      BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_compliance_acc_org ON compliance_accreditation(org, created_at);
`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(s.db)
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("compliance migrate: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	// A SQLite file from before the columns were sealed holds them in plaintext. Seal them
	// in place, before any read, so the store never reads a value it did not write and
	// cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed, and Upgrade leaves
	// them alone. It is a walk in small transactions of its own, after the schema has
	// committed.
	for _, t := range []struct {
		table       string
		key, sealed []string
	}{
		{"compliance_subject", subjectKey, subjectSealed},
		{"compliance_check", checkKey, checkSealed},
		{"compliance_accreditation", accKey, accSealed},
	} {
		if _, err := s.fields.Upgrade(context.Background(), s.db, t.table, t.key, t.sealed); err != nil {
			return fmt.Errorf("compliance migrate: %w", err)
		}
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ---- subjects ----

func (s *Store) CreateSubject(ctx context.Context, sub Subject) error {
	r := s.fields.Row("compliance_subject", sub.Org, sub.ID)
	ref, email, name := r.Seal("ref", sub.Ref), r.Seal("email", sub.Email), r.Seal("name", sub.Name)
	if err := r.Err(); err != nil {
		return fmt.Errorf("create subject: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO compliance_subject (id, org, kind, ref, email, name, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		sub.ID, sub.Org, string(sub.Kind), ref, email, name, sub.CreatedAt, sub.UpdatedAt)
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
		   FROM compliance_subject WHERE org=$1 AND id=$2`, org, id).
		Scan(&sub.ID, &sub.Org, &kind, &sub.Ref, &sub.Email, &sub.Name, &sub.CreatedAt, &sub.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Subject{}, errNotFound
	}
	if err != nil {
		return Subject{}, fmt.Errorf("get subject: %w", err)
	}
	sub.Kind = SubjectKind(kind)
	if err := s.openSubject(&sub); err != nil {
		return Subject{}, fmt.Errorf("get subject: %w", err)
	}
	return sub, nil
}

// openSubject opens the sealed fields of a subject as the row delivered them. The row
// key is read off the row itself, so a ciphertext carried to another row or tenant
// fails here rather than answering with someone else's name.
func (s *Store) openSubject(sub *Subject) error {
	r := s.fields.Row("compliance_subject", sub.Org, sub.ID)
	sub.Ref, sub.Email, sub.Name = r.Open("ref", sub.Ref), r.Open("email", sub.Email), r.Open("name", sub.Name)
	return r.Err()
}

func (s *Store) ListSubjects(ctx context.Context, org string, limit int) ([]Subject, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, kind, ref, email, name, created_at, updated_at
		   FROM compliance_subject WHERE org=$1 ORDER BY created_at DESC LIMIT $2`, org, capLimit(limit))
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
		if err := s.openSubject(&sub); err != nil {
			return nil, fmt.Errorf("scan subject: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// ---- checks ----

func (s *Store) CreateCheck(ctx context.Context, chk Check) error {
	r := s.fields.Row("compliance_check", chk.Org, chk.ID)
	verifyURL := r.Seal("verify_url", chk.VerifyURL)
	if err := r.Err(); err != nil {
		return fmt.Errorf("create check: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO compliance_check (id, org, subject_id, kind, provider, provider_ref, verify_url, status, decided_by, created_at, updated_at, decided_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		chk.ID, chk.Org, chk.SubjectID, string(chk.Kind), chk.Provider, chk.ProviderRef, verifyURL,
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
	r := s.fields.Row("compliance_check", chk.Org, chk.ID)
	chk.VerifyURL = r.Open("verify_url", chk.VerifyURL)
	if err := r.Err(); err != nil {
		return Check{}, err
	}
	return chk, nil
}

const checkCols = `id, org, subject_id, kind, provider, provider_ref, verify_url, status, decided_by, created_at, updated_at, decided_at`

func (s *Store) GetCheck(ctx context.Context, org, id string) (Check, error) {
	chk, err := s.scanCheck(s.db.QueryRowContext(ctx,
		`SELECT `+checkCols+` FROM compliance_check WHERE org=$1 AND id=$2`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Check{}, errNotFound
	}
	if err != nil {
		return Check{}, fmt.Errorf("get check: %w", err)
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
		`SELECT `+checkCols+` FROM compliance_check WHERE provider_ref=$1`, providerRef))
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
		`UPDATE compliance_check SET status=$1, decided_by=$2, updated_at=$3, decided_at=$4 WHERE org=$5 AND id=$6`,
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
		`SELECT `+checkCols+` FROM compliance_check WHERE org=$1 ORDER BY created_at DESC LIMIT $2`, org, capLimit(limit))
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
		`SELECT status, COUNT(*) FROM compliance_check WHERE org=$1 GROUP BY status`, org)
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
	r := s.fields.Row("compliance_accreditation", a.Org, a.ID)
	note := r.Seal("note", a.Note)
	if err := r.Err(); err != nil {
		return fmt.Errorf("create accreditation: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO compliance_accreditation (id, org, subject_id, method, basis, status, evidence_doc_id, reviewer_sub, note, expires_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		a.ID, a.Org, a.SubjectID, string(a.Method), string(a.Basis), string(a.Status),
		a.EvidenceDocID, a.ReviewerSub, note, a.ExpiresAt, a.CreatedAt, a.UpdatedAt)
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
	r := s.fields.Row("compliance_accreditation", a.Org, a.ID)
	a.Note = r.Open("note", a.Note)
	if err := r.Err(); err != nil {
		return Accreditation{}, err
	}
	return a, nil
}

func (s *Store) GetAccreditation(ctx context.Context, org, id string) (Accreditation, error) {
	a, err := s.scanAcc(s.db.QueryRowContext(ctx,
		`SELECT `+accCols+` FROM compliance_accreditation WHERE org=$1 AND id=$2`, org, id))
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
		`UPDATE compliance_accreditation SET status=$1, reviewer_sub=$2, updated_at=$3 WHERE org=$4 AND id=$5`,
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
		`SELECT `+accCols+` FROM compliance_accreditation WHERE org=$1 ORDER BY created_at DESC LIMIT $2`, org, capLimit(limit))
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
