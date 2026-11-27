// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

func status(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// Setting a credit limit is a SuperAdmin's act, recorded on the trail before it is
// made with the account before and after; a caller who is not a SuperAdmin is
// refused, and a trail that does not take it refuses the change — neither moves the
// limit.
func TestPlaneLimitIsSuperAdminOnlyAndAudited(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	ctx := context.Background()
	in := &client.LimitIn{Limit: client.Amount(money.FromCents(1000).Unwrap()), Reason: "postpaid"}
	limitOf := func() int64 {
		l, err := fin.Line(ctx, "acme", false)
		if err != nil {
			t.Fatal(err)
		}
		return l.Limit.Cents()
	}

	o := lineOps{trail: trail}
	if _, err := o.limit(cloud.For(ctx, "acme"), in); status(err) != http.StatusForbidden {
		t.Fatalf("a caller who is not a SuperAdmin = %v; want 403", err)
	}
	if rows, _, _ := trail.Query(ctx, audit.Filter{Action: "admin.credit.limit"}); len(rows) != 0 || limitOf() != 0 {
		t.Fatalf("a refused change was recorded or made: %d rows, limit %d", len(rows), limitOf())
	}

	super := zip.WithCaller(ctx, zip.Caller{Org: "acme", Owner: "admin", User: "admin/z", Email: "z@hanzo.ai", Admin: true})
	if _, err := (lineOps{}).limit(super, in); status(err) != http.StatusServiceUnavailable || limitOf() != 0 {
		t.Fatalf("no trail = %v, limit %d; want 503 and nothing set", err, limitOf())
	}
	neg := &client.LimitIn{Limit: client.Amount(money.FromCents(-1).Unwrap())}
	if _, err := o.limit(super, neg); status(err) != http.StatusBadRequest {
		t.Fatalf("a negative limit = %v; want 400", err)
	}

	got, err := o.limit(super, in)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := got.Limit.FloorMinor(); c != 1000 || limitOf() != 1000 {
		t.Fatalf("limit = %v (books %d); want 1000", got.Limit, limitOf())
	}
	rows, total, err := trail.Query(ctx, audit.Filter{Action: "admin.credit.limit"})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("the change is not on the trail once: total %d, %v", total, err)
	}
	r := rows[0]
	if r.Resource.ID != "acme" || r.Actor.Org != "admin" || r.Actor.Sub != "admin/z" || r.Outcome.Result != "success" {
		t.Fatalf("audit record = %+v", r)
	}
	if !strings.Contains(string(r.Before), `"limit":"0`) || !strings.Contains(string(r.After), `"limit":"10`) {
		t.Fatalf("before/after = %s / %s; want limit 0 then 10", r.Before, r.After)
	}
}

// The balance op reports the line beside the money: what is left of it, what is owed,
// and the limit, and the spendable amount counts the line.
func TestPlaneBalanceReportsTheLine(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	ctx := context.Background()
	if _, err := fin.SetLimit(ctx, "acme", false, money.FromCents(1000)); err != nil {
		t.Fatal(err)
	}
	if err := fin.RecordUsage(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(400), Ref: "u1"}); err != nil {
		t.Fatal(err)
	}
	got, err := planeBalance(cloud.For(ctx, "acme"), &client.BalanceIn{Currency: "usd"})
	if err != nil {
		t.Fatal(err)
	}
	amount, _ := got.Amount.FloorMinor()
	line, _ := got.Line.FloorMinor()
	owed, _ := got.Owed.FloorMinor()
	limit, _ := got.Limit.FloorMinor()
	if amount != 600 || line != 600 || owed != 400 || limit != 1000 {
		t.Fatalf("balance = amount %d line %d owed %d limit %d; want 600 600 400 1000", amount, line, owed, limit)
	}
	l, err := (lineOps{}).line(cloud.For(ctx, "acme"), nil)
	if err != nil || !l.Books {
		t.Fatalf("line = %+v, %v", l, err)
	}
	if left, _ := l.Left.FloorMinor(); left != 600 {
		t.Fatalf("line left = %d; want 600", left)
	}
}

