package kafka

import (
	"context"
	"fmt"
	"strconv"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/readiness"
	"github.com/hanzoai/kafka/protocol"
	"github.com/hanzoai/kafka/types"
)

// Mount order is the row position in manifest/apps.go: this must stay AFTER
// apps/pubsub so the embedded NATS :4222 is already accepting when the broker
// dials it.

// broker holds the running adaptor so Shutdown can stop it. Set once by Use.
var broker *protocol.Broker

// Use connects the Kafka adaptor to the bus and serves it.
func Use(app cloud.Router, deps cloud.Deps) error {
	log := luxlog.Default().New("subsystem", "kafka")

	// The port clients are told to dial: the one the host binds, or this
	// process's own when it runs standalone. One value for both, so the two
	// cannot disagree about where Kafka is.
	row, _ := manifest.Named("kafka")
	port := row.Port()
	// 0 = admin HTTP disabled, which is also what a value that is not a port gives.
	adminPort := env.Int("CLOUD_KAFKA_ADMIN_PORT", 0)
	// 0 takes the adaptor's default; negative lifts the limit.
	maxAge, err := time.ParseDuration(env.Or("CLOUD_KAFKA_RETENTION", "0s"))
	if err != nil {
		return fmt.Errorf("kafka.Use:  CLOUD_KAFKA_RETENTION: %w", err)
	}
	maxBytes, err := strconv.ParseInt(env.Or("CLOUD_KAFKA_RETENTION_BYTES", "0"), 10, 64)
	if err != nil {
		return fmt.Errorf("kafka.Use:  CLOUD_KAFKA_RETENTION_BYTES: %w", err)
	}

	cfg := &types.Configuration{
		PubSubUrl:         bus.URL(),
		PubSubCredFile:    env.Or("CLOUD_KAFKA_PUBSUB_CREDS", ""),
		BrokerHost:        env.Or("CLOUD_KAFKA_HOST", "cloud"),
		BrokerPort:        port,
		AdminPort:         adminPort,
		NodeID:            1,
		StreamReplicas:    bus.Replicas(),
		StorageType:       "file",
		RetentionMaxAge:   maxAge,
		RetentionMaxBytes: maxBytes,
	}

	b := protocol.NewBroker(cfg)
	// Fail closed on the bus BEFORE the listener exists, so a start that cannot
	// reach it never accepts a client it cannot serve.
	if err := b.Connect(); err != nil {
		b.Shutdown()
		return fmt.Errorf("kafka.Use:  connect to the bus (fail-closed): %w", err)
	}
	ln, where, err := row.Listen()
	if err != nil {
		b.Shutdown()
		return fmt.Errorf("kafka.Use:  %w", err)
	}

	broker = b
	// Watch for a later exit. A clean Shutdown makes Accept return nil, which is
	// silent; an UNEXPECTED exit is a broker that died after a healthy start, which
	// nothing above K8s would notice (the generic /v1/kafka/health answers ok
	// regardless). Record a degradation so it surfaces on the binary's /v1/health
	// `degraded` field and the release smoke.
	go func() {
		if err := b.Accept(ln); err != nil {
			readiness.Degraded("kafka", err)
			log.Error("kafka broker accept loop exited", "err", err)
		}
	}()

	log.Info("kafka adaptor serving", "listen", where, "advertise", fmt.Sprintf("%s:%d", cfg.BrokerHost, cfg.BrokerPort), "pubsub_url", cfg.PubSubUrl)
	return nil
}

// Shutdown stops the embedded broker on graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	if broker != nil {
		broker.Shutdown()
		broker = nil
	}
	return nil
}
