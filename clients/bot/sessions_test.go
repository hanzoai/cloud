package bot

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// These tests drive the two ways a conversation is started and the two ways one
// is stopped, because each pair has to agree: a create and a send both end in
// the same turn, and a chat abort and a session abort both end the same
// goroutine.

// sessionRow reads one row back the way the sidebar does.
func sessionRow(t *testing.T, app *zip.App, w who, key string) map[string]any {
	t.Helper()
	_, frame := ask(t, app, w, "d:"+key, "sessions.describe", `{"key":"`+key+`"}`)
	if frame["ok"] != true {
		t.Fatalf("sessions.describe refused: %v", frame)
	}
	row, _ := payload(t, frame)["session"].(map[string]any)
	return row
}

// The New Chat control sends the parent it was opened from and the two hook
// flags beside it whenever a session is already open
// (ui/src/lib/sessions/create.ts). A gateway that refuses those refuses the
// primary way a conversation is started.
func TestSessionCreateTakesWhatNewChatSends(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "hi"}, "flash"))
	me := who{org: "acme"}

	if _, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"first"}`); frame["ok"] != true {
		t.Fatalf("the first session could not be created: %v", frame)
	}
	_, frame := ask(t, app, me, "2:a", "sessions.create",
		`{"agentId":"main","parentSessionKey":"first","emitCommandHooks":true,"succeedsParent":false}`)
	if frame["ok"] != true {
		t.Fatalf("New Chat was refused: %v", frame)
	}
	out := payload(t, frame)
	key, _ := out["key"].(string)
	if key == "" {
		t.Fatalf("the create answered no key: %v", out)
	}
	// The parent is provenance, and the row keeps it.
	entry, _ := out["entry"].(map[string]any)
	if entry["spawnedBy"] != "first" {
		t.Errorf("the new session records its parent as %v", entry["spawnedBy"])
	}

	// succeedsParent:true asks the new session to end its parent, which nothing
	// here does — so it is refused rather than accepted and ignored.
	_, frame = ask(t, app, me, "3:a", "sessions.create",
		`{"parentSessionKey":"first","emitCommandHooks":true,"succeedsParent":true}`)
	if frame["ok"] != false {
		t.Errorf("succeedsParent was accepted and would do nothing: %v", frame)
	}

	// A parameter that asks for a subsystem this gateway does not have is still
	// refused, so the acceptance above is about those three names and not about
	// the check being gone.
	for _, params := range []string{
		`{"fork":true}`, `{"projectId":"p"}`, `{"worktree":true}`,
		`{"execNode":"n1"}`, `{"cwd":"/tmp"}`,
	} {
		if _, frame := ask(t, app, me, "4:a", "sessions.create", params); frame["ok"] != false {
			t.Errorf("%s was accepted and cannot be honoured", params)
		}
	}
}

// A create naming a key that already exists adopts the row, and the message it
// carried still runs: the caller asked for a turn, and landing on an existing
// row is not a reason to drop it.
func TestSessionCreateOnAHeldKeyStillSendsItsMessage(t *testing.T) {
	app := mount(t, aiServes(&aiFake{reply: "answered"}, "flash"))
	me := who{org: "acme"}

	if _, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"k1"}`); frame["ok"] != true {
		t.Fatalf("create: %v", frame)
	}
	_, frame := ask(t, app, me, "2:a", "sessions.create", `{"key":"k1","message":"hello there"}`)
	if frame["ok"] != true {
		t.Fatalf("a create adopting a key refused: %v", frame)
	}
	out := payload(t, frame)
	if out["runStarted"] != true {
		t.Fatalf("the adopted session started no run: %v", out)
	}
	if id, _ := out["runId"].(string); id == "" {
		t.Errorf("the create answered no run id: %v", out)
	}
	if seq, _ := out["messageSeq"].(float64); seq != 1 {
		t.Errorf("the create answered messageSeq %v, want the position the message took", out["messageSeq"])
	}
	messages := aiSettled(t, app, me, "k1", 2)
	if aiSaid(t, messages[0]) != "hello there" {
		t.Errorf("the message was lost: %v", messages)
	}
}

