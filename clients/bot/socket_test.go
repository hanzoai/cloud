package bot

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/hanzoai/cloud"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// The socket is the door the web UI uses, so these tests drive a real one: a
// listener on a real port, a real upgrade, real frames. The client is the
// WebSocket the server side already depends on (zip/wsx is built on it), used
// here only to dial.

// serve puts the mounted surface behind a real listener and returns its ws://
// address.
func serve(t *testing.T, shape ...func(*cloud.Deps)) string {
	t.Helper()
	app := mount(t, shape...)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Fiber().Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = app.Fiber().ShutdownWithTimeout(time.Second) })
	return "ws://" + ln.Addr().String() + "/v1/bot"
}

// announceAt registers one bot over the same listener, for the tests that hold
// a socket address rather than the app. A socket may bind to a bot only when
// the registry that owns bots has one.
func announceAt(t *testing.T, url, org, id string) {
	t.Helper()
	at := "http" + strings.TrimSuffix(strings.TrimPrefix(url, "ws"), "/bot") + "/bot"
	req, err := http.NewRequest(http.MethodPost, at, strings.NewReader(`{"id":"`+id+`","where":"cloud"}`))
	if err != nil {
		t.Fatalf("announce %s: %v", id, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u@"+org)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("announce %s: %v", id, err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("announce %s: %d %s", id, res.StatusCode, body)
	}
}

// dial opens the protocol socket as a validated caller.
func dial(t *testing.T, url string, w who) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	h.Set("X-Org-Id", w.org)
	h.Set("X-User-Id", "u@"+w.org)
	if w.admin {
		h.Set("X-User-IsOrgAdmin", "true")
	}
	ws, res, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = res.Body.Close()
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// say sends one request frame and returns the next frame the server writes.
func say(t *testing.T, ws *websocket.Conn, id, method, params string) map[string]any {
	t.Helper()
	frame := `{"type":"req","id":"` + id + `","method":"` + method + `"`
	if params != "" {
		frame += `,"params":` + params
	}
	frame += `}`
	if err := ws.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	return awaited(t, ws, id)
}

// awaited reads until the response to id arrives, passing over events. A server
// may say something unprompted at any moment — it opens with a challenge, and
// it ticks — so a client that treated the next frame as its reply would be
// reading whatever happened to arrive. Real clients correlate on the id, and so
// does this one.
func awaited(t *testing.T, ws *websocket.Conn, id string) map[string]any {
	t.Helper()
	for i := 0; i < 16; i++ {
		frame := next(t, ws)
		if frame["type"] == "event" {
			continue
		}
		if frame["id"] != id {
			t.Fatalf("answered %v, asked %q", frame["id"], id)
		}
		return frame
	}
	t.Fatalf("no answer to %q", id)
	return nil
}

// next reads one frame, with a deadline so a silent server fails the test
// instead of hanging it.
func next(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	kind, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != websocket.TextMessage {
		t.Fatalf("the server wrote a %d frame; the envelope is JSON text", kind)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("frame is not JSON: %v (%s)", err, data)
	}
	return out
}

// The handshake over the door the UI actually uses: connect, then a method,
// each answered on the id it was asked with.
func TestSocketRoundTripsTheEnvelope(t *testing.T) {
	ws := dial(t, serve(t), who{org: "acme"})

	frame := say(t, ws, "1:abc", "connect", `{"minProtocol":4,"maxProtocol":4}`)
	if frame["type"] != "res" || frame["id"] != "1:abc" || frame["ok"] != true {
		t.Fatalf("connect answered %v", frame)
	}
	hello, _ := frame["payload"].(map[string]any)
	if hello["type"] != "hello-ok" {
		t.Fatalf("handshake payload is %v", hello)
	}
	server, _ := hello["server"].(map[string]any)
	if id, _ := server["connId"].(string); id == "" {
		t.Errorf("the handshake names no connection: %v", server)
	}

	if frame = say(t, ws, "2:abc", "probe.write", `{"key":"k","value":"over the wire"}`); frame["ok"] != true {
		t.Fatalf("write: %v", frame)
	}
	frame = say(t, ws, "3:abc", "probe.read", `{"key":"k"}`)
	payload, _ := frame["payload"].(map[string]any)
	if payload["value"] != "over the wire" {
		t.Errorf("read back %v", payload)
	}
}

// An event reaches the connections of its org, numbered so a client can tell a
// dropped event from a quiet server.
func TestSocketCarriesEvents(t *testing.T) {
	url := serve(t)
	ws := dial(t, url, who{org: "acme"})
	if frame := say(t, ws, "1:a", "connect", ""); frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}

	Publish("acme", "", "", "probe.moved", map[string]any{"key": "main"})
	frame := next(t, ws)
	if frame["type"] != "event" || frame["event"] != "probe.moved" {
		t.Fatalf("published event arrived as %v", frame)
	}
	if frame["seq"] != float64(1) {
		t.Errorf("first event on this socket is seq %v, want 1", frame["seq"])
	}
	payload, _ := frame["payload"].(map[string]any)
	if payload["key"] != "main" {
		t.Errorf("event payload is %v", payload)
	}

	Publish("acme", "", "", "probe.moved", map[string]any{"key": "second"})
	if frame = next(t, ws); frame["seq"] != float64(2) {
		t.Errorf("second event is seq %v, want 2 — the client reads a skip as a dropped event", frame["seq"])
	}
}

