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
// ONE OPS KNOB. The bus URL is CLOUD_WEBHOOKS_NATS_URL, falling back to the SAME
// CLOUD_COMMERCE_NATS_URL clients/catalogsync reads — so a deployment sets one variable
// and both the reverse-storefront loop and this dispatcher come alive, with an optional
// webhooks-specific override. Unset ⇒ the dispatcher is inert (the registry still serves).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/commerce/events"
	"github.com/hanzoai/commerce/infra"
	luxlog "github.com/luxfi/log"
)

const (
	// natsURLEnv is the dispatcher's own knob; natsURLFallback is the shared one
	// catalogsync also reads, so ops has ONE variable to turn the bus loops on.
	natsURLEnv      = "CLOUD_WEBHOOKS_NATS_URL"
	natsURLFallback = "CLOUD_COMMERCE_NATS_URL"

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

// streamSource names a JetStream stream to consume and the subjects it carries (for the
// idempotent EnsureStream). Adding BASE / WORLD / IAM is appending a row here, not a
// rewrite: the org-resolution + match + deliver path is stream-agnostic.
type streamSource struct {
	stream   string
	subjects []string
}

// streams is the consumed set. COMMERCE (commerce.>) is live today.
var streams = []streamSource{
	{stream: events.StreamName, subjects: events.StreamSubjects},
}

// deliveryJob is a self-contained unit of work: the resolved subscriber + the exact
// bytes to sign and send. It carries the org's endpoint secret, so the worker never
// touches the store.
type deliveryJob struct {
	url      string
	secret   string
	subject  string
	delivery string // stable UUID across the attempt-group
	body     []byte
}

// dispatcher owns the bus consumer, the worker pool, and the delivery HTTP client.
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

// start brings the dispatcher up when a bus URL is configured, else logs inert and
// returns (the registry still serves). It never blocks and never fails the mount: the
// connect + consume loop runs in the background and retries a down bus forever.
func (d *dispatcher) start() {
	url := firstNonEmpty(os.Getenv(natsURLEnv), os.Getenv(natsURLFallback))
	if url == "" {
		d.log.Info("webhooks dispatcher inert (set " + natsURLEnv + " or " + natsURLFallback + " to deliver bus events)")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()

	for i := 0; i < numWorkers; i++ {
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
		if err := cl.EnsureStream(ctx, &infra.StreamConfig{Name: s.stream, Subjects: s.subjects}); err != nil {
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
		s := s
		go func() {
			errc <- cl.ConsumeMessages(ctx, s.stream, durableName, func(m *infra.StreamMessage) error {
				return d.handle(ctx, m)
			})
		}()
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
func (d *dispatcher) handle(ctx context.Context, m *infra.StreamMessage) error {
	subject := m.Subject
	org := orgOf(m.Data)
	if org == "" {
		// No org on the envelope ⇒ deliver to nobody (never cross-tenant). Log once.
		d.noOrgOnce.Do(func() {
			d.log.Debug("webhooks: event without an org field — delivered to nobody", "subject", subject)
		})
		return nil
	}
	st, err := d.stores.For(org, "")
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
		job := deliveryJob{url: e.URL, secret: e.Secret, subject: subject, delivery: newUUID(), body: m.Data}
		select {
		case d.jobs <- job: // QUEUED — the ack below covers it, not the retries
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// deliver runs the retry ladder for ONE job: up to maxAttempts POSTs, retrying only on a
// network error, 5xx, or 429, and treating any other 4xx as a permanent failure.
func (d *dispatcher) deliver(ctx context.Context, job deliveryJob) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ok, retryable := d.attempt(ctx, job)
		if ok {
			return
		}
		if !retryable {
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

// attempt makes ONE signed POST. It returns (delivered, retryable). The signature's
// timestamp is fresh per attempt (so a retry is not a replay of a stale-t request); the
// delivery id is stable across the group.
func (d *dispatcher) attempt(ctx context.Context, job deliveryJob) (ok, retryable bool) {
	ts := time.Now().Unix()
	sig := signPayload(job.secret, ts, job.body)

	actx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, job.url, bytes.NewReader(job.body))
	if err != nil {
		return false, false // an unbuildable request is permanent (never retried)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", fmt.Sprintf("t=%d,v1=%s", ts, sig))
	req.Header.Set("X-Webhook-Event", job.subject)
	req.Header.Set("X-Webhook-Delivery", job.delivery)

	resp, err := d.http.Do(req)
	if err != nil {
		return false, true // network/timeout ⇒ retry
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, true
	default:
		return false, false // 4xx (except 429) ⇒ permanent
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

// orgOf reads the emitting org from a commerce event envelope (organization_id). An
// undecodable body or an absent org yields "" ⇒ delivered to nobody.
func orgOf(data []byte) string {
	var env struct {
		OrganizationID string `json:"organization_id"`
	}
	if json.Unmarshal(data, &env) != nil {
		return ""
	}
	return strings.TrimSpace(env.OrganizationID)
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func streamNames() []string {
	out := make([]string, len(streams))
	for i, s := range streams {
		out[i] = s.stream
	}
	return out
}
