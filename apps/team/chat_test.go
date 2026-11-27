package team

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// ── pure helpers ───────────────────────────────────────────────────────────────

func TestParseChatMessageRecognizesChunterCreate(t *testing.T) {
	raw := mustMarshal(t, map[string]any{
		"_class": clTxCreate, "objectId": "m1", "objectClass": clChatMessage,
		"objectSpace": "dm1", "attachedTo": "dm1", "attachedToClass": clDirectMessage,
		"collection": "messages", "createdBy": "hanzo:u-human",
		"attributes": map[string]any{"message": "<p>hi</p>"},
	})
	m, ok := parseChatMessage(raw)
	if !ok {
		t.Fatal("parseChatMessage did not recognize a ChatMessage create")
	}
	if m.space != "dm1" || m.attachedTo != "dm1" || m.attachedToClass != clDirectMessage ||
		m.collection != "messages" || m.message != "<p>hi</p>" || m.authorUID != "u-human" {
		t.Fatalf("parsed chatMsg wrong: %+v", m)
	}
}

func TestParseChatMessageIgnoresNonChat(t *testing.T) {
	// A Person create (roster projection) must not be read as a chat message.
	raw := mustMarshal(t, map[string]any{
		"_class": clTxCreate, "objectId": "p1", "objectClass": clPerson,
		"objectSpace": spaceContacts, "attributes": map[string]any{"name": ",x"},
	})
	if _, ok := parseChatMessage(raw); ok {
		t.Fatal("a Person create was misread as a chat message")
	}
	// A ChatMessage create missing its conversation coordinates is not answerable.
	raw2 := mustMarshal(t, map[string]any{
		"_class": clTxCreate, "objectId": "m2", "objectClass": clChatMessage,
		"attributes": map[string]any{"message": "hi"},
	})
	if _, ok := parseChatMessage(raw2); ok {
		t.Fatal("a ChatMessage with no space/attachedTo was accepted")
	}
	// An update tx is never a create.
	raw3 := mustMarshal(t, map[string]any{"_class": clTxUpdate, "objectId": "m1", "objectClass": clChatMessage})
	if _, ok := parseChatMessage(raw3); ok {
		t.Fatal("an update tx was misread as a create")
	}
}

