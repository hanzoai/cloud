// tap drives the protocol socket and records every frame, sent and received,
// as the JSON lines the conformance oracle reads:
//
//	{"kind":"req","method":"sessions.list","frame":{…}}
//	{"kind":"res","method":"sessions.list","frame":{…}}
//	{"kind":"event","event":"chat","frame":{…}}
//	{"kind":"hello","frame":{…}}
//
// The frame is copied through verbatim — the bytes the wire carried, never a
// re-encoding — so what the oracle judges is what the server actually said.
//
// The floor comes from the server's own hello: every method it advertises is
// called. A method whose parameters are not known here is called with {} and
// answers a refusal, which is itself a frame worth checking, since the error
// shape is part of the contract.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
)

// params a method needs beyond the empty object. Anything absent is called
// with {}.
var params = map[string]string{
	// The handshake the control UI sends: MIN_CLIENT_PROTOCOL_VERSION..PROTOCOL_VERSION
	// and CONTROL_UI_OPERATOR_SCOPES (ui/src/api/gateway.ts:102). A caller that
	// names a scope the server does not know gets the overlap, which is nothing,
	// so the six are spelled exactly.
	"connect": `{"minProtocol":4,"maxProtocol":4,"role":"operator",` +
		`"scopes":["operator.admin","operator.read","operator.write","operator.approvals","operator.questions","operator.pairing"],` +
		`"client":{"id":"openclaw-control-ui","version":"tap","platform":"cli","mode":"ui"}}`,
	"sessions.create":   `{"key":"main","idempotencyKey":"tap-1","label":"Main"}`,
	"sessions.resolve":  `{"key":"main"}`,
	"sessions.describe": `{"key":"main"}`,
	"sessions.reclaim":  `{"key":"main"}`,
	"chat.startup":      `{"sessionKey":"main"}`,
	"chat.history":      `{"sessionKey":"main"}`,
	"chat.metadata":     `{"sessionKey":"main"}`,
	"chat.send":         `{"sessionKey":"main","message":"hello from tap","idempotencyKey":"tap-run"}`,
	"chat.abort":        `{"sessionKey":"main","runId":"tap-run"}`,
	"board.get":         `{"sessionKey":"main"}`,
	"agents.update":     `{"agentId":"main","name":"Main"}`,
	"sessions.abort":    `{"key":"main"}`,
	"sessions.patch":    `{"key":"main","label":"Renamed"}`,
	"sessions.send":     `{"key":"main","message":"a second turn"}`,
	// Every parameter here is the one the method's own ParamsSchema declares,
	// so a refusal is the Go side reading a different field.
	"session.typing":         `{"sessionKey":"main","sessionId":"{{sessionId}}","typing":true}`,
	"session.visibility.set": `{"sessionKey":"main","visibility":"shared"}`,
	"sessions.groups.put":    `{"names":["tap-group"]}`,
	"sessions.groups.update": `{"name":"tap-group","cwd":null,"worktree":false}`,
	"chat.message.get":       `{"sessionKey":"main","messageId":"{{messageId}}"}`,
	"board.widget.put": `{"sessionKey":"main","name":"tap-widget","title":"Tap",` +
		`"content":{"kind":"html","html":"<p>tap</p>"}}`,
}

// after the turn: methods that need something the turn produced.
var after = []string{"chat.message.get", "sessions.send"}

// first runs before the floor: the session everything else names.
var first = []string{"sessions.create", "sessions.subscribe"}

// last runs after it: the turn, whose events are the point of the drain.
var last = []string{"chat.send"}

// skip keeps a method out of the middle of the walk, because it belongs to
// another phase: connect opens the drive, sessions.send starts a turn and runs
// after the first one has ended. Nothing is skipped for shape reasons — a
// refusal is a frame worth checking.
var skip = map[string]bool{"connect": true, "sessions.send": true}

type tap struct {
	ws   *websocket.Conn
	out  *os.File
	mu   sync.Mutex
	seq  int
	sent map[string]string      // request id -> method
	wait map[string]chan []byte // request id -> answer
}

