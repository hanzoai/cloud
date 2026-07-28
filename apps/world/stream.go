package world

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

// streamUpdate is one live news refresh fanned out to SSE subscribers. Org is the
// bus tenant filter; Project narrows within the org (the handler drops updates
// for other projects). Items is the freshest merged+filtered feed for that scope.
type streamUpdate struct {
	Org     string     `json:"-"`
	Project string     `json:"-"`
	Items   []NewsItem `json:"items"`
}

// bus is the in-process, org-scoped publish/subscribe fan-out behind
// GET /v1/world/stream. It mirrors the agents session bus exactly (that pattern is
// proven to stream natively over the ZAP machine transport via zip's
// SendStreamWriter): delivery is best-effort and non-blocking — a slow subscriber
// is dropped on buffer overrun and reconnects + re-fetches truth from GET
// /v1/world/news. The GET endpoint is the source of truth; the stream is a live hint.
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

// subscribe registers a tenant-scoped subscriber and returns its channel plus an
// idempotent cancel. After close(), subscribe returns a closed channel so a late
// subscriber exits immediately.
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
// blocking: a subscriber whose buffer is full is dropped (channel closed) so one
// stuck dashboard can never back-pressure a getNews recompute.
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
			delete(b.subs, id)
			close(s.ch)
		}
	}
}

// close tears the bus down on Shutdown: every subscriber channel is closed so its
// SSE loop returns and the handler unblocks within the shutdown deadline.
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

// stream is GET /v1/world/stream — a Server-Sent Events feed of live news
// refreshes for the caller's (org, project). Org-scoped (fail-closed): the bus
// filters on org and the loop drops any update whose Project differs, so a
// subscriber only ever receives its own tenant+project. It streams over both the
// plain HTTP listener and the ZAP machine transport with no transport-specific
// code (zip SendStreamWriter is transport-agnostic). org/project are captured
// (both cloned by scope) BEFORE SendStreamWriter so the loop never touches the
// request Ctx after the handler returns — client-gone is a flush error, bounded
// by a 25s heartbeat.
func (s *service) stream(c *zip.Ctx) error {
	org, project, err := scope(c)
	if err != nil {
		return err
	}

	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")
	c.SetHeader("X-Accel-Buffering", "no") // defeat proxy buffering of the stream

	ch, cancel := s.bus.subscribe(org)
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer cancel()
		if _, err := w.WriteString(": open\n\n"); err != nil {
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
				if u.Project != project {
					continue
				}
				if !writeSSE(w, "news", newsResponse{Items: u.Items}) {
					return // client gone
				}
			}
		}
	})
}

// writeSSE writes one SSE frame (event: <type>\ndata: <json>\n\n) and flushes.
// Returns false on any write/flush error (client disconnected).
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
