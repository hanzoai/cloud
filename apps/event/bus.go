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

package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/pubsub"
	"github.com/hanzoai/commerce/infra"
	"github.com/hanzoai/pubsub-go/jetstream"
	"github.com/zap-proto/zip"
)

// EventStream is the JetStream stream every signal lands on, and EventSubjects is the
// ONE wildcard it binds. Upper-case is the NATS convention for a stream name; it is the
// same word as the database and the subject root (plane, fact.go) — one name, three
// layers.
//
// They are EXPORTED because JetStream enforces single ownership: a second stream
// binding event.> is refused with "subjects overlap with an existing stream", which
// takes down whichever subsystem loses the race. So a consumer (apps/webhooks) names
// THIS stream rather than declaring one of its own — the compiler is what keeps the
// two from drifting into an outage.
var EventStream = strings.ToUpper(plane)

// EventSubjects is the stream's binding: every signal, present and future. A new signal
// is a new constant in fact.go and a consumer that filters for it — never a stream edit.
var EventSubjects = []string{plane + ".>"}

// EventOrgKey is the ONE field an envelope on this plane names its tenant with. Every
// consumer resolves the org by reading it, so a publisher that spelled it differently
// would deliver to nobody rather than fail loudly — which is precisely why the spelling
// is a constant here, in the plane's own package, and not a string literal at each end.
const EventOrgKey = "org"

// EventSignalKey is the field that says WHICH VOCABULARY a message on this plane
// speaks, and it exists because two of them share it.
//
// A fact (message, below) carries it; the subscriber-facing EventEnvelope does not.
// The two are separate contracts by design, and their subject spaces WOULD have kept
// them apart — event.<signal> is a closed set of five, event.<folded product name> is
// whatever a caller names an event — except that the fold maps a product event named
// "$error" straight onto event.error, which is precisely the subject the fact plane's
// error writer drains, and "$error" is what a browser error with no name of its own is
// called. An open namespace minting a reserved token is the collision; the fold's
// mapping is a PUBLISHED contract (orgs subscribe to it) and so cannot be the thing
// that moves.
//
// So the discriminator is the BODY, not the subject: each consumer takes the
// vocabulary it speaks and leaves the other alone. The warehouse lands facts and
// ignores envelopes (warehouse.go); webhooks delivers envelopes and ignores facts
// (apps/webhooks). It is a constant HERE, in the plane's own package, for the same
// reason EventOrgKey is — a consumer that spelled it itself would silently take the
// wrong half.
const EventSignalKey = "signal"

// Retention. The stream is a HAND-OFF to the consumers, not the system of record — the
// warehouse is — so it holds enough for a consumer to be down, redeployed or added and
// still catch up, and no more.
const (
	streamAge   = 72 * time.Hour
	streamBytes = 8 << 30 // 8 GiB, the pod's ceiling for undrained facts
)

// publishTimeout bounds ONE batch's publish so an ingest request cannot hang on a
// wedged bus; the caller gets an honest 503 instead.
const publishTimeout = 5 * time.Second

// busURL is where this process reaches the plane's bus: the ONE address apps/pubsub
// exports, which every app in this binary dials. It is not analytics' knob to own — a
// private variable here would be one more place for a deployment to point half the
// platform at a different bus. See the apps/pubsub package doc for the knob itself.
func busURL() string { return pubsub.URL() }

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
	if err := EnsureEventStream(ctx, cl); err != nil {
		_ = cl.Close()
		return nil, fmt.Errorf("%w: ensure stream %s: %v", errBusUnavailable, EventStream, err)
	}
	b.client, b.ready = cl, true
	return cl, nil
}

