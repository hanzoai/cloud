package webhooks

// dispatch.go — the ONE delivery engine. A durable JetStream consumer on the platform
// bus reads EVERY event on the configured stream(s), resolves the emitting org from the
// event envelope, matches it against THAT org's active subscriptions ONLY (physical
// per-org store ⇒ no cross-tenant delivery), and hands each match to a bounded worker
// pool that POSTs it with a fresh HMAC signature and a bounded retry ladder.
//
// FIRE-AND-FORGET FROM THE CONSUMER. The JetStream message is ACKed as soon as the
// matched deliveries are QUEUED (handed to the worker pool) — never after the retries
// finish. A slow subscriber occupies a worker, never the consumer, so one bad endpoint
// can never stall the bus. Concurrency is bounded by the fixed worker pool; the queue
// applies backpressure under true overload (the consumer waits for a slot rather than
// spawning unbounded goroutines or dropping events).
//
// NO OPS KNOB, and no publish. This subsystem is a pure CONSUMER: it dials the ONE bus
// apps/pubsub exports (pubsub.URL — see that package's doc), and it consumes streams
// that OTHER subsystems own and create. It used to carry a knob of its own that defaulted
// to OFF, which meant a deployment that set nothing delivered no webhooks at all while
// every other app on the same bus worked; the embedded bus always serves and fails boot
// closed, so "no bus configured" was never a real state to have an opt-in for.
//
// ONE OWNER PER STREAM. Each consumed stream is created by the subsystem that DEFINES it
// (streamSource.ensure), never by a config copy held here. JetStream refuses a second
// stream over the same subjects — "subjects overlap with an existing stream" — so a
// consumer that declared its own name for a plane someone else publishes does not
// diverge quietly: it fails to bind, forever, and delivers nothing on ANY stream.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/analytics"
	"github.com/hanzoai/cloud/apps/pubsub"
	"github.com/hanzoai/commerce/events"
	"github.com/hanzoai/commerce/infra"
	luxlog "github.com/luxfi/log"
)

const (
	// durableName is the shared JetStream durable this subsystem binds per stream:
	// durable + explicit-ack, so multiple cloud replicas load-balance events and each
	// is handled exactly once across restarts.
	durableName = "webhooks-dispatch"
	clientName  = "webhooks-dispatch"

	// The worker pool: numWorkers bounds concurrent in-flight deliveries; queueSize is
	// the buffered hand-off between the consumer and the workers.
	numWorkers = 32
	queueSize  = 1024

	// maxAttempts POSTs per delivery; attemptTimeout bounds one POST.
	maxAttempts    = 3
	attemptTimeout = 10 * time.Second
)

// retryBackoff is the retry ladder (1s → 5s → 25s). retryBackoff[k-1] is the wait
// before retry k: a failed attempt 1 waits retryBackoff[0]=1s, a failed attempt 2 waits
// retryBackoff[1]=5s. The third rung (25s) is the ladder a further retry would use and
// is retained so raising maxAttempts needs no edit here — attempts and ladder are two
// independent knobs.
var retryBackoff = []time.Duration{1 * time.Second, 5 * time.Second, 25 * time.Second}

// streamSource is ONE consumed plane, described entirely by its OWNER's vocabulary: the
// stream's name, the subjects it carries, the field its publisher names the tenant with,
// and the owner's own idempotent constructor. Adding BASE / WORLD / IAM is appending a
// row here, not a rewrite: the org-resolution + match + deliver path is stream-agnostic.
//
// orgKey is per-stream because the platform genuinely has two envelope dialects and this
// is the seam that reads both — commerce says organization_id, the event plane says org.
// Declaring it here (rather than trying keys until one hits) means a stream whose
// publisher renames its tenant field goes red at this table instead of silently
// resolving every event to "" and delivering to nobody.
//
// notMineKey is the same idea one level down. A plane may carry MORE than one
// vocabulary, and this package speaks exactly one of them: the subscriber envelope. The
// event plane also carries the warehouse's facts, whose subjects overlap the envelope
// subjects (a product event named "$error" folds onto event.error, which is also the
// error signal's subject), so subject matching alone cannot tell them apart. A message
// carrying this field is the OTHER vocabulary and is not this package's to deliver.
// Empty ⇒ the stream carries one vocabulary and everything on it is ours.
type streamSource struct {
	stream     string
	subjects   []string
	orgKey     string
	notMineKey string
	ensure     func(context.Context, *infra.PubSubClient) error
}

