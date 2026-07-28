// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bot

// ws.go — the socket half of the node control plane: nodes dial in here, and
// what they hold open is the rendezvous registry.go routes invocations down.
//
// # The wire, as nodes in the wild already speak it
//
// Every frame is one JSON text message. Protocol version 3. Read off bot's
// TypeScript gateway — protocol/schema/{frames,nodes}.ts for the shapes,
// server/ws-connection/message-handler.ts for the handshake, node-registry.ts
// for the invoke pair, node-host/{runner,invoke}.ts for what the node expects.
//
//	gw → node  {"type":"event","event":"connect.challenge","payload":{"nonce":…,"ts":…}}
//	node → gw  {"type":"req","id":…,"method":"connect","params":{minProtocol,maxProtocol,
//	            client:{id,displayName,version,platform,mode,instanceId,…},
//	            caps[],commands[],permissions{},pathEnv,role,scopes[],device{id,…},auth{…}}}
//	gw → node  {"type":"res","id":<same>,"ok":true,"payload":{"type":"hello-ok",protocol,
//	            server:{version,connId},features:{methods[],events[]},snapshot,policy:{…}}}
//	gw → node  {"type":"event","event":"node.invoke.request","payload":{id,nodeId,command,
//	            paramsJSON,timeoutMs,idempotencyKey}}
//	node → gw  {"type":"req","id":…,"method":"node.invoke.result","params":{id,nodeId,ok,
//	            payload|payloadJSON,error:{code,message}}}
//	gw → node  {"type":"event","event":"tick","payload":{"ts":…}}
//
// Two things about that wire are easy to get wrong and expensive to get wrong.
//
// The correlation id is payload.id going down and params.id coming back — NOT
// the frame's own id, which is the node's per-socket request counter for its
// own req/res pairs and correlates nothing here. So the registry's corrID is
// what goes in the payload.
//
// paramsJSON and payloadJSON are STRINGS holding JSON, not JSON values; the
// node parses them lazily and re-emits results the same way. A result may
// arrive either way, so both are accepted and yield the same bytes.
//
// # The socket library
//
// zip/wsx, because cloud already serves every other WebSocket route through it
// (clients/team/collabws.go, zapface/server.go). zip runs on fasthttp, whose
// hijack is what wsx wraps; a net/http-based library such as coder/websocket
// cannot upgrade a fasthttp connection at all, so this is not a preference
// between two working options. It is only used as the test CLIENT, where being
// a different implementation than the server is a feature.
//
// # The org is the gateway's verdict, never the node's claim
//
// ConnectParams carries a nodeId and no tenant — it never could, the node
// writes it. The org is the X-Org-Id the gateway injects after validating IAM
// (it strips any client copy first), read once at upgrade and fixed for the
// life of the socket. Nothing a node sends afterwards can change which tenant
// it belongs to, and a node that names another tenant's node id simply
// registers its own under its own org.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"
)

const (
	// protocolVersion is bot's PROTOCOL_VERSION. A node advertises the range it
	// can speak; the handshake fails when this falls outside it.
	protocolVersion = 3

	// maxFrameBytes caps one inbound frame — bot's MAX_PAYLOAD_BYTES, sized for
	// a high-resolution screen capture riding home in an invoke result.
	maxFrameBytes = 25 << 20

	// defaultHandshakeTimeout bounds the wait for the connect frame. A socket
	// that never identifies itself is not a node and must not hold a slot.
	defaultHandshakeTimeout = 10 * time.Second

	// tickInterval is advertised as policy.tickIntervalMs and is a contract,
	// not a nicety: a node closes the socket with 4000 "tick timeout" after 2x
	// this with no tick, so a gateway that stays silent gets reconnect storms.
	tickInterval = 30 * time.Second

	// idleTimeout drops a socket with no inbound traffic. An idle node sends
	// nothing on its own, so what keeps a healthy one alive is the pong its WS
	// stack answers our ping with automatically — hence comfortably > 2 pings.
	idleTimeout = 90 * time.Second

	// writeTimeout bounds one frame write so a wedged peer cannot pin a caller.
	writeTimeout = 10 * time.Second
)

