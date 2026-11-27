package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
)

type lineView struct {
	Available   int64  `json:"available"`
	Cash        int64  `json:"cash"`
	Credit      int64  `json:"credit"`
	CreditLimit *int64 `json:"creditLimit"`
	Due         *int64 `json:"due"`
	Line        *int64 `json:"line"`
}

// The balance view shows the org's credit line: what it owes on the line is due, a
// payment pays the debt first, and the line is whole again once it is paid.
func TestBalanceShowsTheLineAndWhatIsDue(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	ctx := context.Background()
	if _, err := fin.SetLimit(ctx, "acme", false, money.FromCents(1000)); err != nil {
		t.Fatal(err)
	}
	if err := fin.RecordUsage(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "u1"}); err != nil {
		t.Fatal(err)
	}
	app := mountApp(t)
	read := func() lineView {
		t.Helper()
		code, body := call(t, app, http.MethodGet, "/v1/billing/balance", "acme/dave", "acme")
		if code != http.StatusOK {
			t.Fatalf("balance = %d (%s)", code, body)
		}
		var v lineView
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		if v.CreditLimit == nil || v.Due == nil || v.Line == nil {
			t.Fatalf("the line went unreported: %s", body)
		}
		return v
	}
	if v := read(); *v.CreditLimit != 1000 || *v.Due != 1000 || *v.Line != 0 || v.Available != 0 || v.Cash != 0 {
		t.Fatalf("spent to the limit = %+v; want limit 1000, due 1000, line 0, nothing available", v)
	}
	if _, err := fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1500), Ref: "sq_1", Cash: true}); err != nil {
		t.Fatal(err)
	}
	if v := read(); *v.Due != 0 || v.Cash != 500 || *v.Line != 1000 || v.Available != 1500 {
		t.Fatalf("after $15 = %+v; want due 0, cash 500, line 1000, available 1500", v)
	}
}
