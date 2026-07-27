// Package pubsub embeds the Hanzo PubSub core data plane (NATS + JetStream) as
// an in-process cloud subsystem (HIP-0106) — the same fold pattern as
// iam/kms/tasks. When enabled it binds the NATS client port (default :4222) and
// serves JetStream over the cloud data dir; the embedded Kafka adaptor
// (clients/kafka) and any in-cluster NATS/Kafka client talk to it. It is a
// single embedded node running JetStream over the local file store — there is
// NO ZooKeeper, raft, or etcd in the path (Lux consensus only; the optional
// Quasar PQ control plane is a follow-up, see github.com/hanzoai/pubsub/embed).
//
// It mounts NO HTTP routes of its own: it is a background TCP server. Cloud's
// generic per-subsystem liveness route answers /v1/pubsub/health, and the K8s
// Service TCP-probes :4222 directly.
//
// ACTIVATION is opt-in via CLOUD_PUBSUB_ENABLED (a staged cutover from the
// standalone `pubsub` Deployment). Unset ⇒ the subsystem is a no-op and binds
// nothing, so every existing deployment is unaffected. Enabled ⇒ Open fails
// CLOSED: a bind/start error aborts boot rather than serving a phantom
// messaging plane.
package pubsub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	psembed "github.com/hanzoai/pubsub/embed"
)

// order 5: an infrastructure data plane that must bind before clients/kafka
// (order 6) dials it. It registers no HTTP routes, so the order only fixes the
// pubsub-before-kafka mount sequence.
const order = 5

// srv holds the running embedded server so shutdown can stop it. Set once by Mount.
var srv *psembed.Server

// Mount starts the embedded PubSub server when CLOUD_PUBSUB_ENABLED is set,
// binding NATS + JetStream in-process. Disabled by default (no-op).
func Mount(app cloud.Router, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("pubsub.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "pubsub")

	if !envBool("CLOUD_PUBSUB_ENABLED") {
		log.Info("pubsub embed disabled (set CLOUD_PUBSUB_ENABLED=true to serve NATS+JetStream in-process)")
		return nil
	}

	dataDir := firstNonEmpty(
		os.Getenv("CLOUD_PUBSUB_STORE_DIR"),
		filepath.Join(firstNonEmpty(deps.DataDir, "/var/lib/cloud"), "pubsub"),
	)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("pubsub.Mount: store dir %s: %w", dataDir, err)
	}

	port := 4222
	if v := strings.TrimSpace(os.Getenv("CLOUD_PUBSUB_PORT")); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("pubsub.Mount: bad CLOUD_PUBSUB_PORT %q: %w", v, err)
		}
		port = p
	}

	s, err := psembed.Open(psembed.Options{
		Host:       firstNonEmpty(os.Getenv("CLOUD_PUBSUB_HOST"), "0.0.0.0"),
		Port:       port,
		ServerName: firstNonEmpty(os.Getenv("CLOUD_PUBSUB_SERVER_NAME"), "cloud-pubsub-"+firstNonEmpty(deps.Brand, "hanzo")),
		StoreDir:   dataDir,
	})
	if err != nil {
		// Fail closed: an enabled-but-broken messaging plane must abort boot.
		return fmt.Errorf("pubsub.Mount: open embedded server (fail-closed): %w", err)
	}
	srv = s
	log.Info("pubsub embedded server serving", "client_url", s.ClientURL(), "store_dir", dataDir, "port", port)
	return nil
}

// shutdown stops the embedded server on graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	if srv != nil {
		srv.Shutdown()
		srv = nil
	}
	return nil
}

func envBool(k string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