func TestPlainText(t *testing.T) {
	cases := map[string]string{
		"<p>hello <b>world</b></p>":     "hello world",
		"a<br>b":                        "a b",
		"<p>one</p><p>two</p>":          "one two",
		"tom &amp; jerry &lt;3":         "tom & jerry <3",
		"  <div> spaced   out </div>  ": "spaced out",
		"":                              "",
	}
	for in, want := range cases {
		if got := plainText(in); got != want {
			t.Errorf("plainText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAnswerMarkupIsThePlatformsJSON: an agent's answer is stored as the
// ProseMirror JSON the Team client parses — never HTML, which markupToJSON would
// render as literal tags — and whatever markup the MODEL wrote is text in a
// paragraph, never markup of its own.
func TestAnswerMarkupIsThePlatformsJSON(t *testing.T) {
	cases := map[string]string{
		"<p>yes.</p>":                `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"yes."}]}]}`,
		"<p>line 1</p><p>line 2</p>": `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"line 1"}]},{"type":"paragraph","content":[{"type":"text","text":"line 2"}]}]}`,
		"one<br>two":                 `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]},{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}`,
		"<p>done &amp; dusted</p>":   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"done & dusted"}]}]}`,
		"a <script>x</script>":       `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"a x"}]}]}`,
		"":                           `{"type":"doc","content":[{"type":"paragraph"}]}`,
	}
	for in, want := range cases {
		if got := answerMarkup(in); got != want {
			t.Errorf("answerMarkup(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestPlainTextReadsJSONMarkup: the platform's stored form reads as its words,
// with a mention as @Label.
func TestPlainTextReadsJSONMarkup(t *testing.T) {
	stored := textMarkup("ping <@11111111-1111-4111-8111-111111111111> now\nsecond line",
		map[string]string{"11111111-1111-4111-8111-111111111111": "Ada"})
	if got := plainText(stored); got != "ping @Ada now second line" {
		t.Fatalf("plainText(%s) = %q", stored, got)
	}
}

func TestStripHanzo(t *testing.T) {
	if stripHanzo("hanzo:abc") != "abc" {
		t.Fatal("stripHanzo did not drop the prefix")
	}
	if stripHanzo("abc") != "abc" {
		t.Fatal("stripHanzo mangled a bare uuid")
	}
}

func TestMentionsBot(t *testing.T) {
	uid := "11111111-1111-4111-8111-111111111111"
	ref := PersonRef(uid)
	for _, markup := range []string{
		textMarkup("hey <@"+uid+"> please", map[string]string{uid: "bot"}),
		`<p>hey <span data-type="reference" data-id="` + ref + `">@bot</span> please</p>`,
	} {
		if !mentionsBot(markup, uid) {
			t.Fatalf("mentionsBot missed the reference node in %s", markup)
		}
	}
	// A person id that is merely TEXT mentions nobody — anybody can type it —
	// and neither does a span that is not a reference node.
	for _, markup := range []string{
		"hey " + ref + " please",
		textMarkup("hey "+ref, nil),
		`<p><span data-id="` + ref + `">@bot</span></p>`,
		"hey nobody here",
	} {
		if mentionsBot(markup, uid) {
			t.Fatalf("mentionsBot matched %s, which holds no reference node", markup)
		}
	}
	if mentionsBot("anything", "") {
		t.Fatal("mentionsBot must never match an empty uid")
	}
}

func TestToStringSlice(t *testing.T) {
	got := toStringSlice([]any{"a", 1, "b", nil})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("toStringSlice = %v, want [a b]", got)
	}
	if toStringSlice(nil) != nil || toStringSlice("nope") != nil {
		t.Fatal("toStringSlice must be nil-safe for non-arrays")
	}
}

func TestNewMsgIDUniqueAndOpaque(t *testing.T) {
	a, b := newMsgID(), newMsgID()
	if a == "" || a == b {
		t.Fatalf("newMsgID not unique/non-empty: %q %q", a, b)
	}
	if strings.ContainsAny(a, "/ \t") {
		t.Fatalf("newMsgID is not a clean ref segment: %q", a)
	}
}

// ── addressing policy ──────────────────────────────────────────────────────────

func TestReplyTargetsDirectMessage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "11111111-1111-4111-8111-111111111111"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Ada", []Bot{bot})
		botUID := botUserID(bot.ID)

		dmID := "dm-1"
		putDoc(t, srv, org, ws, map[string]any{
			"_id": dmID, "_class": clDirectMessage, "space": dmID,
			"members": []any{human, botUID},
		})
		byUID := map[string]Bot{botUID: bot}
		m := chatMsg{class: clChatMessage, space: dmID, attachedTo: dmID, attachedToClass: clDirectMessage, authorUID: human, message: "<p>hi</p>"}
		got := srv.replyTargets(org, ws, m, byUID)
		if len(got) != 1 || got[0].ID != bot.ID {
			t.Fatalf("DM replyTargets = %v, want the one bot member", got)
		}

		// A DM between two humans (no bot member) → no target.
		putDoc(t, srv, org, ws, map[string]any{
			"_id": "dm-2", "_class": clDirectMessage, "space": "dm-2",
			"members": []any{human, "someone-else"},
		})
		if got := srv.replyTargets(org, ws, chatMsg{class: clChatMessage, space: "dm-2", attachedTo: "dm-2", attachedToClass: clDirectMessage, authorUID: human}, byUID); len(got) != 0 {
			t.Fatalf("human-only DM replyTargets = %v, want none", got)
		}
	})
}

func TestReplyTargetsChannelMentionOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "22222222-2222-4222-8222-222222222222"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Ada", []Bot{bot})
		botUID := botUserID(bot.ID)
		byUID := map[string]Bot{botUID: bot}

		chID := "channel-1"
		putDoc(t, srv, org, ws, map[string]any{"_id": chID, "_class": clChannel, "space": chID, "members": []any{human, botUID}})
		at := func(msg string) chatMsg {
			return chatMsg{class: clChatMessage, space: chID, attachedTo: chID, attachedToClass: clChannel, authorUID: human, message: msg}
		}

		// Plain channel chatter → the bot stays quiet.
		if got := srv.replyTargets(org, ws, at("<p>hello all</p>"), byUID); len(got) != 0 {
			t.Fatalf("un-mentioned channel message replyTargets = %v, want none", got)
		}
		// A message @-mentioning the bot → it answers.
		mention := textMarkup("hey <@"+botUID+">", map[string]string{botUID: "enso"})
		if got := srv.replyTargets(org, ws, at(mention), byUID); len(got) != 1 || got[0].ID != bot.ID {
			t.Fatalf("mention channel message replyTargets = %v, want the bot", got)
		}
		// A PUBLIC channel is the whole space's, and every agent of the org is a
		// member of the space: the mention is enough, no per-channel add.
		putDoc(t, srv, org, ws, map[string]any{"_id": "channel-2", "_class": clChannel, "space": "channel-2", "members": []any{human}})
		in := func(channel string) chatMsg {
			m := at(mention)
			m.space, m.attachedTo = channel, channel
			return m
		}
		if got := srv.replyTargets(org, ws, in("channel-2"), byUID); len(got) != 1 {
			t.Fatalf("mention in a public channel the bot was not added to = %v, want the bot", got)
		}
		// A PRIVATE channel is its members': the bot answers there only once added.
		putDoc(t, srv, org, ws, map[string]any{"_id": "private-1", "_class": clChannel, "space": "private-1", "private": true, "members": []any{human}})
		if got := srv.replyTargets(org, ws, in("private-1"), byUID); len(got) != 0 {
			t.Fatalf("mention in a private channel the bot is not in = %v, want none", got)
		}
		putDoc(t, srv, org, ws, map[string]any{"_id": "private-2", "_class": clChannel, "space": "private-2", "private": true, "members": []any{human, botUID}})
		if got := srv.replyTargets(org, ws, in("private-2"), byUID); len(got) != 1 {
			t.Fatalf("mention in a private channel the bot is in = %v, want the bot", got)
		}
	})
}

// TestTurnsAreCappedPerOrgAndPerAsker: one person may hold two turns, one org
// four, the process its ceiling — and a turn given back frees its slot.
func TestTurnsAreCappedPerOrgAndPerAsker(t *testing.T) {
	l := newTurns(5, 4, 2)
	if !l.take("a", "u1") || !l.take("a", "u1") || l.take("a", "u1") {
		t.Fatal("one asker got a third turn")
	}
	if !l.take("a", "u2") || !l.take("a", "u2") || l.take("a", "u3") {
		t.Fatal("one org got a fifth turn")
	}
	if !l.take("b", "u1") || l.take("b", "u2") {
		t.Fatal("the process went past its ceiling")
	}
	l.give("a", "u1")
	if !l.take("a", "u3") {
		t.Fatal("a returned turn was not free again")
	}
	var none *turns
	if !none.take("x", "y") {
		t.Fatal("a nil limiter capped")
	}
}

// ── full reply loop (fake runner, real store) ──────────────────────────────────

// fakeRunner records the one call and returns a fixed answer, signalling done.
type fakeRunner struct {
	mu                        sync.Mutex
	called                    bool
	org, userSub, agent, text string
	history                   []client.Turn
	out                       string
	err                       error
}

func (f *fakeRunner) run(_ context.Context, org, userSub, agentID, input string, history []client.Turn) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.org, f.userSub, f.agent, f.text, f.history = org, userSub, agentID, input, history
	return f.out, f.err
}

func TestMaybeAgentReplyAnswersDirectMessage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "maxpower", "113d4dd4-2486-40de-be2b-88d6e3e0b718"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Dave Lorenzini", []Bot{bot})
		botUID := botUserID(bot.ID)

		fr := &fakeRunner{out: "Hello Dave, I am enso."}
		srv.runAgent = fr.run

		dmID := "dm-enso"
		putDoc(t, srv, org, ws, map[string]any{
			"_id": dmID, "_class": clDirectMessage, "space": dmID,
			"members": []any{human, botUID},
		})

		inbound := chatCreateRaw(t, dmID, clDirectMessage, "hanzo:"+human, "<p>hello <b>agent</b></p>")
		srv.maybeAgentReply(org, ws, []json.RawMessage{inbound})

		waitFor(t, 2*time.Second, func() bool {
			return len(botMessages(t, srv, org, ws, botUID)) == 1
		}, "the bot never posted a reply")

		// The reply is the model output, authored BY the bot, in the SAME conversation.
		reply := botMessages(t, srv, org, ws, botUID)[0]
		if reply["space"] != dmID || reply["attachedTo"] != dmID {
			t.Fatalf("reply not in the DM conversation: %v", reply)
		}
		attrs, _ := reply["attributes"].(map[string]any)
		_ = attrs
		if got := str(reply["message"]); !strings.Contains(got, "I am enso") {
			t.Fatalf("reply message = %q, want the model output", got)
		}

		// The runner was invoked on-behalf-of the human, with the agent id and the
		// PLAIN-TEXT prompt (markup stripped).
		fr.mu.Lock()
		defer fr.mu.Unlock()
		if !fr.called || fr.org != org || fr.userSub != human || fr.agent != bot.ID || fr.text != "hello agent" {
			t.Fatalf("runner call wrong: called=%v org=%q sub=%q agent=%q text=%q", fr.called, fr.org, fr.userSub, fr.agent, fr.text)
		}
	})
}

// TestABotIsShownTheConversation: a bot answering in a DM is sent the messages
// before the one it answers, its own as Self; in a channel thread it is sent the
// message that opened the thread and the replies before it, each said by a named
// person. Nothing said after the question, and not the question itself.
func TestABotIsShownTheConversation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "44444444-4444-4444-8444-444444444444"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Ada Lovelace", []Bot{bot})
		botUID := botUserID(bot.ID)
		putDoc(t, srv, org, ws, map[string]any{
			"_id": PersonRef(human), "_class": clPerson, "personUuid": human, "name": "Lovelace,Ada",
		})
		now := time.Now().UnixMilli()
		said := func(id, class, at, by, msg string, when int64, extra map[string]any) {
			doc := map[string]any{
				"_id": id, "_class": class, "space": at, "attachedTo": at,
				"createdBy": "hanzo:" + by, "createdOn": when, "message": msg,
			}
			for k, v := range extra {
				doc[k] = v
			}
			putDoc(t, srv, org, ws, doc)
		}

		// A DM: what came before, and something said after the question.
		dm := "dm-history"
		putDoc(t, srv, org, ws, map[string]any{
			"_id": dm, "_class": clDirectMessage, "space": dm, "members": []any{human, botUID},
		})
		said("m1", clChatMessage, dm, human, "<p>weather in Benicia</p>", now-3000, nil)
		said("m2", clChatMessage, dm, botUID, "<p>Sunny, 21C.</p>", now-2000, nil)
		said("m9", clChatMessage, dm, human, "<p>later</p>", now+60_000, nil)
		fr := &fakeRunner{out: "Still sunny."}
		srv.runAgent = fr.run
		srv.maybeAgentReply(org, ws, []json.RawMessage{chatCreateRawAt(t, dm, clDirectMessage, "hanzo:"+human, "<p>try again</p>", now)})
		waitFor(t, 2*time.Second, func() bool { fr.mu.Lock(); defer fr.mu.Unlock(); return fr.called }, "the bot never answered the DM")
		fr.mu.Lock()
		got := fr.history
		fr.mu.Unlock()
		want := []client.Turn{
			{Sender: human, Text: "weather in Benicia", At: now - 3000},
			{Sender: botUID, Self: true, Text: "Sunny, 21C.", At: now - 2000},
		}
		if !sameTurns(got, want) {
			t.Fatalf("DM history = %+v, want %+v", got, want)
		}

		// A thread in a channel: the message that opened it, the replies before.
		ch := "channel-history"
		putDoc(t, srv, org, ws, map[string]any{"_id": ch, "_class": clChannel, "space": ch, "members": []any{human}})
		said("root", clChatMessage, ch, human, "<p>the deploy failed</p>", now-5000, nil)
		said("r1", clThreadMessage, "root", botUID, "<p>checking the logs</p>", now-4000,
			map[string]any{"space": ch, "objectId": ch, "objectClass": clChannel})
		said("elsewhere", clChatMessage, ch, human, "<p>unrelated</p>", now-4500, nil)
		ask := mustMarshal(t, map[string]any{
			"_class": clTxCreate, "objectId": newMsgID(), "objectClass": clThreadMessage,
			"objectSpace": ch, "attachedTo": "root", "attachedToClass": clChatMessage,
			"collection": "replies", "createdBy": "hanzo:" + human, "createdOn": now,
			"attributes": map[string]any{
				"message":  textMarkup("<@"+botUID+"> and now?", map[string]string{botUID: "enso"}),
				"objectId": ch, "objectClass": clChannel,
			},
		})
		fr2 := &fakeRunner{out: "Found it."}
		srv.runAgent = fr2.run
		srv.maybeAgentReply(org, ws, []json.RawMessage{ask})
		waitFor(t, 2*time.Second, func() bool { fr2.mu.Lock(); defer fr2.mu.Unlock(); return fr2.called }, "the bot never answered in the thread")
		fr2.mu.Lock()
		got = fr2.history
		fr2.mu.Unlock()
		want = []client.Turn{
			{Sender: human, Text: "Ada Lovelace: the deploy failed", At: now - 5000},
			{Sender: botUID, Self: true, Text: "checking the logs", At: now - 4000},
		}
		if !sameTurns(got, want) {
			t.Fatalf("thread history = %+v, want %+v", got, want)
		}
	})
}

func sameTurns(a, b []client.Turn) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMaybeAgentReplyNeverAnswersItself(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "33333333-3333-4333-8333-333333333333"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Ada", []Bot{bot})
		botUID := botUserID(bot.ID)

		fr := &fakeRunner{out: "should not fire"}
		srv.runAgent = fr.run

		dmID := "dm-enso"
		putDoc(t, srv, org, ws, map[string]any{
			"_id": dmID, "_class": clDirectMessage, "space": dmID,
			"members": []any{human, botUID},
		})

		// The message is authored BY THE BOT — the loop guard must suppress any reply.
		selfMsg := chatCreateRaw(t, dmID, clDirectMessage, "hanzo:"+botUID, "<p>my own message</p>")
		srv.maybeAgentReply(org, ws, []json.RawMessage{selfMsg})

		// Give any (erroneous) goroutine a chance, then assert nothing happened.
		time.Sleep(150 * time.Millisecond)
		fr.mu.Lock()
		called := fr.called
		fr.mu.Unlock()
		if called {
			t.Fatal("runner fired for a bot-authored message (infinite-loop risk)")
		}
		if n := len(botMessages(t, srv, org, ws, botUID)); n != 0 {
			t.Fatalf("bot posted %d messages for its own message, want 0", n)
		}
	})
}

func TestMaybeAgentReplyDisabledWhenNoRunner(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "44444444-4444-4444-8444-444444444444"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Ada", []Bot{bot})
		botUID := botUserID(bot.ID)
		srv.runAgent = nil // responder off

		dmID := "dm-enso"
		putDoc(t, srv, org, ws, map[string]any{
			"_id": dmID, "_class": clDirectMessage, "space": dmID, "members": []any{human, botUID},
		})
		inbound := chatCreateRaw(t, dmID, clDirectMessage, "hanzo:"+human, "<p>hi</p>")
		srv.maybeAgentReply(org, ws, []json.RawMessage{inbound}) // must be a no-op, no panic
		time.Sleep(100 * time.Millisecond)
		if n := len(botMessages(t, srv, org, ws, botUID)); n != 0 {
			t.Fatalf("reply posted with no runner wired: %d", n)
		}
	})
}

// ── hardening / anti-storm regression ──────────────────────────────────────────

// countingRunner records how many times it was invoked (thread-safe) and returns a
// fixed outcome. It is the "outbound call" probe the boot-backlog test asserts on.
type countingRunner struct {
	mu   sync.Mutex
	n    int
	out  string
	err  error
	gate chan struct{} // if non-nil, each call blocks on it (for concurrency tests)
}

func (c *countingRunner) run(_ context.Context, _, _, _, _ string, _ []client.Turn) (string, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	if c.gate != nil {
		<-c.gate
	}
	return c.out, c.err
}

func (c *countingRunner) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// TestNoReplyToBacklogAtBoot is the anti-storm invariant the writer post-mortem
// demands: a space with a BACKLOG of old messages, replayed through the
// responder right after boot, must produce ZERO outbound model calls. Only a
// genuinely fresh (post-boot) message is ever answered. This is what prevents "a
// replayed message backlog fans out into thousands of HTTP calls".
func TestNoReplyToBacklogAtBoot(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "maxpower", "113d4dd4-2486-40de-be2b-88d6e3e0b718"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Dave", []Bot{bot})
		botUID := botUserID(bot.ID)

		cr := &countingRunner{out: "hi"}
		srv.runAgent = cr.run
		srv.startedAt = time.Now().UnixMilli() // boot NOW

		dmID := "dm-enso"
		putDoc(t, srv, org, ws, map[string]any{
			"_id": dmID, "_class": clDirectMessage, "space": dmID, "members": []any{human, botUID},
		})

		// A backlog of 500 messages, each created an hour before boot (a replay/backfill).
		old := srv.startedAt - int64(60*60*1000)
		backlog := make([]json.RawMessage, 0, 500)
		for range 500 {
			backlog = append(backlog, chatCreateRawAt(t, dmID, clDirectMessage, "hanzo:"+human, "<p>old</p>", old))
		}
		srv.maybeAgentReply(org, ws, backlog)

		time.Sleep(150 * time.Millisecond) // give any (erroneous) goroutine a chance
		if n := cr.calls(); n != 0 {
			t.Fatalf("backlog replay fired %d outbound model calls, want 0 (storm risk)", n)
		}

		// A single FRESH message IS answered — the filter is precise, not a blanket off.
		fresh := chatCreateRawAt(t, dmID, clDirectMessage, "hanzo:"+human, "<p>hello now</p>", time.Now().UnixMilli())
		srv.maybeAgentReply(org, ws, []json.RawMessage{fresh})
		waitFor(t, 2*time.Second, func() bool { return cr.calls() == 1 }, "a fresh post was not answered")
	})
}

// TestConcurrencyCapBounded proves the hard concurrency cap: with the process
// ceiling set to 2, firing 8 messages to DISTINCT conversations must never run
// more than 2 turns at once — the surplus is DROPPED, not queued (no unbounded
// goroutine/HTTP fan-out).
func TestConcurrencyCapBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "11111111-1111-4111-8111-111111111111"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Ada", []Bot{bot})
		botUID := botUserID(bot.ID)

		gate := make(chan struct{})
		cr := &countingRunner{out: "ok", gate: gate}
		srv.runAgent = cr.run
		srv.turns = newTurns(2, 8, 8) // process cap = 2

		// 8 distinct DMs (distinct single-flight keys) all with the bot.
		for i := range 8 {
			dm := "dm-" + string(rune('a'+i))
			putDoc(t, srv, org, ws, map[string]any{
				"_id": dm, "_class": clDirectMessage, "space": dm, "members": []any{human, botUID},
			})
			srv.maybeAgentReply(org, ws, []json.RawMessage{chatCreateRaw(t, dm, clDirectMessage, "hanzo:"+human, "<p>hi</p>")})
		}

		// Exactly cap(=2) turns acquire the semaphore and block on the gate; the other 6
		// hit the default branch and drop. Wait for the 2 to be in-flight, then confirm
		// no third starts.
		waitFor(t, 2*time.Second, func() bool { return cr.calls() == 2 }, "cap turns never started")
		time.Sleep(150 * time.Millisecond)
		if n := cr.calls(); n != 2 {
			t.Fatalf("in-flight turns = %d, want exactly 2 (cap); surplus must drop, not queue", n)
		}
		close(gate) // release the 2
	})
}

// TestSingleFlightPerConversation proves at most one in-flight answer per
// (space, space, bot): a burst of 5 messages to the SAME DM collapses to ONE
// turn while it runs; the rest are dropped.
func TestSingleFlightPerConversation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "22222222-2222-4222-8222-222222222222"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Ada", []Bot{bot})
		botUID := botUserID(bot.ID)

		gate := make(chan struct{})
		cr := &countingRunner{out: "ok", gate: gate}
		srv.runAgent = cr.run

		dm := "dm-solo"
		putDoc(t, srv, org, ws, map[string]any{
			"_id": dm, "_class": clDirectMessage, "space": dm, "members": []any{human, botUID},
		})
		for range 5 {
			srv.maybeAgentReply(org, ws, []json.RawMessage{chatCreateRaw(t, dm, clDirectMessage, "hanzo:"+human, "<p>spam</p>")})
		}
		waitFor(t, 2*time.Second, func() bool { return cr.calls() == 1 }, "no turn started")
		time.Sleep(150 * time.Millisecond)
		if n := cr.calls(); n != 1 {
			t.Fatalf("single-flight broke: %d concurrent turns for one conversation, want 1", n)
		}
		close(gate)
	})
}

// TestCircuitBreakerBacksOff proves a persistently-failing agent is skipped after
// breakerThreshold failures — the backoff that turns a 403 storm into a quiet
// trickle. Driven synchronously (replyAsBot direct) for determinism.
func TestCircuitBreakerBacksOff(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "33333333-3333-4333-8333-333333333333"
		bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
		srv, _, ws := rosterServer(t, org, human, "Ada", []Bot{bot})
		botUID := botUserID(bot.ID)

		cr := &countingRunner{err: errForced}
		srv.runAgent = cr.run

		// Each call to a DISTINCT conversation (so single-flight never collapses them),
		// same bot. After breakerThreshold failures the circuit opens and the runner is
		// no longer called.
		for i := range 10 {
			dm := "dm-" + string(rune('a'+i))
			putDoc(t, srv, org, ws, map[string]any{
				"_id": dm, "_class": clDirectMessage, "space": dm, "members": []any{human, botUID},
			})
			m := chatMsg{space: dm, attachedTo: dm, attachedToClass: clDirectMessage, collection: "messages",
				authorUID: human, message: "<p>hi</p>", createdOn: time.Now().UnixMilli()}
			srv.replyAsBot(org, ws, m, bot) // synchronous
		}
		if n := cr.calls(); n != breakerThreshold {
			t.Fatalf("runner called %d times, want %d (circuit must open after threshold)", n, breakerThreshold)
		}
		if !srv.breakerOpen(bot.ID) {
			t.Fatal("circuit did not open after repeated failures")
		}
	})
}

// ── test helpers ───────────────────────────────────────────────────────────────

var errForced = errForcedType("forced failure")

type errForcedType string

func (e errForcedType) Error() string { return string(e) }

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func putDoc(t *testing.T, srv *transServer, org, ws string, doc map[string]any) {
	t.Helper()
	if err := srv.store.put(org, ws, doc); err != nil {
		t.Fatalf("put doc: %v", err)
	}
}

func chatCreateRaw(t *testing.T, space, spaceClass, author, message string) json.RawMessage {
	t.Helper()
	return chatCreateRawAt(t, space, spaceClass, author, message, time.Now().UnixMilli())
}

// chatCreateRawAt is chatCreateRaw with an explicit createdOn (unix millis) so a
// test can forge a backlog message that predates the server boot.
func chatCreateRawAt(t *testing.T, space, spaceClass, author, message string, createdOn int64) json.RawMessage {
	t.Helper()
	return mustMarshal(t, map[string]any{
		"_class": clTxCreate, "objectId": newMsgID(), "objectClass": clChatMessage,
		"objectSpace": space, "attachedTo": space, "attachedToClass": spaceClass,
		"collection": "messages", "createdBy": author, "modifiedBy": author,
		"createdOn": createdOn, "modifiedOn": createdOn,
		"attributes": map[string]any{"message": message},
	})
}

// botMessages returns every ChatMessage doc authored by the bot (social id).
func botMessages(t *testing.T, srv *transServer, org, ws, botUID string) []map[string]any {
	t.Helper()
	docs, err := srv.store.byClasses(org, ws, []string{clChatMessage})
	if err != nil {
		t.Fatalf("byClasses: %v", err)
	}
	var out []map[string]any
	for _, d := range docs {
		if str(d["createdBy"]) == "hanzo:"+botUID {
			out = append(out, d)
		}
	}
	return out
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