// planeReady answers the ONE question a liveness probe has to ask about the write
// path: would an ingest succeed right now?
//
// It answers it by WALKING THE INGEST PATH ITSELF — the same connect, the same
// stream, the same names — because a probe that asks its own private version of the
// question is a probe that can disagree with production, and it did: /v1/event/health
// reported ok on datastore connectivity alone while 100% of writes 503'd on the bus,
// so a total ingest outage was invisible to monitoring. A second notion of "healthy"
// is what made that possible, so there is not one here.
//
// It re-reads the STREAM on every probe rather than trusting the cached connection,
// because the cache says a client was built once, not that the plane is still there —
// the stream is exactly what went missing. On any failure it drops the cached
// connection, so the probe also heals: the next publish re-dials and re-ensures
// instead of riding a client that is known bad.
func planeReady(ctx context.Context) error {
	cl, err := conn.connect(ctx)
	if err != nil {
		return err
	}
	nc := cl.Conn()
	if nc == nil || !nc.IsConnected() {
		conn.drop()
		return fmt.Errorf("%w: not connected to %s", errBusUnavailable, busURL())
	}
	js := cl.JetStream()
	if js == nil {
		conn.drop()
		return fmt.Errorf("%w: jetstream not enabled on %s", errBusUnavailable, busURL())
	}
	if _, err := js.Stream(ctx, EventStream); err != nil {
		conn.drop()
		return fmt.Errorf("%w: stream %s: %v", errBusUnavailable, EventStream, err)
	}
	return nil
}

// EnsureEventStream RECONCILES the event plane to the configuration this package chose
// for it, creating it when absent. It is the ONE declaration of that configuration
// anywhere in the platform.
//
// It is EXPORTED so a consumer can make sure the plane exists before binding a durable
// to it — a consumer that starts before the first ingest would otherwise find no stream
// — WITHOUT holding a second copy of the config. Whoever calls it first applies it, and
// because there is only one config, it does not matter who that is.
//
// RECONCILES, NOT CREATE-IF-MISSING, and the difference is the whole point. A
// create-if-missing ensure returns success the moment the stream exists and never looks
// at what it looks like, so every constant below was decorative on a live deployment:
// raising the ceiling, shortening the hand-off window, or fixing the discard policy
// changed the source and nothing else, forever. CreateOrUpdateStream applies them.
//
// DISCARD NEW, NOT OLD, which is the config that was silently wrong. On a full stream
// the JetStream default (DiscardOld) evicts the OLDEST messages to make room — and the
// oldest messages on a hand-off stream are precisely the ones no consumer has drained
// yet. That is data the door already answered 200 for, deleted to make room for data the
// door has not answered for yet, with no error at either end. DiscardNew inverts it: a
// full stream REFUSES THE PUBLISH, so publishToStream fails, the door answers 503, and
// the client retries — backpressure the caller can see instead of loss nobody can. The
// ceiling stops being a silent shredder and becomes what it reads like: a limit.
//
// MaxAge still expires drained-or-not after streamAge. That is the deliberate hand-off
// window, not a capacity failure, and a consumer down for three days is an outage to
// alarm on rather than a case to size storage for.
//
// It goes through cl.JetStream() rather than infra.EnsureStream because the wrapper
// models neither operation this needs: its StreamConfig has no Discard field at all,
// and it returns early the moment the stream exists. That accessor is exported for
// exactly this — a caller that needs a capability the thin wrapper does not carry —
// and this package is the stream's owner, so the full config belongs here in the
// vocabulary that can express it. The wrapper's own create-if-missing behavior is a
// platform-wide defect (every other stream in the fleet is declared through it and is
// equally undeclarable after creation); fixing it there is a change to hanzoai/commerce
// and to every stream owner at once, which is not this package's to make.
// A STREAM IS NOT RENAMEABLE, which is the failure this function has to survive.
// CreateOrUpdateStream reconciles a stream by NAME; JetStream binds subjects by
// OWNERSHIP. So when the plane's name changes — EVENTS to EVENT, the day
// apps/webhooks stopped declaring a plane it only consumes — the old stream keeps
// event.> and the new name can never bind it. Every publish then 503s, forever,
// and no restart, redeploy or rollback clears it: the store is durable, so the
// stale stream outlives the code that made it. That is not a race that resolves,
// it is a deadlock that needs a migration, and a plane that cannot migrate itself
// is a plane that takes ingest down until somebody notices.
//
// So the ensure RETIRES the earlier generation. See retire for what it will and
// will not remove.
func EnsureEventStream(ctx context.Context, cl *infra.PubSubClient) error {
	js := cl.JetStream()
	if js == nil {
		return fmt.Errorf("jetstream not enabled on %s", busURL())
	}
	_, err := js.CreateOrUpdateStream(ctx, eventStream)
	if !overlaps(err) {
		return err
	}
	if err := retire(ctx, js); err != nil {
		return err
	}
	_, err = js.CreateOrUpdateStream(ctx, eventStream)
	return err
}

