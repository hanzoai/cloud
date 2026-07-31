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
// ONE DURABLE PER TABLE. Each signal gets its own durable consumer filtered to its own
// subject, so event.error draining slowly cannot hold up event.event, and a writer can
// be added or replaced per table.
//
// COMMIT, THEN ACK — in that order, always. The insert must be on disk before the
// message is acknowledged; acking first would lose the fact while the bus believed it
// delivered. An unacked message is redelivered, which is safe because every table is a
// ReplacingMergeTree keyed on the fact id: a redelivered fact collapses on merge instead
// of duplicating. Idempotency is therefore STRUCTURAL — no consumer hand-rolls it, and
// a new consumer cannot forget it.
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
	"fmt"
	"strings"
	"sync"
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

// writer is ONE table's writer: the signal it drains, the columns it inserts, and the
// values one message contributes in exactly that order. Adding a table is adding a row
// here — the consume loop, the ack ordering and the tenancy check are shared.
type writer struct {
	signal  signal
	columns []string
	args    func(message) []any
}

// envelopeColumns is the IDENTICAL 15-column head of every table, in position order.
// ingested_at is deliberately absent: the server stamps it through a column DEFAULT, so
// nothing on the wire can influence when a row expires.
//
// The four writers below all open with this list and with envelopeArgs, so the envelope
// is written down ONCE. If it ever drifts per table, the tables stop being
// UNION ALL-able and the plane loses the property that makes it one plane.
var envelopeColumns = []string{
	"org", "time", "id", "name", "kind", "product",
	"session_id", "distinct_id", "anonymous_id", "person_id",
	"url", "path", "attributes", "el",
}

// elPlaceholder is the `el` tuple's binding: a tuple literal of six placeholders. A
// named tuple accepts a positional tuple of convertible element types, so the six
// annotation values bind as ordinary scalars.
const elPlaceholder = "(?, ?, ?, ?, ?, ?)"

func envelopeArgs(m message) []any {
	el := m.El
	if el == nil {
		el = &messageEl{}
	}
	path := el.Path
	if path == nil {
		path = []string{}
	}
	attrs := m.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	return []any{
		m.Org, m.Time, m.ID, m.Name, m.Kind, m.Product,
		m.Session, m.Distinct, m.Anonymous, m.Person,
		m.URL, m.Path, attrs,
		el.Label, el.Role, el.Testid, el.Name, el.Component, path,
	}
}

// writers is THE SINK SURFACE: one writer per table, and the one list the consumers
// derive from.
//
// event.metric IS ABSENT, DELIBERATELY. The metric table's identity is
// (env, temporality, metric_name, fingerprint) and it has NO org column — it is the
// samples model, moved into this database by RENAME with its 268M rows and its
// fingerprint ordering intact, because rate()/increase() need that physical ordering
// and no time-ordered table can express it. A writer here would therefore have to
// insert a row it cannot attribute to a tenant, and two orgs reporting the same metric
// name and labels would hash to the SAME fingerprint and interleave their samples into
// one series. That is a cross-tenant write, not a missing feature, so the sink refuses
// it rather than performing it.
//
// Metric facts are still normalized and PUBLISHED (event.metric on the bus carries org
// in its envelope), so nothing is lost and no ingest change is needed later: the fix is
// to fold org into the series fingerprint, and then this list grows by one row.
var writers = []writer{
	{
		signal:  signalEvent,
		columns: envelopeColumns,
		args:    envelopeArgs,
	},
	{
		signal: signalError,
		columns: append(append([]string{}, envelopeColumns...),
			"`group`", "message", "class", "site", "handled", "level", "release",
			"environment", "service", "trace_id", "span_id",
			"`frames.function`", "`frames.file`", "`frames.line`", "`frames.column`", "`frames.own`"),
		args: func(m message) []any {
			f := m.Fault
			if f == nil {
				f = &messageFault{}
			}
			fn, file := make([]string, 0, len(f.Frames)), make([]string, 0, len(f.Frames))
			line, col := make([]uint32, 0, len(f.Frames)), make([]uint32, 0, len(f.Frames))
			own := make([]bool, 0, len(f.Frames))
			for _, fr := range f.Frames {
				fn, file = append(fn, fr.Function), append(file, fr.File)
				line, col = append(line, fr.Line), append(col, fr.Column)
				own = append(own, fr.Own)
			}
			return append(envelopeArgs(m),
				f.Group, f.Message, f.Class, f.Site, f.Handled, f.Level, f.Release,
				f.Environment, f.Service, f.Trace, f.Span,
				fn, file, line, col, own)
		},
	},
	{
		signal: signalLog,
		columns: append(append([]string{}, envelopeColumns...),
			"service", "severity_text", "severity_number", "body", "trace_id", "span_id", "resource"),
		args: func(m message) []any {
			r := m.Record
			if r == nil {
				r = &messageRecord{}
			}
			return append(envelopeArgs(m),
				r.Service, r.Severity, r.Number, r.Body, r.Trace, r.Span, r.Resource)
		},
	},
	{
		signal: signalSpan,
		columns: append(append([]string{}, envelopeColumns...),
			"service", "trace_id", "span_id", "parent", "duration", "status"),
		args: func(m message) []any {
			s := m.Span
			if s == nil {
				s = &messageSpan{}
			}
			return append(envelopeArgs(m), s.Service, s.Trace, s.ID, s.Parent, s.Duration, s.Status)
		},
	},
}

// statement renders this writer's INSERT: the fixed column list (never caller input)
// and one placeholder per bound value. `el` is the one column whose binding is a tuple
// of six, so the placeholder list is built from the columns rather than counted.
func (w writer) statement() string {
	ph := make([]string, 0, len(w.columns))
	for _, c := range w.columns {
		if c == "el" {
			ph = append(ph, elPlaceholder)
			continue
		}
		ph = append(ph, "?")
	}
	return "INSERT INTO " + w.signal.table() + " (" + strings.Join(w.columns, ", ") + ")" +
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
	return warehouseExec(ctx, w.statement(), w.args(m)...)
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
	s.log.Info("event sink started", "stream", stream, "tables", tableNames())
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
	for _, w := range writers {
		w := w
		durable := plane + "-" + string(w.signal)
		if _, err := cl.CreateConsumer(ctx, stream, &infra.ConsumerConfig{
			Name:          durable,
			Durable:       durable,
			Description:   "land " + w.signal.subject() + " in " + w.signal.table(),
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
			send(cl.ConsumeMessages(ctx, stream, durable, func(sm *infra.StreamMessage) error {
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
	if err != nil {
		s.log.Warn("event sink: undecodable message — acked and dropped",
			"subject", sm.Subject, "err", err)
		return nil
	}
	if !warehouseReady() {
		return fmt.Errorf("warehouse not ready")
	}
	if err := w.write(ctx, m); err != nil {
		s.log.Warn("event sink: insert failed — will redeliver",
			"table", w.signal.table(), "id", m.ID, "err", err)
		return err
	}
	return nil
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

func tableNames() []string {
	out := make([]string, len(writers))
	for i, w := range writers {
		out[i] = w.signal.table()
	}
	return out
}