// The line's total is the balance op's amount for the org's own wallet, exactly: cash,
// the line left and granted credit as finance.Funds.Total adds them, sub-cent tail and
// all, so a reader that floors either one reads one number.
func TestPlaneLineTotalIsTheBalance(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	ctx := context.Background()
	grant, err := money.ParseUSD("2.505")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fin.SetLimit(ctx, "acme", false, money.FromCents(1000)); err != nil {
		t.Fatal(err)
	}
	for _, in := range []money.DepositInput{
		{Org: "acme", Subject: "acme", Amount: money.FromCents(300), Cash: true, Ref: "wire:t1"},
		{Org: "acme", Subject: "acme", Amount: grant, Ref: "g1"},
	} {
		if _, err := fin.Deposit(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if err := fin.RecordUsage(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(700), Ref: "u1"}); err != nil {
		t.Fatal(err)
	}
	bal, err := planeBalance(cloud.For(ctx, "acme"), &client.BalanceIn{Currency: "usd"})
	if err != nil {
		t.Fatal(err)
	}
	l, err := (lineOps{}).line(cloud.For(ctx, "acme"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != bal.Amount {
		t.Fatalf("line total %+v; want the balance amount %+v", l.Total, bal.Amount)
	}
	// $3 cash, $10 line, $2.505 credit; $7 of usage takes the cash and $4 of the line.
	cash, _ := l.Cash.FloorMinor()
	left, _ := l.Left.FloorMinor()
	credit, _ := l.Credit.FloorMinor()
	total, _ := l.Total.FloorMinor()
	if cash != 0 || left != 600 || credit != 250 || total != 850 {
		t.Fatalf("line = cash %d left %d credit %d total %d; want 0 600 250 850", cash, left, credit, total)
	}

	none, err := (lineOps{}).line(cloud.For(ctx, "globex"), nil)
	if err != nil || none.Books {
		t.Fatalf("an org with no books = %+v, %v", none, err)
	}
	if c, err := none.Total.FloorMinor(); err != nil || c != 0 {
		t.Fatalf("no books total = %d, %v; want 0", c, err)
	}
}

// The usage op lists every served call the log counted, a $0 one included, with what
// it cost us and who paid.
func TestPlaneUsageListsAFreeCall(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	ctx := context.Background()
	if err := fin.RecordUsage(ctx, money.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.Zero(), Cost: money.FromCents(2),
		Model: "zen-free", Class: "free", Units: 900, Ref: "f1",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := planeUsage(cloud.For(ctx, "acme"), nil)
	if err != nil || len(rows.Rows) != 1 {
		t.Fatalf("usage = %+v, %v; want the one free call", rows, err)
	}
	r := rows.Rows[0]
	cost, _ := r.Cost.FloorMinor()
	if r.PaidBy != money.PaidByHanzo || r.Class != "free" || r.Units != 900 || cost != 2 || r.Amount.Decimal != "0" {
		t.Fatalf("free row = %+v; want $0, cost 2c, paid by hanzo", r)
	}
}

// Waiving an org's owed balance is SuperAdmin-only, audited before it moves, and
// brings Owed to zero while keeping granted credit intact.
func TestPlaneWaiveIsSuperAdminOnlyAndAudited(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	ctx := context.Background()

	if _, err := fin.SetLimit(ctx, "acme", false, money.FromCents(2513)); err != nil {
		t.Fatal(err)
	}
	if err := fin.RecordUsage(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2513), Ref: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fin.SetLimit(ctx, "acme", false, money.Zero()); err != nil {
		t.Fatal(err)
	}
	if _, err := fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1500), Ref: "g1"}); err != nil {
		t.Fatal(err)
	}

	o := lineOps{trail: trail}
	in := &client.WaiveIn{Reason: "phantom local CLI charges"}
	if _, err := o.waive(cloud.For(ctx, "acme"), in); status(err) != http.StatusForbidden {
		t.Fatalf("non-SuperAdmin waive = %v; want 403", err)
	}
	super := zip.WithCaller(ctx, zip.Caller{Org: "acme", Owner: "admin", User: "admin/a", Email: "a@hanzo.ai", Admin: true})
	if _, err := (lineOps{}).waive(super, in); status(err) != http.StatusServiceUnavailable {
		t.Fatalf("no trail waive = %v; want 503", err)
	}
	got, err := o.waive(super, in)
	if err != nil {
		t.Fatalf("waive: %v", err)
	}
	if owed, _ := got.Owed.FloorMinor(); owed != 0 {
		t.Fatalf("owed after waive = %d; want 0", owed)
	}
	if cred, _ := got.Credit.FloorMinor(); cred != 1500 {
		t.Fatalf("credit after waive = %d; want 1500", cred)
	}
	if rows, _, _ := trail.Query(ctx, audit.Filter{Action: "admin.credit.waive"}); len(rows) != 1 {
		t.Fatalf("audit rows = %d; want 1", len(rows))
	}
}
