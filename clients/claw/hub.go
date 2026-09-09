package claw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/zap-proto/zip/wsx"
)

// A connection is one open socket and everything the protocol tracks about it:
// who is on the other end, what they may do, how many events they have been
// sent, and which keys they asked to hear about.
//
// Exactly one goroutine writes to the socket — the pump below. Everything else
// hands it a frame. That is what makes an event publisher, a method answering
// a request, and the heartbeat safe to run at once.
type conn struct {
	id string
	// me is the identity the socket was opened under, carried onto every call
	// that arrives on it. The grant below is the narrowed one the handshake
	// settled; me.grant is what IAM granted before the client asked.
	me   caller
	out  chan any
	done chan struct{}
	// gone closes when the writer has finished. The socket is reclaimed the
	// moment the upgrade callback returns, and a write after that reclaim
	// dereferences a connection that is no longer there, so the callback waits
	// on this before returning.
	gone chan struct{}
	shut sync.Once
	// ctx lives as long as the socket. The upgrade request's context does not:
	// fasthttp recycles it the moment the route handler returns, which is
	// before the first frame is read.
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	grant Grant
	seq   uint64
	keys  map[string]bool
}

// buffered bounds what may be queued for one connection. A client that cannot
// drain its socket is gone; holding its backlog would let it consume the
// server instead of only itself.
const buffered = 256

func newConn(me caller) *conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &conn{
		id:     mint("conn"),
		me:     me,
		grant:  me.grant,
		out:    make(chan any, buffered),
		done:   make(chan struct{}),
		gone:   make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
		keys:   map[string]bool{},
	}
}

// mint makes an opaque id with the given prefix.
func mint(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// send queues a frame. It never blocks: a full buffer means the reader has
// stopped reading, and the honest response to that is to end the connection
// rather than to wait on it.
func (k *conn) send(msg any) {
	select {
	case k.out <- msg:
	case <-k.done:
	default:
		k.stop()
	}
}

// stop ends the connection and everything running on its behalf. Idempotent,
// and safe from any goroutine: it closes the signal channel, never the frame
// channel, so a concurrent send cannot write to a closed channel.
func (k *conn) stop() {
	k.shut.Do(func() {
		close(k.done)
		k.cancel()
	})
}

// watch adds or removes the keys this connection wants events for.
func (k *conn) watch(want bool, keys ...string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, key := range keys {
		if key == "" {
			continue
		}
		if want {
			k.keys[key] = true
		} else {
			delete(k.keys, key)
		}
	}
}

// watching reports whether this connection asked for key.
func (k *conn) watching(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys[key]
}

// hold narrows what the connection may do. The handshake calls it once, with
// the overlap of what IAM granted and what the client asked for.
func (k *conn) hold(g Grant) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.grant = g
}

func (k *conn) held() Grant {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.grant
}

