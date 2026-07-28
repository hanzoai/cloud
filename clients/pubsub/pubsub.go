// Package pubsub embeds the Hanzo PubSub core data plane (NATS + JetStream) as
// an in-process cloud subsystem (HIP-0106) — the same fold pattern as
// iam/kms/tasks. It binds the NATS client port (default :4222) and
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
// It ALWAYS serves. The staged cutover it was gated behind is over — the
// standalone nats StatefulSet and the pubsub App are retired, so this is the ONE
// in-cluster messaging plane and a cloud that did not serve it would simply have
// no messaging. WHERE it listens stays configurable (CLOUD_PUBSUB_PORT,
// CLOUD_PUBSUB_HOST); a port collision is answered by moving the port, never by
// running without the plane.
//
// Open fails CLOSED: a bind/start error aborts boot rather than serving a phantom
// messaging plane.
package pubsub

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	psembed "github.com/hanzoai/pubsub/embed"
)

// Mount order is the slice position in apps.Wire(): this infrastructure data
// plane must bind BEFORE clients/kafka dials it. It registers no HTTP routes, so
// the position only fixes the pubsub-before-kafka mount sequence.

// srv holds the running embedded server so shutdown can stop it. Set once by Mount.
var srv *psembed.Server

// Mount starts the embedded PubSub server, binding NATS + JetStream in-process.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("pubsub.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "pubsub")

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

	host := firstNonEmpty(os.Getenv("CLOUD_PUBSUB_HOST"), "0.0.0.0")

	// Claim the address BEFORE handing it to the embedded server, because the
	// embedded server cannot report that it failed to take it: Open calls
	// ConfigureLogger, installing the NATS logger whose Fatalf exits the process,
	// and the accept loop hits the bind error before ReadyForConnections is ever
	// consulted. So on the most common failure — something already holds the port —
	// Open never returns and the error branch below is unreachable; the process
	// simply vanishes, with no cloud log line naming the subsystem and no shutdown
	// of what already mounted.
	//
	// Probing first turns that into the error Mount is documented to return, so boot
	// aborts through cloud's own path. It cannot close the race completely (the port
	// can be taken between this Close and the server's own bind), which is why the
	// library's exit stays as the backstop rather than being papered over.
	if err := claimable(host, port); err != nil {
		return fmt.Errorf("pubsub.Mount: cannot bind %s (fail-closed): %w", net.JoinHostPort(host, strconv.Itoa(port)), err)
	}

	s, err := psembed.Open(psembed.Options{
		Host:       host,
		Port:       port,
		ServerName: firstNonEmpty(os.Getenv("CLOUD_PUBSUB_SERVER_NAME"), "cloud-pubsub-"+firstNonEmpty(deps.Brand, "hanzo")),
		StoreDir:   dataDir,
	})
	if err != nil {
		// Fail closed: a broken messaging plane must abort boot.
		return fmt.Errorf("pubsub.Mount: open embedded server (fail-closed): %w", err)
	}
	srv = s
	log.Info("pubsub embedded server serving", "client_url", s.ClientURL(), "store_dir", dataDir, "port", port)
	return nil
}

// claimable reports whether host:port can be bound right now, by binding it and
// letting it go. A non-positive port means "pick a free one" (-1 in tests), which
// cannot collide and so is nothing to probe.
func claimable(host string, port int) error {
	if port <= 0 {
		return nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return ln.Close()
}

// shutdown stops the embedded server on graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	if srv != nil {
		srv.Shutdown()
		srv = nil
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
