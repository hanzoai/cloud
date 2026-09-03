package metering_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/metering"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// meteredWallet stands a co-resident ledger up with a funded org pool and a metering
// client whose commerce fake must never be reached. It returns the ledger and the client.
func meteredWallet(t *testing.T, seedCents int64) (finance.Client, *metering.Client) {
	t.Helper()
	fin := finance.New(finance.Local(t.TempDir()))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil); _ = fin.Close() })

	if _, err := fin.Deposit(context.Background(), types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(seedCents),
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}
	srv := httptest.NewServer((&fakeCommerce{status: 500, reply: `boom`}).handler())
	t.Cleanup(srv.Close)
	return fin, newClient(t, srv, metering.Config{})
}

func balanceCents(t *testing.T, fin finance.Client) int64 {
	t.Helper()
	bal, err := fin.Balance(context.Background(), "acme", "acme", "usd", false)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return bal.Cents()
}

// TestClientPinnedRequestIDBillsEveryAct is the bypass, closed.
//
// THE BUG. The edge propagates the caller's X-Request-Id verbatim (zip's RequestID
// middleware keeps an incoming one) and the gateway CORS-allows the header from a
// browser, so the value in metering.Usage.RequestID is CHOSEN BY THE PAYER. It was
// handed to the ledger as the debit's idempotency key, and the ledger did exactly what
// an idempotency key says: the first call posted and every call after it "replayed"
// into the first one's entry. One header, pinned, and inference was free — while the
// spend cap, which sums the ledger, never moved.
//
// THE PROPERTY. Twenty distinct calls are twenty acts and bill twenty times, however
// hard the caller pins its correlation header. The key is minted per act inside the
// meter (metering.Usage.Seal) and is not a field of the request.
//
// MUTATION PROOF: in Client.Record, key the ledger on the header again —
//
//	Ref: u.RequestID,   // instead of the sealed act's Ref
//
// and the wallet is 99¢ instead of 80¢: nineteen of twenty calls billed nobody. That is
// the shipped behaviour this test refuses.
func TestClientPinnedRequestIDBillsEveryAct(t *testing.T) {
	ctx := context.Background()
	fin, c := meteredWallet(t, 100)

	const pinned = "deadbeefdeadbeefdeadbeefdeadbeef" // one header, every call
	const calls = 20
	for i := range calls {
		if _, err := c.Record(ctx, metering.Usage{
			User: "acme", Org: "acme", AmountCents: 1,
			Model: fmt.Sprintf("zen-%d", i), RequestID: pinned,
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if got := balanceCents(t, fin); got != 100-calls {
		t.Fatalf("balance after %d pinned-header calls = %d¢; want %d¢ — every act must bill",
			calls, got, 100-calls)
	}
}

// TestSealedActIsExactlyOnceAcrossItsOwnRetry is the other half of the contract, and the
// half a fix for the bypass is most likely to break.
//
// An act's name is minted ONCE, by the server, and it travels WITH the act — so the
// meter re-sending one debit (a re-drive after a lost reply, a queued retry) finds the
// same ledger ref and moves the money once, while two DIFFERENT acts are two names and
// bill twice even when every field matches.
//
// MUTATION PROOF: make the mint non-stable — drop the guard in Usage.Seal so it mints
// unconditionally —
//
//	func (u Usage) Seal() Usage { u.Ref = mintRef(); return u }
//
// and Record re-names the caller's sealed act on the retry: the wallet drops 60¢ instead
// of 30¢. A double-charged customer is the exact failure the key exists to prevent.
func TestSealedActIsExactlyOnceAcrossItsOwnRetry(t *testing.T) {
	ctx := context.Background()
	fin, c := meteredWallet(t, 100)

	// ONE act, sealed by the caller because the caller intends to retry it.
	act := metering.Usage{User: "acme", Org: "acme", AmountCents: 30, Model: "zen-1"}.Seal()
	if act.Ref == "" {
		t.Fatal("Seal minted no act name")
	}
	for attempt := range 3 {
		if _, err := c.Record(ctx, act); err != nil {
			t.Fatalf("record attempt %d: %v", attempt, err)
		}
	}
	if got := balanceCents(t, fin); got != 70 {
		t.Fatalf("balance after 3 attempts at ONE act = %d¢; want 70¢ (charged once)", got)
	}

	// Sealing is idempotent: a second Seal on a named act keeps its name, which is what
	// lets Record seal freely without re-identifying an act its caller already named.
	if resealed := act.Seal(); resealed.Ref != act.Ref {
		t.Fatalf("re-Seal renamed the act: %q -> %q", act.Ref, resealed.Ref)
	}

	// Two DISTINCT acts, identical in every field, are two names and bill twice.
	for range 2 {
		if _, err := c.Record(ctx, metering.Usage{
			User: "acme", Org: "acme", AmountCents: 10, Model: "zen-1",
		}); err != nil {
			t.Fatalf("record distinct act: %v", err)
		}
	}
	if got := balanceCents(t, fin); got != 50 {
		t.Fatalf("balance after two distinct acts = %d¢; want 50¢ (both billed)", got)
	}
}