func main() {
	url := flag.String("url", "ws://127.0.0.1:8899/v1/bot", "protocol socket")
	path := flag.String("frames", "frames.jsonl", "where to write the captured frames")
	drain := flag.Duration("drain", 8*time.Second, "how long to keep reading after the last request")
	flag.Parse()

	out, err := os.Create(*path)
	if err != nil {
		die(err)
	}
	defer out.Close() //nolint:errcheck

	ws, _, err := websocket.DefaultDialer.Dial(*url, nil)
	if err != nil {
		die(fmt.Errorf("dial %s: %w", *url, err))
	}
	defer ws.Close() //nolint:errcheck
	fmt.Println("dialed", *url)

	t := &tap{ws: ws, out: out, sent: map[string]string{}, wait: map[string]chan []byte{}}
	closed := make(chan struct{})
	go t.read(closed)

	hello := t.call("connect", params["connect"], 10*time.Second)
	if hello == nil {
		die(fmt.Errorf("no answer to connect"))
	}
	methods, events := t.hello(hello)
	fmt.Printf("advertised: %d methods, %d events\n", len(methods), len(events))

	for _, m := range plan(methods) {
		res := t.walk(m)
		if m == "sessions.create" {
			learn["sessionId"] = at(res, "payload", "sessionId")
		}
	}

	// Everything the server says on its own initiative after the turn.
	fmt.Println("draining", *drain)
	select {
	case <-time.After(*drain):
	case <-closed:
	}

	// A second turn, stopped while the model is still being asked. It is the
	// only way to see the aborted state on the wire: a run that has already
	// finished answers no-active-run and publishes nothing.
	t.call("chat.send", `{"sessionKey":"main","message":"stop me","idempotencyKey":"tap-abort"}`, 10*time.Second)
	if res := t.call("chat.abort", `{"sessionKey":"main","runId":"tap-abort"}`, 10*time.Second); res != nil {
		fmt.Printf("%-34s %s\n", "chat.abort (running turn)", verdict(res))
	}
	select {
	case <-time.After(3 * time.Second):
	case <-closed:
	}

	// The transcript after the turn, so the history payload is checked with
	// real messages in it rather than empty.
	learn["messageId"] = firstMessageID(t.walk("chat.history"))
	for _, m := range after {
		if has(methods, m) {
			t.walk(m)
		}
	}
	// The last turn's own events, so no run in the capture is left without the
	// frame that ends it.
	select {
	case <-time.After(3 * time.Second):
	case <-closed:
	}
	// A cursor this surface never minted: the protocol's answer is the
	// discontinuity, which is a different result shape from a page.
	t.call("chat.history", `{"sessionKey":"main","cursor":"tap-not-a-cursor"}`, 10*time.Second)
	fmt.Println("frames written to", *path)
}

// learn holds what one call taught the next: an id the server minted, which no
// table can hold ahead of time.
var learn = map[string]string{}

func fill(p string) string {
	for k, v := range learn {
		p = strings.ReplaceAll(p, "{{"+k+"}}", v)
	}
	return p
}

// walk calls one method with the parameters for it, and says how it answered.
func (t *tap) walk(m string) []byte {
	p, ok := params[m]
	if !ok {
		p = "{}"
	}
	res := t.call(m, fill(p), 10*time.Second)
	if res == nil {
		fmt.Printf("%-34s NO ANSWER\n", m)
		return nil
	}
	fmt.Printf("%-34s %s\n", m, verdict(res))
	return res
}

// at reads a string down a path of object keys, or "" if it is not there.
func at(frame []byte, path ...string) string {
	var v any
	if frame == nil || json.Unmarshal(frame, &v) != nil {
		return ""
	}
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[k]
	}
	s, _ := v.(string)
	return s
}

// firstMessageID reads the id of the first message of a transcript page, which
// is where the server states the identity a client asks for it by.
func firstMessageID(frame []byte) string {
	var r struct {
		Payload struct {
			Messages []struct {
				Mark struct {
					ID string `json:"id"`
				} `json:"__openclaw"`
			} `json:"messages"`
		} `json:"payload"`
	}
	if frame == nil || json.Unmarshal(frame, &r) != nil || len(r.Payload.Messages) == 0 {
		return ""
	}
	return r.Payload.Messages[0].Mark.ID
}

