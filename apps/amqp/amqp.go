package amqp

import (
	"context"
	"fmt"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/readiness"
	"github.com/hanzoai/amqp/protocol"
)

// gateway holds the running broker so Shutdown can stop it. Set once by Use.
var gateway *protocol.Broker

// Use connects the AMQP gateway to the bus and serves it.
func Use(app cloud.Router, deps cloud.Deps) error {
	log := luxlog.Default().New("subsystem", "amqp")

	b := protocol.NewBroker(protocol.Config{
		PubSubURL:   bus.URL(),
		PubSubCreds: env.Or("CLOUD_AMQP_PUBSUB_CREDS", ""),
		Product:     "hanzo-cloud",
		Version:     deps.Version,
	})
	// Fail closed on the bus BEFORE the listener exists, so a start that cannot
	// reach it never accepts a client it cannot serve.
	if err := b.Connect(); err != nil {
		b.Shutdown()
		return fmt.Errorf("amqp.Use:  connect to the bus (fail-closed): %w", err)
	}
	row, _ := manifest.Named("amqp")
	ln, where, err := row.Listen()
	if err != nil {
		b.Shutdown()
		return fmt.Errorf("amqp.Use:  %w", err)
	}

	gateway = b
	// Watch for a later exit. A clean Shutdown makes Accept return nil, which is
	// silent; an UNEXPECTED exit is a gateway that died after a healthy start,
	// which nothing above K8s would notice (the generic /v1/amqp/health answers ok
	// regardless). Record a degradation so it surfaces on the binary's /v1/health
	// `degraded` field and the release smoke.
	go func() {
		if err := b.Accept(ln); err != nil {
			readiness.Degraded("amqp", err)
			log.Error("amqp gateway accept loop exited", "err", err)
		}
	}()

	log.Info("amqp gateway serving", "listen", where, "pubsub_url", bus.Display())
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