// Frame, method and event names. One spelling each, in one place.
const (
	frameReq   = "req"
	frameRes   = "res"
	frameEvent = "event"

	methodConnect      = "connect"
	methodInvokeResult = "node.invoke.result"
	methodNodeEvent    = "node.event"

	eventChallenge     = "connect.challenge"
	eventTick          = "tick"
	eventInvokeRequest = "node.invoke.request"
)

// Error codes are bot's ErrorCodes; a node surfaces them verbatim.
const (
	codeInvalidRequest = "INVALID_REQUEST"
	codeUnavailable    = "UNAVAILABLE"
)

// nodeMethods and nodeEvents are what this endpoint actually serves, announced
// in hello-ok. A node feature-detects off them.
var (
	nodeMethods = []string{methodConnect, methodInvokeResult, methodNodeEvent}
	nodeEvents  = []string{eventChallenge, eventTick, eventInvokeRequest}
)

// ── frames ───────────────────────────────────────────────────────────────────

// reqFrame is the only frame kind a node sends: the handshake and everything
// after it are requests. Responses to our events are not part of the wire.
type reqFrame struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type eventFrame struct {
	Type    string `json:"type"`
	Event   string `json:"event"`
	Payload any    `json:"payload,omitempty"`
}

type resFrame struct {
	Type    string     `json:"type"`
	ID      string     `json:"id"`
	OK      bool       `json:"ok"`
	Payload any        `json:"payload,omitempty"`
	Error   *wireError `json:"error,omitempty"`
}

type wireError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func okRes(id string, payload any) resFrame {
	return resFrame{Type: frameRes, ID: id, OK: true, Payload: payload}
}

func errRes(id, code, msg string) resFrame {
	return resFrame{Type: frameRes, ID: id, OK: false, Error: &wireError{Code: code, Message: msg}}
}

// connectParams is the handshake. Fields cloud does not act on (auth, scopes,
// locale…) are deliberately absent: the gateway already decided identity, and
// carrying a field we never read invites someone to start trusting it.
type connectParams struct {
	MinProtocol int `json:"minProtocol"`
	MaxProtocol int `json:"maxProtocol"`
	Client      struct {
		ID           string `json:"id"`
		DisplayName  string `json:"displayName"`
		Version      string `json:"version"`
		Platform     string `json:"platform"`
		DeviceFamily string `json:"deviceFamily"`
		Mode         string `json:"mode"`
		InstanceID   string `json:"instanceId"`
	} `json:"client"`
	Caps        []string        `json:"caps"`
	Commands    []string        `json:"commands"`
	Permissions map[string]bool `json:"permissions"`
	Role        string          `json:"role"`
	Device      *struct {
		ID string `json:"id"`
	} `json:"device"`
}

// nodeID mirrors bot's resolution order exactly: the paired device id when the
// node has key material, else the instance id a cloud-provisioned node was
// handed, else the client name. Diverging here renames every node in a fleet.
func (p *connectParams) nodeID() string {
	if p.Device != nil {
		if id := strings.TrimSpace(p.Device.ID); id != "" {
			return id
		}
	}
	if id := strings.TrimSpace(p.Client.InstanceID); id != "" {
		return id
	}
	return strings.TrimSpace(p.Client.ID)
}

// invokeRequest is the node.invoke.request payload.
type invokeRequest struct {
	ID     string `json:"id"`
	NodeID string `json:"nodeId"`
	// ParamsJSON is JSON-as-a-string, and null (not absent) when there are no
	// params — the shape bot's coerceNodeInvokePayload reads.
	ParamsJSON     *string `json:"paramsJSON"`
	Command        string  `json:"command"`
	TimeoutMs      *int    `json:"timeoutMs,omitempty"`
	IdempotencyKey string  `json:"idempotencyKey,omitempty"`
}

