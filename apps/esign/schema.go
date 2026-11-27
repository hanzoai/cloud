package esign

import (
	"context"
	"database/sql"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// numbered are the tables whose rows are listed in the order they were written: a
// signing order that ties, and an audit trail. A file ordered them by rowid, which
// hanzo-sql has no spelling of, so each carries a seq the bundle numbers as it writes
// (dialect.NextSeq). A tenant file made before it gains the column and has its rows
// numbered by rowid, once.
var numbered = []string{"recipients", "audit_logs"}

// migrate is the NewBase Migrate hook: the schema, and the order of the rows a file
// kept for free, in the transaction that holds the store's migration lock.
func migrate(ctx context.Context, tx *sql.Tx, d dialect.Dialect) error {
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	for _, table := range numbered {
		if err := d.Sequence(ctx, tx, table); err != nil {
			return err
		}
		// After the column exists, so a file made without it can be given one: the
		// next number is the largest plus one, and an index is what makes the largest a
		// lookup and not a scan of every row the table has ever held.
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ix_`+table+`_seq ON `+table+`(seq)`); err != nil {
			return err
		}
	}
	return nil
}

// schema is the per-tenant DDL NewBase runs (idempotently) on first open of each
// tenant's store: {DataDir}/orgs/{org}/esign.db, or a schema of hanzo-sql. It is in the
// dialect both share; a file made when these columns said INTEGER and REAL is left as
// it is, since SQLite gives BIGINT and DOUBLE PRECISION the same affinity. A page, a
// signing order and a position are DOUBLE PRECISION because they are whatever number the
// caller sent (the bundle does not round, and a typed field says float64).
//
// It is a faithful, lean projection of the Documenso Envelope/DocumentData/Recipient/Field/Signature/DocumentAuditLog
// models onto one PDF per document — the complete single-document signing flow.
// Ids are application-generated TEXT (via the NewBase __newId() host-fn), so no
// autoincrement crosses the host boundary. Tenant isolation is the per-tenant
// FILE (NewBase), so there is no org column — the bundle only ever sees its own
// tenant's DB.
// PDF BYTES do NOT live in this DB: document_data.data / initial_data hold the
// opaque object-storage blob KEY (type 'BLOB_KEY'), and the bytes live on the
// NewBase __blob client (deps.VFS / S3). A 32 MiB base64 PDF in a TEXT column would
// bloat the per-tenant SQLite and be copied on every read — the same object-store
// client clients/dataroom uses for document bytes. `data` points at the current
// (sealed once completed) PDF; `initial_data` keeps the original.
const schema = `
CREATE TABLE IF NOT EXISTS document_data (
  id           TEXT PRIMARY KEY,
  type         TEXT NOT NULL DEFAULT 'BLOB_KEY',
  data         TEXT NOT NULL,
  initial_data TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS documents (
  id               TEXT PRIMARY KEY,
  title            TEXT NOT NULL,
  status           TEXT NOT NULL DEFAULT 'DRAFT',
  external_id      TEXT,
  source           TEXT NOT NULL DEFAULT 'DOCUMENT',
  signing_order    TEXT NOT NULL DEFAULT 'PARALLEL',
  subject          TEXT,
  message          TEXT,
  document_data_id TEXT NOT NULL,
  internal_version BIGINT NOT NULL DEFAULT 2,
  created_at       BIGINT NOT NULL,
  updated_at       BIGINT NOT NULL,
  completed_at     BIGINT
);
CREATE INDEX IF NOT EXISTS ix_documents_status ON documents(status);
CREATE INDEX IF NOT EXISTS ix_documents_created ON documents(created_at);

CREATE TABLE IF NOT EXISTS recipients (
  id               TEXT PRIMARY KEY,
  document_id      TEXT NOT NULL,
  email            TEXT NOT NULL,
  name             TEXT NOT NULL DEFAULT '',
  token            TEXT NOT NULL,
  role             TEXT NOT NULL DEFAULT 'SIGNER',
  signing_order    DOUBLE PRECISION,
  read_status      TEXT NOT NULL DEFAULT 'NOT_OPENED',
  signing_status   TEXT NOT NULL DEFAULT 'NOT_SIGNED',
  send_status      TEXT NOT NULL DEFAULT 'NOT_SENT',
  signed_at        BIGINT,
  rejection_reason TEXT,
  created_at       BIGINT NOT NULL,
  seq              BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_recipients_token ON recipients(token);
CREATE INDEX IF NOT EXISTS ix_recipients_document ON recipients(document_id);

CREATE TABLE IF NOT EXISTS fields (
  id           TEXT PRIMARY KEY,
  document_id  TEXT NOT NULL,
  recipient_id TEXT NOT NULL,
  type         TEXT NOT NULL,
  page         DOUBLE PRECISION NOT NULL DEFAULT 1,
  position_x   DOUBLE PRECISION NOT NULL DEFAULT 0,
  position_y   DOUBLE PRECISION NOT NULL DEFAULT 0,
  width        DOUBLE PRECISION NOT NULL DEFAULT -1,
  height       DOUBLE PRECISION NOT NULL DEFAULT -1,
  custom_text  TEXT NOT NULL DEFAULT '',
  inserted     BIGINT NOT NULL DEFAULT 0,
  field_meta   TEXT,
  created_at   BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_fields_document ON fields(document_id);
CREATE INDEX IF NOT EXISTS ix_fields_recipient ON fields(document_id, recipient_id);

CREATE TABLE IF NOT EXISTS signatures (
  id              TEXT PRIMARY KEY,
  field_id        TEXT NOT NULL,
  recipient_id    TEXT NOT NULL,
  image_base64    TEXT,
  typed_signature TEXT,
  created_at      BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_signatures_field ON signatures(field_id);

CREATE TABLE IF NOT EXISTS audit_logs (
  id          TEXT PRIMARY KEY,
  document_id TEXT NOT NULL,
  type        TEXT NOT NULL,
  data        TEXT,
  name        TEXT,
  email       TEXT,
  ip_address  TEXT,
  user_agent  TEXT,
  created_at  BIGINT NOT NULL,
  seq         BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_audit_document ON audit_logs(document_id, created_at);
`
