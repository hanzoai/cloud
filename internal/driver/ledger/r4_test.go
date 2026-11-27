package ledger_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/meter"
)

// Granted credit pays for a priced call: the gate admits it on credit alone, the debit
// draws that credit, and the next payment lands as spendable cash, not as a debt repaid.
//
// MUTATION PROOF: make a priced debit cash-only (a shortfall owed in cash) and the
// payment reads $0 cash / $40 credit instead of $10 / $30.
func TestGrantedCreditPaysForAPricedCall(t *testing.T) {
	ctx := context.Background()
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	defer finance.Publish(nil)
	if _, err := fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "grant"}); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCommerce{status: 500, reply: `boom`}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()
	c := newClient(t, srv, meter.Config{})
	if err := c.Authorize(ctx, meter.AuthInput{User: "acme", Org: "acme", AmountCents: 2000, Service: "zen"}); err != nil {
		t.Fatalf("a $20 priced call on $50 of granted credit: %v, want admitted", err)
	}
	if _, err := c.Record(ctx, meter.Usage{User: "acme", Org: "acme", AmountCents: 2000, RequestID: "z1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "sq_1", Cash: true}); err != nil {
		t.Fatal(err)
	}
	f, err := fin.Funds(ctx, "acme", "acme", false)
	if err != nil {
		t.Fatal(err)
	}
	if f.Cash.Cents() != 1000 || f.Credit.Cents() != 3000 {
		t.Fatalf("after a $20 call on credit and a $10 payment: cash %d credit %d; want 1000 and 3000", f.Cash.Cents(), f.Credit.Cents())
	}
}
