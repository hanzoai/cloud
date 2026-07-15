// Package catalogsync is the REVERSE half of the storefront loop: it consumes the
// commerce COMMERCE stream and turns each `commerce.product.created` into ONE
// content.EnsureCatalogAsset call, so a newly-created catalog product gets its ecom
// asset rendered (design == slug) — the mirror of the forward edge (clients/content
// storefront.go) that publishes a rendered asset back onto the product image.
//
// It is a thin, in-process consumer subsystem: the mapping/idempotency/skip logic lives
// in the content lane (content.EnsureCatalogAsset); this package owns ONLY "read the
// product event off NATS and dispatch it". Decomplected from commerce and content — the
// COMMERCE stream is the seam, so neither imports the other; catalogsync imports both.
//
// INERT BY DEFAULT. Commerce publishes catalog events only when its NATS publisher is
// wired, and this consumer runs only when CLOUD_COMMERCE_NATS_URL names the NATS that
// carries them (the same server cloud's embedded pubsub binds). Unset ⇒ the subsystem is
// a no-op and connects nothing, exactly like the publisher degrades to no-op when NATS is
// absent — the loop is off end-to-end until an operator wires both halves.
package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/content"
	"github.com/hanzoai/commerce/events"
	"github.com/hanzoai/commerce/infra"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	// natsURLEnv names the NATS server carrying the COMMERCE stream. Unset ⇒ inert.
	natsURLEnv = "CLOUD_COMMERCE_NATS_URL"

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

// runtime holds the running consumer so Shutdown can stop it. Guarded by mu; set once by
// Mount when NATS is configured.
var (
	mu     sync.Mutex
	cancel context.CancelFunc
	client *infra.PubSubClient
)

// Mount starts the catalog-event consumer when CLOUD_COMMERCE_NATS_URL is set, else is a
// no-op (the loop stays off until an operator wires NATS). It never blocks: the connect +
// consume loop runs in the background, and a connect failure degrades to inert (a warning,
// never a crash) — the same fail-soft contract as the forward storefront edge.
func Mount(app *zip.App, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("catalogsync.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "catalogsync")

	url := strings.TrimSpace(os.Getenv(natsURLEnv))
	if url == "" {
		log.Info("catalogsync inert (set " + natsURLEnv + " to consume commerce catalog events)")
		return nil
	}

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

// run connects to NATS, binds the durable COMMERCE consumer filtered to product.created,
// and dispatches each message to handleEvent until ctx is canceled. Every failure is a
// clean inert degrade (warn + return), never a crash.
func run(ctx context.Context, log luxlog.Logger, url string) {
	cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{
		URL:             url,
		Name:            consumerName,
		EnableJetStream: true,
		MaxReconnects:   -1,
	})
	if err != nil {
		log.Warn("catalogsync: cannot connect to NATS — inert", "url", url, "err", err)
		return
	}
	setClient(cl)

	// Ensure the stream + a durable, subject-filtered consumer exist (both idempotent; the
	// commerce publisher also ensures the stream). DeliverNew: on first bind we start from
	// new events, never replaying the whole catalog history into a render flood; the durable
	// then resumes from the last ack across restarts.
	if err := cl.EnsureStream(ctx, &infra.StreamConfig{Name: events.StreamName, Subjects: events.StreamSubjects}); err != nil {
		log.Warn("catalogsync: ensure COMMERCE stream — inert", "err", err)
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
		log.Warn("catalogsync: create consumer — inert", "err", err)
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
func eventString(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}