// invokeResultParams is the node.invoke.result params.
type invokeResultParams struct {
	ID          string          `json:"id"`
	NodeID      string          `json:"nodeId"`
	OK          bool            `json:"ok"`
	Payload     json.RawMessage `json:"payload"`
	PayloadJSON *string         `json:"payloadJSON"`
	Error       *wireError      `json:"error"`
}

// result reduces a node's answer to what a caller can use. payloadJSON wins
// when both are present: a node that sent it chose the pre-serialized form, and
// the plain payload is then its unparsed twin.
func (p *invokeResultParams) result() InvokeResult {
	res := InvokeResult{OK: p.OK}
	switch {
	case p.PayloadJSON != nil && *p.PayloadJSON != "":
		res.Payload = []byte(*p.PayloadJSON)
	case len(p.Payload) > 0 && string(p.Payload) != "null":
		res.Payload = p.Payload
	}
	if p.Error != nil {
		res.Code, res.Message = p.Error.Code, p.Error.Message
	}
	return res
}

// nodeEventParams is the node.event params — a node's unsolicited push.
type nodeEventParams struct {
	Event       string          `json:"event"`
	Payload     json.RawMessage `json:"payload"`
	PayloadJSON *string         `json:"payloadJSON"`
}

// hello-ok, the handshake answer. snapshot and policy are part of the shape a
// node validates against; presence/health belong to bot's own gateway and are
// empty here, tickIntervalMs is what the node's watchdog arms itself with.
type helloOK struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
	Server   struct {
		Version string `json:"version"`
		ConnID  string `json:"connId"`
	} `json:"server"`
	Features struct {
		Methods []string `json:"methods"`
		Events  []string `json:"events"`
	} `json:"features"`
	Snapshot struct {
		Presence     []struct{} `json:"presence"`
		Health       any        `json:"health"`
		StateVersion struct {
			Presence int `json:"presence"`
			Health   int `json:"health"`
		} `json:"stateVersion"`
		UptimeMs int64 `json:"uptimeMs"`
	} `json:"snapshot"`
	Policy struct {
		MaxPayload       int   `json:"maxPayload"`
		MaxBufferedBytes int   `json:"maxBufferedBytes"`
		TickIntervalMs   int64 `json:"tickIntervalMs"`
	} `json:"policy"`
}

// ── transport ────────────────────────────────────────────────────────────────

// WSOptions configures the node transport. Every field is optional.
type WSOptions struct {
	// ServerVersion is reported to the node in hello-ok.
	ServerVersion string

	Logger luxlog.Logger

	// OnNodeEvent receives a node's unsolicited node.event pushes (idle
	// notices, proxy stream chunks). payload is raw JSON, already unwrapped
	// when the node sent it as payloadJSON. Nil drops them, which is the
	// correct default: an unread push is cheaper than a fabricated consumer.
	OnNodeEvent func(key NodeKey, event string, payload []byte)
}

type wsTransport struct {
	reg       *Registry
	opts      WSOptions
	startedAt time.Time

	// handshakeTimeout belongs to the transport rather than the package so it
	// can differ per instance without writing to state a live socket is reading.
	handshakeTimeout time.Duration
}

// NodeWS returns the handler nodes dial. Mount it on a GET route; reg is where
// the sessions it opens become addressable.
func NodeWS(reg *Registry, opts WSOptions) zip.Handler {
	t := &wsTransport{reg: reg, opts: opts, startedAt: time.Now(), handshakeTimeout: defaultHandshakeTimeout}
	if t.opts.ServerVersion == "" {
		t.opts.ServerVersion = "cloud"
	}
	return t.handle
}

