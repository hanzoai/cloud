package cloud_test

// plane_stale_test.go — a socket FILE is not a peer.
//
// THE OUTAGE. The cloud pod keeps its run directory on a volume, so
// /var/lib/cloud/run survives the pod that wrote it. commerce is a LAZY app: nothing
// starts it until someone asks the router to. A previous pod left commerce.sock behind
// (mtime 23:10, pod start 23:41, and /proc/net/unix listing no listener for it), and
// reach() decided the peer was up by stat'ing that file. So commerce was never woken,
// every plane call to it was refused by a kernel with nothing to hand it, and — because
// apps/billing then read the failure as "this fleet has no commerce" — the prepaid
// balance read fell through to an unconfigured HTTP proxy. ai's balance gate is
// fail-CLOSED, so every paid completion in the fleet answered 503 balance_unavailable.
//
// One file, three days, and nothing in any log said why.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// runDir points the plane at a SHORT directory. A unix socket path is capped at ~104
// bytes and t.TempDir() spends most of that on the test's own name, so the run dir is
// where a long test name turns into "bind: invalid argument" — a failure about
// something the test is not about.
func runDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pl")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// leftover writes the socket file a killed process leaves: bound once, never unlinked,
// with nobody behind it. SetUnlinkOnClose(false) is what makes it a leftover rather
// than a tidy shutdown — it is the pod that was killed, not the one that stopped.
func leftover(t *testing.T, app string) string {
	t.Helper()
	path := zip.SocketPath(app)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("bind %s: %v", path, err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the leftover socket file is not there: %v", err)
	}
	return path
}

// TestStaleSocketIsNotAPeer is the regression guard for the outage above.
//
// With no router in the process tree, an app that is not deployed here answers
// ErrNoPeer — that is the developer's case and the split-deploy case alike. The rule
// under test is that a LEFTOVER SOCKET FILE changes nothing about that answer: the file
// is not evidence of a listener, so the call must reach exactly the same conclusion it
// reaches when the file is absent.
//
// Before the fix reach() stat'd the path, took the leftover for a live peer, returned
// nil, and the dial failed with a bare "connection refused" that no caller could tell
// apart from a peer that answered badly. That is the misread the whole ErrNoPeer
// distinction exists to prevent, and it arrived through the one door that skipped it.
func TestStaleSocketIsNotAPeer(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", runDir(t))
	cloud.ResetPlane()

	// The control: no file at all is unambiguously ErrNoPeer.
	_, absent := cloud.Ask[plane.BalanceIn, plane.Balance](context.Background(), "commerce",
		plane.FinanceBalance, &plane.BalanceIn{Subject: "acme", Currency: "usd"})
	if !errors.Is(absent, cloud.ErrNoPeer) {
		t.Fatalf("no socket and no router must be ErrNoPeer, got: %v", absent)
	}

	path := leftover(t, "commerce")

	_, err := cloud.Ask[plane.BalanceIn, plane.Balance](context.Background(), "commerce",
		plane.FinanceBalance, &plane.BalanceIn{Subject: "acme", Currency: "usd"})
	if err == nil {
		t.Fatal("a call answered by a socket file with no listener SUCCEEDED")
	}
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Fatalf("a leftover socket file was taken for a live peer: %v\n"+
			"a file is not a listener, and reading it as one is what left commerce cold "+
			"and every paid completion in the fleet answering 503", err)
	}
	// The prod shape exactly: the run dir is a volume, so the file is STILL there on
	// the next call and the next pod. The answer may not drift.
	if _, serr := os.Stat(path); serr != nil {
		t.Fatalf("the leftover file vanished; this no longer reproduces prod: %v", serr)
	}
}

// TestALiveSocketIsAPeer is the other half, and it is what stops the fix above from
// being "always report absent": a socket with a real listener behind it must be reached
// with no router involved at all. That is the steady state of every plane call in the
// fleet.
func TestALiveSocketIsAPeer(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", runDir(t))
	cloud.ResetPlane()

	// A leftover file at the SAME path first: the listener binds over it, exactly as
	// zaphttp.Server.ListenAndServe does in prod, which is why reach() removes nothing.
	_ = leftover(t, "ledger")

	app := zip.New(zip.Config{AppName: "ledger"})
	zip.Post[plane.BalanceIn, plane.Balance](app, "/ledger/balance",
		func(context.Context, *plane.BalanceIn) (*plane.Balance, error) {
			return &plane.Balance{Amount: plane.Money{Decimal: "149533.00", Currency: "USD"}}, nil
		}, zip.WithOperationID("stale_balance"))
	go func() { _ = app.Listen(zip.SocketPath("ledger")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "ledger")

	out, err := cloud.Ask[plane.BalanceIn, plane.Balance](cloud.For(context.Background(), "acme"),
		"ledger", "stale_balance", &plane.BalanceIn{Subject: "acme", Currency: "usd"})
	if err != nil {
		t.Fatalf("a live peer was not reached: %v", err)
	}
	if out.Amount.Decimal != "149533.00" {
		t.Fatalf("reply lost its value: %+v", out)
	}
}

// TestSocketPathIsUnderTheRunDir keeps the two halves above honest about WHERE they
// looked: both write and probe zip.SocketPath(name), which is the one scheme a server
// and its callers share. If that ever stops resolving under the run dir, the tests
// would be exercising a path nothing in prod uses.
func TestSocketPathIsUnderTheRunDir(t *testing.T) {
	dir := runDir(t)
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	cloud.ResetPlane()

	if got, want := zip.SocketPath("commerce"), filepath.Join(dir, "commerce.sock"); got != want {
		t.Fatalf("SocketPath(commerce) = %q, want %q", got, want)
	}
}
