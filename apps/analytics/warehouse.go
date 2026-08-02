// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// warehouse.go — THE SINK. The first consumer of the EVENT stream, and the one that
// makes a fact queryable.
//
// It is a CONSUMER, not a step in the ingest path. The door published and answered
// already (bus.go); this runs behind it, on its own durable, at its own pace. A
// warehouse that is down therefore stops rows from LANDING, never from being ACCEPTED —
// they wait on the stream and land when it returns. That is the difference between
// backpressure and data loss, and it is the reason the insert moved off the handler.
//
// ONE DURABLE PER SIGNAL, not per table. The signals share one table now, but they do
// NOT share a queue: event.error draining slowly must not hold up event.act, so each
// binds its own durable filtered to its own subject. Isolation is a property of the
// hand-off, and it survives the tables merging.
//
// COMMIT, THEN ACK — in that order, always. The insert must be on disk before the
// message is acknowledged; acking first would lose the fact while the bus believed it
// delivered. An unacked message is redelivered, which is safe because the fact table is
// a ReplacingMergeTree keyed (org, time, id): a redelivered fact collapses on merge
// instead of duplicating. Idempotency is therefore STRUCTURAL — no consumer hand-rolls
// it, and a new consumer cannot forget it. `signal` leading the partition key keeps
// that collapse per-signal, so an id collision across two signals cannot delete a row.
//
// ASYNC INSERT IS WHAT MAKES PER-MESSAGE ACKING AFFORDABLE. One INSERT per event would
// explode the part count; the store's own async_insert batches many statements into one
// part server-side, and wait_for_async_insert=1 makes the statement return only once
// that batch is flushed. So the commit-before-ack ordering holds AND the write pattern
// stays columnar. The batching lives where the batching belongs — in the store — rather
// than in a buffer this process would lose on restart.

package analytics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/commerce/infra"
	luxlog "github.com/luxfi/log"
)

// warehouseReady and warehouseExec are the store gate and the statement executor, held
// as values on the same terms as resolveKeyOrg: production is always the one datastore
// client, and a test substitutes them to drive the sink without standing up a store.
var (
	warehouseReady = datastore.Ready
	warehouseExec  = datastore.Exec
)

// insertSettings put the batching in the store. wait_for_async_insert=1 keeps the
// statement synchronous from this process's point of view, which is what the
// commit-before-ack ordering depends on.
//
// It renders BEFORE the VALUES keyword. The Values input format reads everything
// after VALUES as data, so a trailing SETTINGS is not a setting — it is a row the
// parser cannot read, and the statement fails with CANNOT_PARSE_INPUT_ASSERTION_FAILED.
const insertSettings = " SETTINGS async_insert=1, wait_for_async_insert=1"

// ackWait bounds how long a consumer may hold a message before the bus redelivers it,
// and maxDeliver bounds how many times a poisonous fact can be retried before the bus
// stops handing it out. A fact the store rejects on every attempt is a fact the store
// will never take; retrying it forever would wedge the durable behind it.
const (
	ackWait    = 60 * time.Second
	maxDeliver = 8
)

// table is WHERE a signal lands and HOW a row is built for it: one name, one column
// list, one row builder. There are exactly TWO, because there are exactly two grains —
// an occurrence HAPPENED, a sample was MEASURED. Everything that used to differ per
// signal (the columns, the args, the statement) is now shared, which is the whole
// point: five signals write the identical row shape, so they cannot drift into five
// shapes of one envelope.
type table struct {
	name    string
	columns []string
	args    func(message) []any
}

// writer binds ONE signal to its table. The consume loop, the ack ordering and the
// tenancy check are shared; a writer is that mapping and nothing else.
type writer struct {
	signal signal
	table  table
}

// factColumns is the column list of event.fact, in position order, written down ONCE.
// ingested_at is deliberately absent: the server stamps it through a column DEFAULT, so
// nothing on the wire can influence when a row expires or which copy of a redelivered
// fact wins.
//
// `el` binds as a tuple of six and `frames.*` as five parallel arrays; everything else
// is one placeholder. That is why the placeholder list is BUILT from the columns
// (statement, below) rather than counted by hand.
var factColumns = []string{
	// spine
	"org", "signal", "time", "id",
	// what
	"name", "kind", "message", "severity", "duration",
	// where
	"product", "env", "service", "release", "url", "path",
	// who
	"person_id", "distinct_id", "anonymous_id", "groups",
	// correlation
	"session_id", "trace_id", "span_id", "parent", "resource",
	// open
	"attributes", "el",
	// error
	"issue", "class", "origin", "handled",
	"`frames.function`", "`frames.file`", "`frames.line`", "`frames.column`", "`frames.own`",
	// span
	"status",
	// clip
	"object", "bytes",
}

