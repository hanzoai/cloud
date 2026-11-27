package team

// events.go is the LIVE surface a native client holds open: one Server-Sent
// Events stream per caller, carrying what changes in the channels, documents and
// inbox that caller may see, as it happens.
//
// It is fed by PUSH and nothing else. Every write in this subsystem — the Team
// client's socket, the typed ops, an agent's answer, a projection from another
// app — ends in hub.broadcast, and the hub hands each broadcast to the stream's
// tap as it happens (transactor.go). There is no poll, no sweep and no timer
// that reads state: the only clock here is the heartbeat, a comment line that
// keeps proxies from closing an idle stream and is how a vanished client is
// noticed.
//
// There is no resume. The hub keeps no sequence of what it broadcast, so there
// is no Last-Event-ID to honour and none is sent; a client that reconnects
// re-reads the ops (channels, messages, inbox) for what it missed. Building a log
// only to replay it would be a second store of the same facts.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
)

// Event names, as the stream's `event:` lines carry them.
const (
	evMessageCreated = "message.created"
	evMessageUpdated = "message.updated"
	evMessageDeleted = "message.deleted"
	evReaction       = "reaction.changed"
	evChannel        = "channel.changed"
	evDoc            = "doc.changed"
	evInboxCreated   = "inbox.created"
	evInboxUpdated   = "inbox.updated"
)

// eventsHeartbeat is how often an idle stream writes a comment line. A failing
// write is how a vanished client is noticed, so it also bounds how long one
// lingers.
const eventsHeartbeat = 25 * time.Second

// eventsReadmit is how stale a space membership may be before the stream asks
// IAM again. It is asked ON TOUCH — only when there is something to deliver in
// that space — never on a timer, so a quiet stream costs IAM nothing and a
// person removed from a space stops receiving its events at the next one.
const eventsReadmit = time.Minute

func init() {
	spec.Register("/v1/team/events", http.MethodGet, nil, spec.Bytes{Type: "text/event-stream"})
	spec.Describe("/v1/team/events", http.MethodGet,
		"Stream live changes to what the caller may see, as Server-Sent Events",
		"Holds one text/event-stream open per caller and pushes each change as it is written — by the "+
			"Team client, a typed op or an agent — filtered to the channels, documents and inbox the caller may "+
			"see under the same rule the ops apply. Authenticate with `Authorization: Bearer` (a fetch-based "+
			"SSE reader; a browser EventSource cannot set it). `?space=` narrows the stream to one space; "+
			"without it the stream covers every space the caller is a member of at connect.\n\n"+
			"Each frame is `event: <name>` and a JSON `data:` line carrying `space` and one of:\n"+
			"- `message.created`, `message.updated` — `message`, the message as the message ops answer it "+
			"(a channel message, a thread reply or a document comment; a count or reaction change is an update);\n"+
			"- `message.deleted` — `id`, and `channel`/`doc`/`thread` where known;\n"+
			"- `reaction.changed` — `message`, with its reactions as they now stand;\n"+
			"- `channel.changed` — `channel`, as the channel listing answers it, sent to those who can see it after "+
			"the change (somebody removed from a private channel is not told; the channel list is the truth);\n"+
			"- `doc.changed` — `doc`, as the document ops answer it (`removed: true` and `id` when deleted); "+
			"body edits happen on the collaborator socket and are not streamed here;\n"+
			"- `inbox.created`, `inbox.updated` — `item`, one of the caller's own notifications.\n\n"+
			"A `: ping` comment is written every 25 seconds. There is no resume: the server keeps no event "+
			"sequence, so frames carry no `id` and Last-Event-ID is ignored; on reconnect, re-read the ops. "+
			"A stream that falls too far behind is closed rather than allowed to slow a writer, and so is one "+
			"whose caller is no longer a member of the space an event is in. 401 without a verified team "+
			"credential; 404 for a named space the caller is not in.")
}

// registerEvents declares the stream. UNTYPED, and it cannot be otherwise: the
// reply is a stream that outlives the handler (SendStreamWriter), while a typed
// op's only response path is one JSON value. untypedByDesign cites both.
func (b *channelBridge) registerEvents(app cloud.Router, guard guardFn) {
	g := app.Group(teamPrefix)
	g.Raw(http.MethodGet, "/events", guard(b.events))
}