// A create names its own idempotency key so a retry after a lost answer lands
// on the session the first attempt made rather than beside it. The UI's New
// Chat names no session key, so without this the retry opens a second empty
// conversation.
func TestSessionCreateIsIdempotent(t *testing.T) {
	app := mount(t)
	me := who{org: "acme"}

	_, frame := ask(t, app, me, "1:a", "sessions.create", `{"idempotencyKey":"attempt-1"}`)
	first, _ := payload(t, frame)["key"].(string)
	_, frame = ask(t, app, me, "2:a", "sessions.create", `{"idempotencyKey":"attempt-1"}`)
	second, _ := payload(t, frame)["key"].(string)
	if first == "" || first != second {
		t.Fatalf("a retried create made %q and then %q", first, second)
	}

	// A different key is a different conversation, and so is a different
	// person: two people retrying the same client-minted key are two chats.
	_, frame = ask(t, app, me, "3:a", "sessions.create", `{"idempotencyKey":"attempt-2"}`)
	if other, _ := payload(t, frame)["key"].(string); other == first {
		t.Errorf("two different creates landed on one session %q", other)
	}
	_, frame = ask(t, app, who{org: "acme2"}, "4:a", "sessions.create", `{"idempotencyKey":"attempt-1"}`)
	if theirs, _ := payload(t, frame)["key"].(string); theirs == first {
		t.Errorf("another org's create landed on this org's session %q", theirs)
	}
}

// A first turn that could not start is reported beside the session, not
// instead of it. The session exists; telling the client the create failed sends
// it back to retry into the row this one already wrote.
func TestSessionCreateReportsAFailedFirstTurnBesideTheSession(t *testing.T) {
	app := mount(t) // no inference bound, so the turn cannot start
	me := who{org: "acme"}

	_, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"k2","message":"hello"}`)
	if frame["ok"] != true {
		t.Fatalf("the create was reported as failed: %v", frame)
	}
	out := payload(t, frame)
	if out["runStarted"] == true {
		t.Errorf("a run was reported started with no model bound: %v", out)
	}
	why, _ := out["runError"].(map[string]any)
	if why["code"] == nil {
		t.Fatalf("the create carries no run error for the client to show: %v", out)
	}
	// And the session is there, which is what the client shows the error against.
	if sessionRow(t, app, me, "k2") == nil {
		t.Error("the session the create answered for does not exist")
	}
}

// sessions.send hands the message to the chat family and keeps nothing of its
// own. A row written back after the relay would replace the whole document —
// the store's write is a replace, not a merge — and undo the run state the turn
// had just set.
func TestSessionSendLeavesTheRunStateItStarted(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	me := who{org: "acme"}

	if _, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"k3"}`); frame["ok"] != true {
		t.Fatalf("create: %v", frame)
	}
	_, frame := ask(t, app, me, "2:a", "sessions.send", `{"key":"k3","message":"take your time"}`)
	if frame["ok"] != true {
		t.Fatalf("sessions.send refused: %v", frame)
	}
	t.Cleanup(func() { _, _ = ask(t, app, me, "z", "sessions.abort", `{"key":"k3"}`) })

	row := sessionRow(t, app, me, "k3")
	if row["status"] != "running" {
		t.Errorf("the row reports status %v while a turn is running", row["status"])
	}
	if id, _ := row["lastRunId"].(string); id == "" {
		t.Errorf("the row carries no run id while a turn is running: %v", row)
	}
}

