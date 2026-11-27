package transport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/mint"

	// locus.Use is the ONE opener (it moves the store off its file first, when asked): with CLOUD_SQL_DSN set it is the store's schema
	// on hanzo-sql, and otherwise it renders this subsystem's path from the system
	// namespace, opens it under the key cek derives for that name, and applies the
	// single-connection cap.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both build tags). Blank import registers
	// the driver — same as apps/provider.
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

// store is the transport database. ONE database — the system namespace's
// "channels" (a storage name, kept through the rename), a SQLite file or a schema on
// hanzo-sql — holds every org's policy, pairing, allowlist, inbox,
// send-idempotency, and route rows; tenancy is the org column — org leads every
// PK. No secrets in any row (pairing codes are capability strings a sender must
// present; they are stored, never logged).
//
// Its SQL is the dialect both engines share (sdk/dialect) and its tests run
// on both (internal/sqltest).
type store struct {
	db *sql.DB
	d  dialect.Dialect
	// holder names this process among the replicas that share the database: the
	// holder of the turns it is rendering (resume.go).
	holder string
}

func openStore(dir string) (*store, error) {
	st, err := locus.Use(context.Background(), nil, namespace.System(), "channels", dir, func(db *sql.DB) (*store, error) {
		st := &store{db: db, d: dialect.Of(db), holder: mint.ID("pod")}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := st.d.Migrate(ctx, db, func(tx *sql.Tx) error { return st.migrate(ctx, tx) }); err != nil {
			return nil, fmt.Errorf("channels: migrate: %w", err)
		}
		return st, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open channels store: %w", err)
	}
	return st, nil
}

func (st *store) migrate(ctx context.Context, tx *sql.Tx) error {
	// No account dimension anywhere: integrations' connections PK is
	// (org, provider), so (org, transport) IS the transport-account key — exactly
	// one connected account per pair is representable in the custody plane.
	//
	// channel_allow has exactly two writers, one per source class: 'config'
	// rows are written only by policy.go putAllow; 'pairing' rows only by
	// pairing.go approvePairing. Keeping the classes disjoint is what lets
	// policy edits never revoke an approved pairing and vice versa.
	ddl := `
CREATE TABLE IF NOT EXISTS channel_policy (` + policyColumns + `);

CREATE TABLE IF NOT EXISTS channel_pairing (
  org        TEXT NOT NULL,
  channel    TEXT NOT NULL,
  sender     TEXT NOT NULL,
  code       TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  last_seen  BIGINT NOT NULL,
  PRIMARY KEY (org, channel, sender)
);
CREATE INDEX IF NOT EXISTS ix_pairing_code ON channel_pairing(org, channel, code);

CREATE TABLE IF NOT EXISTS channel_allow (
  org        TEXT NOT NULL,
  channel    TEXT NOT NULL,
  scope      TEXT NOT NULL CHECK (scope IN ('dm','group')),
  entry      TEXT NOT NULL,
  source     TEXT NOT NULL CHECK (source IN ('config','pairing')),
  created_at BIGINT NOT NULL,
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
  created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS channel_inbox (
  id          ` + st.d.Identity() + `,
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
  created_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_inbox_org ON channel_inbox(org, id);
CREATE INDEX IF NOT EXISTS ix_inbox_owner ON channel_inbox(org, sender_user, id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_inbox_event ON channel_inbox(org, channel, event_key) WHERE event_key != '';

CREATE TABLE IF NOT EXISTS channel_send (
  org         TEXT NOT NULL,
  channel     TEXT NOT NULL,
  idempotency TEXT NOT NULL,
  message_id  TEXT NOT NULL DEFAULT '',
  ts          BIGINT NOT NULL,
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
-- Which agent answers: room_id '' is the transport's default for the org, a
-- room row wins over it (agent.go). Absent means the built-in agent.
CREATE TABLE IF NOT EXISTS channel_agent (
  org        TEXT NOT NULL,
  channel    TEXT NOT NULL,
  room_id    TEXT NOT NULL DEFAULT '',
  agent      TEXT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (org, channel, room_id)
);

CREATE TABLE IF NOT EXISTS channel_route (
  org        TEXT NOT NULL,
  channel    TEXT NOT NULL,
  room_id    TEXT NOT NULL,
  reply_root TEXT NOT NULL,
  updated_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (org, channel, room_id)
);

-- A turn this process is RENDERING (resume.go): where its one message lives, so
-- another process can pick the rendering up. The run itself is agents'; this
-- row is only the address of the message showing it. message_id is '' until the
-- first post, and the row goes when the turn's conclusion has been shown. holder is
-- the process rendering it and renewed_at the last time it said so: a row nobody
-- has renewed for a while is a rendering whose process is gone.
CREATE TABLE IF NOT EXISTS channel_turn (
  org        TEXT NOT NULL,
  session    TEXT NOT NULL,
  run        TEXT NOT NULL DEFAULT '',
  subject    TEXT NOT NULL,
  channel    TEXT NOT NULL,
  account    TEXT NOT NULL DEFAULT '',
  room_id    TEXT NOT NULL,
  room_kind  TEXT NOT NULL DEFAULT '',
  reply_to   TEXT NOT NULL DEFAULT '',
  message_id TEXT NOT NULL DEFAULT '',
  started_at BIGINT NOT NULL,
  PRIMARY KEY (org, session)
);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	// A file written before routes could expire has the table without the
	// column, and the same for a turn's owner — the platform's id for its asker,
	// which its buttons name to anyone else who presses. A row from before it names
	// nobody. Each column is added once, found by asking the table for it.
	for _, add := range []struct{ table, column, def string }{
		{"channel_route", "expires_at", "BIGINT NOT NULL DEFAULT 0"},
		{"channel_turn", "owner", "TEXT NOT NULL DEFAULT ''"},
		{"channel_turn", "holder", "TEXT NOT NULL DEFAULT ''"},
		{"channel_turn", "renewed_at", "BIGINT NOT NULL DEFAULT 0"},
	} {
		have, err := st.d.HasColumn(ctx, tx, add.table, add.column)
		if err != nil {
			return err
		}
		if have {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+add.table+` ADD COLUMN `+add.column+` `+add.def); err != nil {
			return err
		}
	}
	return st.widenPolicy(ctx, tx)
}

// policyColumns is channel_policy's shape, written once for the create and the
// rebuild below.
const policyColumns = `
  org          TEXT NOT NULL,
  channel      TEXT NOT NULL,
  dm_policy    TEXT NOT NULL DEFAULT 'pairing' CHECK (dm_policy IN ('pairing','allowlist','open','members')),
  group_policy TEXT NOT NULL DEFAULT 'open' CHECK (group_policy IN ('open','allowlist','disabled')),
  updated_at   BIGINT NOT NULL,
  PRIMARY KEY (org, channel)
`

// widenPolicy rebuilds a channel_policy written before the members policy, whose
// CHECK refuses it. SQLite cannot alter a CHECK, so the rows move to a table of the
// current shape in the migration's transaction; a table already current is left
// alone. Only a SQLite file can hold the old table: a schema on hanzo-sql is made
// from policyColumns, and has no sqlite_master to ask.
func (st *store) widenPolicy(ctx context.Context, tx *sql.Tx) error {
	if st.d.Postgres() {
		return nil
	}
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='channel_policy'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(ddl, "'members'") {
		return nil
	}
	for _, q := range []string{
		`ALTER TABLE channel_policy RENAME TO channel_policy_old`,
		`CREATE TABLE channel_policy (` + policyColumns + `)`,
		`INSERT INTO channel_policy (org, channel, dm_policy, group_policy, updated_at)
		   SELECT org, channel, dm_policy, group_policy, updated_at FROM channel_policy_old`,
		`DROP TABLE channel_policy_old`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("widen channel_policy: %w", err)
		}
	}
	return nil
}

func (st *store) Close() error { return st.db.Close() }

// ── inbox ────────────────────────────────────────────────────────────────────

// inboxRow is one stored inbound message. CreatedAt is Unix seconds.
type inboxRow struct {
	ID          int64
	Org         string
	Transport   string
	Account     string
	ChannelID   string
	ChannelKind ChannelKind
	Sender      string
	SenderUser  string
	Text        string
	ReplyTo     string
	EventKey    string
	CreatedAt   int64
}

// insertInbox stores one allowed inbound message. ON CONFLICT DO NOTHING rides
// ux_inbox_event, so a redelivered event key is a no-op; event_key=""
// (non-dedupable) always inserts.
func (st *store) insertInbox(ctx context.Context, r inboxRow) error {
	if len(r.Text) > inboxTextMax {
		// Cut on a rune boundary: a cut through one is bytes PostgreSQL refuses.
		cut := inboxTextMax
		for cut > 0 && !utf8.RuneStart(r.Text[cut]) {
			cut--
		}
		r.Text = r.Text[:cut]
	}
	if err := dialect.Text(r.Org, r.Transport, r.Account, r.ChannelID, string(r.ChannelKind),
		r.Sender, r.SenderUser, r.Text, r.ReplyTo, r.EventKey); err != nil {
		return err
	}
	_, err := st.db.ExecContext(ctx, `INSERT INTO channel_inbox
  (org, channel, account, room_id, room_kind, sender, sender_user, text, reply_to, event_key, created_at)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
  ON CONFLICT DO NOTHING`,
		r.Org, r.Transport, r.Account, r.ChannelID, string(r.ChannelKind),
		r.Sender, r.SenderUser, r.Text, r.ReplyTo, r.EventKey, r.CreatedAt)
	return err
}

// inboxColumns is the column list every inbox read selects, in scanInbox's order.
const inboxColumns = `id, org, channel, account, room_id, room_kind,
  sender, sender_user, text, reply_to, event_key, created_at`

// scanInbox reads the rows a SELECT of inboxColumns answered.
func scanInbox(rows *sql.Rows) ([]inboxRow, error) {
	defer func() { _ = rows.Close() }()
	out := []inboxRow{}
	for rows.Next() {
		var r inboxRow
		var kind string
		if err := rows.Scan(&r.ID, &r.Org, &r.Transport, &r.Account, &r.ChannelID, &kind,
			&r.Sender, &r.SenderUser, &r.Text, &r.ReplyTo, &r.EventKey, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.ChannelKind = ChannelKind(kind)
		out = append(out, r)
	}
	return out, rows.Err()
}

// recentChannel reads the newest turns of ONE channel, newest first so the LIMIT keeps
// the recent ones rather than the oldest. The caller reverses them.
//
// Keyed (org, transport, channel) because a channel id is unique only inside its
// transport, and org is the tenancy predicate on every read of this table; and
// on the bot's account, because a DM's channel id is the human's own id on some
// transports (Telegram, WhatsApp) — the same under every bot — so two members'
// bots in one org would read each other's conversation. The account is matched
// exactly: a caller that cannot name the bot (an empty account) reads only the
// rows recorded with none, rather than fanning out across every bot of the org.
func (st *store) recentChannel(ctx context.Context, org, transport, account, channel string, limit int) ([]inboxRow, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT `+inboxColumns+`
  FROM channel_inbox WHERE org = $1 AND channel = $2 AND room_id = $3 AND account = $4
  ORDER BY id DESC LIMIT $5`, org, transport, channel, account, limit)
	if err != nil {
		return nil, err
	}
	return scanInbox(rows)
}

