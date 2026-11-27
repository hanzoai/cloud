// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// settle_balance_test.go — a top-up answers ONE balance, and it is the one
// GET /v1/billing/balance reports.
//
// Commerce's money core read a balance out of its own per-subject table and sent it
// as balanceCents. That table is a different book from the finance ledger the spend
// gate and the customer's balance page read, and the two disagreed (4000 against
// 3500 on api-test.hanzo.ai, after the same $5). The host owns the number: it is
// read from the ledger after the credit is posted, so it cannot be stale and cannot
// belong to another book.

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/commerce/billing/creditledger"
	"net/http"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
)

// seeded funds the fixtures' wallet before the top-up, so the balance after it is
// the ledger's TOTAL and not merely the amount that was just charged.
func seeded(t *testing.T, cents int64) {
	t.Helper()
	fin := funded(t)
	if _, err := fin.Deposit(context.Background(), money.DepositInput{
		Org: gateOrg, Subject: wallet(gateOrg), Amount: money.FromCents(cents),
		Currency: "usd", Notes: "seed", Tags: "grant", Ref: "seed-1",
	}); err != nil {
		t.Fatalf("seed the wallet: %v", err)
	}
}

// Mutation proof: set balanceCents from the settled amount instead of the ledger and
// the answer reads 4200 against the page's 5200.
func TestSettle_TheBrowserTopUpAnswersTheBalanceTheBalancePageReports(t *testing.T) {
	const prior = 1000
	seeded(t, prior)
	commerceBanked(t)
	// commerce's answer: the receipt and the charge, and no balance.
	app := creditEndpointBody(t, stating(riskGate(luxlog.New("settletest")), settlement{cents: gateCents, currency: "usd"}),
		`{"transactionId":"`+settledReceipt+`","status":"ok","processorRef":"`+settledRef+`"}`)

	code, body := topup(t, app)
	if code != http.StatusOK {
		t.Fatalf("the top-up answered %d %s", code, body)
	}
	var got struct {
		BalanceCents int64 `json:"balanceCents"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("the answer is not JSON: %v (%s)", err, body)
	}
	page := reportedBalance(t, mountReader(t))
	if want := int64(prior) + gateCents; page != want || got.BalanceCents != want {
		t.Fatalf("answer balanceCents=%d, /v1/billing/balance=%d, want both %d (the ledger's total, not the charge)", got.BalanceCents, page, want)
	}
}

// The typed op is the other way in: the saved-card and the browser top-ups and an
// agent's tools/call all answer through it.
func TestSettle_TheTypedTopUpAnswersTheBalanceTheBalancePageReports(t *testing.T) {
	const prior = 1000
	seeded(t, prior)
	commerceBanked(t)
	quiet(t)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(allowAll)
	s := stating(riskGate(luxlog.New("settletest")), settlement{cents: gateCents, currency: "usd"})

	core := func(context.Context, *client.CardIn) (*client.Charged, error) {
		return &client.Charged{TransactionID: settledReceipt, ProcessorRef: settledRef, Status: "ok"}, nil
	}
	op := screened(s, "/billing/topup/card", true,
		func(_ context.Context, in *client.CardIn) (int64, string) { return in.AmountCents, in.Currency },
		func(in *client.CardIn) string { return in.Subject },
		func(_ *client.CardIn, out *client.Charged) cleared {
			return cleared{ref: firstRefOf(out.ProcessorRef, out.TransactionID), id: out.TransactionID}
		},
		func(out *client.Charged, cents int64) { out.BalanceCents = cents }, core)

	buyer := zip.WithCaller(context.Background(), zip.Caller{Org: gateOrg, User: gateUser})
	out, err := op(buyer, &client.CardIn{SourceID: "pm_x", AmountCents: gateCents, Currency: "usd", Subject: wallet(gateOrg)})
	if err != nil {
		t.Fatalf("the typed top-up: %v", err)
	}
	page := reportedBalance(t, mountReader(t))
	if want := int64(prior) + gateCents; page != want || out.BalanceCents != want {
		t.Fatalf("typed balanceCents=%d, /v1/billing/balance=%d, want both %d", out.BalanceCents, page, want)
	}
}

// commerceBanked is the credit commerce posts to the ledger before it answers a top-up:
// the host no longer writes it, so the test states it where commerce would have.
func commerceBanked(t *testing.T) {
	t.Helper()
	if _, _, err := (ledger{}).Credit(context.Background(), creditledger.CreditInput{
		Org: gateOrg, Subject: wallet(gateOrg), Currency: "usd", Reason: "Top-up via card " + settledRef,
		Tag: "topup", IdempotencyKey: settledRef, AmountCents: gateCents, Cash: true,
	}); err != nil {
		t.Fatalf("commerce's credit: %v", err)
	}
}
