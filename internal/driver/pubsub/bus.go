package pubsub

// bus.go — the driver for sdk/bus: this process's one connection to the plane, on the
// hanzoai/pubsub cluster CLOUD_PUBSUB_URL names, or in memory when this process runs
// the embedded server. Everything says and hears through sdk/bus; the apps whose
// product IS the bus take JetStream from the stream subpackage, which is kept apart
// so the light host links the connection and not the JetStream client.

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/nats-io/nats.go"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/bus"
)

func init() {
	bus.Register(bus.Driver{
		Connect: func() (bus.Conn, error) {
			nc, err := Conn()
			if err != nil {
				return nil, err
			}
			return conn{nc}, nil
		},
		Disconnect: Disconnect,
	})
}

// conn is the plane's connection, as sdk/bus sees it.
type conn struct{ nc *nats.Conn }

func (c conn) Publish(subject string, data []byte) error {
	if err := c.nc.Publish(subject, data); err != nil {
		return err
	}
	return c.nc.Flush()
}

func (c conn) Subscribe(subject string, msgs chan<- bus.Message) (bus.Subscription, error) {
	return c.nc.Subscribe(subject, func(m *nats.Msg) {
		select {
		case msgs <- bus.Message{Subject: m.Subject, Data: m.Data}:
		default: // too slow: dropped, as a channel subscription drops
		}
	})
}

// held is this process's ONE client connection to the plane, dialed on first use and
// reused by every product riding it. Guarded so a burst of concurrent first requests
// dials once; re-dials after a close rather than latching.
var held struct {
	mu sync.Mutex
	nc *nats.Conn
}

// Conn returns the process's live connection to the plane, dialed on first use. It
// answers 503 rather than an error the caller has to translate. The JetStream
// handle for the apps whose product is the bus itself is stream.Bus, which rides
// this connection.
func Conn() (*nats.Conn, error) {
	held.mu.Lock()
	defer held.mu.Unlock()
	if held.nc == nil || held.nc.IsClosed() {
		nc, err := dial()
		if err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "bus unreachable: %v", err)
		}
		held.nc = nc
	}
	return held.nc, nil
}

// dial opens that connection. In-process when THIS process runs the embedded server —
// no TCP, and correct even when a test binds an ephemeral port. Over bus.URL when it
// does not, which is every product that rides the plane from its own binary: one
// server, dialed at the one address the knob names.
//
// An explicit CLOUD_PUBSUB_URL wins over the in-process server, because setting it is
// how a deployment says the plane is somewhere else.
func dial() (*nats.Conn, error) {
	// The connection's name is what an operator reads in connz, so it is the binary
	// asking rather than a literal that would say "pubsub" for kv.
	opts := []nats.Option{nats.Name(filepath.Base(os.Args[0])), nats.MaxReconnects(-1)}
	if p := here(); p != nil && !bus.Remote() {
		return nats.Connect("", append(opts, nats.InProcessServer(p))...)
	}
	return nats.Connect(bus.URL(), opts...)
}

// Disconnect releases the connection on Shutdown, so a remount dials the new server
// instead of a dead pipe.
func Disconnect() {
	held.mu.Lock()
	defer held.mu.Unlock()
	if held.nc != nil {
		held.nc.Close()
		held.nc = nil
	}
}

// Serving states that this process runs the bus, so a dial can reach it in memory
// instead of over TCP. apps/pubsub calls it when it starts a server and calls it with
// nil when it stops one.
//
// It is a HOOK rather than an import because the direction matters: a client that
// imported the server to ask this question would link the server, which is the whole
// thing this driver exists to avoid. The server knows it is running; it says so.
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