// An event addressed to a key reaches only the connections that asked for it,
// which is the whole of what a subscription is.
func TestSocketDeliversOnlyWhatAConnectionAskedFor(t *testing.T) {
	url := serve(t)
	ws := dial(t, url, who{org: "acme"})
	if frame := say(t, ws, "1:a", "connect", ""); frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}

	Publish("acme", "", "session:main", "probe.moved", map[string]any{"key": "main"})
	Publish("acme", "", "", "probe.moved", map[string]any{"key": "everyone"})

	frame := next(t, ws)
	payload, _ := frame["payload"].(map[string]any)
	if payload["key"] != "everyone" {
		t.Errorf("a connection that watched nothing was sent %v", payload)
	}
}

// An org hears its own events and no others.
func TestSocketDoesNotCrossOrgs(t *testing.T) {
	url := serve(t)
	ws := dial(t, url, who{org: "acme"})
	if frame := say(t, ws, "1:a", "connect", ""); frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}

	Publish("other", "", "", "probe.moved", map[string]any{"key": "not yours"})
	Publish("acme", "", "", "probe.moved", map[string]any{"key": "yours"})

	frame := next(t, ws)
	payload, _ := frame["payload"].(map[string]any)
	if payload["key"] != "yours" {
		t.Errorf("an org was sent another's event: %v", payload)
	}
}

// The capability check is the same one whichever door the frame came through.
func TestSocketChecksTheSameScopes(t *testing.T) {
	ws := dial(t, serve(t), who{org: "acme"})
	if frame := say(t, ws, "1:a", "connect", ""); frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}
	frame := say(t, ws, "2:a", "probe.admin", "")
	if frame["ok"] != false {
		t.Fatalf("a member of the org ran an admin method over the socket: %v", frame)
	}
	e, _ := frame["error"].(map[string]any)
	if e["code"] != "FORBIDDEN" {
		t.Errorf("refusal code is %v, want FORBIDDEN", e["code"])
	}
}

