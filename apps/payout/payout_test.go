package payout_test

// payout_test.go drives the REAL read: a real per-org finance ledger with real
// money in it, commerce's real finance_spend op on a real plane socket, and this
// package's production client on the other end.
//
// It used to drive an httptest.Server asserting GET /v1/billing/usage/rollup.
// That test was green for as long as the shape it tested has been broken in
// production: co-resident, the transport dispatched that path back into a router
// that does not serve it (404); split, the base URL was empty and the client
// answered a silent 0. A test that stands up its own server for the wire under
// test cannot see either failure — it proves the mock answers, which nobody
// doubted.

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/payout"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// serveLedger stands up the money plane the way Mount does: a real ledger
// published as the process-wide client, commerce's real ops on it, and the
// canonical socket bound.
func serveLedger(t *testing.T) {
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

	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	// The op, answering from the ledger exactly as commerce's own does — the
	// windowed sum and the wallet behind it. commerce's REAL handler is driven
	// end to end in apps/commerce/here_test.go; what is under test here is the
	// client, the tenancy of the call, and what it does when the answer does not
	// come.
	zip.Post[plane.SpendIn, plane.Spend](cloud.Plane(), "/finance/spend",
		func(ctx context.Context, in *plane.SpendIn) (*plane.Spend, error) {
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden("spend: no org on the call")
			}
			fin := finance.Current()
			if fin == nil {
				// The process that owns the money cannot read it. That is an
				// outage, and answering zero would report every org as idle.
				return nil, errors.New("spend: no ledger in the process that owns it")
			}
			since := in.Since
			if since == 0 {
				now := time.Now().UTC()
				since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
			}
			cents, err := fin.SumUsageSince(ctx, org, false, since)
			if err != nil {
				return nil, err
			}
			bal, err := fin.Balance(ctx, org, org, "usd", false)
			if err != nil {
				return nil, err
			}
			return &plane.Spend{
				Consumed: plane.Amount(money.FromCents(cents).Unwrap()),
				Balance:  plane.Amount(bal.Unwrap()),
			}, nil
		}, zip.WithOperationID(plane.FinanceSpend))

	app := cloud.Plane()
	sock := zip.SocketPath("commerce")
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 300 {
		if c, derr := net.Dial("unix", sock); derr == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the commerce plane socket never began listening")
}

// spend writes real consumption into the org's real wallet: a deposit to fund it
// and a metered debit, which is what month-to-date consumption counts.
func spend(t *testing.T, org string, fundCents, useCents int64) {
	t.Helper()
	fin := finance.Current()
	if _, err := fin.Deposit(context.Background(), types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(fundCents), Currency: "usd", Ref: "fund-" + org,
	}); err != nil {
		t.Fatalf("fund %s: %v", org, err)
	}
	if err := fin.RecordUsage(context.Background(), types.UsageInput{
		Org: org, Subject: org, Amount: money.FromCents(useCents), Currency: "usd",
		Model: "zen", Ref: "use-" + org,
	}); err != nil {
		t.Fatalf("meter %s: %v", org, err)
	}
}

// The accrual base is the ledger's OWN consumption figure — the same one the
// rolling spend cap reads, so a program that qualifies an org on its spend and
// the gate that stops that org spending cannot disagree about the amount.
func TestSpendCentsReadsTheLedger(t *testing.T) {
	serveLedger(t)
	spend(t, "acme", 10_000, 4_200)

	got, err := payout.NewClient().SpendCents(context.Background(), "acme")
	if err != nil {
		t.Fatalf("SpendCents: %v", err)
	}
	if got != 4200 {
		t.Fatalf("consumption = %d cents, want the 4200 that was metered", got)
	}
}

// One org can never read another's spend. The org rides the CALL, so there is no
// argument a caller could point somewhere else — this pins that an org with its
// own money reads its own figure and not its neighbour's.
func TestSpendIsPerOrg(t *testing.T) {
	serveLedger(t)
	spend(t, "acme", 10_000, 4_200)
	spend(t, "globex", 10_000, 900)

	c := payout.NewClient()
	acme, err := c.SpendCents(context.Background(), "acme")
	if err != nil {
		t.Fatalf("acme: %v", err)
	}
	globex, err := c.SpendCents(context.Background(), "globex")
	if err != nil {
		t.Fatalf("globex: %v", err)
	}
	if acme != 4200 || globex != 900 {
		t.Fatalf("acme=%d globex=%d, want 4200 and 900 — the two orgs' books ran together", acme, globex)
	}
}

// An org that has spent nothing reads a clean zero, which is a real answer and
// not a failure. The distinction matters because the failure below must not look
// like it.
func TestNoSpendIsZeroNotAFailure(t *testing.T) {
	serveLedger(t)
	got, err := payout.NewClient().SpendCents(context.Background(), "quiet")
	if err != nil {
		t.Fatalf("an org with no activity must read cleanly: %v", err)
	}
	if got != 0 {
		t.Fatalf("consumption = %d, want 0", got)
	}
}

// NO COMMERCE IN THIS FLEET is ErrNoLedger, and it is the only absence a program
// may act on. It is the ROUTER'S word — an unbound socket with no router to ask —
// never an inference from a call that failed.
func TestNoCommerceIsNamedAbsence(t *testing.T) {
	dir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	_, err = payout.NewClient().SpendCents(context.Background(), "acme")
	if !errors.Is(err, payout.ErrNoLedger) {
		t.Fatalf("SpendCents with no commerce = %v, want ErrNoLedger — an absence a program can act on", err)
	}
}

// A LEDGER THAT IS HERE AND FAILING IS AN OUTAGE, never a zero. This is the
// failure mode that cost three programs everything they were meant to pay: the
// old client answered 0 whenever it could not reach commerce, so a broken read
// and an org that had genuinely spent nothing were the same number.
func TestAFailedReadIsNotZero(t *testing.T) {
	serveLedger(t)
	// The socket is up and the ops are registered, but the LEDGER is gone — the
	// one thing that can answer. That is an outage in the process that owns the
	// money, and it must not read as "this org spent nothing".
	finance.Publish(nil)

	got, err := payout.NewClient().SpendCents(context.Background(), "acme")
	if err == nil {
		t.Fatalf("a ledger that cannot answer returned (%d, nil) — a broken read must never be a zero month", got)
	}
	if errors.Is(err, payout.ErrNoLedger) {
		t.Fatal("an outage was reported as ErrNoLedger; only the router may say a peer is absent")
	}
}

// The SHAPE of the money seam. The three credit programs minted platform credit
// because this client carried a Deposit, so reviving that mint must start by
// re-declaring the capability here, in front of a test that says no.
func TestSeamIsReadOnly(t *testing.T) {
	var c any = payout.NewClient()
	if _, ok := c.(interface {
		Deposit(context.Context, string, string, int64, string, string, string, string) (string, error)
	}); ok {
		t.Fatal("payout.Client grew a Deposit again — this seam READS; money-in is an admin grant")
	}
	if _, ok := c.(payout.Commerce); !ok {
		t.Fatal("Client must still satisfy the read seam")
	}
}

// There is no address and no credential on this client, and that is the point: a
// peer is reached through zip.SocketPath(name). The knobs this replaces —
// CLOUD_COMMERCE_HTTP_URL and a service token — were what let a deployment
// believe it had configured a wire while every read 404'd or answered zero.
func TestClientCarriesNoAddress(t *testing.T) {
	if n := reflect.TypeFor[payout.Client]().NumField(); n != 0 {
		t.Fatalf("payout.Client carries %d field(s); reaching a peer by name takes none", n)
	}
}
