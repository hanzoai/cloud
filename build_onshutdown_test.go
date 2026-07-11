package cloud_test

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// noopMount mounts nothing: the fake specs below carry the behavior under test in
// their Shutdown, not their Mount.
func noopMount(any, cloud.Deps) error { return nil }

// freeAddr reserves an ephemeral loopback port and hands back its address; the
// listener is closed so the app under test can bind it.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// waitListening blocks until addr accepts a TCP connection (the app's Listen
// goroutine has bound it) or a short deadline elapses.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never listened on %s", addr)
}

// TestMountAll_ShutdownHooksLIFOAfterDrain proves the OnShutdown teardown wiring:
// MountAll registers each ENABLED subsystem's ShutdownFunc as a zip shutdown hook,
// so on app.Shutdown they run (1) AFTER in-flight requests drain and (2) LIFO =
// reverse-mount order (a dependency mounted before its dependents is torn down
// after them). That is exactly the contract the deleted hand-rolled reverse-loop
// provided by hand — now owned by zip, minus the teardown-before-drain race.
func TestMountAll_ShutdownHooksLIFOAfterDrain(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger(), DisableStartupMessage: true})

	// One monotonic tick stamps the ONLY two kinds of event we order against each
	// other: the in-flight request finishing its drain, and each teardown hook.
	var tick atomic.Int64
	var mu sync.Mutex
	teardown := map[string]int64{}
	record := func(name string) cloud.ShutdownFunc {
		return func(context.Context) error {
			mu.Lock()
			teardown[name] = tick.Add(1)
			mu.Unlock()
			return nil
		}
	}

	// Mount order a, b, c ⇒ LIFO teardown must be c, b, a.
	specs := []cloud.MountSpec{
		{Name: "a", Mount: noopMount, Shutdown: record("a")},
		{Name: "b", Mount: noopMount, Shutdown: record("b")},
		{Name: "c", Mount: noopMount, Shutdown: record("c")},
	}
	cfg := &cloud.Config{Enable: []string{"a", "b", "c"}}
	deps := cloud.Deps{Logger: luxlog.NewNoOpLogger()}

	// A request that parks inside its handler until released, so the shutdown drain
	// has something real to wait on. Its drain tick is stamped the instant the
	// handler returns — i.e. the moment this request finishes draining.
	entered := make(chan struct{})
	release := make(chan struct{})
	var drainTick int64
	app.Get("/hold", func(c *zip.Ctx) error {
		close(entered)
		<-release
		atomic.StoreInt64(&drainTick, tick.Add(1))
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})

	if err := cloud.MountAll(app, specs, cfg, deps); err != nil {
		t.Fatalf("MountAll: %v", err)
	}

	// Serve on a real loopback listener: an in-flight request over the HTTP
	// transport is what zip's shutdown actually drains (closeServers → drain) in
	// production — the path a listener-less test cannot exercise.
	addr := freeAddr(t)
	serveDone := make(chan error, 1)
	go func() { serveDone <- app.Listen("http://" + addr) }()
	waitListening(t, addr)

	// Fire the request; it parks mid-handler with the connection held open.
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/hold", nil)
		req.Close = true // no keep-alive: server drops the conn once the handler returns
		client := &http.Client{Timeout: 30 * time.Second}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered // the request is now in-flight, parked mid-handler

	// Shut down WHILE the request is in-flight. zip stops the listeners accepting,
	// drains (blocking on our parked handler), THEN runs the hooks LIFO.
	shutDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		shutDone <- app.ShutdownWithContext(ctx)
	}()

	// Barrier: with a request still in-flight, Shutdown MUST NOT complete — it is
	// blocked in the drain. If it returns here, teardown ran without waiting for the
	// drain (the exact race the OnShutdown move fixes).
	select {
	case <-shutDone:
		t.Fatal("Shutdown completed while a request was still in-flight — teardown did not wait for the drain")
	case <-time.After(150 * time.Millisecond):
	}

	// Let the handler finish so the drain can complete; hooks fire only after.
	close(release)

	if err := <-shutDone; err != nil {
		t.Fatalf("ShutdownWithContext: %v", err)
	}
	<-reqDone
	<-serveDone // Listen returns once the transport listener is closed

	// (1) AFTER THE DRAIN: the in-flight request finished (drainTick) strictly
	// before ANY teardown hook ran — no hook raced a request still using it.
	dt := atomic.LoadInt64(&drainTick)
	if dt == 0 {
		t.Fatal("in-flight request never drained")
	}
	for name, tk := range teardown {
		if tk < dt {
			t.Fatalf("hook %q ran at tick %d, before the drain finished at tick %d — teardown raced the still-draining request", name, tk, dt)
		}
	}

	// (2) LIFO = reverse mount order: c (mounted last) tears down first, a last.
	if len(teardown) != 3 {
		t.Fatalf("want 3 hooks run, got %d (%v)", len(teardown), teardown)
	}
	if !(teardown["c"] < teardown["b"] && teardown["b"] < teardown["a"]) {
		t.Fatalf("teardown not LIFO: a=%d b=%d c=%d (want c<b<a)", teardown["a"], teardown["b"], teardown["c"])
	}
}

// TestMountAll_ShutdownRegistration_EnablementAndNil proves MountAll registers a
// teardown hook ONLY for an ENABLED spec that HAS a ShutdownFunc: a disabled spec
// never mounts (so never registers a hook), and an enabled spec whose Shutdown is
// nil is skipped without panicking. This keeps the enablement axis and the nil
// guard from silently regressing now that teardown moved onto app.OnShutdown.
func TestMountAll_ShutdownRegistration_EnablementAndNil(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger(), DisableStartupMessage: true})

	var mu sync.Mutex
	var ran []string
	record := func(name string) cloud.ShutdownFunc {
		return func(context.Context) error {
			mu.Lock()
			ran = append(ran, name)
			mu.Unlock()
			return nil
		}
	}

	specs := []cloud.MountSpec{
		{Name: "enabled", Mount: noopMount, Shutdown: record("enabled")},
		{Name: "disabled", Mount: noopMount, Shutdown: record("disabled")},
		{Name: "nilsd", Mount: noopMount}, // enabled, but no Shutdown
	}
	cfg := &cloud.Config{Enable: []string{"enabled", "nilsd"}} // "disabled" omitted

	if err := cloud.MountAll(app, specs, cfg, cloud.Deps{Logger: luxlog.NewNoOpLogger()}); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if len(ran) != 1 || ran[0] != "enabled" {
		t.Fatalf("teardown hooks ran = %v, want [enabled] (disabled never mounts; nil Shutdown skipped)", ran)
	}
}
