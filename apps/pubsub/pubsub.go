package pubsub

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/internal/driver/pubsub"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/env"
	psembed "github.com/hanzoai/pubsub/embed"
)

// Mount order is the row position in manifest/apps.go: this infrastructure data
// plane must bind BEFORE apps/kafka dials it. Its HTTP routes are its own
// prefix (/v1/pubsub), so the position only fixes the pubsub-before-kafka
// mount sequence.

// srv holds the running embedded server so shutdown can stop it. Set once by Mount.
var srv *psembed.Server

// Mount starts the embedded PubSub server, binding NATS + JetStream in-process,
// and registers the tenant endpoint (/v1/pubsub, typed.go) over it.
func Use(app cloud.Router, deps cloud.Deps) error {
	// A typed op is a route PLUS a registry entry, and the registry lives on
	// the App. A router that cannot reach it would serve every route with no
	// schema, no prose, no MCP tool and no SDK method — so the mount FAILS
	// rather than quietly publishing a surface no projection knows about.
	log := luxlog.Default().New("subsystem", "pubsub")

	// THE PLANE IS SOMEWHERE ELSE. CLOUD_PUBSUB_URL names the deployment's shared
	// hanzoai/pubsub cluster, so every pod sees the same streams and consumers and none
	// holds state of its own: this pod binds no port and keeps no store. What stays is
	// the tenant endpoint, which rides that cluster like every other client. The
	// embedded server is for a laptop and a single node: no CLOUD_PUBSUB_URL.
	if bus.Remote() {
		routes(app, &cloud.Service[state]{Base: cloud.NewBase(deps, "pubsub")})
		log.Info("pubsub rides the shared cluster; no embedded server", "url", bus.Display(), "door", "/v1/pubsub")
		return nil
	}

	dataDir := env.Or("CLOUD_PUBSUB_STORE_DIR",
		filepath.Join(cmp.Or(strings.TrimSpace(cloud.DataDir()), "/var/lib/cloud"), "pubsub"))
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("pubsub.Use:  store dir %s: %w", dataDir, err)
	}

	port, err := bus.Port()
	if err != nil {
		return fmt.Errorf("pubsub.Use:  %w", err)
	}

	// The bus's message-body ceiling. Zero means the embed default (8 MiB), which
	// is deliberately well above NATS's own 1 MiB: the Kafka-wire adaptor rides
	// this server, and its clients size themselves in MiB, so a 1 MiB bus caps
	// every one of them and the failure surfaces on the PRODUCER as
	// "Message size too large" — unfixable from the consumer side.
	var maxPayload int32
	if v := env.Or("CLOUD_PUBSUB_MAX_PAYLOAD", ""); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n <= 0 {
			return fmt.Errorf("pubsub.Use:  bad CLOUD_PUBSUB_MAX_PAYLOAD %q (want a positive byte count)", v)
		}
		maxPayload = int32(n)
	}

	// The file store the plane may hold, when the deployment states one. See budget.go.
	store, err := declared()
	if err != nil {
		return fmt.Errorf("pubsub.Use:  %w", err)
	}

	host := env.Or("CLOUD_PUBSUB_HOST", "0.0.0.0")

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
	// An ephemeral port has nothing to probe: the kernel picks one that is free
	// by definition, so the pre-flight below would be asking whether -1 is taken.
	if port > 0 {
		if err := claimable(host, port); err != nil {
			return fmt.Errorf("pubsub.Use:  cannot bind %s (fail-closed): %w", net.JoinHostPort(host, strconv.Itoa(port)), err)
		}
	}

	s, err := psembed.Open(psembed.Options{
		Host:       host,
		Port:       port,
		ServerName: env.Or("CLOUD_PUBSUB_SERVER_NAME", "cloud-pubsub-"+cmp.Or(strings.TrimSpace(cloud.Brand()), "hanzo")),
		StoreDir:   dataDir,
		MaxPayload: maxPayload,
	})
	if err != nil {
		// Fail closed: a broken messaging plane must abort boot.
		return fmt.Errorf("pubsub.Use:  open embedded server (fail-closed): %w", err)
	}
	srv = s
	// State what the plane can hold before anything is told it is serving, so the
	// first stream declared on it is sized for this node. A limit the server will
	// not take is a plane whose streams would be refused one by one: boot stops.
	held, err := budget(s.NATS(), store)
	if err != nil {
		srv = nil
		s.Shutdown()
		return fmt.Errorf("pubsub.Use:  the store budget (fail-closed): %w", err)
	}
	// Say so, so a co-resident client dials this server in memory rather than over
	// TCP. bus cannot ask — asking would mean importing this package, which is the
	// import that put a NATS server in 27 binaries.
	pubsub.Serving(s.NATS())

	// The tenant endpoint rides the server just bound: registered only once the
	// plane is UP, so a served route always has a real bus behind it.
	routes(app, &cloud.Service[state]{Base: cloud.NewBase(deps, "pubsub")})

	log.Info("pubsub embedded server serving", "client_url", s.ClientURL(), "store_dir", dataDir, "port", port, "door", "/v1/pubsub",
		"store_limit", held, "store_declared", store)
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

// Shutdown stops the cached client connection and the embedded server on
// graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	bus.Disconnect()
	if srv != nil {
		// Withdraw the in-process route BEFORE stopping the server, or a dial
		// racing this shutdown reaches a server that is going away.
		pubsub.Serving(nil)
		srv.Shutdown()
		srv = nil
	}
	return nil
}

// ----- riding the plane -----------------------------------------------------
//
// ONE plane, more than one product on it. Everything below is what an app on
// this plane needs and nothing else: how to REACH it, WHO is asking, what the
// caller's name is called out there, and what a refusal from it means on the
// wire. Four calls, four separate questions.
//
// They are exported because apps/kv is the second tenant endpoint on this same
// plane. Key-value is not messaging — a bucket holds values and answers reads;
// nothing about it publishes, subscribes or waits for a reply — so it is its own
// capability at its own address under its own name. But it is the same bus: it
// dials the server THIS package runs, through the dialer below, rather than
// starting a second one. The alternative was a second copy of the tenancy rule
// and a second connection policy, free to drift from these on the day one of
// them changed.
