package channels

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	// sqlpool.Open is the ONE opener: it renders this subsystem's path from the
	// system namespace, opens it under the key cek derives for that name, and
	// applies the single-connection cap.

	"github.com/hanzoai/cloud/sqlpool"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both build tags). Blank import registers
	// the driver — same as apps/integrations.
	_ "github.com/hanzoai/sqlite"
)

const (
	// inboxTextMax bounds one stored inbound text. Together with event-key
	// dedupe, GC, and single-conn SQLite serialization it bounds flood damage
	// under groupPolicy=open; a per-org ingest limiter is the named follow-up
	// alongside agent delivery.
	inboxTextMax = 8 << 10
	// inboxKeepSec is inbox retention; gc drops older rows.
	inboxKeepSec = 30 * 24 * 3600
	// sendKeepSec is the documented idempotency replay window: a completed
	// send replays its Delivery for 48 h, then the key is forgotten.
	sendKeepSec = 48 * 3600
)

// store is the channels database. ONE SQLite file — the system namespace's
// "channels" — holds every org's policy, pairing, allowlist, inbox,
// send-idempotency, and route rows; tenancy is the org column — org leads every
// PK. No secrets in any row (pairing codes are capability strings a sender must
// present; they are stored, never logged).
type store struct {
	db *sql.DB
}

