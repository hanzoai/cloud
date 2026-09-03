// Package pubsub is your message bus: publish, subscribe, and durable streams
// your apps read at their own pace.
//
// It is the platform message bus: publish/subscribe messaging and durable
// JetStream streams, served to tenants at /v1/pubsub over the embedded Hanzo
// PubSub (NATS + JetStream) node this same package runs.
//
// The node binds the NATS client port (default :4222) and serves JetStream
// over the cloud data dir — the ONE durable log every other app publishes
// facts onto and consumes them from. The Kafka-wire adaptor (apps/kafka) and
// any in-cluster NATS/Kafka client talk to it. It is a single embedded node
// running JetStream over the local file store — there is NO ZooKeeper, raft,
// or etcd in the path (Lux consensus only; the optional Quasar PQ control
// plane is a follow-up, see github.com/hanzoai/pubsub/embed).
//
// ONE bus, MANY endpoints. The NATS port is the cluster's listener: in-process
// apps and in-cluster clients, unscoped. /v1/pubsub is this app's tenant
// endpoint: publish and request/reply (typed.go), each org confined to its own
// namespace by the validated principal, never by anything a caller asserts.
// apps/kv is a second tenant endpoint on the SAME plane — a bucket is not a
// message, so it answers under its own name — and it rides the four calls
// under "riding the plane" below rather than opening a bus of its own. Cloud's
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
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	luxlog "github.com/luxfi/log"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
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
	if u := environ.Or(urlEnv, ""); u != "" {
		return u
	}
	port, err := clientPort()
	if err != nil || port <= 0 {
		// -1 is the server's "any free port" and is not an address: a client
		// dialing an ephemeral bus is told where it landed through
		// CLOUD_PUBSUB_URL, and one that was told nothing dials the default.
		port = defaultPort
	}
	return "nats://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// clientPort resolves the port the embedded server binds and the default URL dials —
// ONE parse, so the server and its clients cannot land on different ports.
//
// 0 means ANY FREE PORT, and it is spelled -1 to the library, which reads 0 as
// "use my default" and lands back on 4222. It exists for the same reason
// GIT_SSH_ADDR=127.0.0.1:0 does in mk/plugin.mk: describing an app MOUNTS it,
// mounting this one binds a real bus, and a projection of the route table is no
// reason to contend for a fixed port with whatever else is on the host. Without
// it two describes on one machine cannot run at once — and the second fails as a
// bare make Error 2 that names no app, because the parallel run swallows which
// target died.
func clientPort() (int, error) {
	v := environ.Or(portEnv, "")
	if v == "" {
		return defaultPort, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("bad %s %q: %w", portEnv, v, err)
	}
	if p == 0 {
		return -1, nil
	}
	return p, nil
}

// Mount starts the embedded PubSub server, binding NATS + JetStream in-process,
// and registers the tenant endpoint (/v1/pubsub, typed.go) over it.
func Use(app cloud.Router, deps cloud.Deps) error {
	// A typed op is a route PLUS a registry entry, and the registry lives on
	// the App. A router that cannot reach it would serve every route with no
	// schema, no prose, no MCP tool and no SDK method — so the mount FAILS
	// rather than quietly publishing a surface no projection knows about.
	if cloud.ZipApp(app) == nil {
		return fmt.Errorf("pubsub.Use:  router is not a zip app, so the typed ops have no registry")
	}
	log := luxlog.Default().New("subsystem", "pubsub")

	dataDir := environ.Or("CLOUD_PUBSUB_STORE_DIR",
		filepath.Join(cmp.Or(strings.TrimSpace(cloud.DataDir()), "/var/lib/cloud"), "pubsub"))
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("pubsub.Use:  store dir %s: %w", dataDir, err)
	}

	port, err := clientPort()
	if err != nil {
		return fmt.Errorf("pubsub.Use:  %w", err)
	}

	// The bus's message-body ceiling. Zero means the embed default (8 MiB), which
	// is deliberately well above NATS's own 1 MiB: the Kafka-wire adaptor rides
	// this server, and its clients size themselves in MiB, so a 1 MiB bus caps
	// every one of them and the failure surfaces on the PRODUCER as
	// "Message size too large" — unfixable from the consumer side.
	var maxPayload int32
	if v := environ.Or("CLOUD_PUBSUB_MAX_PAYLOAD", ""); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n <= 0 {
			return fmt.Errorf("pubsub.Use:  bad CLOUD_PUBSUB_MAX_PAYLOAD %q (want a positive byte count)", v)
		}
		maxPayload = int32(n)
	}

	host := environ.Or("CLOUD_PUBSUB_HOST", "0.0.0.0")

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
		ServerName: environ.Or("CLOUD_PUBSUB_SERVER_NAME", "cloud-pubsub-"+cmp.Or(strings.TrimSpace(cloud.Brand()), "hanzo")),
		StoreDir:   dataDir,
		MaxPayload: maxPayload,
	})
	if err != nil {
		// Fail closed: a broken messaging plane must abort boot.
		return fmt.Errorf("pubsub.Use:  open embedded server (fail-closed): %w", err)
	}
	srv = s

	// The tenant endpoint rides the server just bound: registered only once the
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

