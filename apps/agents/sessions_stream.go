package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/principal"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// streamUpdate is one live update fanned out to subscribers: either a session
// lifecycle change (register / status / control) or an appended event. Org and
// RootID are carried so the bus can filter by tenant AND a subscriber can scope
// to a single subagent tree (?root=); Actor is the owner of the session the
// update is about, so a subscriber is sent only what it may see (reader.sees).
// Exactly one of Session/Event is set.
type streamUpdate struct {
	Org     string       `json:"-"`
	RootID  string       `json:"-"`
	Actor   string       `json:"-"`
	Channel string       `json:"-"` // the session's: the app's conversation is watched by its person alone (sees)
	Type    string       `json:"-"` // "session" | "event"
	Session *sessionView `json:"session,omitempty"`
	Event   *eventView   `json:"event,omitempty"`
}

// bus is the in-process publish/subscribe fan-out under the sessions surface. It
// is the SINGLE client the live stream hangs off:
//
//   - Today: the SSE handler (GET /v1/agent/sessions/stream) subscribes and
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

// sandboxApp is who answers client.SandboxAttach. Named here because agents holds
// the only thing that knows a person is watching, and apps/sandbox holds the
// clock that changes length when they are.
const sandboxApp = "sandbox"

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

// sessionsStream is GET /v1/agent/sessions/stream — a Server-Sent Events feed of
// live session + event updates for the caller's org. Optional ?root=<id> scopes
// the feed to one subagent tree. Org-scoped (fail-closed): a subscriber only ever
// receives its own tenant's updates because the bus filters on org. And within
// the org it receives what it may see, by the rule GET /v1/agent/sessions/{id}
// answers by: a member the sessions they opened, an org admin or a SuperAdmin the
// org's — so another member's transcript does not arrive here frame by frame when
// it would be a 404 read whole.
//
// This handler streams over BOTH the plain HTTP listener and the ZAP machine
// transport with no transport-specific code (zip SendStreamWriter is transport-
// agnostic). Everything the stream loop needs is captured BEFORE SendStreamWriter
// so the loop never touches the request Ctx after the handler returns (fasthttp
// recycles it) — client-gone is detected by a flush error, bounded by a 25s
// heartbeat.
func sessionsStream(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	// CLONE the root filter: c.Query returns a zero-copy view into the fasthttp
	// request buffer, and the stream loop below OUTLIVES this handler (it runs
	// under SendStreamWriter after the Ctx is recycled). tenant() already clones
	// org for exactly this reason; root is retained past the request the same way,
	// so it must be an owned copy or the filter races a reused buffer.
	root := strings.Clone(trimField(c.Query("root")))
	// The PROJECT this stream is watching, if it is watching one. Cloned for the
	// same reason as root: the loop below outlives the request.
	//
	// It is what makes this stream say somebody is HERE. A sandbox can see when it
	// was last called and nothing else, so a person reading for twenty minutes and
	// a tab closed twenty minutes ago look identical to it — and its idle clock is
	// a different length depending on which one is true. This is the only place in
	// the fleet that knows.
	project := strings.Clone(trimField(c.Query("project")))
	// Who is watching, read NOW: the loop below outlives the request, and both
	// facts are copies (billingActor builds its string; the bits are booleans).
	// c.Forward() is the context that carries the caller — an untyped handler's
	// own does not — and the subject the boundary attested is parked on it here,
	// as a typed op's is, so a credential that names no stable subject reads as
	// nobody rather than as the username it carries.
	who := readerOf(principal.WithSubject(c.Forward(), c), org)
	// And who is watching as the sandbox reads it (watcherOf), copied now. A
	// viewer the sandbox cannot key holds none, so their stream beats for nothing.
	watcher, keyed := watcherOf(c.Forward(), org)
	if !keyed {
		project = ""
	}

	rawAfter := strings.TrimSpace(c.Query("after"))
	if rawAfter == "" {
		rawAfter = strings.TrimSpace(c.Header("Last-Event-ID"))
	}
	var (
		after    int64
		hasAfter bool
		sto      *Store
	)
	if rawAfter != "" {
		parsed, err := strconv.ParseInt(rawAfter, 10, 64)
		if err != nil || parsed < 0 {
			return zip.ErrBadRequest("after must be a non-negative integer event seq")
		}
		after, hasAfter = parsed, true
		sto, _ = s.State.storeFor(org)
	}

	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")
	c.SetHeader("X-Accel-Buffering", "no") // defeat proxy buffering of the stream

	ch, cancel := s.State.bus.subscribe(org)
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer cancel()
		// Initial comment flushes headers so the client's EventSource opens.
		if _, err := w.WriteString(": stream open\n\n"); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
		var replayed map[string]int64
		if hasAfter && sto != nil {
			replayCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
			missed, rerr := sto.ReplayEvents(replayCtx, org, root, after, 2000)
			done()
			if rerr == nil && len(missed) > 0 {
				replayed = make(map[string]int64, 4)
				for _, r := range missed {
					if !who.sees(Session{Actor: r.SessionActor, Channel: r.Channel}) {
						continue
					}
					ev := toEventView(r.Event)
					u := streamUpdate{Type: "event", Event: &ev}
					if !writeSSEWithID(w, strconv.FormatInt(ev.Seq, 10), "event", u) {
						return
					}
					if ev.Seq > replayed[ev.SessionID] {
						replayed[ev.SessionID] = ev.Seq
					}
				}
			}
		}
		// client.AttachEvery, not a second 25s written here: the beat and the
		// staleness apps/sandbox allows are one contract between two processes, and
		// a copy of the number is how they drift apart.
		hb := time.NewTicker(client.AttachEvery)
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
				// PRESENCE RIDES THE BEAT THAT ALREADY EXISTS, and it is sent AFTER
				// the flush deliberately: the flush is what proves a client is still
				// on the other end, so a stream whose reader has gone reports nothing
				// and the sandbox falls to its short clock, which is the point.
				//
				// Failure is not surfaced. This is a report, not a request the client
				// made: the worst a lost beat costs is one interval of staleness
				// against a grace of three, and a stream that logged a warning every
				// 25 seconds because sandbox is not mounted in this deployment would
				// be noise nobody could act on.
				if project != "" {
					_, _ = plane.Call[client.AttachIn, client.Attached](
						zip.WithCaller(context.Background(), watcher), sandboxApp,
						client.SandboxAttach, &client.AttachIn{Project: project})
				}
			case u, open := <-ch:
				if !open {
					return // bus closed this sub (overrun or Shutdown)
				}
				if root != "" && u.RootID != root {
					continue
				}
				if !who.sees(Session{Actor: u.Actor, Channel: u.Channel}) {
					continue
				}
				if u.Event != nil && replayed != nil && u.Event.Seq <= replayed[u.Event.SessionID] {
					continue
				}
				id := ""
				if u.Event != nil && u.Event.Seq > 0 {
					id = strconv.FormatInt(u.Event.Seq, 10)
				}
				if !writeSSEWithID(w, id, u.Type, u) {
					return // client gone
				}
			}
		}
	})
}

