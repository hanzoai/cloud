// Package amqp is AMQP 0-9-1 on the platform bus: point a standard RabbitMQ
// client at :5672 and it works unchanged.
//
// The Hanzo AMQP gateway (github.com/hanzoai/amqp) speaks the AMQP 0-9-1 wire
// protocol on :5672 and translates it to and from the JetStream apps/pubsub
// serves, so every client shares ONE bus — the same fold apps/kafka is for
// :9092 and apps/mq is for HTTP. It connects to that PubSub as a NATS client
// on loopback :4222, so there is no second broker to run, keep up, or lose a
// message between. An exchange and a routing key become a subject, a queue
// becomes a durable JetStream consumer, and basic.ack becomes an Ack; see the
// gateway's route.go for the whole map.
//
// It mounts NO HTTP routes of its own, so cloud's generic per-subsystem
// liveness route answers /v1/amqp/health with an unconditional ok. That route
// does NOT by itself prove the broker is up; readiness here is structural
// instead. This app is Eager and Mount fails CLOSED (below), so a broker that
// cannot bind :5672 or reach the bus at boot takes the whole process down and
// the pod never goes Ready. A broker that dies AFTER boot is reported through
// cloud.Degraded, which surfaces on the binary's /v1/health `degraded` field
// and is what the release smoke refuses an image on. The :5672 listener is an
// in-pod socket: the K8s Service exposes only the HTTP/metrics/ZAP ports, so
// reaching the broker beyond the pod is a Service (and NetworkPolicy) decision
// this app does not make.
//
// It ALWAYS serves, like the PubSub plane it rides. Mount fails CLOSED: it
// waits for the gateway to report ready — the bus answered, the stream and the
// topology bucket exist, the listener is bound — and a failure or a timeout
// aborts boot rather than serving a phantom broker. It dials the bus through
// pubsub.URL, the ONE knob every app in this process reads, so it cannot end
// up bridging a different bus than the one analytics publishes and webhooks
// consumes, and there is never a silent half-embed.
package amqp

import (
	"github.com/hanzoai/cloud/internal/environ"
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/amqp/protocol"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/pubsub"
)

// Mount order is the row position in manifest/apps.go: this must stay AFTER
// apps/pubsub so the embedded NATS :4222 is already accepting when the gateway
// dials it.

// startup bounds how long Mount waits for the gateway to be ready. It is a
// deadline on a signal, not a sleep: Ready closes the moment everything that
// can fail has succeeded, so a healthy boot takes as long as the bus takes to
// answer and no longer.
const startup = 15 * time.Second

// gateway holds the running broker so Shutdown can stop it. Set once by Mount.
var gateway *protocol.Broker

// Mount starts the embedded AMQP gateway over the embedded JetStream.
func Use(app cloud.Router, deps cloud.Deps) error {
	log := luxlog.Default().New("subsystem", "amqp")

	port := environ.Int("CLOUD_AMQP_PORT", 5672)

	b := protocol.NewBroker(protocol.Config{
		Addr:        net.JoinHostPort("", strconv.Itoa(port)),
		PubSubURL:   pubsub.URL(),
		PubSubCreds: environ.Or("CLOUD_AMQP_PUBSUB_CREDS", ""),
		Product:     "hanzo-cloud",
		Version:     cloud.Version,
	})

	errc := make(chan error, 1)
	go func() { errc <- b.Serve() }()

	select {
	case serveErr := <-errc:
		if serveErr != nil {
			return fmt.Errorf("amqp.Use:  gateway serve (fail-closed): %w", serveErr)
		}
		return fmt.Errorf("amqp.Use:  gateway exited immediately (fail-closed)")
	case <-b.Ready():
	case <-time.After(startup):
		b.Shutdown()
		return fmt.Errorf("amqp.Use:  gateway not ready within %s (fail-closed): pubsub %s", startup, pubsub.URL())
	}

	gateway = b
	// Watch for a later exit (an accept error, say). A clean Shutdown makes Serve
	// return nil, which is silent; an UNEXPECTED exit is a broker that died after
	// a healthy boot, and nothing above K8s would notice it — the container probes
	// are on :8080/:9090 and the generic /v1/amqp/health answers ok regardless. So
	// record a degradation: it surfaces on the binary's /v1/health `degraded`
	// field and is what the release smoke reads to refuse an image.
	go func() {
		if err := <-errc; err != nil {
			cloud.Degraded("amqp", err)
			log.Error("amqp gateway serve exited", "err", err)
		}
	}()

	log.Info("amqp gateway serving", "addr", b.Addr(), "pubsub_url", pubsub.URL())
	return nil
}

// Shutdown stops the embedded gateway on graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	if gateway != nil {
		gateway.Shutdown()
		gateway = nil
	}
	return nil
}