// streams is the consumed set: COMMERCE (commerce.>), owned by hanzoai/commerce, and the
// canonical event plane (analytics.EventStream, event.>), owned by apps/analytics — which
// publishes it, names it, and configures its retention. Neither is this package's to
// declare; both are this package's to read.
var streams = []streamSource{
	{
		stream:   events.StreamName,
		subjects: events.StreamSubjects,
		orgKey:   commerceOrgKey,
		ensure:   ensureCommerceStream,
	},
	{
		stream:     analytics.EventStream,
		subjects:   analytics.EventSubjects,
		orgKey:     analytics.EventOrgKey,
		notMineKey: analytics.EventSignalKey,
		ensure:     analytics.EnsureEventStream,
	},
}

// commerceOrgKey is the tenant field on a commerce event envelope
// (hanzoai/commerce/events.CommerceEvent.OrganizationID).
const commerceOrgKey = "organization_id"

// ensureCommerceStream is the COMMERCE plane's constructor. It is written out here
// because hanzoai/commerce/events exports the stream's NAME and SUBJECTS but ships no
// constructor — and it is byte-identical to the one apps/catalogsync uses, which is what
// keeps two consumers from racing to create two different COMMERCE streams.
func ensureCommerceStream(ctx context.Context, cl *infra.PubSubClient) error {
	return cl.EnsureStream(ctx, &infra.StreamConfig{Name: events.StreamName, Subjects: events.StreamSubjects})
}

// deliveryJob is a self-contained unit of work: the resolved subscriber + the exact
// bytes to sign and send. It carries the org's endpoint secret, so the delivery path
// needs no store read. It also carries org + endpointID SOLELY so the worker can write
// the (best-effort) per-attempt delivery-log row — never for the delivery itself. A job
// with an empty org/endpointID (the delivery-only unit tests) is delivered but not
// logged.
type deliveryJob struct {
	org        string
	endpointID string
	url        string
	secret     string
	subject    string
	delivery   string // stable UUID across the attempt-group
	body       []byte
}

// dispatcher owns the bus consumer, the worker pool, and the delivery HTTP client.
// errStreamConsumerPanicked is what a consumer reports when its goroutine panicked
// rather than returning. It is a distinct value so "the consumer died on bad input"
// is never silently read as "the bus closed".
var errStreamConsumerPanicked = errors.New("webhooks: stream consumer panicked (recovered)")

type dispatcher struct {
	stores *cloud.OrgStore[*store]
	log    luxlog.Logger
	http   *http.Client
	jobs   chan deliveryJob
	sleep  func(time.Duration) // injectable so tests run the retry ladder instantly

	noOrgOnce sync.Once // "log once at debug" for org-less events

	mu     sync.Mutex
	cancel context.CancelFunc
	client *infra.PubSubClient
	wg     sync.WaitGroup
}

func newDispatcher(stores *cloud.OrgStore[*store], log luxlog.Logger) *dispatcher {
	return &dispatcher{
		stores: stores,
		log:    log,
		http:   &http.Client{}, // per-attempt context governs the timeout
		jobs:   make(chan deliveryJob, queueSize),
		sleep:  time.Sleep,
	}
}

// storeFor is the dispatcher's half of the ONE door: the delivery loop names the
// database the same way the API does, so an event and the endpoints it fans out
// to can never come from different files.
func (d *dispatcher) storeFor(org string) (*store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return d.stores.For(ns)
}

