package claw

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// Func answers one protocol method. What it returns becomes the response
// frame's payload; what it fails with becomes the frame's error shape.
type Func func(*Call) (any, error)

// entry is a method and the capability it costs.
type entry struct {
	need Scope
	fn   Func
}

// The surface. A method is added at load and read on every request; the lock
// is there for the case where those overlap — a family registered after the
// surface is already serving.
var surface = struct {
	mu      sync.RWMutex
	methods map[string]entry
	events  map[string]bool
}{
	methods: map[string]entry{},
	events:  map[string]bool{},
}

// Register adds one method to the protocol surface. Call it from an init in
// the file that owns the method:
//
//	func init() { Register("sessions.list", Read, listSessions) }
//
// A duplicate name, an empty name, a nil function or a scope outside the
// closed set is a mistake in the program, so it panics at load rather than
// serving a surface nobody meant.
func Register(name string, need Scope, fn Func) {
	if name == "" {
		panic("claw.Register: a method needs a name")
	}
	if fn == nil {
		panic("claw.Register: " + name + " has no function")
	}
	if need != Open && !scopes[need] {
		panic("claw.Register: " + name + " asks for unknown scope " + string(need))
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if _, dup := surface.methods[name]; dup {
		panic("claw.Register: duplicate method " + name)
	}
	surface.methods[name] = entry{need: need, fn: fn}
}

// Announce declares an event this surface may emit, so the handshake can list
// it. Declaring is separate from emitting because a client chooses what to
// listen for from the list alone, before anything has happened.
func Announce(events ...string) {
	surface.mu.Lock()
	defer surface.mu.Unlock()
	for _, e := range events {
		if e != "" {
			surface.events[e] = true
		}
	}
}

// methodNames and eventNames are what the handshake advertises. They are read
// per handshake rather than snapshotted at mount, so a family that registers
// late is announced on the next connect instead of being dispatchable but
// invisible.
func methodNames() []string {
	surface.mu.RLock()
	defer surface.mu.RUnlock()
	return slices.Sorted(maps.Keys(surface.methods))
}

func eventNames() []string {
	surface.mu.RLock()
	defer surface.mu.RUnlock()
	return slices.Sorted(maps.Keys(surface.events))
}

// Call is one method invocation: the caller, what it asked for, and the
// handles it needs to answer.
type Call struct {
	// Method is the name that was asked for.
	Method string

	svc    *cloud.Service[state]
	conn   *conn // nil on the single-frame HTTP door
	ctx    context.Context
	params json.RawMessage
	me     caller
}

// Org is the tenant this call acts in — the validated IAM owner, never a
// client-supplied value. It is also the file every read and write lands in.
func (c *Call) Org() string { return c.me.org }

// User is the validated principal that made the call.
func (c *Call) User() string { return c.me.user }

// Billing is the org whose ledger pays for the call, which is the caller's own
// home org rather than the org being acted on. See caller.
func (c *Call) Billing() string { return c.me.bill }

// Project is the sub-scope within the org that the call narrows to.
func (c *Call) Project() string { return c.me.project }

// Bot names the bot this call is bound to, or "" for the org itself.
func (c *Call) Bot() string { return c.me.bot }

// Context carries the request deadline and cancellation.
func (c *Call) Context() context.Context { return c.ctx }

// Log is the subsystem logger, already scoped.
func (c *Call) Log() luxlog.Logger { return c.svc.Log }

// Allows reports whether the caller holds a capability. The dispatcher has
// already checked the one the method declared; this is for a method whose
// answer widens or narrows with what the caller may do.
func (c *Call) Allows(need Scope) bool { return c.me.grant.Allows(need) }

// Params is the raw parameter JSON. Use Bind unless the method's parameters
// are declared open — the protocol closes almost all of them, and the two that
// do not (wake, and the session row projection) say so.
func (c *Call) Params() []byte { return c.params }

// Bind decodes the parameters into v and refuses a field v does not declare.
// That is the Go reading of closedObject, which the protocol writes as
// additionalProperties: false on nearly every parameter schema — a caller that
// sends a field the server does not know has misunderstood the method, and
// answering it anyway is how a typo becomes silent data loss. Absent
// parameters leave v untouched, so a method whose fields are all optional
// needs no special case.
func (c *Call) Bind(v any) error {
	if len(c.params) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(c.params))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return Invalid("params: %v", err)
	}
	return nil
}

