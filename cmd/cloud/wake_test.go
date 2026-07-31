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
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const wakeChildEnv = "CLOUD_WAKE_CHILD"

// TestMain lets this binary act as the lazy plugin the host starts.
//
// The child binds TWO sockets, exactly as cloud.Serve does and in the same order:
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
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	cloud.ResetPlane()

	app := zip.New(zip.Config{AppName: "cloud", Logger: luxlog.New("test")})
	err := app.Add(zip.Load(zip.Plugin{
		Name: name, Path: os.Args[0], Lazy: true,
		Env:   []string{wakeChildEnv + "=" + name, "ZIP_RUNTIME_DIR=" + os.Getenv("ZIP_RUNTIME_DIR")},
		Start: 60 * time.Second,
	}, "/v1/"+name))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })

	// Called exactly as run() calls it: no handle, torn down by the app's own
	// shutdown hooks. If this ever grows a return value again, the caller in
	// main.go is one `defer f()()` away from never opening the door at all.
	serveWake(app)
	waitFor(t, zip.SocketPath(plane.HostApp))
}

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
// that can start anything, so "not deployed here" is simply true — and it costs one
// stat, not a dial and a timeout.
func TestWakeWithNoRouterIsNoPeer(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	cloud.ResetPlane()

	_, err := cloud.Ask[struct{}, plane.Started](context.Background(), "sleepy", "wake_alive", &struct{}{})
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Fatalf("no socket and no router must be ErrNoPeer, got: %v", err)
	}
}