func (t *wsTransport) handle(c *zip.Ctx) error {
	if t.reg == nil {
		return zip.ErrInternal("bot: node transport has no registry")
	}
	org := strings.TrimSpace(c.Org())
	if org == "" {
		return zip.ErrUnauthorized("bot: node connections require an authenticated org")
	}
	// A node is a daemon; a browser has no business on this route. Refusing
	// every request that carries an Origin closes WebSocket CSRF — no
	// same-origin policy applies to WS, so a page could otherwise ride a
	// viewer's session into registering a node — and it does so by removing the
	// category rather than by maintaining an allowlist of brand domains.
	if strings.TrimSpace(c.Header("Origin")) != "" {
		return zip.ErrForbidden("bot: node connections are not browser connections")
	}
	ip := c.Fiber().IP()
	return wsx.Upgrade(func(conn *wsx.Conn) error {
		t.serve(conn, org, ip)
		return nil
	})(c)
}

// serve runs one socket start to finish. It owns the session's whole lifetime:
// the deferred Unregister is the only one on this path and serve returns
// exactly once per socket, so teardown happens exactly once.
func (t *wsTransport) serve(conn *wsx.Conn, org, remoteIP string) {
	ctx := context.Background()
	connID := newID()
	w := &wsWriter{conn: conn}

	conn.SetReadLimit(maxFrameBytes)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(idleTimeout))
	})

	// Runs last. fasthttp returns this connection's write buffer to a shared
	// pool the moment serve returns, so a write still in flight then — from the
	// keepalive, or from an Invoke that read s.send just before Unregister —
	// would scribble on whichever socket got that buffer next. shut makes that
	// unrepresentable: once it returns, no goroutine is inside a write and none
	// can enter one.
	defer w.shut()

	var sess *Session
	defer func() {
		if sess != nil {
			t.reg.Unregister(connID)
			t.log().Debug("bot: node disconnected", "org", org, "node", sess.Key.NodeID, "conn", connID)
		}
	}()

	// The challenge is what prompts the node to send its connect frame — it
	// waits for one and closes on a timeout of its own if none arrives.
	if err := w.writeJSON(ctx, eventFrame{
		Type:    frameEvent,
		Event:   eventChallenge,
		Payload: map[string]any{"nonce": newID(), "ts": time.Now().UnixMilli()},
	}); err != nil {
		return
	}

	stop := make(chan struct{})
	defer close(stop)

	// The handshake deadline is one fixed instant, not a per-read timer. A
	// sliding one would let a peer hold a pre-handshake socket open forever by
	// sending frames this endpoint ignores, each refreshing its own deadline.
	handshakeBy := time.Now().Add(t.handshakeTimeout)
	for {
		readBy := time.Now().Add(idleTimeout)
		if sess == nil {
			readBy = handshakeBy
		}
		if err := conn.SetReadDeadline(readBy); err != nil {
			return
		}
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if typ != wsx.TextMessage {
			continue
		}
		var f reqFrame
		if json.Unmarshal(data, &f) != nil || f.Type != frameReq || f.ID == "" {
			continue // not a frame this endpoint answers
		}

		if sess != nil {
			t.dispatch(ctx, w, sess, f)
			continue
		}

		s, werr := t.connect(org, remoteIP, connID, f)
		if werr != nil {
			_ = w.writeJSON(ctx, resFrame{Type: frameRes, ID: f.ID, OK: false, Error: werr})
			t.log().Warn("bot: node handshake refused", "org", org, "conn", connID, "reason", werr.Message)
			return
		}
		s.send = w.write
		if err := t.reg.Register(s); err != nil {
			_ = w.writeJSON(ctx, errRes(f.ID, codeUnavailable, "node registration failed"))
			return
		}
		// From here the deferred Unregister owns teardown.
		sess = s
		if err := w.writeJSON(ctx, okRes(f.ID, t.helloOK(connID))); err != nil {
			return
		}
		t.log().Info("bot: node connected", "org", org, "node", s.Key.NodeID, "conn", connID,
			"platform", s.Platform, "version", s.Version, "commands", len(s.Commands))
		go t.keepalive(w, stop)
	}
}

