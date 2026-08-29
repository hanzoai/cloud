// Package kafka is Kafka on the platform bus: point a standard producer or
// consumer at :9092 and it works unchanged.
//
// The Hanzo Kafka adaptor (github.com/hanzoai/kafka) speaks the Kafka binary
// protocol on :9092 and translates it to and from the JetStream apps/pubsub
// serves, so every client shares ONE bus. It connects to
// that PubSub as a NATS client on loopback :4222 — replacing the standalone
// `insights-kafka` Deployment. No ZooKeeper: the adaptor is stateless over
// JetStream (Lux consensus only).
//
// It mounts NO HTTP routes of its own, so cloud's generic per-subsystem
// liveness route answers /v1/kafka/health with an unconditional ok. That route
// does NOT prove the broker is up; readiness is structural. This app is Eager
// and Mount fails CLOSED (below), so a broker that cannot bind :9092 or reach
// the bus at boot takes the process down and the pod never goes Ready. A broker
// that dies AFTER boot is reported through cloud.Degraded (the binary's
// /v1/health `degraded` field, which the release smoke refuses an image on).
// The :9092 listener is an in-pod socket — the K8s Service exposes only the
// HTTP/metrics/ZAP ports, so reaching it beyond the pod is a Service (and
// NetworkPolicy) decision this app does not make.
//
// It ALWAYS serves, like the PubSub plane it rides (a staged cutover that is
// over). Mount fails CLOSED: a connect/bind error within the startup window
// aborts boot rather than serving a phantom broker. It dials the bus through
// pubsub.URL — the ONE knob every app in this process reads — so it cannot end
// up bridging a different bus than the one analytics publishes and webhooks
// consumes, and there is never a silent half-embed.
package kafka

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/pubsub"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/kafka/protocol"
	"github.com/hanzoai/kafka/types"
)

// Mount order is the row position in manifest/apps.go: this must stay AFTER
// apps/pubsub so the embedded NATS :4222 is already accepting when the broker
// dials it.

// startupProbe bounds how long Mount waits to distinguish a startup failure
// (Serve returns quickly) from a healthy serving broker (Serve blocks on its
// accept loop and never returns until Shutdown).
const startupProbe = 3 * time.Second

// broker holds the running adaptor so Shutdown can stop it. Set once by Mount.
var broker *protocol.Broker

// Mount starts the embedded Kafka adaptor over the embedded JetStream.
func Use(app cloud.Router, deps cloud.Deps) error {
	log := luxlog.Default().New("subsystem", "kafka")

	port, err := envInt("CLOUD_KAFKA_PORT", 9092)
	if err != nil {
		return err
	}
	adminPort, err := envInt("CLOUD_KAFKA_ADMIN_PORT", 0) // 0 = admin HTTP disabled
	if err != nil {
		return err
	}

	cfg := &types.Configuration{
		PubSubUrl:      pubsub.URL(),
		PubSubCredFile: os.Getenv("CLOUD_KAFKA_PUBSUB_CREDS"),
		BrokerHost:     environ.Or("CLOUD_KAFKA_HOST", "cloud"),
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
			return fmt.Errorf("kafka.Use:  broker serve (fail-closed): %w", serveErr)
		}
		return fmt.Errorf("kafka.Use:  broker exited immediately (fail-closed)")
	case <-time.After(startupProbe):
	}

	broker = b
	// Watch for a later exit. A clean Shutdown makes Serve return nil, which is
	// silent; an UNEXPECTED exit is a broker that died after a healthy boot, which
	// nothing above K8s would notice (the container probes are on :8080/:9090 and
	// the generic /v1/kafka/health answers ok regardless). Record a degradation so
	// it surfaces on the binary's /v1/health `degraded` field and the release smoke.
	go func() {
		if err := <-errc; err != nil {
			cloud.Degraded("kafka", err)
			log.Error("kafka broker serve exited", "err", err)
		}
	}()

	log.Info("kafka adaptor serving", "port", port, "pubsub_url", cfg.PubSubUrl, "advertise_host", cfg.BrokerHost)
	return nil
}

// shutdown stops the embedded broker on graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	if broker != nil {
		broker.Shutdown()
		broker = nil
	}
	return nil
}

func envInt(k string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("kafka.Use:  bad %s %q: %w", k, v, err)
	}
	return n, nil
}
