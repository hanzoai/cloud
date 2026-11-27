package destination

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/commerce/infra"
	nats "github.com/hanzoai/pubsub-go"
	"github.com/hanzoai/pubsub-go/jetstream"
)

// bus.go is where destinations reads the events it forwards: a durable consumer
// on the event plane's own stream.
//
// It used to be an in-process sink (event.AddSink), handed each accepted batch
// by the ingest in the same process. The fleet runs one process per app, so the
// ingest runs in the event app and this sink was registered in the destination
// app, where nothing ingests: every conversion anyone connected a platform for
// was dropped before it reached a single adapter. The stream is how a second
// process sees the first one's events, and it is the fan-out the event plane
// already publishes for exactly this (event.PublishEvents).
//
// THE ENVELOPE, NOT THE FACT. Two vocabularies share the stream (see
// event.EventSignalKey). A message naming a signal is a warehouse fact and is
// acked unread; this consumer reads the subscriber envelope, which carries the
// raw properties a conversion is matched on.
//
// THE CONSUMER NEVER WAITS ON A PLATFORM. Planning is a store read, done in
// line; each send then joins the queue of its lane and the message is acked. A
// lane is one (org, platform), split into what a peer or the org's secret key
// stated and what anyone else sent, so a flood of the second never queues ahead
// of a sale. It is drained by up to perLane workers that exist only while it has
// work. A platform that stops answering fills its own lane and nobody else's,
// and the stream's delivery window is never held by a message waiting on one.
//
// WHAT IS HELD IS BOUNDED. An envelope over maxEnvelope is no conversion and is
// not planned; a lane holds laneDepth sends and the app maxQueued in all. A send
// past either bound is recorded as failed on the org's own plane.

const (
	// durable is this app's consumer on the event stream: one copy of every
	// envelope, shared across replicas and kept across restarts.
	durable = "destination"
	// busClient names the connection in the bus's own monitoring.
	busClient = "destination"
	// perLane bounds the sends one (org, platform) has in flight.
	perLane = 4
	// laneDepth bounds the sends one lane holds waiting.
	laneDepth = 256
	// maxQueued bounds the sends the whole app holds waiting.
	maxQueued = 1024
	// maxEnvelope bounds the event a conversion is read from. A browser's
	// conversion is a few kilobytes; one past this is not one.
	maxEnvelope = 32 << 10
)

// lane is one (org, platform)'s queue and the workers draining it.
type lane struct {
	jobs    chan job
	workers int
}

// listener owns the consumer loop and the sends it queued.
type listener struct {
	s      *cloud.Service[state]
	ctx    context.Context // ends when the app stops: no retry starts after it
	cancel context.CancelFunc
	wg     sync.WaitGroup // the consumer loop
	work   sync.WaitGroup // lane workers

	mu     sync.Mutex
	lanes  map[string]*lane
	queued int // sends waiting across every lane
}

// newListener is a listener not yet reading the bus.
func newListener(s *cloud.Service[state]) *listener {
	ctx, cancel := context.WithCancel(context.Background())
	return &listener{s: s, ctx: ctx, cancel: cancel, lanes: map[string]*lane{}}
}

// listen starts the consumer and returns at once. It never fails the mount: a
// bus that is down is retried until it answers.
func listen(s *cloud.Service[state]) *listener {
	l := newListener(s)
	l.wg.Add(1)
	go l.run(l.ctx, bus.URL())
	return l
}

// stop ends the loop, then waits for the lanes: each send already started
// finishes its attempt, and what is still queued is recorded as not sent.
func (l *listener) stop() {
	l.cancel()
	l.wg.Wait()
	l.work.Wait()
}

func (l *listener) run(ctx context.Context, url string) {
	defer l.wg.Done()
	for ctx.Err() == nil {
		cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{URL: url, Name: busClient, EnableJetStream: true, MaxReconnects: -1})
		if err != nil {
			l.s.Log.Warn("destination: bus connect failed, retrying", "url", bus.Display(), "err", err)
			wait(ctx, 5*time.Second)
			continue
		}
		if err := l.consume(ctx, cl); err != nil && ctx.Err() == nil {
			l.s.Log.Warn("destination: consume ended, reconnecting", "err", err)
		}
		_ = cl.Close()
		wait(ctx, 2*time.Second)
	}
}

// consume reads the stream until ctx ends or the subscription does (a deleted
// durable ends it), and returns only once no callback is running, so the loop
// can re-create the durable and stop() never races a callback.
func (l *listener) consume(ctx context.Context, cl *infra.PubSubClient) error {
	if err := event.EnsureEventStream(ctx, cl); err != nil {
		return err
	}
	if _, err := cl.CreateConsumer(ctx, event.EventStream, &infra.ConsumerConfig{
		Name:          durable,
		Durable:       durable,
		Description:   "destination: forward conversions to each org's ad and analytics platforms",
		DeliverPolicy: infra.DeliverNew,
		AckPolicy:     infra.AckExplicit,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
	}); err != nil {
		return err
	}
	cons, err := cl.JetStream().Consumer(ctx, event.EventStream, durable)
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		l.handle(ctx, m.Data())
		_ = m.Ack()
	}, jetstream.ConsumeErrHandler(gone))
	if err != nil {
		return err
	}
	l.s.Log.Info("destination: consuming the event plane", "stream", event.EventStream, "durable", durable)
	select {
	case <-ctx.Done():
		cc.Stop()
		<-cc.Closed()
		return nil
	case <-cc.Closed():
		return errors.New("the subscription ended")
	}
}