// watcherOf is who a stream's presence beat states to the sandbox: the viewer's
// subject and their two admin bits, in the org the stream is for. Presence stamps
// the watcher's own sandbox and no colleague's (apps/sandbox planeAttach), so a
// beat stating only the org would keep nobody's alive.
//
// The subject is the one a sandbox is held by (principal.SubjectFrom), never
// X-User-Id: the sandbox keys on what the caller STATES, and a viewer whose
// credential names no such subject — a token with no `sub`, whose id is a
// username or a display name — would be stated as whoever else goes by it. Such a
// viewer is not keyed, and holds no sandbox to keep alive.
func watcherOf(ctx context.Context, org string) (zip.Caller, bool) {
	sub, keyed := principal.SubjectFrom(ctx)
	if !keyed {
		return zip.Caller{}, false
	}
	v := cloud.Who(ctx)
	return zip.Caller{Org: org, User: strings.Clone(sub), Admin: v.Admin, OrgAdmin: v.OrgAdmin}, true
}

// writeSSE writes one SSE frame (event: <type>\ndata: <json>\n\n) and flushes.
// Returns false on any write/flush error (client disconnected) so the caller
// stops the loop.
func writeSSE(w *bufio.Writer, event string, v any) bool {
	return writeSSEWithID(w, "", event, v)
}

func writeSSEWithID(w *bufio.Writer, id, event string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return true // skip a bad frame, keep the stream alive
	}
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, event, b); err != nil {
			return false
		}
	} else if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return false
	}
	return w.Flush() == nil
}