// errSubjectOverlap is JetStream's refusal to bind subjects another stream already
// holds. The client names every code it can return except this one, so the number
// is written out here rather than matched on the description — the description is
// prose and not part of the wire contract.
const errSubjectOverlap jetstream.ErrorCode = 10065

// overlaps reports whether err is exactly that refusal.
func overlaps(err error) bool {
	var api *jetstream.APIError
	return errors.As(err, &api) && api.ErrorCode == errSubjectOverlap
}

// retire removes the stream that holds this plane's subjects under a name that is
// no longer the plane's, so the canonical name can bind them.
//
// IT REFUSES TO DESTROY DATA, and that is the whole of its judgement. A stream
// carrying messages is somebody's undrained hand-off whatever it is called, so
// retire does not touch it — it returns an error NAMING the stream, its subjects
// and its depth, which is the report an operator can act on and the opaque
// "subjects overlap with an existing stream" never was. Only an EMPTY stream is
// removed, because an empty stream is a name and nothing else.
//
// IT NEVER TOUCHES A TENANT'S STREAM. A tenant cannot reach these subjects in the
// first place — the tenant door roots every subject it accepts at pub.<org>.
// (apps/pubsub) — so this cannot trigger today. It is here because the cost of
// being wrong is a customer's stream, and the check is one comparison: if the
// door's rooting ever regressed, this refuses instead of deleting.
func retire(ctx context.Context, js jetstream.JetStream) error {
	for _, subject := range EventSubjects {
		name, err := js.StreamNameBySubject(ctx, subject)
		if err != nil {
			return fmt.Errorf("%s is held by another stream, which could not be identified: %w", subject, err)
		}
		if name == EventStream {
			continue
		}
		st, err := js.Stream(ctx, name)
		if err != nil {
			return fmt.Errorf("stream %s holds %s and could not be read: %w", name, subject, err)
		}
		info, err := st.Info(ctx)
		if err != nil {
			return fmt.Errorf("stream %s holds %s and its state could not be read: %w", name, subject, err)
		}
		if strings.HasPrefix(name, pubsub.TenantPrefix) {
			return fmt.Errorf("stream %s is a TENANT stream and holds %s, which belongs to the platform event plane; "+
				"refusing to remove it — the tenant door must not be able to bind this subject", name, subject)
		}
		if info.State.Msgs > 0 {
			return fmt.Errorf("stream %s holds %s (subjects %v) with %d undrained message(s); "+
				"refusing to remove it — drain or delete it to release the subject to %s",
				name, subject, info.Config.Subjects, info.State.Msgs, EventStream)
		}
		if err := js.DeleteStream(ctx, name); err != nil {
			return fmt.Errorf("retiring empty stream %s, which held %s: %w", name, subject, err)
		}
	}
	return nil
}

