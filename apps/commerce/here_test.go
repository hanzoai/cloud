// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// here_test.go — a commerce read serving a commerce read, with no request built
// and nothing to bound.
//
// THE SHAPE THIS REPLACES. Every subsystem that needed a fact from commerce
// built an *http.Request and handed it to the commerce transport, which — when
// commerce was co-resident — dispatched that request into the WHOLE shared fiber
// app: every edge middleware, on the way to a route. Some of those middlewares
// read commerce themselves. The per-org scope rate limiter reads its rules from
// commerce; on a cold cache it had not finished the read that would fill the
// cache when the dispatch re-entered it, so it read again, and again. The cure
// was a cap of 8 nested dispatches per goroutine, counted in a map keyed on a
// goroutine id parsed out of the header of runtime.Stack.
//
// A goroutine-id-keyed depth counter is not a fix, it is a description of the
// defect. The recursion existed because a co-resident caller entered the ROUTER.
// A caller that enters the OP cannot recurse into a middleware, because it never
// runs one: there is nothing between plane.Ask and the handler.
//
// So this test does not measure how deep the recursion goes. It removes the
// wire — unlinks the socket the plane serves on — and shows the nested read
// still answers, which no call through a transport could do.

import (
	"context"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// nestedOp is the op under test: a commerce op whose handler asks commerce for
// two more facts. It is the shape that used to recurse — a read taken while a
// read is being served.
const nestedOp = "finance_nested_probe"

// invocations counts how many op handlers actually RAN, so the wire count a
// syscall trace reports can be held against the work that was done. A run with
// no connections and no work would prove nothing.
var invocations atomic.Int64

// nested is what a middleware doing its job looks like from the ledger's side:
// serving one read, it takes two more. Under the transport this re-entered the
// whole app twice per level and was stopped only by the depth cap.
func nested(ctx context.Context, _ *plane.SpendIn) (*plane.Spend, error) {
	invocations.Add(1)
	spend, err := commercepeer.FinanceSpend(ctx, &plane.SpendIn{})
	if err != nil {
		return nil, err
	}
	txns, err := commercepeer.FinanceTxns(ctx, &plane.TxnsIn{})
	if err != nil {
		return nil, err
	}
	if len(txns.Rows) == 0 {
		return nil, context.Canceled // an empty ledger would make the assertion below vacuous
	}
	return spend, nil
}

// serveNested stands up the real thing: a real per-org finance ledger with real
// money in it, commerce's real ops published on the real plane, the probe above
// beside them, and the canonical socket bound. It returns the socket path.
func serveNested(t *testing.T, org string, cents int64) string {
	t.Helper()
	// A unix address is capped near a hundred bytes and t.TempDir embeds this
	// test's name; a short anonymous dir keeps the address inside the cap.
	sockDir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	fin := finance.New(t.TempDir())
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	if _, err := fin.Deposit(context.Background(), types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(cents), Currency: "usd", Ref: "here-test",
	}); err != nil {
		t.Fatalf("seed the ledger: %v", err)
	}

	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeSpend()
	exposeTxns()
	zip.Post[plane.SpendIn, plane.Spend](cloud.Plane(), "/finance/nested", nested,
		zip.WithOperationID(nestedOp))

	app := cloud.Plane()
	sock := zip.SocketPath("commerce")
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 300 {
		if c, derr := net.Dial("unix", sock); derr == nil {
			_ = c.Close()
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the commerce plane socket never began listening")
	return ""
}

// A commerce read taken WHILE a commerce read is being served answers with the
// wire removed — so no request crossed one, and there was no router to re-enter.
func TestCommerceReadServesCommerceReadWithoutTheWire(t *testing.T) {
	const org, cents = "acme", 12_345
	sock := serveNested(t, org, cents)
	ctx := cloud.For(context.Background(), org)

	// Warm: the socket is up, and the nested read answers.
	out, err := plane.Ask[plane.SpendIn, plane.Spend](ctx, "commerce", nestedOp, &plane.SpendIn{})
	if err != nil {
		t.Fatalf("nested read with the socket up: %v", err)
	}
	if got := out.Balance.Decimal; !strings.HasPrefix(got, "123.45") {
		t.Fatalf("balance = %q, want the seeded 123.45", got)
	}

	// TAKE THE WIRE AWAY. Nothing can dial this app any more.
	if err := os.Remove(sock); err != nil {
		t.Fatalf("unlink the plane socket: %v", err)
	}
	if c, derr := net.Dial("unix", sock); derr == nil {
		_ = c.Close()
		t.Fatal("the socket still dials after unlink — the wire was not actually removed")
	}

	// The same nested read — one commerce op asking commerce for two more —
	// still answers. A call that built a request for a transport to carry could
	// not do this.
	out, err = plane.Ask[plane.SpendIn, plane.Spend](ctx, "commerce", nestedOp, &plane.SpendIn{})
	if err != nil {
		t.Fatalf("nested read with NO socket: %v\n"+
			"the co-resident call is still going through a wire; zip.Serving/zip.Here is not carrying it", err)
	}
	if got := out.Balance.Decimal; !strings.HasPrefix(got, "123.45") {
		t.Fatalf("balance with no socket = %q, want the seeded 123.45", got)
	}
	// Stated so a syscall trace of this binary can be read against it: two
	// nested reads ran, each of which took two more, and the second pair ran
	// with no socket in the filesystem at all.
	if got := invocations.Load(); got < 2 {
		t.Fatalf("the probe handler ran %d times, want 2 — the run did too little to prove anything", got)
	}
	t.Logf("op handlers run: %d (each takes 2 further commerce reads)", invocations.Load())
}

// MUTATION. Break the direct path and the call must FAIL — which is what makes
// the test above evidence rather than a coincidence.
//
// zip.Serving is keyed on the app's canonical socket path, so pointing the
// runtime dir somewhere else is exactly "this process does not serve commerce".
// With the direct path broken and no socket at the new address either, the call
// has nowhere to go and says so. Point it back and it answers again.
func TestBreakingTheDirectPathBreaksTheCall(t *testing.T) {
	const org, cents = "acme", 12_345
	serveNested(t, org, cents)
	ctx := cloud.For(context.Background(), org)

	if _, err := plane.Ask[plane.SpendIn, plane.Spend](ctx, "commerce", nestedOp, &plane.SpendIn{}); err != nil {
		t.Fatalf("baseline nested read: %v", err)
	}

	// BREAK IT: a runtime dir this process serves nothing in.
	elsewhere, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(elsewhere) }()
	real := os.Getenv("ZIP_RUNTIME_DIR")
	t.Setenv("ZIP_RUNTIME_DIR", elsewhere)
	plane.Unbind()
	if zip.Serving("commerce") != nil {
		t.Fatal("zip.Serving still answers for commerce at an address nothing bound — the mutation did not take")
	}
	if _, err := plane.Ask[plane.SpendIn, plane.Spend](ctx, "commerce", nestedOp, &plane.SpendIn{}); err == nil {
		t.Fatal("the call SUCCEEDED with the direct path broken and no socket to fall back to; " +
			"something other than zip.Here is answering, and the test above proves nothing")
	}

	// RESTORE IT.
	t.Setenv("ZIP_RUNTIME_DIR", real)
	plane.Unbind()
	if zip.Serving("commerce") == nil {
		t.Fatal("zip.Serving does not answer for commerce after restoring the runtime dir")
	}
	out, err := plane.Ask[plane.SpendIn, plane.Spend](ctx, "commerce", nestedOp, &plane.SpendIn{})
	if err != nil {
		t.Fatalf("nested read after restoring the direct path: %v", err)
	}
	if got := out.Balance.Decimal; !strings.HasPrefix(got, "123.45") {
		t.Fatalf("balance after restore = %q, want the seeded 123.45", got)
	}
}