// elPlaceholder is the `el` tuple's binding: a tuple literal of six placeholders. A
// named tuple accepts a positional tuple of convertible element types, so the six
// annotation values bind as ordinary scalars.
const elPlaceholder = "(?, ?, ?, ?, ?, ?)"

// factArgs renders ONE message as the values of factColumns, in exactly that order.
// There is one of these rather than one per signal, because there is one row shape.
func factArgs(m message) []any {
	el := m.El
	if el == nil {
		el = &messageEl{}
	}
	path := el.Path
	if path == nil {
		path = []string{}
	}
	fn, file := make([]string, 0, len(m.Frames)), make([]string, 0, len(m.Frames))
	line, col := make([]uint32, 0, len(m.Frames)), make([]uint32, 0, len(m.Frames))
	own := make([]bool, 0, len(m.Frames))
	for _, fr := range m.Frames {
		fn, file = append(fn, fr.Function), append(file, fr.File)
		line, col = append(line, fr.Line), append(col, fr.Column)
		own = append(own, fr.Own)
	}
	return []any{
		m.Org, m.Signal, m.Time, m.ID,
		m.Name, m.Kind, m.Message, m.Severity, m.Duration,
		m.Product, m.Env, m.Service, m.Release, m.URL, m.Path,
		m.Person, m.Distinct, m.Anonymous, nonNilMap(m.Groups),
		m.Session, m.Trace, m.Span, m.Parent, m.Resource,
		nonNilMap(m.Attributes), el.Label, el.Role, el.Testid, el.Name, el.Component, path,
		m.Issue, m.Class, m.Origin, m.Handled,
		fn, file, line, col, own,
		m.Status,
		m.Object, m.Bytes,
	}
}

// nonNilMap keeps a nil map from binding as NULL into a non-nullable Map column.
func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// occurrence is the table every signal but `sample` lands in.
var occurrence = table{name: factTable, columns: factColumns, args: factArgs}

// writers is THE SINK SURFACE: one writer per signal, and the one list the consumers
// derive from.
//
// signalSample IS ABSENT, DELIBERATELY, and the reason has moved. It used to be that
// the metric table had no org column at all — its identity was
// (env, temporality, metric_name, fingerprint) — so a writer would have had to insert a
// row it could not attribute to a tenant, and two orgs reporting the same metric name
// would have hashed to one fingerprint and interleaved their samples into one series.
// That is a cross-tenant write, not a missing feature.
//
// The org-keyed table now EXISTS (event.sample, migration 0003), so the remaining
// precondition is that the rename has been applied to the deployment this binary talks
// to. Adding the sixth writer against a table that is not there yet would make the door
// accept metrics and the sink fail every insert until the bus gave up on them — a 200
// that means "discarded", which is exactly the failure the derivation below exists to
// prevent. So the writer lands with the migration, in one line:
//
//	{signalSample, measurement}
//
// where `measurement` is table{name: sampleTable, columns: …, args: …}.
//
// Until then the door REFUSES a sample rather than accepting it (landableSignals,
// below) — the same refusal for the same reason, at the boundary where a caller can
// still be told.
var writers = []writer{
	{signalAct, occurrence},
	{signalClip, occurrence},
	{signalError, occurrence},
	{signalLog, occurrence},
	{signalSpan, occurrence},
}

// landableSignals is WHICH SIGNALS CAN BE MADE DURABLE, derived from writers so the
// answer is written down ONCE. The door reads it (ingestEvents, capture.go) and refuses
// a fact it cannot land, which is what keeps "accepted" honest: publishing to a subject
// no writer drains would put the fact on the stream, answer 200, and then let it expire
// at MaxAge with nothing to show for it.
//
// Deriving it — rather than keeping a second list of "supported types" beside the
// writers — is what makes the two impossible to disagree about. A writer added here is
// a signal the door accepts, in one edit.
var landableSignals = func() map[signal]bool {
	m := make(map[signal]bool, len(writers))
	for _, w := range writers {
		m[w.signal] = true
	}
	return m
}()

// ── acknowledged loss, counted ───────────────────────────────────────────────
//
// Two things remove a fact the door ALREADY ANSWERED 200 FOR, and both used to happen
// with nothing to alarm on:
//
//   - undecodable — a message that does not parse is acked and dropped (land, below).
//     That is the right call for the durable, and it is still a lost fact.
//   - exhausted — after maxDeliver failed attempts the bus stops redelivering. It
//     announces that on the MAX_DELIVERIES advisory, which nothing subscribed to, so
//     the fact vanished leaving no record anywhere at all.
//
// Both are counted here and reported by /v1/analytics/health, so the alarm is
// "lost > 0" on a probe an operator already scrapes rather than a log line nobody
// greps. A COUNTER IS THE FLOOR, NOT THE CEILING: neither case recovers the fact. The
// dead-letter stream that re-publishes the raw payload for replay is the real answer
// and is its own change — this is what makes the loss impossible to miss meanwhile.
var (
	lostUndecodable atomic.Int64
	lostExhausted   atomic.Int64
)

