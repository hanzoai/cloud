package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/zap-proto/zip"
)

// The transport is exercised over a real loopback socket rather than a fake
// conn: the thing under test IS the framing and the upgrade, and a stubbed
// connection would assert only that the stub was called. zip runs on fasthttp,
// which httptest.Server cannot serve, so the listener is the same
// net.Listen("127.0.0.1:0") + app.Fiber().Listener pattern cloud's other
// WebSocket tests use (zapface, git).

// startNodeWS mounts the node transport and returns the ws:// URL for it.
//
// The X-Org-Id the test client sends stands in for the gateway, which is the
// only thing that may set it: in production the gateway validates IAM and
// STRIPS any client copy before injecting its own. Here the test is the gateway.
func startNodeWS(t *testing.T, reg *Registry, opts WSOptions) string {
	t.Helper()
	return serveWS(t, NodeWS(reg, opts))
}

func serveWS(t *testing.T, h zip.Handler) string {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Get("/nodes/ws", h)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Fiber().Listener(ln) }()
	t.Cleanup(func() { _ = app.Fiber().Shutdown() })

	addr := ln.Addr().String()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "ws://" + addr + "/nodes/ws"
}

// node is a test double for a bot node: it speaks the same frames the real
// node-host does.
type node struct {
	t   *testing.T
	ctx context.Context
	c   *websocket.Conn
	seq int
}

func dialNode(t *testing.T, url, org string) *node {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	hdr := http.Header{}
	if org != "" {
		hdr.Set("X-Org-Id", org)
	}
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return &node{t: t, ctx: ctx, c: c}
}

