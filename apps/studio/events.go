package studio

// events.go — GET /v1/studio/runs/{id}/events: a run's progress as Server-Sent
// Events, replayable.
//
// EVERY CHANGE IS A ROW. A run's events are rows numbered from 1 (run, seq), written
// in the same process that changes the run, so a stream is a read of the rows after
// the last one it sent, and a reconnect with Last-Event-ID resumes exactly there:
// nothing missed, nothing twice. A row names what changed (a step, an asset); the
// frame carries it AS IT IS NOW, so an asset's URL in a replayed frame is a fresh
// one rather than one that expired an hour ago.
//
// A write rings the run's bell, which wakes its streams at once; with no ring a
// stream reads again every few seconds, so a writer in another replica is seen too.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/orm/tenant"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
)

const (
	// beat is how often an idle stream writes a comment, which is also how a
	// vanished client is noticed.
	beat = 15 * time.Second
	// reread is how often a stream reads its rows when nothing rang.
	reread = 3 * time.Second
	// maxStreams bounds the streams one org holds open.
	maxStreams = 64
)

// emit appends one event to a run and rings its bell. A failed append is logged and
// dropped: the run's state is in its rows either way, and a stream reads it there.
func (s *state) emit(ctx context.Context, run, kind string, data map[string]any) {
	b, _ := json.Marshal(data)
	if data == nil {
		b = []byte("{}")
	}
	err := s.db.Tx(ctx, func(tx *tenant.DB) error {
		last, ok, err := tx.Select[eventRow]("events", "seq").Where(tenant.Eq("run", run)).OrderBy("seq DESC").Limit(1).First(ctx)
		if err != nil {
			return err
		}
		seq := int64(1)
		if ok {
			seq = last.Seq + 1
		}
		return tx.Insert(ctx, "events", eventRow{Run: run, Seq: seq, Kind: kind, Data: string(b), At: now()})
	})
	if err != nil {
		s.log.Debug("studio: append an event", "run", run, "kind", kind, "err", err)
		return
	}
	s.bells.ring(run)
	if kind == "run" || kind == "done" {
		s.track(ctx, run)
	}
}

// bells wakes the streams of a run.
type bells struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
	open map[string]int
}

func newBells() *bells {
	return &bells{subs: map[string]map[chan struct{}]struct{}{}, open: map[string]int{}}
}

func (b *bells) ring(run string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[run] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// listen opens a bell on a run for one org's stream, or refuses past the bound.
func (b *bells) listen(org, run string) (<-chan struct{}, func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open[org] >= maxStreams {
		return nil, nil, false
	}
	ch := make(chan struct{}, 1)
	if b.subs[run] == nil {
		b.subs[run] = map[chan struct{}]struct{}{}
	}
	b.subs[run][ch] = struct{}{}
	b.open[org]++
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[run], ch)
		if len(b.subs[run]) == 0 {
			delete(b.subs, run)
		}
		if b.open[org]--; b.open[org] <= 0 {
			delete(b.open, org)
		}
	}, true
}

func init() {
	spec.Register(prefix+"/runs/:id/events", http.MethodGet, nil, spec.Bytes{Type: "text/event-stream"})
	spec.Describe(prefix+"/runs/:id/events", http.MethodGet,
		"Stream a run's progress as Server-Sent Events",
		"Holds text/event-stream open on one run you may read and writes one frame per change until the run "+
			"ends, then a final `done` frame, and closes. Every frame carries `id:`, the event's sequence number "+
			"in the run, so a client that reconnects with Last-Event-ID (or ?after=) receives exactly what it "+
			"missed. A run that already ended replays its events and closes.\n\n"+
			"Frames (`event:` name, `data:` JSON): `run` {seq, run} on a status change; `step` {seq, run, step} "+
			"when a step's status, attempt or progress moves; `asset` {seq, run, step, asset} when an output "+
			"lands, as its Library item with fresh signed URLs; `log` {seq, run, step, text}; `done` {seq, run} when the run ended "+
			"(succeeded, failed or canceled). Each carries the run, step or asset as it is now. A `: ping` "+
			"comment every 15 seconds keeps intermediaries from closing the stream.\n\n"+
			"Authenticate with `Authorization: Bearer` (a fetch-based reader; a browser EventSource cannot set "+
			"it) or the session cookie. 404 for a run you may not read; 429 when your org already holds 64 streams.")
}

