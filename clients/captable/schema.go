package captable

import (
	"context"
	"database/sql"
	"time"
)

// schema is the per-tenant SQLite DDL — the Go host owns migrations; the goja
// bundle only issues SQL against these tables. Column names MUST match the SQL in
// the captable bundle (github.com/hanzoai/captable goja/src/routes/*). Idempotent
// (IF NOT EXISTS), so it runs on every tenant DB open via NewBase.
//
// This is the Prisma data model (prisma/schema.prisma) translated to SQLite:
// DateTime → TEXT (ISO strings stored verbatim; the bundle never parses them),
// Boolean → INTEGER 0/1, created_at/updated_at → INTEGER unix millis, and the
// Prisma @@unique constraints become UNIQUE indexes. Every table carries the
// company_id column (== the validated tenant) so a bundle query is scoped both
// physically (one DB file per tenant) and logically (the WHERE company_id = ?).
const schema = `
CREATE TABLE IF NOT EXISTS company (
  id                    TEXT PRIMARY KEY,
  name                  TEXT NOT NULL DEFAULT '',
  public_id             TEXT NOT NULL DEFAULT '',
  incorporation_type    TEXT NOT NULL DEFAULT '',
  incorporation_country TEXT NOT NULL DEFAULT '',
  incorporation_state   TEXT NOT NULL DEFAULT '',
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS stakeholder (
  id                   TEXT PRIMARY KEY,
  company_id           TEXT NOT NULL,
  name                 TEXT NOT NULL,
  email                TEXT NOT NULL,
  institution_name     TEXT,
  stakeholder_type     TEXT NOT NULL DEFAULT 'INDIVIDUAL',
  current_relationship TEXT NOT NULL DEFAULT 'EMPLOYEE',
  tax_id               TEXT,
  street_address       TEXT,
  city                 TEXT,
  state                TEXT,
  zipcode              TEXT,
  country              TEXT NOT NULL DEFAULT 'US',
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_stakeholder_company_email ON stakeholder(company_id, email);
CREATE INDEX IF NOT EXISTS ix_stakeholder_company ON stakeholder(company_id);

CREATE TABLE IF NOT EXISTS share_class (
  id                              TEXT PRIMARY KEY,
  company_id                      TEXT NOT NULL,
  idx                             INTEGER NOT NULL,
  name                            TEXT NOT NULL,
  class_type                      TEXT NOT NULL DEFAULT 'COMMON',
  prefix                          TEXT NOT NULL DEFAULT 'CS',
  initial_shares_authorized       INTEGER NOT NULL DEFAULT 0,
  board_approval_date             TEXT,
  stockholder_approval_date       TEXT,
  votes_per_share                 INTEGER NOT NULL DEFAULT 0,
  par_value                       REAL NOT NULL DEFAULT 0,
  price_per_share                 REAL NOT NULL DEFAULT 0,
  seniority                       INTEGER NOT NULL DEFAULT 0,
  conversion_rights               TEXT NOT NULL DEFAULT 'CONVERTS_TO_FUTURE_ROUND',
  converts_to_share_class_id      TEXT,
  liquidation_preference_multiple REAL NOT NULL DEFAULT 0,
  participation_cap_multiple      REAL NOT NULL DEFAULT 0,
  created_at                      INTEGER NOT NULL,
  updated_at                      INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_share_class_company_idx ON share_class(company_id, idx);
CREATE INDEX IF NOT EXISTS ix_share_class_company ON share_class(company_id);

CREATE TABLE IF NOT EXISTS equity_plan (
  id                            TEXT PRIMARY KEY,
  company_id                    TEXT NOT NULL,
  name                          TEXT NOT NULL,
  board_approval_date           TEXT,
  plan_effective_date           TEXT,
  initial_shares_reserved       INTEGER NOT NULL DEFAULT 0,
  default_cancellation_behavior TEXT NOT NULL DEFAULT 'RETIRE',
  share_class_id                TEXT NOT NULL,
  comments                      TEXT,
  created_at                    INTEGER NOT NULL,
  updated_at                    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_equity_plan_company ON equity_plan(company_id);

CREATE TABLE IF NOT EXISTS share (
  id                   TEXT PRIMARY KEY,
  company_id           TEXT NOT NULL,
  stakeholder_id       TEXT NOT NULL,
  share_class_id       TEXT NOT NULL,
  status               TEXT NOT NULL DEFAULT 'DRAFT',
  certificate_id       TEXT NOT NULL,
  quantity             INTEGER NOT NULL DEFAULT 0,
  price_per_share      REAL,
  capital_contribution REAL,
  ip_contribution      REAL,
  debt_cancelled       REAL,
  other_contributions  REAL,
  cliff_years          INTEGER NOT NULL DEFAULT 0,
  vesting_years        INTEGER NOT NULL DEFAULT 0,
  company_legends      TEXT NOT NULL DEFAULT '[]',
  issue_date           TEXT,
  rule144_date         TEXT,
  vesting_start_date   TEXT,
  board_approval_date  TEXT,
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_share_company_cert ON share(company_id, certificate_id);
CREATE INDEX IF NOT EXISTS ix_share_company     ON share(company_id);
CREATE INDEX IF NOT EXISTS ix_share_stakeholder ON share(stakeholder_id);
CREATE INDEX IF NOT EXISTS ix_share_class       ON share(share_class_id);

CREATE TABLE IF NOT EXISTS option (
  id                  TEXT PRIMARY KEY,
  company_id          TEXT NOT NULL,
  stakeholder_id      TEXT NOT NULL,
  equity_plan_id      TEXT NOT NULL,
  grant_id            TEXT NOT NULL,
  notes               TEXT,
  quantity            INTEGER NOT NULL DEFAULT 0,
  exercise_price      REAL NOT NULL DEFAULT 0,
  type                TEXT NOT NULL DEFAULT 'ISO',
  status              TEXT NOT NULL DEFAULT 'DRAFT',
  cliff_years         INTEGER NOT NULL DEFAULT 0,
  vesting_years       INTEGER NOT NULL DEFAULT 0,
  issue_date          TEXT,
  expiration_date     TEXT,
  vesting_start_date  TEXT,
  board_approval_date TEXT,
  rule144_date        TEXT,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_option_company_grant ON option(company_id, grant_id);
CREATE INDEX IF NOT EXISTS ix_option_company     ON option(company_id);
CREATE INDEX IF NOT EXISTS ix_option_stakeholder ON option(stakeholder_id);

CREATE TABLE IF NOT EXISTS safe (
  id                  TEXT PRIMARY KEY,
  company_id          TEXT NOT NULL,
  stakeholder_id      TEXT NOT NULL,
  public_id           TEXT NOT NULL,
  type                TEXT NOT NULL DEFAULT 'POST_MONEY',
  status              TEXT NOT NULL DEFAULT 'DRAFT',
  capital             REAL NOT NULL DEFAULT 0,
  valuation_cap       REAL,
  discount_rate       REAL,
  mfn                 INTEGER NOT NULL DEFAULT 0,
  pro_rata            INTEGER NOT NULL DEFAULT 0,
  additional_terms    TEXT,
  issue_date          TEXT,
  board_approval_date TEXT,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_safe_company_public ON safe(company_id, public_id);
CREATE INDEX IF NOT EXISTS ix_safe_company ON safe(company_id);

CREATE TABLE IF NOT EXISTS convertible_note (
  id                  TEXT PRIMARY KEY,
  company_id          TEXT NOT NULL,
  stakeholder_id      TEXT NOT NULL,
  public_id           TEXT NOT NULL,
  type                TEXT NOT NULL DEFAULT 'NOTE',
  status              TEXT NOT NULL DEFAULT 'DRAFT',
  capital             REAL NOT NULL DEFAULT 0,
  conversion_cap      REAL,
  discount_rate       REAL,
  mfn                 INTEGER NOT NULL DEFAULT 0,
  interest_rate       REAL,
  additional_terms    TEXT,
  issue_date          TEXT,
  board_approval_date TEXT,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_note_company_public ON convertible_note(company_id, public_id);
CREATE INDEX IF NOT EXISTS ix_note_company ON convertible_note(company_id);

CREATE TABLE IF NOT EXISTS round (
  id                  TEXT PRIMARY KEY,
  company_id          TEXT NOT NULL,
  name                TEXT NOT NULL,
  round_type          TEXT NOT NULL DEFAULT 'PRICED',
  status              TEXT NOT NULL DEFAULT 'OPEN',
  target_amount       REAL NOT NULL DEFAULT 0,
  raised_amount       REAL NOT NULL DEFAULT 0,
  pre_money_valuation REAL,
  price_per_share     REAL,
  share_class_id      TEXT,
  close_date          TEXT,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_round_company ON round(company_id);

CREATE TABLE IF NOT EXISTS investment (
  id             TEXT PRIMARY KEY,
  company_id     TEXT NOT NULL,
  round_id       TEXT NOT NULL,
  stakeholder_id TEXT NOT NULL,
  share_class_id TEXT,
  amount         REAL NOT NULL DEFAULT 0,
  shares         INTEGER NOT NULL DEFAULT 0,
  date           TEXT,
  comments       TEXT,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_investment_company ON investment(company_id);
CREATE INDEX IF NOT EXISTS ix_investment_round   ON investment(round_id);
`

// seedCompany is the NewBase OnOpen hook: it ensures the tenant's cap-table
// company row exists (id == the validated tenant), so the bundle's companyId
// always resolves. The name defaults to the tenant and is renamed via
// PUT /v1/captable/company. INSERT OR IGNORE makes it idempotent across reopens.
func seedCompany(ctx context.Context, tenant string, db *sql.DB) error {
	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO company
		   (id, name, public_id, incorporation_type, incorporation_country,
		    incorporation_state, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		tenant, tenant, "", "", "", "", now, now)
	return err
}
