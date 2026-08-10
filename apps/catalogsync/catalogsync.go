// Package catalogsync is the REVERSE half of the storefront loop: it consumes the
// commerce COMMERCE stream and turns each `commerce.product.created` into ONE
// content.EnsureCatalogAsset call, so a newly-created catalog product gets its ecom
// asset rendered (design == slug) — the mirror of the forward edge (apps/content
// storefront.go) that publishes a rendered asset back onto the product image.
//
// It is a thin, in-process consumer subsystem: the mapping/idempotency/skip logic lives
// in the content lane (content.EnsureCatalogAsset); this package owns ONLY "read the
// product event off NATS and dispatch it". Decomplected from commerce and content — the
// COMMERCE stream is the seam, so neither imports the other; catalogsync imports both.
//
// ALWAYS ON, against the ONE bus. It consumes the COMMERCE stream on pubsub.URL — the
// same embedded server every other app in this process reads, which always serves and
// fails boot closed. It used to require a knob of its own (CLOUD_COMMERCE_NATS_URL) that
// no manifest set, so the reverse edge was permanently inert while the bus it needed was
// running the whole time: an opt-in for a state that does not exist. A commerce publisher
// that is not wired simply produces no events, which this consumer already handles by
// waiting.
package catalogsync

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/content"
	"github.com/hanzoai/cloud/apps/pubsub"
	"github.com/hanzoai/commerce/events"
	"github.com/hanzoai/commerce/infra"
	luxlog "github.com/luxfi/log"
)

const (
	// consumerName is the durable JetStream consumer this subsystem binds on the COMMERCE
	// stream. Durable + shared across pods, so multiple cloud replicas load-balance the
	// product events and each is handled exactly once.
	consumerName = "content-catalogsync"

	// eventProductCreated is the envelope Type the consumer acts on. The subscription is
	// already subject-filtered to product.created; this is belt-and-suspenders.
	eventProductCreated = "product.created"
)

// generate is the content-lane mapping the consumer drives, as a package var so a test
// can substitute a stub without a live content mount. It is the ONE call into content.
var generate = content.EnsureCatalogAsset

// runtime holds the running consumer so Shutdown can stop it. Guarded by mu; the cancel
// is set once by Mount, the client by each consume attempt.
var (
	mu     sync.Mutex
	cancel context.CancelFunc
	client *infra.PubSubClient
)

// Mount starts the catalog-event consumer against the platform bus. It never blocks: the
// connect + consume loop runs in the background, and a connect failure is a warning and a
// retry, never a crash — the same fail-soft contract as the forward storefront edge.
func Mount(app cloud.Router, deps cloud.Deps) error {
	log := luxlog.Default().New("subsystem", "catalogsync")

	url := pubsub.URL()
	ctx, cancelFn := context.WithCancel(context.Background())
	setLifecycle(cancelFn)
	go run(ctx, log, url)
	return nil
}

// Shutdown stops the consumer on graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	mu.Lock()
	c, cl := cancel, client
	cancel, client = nil, nil
	mu.Unlock()
	if c != nil {
		c()
	}
	if cl != nil {
		_ = cl.Close()
	}
	return nil
}

func setLifecycle(c context.CancelFunc) {
	mu.Lock()
	cancel = c
	mu.Unlock()
}

func setClient(cl *infra.PubSubClient) {
	mu.Lock()
	client = cl
	mu.Unlock()
}

// run keeps a consumer on the bus until ctx is canceled, reconnecting after any failure.
// It retries rather than degrading to inert because the subsystem is no longer opt-in: a
// bus that is briefly down at boot must not silently cost this deployment its reverse
// storefront edge until someone notices and restarts the pod.
func run(ctx context.Context, log luxlog.Logger, url string) {
	for ctx.Err() == nil {
		consume(ctx, log, url)
		if ctx.Err() != nil {
			return
		}
		t := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// retryDelay is the wait between consume attempts. The connection itself reconnects on
// its own (MaxReconnects -1); this covers the failures that end the loop entirely.
const retryDelay = 5 * time.Second

// consume connects to NATS, binds the durable COMMERCE consumer filtered to
// product.created, and dispatches each message to handleEvent until ctx is canceled or
// the loop fails. Every failure is a warn + return, never a crash.
func consume(ctx context.Context, log luxlog.Logger, url string) {
	cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{
		URL:             url,
		Name:            consumerName,
		EnableJetStream: true,
		MaxReconnects:   -1,
	})
	if err != nil {
		log.Warn("catalogsync: cannot connect to the bus — retrying", "url", url, "err", err)
		return
	}
	setClient(cl)
	defer func() { setClient(nil); _ = cl.Close() }()

	// Ensure the stream + a durable, subject-filtered consumer exist (both idempotent; the
	// commerce publisher also ensures the stream). DeliverNew: on first bind we start from
	// new events, never replaying the whole catalog history into a render flood; the durable
	// then resumes from the last ack across restarts.
	if err := cl.EnsureStream(ctx, &infra.StreamConfig{Name: events.StreamName, Subjects: events.StreamSubjects}); err != nil {
		log.Warn("catalogsync: ensure COMMERCE stream — retrying", "err", err)
		return
	}
	if _, err := cl.CreateConsumer(ctx, events.StreamName, &infra.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		Description:   "content: render ecom asset on product.created",
		FilterSubject: events.SubjectProductCreated,
		DeliverPolicy: infra.DeliverNew,
		AckPolicy:     infra.AckExplicit,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
	}); err != nil {
		log.Warn("catalogsync: create consumer — retrying", "err", err)
		return
	}

	log.Info("catalogsync consuming commerce catalog events",
		"stream", events.StreamName, "subject", events.SubjectProductCreated)
	if err := cl.ConsumeMessages(ctx, events.StreamName, consumerName, func(m *infra.StreamMessage) error {
		return handleEvent(ctx, log, m.Data)
	}); err != nil && ctx.Err() == nil {
		log.Warn("catalogsync: consume loop ended", "err", err)
	}
}

// handleEvent maps ONE commerce product event to a content render. It returns nil (ACK)
// for every benign outcome — an undecodable message, a non-product.created type, a missing
// org/slug, or any content-side skip (not installed / already rendered / studio not
// configured) — so a poison or no-op message is never redelivered forever. It returns a
// non-nil error (NAK, bounded by MaxDeliver) ONLY for a genuine content fault worth
// retrying.
func handleEvent(ctx context.Context, log luxlog.Logger, data []byte) error {
	var ev events.CommerceEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		log.Warn("catalogsync: undecodable event (skipped)", "err", err)
		return nil
	}
	if ev.Type != eventProductCreated {
		return nil // subject filter should preclude this; ignore defensively
	}
	org := strings.TrimSpace(ev.OrganizationID)
	slug := eventString(ev.Data, "slug")
	if org == "" || slug == "" {
		log.Warn("catalogsync: product.created missing org/slug (skipped)", "org", org, "slug", slug)
		return nil
	}

	res, err := generate(ctx, org, slug)
	if err != nil {
		log.Warn("catalogsync: render ecom asset failed (will retry)", "org", org, "slug", slug, "err", err)
		return err
	}
	if res.Created {
		log.Info("catalogsync rendered ecom asset for new product", "org", org, "slug", slug, "asset", res.Name)
	} else {
		log.Debug("catalogsync skipped product", "org", org, "slug", slug, "reason", res.Skipped)
	}
	return nil
}

// eventString reads a trimmed string field from a commerce event's Data map.
func eventString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}