// reader is who is reading the inbox: the subject the identity boundary attested
// for them ("" when it attested none) and whether they hold platform authority.
type reader struct {
	subject string
	super   bool
	admin   bool // an admin of a customer's own org: reads every row of it
}

// listInbox returns the rows of org that belong to who, with id > since, oldest
// first. limit clamps to 1..200; <=0 selects the default 50.
//
// A row belongs to the person who said it to the bot: sender_user, the Hanzo
// account the sender's own chat identity is linked to. Org membership is not
// ownership — every self-serve customer is a member of one org — and neither is
// org admin of a held org. A row with no sender_user is nobody's, and only a
// SuperAdmin lists those. An admin of a customer's own org lists every row of
// it: its bots are theirs. The empty subject owns nothing, so a caller the
// boundary attested no subject for can never match a row that names nobody.
func (st *store) listInbox(ctx context.Context, org string, who reader, since int64, limit int) ([]inboxRow, error) {
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}
	rows, err := st.db.QueryContext(ctx, `SELECT `+inboxColumns+`
  FROM channel_inbox WHERE org = $1 AND id > $2 AND sender != 'bot'
    AND ($3 OR (sender_user != '' AND sender_user = $4) OR (sender_user = '' AND $5))
  ORDER BY id LIMIT $6`, org, since, who.admin, who.subject, who.super, limit)
	if err != nil {
		return nil, err
	}
	return scanInbox(rows)
}