// events authorizes the caller, taps the hub on their spaces, and streams.
func (b *channelBridge) events(c *zip.Ctx) error {
	cl, err := b.ident.who(c)
	if err != nil || cl.org == "" || cl.account == "" {
		return zip.ErrUnauthorized("team: a signed-in team member is required")
	}
	// Everything the stream needs is captured here: its callback runs after this
	// handler returns and must not touch c.
	ctx := context.WithoutCancel(c.Context())
	seats, err := seatsFor(ctx, b.ident, cl, c.Query("space"))
	if err != nil {
		return err
	}
	if len(seats) == 0 {
		return zip.ErrNotFound("space not found")
	}
	spaces := make([]string, 0, len(seats))
	for _, s := range seats {
		spaces = append(spaces, s.ws)
	}
	tp, stop, err := b.trans.hub.listen(cl.account, spaces)
	if err != nil {
		return zip.Errorf(http.StatusTooManyRequests, "%v", err)
	}
	auth := b.credential(c, cl)
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Accel-Buffering", "no")
	err = c.SendStreamWriter(func(w *bufio.Writer) {
		defer stop()
		b.pump(ctx, w, seats, tp, auth, pace{beat: eventsHeartbeat, readmit: eventsReadmit})
	})
	if err != nil {
		stop() // idempotent: a stream that never started leaves no tap behind
	}
	return err
}

// streamAuth is what keeps a stream honest about the credential it was opened
// with: when that credential expires, and how to ask again whether it still
// verifies and still resolves to the same person.
type streamAuth struct {
	// exp is the credential's own expiry; zero when it states none.
	exp time.Time
	// recheck re-verifies the credential the stream was opened with. Nil in a
	// test that drives the pump directly.
	recheck func(context.Context) error
}

// credential captures the ONE credential this request authenticated with, and the
// verifier it was admitted on (identity.lane), so the stream can re-verify it. The
// raw value never leaves this closure: it is not echoed, logged or stored.
func (b *channelBridge) credential(c *zip.Ctx, cl caller) streamAuth {
	raw, lane := b.ident.lane(c)
	return streamAuth{exp: cl.exp, recheck: func(ctx context.Context) error {
		again, err := lane(ctx, raw)
		if err != nil {
			return err
		}
		if again.account != cl.account || again.org != cl.org {
			return errNoSpace
		}
		return nil
	}}
}

// pump writes the stream until the tap closes (the hub cut it, or stop ran),
// the context ends, a write fails, the credential expires, or the caller is
// found — on touch — to be no longer signed in or no longer a member. It is the
// whole stream minus the HTTP, so a test drives it over a buffer.
// pace is how often a stream beats and how stale its admission may grow.
type pace struct{ beat, readmit time.Duration }

func (b *channelBridge) pump(ctx context.Context, w *bufio.Writer, seats []seat, tp *tap, auth streamAuth, p pace) {
	bySpace := make(map[string]seat, len(seats))
	admitted := make(map[string]time.Time, len(seats))
	now := time.Now()
	for _, s := range seats {
		bySpace[s.ws] = s
		admitted[s.ws] = now
	}
	checked := now
	if !frame(w, ": open\n\n") {
		return
	}
	hb := time.NewTicker(p.beat)
	defer hb.Stop()
	// The credential's own deadline. A timer to a fixed instant, not a poll:
	// when the token a stream was opened with stops being valid, the stream
	// stops with it, and the client reconnects with a fresh one.
	var expired <-chan time.Time
	if !auth.exp.IsZero() {
		t := time.NewTimer(time.Until(auth.exp))
		defer t.Stop()
		expired = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-expired:
			_ = frame(w, ": expired\n\n")
			return
		case <-hb.C:
			if !frame(w, ": ping\n\n") {
				return
			}
		case bt, open := <-tp.ch:
			if !open {
				return
			}
			s, ok := bySpace[bt.space]
			if !ok {
				continue
			}
			// ON TOUCH, at most once a minute and only when there is something to
			// deliver: is the credential still good, and what is the caller now in
			// this space. A revoked credential or a lost membership ends the
			// stream; a changed role (made a guest, made an admin) is what the
			// events that follow are filtered by.
			if auth.recheck != nil && time.Since(checked) >= p.readmit {
				if err := auth.recheck(ctx); err != nil {
					return
				}
				checked = time.Now()
			}
			if time.Since(admitted[bt.space]) >= p.readmit {
				_, role, err := b.ident.seat(ctx, s.caller, s.ws)
				if err != nil {
					return // no longer a member: the stream ends, and a reconnect is refused
				}
				s.role = role
				bySpace[bt.space] = s
				admitted[bt.space] = time.Now()
			}
			for _, ev := range b.project(s, bt) {
				if !emit(w, ev.name, ev.data) {
					return
				}
			}
		}
	}
}

// event is one frame to send.
type event struct {
	name string
	data map[string]any
}