// start brings the dispatcher up against the ONE platform bus. It never blocks and never
// fails the mount: the connect + consume loop runs in the background and retries a down
// bus forever, so the registry serves from the first moment either way.
func (d *dispatcher) start() {
	url := pubsub.URL()
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()

	for range numWorkers {
		d.wg.Add(1)
		go d.worker(ctx)
	}
	go d.run(ctx, url)
	d.log.Info("webhooks dispatcher started", "url", url, "streams", streamNames(), "durable", durableName)
}

// stop cancels the loops, closes the bus client, and waits for the workers to drain.
func (d *dispatcher) stop() {
	d.mu.Lock()
	cancel := d.cancel
	cl := d.client
	d.cancel, d.client = nil, nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cl != nil {
		_ = cl.Close()
	}
	d.wg.Wait()
}

// worker drains the job queue until shutdown.
func (d *dispatcher) worker(ctx context.Context) {
	defer d.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-d.jobs:
			d.deliver(ctx, job)
		}
	}
}

// run connects to the bus and consumes, reconnecting forever on any failure until ctx
// is canceled. A down bus at boot is a warning + retry, never a crash.
func (d *dispatcher) run(ctx context.Context, url string) {
	for ctx.Err() == nil {
		cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{
			URL:             url,
			Name:            clientName,
			EnableJetStream: true,
			MaxReconnects:   -1,
		})
		if err != nil {
			d.log.Warn("webhooks: bus connect failed — retrying", "url", url, "err", err)
			d.backoff(ctx, 5*time.Second)
			continue
		}
		d.setClient(cl)
		if err := d.consume(ctx, cl); err != nil && ctx.Err() == nil {
			d.log.Warn("webhooks: consume ended — reconnecting", "err", err)
		}
		_ = cl.Close()
		d.setClient(nil)
		d.backoff(ctx, 2*time.Second)
	}
}

