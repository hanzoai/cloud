package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
)

// engines_test.go holds what the port to hanzo-sql changed that no behaviour test
// above reaches by itself.

// An inbound text over the cap is cut on a rune boundary: cut through one it is bytes
// PostgreSQL refuses, so the message would be lost where SQLite kept it.
func TestALongTextIsCutOnARuneBoundary(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := newStore(t)
		ctx := context.Background()
		// A three-byte rune straddles the cap whichever way the cap falls.
		for i, pad := range []string{"", "a", "ab"} {
			text := pad + strings.Repeat("日", inboxTextMax)
			key := string(rune('a' + i))
			if err := st.insertInbox(ctx, inboxRow{Org: "acme", Transport: "slack", ChannelID: "C1", ChannelKind: ChannelGroup,
				Sender: "U1", Text: text, EventKey: key, CreatedAt: 1}); err != nil {
				t.Fatalf("a long text with %q before the rune: %v", pad, err)
			}
		}
		rows, err := stored(ctx, st, "acme")
		if err != nil || len(rows) != 3 {
			t.Fatalf("stored %d rows, %v; want 3", len(rows), err)
		}
		for _, r := range rows {
			if !utf8.ValidString(r.Text) || len(r.Text) > inboxTextMax || len(r.Text) < inboxTextMax-3 {
				t.Errorf("stored a text of %d bytes, valid=%v", len(r.Text), utf8.ValidString(r.Text))
			}
		}
	})
}

// A message holding what an engine cannot keep is refused where it would be stored, on
// either engine, and the redelivery of one already stored is still a no-op.
func TestAnInboxTextAnEngineCannotKeepIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := newStore(t)
		ctx := context.Background()
		row := inboxRow{Org: "acme", Transport: "slack", ChannelID: "C1", ChannelKind: ChannelGroup, Sender: "U1", Text: "a\x00b", EventKey: "k1", CreatedAt: 1}
		if err := st.insertInbox(ctx, row); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("a text holding a NUL = %v, want ErrText", err)
		}
		row.Text = "fine"
		for range 2 {
			if err := st.insertInbox(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
		if rows, err := stored(ctx, st, "acme"); err != nil || len(rows) != 1 {
			t.Fatalf("a redelivered event stored %d rows, %v; want 1", len(rows), err)
		}
	})
}

// A SQLite file from before a route could expire, and before a turn named its owner,
// has the tables without those columns; opening it adds them and keeps its rows.
func TestAStoreFromBeforeExpiryAndOwnerGainsThem(t *testing.T) {
	dir := t.TempDir()
	db, err := locus.System("channels", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE channel_route (
  org TEXT NOT NULL, channel TEXT NOT NULL, room_id TEXT NOT NULL,
  reply_root TEXT NOT NULL, updated_at INTEGER NOT NULL,
  PRIMARY KEY (org, channel, room_id)
);
INSERT INTO channel_route VALUES ('acme', 'slack', 'C1', 'root', 5);
CREATE TABLE channel_turn (
  org TEXT NOT NULL, session TEXT NOT NULL, run TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL,
  channel TEXT NOT NULL, account TEXT NOT NULL DEFAULT '', room_id TEXT NOT NULL,
  room_kind TEXT NOT NULL DEFAULT '', reply_to TEXT NOT NULL DEFAULT '',
  message_id TEXT NOT NULL DEFAULT '', started_at INTEGER NOT NULL,
  PRIMARY KEY (org, session)
);
INSERT INTO channel_turn (org, session, subject, channel, room_id, started_at) VALUES ('acme', 's1', 'u', 'slack', 'C1', 6);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	for i := range 2 { // the second open finds the columns and adds nothing
		st, err := openStore(dir)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		root, ok, err := st.routeFor(context.Background(), "acme", "slack", "C1", 1_000_000)
		if err != nil || !ok || root != "root" {
			t.Fatalf("open #%d: the old route = %q %v %v, want it kept and not expiring", i, root, ok, err)
		}
		turns, err := st.openTurns(context.Background())
		if err != nil || len(turns) != 1 || turns[0].owner != "" {
			t.Fatalf("open #%d: the old turn = %+v, %v; want it kept with no owner", i, turns, err)
		}
		_ = st.Close()
	}
}
