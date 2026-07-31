// Package pubsub is your message bus: publish, subscribe, and durable streams
// your apps read at their own pace.
//
// It is the platform message bus: publish/subscribe messaging, durable JetStream
// streams and consumers, and a key-value store, served to tenants at /v1/pubsub
// over the embedded Hanzo PubSub (NATS + JetStream) node this same package runs.
//
// The node binds the NATS client port (default :4222) and serves JetStream
// over the cloud data dir — the ONE durable log every other app publishes
// facts onto and consumes them from. The Kafka-wire adaptor (apps/kafka) and
// any in-cluster NATS/Kafka client talk to it. It is a single embedded node
// running JetStream over the local file store — there is NO ZooKeeper, raft,
// or etcd in the path (Lux consensus only; the optional Quasar PQ control
// plane is a follow-up, see github.com/hanzoai/pubsub/embed).
//
// ONE bus, TWO doors. The NATS port is the cluster's door: in-process apps and
// in-cluster clients, unscoped. /v1/pubsub is the tenant's door: eighteen
// typed ops (typed.go) that publish, request, manage streams and consumers,
// pull batches and keep key-value state — each org confined to its own
// namespace by the validated principal, never by anything a caller asserts.
// Cloud's generic per-subsystem liveness route answers /v1/pubsub/health, and
// the K8s Service TCP-probes :4222 directly.
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
//
// # THE BUS KNOB
//
// URL is where every app in this process reaches that plane, and it is the ONE
// knob for all of them — analytics publishing the event plane, webhooks consuming
// it, the Kafka facade bridging it. It defaults to the LOOPBACK address of the
// server this same binary just bound, so the default needs no configuration and
// cannot disagree with the server: both read CLOUD_PUBSUB_PORT.
//
//	CLOUD_PUBSUB_URL   full dial URL; set ONLY to point this process at a bus
//	                   other than its own embedded one.
//	CLOUD_PUBSUB_PORT  the port the embedded server binds AND the port the
//	                   default URL dials. Default 4222.
//	CLOUD_PUBSUB_HOST  the bind address of the embedded server (default
//	                   0.0.0.0). It is NOT a dial address: URL always dials
//	                   127.0.0.1, because 0.0.0.0 names every interface to a
//	                   listener and none to a client.
//
// There is deliberately no per-app URL variable and no "off". Mount fails boot
// closed, so a cloud that is up HAS a bus; an app that made its bus optional
// would silently ship with its half of the platform disconnected.
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

// Mount order is the row position in manifest/apps.go: this infrastructure data
// plane must bind BEFORE apps/kafka dials it. Its HTTP routes are its own
// prefix (/v1/pubsub), so the position only fixes the pubsub-before-kafka
// mount sequence.

// srv holds the running embedded server so shutdown can stop it. Set once by Mount.
var srv *psembed.Server

// The bus knob, in one place. See the package doc.
const (
	urlEnv      = "CLOUD_PUBSUB_URL"
	portEnv     = "CLOUD_PUBSUB_PORT"
	defaultPort = 4222
)

// URL is THE bus address for every app in this process. Callers dial it; nobody
// reads an environment variable of their own to find the bus.
//
// It never fails and it is never empty: a malformed port is Mount's error to
// report (it aborts boot), so by the time an app dials, the port either parsed or
// the process is gone — and a caller reached before Mount gets the default rather
// than an empty string it would have to branch on.
func URL() string {
	if u := strings.TrimSpace(os.Getenv(urlEnv)); u != "" {
		return u
	}
	port, err := clientPort()
	if err != nil {
		port = defaultPort
	}
	return "nats://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// clientPort resolves the port the embedded server binds and the default URL dials —
// ONE parse, so the server and its clients cannot land on different ports.
func clientPort() (int, error) {
	v := strings.TrimSpace(os.Getenv(portEnv))
	if v == "" {
		return defaultPort, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("bad %s %q: %w", portEnv, v, err)
	}
	return p, nil
}

// Mount starts the embedded PubSub server, binding NATS + JetStream in-process,
// and registers the tenant door (/v1/pubsub, typed.go) over it.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("pubsub.Mount: nil deps.Logger")
	}
	// A typed op is a route PLUS a registry entry, and the registry lives on
	// the App. A router that cannot reach it would serve every route with no
	// schema, no prose, no MCP tool and no SDK method — so the mount FAILS
	// rather than quietly publishing a surface no projection knows about.
	if cloud.ZipApp(app) == nil {
		return fmt.Errorf("pubsub.Mount: router is not a zip app, so the typed ops have no registry")
	}
	log := deps.Logger.New("subsystem", "pubsub")

	dataDir := firstNonEmpty(
		os.Getenv("CLOUD_PUBSUB_STORE_DIR"),
		filepath.Join(firstNonEmpty(deps.DataDir, "/var/lib/cloud"), "pubsub"),
	)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("pubsub.Mount: store dir %s: %w", dataDir, err)
	}

	port, err := clientPort()
	if err != nil {
		return fmt.Errorf("pubsub.Mount: %w", err)
	}

	// The bus's message-body ceiling. Zero means the embed default (8 MiB), which
	// is deliberately well above NATS's own 1 MiB: the Kafka-wire adaptor rides
	// this server, and its clients size themselves in MiB, so a 1 MiB bus caps
	// every one of them and the failure surfaces on the PRODUCER as
	// "Message size too large" — unfixable from the consumer side.
	var maxPayload int32
	if v := strings.TrimSpace(os.Getenv("CLOUD_PUBSUB_MAX_PAYLOAD")); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n <= 0 {
			return fmt.Errorf("pubsub.Mount: bad CLOUD_PUBSUB_MAX_PAYLOAD %q (want a positive byte count)", v)
		}
		maxPayload = int32(n)
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
		MaxPayload: maxPayload,
	})
	if err != nil {
		// Fail closed: a broken messaging plane must abort boot.
		return fmt.Errorf("pubsub.Mount: open embedded server (fail-closed): %w", err)
	}
	srv = s

	// The tenant door rides the server just bound: registered only once the
	// plane is UP, so a served route always has a real bus behind it.
	routes(app, &cloud.Service[state]{Base: cloud.NewBase(deps, "pubsub")})

	log.Info("pubsub embedded server serving", "client_url", s.ClientURL(), "store_dir", dataDir, "port", port, "door", "/v1/pubsub")
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

// Shutdown stops the door's client connection and the embedded server on
// graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	closeDoor()
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
