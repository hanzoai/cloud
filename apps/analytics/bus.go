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

// bus.go — THE CONTAINER. fact.go says what an event IS; this file moves it.
//
// The door accepts, normalizes, authorizes and enriches FIRST, and only then
// publishes. Fan-out happens on the bus, never in the handler:
//
//	cloud service --ZAP/UDS--> the event door --publish--> event.<signal> --> EVENT
//	    -> the warehouse writer      -> error grouping
//	    -> the alert engine          -> session/replay
//	    -> live dashboards           -> webhook/export consumers
//
// Adding a consumer must not require touching the ingest path. That is the point.
//
// LIMITS, NOT WORKQUEUE. The stream retains by age and size. WorkQueue would remove a
// message as soon as ANY consumer took it, so of the six independent consumers above
// five would silently never see the event — each needs its own copy, which is exactly
// what a limits-retention stream plus one durable per consumer gives.
//
// SUBJECTS CARRY TAXONOMY, NOT IDENTITY. The subject is event.error, never
// event.<org>.error. org lives in the signed envelope, where it is authorization AND
// data. A tenant in the subject would make every wildcard subscription (event.>,
// event.error) unstable as tenants come and go, and would leak the tenant list to
// anyone who can list subjects.
//
// PUBLISH IS THE COMMIT POINT, so it is SYNCHRONOUS: PublishToStream returns a PubAck
// only after JetStream has the message on file storage. An "accepted" receipt therefore
// means durable, not "buffered in this process". A fire-and-forget publish would turn
// every 200 into a maybe.
//
// WHY A SOCKET AT ALL, WHEN THE SERVER IS IN THIS PROCESS. Locality picks the
// transport: same process is a direct call, same machine is ZAP over UDS, distributed
// is NATS, DURABLE is JetStream. This is the durable rung. Publishing to the log is not
// an RPC to another plugin — it is the write that makes the fact survive this process,
// which is precisely what no direct call can do.
package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/commerce/infra"
	"github.com/zap-proto/zip"
)

// stream is the JetStream stream every signal lands on, and subjects is the ONE
// wildcard it binds. Upper-case is the NATS convention for a stream name; it is the
// same word as the database and the subject root (plane, fact.go) — one name, three
// layers.
var stream = strings.ToUpper(plane)

// subjects is the stream's binding: every signal, present and future. A new signal is
// a new constant in fact.go and a consumer that filters for it — never a stream edit.
var subjects = []string{plane + ".>"}

// Retention. The stream is a HAND-OFF to the consumers, not the system of record — the
// warehouse is — so it holds enough for a consumer to be down, redeployed or added and
// still catch up, and no more.
const (
	streamAge   = 72 * time.Hour
	streamBytes = 8 << 30 // 8 GiB, the pod's ceiling for undrained facts
)

// busURLEnv overrides where the plane's bus lives; busPortEnv is the port the embedded
// server (apps/pubsub) bound. Reading the SAME variable that server reads is what keeps
// the two from disagreeing about the port after an ops change.
const (
	busURLEnv  = "CLOUD_EVENT_NATS_URL"
	busPortEnv = "CLOUD_PUBSUB_PORT"
)

// publishTimeout bounds ONE batch's publish so an ingest request cannot hang on a
// wedged bus; the caller gets an honest 503 instead.
const publishTimeout = 5 * time.Second

// busURL is where this process reaches the plane's bus. It defaults to the LOOPBACK
// address of the embedded server this same binary runs (apps/pubsub), because that
// server fails boot closed — a cloud that is up HAS a bus — so there is no
// configuration a deployment must remember to set for ingest to work.
func busURL() string {
	if u := strings.TrimSpace(os.Getenv(busURLEnv)); u != "" {
		return u
	}
	port := strings.TrimSpace(os.Getenv(busPortEnv))
	if port == "" {
		port = "4222"
	}
	return "nats://" + net.JoinHostPort("127.0.0.1", port)
}

// bus holds the process's ONE connection to the plane, dialed on first use and reused.
// It is a struct rather than a bare package var so the connect is guarded by a single
// mutex: a burst of concurrent first requests dials once, not once per request.
type bus struct {
	mu     sync.Mutex
	client *infra.PubSubClient
	ready  bool
}

var conn = &bus{}

// errBusUnavailable is what the door answers with when the plane cannot take a fact.
// It is a 503 and never a 200: silently dropping an accepted event is the one failure
// this design exists to make impossible.
var errBusUnavailable = errors.New("event plane unavailable")