// pump is the sole writer. Because it is the only one, the sequence counter
// needs no lock.
func (k *conn) pump(ws *wsx.Conn) {
	defer close(k.gone)
	// A close frame is how a client learns the difference between a gateway
	// that ended the session and a network that dropped it.
	defer func() { _ = ws.WriteMessage(wsx.CloseMessage, nil) }()
	for {
		select {
		case msg := <-k.out:
			if !k.write(ws, msg) {
				return
			}
		case <-k.done:
			// Write what is already queued before going. The last frame is
			// usually the notice that the connection is ending, and a client
			// that never hears it reconnects into a socket already gone.
			for {
				select {
				case msg := <-k.out:
					if !k.write(ws, msg) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// write stamps an event with this connection's next sequence — the number is
// per socket, and the client reports a gap the moment it skips — and sends the
// frame as one JSON text message. It reports whether the socket is still good.
func (k *conn) write(ws *wsx.Conn, msg any) bool {
	if ev, ok := msg.(*Event); ok {
		k.seq++
		stamped := *ev
		stamped.Seq = k.seq
		msg = &stamped
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return true // a frame that cannot be encoded is not a reason to drop the socket
	}
	if err := ws.WriteMessage(wsx.TextMessage, b); err != nil {
		k.stop()
		return false
	}
	return true
}

// beat says nothing, on time. The client closes the socket after twice the
// advertised interval of silence (ui/src/api/gateway.ts:550,
// maxInboundSilenceMs = tickIntervalMs * 2), so the heartbeat is part of the
// contract rather than a convenience.
func (k *conn) beat(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-k.done:
			return
		case now := <-t.C:
			k.send(&Event{Type: kindEvent, Event: "tick", Payload: tick{TS: now.UnixMilli()}})
		}
	}
}

type tick struct {
	TS int64 `json:"ts"`
}

// hub is every open connection, so an event raised anywhere in the process can
// reach the people watching for it.
type hub struct {
	mu    sync.Mutex
	conns map[string]*conn
}

func newHub() *hub { return &hub{conns: map[string]*conn{}} }

func (h *hub) add(k *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[k.id] = k
}

func (h *hub) drop(k *conn) {
	h.mu.Lock()
	delete(h.conns, k.id)
	h.mu.Unlock()
	k.stop()
}

// reach returns the connections of one org, snapshotted so the fan-out below
// never holds the hub lock while writing.
func (h *hub) reach(org string) []*conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*conn, 0, len(h.conns))
	for _, k := range h.conns {
		if k.me.org == org {
			out = append(out, k)
		}
	}
	return out
}

// closeAll tells every connection why it is ending and then ends it.
func (h *hub) closeAll(reason string) {
	h.mu.Lock()
	all := make([]*conn, 0, len(h.conns))
	for _, k := range h.conns {
		all = append(all, k)
	}
	h.conns = map[string]*conn{}
	h.mu.Unlock()
	for _, k := range all {
		k.send(&Event{Type: kindEvent, Event: "shutdown", Payload: ending{Reason: reason}})
		k.stop()
	}
}

type ending struct {
	Reason string `json:"reason"`
}

// Publish sends an event to one partition of an org. Two things address it, and
// they are different in kind.
//
// bot is the partition: state lives in one file per bot (Call.Store), so an
// event about that state belongs to the connections bound to the same bot and
// to no others. "" is the partition of connections bound to no bot, which is
// where the org's own file is read from. A connection never hears across the
// partition, subscribed or not. State an org keeps whatever bot a connection
// bound to is addressed with PublishOrg instead.
//
// key is a subscription: a non-empty key delivers only to the connections that
// asked for it with Call.Watch, and an empty key reaches every connection in
// the partition. Publish under a key only where a method establishes the
// subscription; an event nobody can subscribe to reaches nobody.
//
// It is a no-op before Mount and after Shutdown, so a background producer that
// outlives the surface cannot panic on it.
func Publish(org, bot, key, event string, payload any) {
	emit(org, key, event, payload, inBot(bot))
}

// PublishOrg addresses the org rather than a partition of it: every connection
// the org has open, whatever bot each one bound to. It is for the state a
// family keeps in the org's own file however the caller reached it — the device
// roster, a person's notification defaults — where a partition is the wrong
// audience, because every connection of the org reads that one file and a
// change to it is news to all of them.
func PublishOrg(org, key, event string, payload any) {
	emit(org, key, event, payload, func(*conn) bool { return true })
}

// watchers is who Publish would deliver to at this address. A method that
// counts its audience before it speaks reads it here, so the count it answers
// with and the delivery that follows cannot be two different readings of the
// hub.
func watchers(org, bot, key string) []*conn { return audience(org, key, inBot(bot)) }

func inBot(bot string) func(*conn) bool {
	return func(k *conn) bool { return k.me.bot == bot }
}

// audience is the connections an event so addressed reaches: the org's, narrowed
// to those the address admits, and then to those that asked for the key.
func audience(org, key string, admits func(*conn) bool) []*conn {
	s := mounted.Load()
	if s == nil || org == "" {
		return nil
	}
	out := []*conn{}
	for _, k := range s.State.hub.reach(org) {
		if !admits(k) {
			continue
		}
		if key != "" && !k.watching(key) {
			continue
		}
		out = append(out, k)
	}
	return out
}

func emit(org, key, event string, payload any, admits func(*conn) bool) {
	if event == "" {
		return
	}
	for _, k := range audience(org, key, admits) {
		k.send(&Event{Type: kindEvent, Event: event, Payload: payload})
	}
}
