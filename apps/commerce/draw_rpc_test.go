// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// A draw is all or nothing, once per ref, from the caller's own books: a wallet
// that holds less answers 402 with nothing moved, a second draw under the same
// ref moves nothing, and a ref naming another amount is 409.
func TestPlaneDrawIsAllOrNothingOncePerRef(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	if _, err := fin.Deposit(context.Background(), money.DepositInput{Org: "acme", Subject: "acme",
		Amount: money.FromCents(500), Currency: "usd", Ref: "fund"}); err != nil {
		t.Fatal(err)
	}
	acme := cloud.For(context.Background(), "acme")
	draw := func(cents int64, ref string) (*client.Drawn, int) {
		t.Helper()
		out, err := planeDraw(acme, &client.DrawIn{Amount: client.Amount(money.FromCents(cents).Unwrap()), Ref: ref, Memo: "job:j1"})
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return nil, he.Status
		}
		if err != nil {
			t.Fatalf("draw %d %s: %v", cents, ref, err)
		}
		return out, http.StatusOK
	}
	balance := func() money.Amount {
		t.Helper()
		b, err := fin.Balance(context.Background(), "acme", "acme", "usd", false)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if out, code := draw(300, "x402_a"); code != http.StatusOK || !out.Posted || out.Entry == "" {
		t.Fatalf("a covered draw = %+v %d", out, code)
	}
	if out, code := draw(300, "x402_a"); code != http.StatusOK || out.Posted {
		t.Fatalf("the same draw again = %+v %d, want the first entry and nothing moved", out, code)
	}
	if _, code := draw(1000, "x402_b"); code != http.StatusPaymentRequired {
		t.Fatalf("a draw past the wallet = %d, want 402", code)
	}
	if _, code := draw(100, "x402_a"); code != http.StatusConflict {
		t.Fatalf("a ref naming another amount = %d, want 409", code)
	}
	if got := balance(); got.Cmp(money.FromCents(200)) != 0 {
		t.Fatalf("balance %s, want 2 — one draw of 3 and nothing else", got)
	}
	if _, err := planeDraw(cloud.For(context.Background(), ""), &client.DrawIn{Amount: client.Amount(money.FromCents(1).Unwrap()), Ref: "r"}); err == nil {
		t.Fatal("a draw with no org on the call moved money")
	}
}

// A hold sets a payment aside in the caller's own books, over the plane: a wallet
// that holds less answers 402, the draw under the same ref is paid from the hold
// though the wallet reads empty, and a return after it gives back nothing; a hold
// given up returns once, and its ref never holds again.
func TestPlaneHoldIsPaidOrReturnedOnce(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	if _, err := fin.Deposit(context.Background(), money.DepositInput{Org: "acme", Subject: "acme",
		Amount: money.FromCents(500), Currency: "usd", Ref: "fund"}); err != nil {
		t.Fatal(err)
	}
	acme := cloud.For(context.Background(), "acme")
	status := func(err error) int {
		t.Helper()
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return he.Status
		}
		if err != nil {
			t.Fatal(err)
		}
		return http.StatusOK
	}
	hold := func(cents int64, ref string) int {
		t.Helper()
		_, err := planeHold(acme, &client.HoldIn{Amount: client.Amount(money.FromCents(cents).Unwrap()), Ref: ref, Memo: "job:j1"})
		return status(err)
	}
	release := func(ref string) money.Amount {
		t.Helper()
		out, err := planeReturn(acme, &client.ReturnIn{Ref: ref})
		if err != nil {
			t.Fatal(err)
		}
		back, err := out.Amount.Parse()
		if err != nil {
			t.Fatal(err)
		}
		return money.FromDecimal(back.Decimal())
	}
	balance := func() int64 {
		t.Helper()
		b, err := fin.Balance(context.Background(), "acme", "acme", "usd", false)
		if err != nil {
			t.Fatal(err)
		}
		return b.Cents()
	}
	if code := hold(600, "x402_a"); code != http.StatusPaymentRequired {
		t.Fatalf("a hold past the wallet = %d, want 402", code)
	}
	if code := hold(500, "x402_a"); code != http.StatusOK || balance() != 0 {
		t.Fatalf("a covered hold = %d, balance %d", code, balance())
	}
	if code := hold(1, "x402_b"); code != http.StatusPaymentRequired {
		t.Fatalf("a hold of money already held = %d, want 402", code)
	}
	if _, err := planeDraw(acme, &client.DrawIn{Amount: client.Amount(money.FromCents(500).Unwrap()), Ref: "x402_a"}); err != nil {
		t.Fatalf("the held payment: %v", err)
	}
	if back := release("x402_a"); !back.IsZero() {
		t.Fatalf("a return after the payment gave back %s", back)
	}
	if _, err := fin.Deposit(context.Background(), money.DepositInput{Org: "acme", Subject: "acme",
		Amount: money.FromCents(300), Currency: "usd", Ref: "fund2"}); err != nil {
		t.Fatal(err)
	}
	if code := hold(300, "x402_c"); code != http.StatusOK || balance() != 0 {
		t.Fatalf("hold = %d, balance %d", code, balance())
	}
	if back := release("x402_c"); back.Cents() != 300 || balance() != 300 {
		t.Fatalf("return gave back %s, balance %d", back, balance())
	}
	if back := release("x402_c"); !back.IsZero() {
		t.Fatalf("a second return gave back %s", back)
	}
	if code := hold(300, "x402_c"); code != http.StatusConflict {
		t.Fatalf("a hold again under a returned ref = %d, want 409", code)
	}
	if _, err := planeHold(cloud.For(context.Background(), ""), &client.HoldIn{Amount: client.Amount(money.FromCents(1).Unwrap()), Ref: "r"}); err == nil {
		t.Fatal("a hold with no org on the call moved money")
	}
}
