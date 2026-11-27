package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/manifest"
)

// wire.go — the THIRD entry point to a lazy app: a TCP port that speaks a
// protocol of the app's own, not HTTP or ZAP.
//
// kafka's clients (the datastore's Kafka engine tables, insights, apps/event's
// producer) dial :9092 and speak Kafka's binary protocol. They never call
// /v1/kafka, so a kafka that waited for a request there would never start, and a
// kafka that bound :9092 itself had to be Eager: a resident process on every pod
// whether or not anything spoke Kafka to it. The host owns the port instead
// (manifest App.Wire). A connection is the trigger: App.Start brings the app up
// through the same single-flighted path a first request takes, and the host
// relays bytes to the app's own listener at its zip socket + manifest.WireSuffix.
//
// A connection the host has accepted is not refused for being early. The client
// waits while the app starts — its first request sits in the socket buffer — and
// proceeds once the relay is up. Only a start that fails closes it, and the next
// connection tries the start again.
//
// zip's idle clock counts what zip routes, and a relayed connection is not routed
// through it. So the host touches the app (App.Start on a running plugin is a
// lookup and one store) as requests flow, and on a ticker while any connection is
// open, so neither IdleAfter nor the Warm ceiling stops an app its clients are
// still connected to. When the last connection closes, the app goes idle like any
// lazy app.

// serveWire binds every app's Wire port. A port that will not bind is logged and
// skipped: the host and every other app still serve, and the app's clients get
// connection refused, which says what happened.
func serveWire(app *zip.App) {
	for _, a := range manifest.Apps {
		if a.Wire == 0 || a.Coresident {
			continue
		}
		port := a.Port()
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			app.Logger().Error("wire port is not bound: its clients cannot reach the app",
				"app", a.Name, "port", port, "err", err)
			continue
		}
		d := newDoor(a.Name, ln, app.Start, app.Logger())
		app.OnShutdown(d.close)
		go d.accept()
		go d.keep(manifest.Idle() / 4)
		app.Logger().Info("wire port listening", "app", a.Name, "port", port)
	}
}

// door is one app's Wire port and the connections relayed through it.
type door struct {
	name string
	ln   net.Listener
	stop chan struct{}
	log  luxlog.Logger

	// start is zip's App.Start: it brings the app up if it is down, marks it
	// used if it is running, and answers the socket it serves on.
	start func(name string) (string, error)

	// last is when a request through this door last touched the app, so a busy
	// connection costs one plugin lookup a second rather than one per request.
	last atomic.Int64

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newDoor(name string, ln net.Listener, start func(string) (string, error), log luxlog.Logger) *door {
	return &door{name: name, ln: ln, start: start, log: log, conns: map[net.Conn]struct{}{}, stop: make(chan struct{})}
}

func (d *door) accept() {
	for {
		c, err := d.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			d.log.Error("wire accept", "app", d.name, "err", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		go d.relay(c)
	}
}

// relay starts the app if it is not running, connects c to the app's own
// listener, and copies both ways until either side ends.
func (d *door) relay(c net.Conn) {
	if !d.track(c) {
		_ = c.Close()
		return
	}
	defer d.untrack(c)

	sock, err := d.start(d.name)
	if err != nil {
		d.log.Error("wire connection closed: the app would not start", "app", d.name, "err", err)
		return
	}
	// A path is a child this host started. Anything else is an instance named by
	// CLOUD_<NAME>_ADDR, which serves its wire port where it runs.
	if !filepath.IsAbs(sock) {
		d.log.Error("wire connection closed: the app runs elsewhere and serves its own port", "app", d.name, "addr", sock)
		return
	}
	u, err := dialWire(sock+manifest.WireSuffix, startTimeout(), d.stop)
	if err != nil {
		d.log.Error("wire connection closed: the app's listener is not there", "app", d.name, "err", err)
		return
	}
	if !d.track(u) {
		_ = u.Close()
		return
	}
	defer d.untrack(u)

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(u, touching{c, d}); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, u); done <- struct{}{} }()
	<-done
	// One side ended: the other copy is blocked on a connection only closing it
	// can wake, and the deferred untracks close both.
}

// dialWire connects to an app's wire listener. The app opens it before its zip
// socket accepts, so it is there once Start returns; the retry covers a child
// replaced between the two.
func dialWire(path string, limit time.Duration, stop <-chan struct{}) (net.Conn, error) {
	deadline := time.Now().Add(limit)
	for {
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-stop:
			return nil, err
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// touching is a client connection's request side: every read is the app in use.
type touching struct {
	net.Conn
	d *door
}

func (t touching) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.d.touch()
	}
	return n, err
}

// touch marks the app used, at most once a second.
func (d *door) touch() {
	now := time.Now().UnixNano()
	if last := d.last.Load(); now-last < int64(time.Second) || !d.last.CompareAndSwap(last, now) {
		return
	}
	_, _ = d.start(d.name)
}

// keep touches the app on every tick while a connection is open, for a client
// that holds one quietly for longer than the app's idle bound.
func (d *door) keep(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			d.mu.Lock()
			open := len(d.conns) > 0
			d.mu.Unlock()
			if open {
				_, _ = d.start(d.name)
			}
		}
	}
}

// track records c so a shutdown can close it, and refuses once the door is closed.
func (d *door) track(c net.Conn) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conns == nil {
		return false
	}
	d.conns[c] = struct{}{}
	return true
}

func (d *door) untrack(c net.Conn) {
	_ = c.Close()
	d.mu.Lock()
	delete(d.conns, c)
	d.mu.Unlock()
}

// close stops the door and every connection through it. It runs among the host's
// shutdown hooks ahead of the ones that stop the children, so a client's
// connection ends here rather than as a reset from a dying child.
func (d *door) close(context.Context) error {
	d.mu.Lock()
	conns := d.conns
	d.conns = nil
	d.mu.Unlock()
	if conns == nil {
		return nil // already closed
	}
	close(d.stop)
	err := d.ln.Close()
	for c := range conns {
		_ = c.Close()
	}
	return err
}