// project turns one broadcast into the events this caller may see. Every
// event re-reads what it names from the store, so it carries the state AFTER
// the write — counts moved, reactions folded — and is gated by the same access
// rule the ops apply.
func (b *channelBridge) project(s seat, bt batch) []event {
	var out []event
	seen := map[string]bool{}
	// The reactions and files hanging off messages are read ONCE per broadcast,
	// by whichever stream on the space gets there first, and shared by the rest.
	lazy := func() extras {
		if bt.share == nil {
			return b.extras(s.org, s.ws)
		}
		bt.share.once.Do(func() { bt.share.x = b.extras(s.org, s.ws) })
		return bt.share.x
	}
	add := func(name, key string, data map[string]any) {
		if seen[name+"|"+key] {
			return // one event per thing per broadcast: the last state is the state
		}
		seen[name+"|"+key] = true
		data["space"] = s.ws
		out = append(out, event{name: name, data: data})
	}
	for _, raw := range bt.txes {
		var t map[string]any
		if json.Unmarshal(raw, &t) != nil {
			continue
		}
		kind, id, cls := str(t["_class"]), str(t["objectId"]), str(t["objectClass"])
		removed := kind == clTxRemove
		switch {
		case b.trans.hier.isDerived(cls, clChatMessage):
			if removed {
				if gone, ok := b.goneMessage(s, t); ok {
					add(evMessageDeleted, id, gone)
				}
				continue
			}
			msg, _, err := b.message(s, id)
			if err != nil {
				continue
			}
			name := evMessageUpdated
			if kind == clTxCreate {
				name = evMessageCreated
			}
			add(name, id, map[string]any{"message": messageOf(msg, lazy())})
		case cls == clReaction:
			on := str(t["attachedTo"])
			msg, _, err := b.message(s, on)
			if err != nil {
				continue
			}
			add(evReaction, on, map[string]any{"message": messageOf(msg, lazy())})
		case isChannel(cls):
			if removed {
				continue // a deleted channel names nothing a caller can still be shown to own
			}
			doc, _ := b.trans.store.get(s.org, s.ws, id)
			if doc != nil && s.canSee(doc) {
				add(evChannel, id, map[string]any{"channel": channelOf(s.ws, doc)})
			}
		case cls == clDocument:
			if removed {
				if _, err := b.teamspace(s, str(t["objectSpace"])); err == nil {
					add(evDoc, id, map[string]any{"id": id, "removed": true})
				}
				continue
			}
			if doc, err := b.doc(s, id); err == nil {
				add(evDoc, id, map[string]any{"doc": docOf(s.ws, doc)})
			}
		case b.trans.hier.isDerived(cls, clInboxNotification):
			if removed {
				continue
			}
			n, err := b.note(s, id)
			if err != nil {
				continue // somebody else's
			}
			name := evInboxUpdated
			if kind == clTxCreate {
				name = evInboxCreated
			}
			add(name, id, map[string]any{"item": b.itemOf(s, n, lazy())})
		}
	}
	return out
}

// goneMessage is the deleted-message event, when the caller could see where
// the message was. The message itself is gone, so where it lived is read off
// the remove's own coordinates: its `space` is the channel (or teamspace) it was
// in, and a reply names its parent.
func (b *channelBridge) goneMessage(s seat, t map[string]any) (map[string]any, bool) {
	home := str(t["objectSpace"])
	where, _ := b.trans.store.get(s.org, s.ws, home)
	if where == nil {
		return nil, false
	}
	data := map[string]any{"id": str(t["objectId"])}
	switch cls := str(where["_class"]); {
	case isChannel(cls):
		if !s.canSee(where) {
			return nil, false
		}
		data["channel"] = home
	case cls == clTeamspace:
		if !s.inTeamspace(where) {
			return nil, false
		}
		if str(t["attachedToClass"]) == clDocument {
			data["doc"] = str(t["attachedTo"])
		}
	default:
		return nil, false
	}
	if str(t["objectClass"]) == clThreadMessage {
		data["thread"] = str(t["attachedTo"])
	}
	return data, true
}

// emit writes one event frame and flushes it.
func emit(w *bufio.Writer, name string, data map[string]any) bool {
	body, err := json.Marshal(data)
	if err != nil {
		return true // a frame that cannot be encoded is skipped, not fatal
	}
	return frame(w, fmt.Sprintf("event: %s\ndata: %s\n\n", name, body))
}

// frame writes raw stream bytes and flushes; false means the client is gone.
func frame(w *bufio.Writer, s string) bool {
	if _, err := w.WriteString(s); err != nil {
		return false
	}
	return w.Flush() == nil
}