func (n *node) read() map[string]any {
	n.t.Helper()
	_, data, err := n.c.Read(n.ctx)
	if err != nil {
		n.t.Fatalf("ws read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		n.t.Fatalf("ws read: not a frame: %v (%s)", err, data)
	}
	return m
}

// req sends one request frame and returns the response frame.
func (n *node) req(method string, params any) map[string]any {
	n.t.Helper()
	n.seq++
	id := fmt.Sprintf("r%d", n.seq)
	b, err := json.Marshal(map[string]any{"type": "req", "id": id, "method": method, "params": params})
	if err != nil {
		n.t.Fatalf("marshal: %v", err)
	}
	if err := n.c.Write(n.ctx, websocket.MessageText, b); err != nil {
		n.t.Fatalf("ws write: %v", err)
	}
	for {
		f := n.read()
		if f["type"] == "res" && f["id"] == id {
			return f
		}
	}
}

// handshake performs the real connect exchange: wait for the challenge, send
// connect, read hello-ok.
func (n *node) handshake(nodeID string) map[string]any {
	n.t.Helper()
	if ev := n.read(); ev["event"] != "connect.challenge" {
		n.t.Fatalf("first frame was %v, want connect.challenge", ev)
	}
	res := n.req("connect", map[string]any{
		"minProtocol": 3,
		"maxProtocol": 3,
		"role":        "node",
		"client": map[string]any{
			"id": "bot-node-host", "version": "1.2.3", "platform": "darwin",
			"mode": "node", "instanceId": nodeID,
		},
		"caps":        []string{"system", "vnc"},
		"commands":    []string{"system.run", "vnc.tunnel"},
		"permissions": map[string]bool{"system.run": true},
	})
	if res["ok"] != true {
		n.t.Fatalf("handshake refused: %v", res["error"])
	}
	payload, _ := res["payload"].(map[string]any)
	if payload["type"] != "hello-ok" {
		n.t.Fatalf("handshake payload was %v, want hello-ok", res["payload"])
	}
	return payload
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The handshake is what makes a node addressable, and it must land under the
// org the GATEWAY named — not one the node could claim, since ConnectParams has
// no field for it.
func TestHandshakeRegistersTheSession(t *testing.T) {
	reg := NewRegistry()
	n := dialNode(t, startNodeWS(t, reg, WSOptions{ServerVersion: "test"}), "acme")
	hello := n.handshake("laptop-1")

	if server, _ := hello["server"].(map[string]any); server["connId"] == "" {
		t.Fatal("hello-ok carries no connId")
	}
	waitFor(t, "registration", func() bool { return len(reg.List("acme")) == 1 })

	s := reg.List("acme")[0]
	if s.Key != (NodeKey{Org: "acme", NodeID: "laptop-1"}) {
		t.Fatalf("registered %v, want acme/laptop-1", s.Key)
	}
	if s.Platform != "darwin" || s.Version != "1.2.3" {
		t.Fatalf("handshake metadata lost: platform=%q version=%q", s.Platform, s.Version)
	}
	if len(s.Caps) != 2 || len(s.Commands) != 2 || !s.Permissions["system.run"] {
		t.Fatalf("caps/commands/permissions lost: %v %v %v", s.Caps, s.Commands, s.Permissions)
	}

	// The org came from the header, so no other tenant can see or reach it —
	// and the answer for a foreign node is the not-found answer.
	if got := len(reg.List("globex")); got != 0 {
		t.Fatalf("globex sees %d of acme's nodes", got)
	}
	if _, err := reg.Invoke(context.Background(), NodeKey{Org: "globex", NodeID: "laptop-1"}, frame, time.Second); err != ErrNoSuchNode {
		t.Fatalf("cross-org invoke returned %v, want ErrNoSuchNode", err)
	}
}

// The round trip the whole port exists to carry: an invoke reaches the socket,
// and the node's reply resolves the caller waiting on it.
func TestInvokeReachesTheSocketAndTheReplyResolvesIt(t *testing.T) {
	reg := NewRegistry()
	n := dialNode(t, startNodeWS(t, reg, WSOptions{}), "acme")
	n.handshake("laptop-1")
	waitFor(t, "registration", func() bool { return len(reg.List("acme")) == 1 })

	key := NodeKey{Org: "acme", NodeID: "laptop-1"}
	type outcome struct {
		res InvokeResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := reg.Invoke(context.Background(), key,
			InvokeFrame(key, "system.run", json.RawMessage(`{"cmd":"uname"}`), 5*time.Second, "idem-1"),
			5*time.Second)
		done <- outcome{res, err}
	}()

	// The node receives the invoke as an event carrying the correlation id.
	var ev map[string]any
	for {
		f := n.read()
		if f["event"] == "node.invoke.request" {
			ev = f
			break
		}
	}
	p, _ := ev["payload"].(map[string]any)
	corrID, _ := p["id"].(string)
	if corrID == "" {
		t.Fatalf("invoke frame has no correlation id: %v", p)
	}
	if p["command"] != "system.run" || p["nodeId"] != "laptop-1" {
		t.Fatalf("invoke frame lost its target: %v", p)
	}
	if p["paramsJSON"] != `{"cmd":"uname"}` {
		t.Fatalf("params ride as JSON-in-a-string; got %#v", p["paramsJSON"])
	}
	if p["idempotencyKey"] != "idem-1" || p["timeoutMs"] != float64(5000) {
		t.Fatalf("invoke frame lost idempotency/timeout: %v", p)
	}

	res := n.req("node.invoke.result", map[string]any{
		"id": corrID, "nodeId": "laptop-1", "ok": true,
		"payloadJSON": `{"stdout":"Darwin"}`,
	})
	if res["ok"] != true {
		t.Fatalf("gateway rejected a valid result: %v", res["error"])
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("invoke failed: %v", got.err)
		}
		if !got.res.OK || string(got.res.Payload) != `{"stdout":"Darwin"}` {
			t.Fatalf("reply did not resolve the invoke: %+v", got.res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the reply never resolved the waiting invoke")
	}
}

// A node may only answer calls placed on its own socket. Without this a node —
// and therefore another tenant — could resolve a foreign invoke by naming its
// correlation id.
func TestNodeCannotAnswerAnotherSocketsInvoke(t *testing.T) {
	reg := NewRegistry()
	url := startNodeWS(t, reg, WSOptions{})
	victim := dialNode(t, url, "acme")
	victim.handshake("laptop-1")
	waitFor(t, "victim registration", func() bool { return len(reg.List("acme")) == 1 })

	key := NodeKey{Org: "acme", NodeID: "laptop-1"}
	done := make(chan InvokeResult, 1)
	go func() {
		res, _ := reg.Invoke(context.Background(), key, InvokeFrame(key, "system.run", nil, time.Second, ""), time.Second)
		done <- res
	}()

	var corrID string
	for corrID == "" {
		f := victim.read()
		if f["event"] == "node.invoke.request" {
			p, _ := f["payload"].(map[string]any)
			corrID, _ = p["id"].(string)
		}
	}

	// A second node, in another org, tries to answer that exact call.
	attacker := dialNode(t, url, "globex")
	attacker.handshake("laptop-1")
	res := attacker.req("node.invoke.result", map[string]any{
		"id": corrID, "nodeId": "laptop-1", "ok": true, "payloadJSON": `{"stolen":true}`,
	})
	if res["ok"] != false {
		t.Fatal("a node answered a call belonging to another socket")
	}

	got := <-done
	if got.OK {
		t.Fatalf("the forged answer resolved the invoke: %+v", got)
	}
}

// Closing the socket must retire the session — exactly once, and without
// leaving a phantom node other callers would try to reach.
func TestCloseUnregisters(t *testing.T) {
	reg := NewRegistry()
	n := dialNode(t, startNodeWS(t, reg, WSOptions{}), "acme")
	n.handshake("laptop-1")
	waitFor(t, "registration", func() bool { return len(reg.List("acme")) == 1 })

	if err := n.c.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitFor(t, "unregistration", func() bool { return len(reg.List("acme")) == 0 })

	if _, err := reg.Invoke(context.Background(), NodeKey{Org: "acme", NodeID: "laptop-1"}, frame, time.Second); err != ErrNoSuchNode {
		t.Fatalf("a closed node is still addressable: %v", err)
	}
}

// The org is the gateway's, so a connection that arrives without one is not a
// node cloud can place anywhere. It is refused before the upgrade.
func TestUpgradeWithoutAnOrgIsRefused(t *testing.T) {
	url := startNodeWS(t, NewRegistry(), WSOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, res, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		_ = c.CloseNow()
		t.Fatal("an org-less connection was upgraded")
	}
	if res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("org-less upgrade answered %v, want 401", res)
	}
}

// A browser is not a node, and WebSockets have no same-origin policy — so a
// page riding a viewer's session must not be able to register one.
func TestBrowserOriginIsRefused(t *testing.T) {
	url := startNodeWS(t, NewRegistry(), WSOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Org-Id": {"acme"}, "Origin": {"https://evil.example"}},
	})
	if err == nil {
		_ = c.CloseNow()
		t.Fatal("a browser-origin connection was upgraded")
	}
	if res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("browser-origin upgrade answered %v, want 403", res)
	}
}

