package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/manifest"
)

func TestLoadHistory(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		const org = "acme"
		now := time.Now().Unix()

		// Insert older message
		err := st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "C0123",
			ChannelKind: ChannelGroup,
			Sender:      "U_ALICE",
			SenderUser:  "alice",
			Text:        "weather in Benicia California today",
			EventKey:    "ev_1",
			CreatedAt:   now - 60,
		})
		if err != nil {
			t.Fatalf("insertInbox: %v", err)
		}

		// Insert current message (already recorded by ingest before turn)
		currMsg := Message{
			Transport:   "slack",
			Channel:     Channel{ID: "C0123", Kind: ChannelGroup},
			Sender:      Sender{ExternalID: "U_ALICE", UserID: "alice"},
			Text:        "try again",
			Idempotency: "ev_2",
		}
		err = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   currMsg.Transport,
			ChannelID:   currMsg.Channel.ID,
			ChannelKind: currMsg.Channel.Kind,
			Sender:      currMsg.Sender.ExternalID,
			SenderUser:  currMsg.Sender.UserID,
			Text:        currMsg.Text,
			EventKey:    currMsg.Idempotency,
			CreatedAt:   now,
		})
		if err != nil {
			t.Fatalf("insertInbox: %v", err)
		}

		// loadHistory should return only the prior turn ("weather in Benicia..."), NOT the current input ("try again")
		history := loadHistory(ctx, st, org, currMsg)
		if len(history) != 1 {
			t.Fatalf("expected 1 history turn, got %d: %+v", len(history), history)
		}
		if history[0].Text != "weather in Benicia California today" {
			t.Fatalf("unexpected history turn text: %q", history[0].Text)
		}
		if history[0].Sender != "alice" {
			t.Fatalf("unexpected history turn sender: %q", history[0].Sender)
		}

	})
}

func TestLoadHistoryThreadFiltering(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		const org = "acme"
		now := time.Now().Unix()

		// Channel message not in thread
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "C0123",
			ChannelKind: ChannelGroup,
			Sender:      "U_BOB",
			SenderUser:  "bob",
			Text:        "unrelated channel chatter",
			ReplyTo:     "",
			EventKey:    "ev_transport",
			CreatedAt:   now - 120,
		})

		// Root channel message that started the thread
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "C0123",
			ChannelKind: ChannelGroup,
			Sender:      "U_ALICE",
			SenderUser:  "alice",
			Text:        "check this pr - https://github.com/ClickHouse/ClickHouse/pull/115724",
			ReplyTo:     "",
			EventKey:    "T1:C0123:1710000000.000100",
			CreatedAt:   now - 90,
		})

		// Thread message
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "C0123",
			ChannelKind: ChannelGroup,
			Sender:      "U_ALICE",
			SenderUser:  "alice",
			Text:        "thread question 1",
			ReplyTo:     "1710000000.000100",
			EventKey:    "ev_th1",
			CreatedAt:   now - 60,
		})

		// Current thread message
		currMsg := Message{
			Transport:   "slack",
			Channel:     Channel{ID: "C0123", Kind: ChannelGroup},
			ReplyTo:     "1710000000.000100",
			Sender:      Sender{ExternalID: "U_ALICE", UserID: "alice"},
			Text:        "thread question 2",
			Idempotency: "ev_th2",
		}
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   currMsg.Transport,
			ChannelID:   currMsg.Channel.ID,
			ChannelKind: currMsg.Channel.Kind,
			Sender:      currMsg.Sender.ExternalID,
			SenderUser:  currMsg.Sender.UserID,
			Text:        currMsg.Text,
			ReplyTo:     currMsg.ReplyTo,
			EventKey:    currMsg.Idempotency,
			CreatedAt:   now,
		})

		history := loadHistory(ctx, st, org, currMsg)
		if len(history) != 2 {
			t.Fatalf("expected 2 thread history turns (root + reply), got %d: %+v", len(history), history)
		}
		if history[0].Text != "check this pr - https://github.com/ClickHouse/ClickHouse/pull/115724" {
			t.Fatalf("expected thread root, got %q", history[0].Text)
		}
		if history[1].Text != "thread question 1" {
			t.Fatalf("expected thread question 1, got %q", history[1].Text)
		}

	})
}

