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
// s3/storage client, never in Base). Tenant isolation is the per-tenant DB file
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

-- === trust center ===========================================================
-- An org's trust center: what it publishes about its own security, and the
-- endpoint through which an outsider asks for the part only an auditor can vouch
-- for.
--
-- It adds three tables and reuses everything else. A gated artifact's BYTES are a
-- document row above; a grant is a link row above, time-boxed and addressed to one
-- party; and who read what, page by page, is the view and page_view rows above —
-- so the access record is the same record the data room already keeps, and there
-- is no second document store, no second grant and no second trail.

-- The center itself. One row per tenant, id 'center', so it cannot be duplicated.
CREATE TABLE IF NOT EXISTS trust_center (
  id         TEXT PRIMARY KEY,
  slug       TEXT NOT NULL,
  name       TEXT NOT NULL,
  published  INTEGER NOT NULL DEFAULT 0,
  nda        TEXT,
  room_id    TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- One published item. Its KIND is the taxonomy a page renders by; its ATTESTER is
-- who vouched for it; its TIER is who may read it.
--
-- THE LAST CHECK IS THE WHOLE SAFETY OF THIS DESIGN. Tier defaults to gated, so a
-- kind nobody has thought of yet is private on arrival and someone has to publish
-- it deliberately. On top of that an auditor-attested item can NEVER be public —
-- refused by SQLite, so no path through Go can publish one, including a path
-- written later by someone who has not read this file.
CREATE TABLE IF NOT EXISTS trust_artifact (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
  name        TEXT NOT NULL,
  summary     TEXT,
  framework   TEXT,
  attester    TEXT NOT NULL,
  tier        TEXT NOT NULL DEFAULT 'gated',
  document_id TEXT,
  body        TEXT,
  retired     INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  CHECK (tier IN ('public','gated')),
  CHECK (attester IN ('self','auditor')),
  CHECK (attester <> 'auditor' OR tier = 'gated')
);
CREATE INDEX IF NOT EXISTS ix_ta_tier ON trust_artifact(tier, retired);

-- Someone asking to read the gated tier, and what was decided. A granted request
-- carries the link it became, so the queue and the live grants are one list.
CREATE TABLE IF NOT EXISTS trust_request (
  id          TEXT PRIMARY KEY,
  email       TEXT NOT NULL,
  party       TEXT,
  reason      TEXT,
  artifact_id TEXT,
  nda         TEXT,
  state       TEXT NOT NULL DEFAULT 'open',
  note        TEXT,
  link_id     TEXT,
  expires_at  INTEGER,
  decided_by  TEXT,
  decided_at  INTEGER,
  created_at  INTEGER NOT NULL,
  CHECK (state IN ('open','granted','refused'))
);
CREATE INDEX IF NOT EXISTS ix_tr_state ON trust_request(state, created_at);
-- One OPEN ask per party per target: asking twice is the same ask, and it is also
-- what keeps an anonymous caller from filling a tenant's store.
CREATE UNIQUE INDEX IF NOT EXISTS ux_tr_open
  ON trust_request(email, COALESCE(artifact_id,'')) WHERE state='open';
`