// loss is the sink's own health: what it has irrecoverably dropped since boot.
type loss struct {
	// Undecodable counts messages acked without landing because they did not parse.
	Undecodable int64 `json:"undecodable"`
	// Exhausted counts facts the bus abandoned after maxDeliver failed inserts.
	Exhausted int64 `json:"exhausted"`
}

func lossReport() loss {
	return loss{
		Undecodable: lostUndecodable.Load(),
		Exhausted:   lostExhausted.Load(),
	}
}

// maxDeliverAdvisory is the bus's own announcement that it has GIVEN UP on a message —
// the one event that marks a fact as lost rather than late. The subject is JetStream's
// published advisory grammar
// ($JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<consumer>); the trailing
// wildcard covers every durable this sink binds, present and future, so a table added
// to writers is watched without a second edit.
var maxDeliverAdvisory = "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES." + EventStream + ".*"

// statement renders this sink's INSERT: the fixed column list (never caller input) and
// one placeholder per bound value. `el` is the one column whose binding is a tuple of
// six, so the placeholder list is built from the columns rather than counted.
//
// It hangs off the TABLE and not the writer, because five writers share one statement:
// rendering it per signal would be five identical strings that a sixth signal could
// silently make six different ones.
func (t table) statement() string {
	ph := make([]string, 0, len(t.columns))
	for _, c := range t.columns {
		if c == "el" {
			ph = append(ph, elPlaceholder)
			continue
		}
		ph = append(ph, "?")
	}
	return "INSERT INTO " + t.name + " (" + strings.Join(t.columns, ", ") + ")" +
		insertSettings + " VALUES (" + strings.Join(ph, ", ") + ")"
}

// write commits ONE fact. It returns an error when the row did not land, which is what
// keeps the message unacked and therefore redelivered.
//
// It REFUSES an unattributed fact. org is stamped server-side at normalize, so an empty
// one cannot arrive from a well-formed lane — but this is the last gate before a row
// exists, and a row with no tenant is readable by every tenant. Fail closed here rather
// than trust that every future publisher got it right.
func (w writer) write(ctx context.Context, m message) error {
	if strings.TrimSpace(m.Org) == "" {
		return fmt.Errorf("refusing an unattributed %s fact (id %q)", w.signal, m.ID)
	}
	return warehouseExec(ctx, w.table.statement(), w.table.args(m)...)
}

// drain owns the consumers — one per writer, all on the one bus connection. It is the
// SINK of the plane; forward.go's `sink` is a different thing entirely (the outbound
// fan-out to an org's connected ad platforms), which is why this one is named for what
// it does to the stream rather than for being a sink.
type drain struct {
	log luxlog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	client *infra.PubSubClient
}

// start brings the sink up in the background. It never blocks and never fails a mount:
// a bus or a store that is down at boot is a warning and a retry, because the door can
// keep accepting into the stream regardless — which is exactly the decoupling the bus
// buys.
func (s *drain) start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	go s.run(ctx)
	s.log.Info("event sink started", "stream", EventStream, "tables", tableNames())
}

// stop cancels the consumers and closes the connection.
func (s *drain) stop() {
	s.mu.Lock()
	cancel, cl := s.cancel, s.client
	s.cancel, s.client = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cl != nil {
		_ = cl.Close()
	}
}