func TestLoadHistoryDMAllMessages(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		const org = "acme"
		now := time.Now().Unix()

		// In a DM, multiple previous messages exist
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "D0BJK8J5GP4",
			ChannelKind: ChannelDM,
			Sender:      "U_Z",
			SenderUser:  "z",
			Text:        "hello hanzo",
			EventKey:    "ev_dm1",
			CreatedAt:   now - 100,
		})
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "D0BJK8J5GP4",
			ChannelKind: ChannelDM,
			Sender:      "U_Z",
			SenderUser:  "z",
			Text:        "read #hanzo-gtm",
			EventKey:    "ev_dm2",
			CreatedAt:   now - 50,
		})

		currMsg := Message{
			Transport:   "slack",
			Channel:     Channel{ID: "D0BJK8J5GP4", Kind: ChannelDM},
			Sender:      Sender{ExternalID: "U_Z", UserID: "z"},
			Text:        "any updates?",
			Idempotency: "ev_dm3",
		}
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   currMsg.Transport,
			ChannelID:   currMsg.Channel.ID,
			ChannelKind: currMsg.Channel.Kind,
			Sender:      currMsg.Sender.ExternalID,
			SenderUser:  currMsg.Sender.UserID,
			Text:        currMsg.Text,
			EventKey:    currMsg.Idempotency,
			CreatedAt:   now,
		})

		history := loadHistory(ctx, st, org, currMsg)
		if len(history) != 2 {
			t.Fatalf("expected 2 history turns in DM, got %d: %+v", len(history), history)
		}
		if history[0].Text != "hello hanzo" || history[1].Text != "read #hanzo-gtm" {
			t.Fatalf("unexpected DM history order: %+v", history)
		}

	})
}

// A DM's channel id is the human's own id on some transports, the same under every
// bot, so a bot's history is read under its own account: two members' bots in one
// org never read each other's conversation with the same person.
func TestABotReadsOnlyItsOwnChannelHistory(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := newStore(t)
		ctx := context.Background()
		now := time.Now().Unix()
		for i, acct := range []string{"bot-of-ana", "bot-of-ben"} {
			if err := st.insertInbox(ctx, inboxRow{
				Org: "hanzo", Transport: "telegram", Account: acct, ChannelID: "4242", ChannelKind: ChannelDM,
				Sender: "4242", Text: "said to " + acct, EventKey: "ev_" + acct, CreatedAt: now + int64(i),
			}); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := st.recentChannel(ctx, "hanzo", "telegram", "bot-of-ana", "4242", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Text != "said to bot-of-ana" {
			t.Fatalf("ana's bot read %d turns: %+v", len(rows), rows)
		}

	})
}

func TestLoadHistory_BudgetsHistoryAndTruncatesOversizedTurns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		const org = "acme"
		now := time.Now().Unix()

		// 1. Very long message (oversized turn > 4096 bytes)
		longBody := make([]byte, 5000)
		for i := range longBody {
			longBody[i] = 'A'
		}
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "D0LONG",
			ChannelKind: ChannelDM,
			Sender:      "U_Z",
			SenderUser:  "z",
			Text:        string(longBody),
			EventKey:    "ev_long",
			CreatedAt:   now - 200,
		})

		// 2. Normal recent message
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "D0LONG",
			ChannelKind: ChannelDM,
			Sender:      "U_Z",
			SenderUser:  "z",
			Text:        "recent message",
			EventKey:    "ev_recent",
			CreatedAt:   now - 100,
		})

		currMsg := Message{
			Transport:   "slack",
			Channel:     Channel{ID: "D0LONG", Kind: ChannelDM},
			Sender:      Sender{ExternalID: "U_Z", UserID: "z"},
			Text:        "current message",
			Idempotency: "ev_curr",
		}
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   currMsg.Transport,
			ChannelID:   currMsg.Channel.ID,
			ChannelKind: currMsg.Channel.Kind,
			Sender:      currMsg.Sender.ExternalID,
			SenderUser:  currMsg.Sender.UserID,
			Text:        currMsg.Text,
			EventKey:    currMsg.Idempotency,
			CreatedAt:   now,
		})

		history := loadHistory(ctx, st, org, currMsg)
		if len(history) != 2 {
			t.Fatalf("expected 2 history turns, got %d", len(history))
		}
		// First turn must be truncated to client.TurnBytes with truncation marker
		if len(history[0].Text) > client.TurnBytes+50 {
			t.Fatalf("expected first turn to be capped, got len %d", len(history[0].Text))
		}
		if history[1].Text != "recent message" {
			t.Fatalf("expected second turn to be 'recent message', got %q", history[1].Text)
		}

	})
}