// Store opens the SQLite this call's state lives in: the bot's own file when
// the call is bound to one, the org's otherwise.
func (c *Call) Store() (*Store, error) {
	st, err := c.svc.State.stores.For(c.me.org, c.me.bot)
	if err != nil {
		c.svc.Log.Error("open claw store", "org", c.me.org, "bot", c.me.bot, "err", err)
		return nil, Unavailable("the store could not be opened")
	}
	return st, nil
}

// Emit sends an event to the caller that made this call and nobody else. It is
// how a method reports progress on work it was asked to do. On the
// single-frame door there is nowhere to send it, and it is dropped.
func (c *Call) Emit(event string, payload any) {
	if c.conn == nil {
		return
	}
	c.conn.send(&Event{Type: kindEvent, Event: event, Payload: payload})
}

// Watch records that this connection wants the events published under key —
// a session key, a run id, whatever the family addresses its stream by. It is
// how a subscription is expressed: there is no subscribe frame, only an event
// stream the connection asked to be included in.
func (c *Call) Watch(keys ...string) {
	if c.conn != nil {
		c.conn.watch(true, keys...)
	}
}

// Unwatch undoes Watch.
func (c *Call) Unwatch(keys ...string) {
	if c.conn != nil {
		c.conn.watch(false, keys...)
	}
}

// dispatch resolves the method, checks the capability it costs, and runs it.
// It is the only path from a frame to a method, so the scope check cannot be
// skipped by arriving through a different door.
func dispatch(c *Call) (any, error) {
	surface.mu.RLock()
	m, ok := surface.methods[c.Method]
	surface.mu.RUnlock()
	if !ok {
		return nil, Invalid("unknown method: %s", c.Method)
	}
	if !c.me.grant.Allows(m.need) {
		return nil, MissingScope(m.need)
	}
	return m.fn(c)
}

// relay runs another registered method as the same caller, on the same
// connection, and hands back exactly what it answered. It is how a method whose
// substance belongs to another family reaches it: the registry stays the only
// path from a name to a function, so the capability the target declared is
// still checked and its error shape is still the one the client reads.
//
// What comes back is the target's own Go value, not JSON — nothing marshals a
// payload until the frame is written. Hand it straight to the client, or read
// it with relayInto; a type assertion against map[string]any holds for one
// method and silently fails for the next one that answers with a struct.
func relay(c *Call, method string, params any) (any, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return nil, Unavailable("the call could not be built")
	}
	return dispatch(&Call{
		Method: method,
		svc:    c.svc,
		conn:   c.conn,
		ctx:    c.ctx,
		params: b,
		me:     c.me,
	})
}

// relayInto relays a call and reads its answer into v. The round trip through
// JSON is what makes the read hold whatever shape the target chose: the answer
// travels to a client as JSON either way, so decoding it as JSON here is
// reading the same value the client would read.
func relayInto(c *Call, method string, params, v any) error {
	out, err := relay(c, method, params)
	if err != nil {
		return err
	}
	b, err := json.Marshal(out)
	if err != nil {
		c.svc.Log.Error("claw: encode relayed answer", "method", method, "err", err)
		return Unavailable("%s answered something that could not be read", method)
	}
	if err := json.Unmarshal(b, v); err != nil {
		c.svc.Log.Error("claw: decode relayed answer", "method", method, "err", err)
		return Unavailable("%s answered a shape this method does not know", method)
	}
	return nil
}

// known reports whether a method is on the surface. A method that composes
// another family asks first, so it can say that the thing it composes is absent
// rather than report an unknown method the caller never named.
func known(method string) bool {
	surface.mu.RLock()
	defer surface.mu.RUnlock()
	_, ok := surface.methods[method]
	return ok
}

// answer runs one request frame to its response frame. Every failure a caller
// can see comes back here as ok:false with a code, never as a transport error:
// the envelope carries the outcome of the method, and the transport carries
// only whether the method was reached.
func answer(c *Call, id string) *Response {
	payload, err := dispatch(c)
	if err != nil {
		f := fault(err)
		if f.Code == codeUnavail {
			c.svc.Log.Error("claw method failed", "method", c.Method, "org", c.me.org, "err", err)
		}
		return &Response{Type: kindResponse, ID: id, OK: false, Error: f}
	}
	return &Response{Type: kindResponse, ID: id, OK: true, Payload: payload}
}