// eventStream is that configuration as a VALUE, so what the plane is declared to be can
// be read — and asserted — without a bus to apply it to.
var eventStream = jetstream.StreamConfig{
	Name:        EventStream,
	Description: "the event plane: every signal, one log",
	Subjects:    EventSubjects,
	// LimitsPolicy — every consumer gets its own copy. See the file header.
	Retention: jetstream.LimitsPolicy,
	Storage:   jetstream.FileStorage,
	MaxAge:    streamAge,
	MaxBytes:  streamBytes,
	Discard:   jetstream.DiscardNew,
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

// ── the accepted-batch fan-out ───────────────────────────────────────────────

// EventEnvelope is what an ACCEPTED PRODUCT EVENT looks like on the plane: the
// subscriber-facing projection of a SinkEvent, carrying the commerce and identity
// fields a downstream integration converts on.
//
// Org is FIRST and it is the tenant, spelled EventOrgKey — the delivery engine
// (apps/webhooks) resolves the subscriber's org from it, and an envelope without one is
// delivered to nobody. It is stamped from the SERVER-resolved tenant, never from the
// wire, on the same terms as fact.org.
//
// It is a SEPARATE type from message, and deliberately: message is the warehouse's
// contract (its field names are the column names), this is the SUBSCRIBER's contract,
// and folding them together would make a webhook payload change every time a table
// gains a column. What they share is the one thing that must be shared — the tenant key.
type EventEnvelope struct {
	Org         string         `json:"org"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	DistinctID  string         `json:"distinct_id,omitempty"`
	AnonymousID string         `json:"anonymous_id,omitempty"`
	Time        time.Time      `json:"time"`
	URL         string         `json:"url,omitempty"`
	Path        string         `json:"path,omitempty"`
	Referrer    string         `json:"referrer,omitempty"`
	Revenue     float64        `json:"revenue,omitempty"`
	Currency    string         `json:"currency,omitempty"`
	ProductID   string         `json:"product_id,omitempty"`
	Quantity    uint32         `json:"quantity,omitempty"`
	Properties  map[string]any `json:"properties,omitempty"`
}

// subjectFor maps a canonical event name onto its bus subject. Names are caller-chosen
// strings; a NATS subject token is not — so the name is folded to lowercase, runs of
// anything outside [a-z0-9_] collapse to one '_', the canonical '$' prefix drops, and an
// empty result (or one that would collide with wildcard grammar) lands on "custom".
// Bounded so a hostile name cannot mint unbounded subject cardinality on the stream.
//
//	$pageview → event.pageview, $error → event.error,
//	signup_completed → event.signup_completed
//
// This is the grammar an org subscribes to ("send me event.signup_completed"), so it is
// a PUBLISHED contract: fold rules may gain cases, never change an existing mapping.
func subjectFor(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "$")
	var b strings.Builder
	pendingSep := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(r)
		default:
			pendingSep = true
		}
	}
	token := b.String()
	if token == "" {
		token = "custom"
	}
	if len(token) > maxSubjectToken {
		token = token[:maxSubjectToken]
	}
	return plane + "." + token
}

// maxSubjectToken bounds one folded name, so subject cardinality on the stream stays a
// function of the product's vocabulary and not of what a caller can type.
const maxSubjectToken = 48

// PublishEvents puts an accepted batch on the plane, one publish per event, under the
// batch's SERVER-resolved org. It is the ONE way a product event reaches the platform
// bus, and it lives HERE because this package owns the plane: the stream, the subject
// grammar, and the envelope are one decision, and a consumer that also published would
// be a second owner of all three.
//
// FAIL-SOFT, unlike the fact path above. The batch is already COMMITTED as facts
// (publish, above) by the time this runs, so a bus that is down for this second
// vocabulary loses only envelope deliveries, never data — the ingest already answered
// 200 for the durable copy. It is called detached (forward.go), so a slow bus costs a
// goroutine and never an ingest.
func PublishEvents(org string, evs []SinkEvent) {
	if org == "" || len(evs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	cl, err := conn.connect(ctx)
	if err != nil {
		return // bus down: the fact commit is the durable copy
	}
	for _, e := range evs {
		body, err := json.Marshal(EventEnvelope{
			Org:         org,
			ID:          e.MessageID,
			Name:        e.Name,
			DistinctID:  e.DistinctID,
			AnonymousID: e.AnonymousID,
			Time:        e.Time,
			URL:         e.URL,
			Path:        e.Path,
			Referrer:    e.Referrer,
			Revenue:     e.Revenue,
			Currency:    e.Currency,
			ProductID:   e.ProductID,
			Quantity:    e.Quantity,
			Properties:  e.Properties,
		})
		if err != nil {
			continue
		}
		if _, err := cl.PublishToStream(ctx, subjectFor(e.Name), body); err != nil {
			conn.drop() // re-dial on the next batch
			return
		}
	}
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
// at once: what you see on the subject is what you query in the table. That promise is
// why this is FLAT. It carried four optional sub-bodies while there were four tables to
// carry them into; with one table a body is a subset of columns, and nesting it would
// have been a second shape of the row that every consumer then had to translate.
//
// `sample` stays a pointer, alone, because a measurement is genuinely a different
// value: it has a float and no identity, and it lands in the other table.
type message struct {
	// ── spine ────────────────────────────────────────────────────────────────
	Signal string    `json:"signal"`
	Org    string    `json:"org"`
	Time   time.Time `json:"time"`
	ID     string    `json:"id"`

	// ── what ─────────────────────────────────────────────────────────────────
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"`
	Message  string `json:"message,omitempty"`
	Severity uint8  `json:"severity,omitempty"`
	Duration uint64 `json:"duration,omitempty"`

	// ── where ────────────────────────────────────────────────────────────────
	Product string `json:"product,omitempty"`
	Env     string `json:"env,omitempty"`
	Service string `json:"service,omitempty"`
	Release string `json:"release,omitempty"`
	URL     string `json:"url,omitempty"`
	Path    string `json:"path,omitempty"`

	// ── who ──────────────────────────────────────────────────────────────────
	Person    string            `json:"person_id,omitempty"`
	Distinct  string            `json:"distinct_id,omitempty"`
	Anonymous string            `json:"anonymous_id,omitempty"`
	Groups    map[string]string `json:"groups,omitempty"`

	// ── correlation ──────────────────────────────────────────────────────────
	Session  string `json:"session_id,omitempty"`
	Trace    string `json:"trace_id,omitempty"`
	Span     string `json:"span_id,omitempty"`
	Parent   string `json:"parent,omitempty"`
	Resource string `json:"resource,omitempty"`

	// ── open ─────────────────────────────────────────────────────────────────
	Attributes map[string]string `json:"attributes,omitempty"`
	El         *messageEl        `json:"el,omitempty"`

	// ── error ────────────────────────────────────────────────────────────────
	Issue   string         `json:"issue,omitempty"`
	Class   string         `json:"class,omitempty"`
	Origin  string         `json:"origin,omitempty"`
	Handled bool           `json:"handled,omitempty"`
	Frames  []messageFrame `json:"frames,omitempty"`

	// ── span ─────────────────────────────────────────────────────────────────
	Status string `json:"status,omitempty"`

	// ── clip ─────────────────────────────────────────────────────────────────
	Object string `json:"object,omitempty"`
	Bytes  uint64 `json:"bytes,omitempty"`

	// ── the other grain ──────────────────────────────────────────────────────
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

type messageSample struct {
	Metric string            `json:"metric"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// errNotAFact says the payload is well-formed JSON that simply is not a fact — it names
// no signal, so it belongs to the other vocabulary on this plane (EventSignalKey). It is
// a SENTINEL and not a bare error because the difference is the difference between a
// message this consumer should ignore and a fact it has just LOST: counting an
// EventEnvelope as an unlandable fact would make the loss counter read non-zero on every
// browser error and mean nothing at all (warehouse.go).
var errNotAFact = errors.New("message names no signal")

// decodeMessage reads a published message back off the bus. It is the ONE decode of
// the plane's own contract — the inverse of wire — so a consumer never parses the
// payload by hand.
func decodeMessage(data []byte) (message, error) {
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		return message{}, err
	}
	if strings.TrimSpace(m.Signal) == "" {
		return message{}, errNotAFact
	}
	return m, nil
}

// wire renders a fact as the message that carries it. The two are separate types on
// purpose: `fact` is what this package reasons about, `message` is the published
// contract, and letting one drift is a schema change rather than a rename.
func wire(f fact) message {
	m := message{
		Signal: string(f.signal),
		Org:    f.org,
		Time:   f.time,
		ID:     f.id,

		Name:     f.name,
		Kind:     f.kind,
		Message:  f.message,
		Severity: f.severity,
		Duration: f.duration,

		Product: f.product,
		Env:     f.env,
		Service: f.service,
		Release: f.release,
		URL:     f.url,
		Path:    f.path,

		Person:    f.person,
		Distinct:  f.distinct,
		Anonymous: f.anonymous,
		Groups:    f.groups,

		Session:  f.session,
		Trace:    f.trace,
		Span:     f.span,
		Parent:   f.parent,
		Resource: f.resource,

		Attributes: f.attributes,

		Issue:   f.issue,
		Class:   f.class,
		Origin:  f.origin,
		Handled: f.handled,

		Status: f.status,

		Object: f.object,
		Bytes:  f.bytes,
	}
	if !f.el.empty() {
		m.El = &messageEl{
			Label: f.el.label, Role: f.el.role, Testid: f.el.testid,
			Name: f.el.name, Component: f.el.component, Path: f.el.path,
		}
	}
	if len(f.frames) > 0 {
		m.Frames = make([]messageFrame, 0, len(f.frames))
		for _, fr := range f.frames {
			m.Frames = append(m.Frames, messageFrame{
				Function: fr.function, File: fr.file, Line: fr.line, Column: fr.column, Own: fr.own,
			})
		}
	}
	if f.sample != nil {
		m.Sample = &messageSample{Metric: f.sample.metric, Value: f.sample.value, Labels: f.sample.labels}
	}
	return m
}