// connect returns the live client, dialing and ensuring the stream on first use. It
// re-dials after a failure rather than latching, so a bus that was briefly down heals
// without a restart.
func (b *bus) connect(ctx context.Context) (*infra.PubSubClient, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ready && b.client != nil {
		return b.client, nil
	}
	cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{
		URL:             busURL(),
		Name:            plane + "-ingest",
		EnableJetStream: true,
		MaxReconnects:   -1,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: connect %s: %v", errBusUnavailable, busURL(), err)
	}
	if err := cl.EnsureStream(ctx, &infra.StreamConfig{
		Name:        stream,
		Description: "the event plane: every signal, one log",
		Subjects:    subjects,
		// LimitsPolicy — every consumer gets its own copy. See the file header.
		Retention: infra.RetentionLimits,
		Storage:   infra.StorageFile,
		MaxAge:    streamAge,
		MaxBytes:  streamBytes,
	}); err != nil {
		_ = cl.Close()
		return nil, fmt.Errorf("%w: ensure stream %s: %v", errBusUnavailable, stream, err)
	}
	b.client, b.ready = cl, true
	return cl, nil
}

// drop marks the connection dead so the next publish re-dials.
func (b *bus) drop() {
	b.mu.Lock()
	b.ready = false
	b.mu.Unlock()
}

// close releases the connection on graceful shutdown.
func (b *bus) close() {
	b.mu.Lock()
	cl := b.client
	b.client, b.ready = nil, false
	b.mu.Unlock()
	if cl != nil {
		_ = cl.Close()
	}
}

// publish is the ingest path's ONE seam onto the bus, and the ONLY way a fact leaves
// this package. It is a package var for exactly the reason resolveKeyOrg is: production
// is always publishToStream, and a test substitutes it to read back the facts a lane
// actually produced — the tenant stamp and the route are what matter there, and neither
// is observable through a status code.
var publish = publishToStream

// publishToStream publishes every fact to its own subject, synchronously.
//
// ALL-OR-NOTHING IS NOT AVAILABLE and is not claimed: a batch is many messages, and
// JetStream has no cross-subject transaction. A partial failure returns the error, so
// the caller reports 503 and the client retries the whole batch — which is safe because
// every consumer is idempotent on the fact id (warehouse.go), so a redelivered or
// re-sent fact collapses rather than duplicating.
func publishToStream(ctx context.Context, facts []fact) error {
	if len(facts) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	cl, err := conn.connect(ctx)
	if err != nil {
		return busErr(err)
	}
	for i := range facts {
		if _, err := cl.PublishJSONToStream(ctx, facts[i].signal.subject(), wire(facts[i])); err != nil {
			conn.drop()
			return busErr(fmt.Errorf("%w: publish %s: %v", errBusUnavailable, facts[i].signal.subject(), err))
		}
	}
	return nil
}

// busErr maps a plane failure to the honest HTTP status. It is ALWAYS 503 and never a
// 200 with a short count: the fact was admitted and could not be made durable, which is
// the caller's business.
func busErr(err error) error {
	return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
}

// closeBus releases the ingest connection. Cloud calls it on graceful shutdown.
func closeBus() { conn.close() }

// ── the message ──────────────────────────────────────────────────────────────

