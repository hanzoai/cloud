package bot

import (
	"encoding/json"
	"time"
)

// The handshake is a method, not a frame of its own. On open the client sends
// a request naming "connect" and waits for a response whose payload is
// HelloOk (packages/gateway-client/src/protocol-client.ts:341):
//
//	void this.request<HelloOk>("connect", this.opts.buildConnectParams(plan))
//	  .then((hello) => { this.helloReceived = true; ... })
//
// so it needs no capability — a caller cannot hold one before being told which
// it has — but it does need a validated identity, which the door checked
// before this method was reached.
//
// The client waits 750ms for a `connect.challenge` event before sending
// connect (ui/src/api/gateway.ts:319, handshake mode "fallback"). This surface
// sends no challenge. A challenge exists so a device can sign a server nonce,
// and this surface verifies no device signature: identity is IAM's answer, and
// a nonce nobody checks is a ritual. The cost is that first pause, once per
// connection.
//
// The `device` and `auth` blocks of ConnectParams are read and ignored for the
// same reason.

func init() {
	Register("connect", Open, connect)
	Declare("tick", "shutdown")
}

// connectParams is the part of ConnectParams
// (packages/gateway-protocol/src/schema/frames.ts) this surface acts on. The
// rest is a client describing itself, and is not decoded strictly because the
// client adds fields to that description faster than a server needs to know
// about them.
type connectParams struct {
	MinProtocol int      `json:"minProtocol"`
	MaxProtocol int      `json:"maxProtocol"`
	Scopes      []string `json:"scopes"`
	Client      struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"client"`
}

func connect(c *Call) (any, error) {
	var p connectParams
	if len(c.params) > 0 {
		if err := json.Unmarshal(c.params, &p); err != nil {
			return nil, Invalid("connect params: %v", err)
		}
	}
	if p.MaxProtocol > 0 && (protocol < p.MinProtocol || protocol > p.MaxProtocol) {
		return nil, Invalid("this gateway speaks protocol %d; the client accepts %d..%d",
			protocol, p.MinProtocol, p.MaxProtocol)
	}

	// A client asks for capabilities; IAM decides which of them it has. The
	// answer is the overlap, never a refusal: the UI asks for all six on every
	// connect, and a member who is not an admin of the org must still work.
	held := c.me.grant.narrow(p.Scopes)
	connID := "http"
	if c.conn != nil {
		c.conn.hold(held)
		connID = c.conn.id
	}

	s := c.svc
	return hello{
		Type:     "hello-ok",
		Protocol: protocol,
		Server:   server{Version: s.State.version, ConnID: connID},
		Features: features{Methods: methodNames(), Events: eventNames()},
		Snapshot: snapshot{
			Presence:     []any{},
			Health:       map[string]any{},
			StateVersion: versions{},
			UptimeMs:     time.Since(s.State.started).Milliseconds(),
			AuthMode:     authMode,
		},
		Auth:   auth{Method: authMode, Role: role, Scopes: held.strings()},
		Policy: policy{MaxPayload: maxFrame, MaxBuffered: maxFrame * 2, TickMs: int(tickEvery.Milliseconds())},
	}, nil
}

// authMode is what the client is told about how it was authenticated. The
// identity headers this cloud reads are minted by the gateway from a validated
// IAM token, which is exactly the protocol's "trusted-proxy".
const authMode = "trusted-proxy"

// role is the one role this surface issues. The protocol's other roles belong
// to node and runtime clients, which reach this cloud through their own
// surfaces rather than through the operator protocol.
const role = "operator"

// hello is HelloOkSchema. The schema is closed, so these are all the fields
// there are to send and none may be added.
type hello struct {
	Type     string   `json:"type"`
	Protocol int      `json:"protocol"`
	Server   server   `json:"server"`
	Features features `json:"features"`
	Snapshot snapshot `json:"snapshot"`
	Auth     auth     `json:"auth"`
	Policy   policy   `json:"policy"`
}

type server struct {
	Version string `json:"version"`
	ConnID  string `json:"connId"`
}

// features is what a client checks before offering a control: it lists what
// this surface answers, so a UI built against a fuller gateway hides what is
// not here rather than calling it and failing.
type features struct {
	Methods []string `json:"methods"`
	Events  []string `json:"events"`
}

type snapshot struct {
	Presence     []any          `json:"presence"`
	Health       map[string]any `json:"health"`
	StateVersion versions       `json:"stateVersion"`
	UptimeMs     int64          `json:"uptimeMs"`
	AuthMode     string         `json:"authMode"`
}

type versions struct {
	Presence int `json:"presence"`
	Health   int `json:"health"`
}

type auth struct {
	Method string   `json:"method"`
	Role   string   `json:"role"`
	Scopes []string `json:"scopes"`
}

// policy is the frame budget and the heartbeat interval. The client derives
// its own silence timeout from TickMs, so the number here and conn.beat's
// interval are one value.
type policy struct {
	MaxPayload  int `json:"maxPayload"`
	MaxBuffered int `json:"maxBufferedBytes"`
	TickMs      int `json:"tickIntervalMs"`
}
