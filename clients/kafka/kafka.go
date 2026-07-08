// Package kafka embeds the Hanzo Stream Kafka-wire adaptor (github.com/hanzoai/
// stream) as an in-process cloud subsystem (HIP-0106), translating the Kafka
// protocol to/from the embedded JetStream (clients/pubsub). When enabled it
// binds the Kafka listener (default :9092) and connects, as a NATS client, to
// the in-process PubSub on loopback :4222 — replacing the standalone
// `insights-kafka` Deployment. No ZooKeeper: the adaptor is stateless over
// JetStream (Lux consensus only).
//
// It mounts NO HTTP routes of its own; cloud's generic per-subsystem liveness
// route answers /v1/kafka/health and the K8s Service TCP-probes :9092.
//
// ACTIVATION is opt-in via CLOUD_KAFKA_ENABLED (a staged cutover). Unset ⇒ the
// subsystem is a no-op. Enabled ⇒ Mount fails CLOSED: a connect/bind error
// within the startup window aborts boot rather than serving a phantom broker.
// It dials the embedded PubSub by default, so enabling kafka without
// CLOUD_PUBSUB_ENABLED (and no external CLOUD_KAFKA_PUBSUB_URL) fails closed —
// never a silent half-embed.
package kafka

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/stream/protocol"
	"github.com/hanzoai/stream/types"
	"github.com/zap-proto/zip"
)

// order 6: mounts after clients/pubsub (order 5) so the embedded NATS :4222 is
// already accepting when the broker dials it.
const order = 6

// startupProbe bounds how long Mount waits to distinguish a startup failure
// (Serve returns quickly) from a healthy serving broker (Serve blocks on its
// accept loop and never returns until Shutdown).
const startupProbe = 3 * time.Second

// broker holds the running adaptor so Shutdown can stop it. Set once by Mount.
var broker *protocol.Broker

// Mount starts the embedded Kafka adaptor when CLOUD_KAFKA_ENABLED is set.
func Mount(app *zip.App, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("kafka.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "kafka")

	if !envBool("CLOUD_KAFKA_ENABLED") {
		log.Info("kafka adaptor disabled (set CLOUD_KAFKA_ENABLED=true to serve the Kafka wire protocol in-process over embedded JetStream)")
		return nil
	}

	port, err := envInt("CLOUD_KAFKA_PORT", 9092)
	if err != nil {
		return err
	}
	adminPort, err := envInt("CLOUD_KAFKA_ADMIN_PORT", 0) // 0 = admin HTTP disabled
	if err != nil {
		return err
	}

	cfg := &types.Configuration{
		PubSubUrl:      firstNonEmpty(os.Getenv("CLOUD_KAFKA_PUBSUB_URL"), "nats://127.0.0.1:4222"),
		PubSubCredFile: os.Getenv("CLOUD_KAFKA_PUBSUB_CREDS"),
		BrokerHost:     firstNonEmpty(os.Getenv("CLOUD_KAFKA_HOST"), "cloud"),
		BrokerPort:     port,
		AdminPort:      adminPort,
		NodeID:         1,
		StreamReplicas: 1,
		StorageType:    "file",
	}

	b := protocol.NewBroker(cfg)
	errc := make(chan error, 1)
	go func() { errc <- b.Serve() }()

	// Fail closed on a startup error surfaced within the probe window; otherwise
	// the accept loop is running and Serve stays blocked until Shutdown.
	select {
	case serveErr := <-errc:
		if serveErr != nil {
			return fmt.Errorf("kafka.Mount: broker serve (fail-closed): %w", serveErr)
		}
		return fmt.Errorf("kafka.Mount: broker exited immediately (fail-closed)")
	case <-time.After(startupProbe):
	}

	broker = b
	// Watch for a later exit (e.g. an accept error) and log it. A clean Shutdown
	// makes Serve return nil, which is silent.
	go func() {
		if err := <-errc; err != nil {
			log.Error("kafka broker serve exited", "err", err)
		}
	}()

	log.Info("kafka adaptor serving", "port", port, "pubsub_url", cfg.PubSubUrl, "advertise_host", cfg.BrokerHost)
	return nil
}

// shutdown stops the embedded broker on graceful cloud shutdown. Idempotent.
func shutdown(_ context.Context) error {
	if broker != nil {
		broker.Shutdown()
		broker = nil
	}
	return nil
}

func init() {
	cloud.RegisterWithShutdown("kafka", order, cloud.Typed(Mount), shutdown)
}

func envBool(k string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(k string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("kafka.Mount: bad %s %q: %w", k, v, err)
	}
	return n, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
