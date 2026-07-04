package crm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags (cgo →
	// mattn+SQLCipher, encrypted at rest; !cgo → pure-Go modernc). Importing
	// modernc directly instead would double-register "sqlite" under CGO and
	// panic at init. Blank import registers the driver.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors. Handlers map these to HTTP status codes:
//
//	errNotFound → 404, errConflict → 409, errBadRef → 422.
var (
	errNotFound = errors.New("crm: not found")
	errConflict = errors.New("crm: already exists")
	errBadRef   = errors.New("crm: referenced record not found in org")
)

// Store is the CRM database. ONE SQLite file ({DataDir}/crm.db) holds every
// org's records; tenant isolation is the `org` column, enforced on EVERY query.
// This mirrors clients/prompts and clients/eval exactly (the ONE storage
// pattern). MaxOpenConns(1) serializes writes against the single-writer file.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
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
	if err := s.migrateApplications(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the three core CRM tables. Idempotent (IF NOT EXISTS). Every
// table leads its uniqueness + lookup indexes with `org` so tenant isolation is
// a physical property, not just a WHERE clause. Relations (company_id,
// point_of_contact_id) are plain TEXT refs validated in-org at the write layer
// rather than SQL FKs — a contact may be created before its company, and the
// canonical relation store is still per-org SQLite.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS crm_companies (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  domain_name TEXT NOT NULL DEFAULT '',
  employees   INTEGER NOT NULL DEFAULT 0,
  city        TEXT NOT NULL DEFAULT '',
  country     TEXT NOT NULL DEFAULT '',
  arr         INTEGER NOT NULL DEFAULT 0,
  currency    TEXT NOT NULL DEFAULT 'USD',
  icp         INTEGER NOT NULL DEFAULT 0,
  linkedin    TEXT NOT NULL DEFAULT '',
  x_link      TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_crm_companies_org_updated ON crm_companies(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_crm_companies_org_name    ON crm_companies(org, name);

CREATE TABLE IF NOT EXISTS crm_contacts (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  first_name  TEXT NOT NULL DEFAULT '',
  last_name   TEXT NOT NULL DEFAULT '',
  email       TEXT NOT NULL DEFAULT '',
  phone       TEXT NOT NULL DEFAULT '',
  job_title   TEXT NOT NULL DEFAULT '',
  city        TEXT NOT NULL DEFAULT '',
  company_id  TEXT NOT NULL DEFAULT '',
  linkedin    TEXT NOT NULL DEFAULT '',
  x_link      TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_crm_contacts_org_updated ON crm_contacts(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_crm_contacts_org_company ON crm_contacts(org, company_id);

CREATE TABLE IF NOT EXISTS crm_opportunities (
  id                TEXT PRIMARY KEY,
  org               TEXT NOT NULL,
  name              TEXT NOT NULL,
  amount            INTEGER NOT NULL DEFAULT 0,
  currency          TEXT NOT NULL DEFAULT 'USD',
  stage             TEXT NOT NULL DEFAULT 'NEW',
  close_date        INTEGER NOT NULL DEFAULT 0,
  company_id        TEXT NOT NULL DEFAULT '',
  point_of_contact  TEXT NOT NULL DEFAULT '',
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_crm_opps_org_updated ON crm_opportunities(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_crm_opps_org_stage   ON crm_opportunities(org, stage);
CREATE INDEX IF NOT EXISTS ix_crm_opps_org_company ON crm_opportunities(org, company_id);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("crm migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// exists reports whether a row with (org,id) exists in the named table. Used to
// validate relations stay inside the tenant before a write (Red: cross-tenant
// ref would leak existence). table is a package-internal constant, never user
// input — no injection surface.
func (s *Store) exists(ctx context.Context, table, org, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+table+` WHERE org=? AND id=?`, org, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("exists %s: %w", table, err)
	}
	return true, nil
}

// ---- Company ----

// Company is an org-scoped account record, faithful to Twenty's `company`
// standard object (composites flattened to scalar columns for SQLite): ARR is
// minor units (cents) of Currency; ICP is the ideal-customer-profile flag.
type Company struct {
	ID         string `json:"id"`
	Org        string `json:"-"`
	Name       string `json:"name"`
	DomainName string `json:"domainName"`
	Employees  int64  `json:"employees"`
	City       string `json:"city"`
	Country    string `json:"country"`
	ARR        int64  `json:"arr"`
	Currency   string `json:"currency"`
	ICP        bool   `json:"idealCustomerProfile"`
	Linkedin   string `json:"linkedinLink"`
	XLink      string `json:"xLink"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

const companyCols = `id,org,name,domain_name,employees,city,country,arr,currency,icp,linkedin,x_link,created_at,updated_at`

func scanCompany(sc interface{ Scan(...any) error }) (Company, error) {
	var c Company
	var icp int
	err := sc.Scan(&c.ID, &c.Org, &c.Name, &c.DomainName, &c.Employees, &c.City,
		&c.Country, &c.ARR, &c.Currency, &icp, &c.Linkedin, &c.XLink, &c.CreatedAt, &c.UpdatedAt)
	c.ICP = icp != 0
	return c, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) CreateCompany(ctx context.Context, c Company) (Company, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO crm_companies (`+companyCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Org, c.Name, c.DomainName, c.Employees, c.City, c.Country, c.ARR,
		c.Currency, b2i(c.ICP), c.Linkedin, c.XLink, c.CreatedAt, c.UpdatedAt); err != nil {
		return Company{}, fmt.Errorf("insert company: %w", err)
	}
	return c, nil
}

func (s *Store) GetCompany(ctx context.Context, org, id string) (Company, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+companyCols+` FROM crm_companies WHERE org=? AND id=?`, org, id)
	c, err := scanCompany(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Company{}, errNotFound
	}
	if err != nil {
		return Company{}, fmt.Errorf("get company: %w", err)
	}
	return c, nil
}

func (s *Store) ListCompanies(ctx context.Context, org string, limit int) ([]Company, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+companyCols+` FROM crm_companies WHERE org=? ORDER BY updated_at DESC, name ASC LIMIT ?`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list companies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Company, 0, 16)
	for rows.Next() {
		c, err := scanCompany(rows)
		if err != nil {
			return nil, fmt.Errorf("scan company: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateCompany(ctx context.Context, c Company) (Company, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE crm_companies SET name=?,domain_name=?,employees=?,city=?,country=?,arr=?,currency=?,icp=?,linkedin=?,x_link=?,updated_at=? WHERE org=? AND id=?`,
		c.Name, c.DomainName, c.Employees, c.City, c.Country, c.ARR, c.Currency,
		b2i(c.ICP), c.Linkedin, c.XLink, c.UpdatedAt, c.Org, c.ID)
	if err != nil {
		return Company{}, fmt.Errorf("update company: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Company{}, errNotFound
	}
	return s.GetCompany(ctx, c.Org, c.ID)
}

// DeleteCompany removes a company and NULLs any dangling contact/opportunity
// refs to it within the same org (no orphaned foreign refs). One transaction.
func (s *Store) DeleteCompany(ctx context.Context, org, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM crm_companies WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete company: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE crm_contacts SET company_id='' WHERE org=? AND company_id=?`, org, id); err != nil {
		return false, fmt.Errorf("clear contact refs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE crm_opportunities SET company_id='' WHERE org=? AND company_id=?`, org, id); err != nil {
		return false, fmt.Errorf("clear opp refs: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// ---- Contact ----

// Contact is an org-scoped person record, faithful to Twenty's `person`
// standard object (FULL_NAME/EMAILS/PHONES composites flattened). CompanyID is
// an optional in-org relation to a Company.
type Contact struct {
	ID        string `json:"id"`
	Org       string `json:"-"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	JobTitle  string `json:"jobTitle"`
	City      string `json:"city"`
	CompanyID string `json:"companyId"`
	Linkedin  string `json:"linkedinLink"`
	XLink     string `json:"xLink"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

const contactCols = `id,org,first_name,last_name,email,phone,job_title,city,company_id,linkedin,x_link,created_at,updated_at`

func scanContact(sc interface{ Scan(...any) error }) (Contact, error) {
	var c Contact
	err := sc.Scan(&c.ID, &c.Org, &c.FirstName, &c.LastName, &c.Email, &c.Phone,
		&c.JobTitle, &c.City, &c.CompanyID, &c.Linkedin, &c.XLink, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// checkCompanyRef validates an optional company_id belongs to the org.
func (s *Store) checkCompanyRef(ctx context.Context, org, companyID string) error {
	if companyID == "" {
		return nil
	}
	ok, err := s.exists(ctx, "crm_companies", org, companyID)
	if err != nil {
		return err
	}
	if !ok {
		return errBadRef
	}
	return nil
}

func (s *Store) CreateContact(ctx context.Context, c Contact) (Contact, error) {
	if err := s.checkCompanyRef(ctx, c.Org, c.CompanyID); err != nil {
		return Contact{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO crm_contacts (`+contactCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Org, c.FirstName, c.LastName, c.Email, c.Phone, c.JobTitle, c.City,
		c.CompanyID, c.Linkedin, c.XLink, c.CreatedAt, c.UpdatedAt); err != nil {
		return Contact{}, fmt.Errorf("insert contact: %w", err)
	}
	return c, nil
}

func (s *Store) GetContact(ctx context.Context, org, id string) (Contact, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+contactCols+` FROM crm_contacts WHERE org=? AND id=?`, org, id)
	c, err := scanContact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Contact{}, errNotFound
	}
	if err != nil {
		return Contact{}, fmt.Errorf("get contact: %w", err)
	}
	return c, nil
}

// ListContacts lists the org's contacts, optionally filtered to one company
// (companyID=="" means all). Most-recently-updated first.
func (s *Store) ListContacts(ctx context.Context, org, companyID string, limit int) ([]Contact, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if companyID == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+contactCols+` FROM crm_contacts WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+contactCols+` FROM crm_contacts WHERE org=? AND company_id=? ORDER BY updated_at DESC LIMIT ?`, org, companyID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Contact, 0, 16)
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan contact: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateContact(ctx context.Context, c Contact) (Contact, error) {
	if err := s.checkCompanyRef(ctx, c.Org, c.CompanyID); err != nil {
		return Contact{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE crm_contacts SET first_name=?,last_name=?,email=?,phone=?,job_title=?,city=?,company_id=?,linkedin=?,x_link=?,updated_at=? WHERE org=? AND id=?`,
		c.FirstName, c.LastName, c.Email, c.Phone, c.JobTitle, c.City, c.CompanyID,
		c.Linkedin, c.XLink, c.UpdatedAt, c.Org, c.ID)
	if err != nil {
		return Contact{}, fmt.Errorf("update contact: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Contact{}, errNotFound
	}
	return s.GetContact(ctx, c.Org, c.ID)
}

// DeleteContact removes a contact and clears any opportunity point-of-contact
// refs to it within the org. One transaction.
func (s *Store) DeleteContact(ctx context.Context, org, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM crm_contacts WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete contact: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE crm_opportunities SET point_of_contact='' WHERE org=? AND point_of_contact=?`, org, id); err != nil {
		return false, fmt.Errorf("clear opp poc refs: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// ---- Opportunity ----

// Opportunity is an org-scoped deal record, faithful to Twenty's `opportunity`
// standard object. Amount is minor units (cents) of Currency; CloseDate is a
// unix second (0 == unset). Stage is validated against the default pipeline.
type Opportunity struct {
	ID             string `json:"id"`
	Org            string `json:"-"`
	Name           string `json:"name"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
	Stage          string `json:"stage"`
	CloseDate      int64  `json:"closeDate"`
	CompanyID      string `json:"companyId"`
	PointOfContact string `json:"pointOfContactId"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

const oppCols = `id,org,name,amount,currency,stage,close_date,company_id,point_of_contact,created_at,updated_at`

func scanOpp(sc interface{ Scan(...any) error }) (Opportunity, error) {
	var o Opportunity
	err := sc.Scan(&o.ID, &o.Org, &o.Name, &o.Amount, &o.Currency, &o.Stage,
		&o.CloseDate, &o.CompanyID, &o.PointOfContact, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

// checkOppRefs validates the optional company + point-of-contact relations
// belong to the org before a write.
func (s *Store) checkOppRefs(ctx context.Context, org, companyID, poc string) error {
	if err := s.checkCompanyRef(ctx, org, companyID); err != nil {
		return err
	}
	if poc != "" {
		ok, err := s.exists(ctx, "crm_contacts", org, poc)
		if err != nil {
			return err
		}
		if !ok {
			return errBadRef
		}
	}
	return nil
}

func (s *Store) CreateOpportunity(ctx context.Context, o Opportunity) (Opportunity, error) {
	if err := s.checkOppRefs(ctx, o.Org, o.CompanyID, o.PointOfContact); err != nil {
		return Opportunity{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO crm_opportunities (`+oppCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		o.ID, o.Org, o.Name, o.Amount, o.Currency, o.Stage, o.CloseDate,
		o.CompanyID, o.PointOfContact, o.CreatedAt, o.UpdatedAt); err != nil {
		return Opportunity{}, fmt.Errorf("insert opportunity: %w", err)
	}
	return o, nil
}

func (s *Store) GetOpportunity(ctx context.Context, org, id string) (Opportunity, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+oppCols+` FROM crm_opportunities WHERE org=? AND id=?`, org, id)
	o, err := scanOpp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Opportunity{}, errNotFound
	}
	if err != nil {
		return Opportunity{}, fmt.Errorf("get opportunity: %w", err)
	}
	return o, nil
}

// ListOpportunities lists the org's opportunities, optionally filtered by stage
// (stage=="" means all). Most-recently-updated first.
func (s *Store) ListOpportunities(ctx context.Context, org, stage string, limit int) ([]Opportunity, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if stage == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+oppCols+` FROM crm_opportunities WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+oppCols+` FROM crm_opportunities WHERE org=? AND stage=? ORDER BY updated_at DESC LIMIT ?`, org, stage, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list opportunities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Opportunity, 0, 16)
	for rows.Next() {
		o, err := scanOpp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan opportunity: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) UpdateOpportunity(ctx context.Context, o Opportunity) (Opportunity, error) {
	if err := s.checkOppRefs(ctx, o.Org, o.CompanyID, o.PointOfContact); err != nil {
		return Opportunity{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE crm_opportunities SET name=?,amount=?,currency=?,stage=?,close_date=?,company_id=?,point_of_contact=?,updated_at=? WHERE org=? AND id=?`,
		o.Name, o.Amount, o.Currency, o.Stage, o.CloseDate, o.CompanyID,
		o.PointOfContact, o.UpdatedAt, o.Org, o.ID)
	if err != nil {
		return Opportunity{}, fmt.Errorf("update opportunity: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Opportunity{}, errNotFound
	}
	return s.GetOpportunity(ctx, o.Org, o.ID)
}

func (s *Store) DeleteOpportunity(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM crm_opportunities WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete opportunity: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Counts returns per-org row counts across the three entities — a real,
// non-fabricated summary for the CRM module's overview cards.
func (s *Store) Counts(ctx context.Context, org string) (companies, contacts, opps int, err error) {
	q := func(table string) (int, error) {
		var n int
		e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE org=?`, org).Scan(&n)
		return n, e
	}
	if companies, err = q("crm_companies"); err != nil {
		return 0, 0, 0, fmt.Errorf("count companies: %w", err)
	}
	if contacts, err = q("crm_contacts"); err != nil {
		return 0, 0, 0, fmt.Errorf("count contacts: %w", err)
	}
	if opps, err = q("crm_opportunities"); err != nil {
		return 0, 0, 0, fmt.Errorf("count opportunities: %w", err)
	}
	return companies, contacts, opps, nil
}
