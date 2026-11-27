package captable

import (
	"context"
	"database/sql"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// schema is the per-tenant DDL — the Go host owns migrations; the goja bundle only
// issues SQL against these tables. Column names MUST match the SQL in the captable
// bundle (github.com/hanzoai/captable goja/src/routes/*). Idempotent (IF NOT
// EXISTS), so it runs on every tenant store open via NewBase, on a SQLite file and
// on a schema of hanzo-sql alike. A tenant file made before it said BIGINT and
// DOUBLE PRECISION is left as it is: SQLite gives both the affinity INTEGER and REAL
// gave.
//
// This is the Prisma data model (prisma/schema.prisma) translated to SQL:
// DateTime → TEXT (ISO strings stored verbatim; the bundle never parses them),
// Boolean → BIGINT 0/1, created_at/updated_at → BIGINT unix millis, and the
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
  created_at            BIGINT NOT NULL,
  updated_at            BIGINT NOT NULL
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
  created_at           BIGINT NOT NULL,
  updated_at           BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_stakeholder_company_email ON stakeholder(company_id, email);
CREATE INDEX IF NOT EXISTS ix_stakeholder_company ON stakeholder(company_id);

CREATE TABLE IF NOT EXISTS share_class (
  id                              TEXT PRIMARY KEY,
  company_id                      TEXT NOT NULL,
  idx                             BIGINT NOT NULL,
  name                            TEXT NOT NULL,
  class_type                      TEXT NOT NULL DEFAULT 'COMMON',
  prefix                          TEXT NOT NULL DEFAULT 'CS',
  initial_shares_authorized       BIGINT NOT NULL DEFAULT 0,
  board_approval_date             TEXT,
  stockholder_approval_date       TEXT,
  votes_per_share                 BIGINT NOT NULL DEFAULT 0,
  par_value                       DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_per_share                 DOUBLE PRECISION NOT NULL DEFAULT 0,
  seniority                       BIGINT NOT NULL DEFAULT 0,
  conversion_rights               TEXT NOT NULL DEFAULT 'CONVERTS_TO_FUTURE_ROUND',
  converts_to_share_class_id      TEXT,
  liquidation_preference_multiple DOUBLE PRECISION NOT NULL DEFAULT 0,
  participation_cap_multiple      DOUBLE PRECISION NOT NULL DEFAULT 0,
  created_at                      BIGINT NOT NULL,
  updated_at                      BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_share_class_company_idx ON share_class(company_id, idx);
CREATE INDEX IF NOT EXISTS ix_share_class_company ON share_class(company_id);

CREATE TABLE IF NOT EXISTS equity_plan (
  id                            TEXT PRIMARY KEY,
  company_id                    TEXT NOT NULL,
  name                          TEXT NOT NULL,
  board_approval_date           TEXT,
  plan_effective_date           TEXT,
  initial_shares_reserved       BIGINT NOT NULL DEFAULT 0,
  default_cancellation_behavior TEXT NOT NULL DEFAULT 'RETIRE',
  share_class_id                TEXT NOT NULL,
  comments                      TEXT,
  created_at                    BIGINT NOT NULL,
  updated_at                    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_equity_plan_company ON equity_plan(company_id);

CREATE TABLE IF NOT EXISTS share (
  id                   TEXT PRIMARY KEY,
  company_id           TEXT NOT NULL,
  stakeholder_id       TEXT NOT NULL,
  share_class_id       TEXT NOT NULL,
  status               TEXT NOT NULL DEFAULT 'DRAFT',
  certificate_id       TEXT NOT NULL,
  quantity             BIGINT NOT NULL DEFAULT 0,
  price_per_share      DOUBLE PRECISION,
  capital_contribution DOUBLE PRECISION,
  ip_contribution      DOUBLE PRECISION,
  debt_cancelled       DOUBLE PRECISION,
  other_contributions  DOUBLE PRECISION,
  cliff_years          BIGINT NOT NULL DEFAULT 0,
  vesting_years        BIGINT NOT NULL DEFAULT 0,
  company_legends      TEXT NOT NULL DEFAULT '[]',
  issue_date           TEXT,
  rule144_date         TEXT,
  vesting_start_date   TEXT,
  board_approval_date  TEXT,
  created_at           BIGINT NOT NULL,
  updated_at           BIGINT NOT NULL
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
  quantity            BIGINT NOT NULL DEFAULT 0,
  exercise_price      DOUBLE PRECISION NOT NULL DEFAULT 0,
  type                TEXT NOT NULL DEFAULT 'ISO',
  status              TEXT NOT NULL DEFAULT 'DRAFT',
  cliff_years         BIGINT NOT NULL DEFAULT 0,
  vesting_years       BIGINT NOT NULL DEFAULT 0,
  issue_date          TEXT,
  expiration_date     TEXT,
  vesting_start_date  TEXT,
  board_approval_date TEXT,
  rule144_date        TEXT,
  created_at          BIGINT NOT NULL,
  updated_at          BIGINT NOT NULL
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
  capital             DOUBLE PRECISION NOT NULL DEFAULT 0,
  valuation_cap       DOUBLE PRECISION,
  discount_rate       DOUBLE PRECISION,
  mfn                 BIGINT NOT NULL DEFAULT 0,
  pro_rata            BIGINT NOT NULL DEFAULT 0,
  additional_terms    TEXT,
  issue_date          TEXT,
  board_approval_date TEXT,
  created_at          BIGINT NOT NULL,
  updated_at          BIGINT NOT NULL
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
  capital             DOUBLE PRECISION NOT NULL DEFAULT 0,
  conversion_cap      DOUBLE PRECISION,
  discount_rate       DOUBLE PRECISION,
  mfn                 BIGINT NOT NULL DEFAULT 0,
  interest_rate       DOUBLE PRECISION,
  additional_terms    TEXT,
  issue_date          TEXT,
  board_approval_date TEXT,
  created_at          BIGINT NOT NULL,
  updated_at          BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_note_company_public ON convertible_note(company_id, public_id);
CREATE INDEX IF NOT EXISTS ix_note_company ON convertible_note(company_id);

CREATE TABLE IF NOT EXISTS round (
  id                  TEXT PRIMARY KEY,
  company_id          TEXT NOT NULL,
  name                TEXT NOT NULL,
  round_type          TEXT NOT NULL DEFAULT 'PRICED',
  status              TEXT NOT NULL DEFAULT 'OPEN',
  target_amount       DOUBLE PRECISION NOT NULL DEFAULT 0,
  raised_amount       DOUBLE PRECISION NOT NULL DEFAULT 0,
  pre_money_valuation DOUBLE PRECISION,
  price_per_share     DOUBLE PRECISION,
  share_class_id      TEXT,
  close_date          TEXT,
  created_at          BIGINT NOT NULL,
  updated_at          BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_round_company ON round(company_id);

CREATE TABLE IF NOT EXISTS investment (
  id             TEXT PRIMARY KEY,
  company_id     TEXT NOT NULL,
  round_id       TEXT NOT NULL,
  stakeholder_id TEXT NOT NULL,
  share_class_id TEXT,
  amount         DOUBLE PRECISION NOT NULL DEFAULT 0,
  shares         BIGINT NOT NULL DEFAULT 0,
  date           TEXT,
  comments       TEXT,
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_investment_company ON investment(company_id);
CREATE INDEX IF NOT EXISTS ix_investment_round   ON investment(round_id);
`

// migrate is the NewBase Migrate hook: the schema, in the transaction that holds the
// store's migration lock.
func migrate(ctx context.Context, tx *sql.Tx, _ dialect.Dialect) error {
	_, err := tx.ExecContext(ctx, schema)
	return err
}

// seedCompany is the NewBase OnOpen hook: it ensures the tenant's cap-table
// company row exists (id == the validated tenant), so the bundle's companyId
// always resolves. The name defaults to the tenant and is renamed via
// PUT /v1/captable/company. ON CONFLICT DO NOTHING makes it idempotent across
// reopens.
func seedCompany(ctx context.Context, tenant string, db *sql.DB) error {
	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx,
		`INSERT INTO company
		   (id, name, public_id, incorporation_type, incorporation_country,
		    incorporation_state, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (id) DO NOTHING`,
		tenant, tenant, "", "", "", "", now, now)
	return err
}