func TestLoadHistory_DropsStaleTurnsAcrossMultiDayGap(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		const org = "acme"
		now := time.Now().Unix()

		// Stale probe from 30 days ago
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "D0GAP",
			ChannelKind: ChannelDM,
			Sender:      "U_Z",
			SenderUser:  "z",
			Text:        "Baseline billing probe: reply with the single word PONG.",
			EventKey:    "ev_probe",
			CreatedAt:   now - 30*86400,
		})

		// Recent conversation from 10 minutes ago + assistant reply
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "D0GAP",
			ChannelKind: ChannelDM,
			Sender:      "U_Z",
			SenderUser:  "z",
			Text:        "can you fix this finally? working?",
			EventKey:    "ev_ask",
			CreatedAt:   now - 600,
		})
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "D0GAP",
			ChannelKind: ChannelDM,
			Sender:      "bot",
			SenderUser:  "assistant",
			Text:        "Want me to keep going on the auth flow issue and the Slack post?",
			EventKey:    "reply:run_1",
			CreatedAt:   now - 500,
		})

		currMsg := Message{
			Transport:   "slack",
			Channel:     Channel{ID: "D0GAP", Kind: ChannelDM},
			Sender:      Sender{ExternalID: "U_Z", UserID: "z"},
			Text:        "yes",
			Idempotency: "ev_yes",
		}
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   currMsg.Transport,
			ChannelID:   currMsg.Channel.ID,
			ChannelKind: currMsg.Channel.Kind,
			Sender:      currMsg.Sender.ExternalID,
			SenderUser:  currMsg.Sender.UserID,
			Text:        currMsg.Text,
			EventKey:    currMsg.Idempotency,
			CreatedAt:   now,
		})

		history := loadHistory(ctx, st, org, currMsg)
		if len(history) != 2 {
			t.Fatalf("expected 2 recent turns (user + assistant), got %d: %+v", len(history), history)
		}
		if history[0].Self || history[0].Text != "can you fix this finally? working?" {
			t.Fatalf("unexpected first turn: %+v", history[0])
		}
		if !history[1].Self || history[1].Text != "Want me to keep going on the auth flow issue and the Slack post?" {
			t.Fatalf("unexpected second turn: %+v", history[1])
		}

	})
}

func TestLoadHistory_PreservesToolSummaryWithReply(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		const org = "acme"
		now := time.Now().Unix()

		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "C0TOOLS",
			ChannelKind: ChannelGroup,
			Sender:      "U_ALICE",
			SenderUser:  "alice",
			Text:        "file an auth issue on hanzoai/iam",
			EventKey:    "ev_t1",
			CreatedAt:   now - 120,
		})

		longProse := strings.Repeat("Filed the issue and documented the root cause. ", 100) // > 4096 bytes
		toolSummary := "- task/create_task_project_issue(key=hanzoai/iam, title=Fix auth callback) -> number=42 https://github.com/hanzoai/iam/issues/42"
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			ChannelID:   "C0TOOLS",
			ChannelKind: ChannelGroup,
			Sender:      "bot",
			SenderUser:  "assistant",
			Text:        withToolSummary(longProse, toolSummary),
			EventKey:    "reply:run_t1",
			CreatedAt:   now - 60,
		})

		currMsg := Message{
			Transport:   "slack",
			Channel:     Channel{ID: "C0TOOLS", Kind: ChannelGroup},
			Sender:      Sender{ExternalID: "U_ALICE", UserID: "alice"},
			Text:        "assign it to me",
			Idempotency: "ev_t2",
		}
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   currMsg.Transport,
			ChannelID:   currMsg.Channel.ID,
			ChannelKind: currMsg.Channel.Kind,
			Sender:      currMsg.Sender.ExternalID,
			SenderUser:  currMsg.Sender.UserID,
			Text:        currMsg.Text,
			EventKey:    currMsg.Idempotency,
			CreatedAt:   now,
		})

		history := loadHistory(ctx, st, org, currMsg)
		if len(history) != 2 {
			t.Fatalf("expected 2 history turns, got %d: %+v", len(history), history)
		}
		if !history[1].Self {
			t.Fatalf("expected second turn to be assistant Self=true, got %+v", history[1])
		}
		if !strings.Contains(history[1].Text, "[Tools used]") || !strings.Contains(history[1].Text, "https://github.com/hanzoai/iam/issues/42") {
			t.Fatalf("expected assistant turn to preserve tool summary and issue URL, got %q", history[1].Text)
		}

	})
}

