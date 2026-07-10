package sign

// schema is the per-tenant SQLite DDL gojabase runs (idempotently) on first open
// of each tenant's {DataDir}/sign/{org}.db. It is a faithful, lean projection of
// the Documenso Envelope/DocumentData/Recipient/Field/Signature/DocumentAuditLog
// models onto one PDF per document — the complete single-document signing flow.
// Ids are application-generated TEXT (via the gojabase __newId() host-fn), so no
// autoincrement crosses the host boundary. Tenant isolation is the per-tenant
// FILE (gojabase), so there is no org column — the bundle only ever sees its own
// tenant's DB.
const schema = `
CREATE TABLE IF NOT EXISTS document_data (
  id           TEXT PRIMARY KEY,
  type         TEXT NOT NULL DEFAULT 'BYTES_64',
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
  internal_version INTEGER NOT NULL DEFAULT 2,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  completed_at     INTEGER
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
  signing_order    INTEGER,
  read_status      TEXT NOT NULL DEFAULT 'NOT_OPENED',
  signing_status   TEXT NOT NULL DEFAULT 'NOT_SIGNED',
  send_status      TEXT NOT NULL DEFAULT 'NOT_SENT',
  signed_at        INTEGER,
  rejection_reason TEXT,
  created_at       INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_recipients_token ON recipients(token);
CREATE INDEX IF NOT EXISTS ix_recipients_document ON recipients(document_id);

CREATE TABLE IF NOT EXISTS fields (
  id           TEXT PRIMARY KEY,
  document_id  TEXT NOT NULL,
  recipient_id TEXT NOT NULL,
  type         TEXT NOT NULL,
  page         INTEGER NOT NULL DEFAULT 1,
  position_x   REAL NOT NULL DEFAULT 0,
  position_y   REAL NOT NULL DEFAULT 0,
  width        REAL NOT NULL DEFAULT -1,
  height       REAL NOT NULL DEFAULT -1,
  custom_text  TEXT NOT NULL DEFAULT '',
  inserted     INTEGER NOT NULL DEFAULT 0,
  field_meta   TEXT,
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_fields_document ON fields(document_id);
CREATE INDEX IF NOT EXISTS ix_fields_recipient ON fields(document_id, recipient_id);

CREATE TABLE IF NOT EXISTS signatures (
  id              TEXT PRIMARY KEY,
  field_id        TEXT NOT NULL,
  recipient_id    TEXT NOT NULL,
  image_base64    TEXT,
  typed_signature TEXT,
  created_at      INTEGER NOT NULL
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
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_audit_document ON audit_logs(document_id, created_at);
`