// plan orders the advertised floor: the session first, the turn last.
func plan(methods []string) []string {
	seen := map[string]bool{}
	for _, m := range append(append([]string{}, first...), last...) {
		seen[m] = true
	}
	middle := []string{}
	for _, m := range methods {
		if !seen[m] && !skip[m] {
			middle = append(middle, m)
		}
	}
	sort.Strings(middle)
	order := []string{}
	for _, m := range first {
		if has(methods, m) {
			order = append(order, m)
		}
	}
	order = append(order, middle...)
	for _, m := range last {
		if has(methods, m) {
			order = append(order, m)
		}
	}
	return order
}

func has(all []string, one string) bool {
	for _, a := range all {
		if a == one {
			return true
		}
	}
	return false
}

// call sends one request and waits for the answer with its id.
func (t *tap) call(method, p string, timeout time.Duration) []byte {
	t.mu.Lock()
	t.seq++
	id := fmt.Sprintf("%d:tap", t.seq)
	t.sent[id] = method
	answer := make(chan []byte, 1)
	t.wait[id] = answer
	t.mu.Unlock()

	frame := fmt.Sprintf(`{"type":"req","id":%q,"method":%q,"params":%s}`, id, method, p)
	t.emit("req", "method", method, json.RawMessage(frame))
	if err := t.ws.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		fmt.Fprintln(os.Stderr, "write", method, err)
		return nil
	}
	select {
	case data := <-answer:
		return data
	case <-time.After(timeout):
		return nil
	}
}

// read records every inbound frame and hands responses to their caller.
func (t *tap) read(closed chan struct{}) {
	defer close(closed)
	for {
		kind, data, err := t.ws.ReadMessage()
		if err != nil {
			return
		}
		if kind != websocket.TextMessage {
			t.emit("binary", "", "", json.RawMessage(`{}`))
			continue
		}
		var head struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Event string `json:"event"`
		}
		if json.Unmarshal(data, &head) != nil {
			// Not JSON at all: hand it to the oracle as an unknown kind rather
			// than dropping it.
			t.emit("unparsed", "", "", json.RawMessage(`{}`))
			continue
		}
		switch head.Type {
		case "res":
			t.mu.Lock()
			method := t.sent[head.ID]
			ch := t.wait[head.ID]
			delete(t.wait, head.ID)
			t.mu.Unlock()
			t.emit("res", "method", method, data)
			if method == "connect" {
				t.helloPayload(data)
			}
			if ch != nil {
				ch <- data
			}
		case "event":
			t.emit("event", "event", head.Event, data)
		default:
			t.emit(head.Type, "", "", data)
		}
	}
}

// helloPayload records the connect payload under its own kind: HelloOk is a
// payload schema, and the envelope check alone would not reach it.
func (t *tap) helloPayload(res []byte) {
	var r struct {
		OK      bool            `json:"ok"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(res, &r) != nil || !r.OK || len(r.Payload) == 0 {
		return
	}
	t.emit("hello", "", "", r.Payload)
}

func (t *tap) hello(res []byte) ([]string, []string) {
	var r struct {
		Payload struct {
			Features struct {
				Methods []string `json:"methods"`
				Events  []string `json:"events"`
			} `json:"features"`
		} `json:"payload"`
	}
	if json.Unmarshal(res, &r) != nil {
		return nil, nil
	}
	return r.Payload.Features.Methods, r.Payload.Features.Events
}

// emit writes one oracle line. The frame is the raw wire bytes.
func (t *tap) emit(kind, key, name string, frame json.RawMessage) {
	row := map[string]any{"kind": kind, "frame": frame}
	if key != "" && name != "" {
		row[key] = name
	}
	line, err := json.Marshal(row)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintln(t.out, string(line))
}

func verdict(res []byte) string {
	var r struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(res, &r) != nil {
		return "unreadable"
	}
	if r.OK {
		return "ok"
	}
	if r.Error != nil {
		return "refused " + r.Error.Code + ": " + r.Error.Message
	}
	return "refused"
}

func die(err error) { fmt.Fprintln(os.Stderr, "tap:", err); os.Exit(1) }