// events streams one run. UNTYPED: the answer is a stream that outlives the
// handler, where a typed op answers one value.
func (o ops) events(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	ctx := scoped(c.Forward(), org)
	me, err := person(ctx)
	if err != nil {
		return err
	}
	id := c.Param("id")
	st := o.s.State
	if _, err := st.readableRun(ctx, me, id); err != nil {
		return notFound(err)
	}
	after := int64(0)
	if v := firstNonEmpty(c.Header("Last-Event-ID"), c.Query("after")); v != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n < 0 {
			return zip.ErrBadRequest("after is the sequence number of the last event received")
		}
		after = n
	}
	bell, stop, ok := st.bells.listen(org, id)
	if !ok {
		return zip.Errorf(http.StatusTooManyRequests, "this org holds %d live streams; close one", maxStreams)
	}
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Accel-Buffering", "no")
	bg := scoped(context.WithoutCancel(ctx), org)
	err = c.SendStreamWriter(func(w *bufio.Writer) {
		defer stop()
		st.pump(bg, w, me, id, after, bell)
	})
	if err != nil {
		stop()
	}
	return err
}

// pump writes the run's events after the given one until its done event, a failed
// write, or the run becomes unreadable.
func (s *state) pump(ctx context.Context, w *bufio.Writer, me, run string, after int64, bell <-chan struct{}) {
	if _, err := w.WriteString(": studio\n\n"); err != nil || w.Flush() != nil {
		return
	}
	hb := time.NewTicker(beat)
	defer hb.Stop()
	again := time.NewTicker(reread)
	defer again.Stop()
	for {
		rows, err := s.db.Select[eventRow]("events").Where(tenant.And(tenant.Eq("run", run), tenant.Gt("seq", after))).
			OrderBy("seq ASC").Limit(200).All(ctx)
		if err != nil {
			return
		}
		for _, e := range rows {
			frame, err := s.frame(ctx, me, run, e)
			if err != nil {
				return
			}
			if !write(w, e.Seq, e.Kind, frame) {
				return
			}
			after = e.Seq
			if e.Kind == "done" {
				return
			}
		}
		if len(rows) == 200 {
			continue
		}
		select {
		case <-bell:
		case <-again.C:
		case <-hb.C:
			if _, err := w.WriteString(": ping\n\n"); err != nil || w.Flush() != nil {
				return
			}
		}
	}
}

// frame is one event as the stream carries it.
func (s *state) frame(ctx context.Context, me, run string, e eventRow) (map[string]any, error) {
	var data map[string]any
	_ = json.Unmarshal([]byte(e.Data), &data)
	out := map[string]any{"seq": e.Seq}
	switch e.Kind {
	case "run", "done":
		r, err := s.readableRun(ctx, me, run)
		if err != nil {
			return nil, err
		}
		v, err := s.runView(ctx, r)
		if err != nil {
			return nil, err
		}
		out["run"] = v
	case "step":
		out["run"] = run
		id, _ := data["step"].(string)
		st, ok, err := s.stepRow(ctx, run, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out["step"] = st.view()
		}
	case "asset":
		out["run"], out["step"] = run, data["step"]
		id, _ := data["asset"].(string)
		if it, err := s.readable(ctx, id); err == nil {
			out["asset"] = it
		}
	default:
		out["run"], out["step"], out["text"] = run, data["step"], data["text"]
	}
	return out, nil
}

// write sends one frame.
func write(w *bufio.Writer, seq int64, kind string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	if _, err := w.WriteString("id: " + strconv.FormatInt(seq, 10) + "\nevent: " + kind + "\ndata: "); err != nil {
		return false
	}
	if _, err := w.Write(b); err != nil {
		return false
	}
	if _, err := w.WriteString("\n\n"); err != nil {
		return false
	}
	return w.Flush() == nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
