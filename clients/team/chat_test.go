package team

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestHTMLMarkupEscapesAndWraps(t *testing.T) {
	got := htmlMarkup("a <script>x</script>\n\nb & c")
	if strings.Contains(got, "<script>") {
		t.Fatalf("htmlMarkup did not escape html: %q", got)
	}
	if got != "<p>a &lt;script&gt;x&lt;/script&gt;</p><p>b &amp; c</p>" {
		t.Fatalf("htmlMarkup wrong: %q", got)
	}
	if htmlMarkup("") != "<p></p>" {
		t.Fatalf("htmlMarkup(empty) = %q, want <p></p>", htmlMarkup(""))
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
	uid := "u-bot"
	ref := PersonRef(uid) // person-u-bot
	if !mentionsBot(`hey <span data-id="`+ref+`">@bot</span> please`, uid) {
		t.Fatal("mentionsBot missed a reference to the bot")
	}
	if mentionsBot("hey nobody here", uid) {
		t.Fatal("mentionsBot matched a message with no reference")
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
	m := chatMsg{space: dmID, authorUID: human, message: "<p>hi</p>"}
	got := srv.replyTargets(org, ws, m, byUID)
	if len(got) != 1 || got[0].ID != bot.ID {
		t.Fatalf("DM replyTargets = %v, want the one bot member", got)
	}

	// A DM between two humans (no bot member) → no target.
	putDoc(t, srv, org, ws, map[string]any{
		"_id": "dm-2", "_class": clDirectMessage, "space": "dm-2",
		"members": []any{human, "someone-else"},
	})
	if got := srv.replyTargets(org, ws, chatMsg{space: "dm-2", authorUID: human}, byUID); len(got) != 0 {
		t.Fatalf("human-only DM replyTargets = %v, want none", got)
	}
}

func TestReplyTargetsChannelMentionOnly(t *testing.T) {
	const org, human = "acme", "22222222-2222-4222-8222-222222222222"
	bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
	srv, _, ws := rosterServer(t, org, human, "Ada", []Bot{bot})
	botUID := botUserID(bot.ID)
	byUID := map[string]Bot{botUID: bot}

	chID := "channel-1"
	putDoc(t, srv, org, ws, map[string]any{"_id": chID, "_class": clChannel, "space": chID})

	// Plain channel chatter → the bot stays quiet.
	if got := srv.replyTargets(org, ws, chatMsg{space: chID, authorUID: human, message: "<p>hello all</p>"}, byUID); len(got) != 0 {
		t.Fatalf("un-mentioned channel message replyTargets = %v, want none", got)
	}
	// A message @-mentioning the bot → it answers.
	mention := `<p>hey <span data-id="` + PersonRef(botUID) + `">@enso</span></p>`
	if got := srv.replyTargets(org, ws, chatMsg{space: chID, authorUID: human, message: mention}, byUID); len(got) != 1 || got[0].ID != bot.ID {
		t.Fatalf("mention channel message replyTargets = %v, want the bot", got)
	}
}

// ── full reply loop (fake runner, real store) ──────────────────────────────────

// fakeRunner records the one call and returns a fixed answer, signalling done.
type fakeRunner struct {
	mu                        sync.Mutex
	called                    bool
	org, userSub, agent, text string
	out                       string
	err                       error
}

func (f *fakeRunner) run(_ context.Context, org, userSub, agentID, input string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.org, f.userSub, f.agent, f.text = org, userSub, agentID, input
	return f.out, f.err
}

func TestMaybeAgentReplyAnswersDirectMessage(t *testing.T) {
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
}

func TestMaybeAgentReplyNeverAnswersItself(t *testing.T) {
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
}

func TestMaybeAgentReplyDisabledWhenNoRunner(t *testing.T) {
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
}

// ── test helpers ───────────────────────────────────────────────────────────────

const clChannel = "chunter:class:Channel"

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
	return mustMarshal(t, map[string]any{
		"_class": clTxCreate, "objectId": newMsgID(), "objectClass": clChatMessage,
		"objectSpace": space, "attachedTo": space, "attachedToClass": spaceClass,
		"collection": "messages", "createdBy": author, "modifiedBy": author,
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