// consume ensures each configured stream + its durable consumer, then runs one consume
// loop per stream, returning on the first loop error or ctx cancellation.
func (d *dispatcher) consume(ctx context.Context, cl *infra.PubSubClient) error {
	for _, s := range streams {
		// The OWNER's constructor, so the stream is created once with one config no
		// matter who gets to the bus first. Never a copy of it written down here.
		if err := s.ensure(ctx, cl); err != nil {
			return fmt.Errorf("ensure stream %s: %w", s.stream, err)
		}
		// No FilterSubject: we consume the WHOLE stream and match per subscription, so
		// one durable serves every subject on the stream.
		if _, err := cl.CreateConsumer(ctx, s.stream, &infra.ConsumerConfig{
			Name:          durableName,
			Durable:       durableName,
			Description:   "webhooks: fan platform events to org subscribers",
			DeliverPolicy: infra.DeliverNew,
			AckPolicy:     infra.AckExplicit,
			AckWait:       30 * time.Second,
			MaxDeliver:    5,
		}); err != nil {
			return fmt.Errorf("create consumer %s: %w", s.stream, err)
		}
	}

	errc := make(chan error, len(streams))
	for _, s := range streams {
		// Contained: d.handle runs over bus payloads and delivers to customer-
		// controlled endpoints, so it is fed by input we do not author on both
		// sides. A panic here would kill the process rather than this consumer,
		// taking every tenant with it. The inner defer answers errc first so the
		// select below returns instead of blocking until ctx expires — a panicking
		// consumer must look like a failed consumer, not a hung one.
		cloud.Go(d.log, "webhooks.consume", []any{"stream", s.stream}, func() {
			var once sync.Once
			send := func(err error) { once.Do(func() { errc <- err }) }
			defer send(errStreamConsumerPanicked)
			send(cl.ConsumeMessages(ctx, s.stream, durableName, func(m *infra.StreamMessage) error {
				return d.handle(ctx, s, m)
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

// handle maps ONE bus event to its org's matching deliveries and QUEUES them. It ACKs
// (returns nil) once queued — never after the retries. It NAKs (returns an error) only
// on a genuine store fault worth a bounded redelivery; a benign outcome (no org, no
// subscriber, no match) ACKs so a no-op message is never redelivered forever.
func (d *dispatcher) handle(ctx context.Context, s streamSource, m *infra.StreamMessage) error {
	subject := m.Subject
	// Another vocabulary on the same plane (streamSource.notMineKey) — the warehouse's
	// facts, which share subjects with the subscriber envelopes this package delivers.
	// Acked and skipped: delivering one would send a subscriber a body in a shape its
	// endpoint has never been promised, and send it a SECOND time for an event it was
	// already delivered.
	if s.notMineKey != "" && orgOf(m.Data, s.notMineKey) != "" {
		return nil
	}
	org := orgOf(m.Data, s.orgKey)
	if org == "" {
		// No org on the envelope ⇒ deliver to nobody (never cross-tenant). Log once.
		d.noOrgOnce.Do(func() {
			d.log.Debug("webhooks: event without an org field — delivered to nobody", "subject", subject)
		})
		return nil
	}
	st, err := d.storeFor(org)
	if err != nil {
		d.log.Warn("webhooks: open org store — will retry", "org", org, "err", err)
		return err
	}
	eps, err := st.listActive(ctx)
	if err != nil {
		d.log.Warn("webhooks: list endpoints — will retry", "org", org, "err", err)
		return err
	}
	for _, e := range eps {
		if !e.matches(subject) {
			continue
		}
		job := deliveryJob{org: org, endpointID: e.ID, url: e.URL, secret: e.Secret, subject: subject, delivery: newUUID(), body: m.Data}
		select {
		case d.jobs <- job: // QUEUED — the ack below covers it, not the retries
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// attemptResult is the outcome of ONE signed POST: whether it delivered, whether a
// failure is worth retrying, the HTTP status (0 on a network/timeout error), a short
// error string (empty on success), and the wall-clock the attempt took. It is what both
// the retry ladder and the synchronous test-send consume — the ONE delivery primitive.
type attemptResult struct {
	ok         bool
	retryable  bool
	httpStatus int
	err        string
	duration   time.Duration
}

// deliver runs the retry ladder for ONE job: up to maxAttempts POSTs, retrying only on a
// network error, 5xx, or 429, and treating any other 4xx as a permanent failure. Each
// attempt is logged (best-effort) as one delivery row — "retrying" while a further
// attempt will follow, "ok"/"failed" for the terminal one.
func (d *dispatcher) deliver(ctx context.Context, job deliveryJob) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res := d.attempt(ctx, job)
		willRetry := !res.ok && res.retryable && attempt < maxAttempts
		d.recordAttempt(ctx, job, attempt, statusLabel(res.ok, willRetry), res)
		if res.ok {
			return
		}
		if !res.retryable {
			d.log.Warn("webhook delivery failed permanently", "url", job.url, "event", job.subject, "delivery", job.delivery)
			return
		}
		if attempt < maxAttempts {
			d.sleep(jitter(retryBackoff[attempt-1]))
			continue
		}
	}
	d.log.Warn("webhook delivery exhausted retries", "url", job.url, "event", job.subject, "delivery", job.delivery)
}

// attempt makes ONE signed POST and reports the full outcome. The signature's timestamp
// is fresh per attempt (so a retry is not a replay of a stale-t request); the delivery id
// is stable across the group. This is the SINGLE sign+POST both the dispatcher (looped)
// and the /v1/webhooks/:id/test handler (once) call — there is no parallel test path.
func (d *dispatcher) attempt(ctx context.Context, job deliveryJob) attemptResult {
	start := time.Now()
	ts := start.Unix()
	sig := signPayload(job.secret, ts, job.body)

	actx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, job.url, bytes.NewReader(job.body))
	if err != nil {
		// An unbuildable request is permanent (never retried).
		return attemptResult{err: err.Error(), duration: time.Since(start)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", fmt.Sprintf("t=%d,v1=%s", ts, sig))
	req.Header.Set("X-Webhook-Event", job.subject)
	req.Header.Set("X-Webhook-Delivery", job.delivery)

	resp, err := d.http.Do(req)
	if err != nil {
		return attemptResult{retryable: true, err: err.Error(), duration: time.Since(start)} // network/timeout ⇒ retry
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	res := attemptResult{httpStatus: resp.StatusCode, duration: time.Since(start)}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.ok = true
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		res.retryable = true
		res.err = fmt.Sprintf("HTTP %d", resp.StatusCode)
	default:
		res.err = fmt.Sprintf("HTTP %d", resp.StatusCode) // 4xx (except 429) ⇒ permanent
	}
	return res
}

// statusLabel maps an attempt outcome to its stored delivery-row status: "ok" on
// success, "retrying" when a further attempt will follow, else "failed" (permanent or
// last of the ladder). Every attempt-group ends in exactly one terminal ok/failed row.
func statusLabel(ok, willRetry bool) string {
	switch {
	case ok:
		return "ok"
	case willRetry:
		return "retrying"
	default:
		return "failed"
	}
}

// recordAttempt best-effort persists ONE delivery-attempt row for job. A job with no
// org/endpointID (the delivery-only unit tests) is skipped, so no spurious per-org store
// is created; any store or write error is logged and swallowed — the delivery log must
// never affect delivery, and it runs in the worker goroutine, off the consumer's ack path.
func (d *dispatcher) recordAttempt(ctx context.Context, job deliveryJob, attempt int, status string, res attemptResult) {
	if job.org == "" || job.endpointID == "" {
		return
	}
	st, err := d.storeFor(job.org)
	if err != nil {
		d.log.Warn("webhooks: open store for delivery log", "org", job.org, "err", err)
		return
	}
	row := DeliveryRow{
		EndpointID: job.endpointID,
		DeliveryID: job.delivery,
		Subject:    job.subject,
		Attempt:    attempt,
		Status:     status,
		HTTPStatus: res.httpStatus,
		Error:      clip(res.err, maxDeliveryError),
		DurationMs: res.duration.Milliseconds(),
		Created:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := st.recordDelivery(ctx, row); err != nil {
		d.log.Warn("webhooks: record delivery log", "org", job.org, "endpoint", job.endpointID, "err", err)
	}
}

func (d *dispatcher) setClient(cl *infra.PubSubClient) {
	d.mu.Lock()
	d.client = cl
	d.mu.Unlock()
}

// backoff sleeps for d, aborting early if ctx is canceled.
func (d *dispatcher) backoff(ctx context.Context, dur time.Duration) {
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ---- pure helpers ----

// signPayload computes the delivery signature: hex HMAC-SHA256("<t>.<body>") under the
// endpoint's secret. The header carries it as `t=<t>,v1=<hex>` (Stripe-style), so a
// subscriber recomputes v1 over the received timestamp + raw body.
func signPayload(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// orgOf reads the emitting org out of an envelope, from the field the message's OWN
// stream declares (streamSource.orgKey). An undecodable body, a missing field, or a
// non-string value yields "" ⇒ delivered to nobody, which is the safe answer: a tenant
// this function guessed would be a cross-tenant delivery.
func orgOf(data []byte, key string) string {
	var env map[string]json.RawMessage
	if key == "" || json.Unmarshal(data, &env) != nil {
		return ""
	}
	raw, ok := env[key]
	if !ok {
		return ""
	}
	var org string
	if json.Unmarshal(raw, &org) != nil {
		return ""
	}
	return strings.TrimSpace(org)
}

// jitter adds up to +25% random spread to a backoff so a fleet of retrying deliveries
// does not thundering-herd a recovering subscriber.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d/4)+1))
	if err != nil {
		return d
	}
	return d + time.Duration(n.Int64())
}

// newUUID mints a canonical RFC-4122 v4 UUID string from crypto/rand — the stable
// per-attempt-group delivery id, with no new dependency.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func streamNames() []string {
	out := make([]string, len(streams))
	for i, s := range streams {
		out[i] = s.stream
	}
	return out
}