// The Stop button reaches sessions.abort whenever the tab holds no run id —
// after a reload mid-turn, or from a second tab. It has to stop the same work
// chat.abort stops, not only rewrite the row: a row that says killed over a
// goroutine still asking a model reports success and changes nothing.
func TestSessionAbortStopsTheLiveTurn(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	me := who{org: "acme"}

	aiSend(t, app, me, "k4", "run-a", "take your time")
	_, frame := ask(t, app, me, "1:a", "sessions.abort", `{"key":"k4"}`)
	if frame["ok"] != true {
		t.Fatalf("sessions.abort refused: %v", frame)
	}
	out := payload(t, frame)
	if out["status"] != "aborted" || out["abortedRunId"] != "run-a" {
		t.Fatalf("sessions.abort answered %v", out)
	}
	// The turn was cancelled, and it unwinds and leaves the registry. A row
	// rewritten over a goroutine still asking a model would leave it there
	// forever, which is the defect this test exists for.
	deadline := time.Now().Add(5 * time.Second)
	for chatFind("acme", "", "run-a", "k4") != nil {
		if time.Now().After(deadline) {
			t.Fatal("sessions.abort answered aborted and left the turn running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, frame = ask(t, app, me, "2:a", "sessions.abort", `{"key":"k4"}`)
	if payload(t, frame)["status"] != "no-active-run" {
		t.Errorf("a second abort answered %v", payload(t, frame))
	}
}

// The two aborts are one stop. Whichever a client reaches, the run ends and the
// row settles the same way.
func TestBothAbortsEndTheSameRun(t *testing.T) {
	for _, method := range []string{"chat.abort", "sessions.abort"} {
		t.Run(method, func(t *testing.T) {
			app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
			me := who{org: "acme"}
			aiSend(t, app, me, "k5", "run-b", "wait")

			key := `{"sessionKey":"k5"}`
			if method == "sessions.abort" {
				key = `{"key":"k5"}`
			}
			_, frame := ask(t, app, me, "1:a", method, key)
			if payload(t, frame)["status"] != "aborted" {
				t.Fatalf("%s answered %v", method, payload(t, frame))
			}
			settled := false
			for range 200 {
				if row := sessionRow(t, app, me, "k5"); row["status"] == "killed" {
					settled = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !settled {
				t.Errorf("%s stopped the run but the row never settled", method)
			}
		})
	}
}

// One session admits one turn, and the admission is the whole check-and-act.
// Two sends racing on one socket must not both find the session free: both
// would compute the same next position and one message would be written over
// the other, with nothing logged.
func TestConcurrentSendsCannotBothClaimASession(t *testing.T) {
	app := mount(t, aiServes(&aiFake{hold: true}, "flash"))
	me := who{org: "acme"}
	t.Cleanup(func() { _, _ = ask(t, app, me, "z", "chat.abort", `{"sessionKey":"race"}`) })

	var wg sync.WaitGroup
	accepted := make([]bool, 8)
	for i := range accepted {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run := string(rune('a'+i)) + "-run"
			_, frame := ask(t, app, me, "s:"+run, "chat.send",
				`{"sessionKey":"race","message":"m","idempotencyKey":"`+run+`"}`)
			accepted[i] = frame["ok"] == true
		}(i)
	}
	wg.Wait()

	live := 0
	for _, ok := range accepted {
		if ok {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("%d of %d concurrent sends were accepted on one session", live, len(accepted))
	}
	page := aiPage(t, app, me, `{"sessionKey":"race"}`)
	messages, _ := page["messages"].([]any)
	if len(messages) != 1 {
		t.Errorf("the transcript holds %d messages for one accepted send", len(messages))
	}
}

// ── one row, several writers ─────────────────────────────────────────────────

// A patch writes the whole row, so the loser of a race does not lose one field
// — it loses everything the winner did not also set, and it was told ok. The
// four session controls in the chat header patch the same key from four
// separate handlers with no queue between them
// (ui/src/pages/chat/chat-pane-session-controls.ts), and slash commands patch
// it from a fifth.
func TestConcurrentPatchesAllLand(t *testing.T) {
	app := mount(t)
	me := who{org: "acme", admin: true}
	if _, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"k"}`); frame["ok"] != true {
		t.Fatalf("create: %v", frame)
	}

	// One field each, and each field readable on its own afterwards.
	want := map[string]string{
		"label":         "L",
		"icon":          "I",
		"color":         "C",
		"category":      "G",
		"model":         "M",
		"contextWindow": "W",
		"statusNote":    "N",
		"execHost":      "H",
	}
	fields := make([]string, 0, len(want))
	params := make([]string, 0, len(want))
	for field, value := range want {
		fields = append(fields, field)
		params = append(params, `{"key":"k","`+field+`":"`+value+`"}`)
	}
	for i, frame := range atOnce(t, app, me, "sessions.patch", params) {
		if frame["ok"] != true {
			t.Fatalf("patching %s was refused: %v", fields[i], frame)
		}
	}

	row := sessionRow(t, app, me, "k")
	lost := []string{}
	for field, value := range want {
		if row[field] != value {
			lost = append(lost, field)
		}
	}
	sort.Strings(lost)
	if len(lost) > 0 {
		t.Errorf("%d of %d accepted patches are not on the row: %v — each was answered ok",
			len(lost), len(want), lost)
	}
}

// A precondition read outside the act that writes is not a precondition. Two
// patches may both read the row, both find the revision they were told to
// expect, and both be accepted — after which one silently overwrites the other
// having verified nothing.
//
// The revision moves on every write, so of several patches that all state the
// revision they read, exactly one can be right: the rest are acting on a row
// that has moved. Two acceptances mean the check ran against a row the write
// did not.
func TestARevisionPreconditionAdmitsOneWriter(t *testing.T) {
	app := mount(t)
	me := who{org: "acme"}
	fields := []string{"label", "icon", "color", "category", "model", "boardFace", "pinned", "archived"}

	for round := range 40 {
		key := fmt.Sprintf("k%d", round)
		if _, frame := ask(t, app, me, "1:a", "sessions.create", `{"key":"`+key+`"}`); frame["ok"] != true {
			t.Fatalf("create: %v", frame)
		}
		// The revision a caller states is the row's transcript identity and the
		// time it last moved, which is what the row carries and what the server
		// recomputes (sessionRevision).
		row := sessionRow(t, app, me, key)
		id, _ := row["sessionId"].(string)
		at, _ := row["updatedAt"].(float64)
		if id == "" || at == 0 {
			t.Fatalf("the row states nothing to act on: %v", row)
		}
		was := id + ":" + strconv.FormatInt(int64(at), 10)

		params := make([]string, len(fields))
		for i, field := range fields {
			params[i] = `{"key":"` + key + `","` + field + `":"v` + strconv.Itoa(i) + `","expectedLifecycleRevision":"` + was + `"}`
		}
		took := []string{}
		for i, frame := range atOnce(t, app, me, "sessions.patch", params) {
			if frame["ok"] == true {
				took = append(took, fields[i])
			}
		}
		if len(took) != 1 {
			t.Fatalf("%d patches stating revision %q were accepted (%v): a precondition that admits "+
				"two writers guards nothing — the second overwrites the first having checked a row it did not write",
				len(took), was, took)
		}
	}
}

// A create carrying an idempotency key is one conversation however many times
// it is retried. Two retries that both read no record of the first both mint a
// key, and the person is left with two.
func TestARetriedCreateMakesOneSession(t *testing.T) {
	app := mount(t)
	me := who{org: "acme"}

	const tries = 6
	params := make([]string, tries)
	for i := range params {
		params[i] = `{"idempotencyKey":"one-new-chat"}`
	}
	keys := map[string]bool{}
	for i, frame := range atOnce(t, app, me, "sessions.create", params) {
		if frame["ok"] != true {
			t.Fatalf("create %d refused: %v", i, frame)
		}
		key, _ := payload(t, frame)["key"].(string)
		if key == "" {
			t.Fatalf("create %d answered no key: %v", i, frame)
		}
		keys[key] = true
	}
	if len(keys) != 1 {
		t.Errorf("%d retries of one idempotent create made %d sessions: %v", tries, len(keys), keys)
	}
}

// The chat pane takes a lease on the session it shows, on every open. This
// surface narrows nothing by key — every connection on a bot hears that bot's
// sessions — so the lease succeeds, and says so. Refusing would claim the
// messages are not coming when they are, and the client renders that claim
// over the conversation.
func TestAChatPaneMayLeaseASession(t *testing.T) {
	ws := dial(t, serve(t), who{org: "acme"})

	frame := say(t, ws, "1", "connect", `{"minProtocol":4,"maxProtocol":4}`)
	if frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}
	for _, c := range []struct{ method, want string }{
		{"sessions.messages.subscribe", "true"},
		{"sessions.messages.unsubscribe", "false"},
	} {
		got := say(t, ws, "2", c.method, `{"key":"main","agentId":"main"}`)
		if got["ok"] != true {
			t.Fatalf("%s refused: %v", c.method, got)
		}
		out, _ := got["payload"].(map[string]any)
		if fmt.Sprint(out["subscribed"]) != c.want {
			t.Errorf("%s answered subscribed=%v, want %s", c.method, out["subscribed"], c.want)
		}
		if out["key"] != "main" {
			t.Errorf("%s answered key %v, want the key it was given", c.method, out["key"])
		}
	}
}