// connect validates a handshake and shapes the session it describes. It is
// pure — no registration, no writes — so serve keeps every side effect, and
// therefore the teardown invariant, in one place.
func (t *wsTransport) connect(org, remoteIP, connID string, f reqFrame) (*Session, *wireError) {
	if f.Method != methodConnect {
		return nil, &wireError{Code: codeInvalidRequest, Message: "connect required"}
	}
	var p connectParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil, &wireError{Code: codeInvalidRequest, Message: "malformed connect params"}
	}
	if p.MaxProtocol < protocolVersion || p.MinProtocol > protocolVersion {
		return nil, &wireError{Code: codeInvalidRequest, Message: "protocol mismatch"}
	}
	// bot treats a roleless connect as an operator. This route serves nodes, so
	// anything else is a client on the wrong endpoint, not a node to register.
	if p.Role != "node" {
		return nil, &wireError{Code: codeInvalidRequest, Message: "this endpoint serves bot nodes"}
	}
	nodeID := p.nodeID()
	if nodeID == "" {
		return nil, &wireError{Code: codeInvalidRequest, Message: "connect names no node"}
	}
	return &Session{
		Key:         NodeKey{Org: org, NodeID: nodeID},
		ConnID:      connID,
		DisplayName: p.Client.DisplayName,
		Platform:    p.Client.Platform,
		Version:     p.Client.Version,
		Caps:        p.Caps,
		Commands:    p.Commands,
		Permissions: p.Permissions,
		RemoteIP:    remoteIP,
		ConnectedAt: time.Now(),
	}, nil
}

// dispatch handles one post-handshake request.
func (t *wsTransport) dispatch(ctx context.Context, w *wsWriter, sess *Session, f reqFrame) {
	switch f.Method {
	case methodInvokeResult:
		var p invokeResultParams
		if json.Unmarshal(f.Params, &p) != nil {
			_ = w.writeJSON(ctx, errRes(f.ID, codeInvalidRequest, "malformed invoke result"))
			return
		}
		// A node may only answer calls placed on ITS socket. The registry mints
		// corrIDs as "<connID>:<n>", so the prefix test makes that structural:
		// without it a node could resolve another node's — another tenant's —
		// in-flight invoke by naming its correlation id. One message covers both
		// checks, for the same reason ErrNoSuchNode covers a foreign node.
		if p.NodeID != sess.Key.NodeID || !strings.HasPrefix(p.ID, sess.ConnID+":") {
			_ = w.writeJSON(ctx, errRes(f.ID, codeInvalidRequest, "invoke result does not belong to this node"))
			return
		}
		// Answer drops a corrID nobody waits on, which is exactly right for a
		// result that arrives after its caller timed out: the node is told ok so
		// it stops retrying, and nothing is queued to surface on a later call.
		t.reg.Answer(p.ID, p.result())
		_ = w.writeJSON(ctx, okRes(f.ID, map[string]bool{"ok": true}))

	case methodNodeEvent:
		var p nodeEventParams
		if json.Unmarshal(f.Params, &p) != nil {
			_ = w.writeJSON(ctx, errRes(f.ID, codeInvalidRequest, "malformed node event"))
			return
		}
		if t.opts.OnNodeEvent != nil && p.Event != "" {
			payload := p.Payload
			if p.PayloadJSON != nil {
				payload = json.RawMessage(*p.PayloadJSON)
			}
			t.opts.OnNodeEvent(sess.Key, p.Event, payload)
		}
		_ = w.writeJSON(ctx, okRes(f.ID, map[string]bool{"ok": true}))

	default:
		_ = w.writeJSON(ctx, errRes(f.ID, codeUnavailable, "unknown method"))
	}
}

// keepalive sends the tick a node's watchdog waits for, and a ping whose
// automatic pong is what extends the read deadline of an otherwise silent node.
func (t *wsTransport) keepalive(w *wsWriter, stop <-chan struct{}) {
	tk := time.NewTicker(tickInterval)
	defer tk.Stop()
	ctx := context.Background()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			ev := eventFrame{Type: frameEvent, Event: eventTick, Payload: map[string]int64{"ts": time.Now().UnixMilli()}}
			if w.writeJSON(ctx, ev) != nil || w.ping() != nil {
				return
			}
		}
	}
}

