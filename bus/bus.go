// Package bus is how a process TALKS to the message bus. It does not run one.
//
// This surface lived in apps/pubsub beside the embedded NATS server, so the six
// apps that wanted only the bus ADDRESS had to import the app that embeds a
// broker in order to get it. Measured, that single edge put
// github.com/hanzoai/pubsub/server — a whole NATS server and everything it links
// — into 27 plugin binaries, and boot time across this fleet tracks binary size
// almost linearly. apps/event imported it for exactly two values: URL() and
// TenantPrefix.
//
// So the bus is two things and they are separated. apps/pubsub RUNS one and is
// linked by plugin/pubsub alone; this package ADDRESSES one and is linked by
// everybody. Nothing here can start a server, which is the property that keeps
// the separation true as callers are added.
package bus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
)

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
	port, err := Port()
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
func Port() (int, error) {
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
	if p := here(); p != nil && environ.Or(urlEnv, "") == "" {
		return nats.Connect("", append(opts, nats.InProcessServer(p))...)
	}
	return nats.Connect(URL(), opts...)
}

// disconnect releases the connection on Shutdown, so a remount dials the new
// server instead of a dead pipe.
func Disconnect() {
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
	return Phys(org, name), true
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
func Phys(org, name string) string { return TenantPrefix + org + "-" + name }

// Serving states that this process runs the bus, so a dial can reach it in memory
// instead of over TCP. apps/pubsub calls it when it starts a server and calls it
// with nil when it stops one.
//
// It is a HOOK rather than an import because the direction matters: a client that
// imported the server to ask this question would link the server, which is the
// whole thing this package exists to avoid. The server knows it is running; it
// says so.
func Serving(p nats.InProcessConnProvider) {
	local.Lock()
	defer local.Unlock()
	local.p = p
}

// here answers the in-process server, or nil when this process runs none.
func here() nats.InProcessConnProvider {
	local.Lock()
	defer local.Unlock()
	return local.p
}

var local struct {
	sync.Mutex
	p nats.InProcessConnProvider
}