// A client on the wrong endpoint is not a node: bot treats a roleless connect
// as an operator, and registering one would put a viewer in the node fleet.
func TestNonNodeHandshakeIsRefused(t *testing.T) {
	reg := NewRegistry()
	n := dialNode(t, startNodeWS(t, reg, WSOptions{}), "acme")
	if ev := n.read(); ev["event"] != "connect.challenge" {
		t.Fatalf("first frame was %v, want connect.challenge", ev)
	}
	res := n.req("connect", map[string]any{
		"minProtocol": 3, "maxProtocol": 3,
		"client": map[string]any{"id": "viewer", "version": "1", "platform": "web", "mode": "backend"},
	})
	if res["ok"] != false {
		t.Fatal("an operator connect was registered as a node")
	}
	if len(reg.List("acme")) != 0 {
		t.Fatal("a refused handshake still registered a session")
	}
}

// A node that speaks a wire this gateway does not is told so, rather than
// registered and then misunderstood.
func TestProtocolMismatchIsRefused(t *testing.T) {
	reg := NewRegistry()
	n := dialNode(t, startNodeWS(t, reg, WSOptions{}), "acme")
	if ev := n.read(); ev["event"] != "connect.challenge" {
		t.Fatalf("first frame was %v, want connect.challenge", ev)
	}
	res := n.req("connect", map[string]any{
		"minProtocol": 4, "maxProtocol": 4, "role": "node",
		"client": map[string]any{"id": "n", "version": "1", "platform": "linux", "mode": "node", "instanceId": "n1"},
	})
	if res["ok"] != false {
		t.Fatal("a future protocol version was accepted")
	}
	if len(reg.List("acme")) != 0 {
		t.Fatal("a refused handshake still registered a session")
	}
}