// gone ends a subscription whose durable no longer exists, so run makes it again.
// A pull already waiting when the durable is deleted is answered "consumer
// deleted", which ends the subscription by itself. One that reaches the bus after
// is answered "no responders", which the client only reports and then pulls again
// on its heartbeat, forever, reading nothing.
func gone(cc jetstream.ConsumeContext, err error) {
	if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrConsumerNotFound) {
		cc.Stop()
	}
}

// handle takes one envelope: it plans in line and queues each send on its lane.
// A message that panics is logged, not redelivered into the same panic.
func (l *listener) handle(ctx context.Context, data []byte) {
	defer func() {
		if r := recover(); r != nil {
			l.s.Log.Warn("destination: envelope panicked", "err", r)
		}
	}()
	if len(data) > maxEnvelope {
		return
	}
	var env event.EventEnvelope
	if json.Unmarshal(data, &env) != nil || env.Org == "" {
		return
	}
	// A program's visit and the site team's own (event.Audience) are no audience to
	// buy against: they stay in the org's stream and are forwarded nowhere.
	if fact(data) || audited(env) || env.Properties[forwarded] != nil || !event.FromPerson(env.Properties) {
		return
	}
	org := env.Org
	if revenue(env) {
		// The platform's own sale: its ad accounts are the brand org's. The paying
		// org's are not told it bought from us, and a SuperAdmin's own books are
		// no customer's purchase.
		org = l.s.Brand
		if org == "" || env.Org == authz.AdminOrg {
			return
		}
	}
	for _, j := range plan(l.s, org, []event.SinkEvent{sinkOf(env)}) {
		l.enqueue(j)
	}
}

// errFull is what a send a full queue could not take is recorded with.
var errFull = errors.New("not sent: the destination's queue was full")

// errStopped is what a send still queued when the app stopped is recorded with.
var errStopped = errors.New("not sent: the app stopped before it")

// enqueue puts j on its lane and starts a worker when the lane has room for one.
func (l *listener) enqueue(j job) {
	key := j.org + "\x00" + j.dest.ID()
	if stated(j.batch[0]) {
		key += "\x00stated"
	}
	l.mu.Lock()
	ln := l.lanes[key]
	if ln == nil {
		ln = &lane{jobs: make(chan job, laneDepth)}
		l.lanes[key] = ln
	}
	full := l.queued >= maxQueued
	if !full {
		select {
		case ln.jobs <- j:
		default:
			full = true
		}
	}
	switch {
	case !full:
		l.queued++
		if ln.workers < perLane {
			ln.workers++
			l.work.Add(1)
			cloud.Go(l.s.Log, "destination.lane", []any{"org", j.org, "platform", j.dest.ID()}, func() { l.drain(key, ln) })
		}
		l.mu.Unlock()
	default:
		if ln.workers == 0 && len(ln.jobs) == 0 {
			delete(l.lanes, key)
		}
		l.mu.Unlock()
		l.s.Log.Warn("destination: lane full, not sent", "org", j.org, "platform", j.dest.ID())
		// Recorded off the consumer's thread, which must not wait on the event
		// plane either; past recording's own bound the log line is the record.
		select {
		case recording <- struct{}{}:
			cloud.Go(l.s.Log, "destination.record", nil, func() {
				defer func() { <-recording }()
				audit(l.s, j.org, j.dest.ID(), j.batch, 0, Result{}, errFull, false)
			})
		default:
		}
	}
}

// recording bounds the not-sent records being written at once.
var recording = make(chan struct{}, 64)

// drain sends what its lane holds and exits when the lane is empty. The check
// and the exit happen under the lock enqueue holds, so no send is left behind.
func (l *listener) drain(key string, ln *lane) {
	defer l.work.Done()
	for {
		l.mu.Lock()
		select {
		case j := <-ln.jobs:
			l.queued--
			l.mu.Unlock()
			if l.ctx.Err() != nil {
				audit(l.s, j.org, j.dest.ID(), j.batch, 0, Result{}, errStopped, false)
				continue
			}
			deliver(l.ctx, l.s, j)
		default:
			if ln.workers--; ln.workers == 0 {
				delete(l.lanes, key)
			}
			l.mu.Unlock()
			return
		}
	}
}

// fact reports a warehouse fact, the stream's other vocabulary.
func fact(data []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(data, &probe) != nil {
		return false
	}
	_, ok := probe[event.EventSignalKey]
	return ok
}

// revenue reports a sale the platform itself made: a payment commerce stated
// after a card settled. It is filed under the org that paid, and it is the
// platform's conversion, so it is forwarded to the platform org's destinations
// and no other's. Only the commerce app can state an event on the plane wire,
// which is what keeps a browser from putting a sale into the platform's ad
// accounts by naming the product "commerce".
func revenue(env event.EventEnvelope) bool {
	return env.Source == sourcePlane && env.Product == "commerce" && event.CarriesMoney(env.Name)
}

// audited reports this app's own delivery record coming back round the plane.
// Forwarding it would record another, forever.
func audited(env event.EventEnvelope) bool {
	return env.Source == sourcePlane && env.Product == auditProduct
}

// sinkOf is the envelope as the translator reads it.
func sinkOf(env event.EventEnvelope) event.SinkEvent {
	return event.SinkEvent{
		MessageID:   env.ID,
		Name:        strings.TrimSpace(env.Name),
		Source:      env.Source,
		Product:     env.Product,
		Vouched:     env.Vouched,
		DistinctID:  env.DistinctID,
		AnonymousID: env.AnonymousID,
		Time:        env.Time,
		URL:         env.URL,
		Path:        env.Path,
		Referrer:    env.Referrer,
		Revenue:     env.Revenue,
		Currency:    env.Currency,
		ProductID:   env.ProductID,
		Quantity:    env.Quantity,
		Properties:  env.Properties,
	}
}

func wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
