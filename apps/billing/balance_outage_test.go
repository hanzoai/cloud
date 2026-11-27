package billing

// balance_outage_test.go — a peer that is HERE and failed is an outage, never a fleet
// that does not run commerce.
//
// cloud.Ask states the rule (peer.go): "a router that does not know the name — or a
// fleet with no router at all — answers ErrNoPeer, which is the ONLY error a caller may
// read as 'fall back'. Every other failure is an outage." availableCents read every
// error as ErrNoPeer. So when commerce was reachable-in-principle and the call failed,
// the billing surface concluded "split deploy", handed the read to a commerceProxy that
// is UNCONFIGURED wherever the plane is the real path, and answered the customer
// "billing is not configured" — while ai's fail-CLOSED balance gate read the refusal and
// answered 503 balance_unavailable for every paid completion in the fleet.
//
// The failure was invisible for three days because that one line dropped the error on
// the floor: nothing logged it, because after that line nothing had it.

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// planeDir points the plane at a SHORT run directory (a unix socket path is capped near
// 104 bytes and t.TempDir() spends most of it on the test name) and resets the process
// plane so ops from a previous test cannot answer this one.
func planeDir(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bl")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
}

// brokenCommerce serves the commerce plane socket and FAILS the balance op. It is the
// reachable-but-broken peer: the socket answers, so absence is not the fact — the read
// is. (A cold lazy peer behind a leftover socket file reaches the same line by a
// different road; the rule under test is what availableCents does with the error.)
func brokenCommerce(t *testing.T) {
	t.Helper()
	app := zip.New(zip.Config{AppName: "commerce"})
	app.Post("/finance/balance",
		func(context.Context, *client.BalanceIn) (*client.Balance, error) {
			return nil, zip.Errorf(http.StatusInternalServerError, "ledger is locked")
		}, zip.WithOperationID(client.FinanceBalance))
	go func() { _ = app.Listen(zip.SocketPath("commerce")) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	path := zip.SocketPath("commerce")
	for range 400 {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never began listening", path)
}

// TestBalance_APlaneOutageIsNotASplitDeploy is the regression guard.
//
// The plugin-process shape (no co-resident ledger — see balance_s2s_test.go) plus a
// commerce peer that is present and cannot answer. The balance read must be a 502
// "billing upstream unreachable", which s.Log.Warn NAMES — never a balance nobody read.
func TestBalance_APlaneOutageIsNotASplitDeploy(t *testing.T) {
	noFinance(t) // the plugin process: finance.Current() is nil forever
	planeDir(t)
	brokenCommerce(t)

	app := mountApp(t)

	code, body := userCall(t, app, "/v1/billing/balance", "u1", "hanzo")
	if code == http.StatusOK {
		t.Fatalf("a failed ledger read was answered 200 from the proxy: %s\n"+
			"an outage was laundered into a split deploy, and the number returned is "+
			"one nobody read", body)
	}
	if code != http.StatusBadGateway {
		t.Fatalf("failed ledger read: want 502, got %d (%s)", code, body)
	}
}

// TestBalance_NoCommerceInTheFleetIs503 is the half that keeps the fix from becoming
// "always 502". A fleet that runs no commerce reaches ErrNoPeer — the router's own
// word, from the manifest it owns, or the absence of any router at all — and has no
// ledger to read: 503, an outage, never a balance nobody read.
func TestBalance_NoCommerceInTheFleetIs503(t *testing.T) {
	noFinance(t)
	planeDir(t) // an empty run dir and no router: ErrNoPeer by construction
	app := mountApp(t)

	code, body := userCall(t, app, "/v1/billing/balance", "u1", "hanzo")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no commerce: want 503, got %d (%s)", code, body)
	}
}