func openStore(dir string) (*store, error) {
	db, err := sqlpool.Open("channels", dir)
	if err != nil {
		return nil, err
	}
	st := &store{db: db}
	if err := st.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

func (st *store) migrate() error {
	// No account dimension anywhere: integrations' connections PK is
	// (org, provider), so (org, channel) IS the channel-account key — exactly
	// one connected account per pair is representable in the custody plane.
	//
	// channel_allow has exactly two writers, one per source class: 'config'
	// rows are written only by policy.go putAllow; 'pairing' rows only by
	// pairing.go approvePairing. Keeping the classes disjoint is what lets
	// policy edits never revoke an approved pairing and vice versa.
	const ddl = `
CREATE TABLE IF NOT EXISTS channel_policy (
  org          TEXT NOT NULL,
  channel      TEXT NOT NULL,
  dm_policy    TEXT NOT NULL DEFAULT 'pairing' CHECK (dm_policy IN ('pairing','allowlist','open')),
  group_policy TEXT NOT NULL DEFAULT 'open' CHECK (group_policy IN ('open','allowlist','disabled')),
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (org, channel)
);

CREATE TABLE IF NOT EXISTS channel_pairing (
  org        TEXT NOT NULL,
  channel    TEXT NOT NULL,
  sender     TEXT NOT NULL,
  code       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  PRIMARY KEY (org, channel, sender)
);
CREATE INDEX IF NOT EXISTS ix_pairing_code ON channel_pairing(org, channel, code);

CREATE TABLE IF NOT EXISTS channel_allow (
  org        TEXT NOT NULL,
  channel    TEXT NOT NULL,
  scope      TEXT NOT NULL CHECK (scope IN ('dm','group')),
  entry      TEXT NOT NULL,
  source     TEXT NOT NULL CHECK (source IN ('config','pairing')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (org, channel, scope, entry)
);

CREATE TABLE IF NOT EXISTS channel_access_group (
  org     TEXT NOT NULL,
  name    TEXT NOT NULL,
  channel TEXT NOT NULL, -- '*' = shared across channels
  entry   TEXT NOT NULL,
  PRIMARY KEY (org, name, channel, entry)
);

CREATE TABLE IF NOT EXISTS channel_owner (
  org        TEXT NOT NULL PRIMARY KEY,
  entry      TEXT NOT NULL, -- '<channel>:<sender>', set on first pairing approval only
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS channel_inbox (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  org         TEXT NOT NULL,
  channel     TEXT NOT NULL,
  account     TEXT NOT NULL DEFAULT '',
  room_id     TEXT NOT NULL,
  room_kind   TEXT NOT NULL CHECK (room_kind IN ('dm','group','thread')),
  sender      TEXT NOT NULL,
  sender_user TEXT NOT NULL DEFAULT '',
  text        TEXT NOT NULL,
  reply_to    TEXT NOT NULL DEFAULT '',
  event_key   TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_inbox_org ON channel_inbox(org, id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_inbox_event ON channel_inbox(org, channel, event_key) WHERE event_key != '';

CREATE TABLE IF NOT EXISTS channel_send (
  org         TEXT NOT NULL,
  channel     TEXT NOT NULL,
  idempotency TEXT NOT NULL,
  message_id  TEXT NOT NULL DEFAULT '',
  ts          INTEGER NOT NULL,
  PRIMARY KEY (org, channel, idempotency)
);

-- Route presence is the send capability for global-token transports: a row is
-- upserted ONLY from allowed inbound, so an org can drive only rooms it was
-- messaged from. reply_root='' for discord; teams stores the JWT-verified
-- serviceURL.
--
-- expires_at is when that capability lapses, in Unix seconds, and 0 means it
-- does not: a route minted by an ALLOWED sender lasts, a route minted so a
-- PAIRING REPLY can be delivered lasts exactly as long as the pairing request
-- does. Without it a stranger the org never approved left behind a permanent
-- reply target, since an unapproved pairing lapses and nothing else here ever
-- deletes a route.
CREATE TABLE IF NOT EXISTS channel_route (
  org        TEXT NOT NULL,
  channel    TEXT NOT NULL,
  room_id    TEXT NOT NULL,
  reply_root TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (org, channel, room_id)
);
`
	if _, err := st.db.Exec(ddl); err != nil {
		return err
	}
	// A file written before routes could expire has the table without the
	// column. SQLite has no IF NOT EXISTS for a column, and re-adding one is a
	// duplicate-column error rather than a broken database, so the second run is
	// the no-op.
	if _, err := st.db.Exec(`ALTER TABLE channel_route ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return nil
}

func (st *store) Close() error { return st.db.Close() }

// ── inbox ────────────────────────────────────────────────────────────────────

// inboxRow is one stored inbound message. CreatedAt is Unix seconds.
type inboxRow struct {
	ID         int64
	Org        string
	Channel    string
	Account    string
	RoomID     string
	RoomKind   RoomKind
	Sender     string
	SenderUser string
	Text       string
	ReplyTo    string
	EventKey   string
	CreatedAt  int64
}

// insertInbox stores one allowed inbound message. INSERT OR IGNORE rides
// ux_inbox_event, so a redelivered event key is a no-op; event_key=""
// (non-dedupable) always inserts.
func (st *store) insertInbox(ctx context.Context, r inboxRow) error {
	if len(r.Text) > inboxTextMax {
		r.Text = r.Text[:inboxTextMax]
	}
	_, err := st.db.ExecContext(ctx, `INSERT OR IGNORE INTO channel_inbox
  (org, channel, account, room_id, room_kind, sender, sender_user, text, reply_to, event_key, created_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.Org, r.Channel, r.Account, r.RoomID, string(r.RoomKind),
		r.Sender, r.SenderUser, r.Text, r.ReplyTo, r.EventKey, r.CreatedAt)
	return err
}

// listInbox returns the org's inbox rows with id > since, oldest first. limit
// clamps to 1..200; <=0 selects the default 50.
// recentRoom reads the newest turns of ONE room, newest first so the LIMIT keeps
// the recent ones rather than the oldest. The caller reverses them.
//
// Keyed (org, channel, room) because a room id is unique only inside its
// transport, and org is the tenancy predicate on every read of this table.
func (st *store) recentRoom(ctx context.Context, org, channel, room string, limit int) ([]inboxRow, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT id, org, channel, account, room_id, room_kind,
  sender, sender_user, text, reply_to, event_key, created_at
  FROM channel_inbox WHERE org = ? AND channel = ? AND room_id = ?
  ORDER BY id DESC LIMIT ?`, org, channel, room, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []inboxRow{}
	for rows.Next() {
		var r inboxRow
		var kind string
		if err := rows.Scan(&r.ID, &r.Org, &r.Channel, &r.Account, &r.RoomID, &kind,
			&r.Sender, &r.SenderUser, &r.Text, &r.ReplyTo, &r.EventKey, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.RoomKind = RoomKind(kind)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (st *store) listInbox(ctx context.Context, org string, since int64, limit int) ([]inboxRow, error) {
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}
	rows, err := st.db.QueryContext(ctx, `SELECT id, org, channel, account, room_id, room_kind,
  sender, sender_user, text, reply_to, event_key, created_at
  FROM channel_inbox WHERE org = ? AND id > ? ORDER BY id LIMIT ?`, org, since, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []inboxRow{}
	for rows.Next() {
		var r inboxRow
		var kind string
		if err := rows.Scan(&r.ID, &r.Org, &r.Channel, &r.Account, &r.RoomID, &kind,
			&r.Sender, &r.SenderUser, &r.Text, &r.ReplyTo, &r.EventKey, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.RoomKind = RoomKind(kind)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── send idempotency ─────────────────────────────────────────────────────────

// markSend claims an idempotency key. fresh=true ⇒ the caller owns the send;
// fresh=false ⇒ the key was already claimed and prior holds the stored receipt.
// A concurrent duplicate racing an in-flight send may replay an empty Delivery
// once (message_id not yet finished, or the row unmarked between statements) —
// accepted tradeoff; only a completed send replays a real receipt.
func (st *store) markSend(ctx context.Context, org, channel, idem string, now int64) (fresh bool, prior Delivery, err error) {
	res, err := st.db.ExecContext(ctx, `INSERT INTO channel_send (org, channel, idempotency, message_id, ts)
  VALUES (?,?,?,'',?) ON CONFLICT (org, channel, idempotency) DO NOTHING`, org, channel, idem, now)
	if err != nil {
		return false, Delivery{}, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, Delivery{}, nil
	}
	err = st.db.QueryRowContext(ctx, `SELECT message_id, ts FROM channel_send
  WHERE org = ? AND channel = ? AND idempotency = ?`, org, channel, idem).Scan(&prior.MessageID, &prior.Timestamp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, Delivery{}, nil
	}
	return false, prior, err
}

// unmarkSend releases a claimed key after a transport-send failure so the key
// can re-attempt; without it a failed send would replay an empty receipt for
// the whole retention window.
func (st *store) unmarkSend(ctx context.Context, org, channel, idem string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM channel_send
  WHERE org = ? AND channel = ? AND idempotency = ?`, org, channel, idem)
	return err
}

// finishSend records the transport receipt on a claimed key.
func (st *store) finishSend(ctx context.Context, org, channel, idem, messageID string) error {
	_, err := st.db.ExecContext(ctx, `UPDATE channel_send SET message_id = ?
  WHERE org = ? AND channel = ? AND idempotency = ?`, messageID, org, channel, idem)
	return err
}

// ── routes (inbound-learned reply targets) ───────────────────────────────────

// upsertRoute records an allowed inbound room as a send target. Called only on
// the allow and pair branches — a blocked sender mints no route. expires is when
// the capability lapses, or 0 for one that does not.
//
// The two ways a row can already be there decide the merge: a lasting route is
// never shortened to a pairing's hour, and a pairing route the org then approved
// becomes lasting on the next allowed message. So 0 wins, and otherwise the
// later expiry does.
func (st *store) upsertRoute(ctx context.Context, org, channel, roomID, replyRoot string, now, expires int64) error {
	_, err := st.db.ExecContext(ctx, `INSERT INTO channel_route (org, channel, room_id, reply_root, updated_at, expires_at)
  VALUES (?,?,?,?,?,?)
  ON CONFLICT (org, channel, room_id) DO UPDATE SET
    reply_root = excluded.reply_root,
    updated_at = excluded.updated_at,
    expires_at = CASE WHEN excluded.expires_at = 0 OR channel_route.expires_at = 0 THEN 0
                      ELSE MAX(channel_route.expires_at, excluded.expires_at) END`,
		org, channel, roomID, replyRoot, now, expires)
	return err
}

// routeFor returns the stored reply root for a room; ok=false when the org has
// never received allowed inbound from it, or when the capability that minted the
// row has lapsed. The expiry is read here and not left to gc: gc rides inbound
// traffic, so a room nobody writes to again would keep an expired route for as
// long as the org stayed quiet.
func (st *store) routeFor(ctx context.Context, org, channel, roomID string, now int64) (string, bool, error) {
	var root string
	err := st.db.QueryRowContext(ctx, `SELECT reply_root FROM channel_route
  WHERE org = ? AND channel = ? AND room_id = ? AND (expires_at = 0 OR expires_at > ?)`,
		org, channel, roomID, now).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return root, true, nil
}

// ── retention ────────────────────────────────────────────────────────────────

// gc drops inbox rows past retention, send keys past the idempotency replay
// window, and routes whose capability has lapsed. Ridden opportunistically from
// ingest (bounded to once per 10 min).
func (st *store) gc(ctx context.Context, now int64) error {
	if _, err := st.db.ExecContext(ctx, `DELETE FROM channel_inbox WHERE created_at < ?`, now-inboxKeepSec); err != nil {
		return err
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM channel_send WHERE ts < ?`, now-sendKeepSec); err != nil {
		return err
	}
	_, err := st.db.ExecContext(ctx, `DELETE FROM channel_route WHERE expires_at != 0 AND expires_at <= ?`, now)
	return err
}
