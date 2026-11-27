package transport

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// planeRecent reads a channel under the bot the caller names, so a DM whose channel id
// is the human's own id — the same under every bot on Telegram and WhatsApp —
// never crosses from one member's bot to another's. An empty account reads only
// the rows recorded with none. (Regression: the plane reader passed no account
// and the store fanned out across every bot of the org.)
func TestPlaneRecentIsScopedToTheBot(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		now := time.Now().Unix()
		for i, acct := range []string{"bot-of-ana", "bot-of-ben"} {
			if err := st.insertInbox(ctx, inboxRow{
				Org: "hanzo", Transport: "telegram", Account: acct, ChannelID: "4242", ChannelKind: ChannelDM,
				Sender: "4242", SenderUser: "u", Text: "to " + acct, EventKey: "k-" + acct, CreatedAt: now + int64(i),
			}); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := st.recentChannel(ctx, "hanzo", "telegram", "bot-of-ana", "4242", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Account != "bot-of-ana" {
			t.Fatalf("ana's bot read %d turns across bots: %+v", len(rows), rows)
		}
		none, err := st.recentChannel(ctx, "hanzo", "telegram", "", "4242", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(none) != 0 {
			t.Fatalf("an empty account fanned out across %d bots", len(none))
		}

	})
}
