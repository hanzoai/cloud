// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"

	// devmaster keys this test binary: cek opens no ledger file without a master, and a
	// test process has no KMS to resolve one from.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// serveCommerce stands the REAL money peer up on a real socket: the finance ledger the
// process owns, behind the metering client, behind plane.FinanceRecord — the same three
// layers apps/commerce puts behind that op, and the same rule for where the billed org
// comes from (the CALLER, never the argument).
//
// It is the peer and not a spy on purpose. What is under test is whether a client can
// name the key the LEDGER dedups on, and only a ledger can answer that: a recording fake
// would show the ref arriving and say nothing about the money.
func serveCommerce(t *testing.T, seedSubject string, seedCents int64) finance.Client {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	fin := finance.New(t.TempDir())
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil); _ = fin.Close() })
	if _, err := fin.Deposit(context.Background(), types.DepositInput{
		Org: "acme", Subject: seedSubject, Amount: money.FromCents(seedCents),
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}

	// A configured meter that never speaks HTTP: finance is published, so Record takes
	// the co-resident native path — the shape a fused binary runs.
	meter, err := metering.New(metering.Config{BaseURL: "http://127.0.0.1:1", Token: "svc"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}

	app := zip.New(zip.Config{AppName: "commerce"})
	zip.Post[plane.RecordIn, plane.Recorded](app, "/finance/record",
		func(ctx context.Context, in *plane.RecordIn) (*plane.Recorded, error) {
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden("no org on the debit")
			}
			amount, perr := in.Amount.Parse()
			if perr != nil {
				return nil, zip.ErrBadRequest(perr.Error())
			}
			if _, rerr := meter.Record(ctx, metering.Usage{
				User: in.Subject, Org: org,
				Amount:    money.FromDecimal(amount.Decimal()),
				Model:     in.Usage.Model,
				Provider:  in.Usage.Provider,
				Ref:       in.Usage.Ref,
				RequestID: in.Usage.RequestID,
				Currency:  amount.Currency().Code,
			}); rerr != nil {
				return nil, rerr
			}
			return &plane.Recorded{Amount: in.Amount}, nil
		}, zip.WithOperationID(plane.FinanceRecord))

	// RESOLVE THE ADDRESS HERE, NOT IN THE GOROUTINE — zip.SocketPath reads
	// ZIP_RUNTIME_DIR on every call and each test points it at its own temp dir, so a
	// listener that resolved its own address could bind at the NEXT test's address and
	// answer that test's debits with "unknown op".
	path := zip.SocketPath("commerce")
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitForCommerce(t, path)
	return fin
}