func (t *wsTransport) helloOK(connID string) helloOK {
	var h helloOK
	h.Type = "hello-ok"
	h.Protocol = protocolVersion
	h.Server.Version = t.opts.ServerVersion
	h.Server.ConnID = connID
	h.Features.Methods = nodeMethods
	h.Features.Events = nodeEvents
	h.Snapshot.Presence = []struct{}{}
	h.Snapshot.UptimeMs = time.Since(t.startedAt).Milliseconds()
	h.Policy.MaxPayload = maxFrameBytes
	h.Policy.MaxBufferedBytes = 2 * maxFrameBytes
	h.Policy.TickIntervalMs = tickInterval.Milliseconds()
	return h
}

func (t *wsTransport) log() luxlog.Logger {
	if t.opts.Logger != nil {
		return t.opts.Logger
	}
	return luxlog.NewNoOpLogger()
}

// ── outbound framing ─────────────────────────────────────────────────────────

// InvokeFrame builds the node.invoke.request event for one call, ready to hand
// to Registry.Invoke — which mints the correlation id and passes it in.
//
// It takes a NodeKey rather than a bare id so a frame cannot be built without
// having named a tenant, even though only the node id goes on the wire: which
// org a node belongs to is the gateway's business, never the node's.
//
// params is the command's arguments as JSON, nil for none.
func InvokeFrame(key NodeKey, command string, params json.RawMessage, timeout time.Duration, idempotencyKey string) func(corrID string) ([]byte, error) {
	return func(corrID string) ([]byte, error) {
		req := invokeRequest{
			ID:             corrID,
			NodeID:         key.NodeID,
			Command:        command,
			IdempotencyKey: idempotencyKey,
		}
		if len(params) > 0 {
			s := string(params)
			req.ParamsJSON = &s
		}
		if timeout > 0 {
			ms := int(timeout / time.Millisecond)
			req.TimeoutMs = &ms
		}
		return json.Marshal(eventFrame{Type: frameEvent, Event: eventInvokeRequest, Payload: req})
	}
}

// errWSClosed is what a write attempted after the socket is done returns.
var errWSClosed = errors.New("bot: node socket closed")

// wsWriter serializes every write onto one socket: fasthttp/websocket permits a
// single concurrent writer, and the registry's send path, the read loop's
// replies and the keepalive all write.
type wsWriter struct {
	mu   sync.Mutex
	conn *wsx.Conn
	done bool
}

// shut retires the writer. Taking the same lock every write takes is what makes
// it a barrier: a write already inside it finishes first, and every later one
// is refused, so no write outlives the connection.
func (w *wsWriter) shut() {
	w.mu.Lock()
	w.done = true
	w.mu.Unlock()
}

// write puts one already-framed message on the wire. This is the Session's send
// func, so it honours the caller's context: an invoke whose caller has given up
// must not spend the full write timeout on a wedged socket.
func (w *wsWriter) write(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return errWSClosed
	}
	dl := time.Now().Add(writeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	if err := w.conn.SetWriteDeadline(dl); err != nil {
		return err
	}
	return w.conn.WriteMessage(wsx.TextMessage, b)
}

func (w *wsWriter) writeJSON(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.write(ctx, b)
}

func (w *wsWriter) ping() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return errWSClosed
	}
	return w.conn.WriteControl(wsx.PingMessage, nil, time.Now().Add(writeTimeout))
}

// newID mints a connection id and a challenge nonce. Fixed length matters: the
// registry sweeps pending invokes by connID prefix, so an id that could be a
// prefix of another would cancel calls belonging to a different socket.
func newID() string {
	var b [16]byte
	// crypto/rand.Read never returns an error and always fills b (Go 1.24+), so
	// there is no fallback to keep — and any fallback of a different length
	// would be the very hazard this comment is about.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
