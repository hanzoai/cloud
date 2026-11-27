package patrol

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/zap-proto/zip"
)

// feed.go carries a change in the picture from the write that made it to every
// screen watching, and holds the connection those screens read.
//
// A write publishes one frame on the org's own bus subject, bus.Phys(org,
// "patrol"). GET /v1/patrol/feed subscribes to that subject and nothing else and
// writes each frame as a Server-Sent Events record.
//
// The bus rather than a map of subscribers in this process: a control room runs
// behind more than one replica, and an officer's position posted to one pod has
// to reach a controller watching from another. Delivery is best effort — the GET
// routes are the truth and a dropped frame costs one refresh.

// SSE keep-alive, how much of the event trail a reconnecting reader may ask for,
// and the two bounds on open streams. A stream costs a goroutine, a bus
// subscription and a socket for as long as it is held, so it is counted.
//
// Two bounds, because a single one is paid by the wrong caller. Counted against
// the org alone, a customer's portal opening tabs closes the control room's wall
// — the count has no term that could tell them apart, and the screens refused are
// the ones the product exists for. So a reader is bounded by what they
// themselves hold, and the tenant's ceiling sits above that, where it bounds the
// sum without standing in any one reader's way.
const (
	beat        = 25 * time.Second
	feedBacklog = 80
	maxStreams  = 32
	maxTenant   = 512
)

// open streams per key, held in memory: what one process is holding, which is
// what the count protects.
var streams struct {
	sync.Mutex
	held map[string]int
}

// count charges one stream to a key, up to the most that key may hold, and
// answers how to let it go.
func count(key string, most int) (func(), bool) {
	streams.Lock()
	defer streams.Unlock()
	if streams.held == nil {
		streams.held = map[string]int{}
	}
	if streams.held[key] >= most {
		return nil, false
	}
	streams.held[key]++
	return func() {
		streams.Lock()
		defer streams.Unlock()
		if streams.held[key]--; streams.held[key] <= 0 {
			delete(streams.held, key)
		}
	}, true
}

// hold charges one stream to the tenant, against the ceiling everyone in the org
// shares.
func hold(org string) (func(), bool) { return count(org, maxTenant) }

// frame is one live update. Exactly one of the four payloads is set, and Kind
// says which.
type frame struct {
	Kind     string            `json:"kind"`
	Unit     string            `json:"unit,omitempty"`
	Lat      float64           `json:"lat,omitempty"`
	Lon      float64           `json:"lon,omitempty"`
	At       string            `json:"at,omitempty"`
	Incident *patrolIncident   `json:"incident,omitempty"`
	Event    *patrolEvent      `json:"event,omitempty"`
	Point    *patrolCheckpoint `json:"point,omitempty"`
}

// publish sends one frame on the org's subject. Best effort: a bus that is not
// reachable leaves a screen to its next read, and never fails the write that
// produced the frame.
func (o ops) publish(ctx context.Context, org string, f frame) {
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	if err := bus.Publish(bus.Phys(org, Module), b); err != nil {
		o.s.Log.Debug("patrol: frame not published", "org", org, "kind", f.Kind, "err", err)
		return
	}
}

// feed is GET /v1/patrol/feed: an open Server-Sent Events response carrying the
// caller's org and no other. ?since=<YYYY-MM-DD HH:MM:SS> replays the events
// written at or after that moment before the stream goes live, so a reader that
// reconnected misses nothing it can still see.
//
// Everything the loop needs is read before SendStreamWriter, because the loop
// outlives the handler and the request is recycled under it.
func feed(s *cloud.Service[state], c *zip.Ctx) error {
	o := ops{s: s}
	ctx := c.Context()
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return err
	}
	// Resolved before the loop, like everything else it needs: a customer reads
	// the frames about their own estate and no others, and the request the answer
	// came from is recycled under the loop.
	r := o.reachOf(ctx, org)
	since := strings.Clone(strings.TrimSpace(c.Query("since")))
	backlog := o.backlog(ctx, org, since, r)

	mine, held := count(actor(ctx), maxStreams)
	if !held {
		return zip.Errorf(http.StatusTooManyRequests, "too many open streams")
	}
	let, held := hold(org)
	if !held {
		mine()
		return zip.Errorf(http.StatusTooManyRequests, "too many open streams")
	}
	msgs := make(chan bus.Message, feedBacklog)
	sub, err := bus.Subscribe(bus.Phys(org, Module), msgs)
	if err != nil {
		mine()
		let()
		return zip.Errorf(http.StatusServiceUnavailable, "feed unavailable")
	}

	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")
	c.SetHeader("X-Accel-Buffering", "no")

	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer mine()
		defer let()
		defer func() { _ = sub.Unsubscribe() }()
		if !open(w) {
			return
		}
		for _, f := range backlog {
			if !send(w, f) {
				return
			}
		}
		hb := time.NewTicker(beat)
		defer hb.Stop()
		for {
			select {
			case <-hb.C:
				if !ping(w) {
					return
				}
			case m, live := <-msgs:
				if !live {
					return
				}
				if !r.carries(m.Data) {
					continue
				}
				if !raw(w, m.Data) {
					return
				}
			}
		}
	})
}

// carries reports whether a published frame is this caller's to read. A frame
// names the site it is about, so the answer is the one sees gives for any other
// row; a unit's position names no site and belongs to the operation.
func (r reach) carries(b []byte) bool {
	if r.all {
		return true
	}
	var f frame
	if err := json.Unmarshal(b, &f); err != nil {
		return false
	}
	switch {
	case f.Incident != nil:
		return r.sees(f.Incident.Site)
	case f.Event != nil:
		return r.sees(f.Event.Site)
	case f.Point != nil:
		return r.sees(f.Point.Site)
	}
	return false
}

// backlog is the events a reconnecting reader asks for with ?since=. It reads
// the last feedBacklog events THIS reader may see and keeps those at or after
// the moment named; an unreadable or absent value replays nothing.
//
// The window is the caller's own rather than the operation's trimmed afterwards:
// a customer whose row sits behind eighty of somebody else's would reconnect to
// an empty replay, and the one read a reconnecting screen makes is the one that
// must not lose it.
func (o ops) backlog(ctx context.Context, org, since string, r reach) []frame {
	if since == "" {
		return nil
	}
	mark, err := time.Parse(stampLayout, since)
	if err != nil {
		return nil
	}
	docs := o.seen(ctx, org, DTEvent, nil, feedBacklog, r)
	out := make([]frame, 0, len(docs))
	for _, d := range oldest(docs) {
		at, err := time.Parse(stampLayout, strField(d, "at"))
		if err != nil || at.Before(mark) {
			continue
		}
		v := viewEvent(d)
		out = append(out, frame{Kind: "event", Event: &v})
	}
	return out
}

// open flushes the headers so the reader's connection is established.
func open(w *bufio.Writer) bool {
	if _, err := w.WriteString(": stream open\n\n"); err != nil {
		return false
	}
	return w.Flush() == nil
}

// ping holds an idle connection open and proves the reader is still there.
func ping(w *bufio.Writer) bool {
	if _, err := w.WriteString(": ping\n\n"); err != nil {
		return false
	}
	return w.Flush() == nil
}

// send writes one frame as an SSE record.
func send(w *bufio.Writer, f frame) bool {
	b, err := json.Marshal(f)
	if err != nil {
		return true
	}
	return raw(w, b)
}

// raw writes bytes that are already a frame. Reports false once the reader has
// gone, which is the only way this loop learns it.
func raw(w *bufio.Writer, b []byte) bool {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return false
	}
	return w.Flush() == nil
}
