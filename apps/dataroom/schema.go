package dataroom

// schema is the per-tenant SQLite DDL — the Go host owns migrations (NewBase
// runs this on every tenant DB open); the goja bundle only issues SQL against
// these tables. Column names MUST match the SQL in bundle.js. Idempotent
// (IF NOT EXISTS).
//
// This is the dataroom Prisma data model (documents, data rooms + membership,
// shareable links with access controls, viewers, views, per-page analytics)
// translated to SQLite: Boolean → INTEGER 0/1, timestamps → INTEGER unix millis,
// storage references → the opaque object-storage key (bytes live on the cloud
// s3/storage seam, never in Base). Tenant isolation is the per-tenant DB file
// selected by the validated org (there is one file per org, so no org column is
// needed for scoping).
const schema = `
CREATE TABLE IF NOT EXISTS document (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  file_key     TEXT NOT NULL,
  content_type TEXT,
  type         TEXT,
  num_pages    INTEGER,
  file_size    INTEGER,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS dataroom (
  id          TEXT PRIMARY KEY,
  p_id        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  description TEXT,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS dataroom_document (
  id          TEXT PRIMARY KEY,
  dataroom_id TEXT NOT NULL,
  document_id TEXT NOT NULL,
  order_index INTEGER,
  created_at  INTEGER NOT NULL,
  UNIQUE(dataroom_id, document_id)
);
CREATE INDEX IF NOT EXISTS ix_dd_room ON dataroom_document(dataroom_id);

CREATE TABLE IF NOT EXISTS link (
  id              TEXT PRIMARY KEY,
  link_type       TEXT NOT NULL DEFAULT 'DATAROOM_LINK',
  dataroom_id     TEXT,
  document_id     TEXT,
  name            TEXT,
  password_hash   TEXT,
  email_protected INTEGER NOT NULL DEFAULT 1,
  allow_list      TEXT NOT NULL DEFAULT '[]',
  deny_list       TEXT NOT NULL DEFAULT '[]',
  allow_download  INTEGER NOT NULL DEFAULT 0,
  expires_at      INTEGER,
  is_archived     INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_link_room ON link(dataroom_id);

CREATE TABLE IF NOT EXISTS viewer (
  id         TEXT PRIMARY KEY,
  email      TEXT NOT NULL UNIQUE,
  verified   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS view (
  id           TEXT PRIMARY KEY,
  link_id      TEXT NOT NULL,
  dataroom_id  TEXT,
  document_id  TEXT,
  viewer_id    TEXT,
  viewer_email TEXT,
  view_type    TEXT NOT NULL DEFAULT 'DATAROOM_VIEW',
  verified     INTEGER NOT NULL DEFAULT 0,
  viewed_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_view_link ON view(link_id);

CREATE TABLE IF NOT EXISTS page_view (
  id             TEXT PRIMARY KEY,
  view_id        TEXT NOT NULL,
  link_id        TEXT NOT NULL,
  document_id    TEXT,
  dataroom_id    TEXT,
  page_number    INTEGER NOT NULL,
  version_number INTEGER,
  duration       INTEGER NOT NULL DEFAULT 0,
  viewed_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_pv_link ON page_view(link_id);
CREATE INDEX IF NOT EXISTS ix_pv_view ON page_view(view_id);
`
