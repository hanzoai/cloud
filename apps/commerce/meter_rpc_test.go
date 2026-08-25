// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// meter_rpc_test.go holds the plane debit to the MONEY it moves.
//
// A debit crosses from the process that served the work to the process that owns the
// books, and it carries the name of the act it charges for. That name is what makes a
// re-send of ONE act ONE charge. It rides as a field of the crossing (plane.Usage.Ref)
// because metering.Usage.Ref is `json:"-"` — no request body anywhere may set the
// ledger's key — so the handler on THIS side is the only thing that can put a peer's
// act name into the ledger entry.
//
// Nothing measured that. The sending half has tests proving the name goes onto the wire;
// blanking the field where it comes off again left the entire suite green, and every
// re-sent debit took the money a second time. A test that watches the wire and not the
// wallet cannot see it.
//
// So this reads the WALLET. Two sends of one act leave one charge in it; two acts leave
// two. Neither figure can come out of a handler that forgets the name.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
)

// meterBooks stands the debit's whole path up in one process: a real per-org finance
// ledger published as the process-wide books, funded by a grant, and the REAL
// /finance/record handler over a metering client whose HTTP leg fails the test if it is
// ever dialled. The debit is co-resident by construction, so whatever lands in the wallet
// got there through the handler under test.
func meterBooks(t *testing.T, org string, grantCents int64) (meterOps, finance.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the debit left the process for %s — the co-resident ledger must take it", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	fin := finance.New(t.TempDir())
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })

	if _, err := fin.Deposit(context.Background(), types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(grantCents),
		Currency: "usd", Notes: "grant", Ref: "grant-1",
	}); err != nil {
		t.Fatalf("fund the wallet: %v", err)
	}

	m, err := metering.New(metering.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("metering client: %v", err)
	}
	return meterOps{m: m}, fin
}

// spent is what left the wallet: the grant less what is in it now.
func spent(t *testing.T, fin finance.Client, org string, grantCents int64) int64 {
	t.Helper()
	bal, err := fin.Balance(context.Background(), org, org, "usd", false)
	if err != nil {
		t.Fatalf("read the wallet: %v", err)
	}
	return grantCents - bal.Cents()
}

// debits counts the usage entries standing in org's books.
func debits(t *testing.T, fin finance.Client, org string) int {
	t.Helper()
	lister, ok := fin.(interface {
		ListUsage(context.Context, string, bool, int) ([]finance.UsageRow, error)
	})
	if !ok {
		t.Fatal("these books cannot list their own usage")
	}
	rows, err := lister.ListUsage(context.Background(), org, false, 0)
	if err != nil {
		t.Fatalf("list usage: %v", err)
	}
	return len(rows)
}

// send drives one debit through the real handler, as a peer whose org is the caller's.
func (o meterOps) send(t *testing.T, org, ref string, cents int64) {
	t.Helper()
	if _, err := o.record(cloud.For(context.Background(), org), &plane.RecordIn{
		Subject: org,
		Amount:  plane.Amount(money.FromCents(cents).Unwrap()),
		Usage:   plane.Usage{Model: "zen", Service: "ai", Ref: ref},
	}); err != nil {
		t.Fatalf("debit %q: %v", ref, err)
	}
}

// A peer that re-sends one act — a reply it never got, a queued debit run twice — is
// charged for the act, not for the sending.
func TestPlaneDebit_ReSendingOneActChargesOnce(t *testing.T) {
	const org, grant, price = "acme", 5_000, 1_200
	o, fin := meterBooks(t, org, grant)

	o.send(t, org, "act-1", price)
	o.send(t, org, "act-1", price)

	if got := spent(t, fin, org, grant); got != price {
		t.Errorf("one act re-sent took %d cents, want %d — the act's name did not reach the books", got, price)
	}
	if got := debits(t, fin, org); got != 1 {
		t.Errorf("one act re-sent wrote %d entries, want 1", got)
	}
}

// And the other direction, which is the half a handler could pass by refusing every
// second debit: two acts are two charges however alike they look.
func TestPlaneDebit_TwoActsChargeTwice(t *testing.T) {
	const org, grant, price = "acme", 5_000, 1_200
	o, fin := meterBooks(t, org, grant)

	o.send(t, org, "act-1", price)
	o.send(t, org, "act-2", price)

	if got := spent(t, fin, org, grant); got != 2*price {
		t.Errorf("two acts took %d cents, want %d", got, 2*price)
	}
	if got := debits(t, fin, org); got != 2 {
		t.Errorf("two acts wrote %d entries, want 2", got)
	}
}

// An act sent with no name still charges, and charges once per sending: an unnamed act
// is a debit that stands alone, never a debit that is skipped. This is the branch that
// keeps the two tests above from being satisfiable by a handler that simply drops every
// debit it cannot name.
func TestPlaneDebit_AnUnnamedActStandsAlone(t *testing.T) {
	const org, grant, price = "acme", 5_000, 1_200
	o, fin := meterBooks(t, org, grant)

	o.send(t, org, "", price)
	o.send(t, org, "", price)

	if got := spent(t, fin, org, grant); got != 2*price {
		t.Errorf("two unnamed acts took %d cents, want %d", got, 2*price)
	}
}
