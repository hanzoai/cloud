package main

// wake_test.go — a LAZY app, reached over the plane, with nothing but a plane call
// to start it.
//
// This is task #108 and it is the precondition for every internal call in the
// fleet: 106 of 112 apps start on their first prefix request, and a plane call goes
// straight to the app's canonical socket without touching the router. So the app was
// never started, the socket was never bound, and the caller dialled a path that does
// not exist — correct by design, inert in practice. Nothing in this repo called
// zip.App.Start, the second door zip added for exactly this.
//
// Both halves are real here: a real host with a real lazy plugin, a real child
// PROCESS (this test binary, re-execed), the real start door, and the real
// cloud.Peer on the caller's side. The only thing stubbed is what the child serves,
// because what it serves is not what is under test — that it is RUNNING is.

import (
	"github.com/hanzoai/cloud/internal/planetest"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const wakeChildEnv = "CLOUD_WAKE_CHILD"

// TestMain lets this binary act as the lazy plugin the host starts.
//
// The child binds TWO sockets, exactly as cloud.Listen does and in the same order:
// its plane socket first, then the private one the host handed it on ZIP_ADDR. That
// order is the whole reason a woken peer is reachable the instant Start returns —
// the host waits on the second, so the first is already accepting.
func TestMain(m *testing.M) {
	name := strings.TrimSpace(os.Getenv(wakeChildEnv))
	if name == "" {
		os.Exit(m.Run())
	}
	app := zip.New(zip.Config{AppName: name})
	// The op goes on cloud.Plane(), which is what ServePlane serves — the same
	// place every real app declares its internal surface.
	zip.Post[struct{}, plane.Started](cloud.Plane(), "/alive",
		func(context.Context, *struct{}) (*plane.Started, error) {
			return &plane.Started{Addr: name}, nil
		}, zip.WithOperationID("wake_alive"))

	stop, err := cloud.ServePlane(name, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child ServePlane:", err)
		os.Exit(1)
	}
	defer func() { _ = stop() }()
	if err := app.Listen(zip.Addr("")); err != nil {
		fmt.Fprintln(os.Stderr, "child listen:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// router builds a host with one LAZY plugin — this binary — and opens the start
// door on it. Nothing is running when it returns; that is the point.
func router(t *testing.T, name string) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	cloud.ResetPlane()

	app := zip.New(zip.Config{AppName: "cloud", Logger: luxlog.New("test")})
	leaf, err := zip.Load(zip.Plugin{
		Name: name, Path: os.Args[0], Lazy: true,
		Env:   []string{wakeChildEnv + "=" + name, "ZIP_RUNTIME_DIR=" + os.Getenv("ZIP_RUNTIME_DIR")},
		Start: 60 * time.Second,
	}, "/v1/"+name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	app.Use(leaf)
	t.Cleanup(func() { _ = app.Shutdown() })

	// Called exactly as run() calls it: no handle, torn down by the app's own
	// shutdown hooks. If this ever grows a return value again, the caller in
	// main.go is one `defer f()()` away from never opening the door at all.
	// The agent door rides the same socket, over this host's own children —
	// which is one lazy plugin here, and none of it is what this file tests.
	serveWake(app, fleet.Mount(app, manifest.MCPPath, routed([]string{name}), locate(app)))
	waitFor(t, zip.SocketPath(plane.HostApp))
}

// runDir is the plane's run directory for one test, and it is SHORT on purpose: a
// unix socket path is capped near 104 bytes, and t.TempDir() spends most of that
// budget on the test's own name — so a long test name binds nothing and fails with
// "invalid argument" about something the test is not about.
func waitFor(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never accepted", path)
}

// TestWakeStartsALazyAppForAPlaneCall is the whole of #108. The app is cold, no
// request has ever reached its prefix, and a plane call alone brings it up and gets
// an answer.
func TestWakeStartsALazyAppForAPlaneCall(t *testing.T) {
	const name = "sleepy"
	router(t, name)

	// COLD: nothing has started it, so its socket does not exist. State the
	// precondition, or the test could pass against an app that was already up.
	if _, err := os.Stat(zip.SocketPath(name)); err == nil {
		t.Fatal("the app was already running; this test proves nothing")
	}

	out, err := cloud.Ask[struct{}, plane.Started](context.Background(), name, "wake_alive", &struct{}{})
	if err != nil {
		t.Fatalf("a plane call to a lazy app failed: %v", err)
	}
	if out == nil || out.Addr != name {
		t.Fatalf("woke something that is not %s: %+v", name, out)
	}
	if _, err := os.Stat(zip.SocketPath(name)); err != nil {
		t.Fatalf("the call was answered but no socket was bound: %v", err)
	}

	// Idempotent: a second call reuses the child rather than starting another.
	if _, err := cloud.Ask[struct{}, plane.Started](context.Background(), name, "wake_alive", &struct{}{}); err != nil {
		t.Fatalf("second call to a woken app failed: %v", err)
	}
	if n := running(t); n != 1 {
		t.Fatalf("%d children running, want exactly 1 — waking is not idempotent", n)
	}
}

// running counts this process's live children, which is how e2e/mcp-door.sh counts
// woken plugins: a wake that spawns two is a wake that will spawn a hundred.
func running(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", fmt.Sprint(os.Getpid())).Output()
	if err != nil && len(out) == 0 {
		return 0
	}
	return len(strings.Fields(string(out)))
}

// TestWakeRefusesAnAppThisFleetDoesNotRun is the other half, and the one the price
// table depends on: an app that is not in the manifest must be a 404 the caller can
// read as "not deployed here", never a start failure it must treat as an outage.
// x402 reads exactly this distinction to decide whether an absent marketplace means
// "nothing is priced" or "refuse everything".
func TestWakeRefusesAnAppThisFleetDoesNotRun(t *testing.T) {
	router(t, "sleepy")

	_, err := cloud.Ask[struct{}, plane.Started](context.Background(), "nosuchapp", "wake_alive", &struct{}{})
	if err == nil {
		t.Fatal("calling an app this fleet does not run SUCCEEDED")
	}
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Fatalf("an unknown app must answer ErrNoPeer so a caller can fall back, got: %v", err)
	}
}

// TestWakeWithNoRouterIsNoPeer pins the developer's case, and it is what keeps every
// unit test in the fleet honest: with no router in the process tree there is nothing
// that can start anything, so "not deployed here" is simply true — and it is answered
// at once, from a connect that is refused, never a timeout.
func TestWakeWithNoRouterIsNoPeer(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	cloud.ResetPlane()

	_, err := cloud.Ask[struct{}, plane.Started](context.Background(), "sleepy", "wake_alive", &struct{}{})
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Fatalf("no socket and no router must be ErrNoPeer, got: %v", err)
	}
}

// TestWakeAgainstAnOlderRouterIsAnOutage is the ROLLING DEPLOY, and it is the case
// that made carrying this fact on a status code untenable.
//
// A host pod on a build that predates the start door still serves a plane socket —
// it just has no host_start on it — so the call is answered "unknown op", which on
// the wire is a 404. The router's own "this fleet runs no such app" was ALSO a 404,
// and both rebuild into the same *HTTPError with an empty Code, so nothing but the
// message text told them apart.
//
// Read as absence, that skew says "this fleet prices nothing" — and the payment rail
// serves every priced tool free for as long as one old pod is still routing. So the
// absence now travels as a FIELD on a 200, and a router that cannot answer this op
// cannot claim anything about the fleet.
func TestWakeAgainstAnOlderRouterIsAnOutage(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	cloud.ResetPlane()

	// A router of the previous generation: a plane socket at the host's name, with
	// some other op on it and no host_start.
	old := zip.New(zip.Config{AppName: "plane", Logger: luxlog.New("test")})
	zip.Post[struct{}, plane.Started](old, "/host/other",
		func(context.Context, *struct{}) (*plane.Started, error) { return &plane.Started{}, nil },
		zip.WithOperationID("host_other"))
	go func() { _ = old.Listen(zip.SocketPath(plane.HostApp)) }()
	t.Cleanup(func() { _ = old.Shutdown() })
	waitFor(t, zip.SocketPath(plane.HostApp))

	_, err := cloud.Ask[struct{}, plane.Started](context.Background(), "sleepy", "wake_alive", &struct{}{})
	if err == nil {
		t.Fatal("a call through a router that cannot start anything SUCCEEDED")
	}
	if errors.Is(err, cloud.ErrNoPeer) {
		t.Fatalf("version skew read as \"not deployed here\" — a rail believing this serves "+
			"every priced tool free until the last old pod turns over: %v", err)
	}
}

// TestWakeStartsALazyAppBehindALeftoverSocket is the OUTAGE, end to end, in the shape
// prod ran it.
//
// The cloud pod keeps its run directory on a volume, so /var/lib/cloud/run survives the
// pod that wrote it. commerce is lazy; a previous pod left commerce.sock behind and
// nothing unlinked it. reach() decided the peer was up by STAT'ing that file, so the
// wake above never ran, the dial hit a kernel with no listener, and apps/billing read
// the failure as "this fleet has no commerce" and fell through to an HTTP proxy that is
// unconfigured in this deployment. ai's balance gate is fail-CLOSED, so every paid
// completion in the fleet answered 503 balance_unavailable — for three days, with
// nothing in any log naming the reason.
//
// The rule: a socket FILE is not a listener. A leftover one must change NOTHING about
// waking a cold app — the app comes up and answers, exactly as it does with no file at
// all. Both halves are asserted, because "always report absent" would pass one of them.
func TestWakeStartsALazyAppBehindALeftoverSocket(t *testing.T) {
	const name = "sleepy"
	router(t, name)

	// The pod that died: bound once, never unlinked, nobody behind it. The run dir
	// outlives the process, so this is what the next pod finds.
	path := zip.SocketPath(name)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("bind the leftover socket: %v", err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatalf("close the leftover socket: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no leftover socket file; this does not reproduce prod: %v", err)
	}
	if c, derr := net.DialTimeout("unix", path, time.Second); derr == nil {
		_ = c.Close()
		t.Fatal("the leftover socket still ACCEPTS; this does not reproduce prod")
	}

	out, err := cloud.Ask[struct{}, plane.Started](context.Background(), name, "wake_alive", &struct{}{})
	if err != nil {
		t.Fatalf("a leftover socket file suppressed the wake of a cold app: %v\n"+
			"this is the outage: commerce never started, every prepaid balance read "+
			"refused, and the fail-CLOSED gate answering 503 for every paid completion", err)
	}
	if out == nil || out.Addr != name {
		t.Fatalf("woke something that is not %s: %+v", name, out)
	}

	// It is really up: a second call is answered by the SAME child, not a new one.
	if _, err := cloud.Ask[struct{}, plane.Started](context.Background(), name, "wake_alive", &struct{}{}); err != nil {
		t.Fatalf("second call to the woken app failed: %v", err)
	}
	if n := running(t); n != 1 {
		t.Fatalf("%d children running, want exactly 1", n)
	}
}