// waitForCommerce blocks until the peer's socket ACCEPTS. Listen runs in a goroutine, so
// without this the first debit races the bind and the test reports a money bug that is
// really a harness bug — which it did, on the first run of this file.
func waitForCommerce(t *testing.T, path string) {
	t.Helper()
	for range 400 {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("commerce never began listening on %s", path)
}

func walletCents(t *testing.T, fin finance.Client, subject string) int64 {
	t.Helper()
	bal, err := fin.Balance(context.Background(), "acme", subject, "usd", false)
	if err != nil {
		t.Fatalf("balance(%s): %v", subject, err)
	}
	return bal.Cents()
}

// EVERY ANSWER IS PAID FOR, however the caller names the message.
//
// THE BUG. The ai module debits a completion under its message row's id, and that id is
// `Owner + "/" + Name` (object.Message.GetId) — both of them fields of the JSON body the
// CLIENT posts to AddMessage, which unmarshals the whole Message off the wire. Riding that
// value across as the debit's Ref made it the ledger's idempotency key, so a client that
// pinned one owner/name pair got the first completion billed and every completion after it
// deduped into that first entry: free inference, at whatever volume, from two body fields.
//
// THE PROPERTY. Twenty answers under ONE pinned owner/name are twenty acts and bill twenty
// times. The key is the server's, minted per debit at the far end (metering.Usage.Seal),
// and nothing a caller can write reaches it.
//
// It is not a downgrade from a stable key either: this debit is made once per streamed
// answer and is never re-driven (the ai module's retry sweep matches only its own HTTP
// fallback's error text), and the sibling debit on the OpenAI surface already keys on a
// fresh uuid per call. Both surfaces now mint.
//
// MUTATION PROOF: put the client's value back in debitOverPlane —
//
//	Model: u.Model, Provider: u.Provider, Ref: u.RequestID,
//
// and the wallet ends at 99¢ instead of 80¢: nineteen of twenty answers billed nobody.
func TestPinnedMessageIDBillsEveryAnswer(t *testing.T) {
	const subject = "acme/bob"
	fin := serveCommerce(t, subject, 100)

	// One owner/name pair, posted on every request — the whole of the attack.
	const pinned = "acme/msg-pinned"
	const answers = 20
	for i := range answers {
		if err := debitOverPlane(context.Background(), aiobject.UsageEvent{
			Subject:   subject,
			Namespace: "acme",
			USD:       "0.01",
			Currency:  "usd",
			Model:     fmt.Sprintf("zen-%d", i),
			Provider:  "hanzo",
			RequestID: pinned,
		}); err != nil {
			t.Fatalf("answer %d: %v", i, err)
		}
	}
	if got := walletCents(t, fin, subject); got != 100-answers {
		t.Fatalf("wallet after %d answers under one pinned message id = %d¢; want %d¢ — every answer must bill",
			answers, got, 100-answers)
	}
}

// The debit acts for the org the event names, and the peer bills the wallet the gate read.
// Both are asserted here because the pinning fix must not quietly re-point either: money
// billed to the wrong books is the same failure as money not billed at all.
func TestTheDebitCarriesTheOrgAndBillsTheSubjectsWallet(t *testing.T) {
	const subject = "acme/bob"
	fin := serveCommerce(t, subject, 100)

	if err := debitOverPlane(context.Background(), aiobject.UsageEvent{
		Subject: subject, Namespace: "acme", USD: "0.25", Currency: "usd", Model: "zen", Provider: "hanzo",
	}); err != nil {
		t.Fatalf("debit: %v", err)
	}
	if got := walletCents(t, fin, subject); got != 75 {
		t.Fatalf("bob's wallet = %d¢; want 75¢", got)
	}
	// The org POOL is a different wallet in the same file and must be untouched.
	if got := walletCents(t, fin, "acme"); got != 0 {
		t.Fatalf("the org pool was billed %d¢ for a user's completion", 0-got)
	}
}

// An event with NO currency still bills in USD rather than failing the debit: the ai
// module leaves it empty on some paths, and a debit that errored there would be an unbilled
// completion — the same hole from the other end.
func TestAnAbsentCurrencyStillBills(t *testing.T) {
	const subject = "acme/bob"
	fin := serveCommerce(t, subject, 100)

	if err := debitOverPlane(context.Background(), aiobject.UsageEvent{
		Subject: subject, Namespace: "acme", USD: "0.10", Model: "zen", Provider: "hanzo",
	}); err != nil {
		t.Fatalf("debit with no currency: %v", err)
	}
	if got := walletCents(t, fin, subject); got != 90 {
		t.Fatalf("wallet = %d¢; want 90¢ — an empty currency defaults to usd", got)
	}
}

// THE CO-RESIDENT HALF, STRUCTURALLY.
//
// When cloud owns the ledger the debit does not cross a socket — it is handed to the host
// as a cloud.UsageEvent — so the same question has to be answered about that value: can
// anything a client wrote reach the ledger's key through it? The answer is that the field
// no longer exists. There is no channel to police, no site to review, and no way to
// re-open one without deleting this test.
func TestTheHostUsageEventCarriesNoRef(t *testing.T) {
	typ := reflect.TypeOf(cloud.UsageEvent{})
	if _, found := typ.FieldByName("Ref"); found {
		t.Error("cloud.UsageEvent has a Ref again — the ai module's only candidate for it is " +
			"the message row's id, which is two fields of the client's own request body")
	}
}
