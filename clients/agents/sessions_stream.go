package agents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

// streamUpdate is one live update fanned out to subscribers: either a session
// lifecycle change (register / status / control) or an appended event. Org and
// RootID are carried so the bus can filter by tenant AND a subscriber can scope
// to a single subagent tree (?root=). Exactly one of Session/Event is set.
type streamUpdate struct {
	Org     string       `json:"-"`
	RootID  string       `json:"-"`
	Type    string       `json:"-"` // "session" | "event"
	Session *sessionView `json:"session,omitempty"`
	Event   *eventView   `json:"event,omitempty"`
}

// bus is the in-process publish/subscribe fan-out under the sessions surface. It
// is the SINGLE seam the live stream hangs off:
//
//   - Today: the SSE handler (GET /v1/agents/sessions/stream) subscribes and
//     writes each update as an SSE frame. Because zip's SendStreamWriter streams
//     THROUGH the ZAP machine transport natively (proven by zip stream_test
//     TestListenZAP_Streams), that SSE endpoint IS the live ZAP stream — a ZAP
//     subscriber gets frames as they flush, no per-handler transport code.
//
//   - ZAP HOOK POINT: a future direct ZAP push subscription (e.g. a browser
//     /zap duplex that grows server-push, or a tasks-events → session-events
//     indexer) attaches by calling subscribe(org) and forwarding updates. The
//     publisher side (publish) does not change.
//
// Delivery is best-effort and non-blocking: a slow subscriber never stalls a
// writer. On buffer overrun the subscriber is dropped (its channel closed) and
// the client reconnects + re-fetches truth from the GET endpoints. The GET tree/
// list/detail endpoints are the source of truth; the stream is a live hint.
type bus struct {
	mu     sync.Mutex
	subs   map[int]*subscriber
	nextID int
	closed bool
}

type subscriber struct {
	org string // tenant filter — a subscriber only ever receives its own org
	ch  chan streamUpdate
}

const subBuffer = 256

func newBus() *bus { return &bus{subs: map[int]*subscriber{}} }

// subscribe registers a tenant-scoped subscriber and returns its channel plus a
// cancel func. cancel is idempotent. After close(), subscribe returns a closed
// channel so a late subscriber exits immediately.
func (b *bus) subscribe(org string) (<-chan streamUpdate, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan streamUpdate)
		close(ch)
		return ch, func() {}
	}
	id := b.nextID
	b.nextID++
	s := &subscriber{org: org, ch: make(chan streamUpdate, subBuffer)}
	b.subs[id] = s
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if cur, ok := b.subs[id]; ok && cur == s {
				delete(b.subs, id)
				close(s.ch)
			}
		})
	}
	return s.ch, cancel
}

// publish fans an update out to every subscriber of the update's org. Non-
// blocking: if a subscriber's buffer is full it is dropped (channel closed), so
// one stuck dashboard can never back-pressure a session write.
func (b *bus) publish(u streamUpdate) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for id, s := range b.subs {
		if s.org != u.Org {
			continue
		}
		select {
		case s.ch <- u:
		default:
			// Overrun: drop this laggard. It reconnects and re-syncs via GET.
			delete(b.subs, id)
			close(s.ch)
		}
	}
}

// close tears the bus down on Shutdown: every subscriber channel is closed so
// its SSE loop returns and the handler unblocks within the shutdown deadline.
func (b *bus) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, s := range b.subs {
		delete(b.subs, id)
		close(s.ch)
	}
}

// sessionsStream is GET /v1/agents/sessions/stream — a Server-Sent Events feed of
// live session + event updates for the caller's org. Optional ?root=<id> scopes
// the feed to one subagent tree. Org-scoped (fail-closed): a subscriber only ever
// receives its own tenant's updates because the bus filters on org.
//
// This handler streams over BOTH the plain HTTP listener and the ZAP machine
// transport with no transport-specific code (zip SendStreamWriter is transport-
// agnostic). Everything the stream loop needs is captured BEFORE SendStreamWriter
// so the loop never touches the request Ctx after the handler returns (fasthttp
// recycles it) — client-gone is detected by a flush error, bounded by a 25s
// heartbeat.
func (s *svc) sessionsStream(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	root := trimField(c.Query("root"))

	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")
	c.SetHeader("X-Accel-Buffering", "no") // defeat proxy buffering of the stream

	ch, cancel := s.bus.subscribe(org)
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer cancel()
		// Initial comment flushes headers so the client's EventSource opens.
		if _, err := w.WriteString(": stream open\n\n"); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
		hb := time.NewTicker(25 * time.Second)
		defer hb.Stop()
		for {
			select {
			case <-hb.C:
				if _, err := w.WriteString(": ping\n\n"); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			case u, open := <-ch:
				if !open {
					return // bus closed this sub (overrun or Shutdown)
				}
				if root != "" && u.RootID != root {
					continue
				}
				if !writeSSE(w, u.Type, u) {
					return // client gone
				}
			}
		}
	})
}

// writeSSE writes one SSE frame (event: <type>\ndata: <json>\n\n) and flushes.
// Returns false on any write/flush error (client disconnected) so the caller
// stops the loop.
func writeSSE(w *bufio.Writer, event string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return true // skip a bad frame, keep the stream alive
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return false
	}
	return w.Flush() == nil
}