// message is a fact ON THE WIRE — the container, JSON, versionless. It exists so the
// bus payload is an explicit, reviewable contract rather than whatever the unexported
// struct fields happen to be called this week, and so a consumer in another language
// can read the plane.
//
// The field names are the COLUMN names, so a reader never has to hold two vocabularies
// at once: what you see on the subject is what you query in the table.
type message struct {
	Signal string `json:"signal"`

	// The envelope — identical for every signal.
	Org        string            `json:"org"`
	Time       time.Time         `json:"time"`
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind,omitempty"`
	Product    string            `json:"product,omitempty"`
	Session    string            `json:"session_id,omitempty"`
	Distinct   string            `json:"distinct_id,omitempty"`
	Anonymous  string            `json:"anonymous_id,omitempty"`
	Person     string            `json:"person_id,omitempty"`
	URL        string            `json:"url,omitempty"`
	Path       string            `json:"path,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	El         *messageEl        `json:"el,omitempty"`

	// Exactly one of these is set, chosen by Signal.
	Fault  *messageFault  `json:"fault,omitempty"`
	Record *messageRecord `json:"record,omitempty"`
	Span   *messageSpan   `json:"span,omitempty"`
	Sample *messageSample `json:"sample,omitempty"`
}

type messageEl struct {
	Label     string   `json:"label,omitempty"`
	Role      string   `json:"role,omitempty"`
	Testid    string   `json:"testid,omitempty"`
	Name      string   `json:"name,omitempty"`
	Component string   `json:"component,omitempty"`
	Path      []string `json:"path,omitempty"`
}

type messageFrame struct {
	Function string `json:"function,omitempty"`
	File     string `json:"file,omitempty"`
	Line     uint32 `json:"line,omitempty"`
	Column   uint32 `json:"column,omitempty"`
	Own      bool   `json:"own"`
}

type messageFault struct {
	Group       string         `json:"group"`
	Message     string         `json:"message,omitempty"`
	Class       string         `json:"class,omitempty"`
	Site        string         `json:"site,omitempty"`
	Handled     bool           `json:"handled"`
	Level       string         `json:"level,omitempty"`
	Release     string         `json:"release,omitempty"`
	Environment string         `json:"environment,omitempty"`
	Service     string         `json:"service,omitempty"`
	Trace       string         `json:"trace_id,omitempty"`
	Span        string         `json:"span_id,omitempty"`
	Frames      []messageFrame `json:"frames,omitempty"`
}

type messageRecord struct {
	Service  string `json:"service,omitempty"`
	Severity string `json:"severity_text,omitempty"`
	Number   uint8  `json:"severity_number,omitempty"`
	Body     string `json:"body,omitempty"`
	Trace    string `json:"trace_id,omitempty"`
	Span     string `json:"span_id,omitempty"`
	Resource uint64 `json:"resource,omitempty"`
}

type messageSpan struct {
	Service  string `json:"service,omitempty"`
	Trace    string `json:"trace_id,omitempty"`
	ID       string `json:"span_id,omitempty"`
	Parent   string `json:"parent,omitempty"`
	Duration uint64 `json:"duration,omitempty"`
	Status   string `json:"status,omitempty"`
}

type messageSample struct {
	Metric string            `json:"metric"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// decodeMessage reads a published message back off the bus. It is the ONE decode of
// the plane's own contract — the inverse of wire — so a consumer never parses the
// payload by hand.
func decodeMessage(data []byte) (message, error) {
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		return message{}, err
	}
	if strings.TrimSpace(m.Signal) == "" {
		return message{}, errors.New("message names no signal")
	}
	return m, nil
}

// wire renders a fact as the message that carries it. The two are separate types on
// purpose: `fact` is what this package reasons about, `message` is the published
// contract, and letting one drift is a schema change rather than a rename.
func wire(f fact) message {
	m := message{
		Signal:     string(f.signal),
		Org:        f.org,
		Time:       f.time,
		ID:         f.id,
		Name:       f.name,
		Kind:       f.kind,
		Product:    f.product,
		Session:    f.session,
		Distinct:   f.distinct,
		Anonymous:  f.anonymous,
		Person:     f.person,
		URL:        f.url,
		Path:       f.path,
		Attributes: f.attributes,
	}
	if !f.el.empty() {
		m.El = &messageEl{
			Label: f.el.label, Role: f.el.role, Testid: f.el.testid,
			Name: f.el.name, Component: f.el.component, Path: f.el.path,
		}
	}
	if f.fault != nil {
		frames := make([]messageFrame, 0, len(f.fault.frames))
		for _, fr := range f.fault.frames {
			frames = append(frames, messageFrame{
				Function: fr.function, File: fr.file, Line: fr.line, Column: fr.column, Own: fr.own,
			})
		}
		m.Fault = &messageFault{
			Group: f.fault.group, Message: f.fault.message, Class: f.fault.class,
			Site: f.fault.site, Handled: f.fault.handled, Level: f.fault.level,
			Release: f.fault.release, Environment: f.fault.environment,
			Service: f.fault.service, Trace: f.fault.trace, Span: f.fault.span,
			Frames: frames,
		}
	}
	if f.record != nil {
		m.Record = &messageRecord{
			Service: f.record.service, Severity: f.record.severity, Number: f.record.number,
			Body: f.record.body, Trace: f.record.trace, Span: f.record.span,
			Resource: f.record.resource,
		}
	}
	if f.span != nil {
		m.Span = &messageSpan{
			Service: f.span.service, Trace: f.span.trace, ID: f.span.id,
			Parent: f.span.parent, Duration: f.span.duration, Status: f.span.status,
		}
	}
	if f.sample != nil {
		m.Sample = &messageSample{Metric: f.sample.metric, Value: f.sample.value, Labels: f.sample.labels}
	}
	return m
}
