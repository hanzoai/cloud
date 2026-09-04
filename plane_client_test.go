package cloud_test

// plane_client_test.go — the generated peer client, against the REAL commerce
// app, over a REAL unix socket.
//
// Nothing here is stubbed. commerce is mounted exactly as a plugin process
// mounts it, its plane socket is bound exactly as Serve binds it, and the call
// is the generated function an ordinary caller would write. What is being proven
// is not that ZAP works — plane_test.go already does that — but that the
// GENERATED wrapper reaches a real handler with a real answer, so the 45 HTTP
// call sites have somewhere true to go.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/commerce"
	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// shortRun points the plane at a SHORT directory, for the reason plane_stale_test
// gives: a unix socket path is capped near 104 bytes and a test's own name spends
// most of that.
func shortRun(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pc")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", dir)
	client.Unbind()
	t.Cleanup(client.Unbind)
}

// liveCommerce mounts the real commerce subsystem and binds its plane socket,
// which is what a plugin process does at boot.
func liveCommerce(t *testing.T) {
	t.Helper()
	shortRun(t)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	cfg, done, err := cloud.SpecConfig()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	t.Cleanup(done)
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: luxlog.Default(), DisableStartupMessage: true})
	if err := cloud.UseAll(app, []cloud.Plugin{{
		Name: commercepeer.App, Price: cloud.Free, Use: commerce.Use,
		// commerce wraps all of /v1, exactly as plugin/commerce/main.go declares.
		// Mounting it any other way is refused by the scope check, and rightly.
		Global: true,
	}}, cfg, deps); err != nil {
		t.Fatalf("mount commerce: %v", err)
	}
	stop, err := cloud.ServePlane(commercepeer.App, luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
}

// TestGeneratedClientReachesTheRealApp is the whole claim in one call: a typed Go
// function, no URL and no env var anywhere in it, answered by the process that
// owns the ledger — over the socket, not through the edge.
//
// The ANSWER is what makes it a round trip and not a dial. A reply that decodes
// into the declared type could only have come from the declared handler.
func TestGeneratedClientReachesTheRealApp(t *testing.T) {
	liveCommerce(t)

	ctx := cloud.For(context.Background(), "acme")
	got, err := commercepeer.FinanceBalance(ctx, &client.BalanceIn{Currency: "usd"})
	if err != nil {
		t.Fatalf("FinanceBalance over the plane: %v", err)
	}
	if got == nil {
		t.Fatal("a nil reply from the process that owns the ledger is not an answer")
	}
	if got.Amount.Currency == "" {
		t.Fatalf("reply carries no currency: %+v — the handler did not fill it, so this was not the real op", got)
	}
	t.Logf("commerce answered over UDS: amount=%q currency=%q", got.Amount.Decimal, got.Amount.Currency)
}

// TestGeneratedClientCarriesTheCallersOrg: the tenant still rides the caller.
// The generated signature has no org parameter and cannot grow one, because the
// declared input has no org field — which is the property that stops a caller
// naming another tenant's books.
func TestGeneratedClientCarriesTheCallersOrg(t *testing.T) {
	liveCommerce(t)

	if _, err := commercepeer.FinanceBalance(context.Background(), &client.BalanceIn{Currency: "usd"}); err == nil {
		t.Fatal("a call with no caller org was answered — the org must never be optional")
	}
}

// TestColdPeerIsNamedNotGuessed is issue #108, asked directly.
//
// A lazy app is started when a request reaches its HTTP PREFIX. A plane call
// reaches no prefix, so the question is what a caller gets when the peer has
// never been woken. The answer must be a NAMED absence — ErrNoPeer, the one
// error a caller may read as "fall back" — and never a timeout, a dial error, or
// anything a caller would have to guess at.
func TestColdPeerIsNamedNotGuessed(t *testing.T) {
	shortRun(t)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	// An empty run directory: commerce has no socket, and there is no router in
	// this process tree to start it.
	_, err := commercepeer.FinanceBalance(cloud.For(context.Background(), "acme"),
		&client.BalanceIn{Currency: "usd"})
	if err == nil {
		t.Fatal("a cold peer answered — there is nothing here to have answered it")
	}
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Fatalf("cold peer reported %v; want ErrNoPeer so a caller can tell absence from an outage", err)
	}
	if !strings.Contains(err.Error(), commercepeer.App) {
		t.Fatalf("the error does not name the app it is about: %v", err)
	}
	t.Logf("cold peer, no router: %v", err)
}