// Shutdown stops the cached client connection and the embedded server on
// graceful cloud shutdown. Idempotent.
func Shutdown(_ context.Context) error {
	disconnect()
	if srv != nil {
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

// conn holds this process's ONE client connection to the plane, dialed on first
// use and reused by every product riding it. Guarded so a burst of concurrent
// first requests dials once; re-dials after a close rather than latching.
var conn struct {
	mu sync.Mutex
	nc *nats.Conn
}

// Bus returns the live JetStream handle and connection on THE one plane.
//
// It answers 503 rather than an error the caller has to translate, because
// every caller is a typed op and there is one honest answer to "the bus is not
// reachable": the endpoint is up, the plane behind it is not.
func Bus() (jetstream.JetStream, *nats.Conn, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.nc == nil || conn.nc.IsClosed() {
		nc, err := dial()
		if err != nil {
			return nil, nil, zip.Errorf(http.StatusServiceUnavailable, "bus unreachable: %v", err)
		}
		conn.nc = nc
	}
	js, err := jetstream.New(conn.nc)
	if err != nil {
		return nil, nil, zip.Errorf(http.StatusServiceUnavailable, "bus unreachable: %v", err)
	}
	return js, conn.nc, nil
}

// dial opens that connection. In-process when THIS process runs the embedded
// server — no TCP, and correct even when a test binds an ephemeral port. Over
// [URL] when it does not, which is every product that rides the plane from its
// own binary: one server, dialed at the one address the knob names.
//
// An explicit CLOUD_PUBSUB_URL wins over the in-process server, because setting
// it is how a deployment says the plane is somewhere else.
func dial() (*nats.Conn, error) {
	// The connection's name is what an operator reads in connz, so it is the
	// binary asking rather than a literal that would say "pubsub" for kv.
	opts := []nats.Option{nats.Name(filepath.Base(os.Args[0])), nats.MaxReconnects(-1)}
	if srv != nil && environ.Or(urlEnv, "") == "" {
		return nats.Connect("", append(opts, nats.InProcessServer(srv.NATS()))...)
	}
	return nats.Connect(URL(), opts...)
}

// disconnect releases the connection on Shutdown, so a remount dials the new
// server instead of a dead pipe.
func disconnect() {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.nc != nil {
		conn.nc.Close()
		conn.nc = nil
	}
}

// TenantPrefix marks every tenant-created stream and KV bucket, keeping the
// tenant plane disjoint by construction from the platform's own streams (EVENT
// et al.), which never carry it.
//
// It is EXPORTED for the one question a platform subsystem must be able to ask
// before it removes a stream it did not create: is this a tenant's? Asking the
// prefix's OWNER is what keeps that check from becoming a second "t-" literal
// somewhere else, which is how the two would drift apart.
const TenantPrefix = "t-"

// Org resolves the VALIDATED org — the tenant-isolation key — from the context
// cloud.Bridge parked it on. It is never an In field: an In field is
// caller-supplied, so a tenant key read from one would be a cross-tenant read
// the caller asserted for itself. Fails closed off the HTTP path with the same
// 403 every data plane answers.
//
// The org id must also be usable as a NATS subject token and name fragment; one
// that is not (outside [A-Za-z0-9_-], or over 64 bytes) is refused rather than
// mangled — a mangling could collide two orgs onto one namespace.
func Org(ctx context.Context) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	if !token(org, true) {
		return "", zip.ErrForbidden("org id is not addressable on the bus")
	}
	return org, nil
}

// Qualify is the plane-wide name of one org's stream or bucket, and reports
// whether the caller's name can have one.
//
// The two answers are one call because they are one rule: the physical name is
// "t-<org>-<name>", so it decodes to exactly one (org, name) pair ONLY while the
// caller's half carries no dash. Splitting them into a validator and a formatter
// is how a caller ends up formatting a name it never checked. What the refusal
// MEANS is the caller's to choose — 400 on a create, 404 on a read, so a bad
// name never tells one org that another org's bucket exists.
func Qualify(org, name string) (string, bool) {
	if !token(name, false) {
		return "", false
	}
	return phys(org, name), true
}

// Err maps the plane's own refusals onto the wire honestly: absence is 404, a
// name already taken is 409, a config JetStream refuses is the 4xx it reports,
// and only a genuinely broken bus is a 5xx.
func Err(err error) error {
	var apiErr *jetstream.APIError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jetstream.ErrStreamNotFound):
		return zip.ErrNotFound("stream not found")
	case errors.Is(err, jetstream.ErrConsumerNotFound):
		return zip.ErrNotFound("consumer not found")
	case errors.Is(err, jetstream.ErrBucketNotFound):
		return zip.ErrNotFound("bucket not found")
	case errors.Is(err, jetstream.ErrKeyNotFound):
		return zip.ErrNotFound("key not found")
	case errors.Is(err, jetstream.ErrStreamNameAlreadyInUse):
		return zip.Errorf(http.StatusConflict, "stream name already in use")
	case errors.Is(err, jetstream.ErrConsumerExists):
		return zip.Errorf(http.StatusConflict, "consumer already exists")
	case errors.Is(err, jetstream.ErrBucketExists):
		return zip.Errorf(http.StatusConflict, "bucket already exists")
	case errors.As(err, &apiErr):
		if apiErr.Code >= 400 && apiErr.Code < 500 {
			return zip.Errorf(apiErr.Code, "%s", apiErr.Description)
		}
		return zip.Errorf(http.StatusInternalServerError, "bus: %s", apiErr.Description)
	case errors.Is(err, context.DeadlineExceeded):
		return zip.Errorf(http.StatusGatewayTimeout, "bus timeout")
	default:
		return zip.Errorf(http.StatusInternalServerError, "bus: %v", err)
	}
}

// token reports whether s is 1–64 bytes of [A-Za-z0-9_], plus '-' when dash is
// allowed. Stream and bucket names refuse the dash so the physical name
// "t-<org>-<name>" splits at its LAST dash into exactly one (org, name) pair.
func token(s string, dash bool) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		case c == '-' && dash:
		default:
			return false
		}
	}
	return true
}

// phys is the physical (whole-plane) name of an org's stream or bucket.
func phys(org, name string) string { return TenantPrefix + org + "-" + name }