// ── send idempotency ─────────────────────────────────────────────────────────

// markSend claims an idempotency key. fresh=true ⇒ the caller owns the send;
// fresh=false ⇒ the key was already claimed and prior holds the stored receipt.
// A concurrent duplicate racing an in-flight send may replay an empty Delivery
// once (message_id not yet finished, or the row unmarked between statements) —
// accepted tradeoff; only a completed send replays a real receipt.
func (st *store) markSend(ctx context.Context, org, transport, idem string, now int64) (fresh bool, prior Delivery, err error) {
	var claimed string
	err = st.db.QueryRowContext(ctx, `INSERT INTO channel_send (org, channel, idempotency, message_id, ts)
  VALUES ($1,$2,$3,'',$4) ON CONFLICT (org, channel, idempotency) DO NOTHING RETURNING idempotency`,
		org, transport, idem, now).Scan(&claimed)
	if err == nil {
		return true, Delivery{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, Delivery{}, err
	}
	err = st.db.QueryRowContext(ctx, `SELECT message_id, ts FROM channel_send
  WHERE org = $1 AND channel = $2 AND idempotency = $3`, org, transport, idem).Scan(&prior.MessageID, &prior.Timestamp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, Delivery{}, nil
	}
	return false, prior, err
}

// unmarkSend releases a claimed key after a transport-send failure so the key
// can re-attempt; without it a failed send would replay an empty receipt for
// the whole retention window.
func (st *store) unmarkSend(ctx context.Context, org, transport, idem string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM channel_send
  WHERE org = $1 AND channel = $2 AND idempotency = $3`, org, transport, idem)
	return err
}

// finishSend records the transport receipt on a claimed key.
func (st *store) finishSend(ctx context.Context, org, transport, idem, messageID string) error {
	_, err := st.db.ExecContext(ctx, `UPDATE channel_send SET message_id = $1
  WHERE org = $2 AND channel = $3 AND idempotency = $4`, messageID, org, transport, idem)
	return err
}

// ── routes (inbound-learned reply targets) ───────────────────────────────────

// upsertRoute records an allowed inbound channel as a send target. Called only on
// the allow and pair branches — a blocked sender mints no route. expires is when
// the capability lapses, or 0 for one that does not.
//
// The two ways a row can already be there decide the merge: a lasting route is
// never shortened to a pairing's hour, and a pairing route the org then approved
// becomes lasting on the next allowed message. So 0 wins, and otherwise the
// later expiry does.
func (st *store) upsertRoute(ctx context.Context, org, transport, channelID, replyRoot string, now, expires int64) error {
	_, err := st.db.ExecContext(ctx, `INSERT INTO channel_route (org, channel, room_id, reply_root, updated_at, expires_at)
  VALUES ($1,$2,$3,$4,$5,$6)
  ON CONFLICT (org, channel, room_id) DO UPDATE SET
    reply_root = excluded.reply_root,
    updated_at = excluded.updated_at,
    expires_at = CASE WHEN excluded.expires_at = 0 OR channel_route.expires_at = 0 THEN 0
                      WHEN excluded.expires_at > channel_route.expires_at THEN excluded.expires_at
                      ELSE channel_route.expires_at END`,
		org, transport, channelID, replyRoot, now, expires)
	return err
}

// routeFor returns the stored reply root for a channel; ok=false when the org has
// never received allowed inbound from it, or when the capability that minted the
// row has lapsed. The expiry is read here and not left to gc: gc rides inbound
// traffic, so a channel nobody writes to again would keep an expired route for as
// long as the org stayed quiet.
func (st *store) routeFor(ctx context.Context, org, transport, channelID string, now int64) (string, bool, error) {
	var root string
	err := st.db.QueryRowContext(ctx, `SELECT reply_root FROM channel_route
  WHERE org = $1 AND channel = $2 AND room_id = $3 AND (expires_at = 0 OR expires_at > $4)`,
		org, transport, channelID, now).Scan(&root)
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
	if _, err := st.db.ExecContext(ctx, `DELETE FROM channel_inbox WHERE created_at < $1`, now-inboxKeepSec); err != nil {
		return err
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM channel_send WHERE ts < $1`, now-sendKeepSec); err != nil {
		return err
	}
	_, err := st.db.ExecContext(ctx, `DELETE FROM channel_route WHERE expires_at != 0 AND expires_at <= $1`, now)
	return err
}
