package claw

import (
	"slices"
	"testing"

	"github.com/fasthttp/websocket"
)

// A typing preview is the last few hundred characters of what an operator has
// not sent yet. The people entitled to see it are the people looking at the
// session it is being written into, which the protocol states as a rule of its
// own: session.typing is the one event upstream delivers only to a client that
// subscribed to the session (src/gateway/server-broadcast.ts:413).

// opened creates a session and returns the transcript identity a typing notice
// has to name.
func opened(t *testing.T, ws *websocket.Conn, key string) string {
	t.Helper()
	frame := reply(t, ws, "c:"+key, "sessions.create", `{"key":"`+key+`"}`)
	if frame["ok"] != true {
		t.Fatalf("sessions.create refused: %v", frame)
	}
	payload, _ := frame["payload"].(map[string]any)
	id, _ := payload["sessionId"].(string)
	if id == "" {
		t.Fatalf("the new session states no transcript: %v", payload)
	}
	return id
}

// A connection that never opened the session is not sent the draft.
func TestATypingDraftReachesOnlyTheSessionsWatchers(t *testing.T) {
	url := serve(t)
	writer := dialAs(t, url, "acme", "writer@acme", "", false)
	watcher := dialAs(t, url, "acme", "watcher@acme", "", false)
	lurker := dialAs(t, url, "acme", "lurker@acme", "", false)

	id := opened(t, writer, "deal")
	// Reading a board is how a client says it is watching a session; it is the
	// one thing on this surface that subscribes to a session key.
	if frame := reply(t, watcher, "b:deal", "board.get", `{"sessionKey":"deal"}`); frame["ok"] != true {
		t.Fatalf("board.get refused: %v", frame)
	}

	seen, quiet := listen(watcher), listen(lurker)
	frame := reply(t, writer, "t:1", "session.typing",
		`{"sessionKey":"deal","sessionId":"`+id+`","typing":true,"preview":"UNSENT DRAFT"}`)
	if frame["ok"] != true {
		t.Fatalf("session.typing refused: %v", frame)
	}
	if payload, _ := frame["payload"].(map[string]any); payload["broadcast"] != true {
		t.Fatalf("a session with a watcher answered %v", payload)
	}

	// A marker everyone in the partition hears, raised after the draft. What a
	// connection was sent before it has arrived by the time it does.
	Publish("acme", "", "", "probe.moved", map[string]any{"key": "marker"})
	if got := until(t, seen, "probe.moved"); !slices.Contains(got, "session.typing") {
		t.Errorf("the connection watching the session was not sent the draft: %v", got)
	}
	if got := until(t, quiet, "probe.moved"); slices.Contains(got, "session.typing") {
		t.Errorf("a connection that never opened this session was sent the draft: %v", got)
	}
}

// The count a typing notice answers with is the audience it published to. A
// board opened in another bot's partition is not that audience — it reads a
// different file and the draft never reaches it — so answering that the draft
// was broadcast is untrue, and it is an oracle for whether a session key is
// open somewhere else in the org.
func TestATypingNoticeDoesNotCountAnotherBotsWatcher(t *testing.T) {
	url := serve(t)
	announceAt(t, url, "acme", "bot_aaaaaaaa")
	announceAt(t, url, "acme", "bot_bbbbbbbb")
	writer := dialAs(t, url, "acme", "writer@acme", "bot_aaaaaaaa", false)
	elsewhere := dialAs(t, url, "acme", "other@acme", "bot_bbbbbbbb", false)

	id := opened(t, writer, "deal")
	if frame := reply(t, elsewhere, "b:deal", "board.get", `{"sessionKey":"deal"}`); frame["ok"] != true {
		t.Fatalf("board.get refused: %v", frame)
	}

	quiet := listen(elsewhere)
	frame := reply(t, writer, "t:1", "session.typing",
		`{"sessionKey":"deal","sessionId":"`+id+`","typing":true,"preview":"UNSENT DRAFT"}`)
	if frame["ok"] != true {
		t.Fatalf("session.typing refused: %v", frame)
	}
	if payload, _ := frame["payload"].(map[string]any); payload["broadcast"] != false {
		t.Errorf("the notice answered %v, and the only board open for that key is in another bot's partition, "+
			"which the draft never reaches", payload)
	}
	Publish("acme", "bot_bbbbbbbb", "", "probe.moved", map[string]any{"key": "marker"})
	if got := until(t, quiet, "probe.moved"); slices.Contains(got, "session.typing") {
		t.Errorf("a draft crossed into another bot's partition: %v", got)
	}
}