// run connects and consumes, reconnecting forever until ctx is canceled.
func (s *drain) run(ctx context.Context) {
	for ctx.Err() == nil {
		cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{
			URL:             busURL(),
			Name:            plane + "-sink",
			EnableJetStream: true,
			MaxReconnects:   -1,
		})
		if err != nil {
			s.log.Warn("event sink: bus connect failed — retrying", "url", busURL(), "err", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		s.setClient(cl)
		if err := s.consume(ctx, cl); err != nil && ctx.Err() == nil {
			s.log.Warn("event sink: consume ended — reconnecting", "err", err)
		}
		_ = cl.Close()
		s.setClient(nil)
		sleep(ctx, 2*time.Second)
	}
}

// consume binds one durable per writer and runs its loop. Each is filtered to its own
// subject, so the tables drain independently.
func (s *drain) consume(ctx context.Context, cl *infra.PubSubClient) error {
	errc := make(chan error, len(writers))
	// Watch the give-up advisory BEFORE binding the durables, so a message that
	// exhausts its deliveries during this connection is counted rather than missed.
	// A failure to subscribe is a warning and not a consume failure: losing the
	// ACCOUNTING must never stop the LANDING.
	if sub, err := cl.Subscribe(maxDeliverAdvisory, s.exhausted); err != nil {
		s.log.Warn("event sink: max-deliveries advisory unwatched — abandoned facts will not be counted",
			"subject", maxDeliverAdvisory, "err", err)
	} else {
		defer func() { _ = sub.Unsubscribe() }()
	}
	for _, w := range writers {
		w := w
		durable := plane + "-" + string(w.signal)
		if _, err := cl.CreateConsumer(ctx, EventStream, &infra.ConsumerConfig{
			Name:          durable,
			Durable:       durable,
			Description:   "land " + w.signal.subject() + " in " + w.table.name,
			FilterSubject: w.signal.subject(),
			// DeliverAll, not DeliverNew: a sink that starts after the door has been
			// accepting must land what is already on the stream, which is the whole
			// point of the stream outliving this process.
			DeliverPolicy: infra.DeliverAll,
			AckPolicy:     infra.AckExplicit,
			AckWait:       ackWait,
			MaxDeliver:    maxDeliver,
		}); err != nil {
			return fmt.Errorf("create consumer %s: %w", durable, err)
		}
		// Contained: this runs over bus payloads, so a panic here would kill the
		// process and every tenant with it rather than this one consumer.
		cloud.Go(s.log, "event.sink", []any{"signal", string(w.signal)}, func() {
			var once sync.Once
			send := func(err error) { once.Do(func() { errc <- err }) }
			defer send(fmt.Errorf("event sink: %s consumer panicked (recovered)", w.signal))
			send(cl.ConsumeMessages(ctx, EventStream, durable, func(sm *infra.StreamMessage) error {
				return s.land(ctx, w, sm)
			}))
		})
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errc:
		return err
	}
}

// land commits ONE message and reports whether it may be acked. Returning an error
// leaves the message unacked, so the bus redelivers it — which is the correct answer
// for a store that is down and, thanks to the ReplacingMergeTree key, a harmless one
// for a store that already took it.
//
// A message that does not DECODE is acked, not retried: it is not going to decode on
// the ninth attempt either, and holding the durable behind it would stop every
// well-formed fact on the same table. It is logged rather than swallowed.
func (s *drain) land(ctx context.Context, w writer, sm *infra.StreamMessage) error {
	m, err := decodeMessage(sm.Data)
	switch {
	case errors.Is(err, errNotAFact):
		// The other vocabulary on this plane (EventSignalKey, bus.go) — an
		// EventEnvelope the subscriber fan-out published onto a subject this writer
		// happens to drain. Nothing was lost: it is not addressed to this consumer, so
		// it is acked and NOT counted. Debug, because on a busy plane it is the
		// steady state and not an event.
		s.log.Debug("event sink: not a fact — acked and left to its own consumer",
			"subject", sm.Subject)
		return nil
	case err != nil:
		s.log.Error("event sink: undecodable message — acked and dropped, FACT LOST",
			"subject", sm.Subject, "lost", lostUndecodable.Add(1), "err", err)
		return nil
	}
	if !warehouseReady() {
		return fmt.Errorf("warehouse not ready")
	}
	if err := w.write(ctx, m); err != nil {
		s.log.Warn("event sink: insert failed — will redeliver",
			"table", w.table.name, "id", m.ID, "err", err)
		return err
	}
	return nil
}

// exhausted records one fact the bus has given up redelivering. The advisory payload
// carries the stream sequence of the abandoned message, so it is logged VERBATIM: that
// sequence is the only handle an operator has left on a fact this process will never
// see again.
func (s *drain) exhausted(m *infra.Message) {
	s.log.Error("event sink: fact abandoned after maxDeliver attempts — ACKNOWLEDGED DATA LOST",
		"lost", lostExhausted.Add(1), "advisory", string(m.Data))
}

func (s *drain) setClient(cl *infra.PubSubClient) {
	s.mu.Lock()
	s.client = cl
	s.mu.Unlock()
}

// sleep waits for d, aborting early if ctx is canceled.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// tableNames is the DISTINCT set of tables this sink writes, in writer order. It is a
// set and not one name per writer, because five writers share one table and a boot log
// that repeated it five times would read like five tables.
func tableNames() []string {
	seen := make(map[string]bool, len(writers))
	out := make([]string, 0, len(writers))
	for _, w := range writers {
		if seen[w.table.name] {
			continue
		}
		seen[w.table.name] = true
		out = append(out, w.table.name)
	}
	return out
}