// node.event is the node's unsolicited push; the hook is the seam other
// subsystems fan it out from.
func TestNodeEventReachesTheHook(t *testing.T) {
	reg := NewRegistry()
	type push struct {
		key     NodeKey
		event   string
		payload string
	}
	got := make(chan push, 1)
	url := startNodeWS(t, reg, WSOptions{OnNodeEvent: func(k NodeKey, e string, p []byte) {
		got <- push{k, e, string(p)}
	}})
	n := dialNode(t, url, "acme")
	n.handshake("laptop-1")

	res := n.req("node.event", map[string]any{"event": "idle", "payload": map[string]any{"seconds": 42}})
	if res["ok"] != true {
		t.Fatalf("node.event rejected: %v", res["error"])
	}
	select {
	case p := <-got:
		if p.key != (NodeKey{Org: "acme", NodeID: "laptop-1"}) || p.event != "idle" || p.payload != `{"seconds":42}` {
			t.Fatalf("hook received %+v", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("node.event never reached the hook")
	}
}

// The handshake deadline must be an instant, not a timer a peer can refresh.
// Traffic this endpoint ignores must not buy a socket more time to identify
// itself, or an unidentified connection holds a slot indefinitely.
func TestHandshakeDeadlineDoesNotSlide(t *testing.T) {
	// The deadline is shortened rather than waited out; the property under test
	// is that it is an instant, which does not depend on its length.
	tr := &wsTransport{
		reg:              NewRegistry(),
		opts:             WSOptions{ServerVersion: "test"},
		startedAt:        time.Now(),
		handshakeTimeout: 300 * time.Millisecond,
	}
	n := dialNode(t, serveWS(t, tr.handle), "acme")
	if ev := n.read(); ev["event"] != "connect.challenge" {
		t.Fatalf("first frame was %v, want connect.challenge", ev)
	}

	// Noise the endpoint ignores, sent faster than the deadline and without
	// waiting for replies there will never be any of. Under a sliding deadline
	// every frame buys another full interval and the socket never closes.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		b, _ := json.Marshal(map[string]any{"type": "event", "event": "noise"})
		for {
			select {
			case <-stop:
				return
			default:
			}
			if n.c.Write(n.ctx, websocket.MessageText, b) != nil {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
	}()

	closed := make(chan error, 1)
	go func() {
		_, _, err := n.c.Read(n.ctx)
		closed <- err
	}()
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("the endpoint answered a frame it should have ignored")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a socket that never identified itself was held open past the handshake deadline")
	}
}

// The frame builder is the contract the HTTP lane hands to Registry.Invoke.
func TestInvokeFrameShape(t *testing.T) {
	b, err := InvokeFrame(NodeKey{Org: "acme", NodeID: "n1"}, "system.run", nil, 0, "")("c1:7")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Type    string        `json:"type"`
		Event   string        `json:"event"`
		Payload invokeRequest `json:"payload"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Type != "event" || f.Event != "node.invoke.request" {
		t.Fatalf("invoke rides as %s/%s, want event/node.invoke.request", f.Type, f.Event)
	}
	if f.Payload.ID != "c1:7" || f.Payload.NodeID != "n1" || f.Payload.Command != "system.run" {
		t.Fatalf("payload lost its addressing: %+v", f.Payload)
	}
	// No params is null, not an empty string: that is the shape the node reads.
	if f.Payload.ParamsJSON != nil || f.Payload.TimeoutMs != nil {
		t.Fatalf("absent params/timeout were serialized: %+v", f.Payload)
	}
	// The org is cloud's business, never the node's — it must not be on the wire.
	if json.Valid(b) && contains(string(b), "acme") {
		t.Fatalf("the tenant leaked onto the wire: %s", b)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
