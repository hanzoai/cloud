package ai

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/zap-proto/zip"
)

// A wallet funded only by a grant admits a priced call. This is the admin org as the
// gate read it in production: $100,505 of granted credit, no cash, and every
// /v1/decisions call refused 402 because the reader returned the cash half, 0¢.
//
// MUTATION PROOF: read funds.Cash in balance and this reads 0.
func TestGrantedCreditAdmitsAPricedCall(t *testing.T) {
	ctx := context.Background()
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil); _ = fin.Close() })

	deposit := func(in money.DepositInput) {
		t.Helper()
		if _, err := fin.Deposit(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	deposit(money.DepositInput{Org: "admin", Subject: "admin", Amount: money.FromCents(10_050_500), Ref: "grant"})
	if got, err := balance(ctx, "admin", "admin", "usd"); err != nil || got != 10_050_500 {
		t.Fatalf("a wallet of granted credit = %d, %v; want 10050500", got, err)
	}
	deposit(money.DepositInput{Org: "admin", Subject: "admin", Amount: money.FromCents(2000), Ref: "sq_1", Cash: true})
	if got, err := balance(ctx, "admin", "admin", "usd"); err != nil || got != 10_052_500 {
		t.Fatalf("after a payment = %d, %v; want 10052500, cash and credit together", got, err)
	}
	// Floored: a sub-cent tail never rounds a balance up past what it can pay.
	tail, err := money.ParseUSD("0.009")
	if err != nil {
		t.Fatal(err)
	}
	deposit(money.DepositInput{Org: "admin", Subject: "admin", Amount: tail, Ref: "tail"})
	if got, err := balance(ctx, "admin", "admin", "usd"); err != nil || got != 10_052_500 {
		t.Fatalf("with a sub-cent tail = %d, %v; want 10052500, floored", got, err)
	}
}

// The ai process holds no ledger, so in production the read crosses the plane to
// commerce, which answers Amount beside Cash and Credit. The gate weighs Amount.
//
// The peer answers the way apps/commerce planeBalance does: finance.Funds of the
// caller's org, split. This is the hanzo org's wallet as measured: credit, no cash.
//
// MUTATION PROOF: read bal.Cash in balance and this reads 0.
func TestThePlaneReadWeighsCreditAndCash(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	finance.Publish(nil) // the ai process: no ledger here, so the read must cross

	books := finance.New(finance.Local(t.TempDir(), testMaster))
	t.Cleanup(func() { _ = books.Close() })
	ctx := context.Background()
	if _, err := books.Deposit(ctx, money.DepositInput{Org: "hanzo", Subject: "hanzo", Amount: money.FromCents(14_759_266), Ref: "grant"}); err != nil {
		t.Fatal(err)
	}

	app := zip.New(zip.Config{AppName: "commerce"})
	app.Post("/finance/balance", func(ctx context.Context, in *client.BalanceIn) (*client.Balance, error) {
		funds, err := books.Funds(ctx, cloud.Who(ctx).Org, in.Subject, false)
		if err != nil {
			return nil, err
		}
		return &client.Balance{
			Amount: client.Amount(funds.Total().Unwrap()),
			Cash:   client.Amount(funds.Cash.Unwrap()),
			Credit: client.Amount(funds.Credit.Unwrap()),
		}, nil
	}, zip.WithOperationID(client.FinanceBalance))
	path := zip.SocketPath("commerce")
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitForCommerce(t, path)

	if got, err := balance(ctx, "hanzo", "hanzo", "usd"); err != nil || got != 14_759_266 {
		t.Fatalf("hanzo's granted credit over the plane = %d, %v; want 14759266", got, err)
	}
}