// No socket is opened for a caller IAM has not validated: the refusal is an
// HTTP one, before the upgrade.
func TestSocketRefusesAnUnvalidatedCaller(t *testing.T) {
	h := http.Header{}
	h.Set("X-Org-Id", "acme") // an org, and nothing that validated it
	ws, res, err := websocket.DefaultDialer.Dial(serve(t), h)
	if err == nil {
		_ = ws.Close()
		t.Fatal("an unvalidated caller opened a socket")
	}
	if res == nil {
		t.Fatalf("dial failed without a response: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("the upgrade was refused with %d, want 403", res.StatusCode)
	}
}

// A connection ends with the reason it ended, not with silence.
func TestShutdownTellsTheConnectionsWhy(t *testing.T) {
	ws := dial(t, serve(t), who{org: "acme"})
	if frame := say(t, ws, "1:a", "connect", ""); frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}
	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	frame := next(t, ws)
	if frame["type"] != "event" || frame["event"] != "shutdown" {
		t.Fatalf("a closing gateway said %v", frame)
	}
	payload, _ := frame["payload"].(map[string]any)
	if reason, _ := payload["reason"].(string); reason == "" {
		t.Errorf("the notice carries no reason: %v", payload)
	}
}

// A binary frame is a different protocol on the same port; there is nothing to
// answer, so the socket ends.
func TestSocketRefusesABinaryFrame(t *testing.T) {
	ws := dial(t, serve(t), who{org: "acme"})
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte{0x00}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	// Frames the server had already sent — the opening challenge, a tick — are
	// still queued ahead of the close, so read past them. What is asserted is
	// that the socket ends, not that it ends on the very next frame.
	for i := 0; ; i++ {
		if _, _, err := ws.ReadMessage(); err != nil {
			break
		}
		if i == 8 {
			t.Error("a binary frame left the socket open")
			break
		}
	}
}

// zip's own error shape is what a door-level refusal looks like, so the
// compile-time check that bot returns one stays honest.
var _ error = (*zip.HTTPError)(nil)

// listen reads every frame a socket writes into a channel until the socket
// ends. A test that wants to know what a connection was sent reads it this way
// rather than timing a read out: a read deadline is terminal for this client,
// so a timed-out read leaves the socket unusable and every later read reports a
// silence that is the test's own doing.
//
// Once a socket is listened to, nothing else may read it — say and reply read
// the socket themselves.
func listen(ws *websocket.Conn) <-chan map[string]any {
	out := make(chan map[string]any, 64)
	go func() {
		defer close(out)
		for {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if kind != websocket.TextMessage {
				return
			}
			var frame map[string]any
			if json.Unmarshal(data, &frame) == nil {
				out <- frame
			}
		}
	}()
	return out
}

// until reads a listened socket up to the first event named last and returns
// the events that arrived before it. Delivery to one connection is ordered, so
// an event raised before last and not in what comes back was not sent to this
// connection — which is a fact rather than a wait long enough to be convincing.
func until(t *testing.T, in <-chan map[string]any, last string) []string {
	t.Helper()
	seen := []string{}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case frame, ok := <-in:
			if !ok {
				t.Fatalf("the socket ended before %s; it was sent %v", last, seen)
			}
			name, _ := frame["event"].(string)
			if name == "" {
				continue // a response, not an event
			}
			if name == last {
				return seen
			}
			seen = append(seen, name)
		case <-deadline:
			t.Fatalf("%s never arrived; the socket was sent %v", last, seen)
		}
	}
}

// The first frame is a challenge, before the client has said anything. The
// browser does not need it — it waits 750ms and connects either way — but the
// iOS client blocks on one for six seconds and the Android client for two, and
// closes the socket when none arrives. Nothing else here would notice its
// absence, which is why it is asserted.
func TestSocketOpensWithAChallenge(t *testing.T) {
	ws := dial(t, serve(t), who{org: "acme"})

	first := next(t, ws)
	if first["event"] != "connect.challenge" {
		t.Fatalf("the socket opened with %v; iOS and Android close a socket that is not challenged", first)
	}
	p, _ := first["payload"].(map[string]any)
	if nonce, _ := p["nonce"].(string); nonce == "" {
		t.Errorf("the challenge carries no nonce: %v", p)
	}
	if ts, _ := p["ts"].(float64); ts == 0 {
		t.Errorf("the challenge carries no ts: %v", p)
	}
}
