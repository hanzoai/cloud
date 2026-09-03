package domain

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/metering"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"

	// devmaster keys this test binary: cek opens no ledger file without a master, and a
	// test process has no KMS to resolve one from.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// fundedBiller wires the REAL production biller — meterBiller over cloud's Meter
// over the metering client over the finance ledger — to a wallet with seedCents in it.
//
// Every layer is the real one because the question is what the LEDGER does with a
// purchase, and only a ledger can answer that: a spy on Capture would show three calls
// and say nothing about whether three charges were taken.
func fundedBiller(t *testing.T, seedCents int64) (finance.Client, *meterBiller) {
	t.Helper()
	fin := finance.New(finance.Local(t.TempDir()))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil); _ = fin.Close() })
	if _, err := fin.Deposit(context.Background(), types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(seedCents),
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}
	// A configured meter that never speaks HTTP: finance is published, so the debit takes
	// the co-resident native path.
	meter, err := metering.New(metering.Config{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return fin, &meterBiller{rm: cloud.NewMeter(cloud.Deps{Metering: meter}, "domain")}
}

// settledCents waits for the fire-and-forget debits to land and reports the wallet.
//
// Capture returns before the money moves — deliberately, so a registrar success is never
// held up by a ledger write — so a test that read the balance immediately would be
// reading the gap rather than the result. It waits for the balance to REACH want and
// then holds still, so it can neither pass early on a value in flight nor pass late on
// one that overshot.
func settledCents(t *testing.T, fin finance.Client, want int64) int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got int64
	for time.Now().Before(deadline) {
		bal, err := fin.Balance(context.Background(), "acme", "acme", "usd", false)
		if err != nil {
			t.Fatalf("balance: %v", err)
		}
		got = bal.Cents()
		if got == want {
			time.Sleep(50 * time.Millisecond) // a late fourth debit would show up here
			bal, err = fin.Balance(context.Background(), "acme", "acme", "usd", false)
			if err != nil {
				t.Fatalf("balance: %v", err)
			}
			return bal.Cents()
		}
		time.Sleep(10 * time.Millisecond)
	}
	return got
}

// RENEWING A DOMAIN THREE TIMES COSTS THREE RENEWALS.
//
// THE BUG. The three purchase sites named the debit after the DOMAIN —
// "domain:register:<name>", "domain:renew:<name>", "domain:transfer:<name>" — and that
// string rode across as the ledger's idempotency key. A ref names an ACT, and a domain is
// not an act: it is a thing that can be bought again. So the second renewal of one domain
// found the first renewal's entry, replayed into it, and moved no money — the customer got
// another year for free, every year, and the spend cap never saw it. Nothing failed and
// nothing logged; the books simply agreed with themselves.
//
// THE PROPERTY. Three renewals of one domain are three acts and are charged three times.
// The key is minted per debit by the server (metering.Usage.Seal), so it cannot collide
// with an earlier purchase of the same name.
//
// Nothing is lost by not naming one: Capture is fire-and-forget and is never re-driven,
// and it runs only AFTER the registrar has confirmed — the two failure paths that must not
// charge (refused authorize, registrar error) return before reaching it.
//
// MUTATION PROOF: give Capture the ref back and hand it to the ledger —
//
//	func (m *meterBiller) Capture(org string, cents int64, ref string) {
//	    m.rm.Record(org, "domain.register", metering.Usage{..., Ref: ref})
//
// and the wallet ends at 95¢ instead of 85¢: two of the three renewals were free.
func TestRenewingOneDomainThreeTimesChargesThreeTimes(t *testing.T) {
	fin, bill := fundedBiller(t, 100)

	const renewals = 3
	for range renewals {
		bill.Capture("acme", 5)
	}
	if got := settledCents(t, fin, 100-renewals*5); got != 100-renewals*5 {
		t.Fatalf("wallet after %d renewals of ONE domain = %d¢; want %d¢ — each renewal is its own purchase",
			renewals, got, 100-renewals*5)
	}
}

// Buying the same name back is also a purchase: a domain that lapses and is registered
// again months later must be charged again, and under the old key it never was — the
// original registration's entry was still sitting there under "domain:register:<name>".
func TestReRegisteringALapsedDomainCharges(t *testing.T) {
	fin, bill := fundedBiller(t, 100)

	bill.Capture("acme", 20) // the original registration
	if got := settledCents(t, fin, 80); got != 80 {
		t.Fatalf("wallet after the first registration = %d¢; want 80¢", got)
	}
	bill.Capture("acme", 20) // the same name, bought again after it lapsed
	if got := settledCents(t, fin, 60); got != 60 {
		t.Fatalf("wallet after re-registering the same name = %d¢; want 60¢ — it is a second purchase", got)
	}
}

// The client itself carries no key any more, and that is what keeps the fix from being
// undone by a well-meaning edit: there is no ref parameter to route back to the ledger.
// Restoring the bug means restoring the parameter, at all three purchase sites, which is
// a change nobody makes by accident.
func TestTheBillerCannotBeHandedAKey(t *testing.T) {
	var b Biller = &meterBiller{}
	// Capture takes (org, cents) and nothing else. This is a COMPILE-time assertion; it
	// stops building the moment a third argument comes back.
	var capture func(string, int64) = b.Capture
	if capture == nil {
		t.Fatal("Biller.Capture is nil")
	}
}