func TestTurnHistoryUsesSlackPlatformHistoryAndMergesToolSummaries(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		const org = "acme"
		now := time.Now().Unix()

		// Local inbox only knows about a prior Hanzo reply (with tool summary) and
		// never saw Alice's earlier unmentioned error report in the channel.
		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			Account:     "t1",
			ChannelID:   "C0DEV",
			ChannelKind: ChannelGroup,
			Sender:      "bot",
			SenderUser:  "assistant",
			Text:        withToolSummary("Opened PR #99.", "- agent/create_agent_coding -> https://github.com/hanzoai/cloud/pull/99"),
			EventKey:    "reply:run_99",
			CreatedAt:   now - 60,
		})

		prevFetch := fetchHistory
		t.Cleanup(func() { fetchHistory = prevFetch })
		var gotIn *client.ChatHistoryIn
		fetchHistory = func(_ context.Context, app, op string, in *client.ChatHistoryIn) (*client.ChatHistoryOut, error) {
			if app != manifest.Provider || op != client.ChatHistory {
				t.Errorf("unexpected plane target %s/%s", app, op)
			}
			cp := *in
			gotIn = &cp
			return &client.ChatHistoryOut{
				Turns: []client.Turn{
					{Sender: "Alice", Text: "502 on /v1/auth/callback after deploy", At: now - 120},
					{Sender: "UBOT", Self: true, Text: "Opened PR #99.", At: now - 60},
					{Sender: "Bob", Text: "Still seeing 502 on staging", At: now - 30},
				},
			}, nil
		}

		curr := Message{
			Transport: "slack",
			Account:   "t1",
			Channel:   Channel{ID: "C0DEV", Kind: ChannelGroup},
			Sender:    Sender{ExternalID: "U_BOB"},
			ID:        "1700000100.000100",
			Text:      "check staging logs",
		}
		history := turnHistory(ctx, mounted.Load(), org, curr)
		if gotIn == nil || gotIn.Channel != "C0DEV" || gotIn.MessageID != "1700000100.000100" {
			t.Fatalf("expected ChatHistory call for C0DEV/1700000100.000100, got %+v", gotIn)
		}
		if len(history) != 3 {
			t.Fatalf("expected 3 turns from Slack platform history, got %d: %+v", len(history), history)
		}
		if history[0].Sender != "Alice" || !strings.Contains(history[0].Text, "502 on /v1/auth/callback") {
			t.Fatalf("expected unmentioned channel context from Alice, got %+v", history[0])
		}
		if !history[1].Self || !strings.Contains(history[1].Text, "[Tools used]") || !strings.Contains(history[1].Text, "pull/99") {
			t.Fatalf("expected local tool summary merged onto Slack assistant reply, got %+v", history[1])
		}
		if history[2].Sender != "Bob" || history[2].Text != "Still seeing 502 on staging" {
			t.Fatalf("expected Bob's follow-up, got %+v", history[2])
		}
	})
}

func TestTurnHistoryFallsBackToInboxOnSlackError(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		ctx := context.Background()
		const org = "acme"
		now := time.Now().Unix()

		_ = st.insertInbox(ctx, inboxRow{
			Org:         org,
			Transport:   "slack",
			Account:     "t1",
			ChannelID:   "C0SCOPE",
			ChannelKind: ChannelGroup,
			Sender:      "U_ALICE",
			SenderUser:  "alice",
			Text:        "previous addressed question",
			EventKey:    "ev_1",
			CreatedAt:   now - 60,
		})

		prevFetch := fetchHistory
		t.Cleanup(func() { fetchHistory = prevFetch })
		fetchHistory = func(context.Context, string, string, *client.ChatHistoryIn) (*client.ChatHistoryOut, error) {
			return nil, errors.New("slack conversations.history: missing_scope (needed: channels:history)")
		}

		curr := Message{
			Transport:   "slack",
			Account:     "t1",
			Channel:     Channel{ID: "C0SCOPE", Kind: ChannelGroup},
			Sender:      Sender{ExternalID: "U_ALICE"},
			Text:        "try again",
			Idempotency: "ev_2",
		}
		history := turnHistory(ctx, mounted.Load(), org, curr)
		if len(history) != 1 || history[0].Text != "previous addressed question" {
			t.Fatalf("expected graceful fallback to inbox history, got %+v", history)
		}
	})
}
