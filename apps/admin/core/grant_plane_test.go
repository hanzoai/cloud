package core

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sock"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// A grant is a WRITE, and apps are their own binaries — so the process serving
// /v1/admin/customers/:org/credit is never the process that holds the books.
// creditledger.Get() is permanently nil here, which is the ordinary arrangement
// and not a gap, and for a while it was answered with the literal string
// "commerce not configured" for a ledger one socket away. These tests pin the
// leg that answers it now.

// creditCall is what the fake commerce peer was asked for.
type creditCall struct {
	Org, Subject, Ref, Notes, Tags, Decimal string
}

// servePlaneCredit serves finance.credit + finance.balance as app "commerce" on
// this process's plane. It is the REAL op ids and the REAL wire — only the books
// are a fixture — so a test cannot pass through a path production closes.
func servePlaneCredit(t *testing.T, got *creditCall, balance string) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", sock.Dir(t))
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	zip.Post[client.CreditIn, client.Credited](cloud.Plane(), "/finance/credit",
		func(ctx context.Context, in *client.CreditIn) (*client.Credited, error) {
			// The org rides the CALLER, never an argument — the same rule the real
			// op enforces. client.CreditIn cannot name one, so this is the only place
			// a tenant can come from.
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden("credit: no org on the call")
			}
			if in.Ref == "" {
				return nil, zip.ErrBadRequest("credit: ref is required (the idempotency key)")
			}
			*got = creditCall{
				Org: org, Subject: in.Subject, Ref: in.Ref,
				Notes: in.Notes, Tags: in.Tags, Decimal: in.Amount.Decimal,
			}
			return &client.Credited{Amount: in.Amount, ID: "fe_from_commerce"}, nil
		}, zip.WithOperationID(client.FinanceCredit))

	zip.Post[client.BalanceIn, client.Balance](cloud.Plane(), "/finance/balance",
		func(ctx context.Context, _ *client.BalanceIn) (*client.Balance, error) {
			if cloud.Who(ctx).Org == "" {
				return nil, zip.ErrForbidden("balance: no org on the call")
			}
			return &client.Balance{Amount: client.Money{Decimal: balance, Currency: "USD"}}, nil
		}, zip.WithOperationID(client.FinanceBalance))

	stop, err := cloud.ServePlane("commerce", luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
}

// deposit drives grantDeposit inside a real request, so the caller identity the
// plane leg delegates is a real one rather than a constructed context.
func deposit(t *testing.T, org, subject string, cents int64) (before int64, txID string, after int64, exact string, err error) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/g", func(c *zip.Ctx) error {
		before, txID, after, exact, err = grantDeposit(c, org, subject, "usd", "a note", "admin-grant", "prepaid", cents)
		return c.JSON(200, map[string]string{"ok": "true"})
	})
	req := httptest.NewRequest("GET", "/g", nil)
	req.Header.Set("X-Org-Id", "admin")
	req.Header.Set("X-User-Id", "operator")
	resp, terr := app.Test(req)
	if terr != nil {
		t.Fatalf("deposit probe: %v", terr)
	}
	_ = resp.Body.Close()
	return before, txID, after, exact, err
}

// TestSplitDeployGrantAsksTheLedger is the defect itself: with no co-resident
// credit ledger the grant must ASK the process that owns one, and must credit the
// TENANT BEING GRANTED — not the operator's own org, which is where the caller's
// identity points.
func TestSplitDeployGrantAsksTheLedger(t *testing.T) {
	var got creditCall
	servePlaneCredit(t, &got, "70.00")

	before, txID, after, exact, err := deposit(t, "lux", "lux", 5000)
	if err != nil {
		t.Fatalf("grant refused: %v", err)
	}
	if got.Org != "lux" {
		t.Errorf("credited org = %q, want lux — As(c, org) must re-point the tenant, not carry the operator's", got.Org)
	}
	if got.Subject != "lux" {
		t.Errorf("credited subject = %q, want lux", got.Subject)
	}
	if got.Ref == "" {
		t.Error("no ref crossed — commerce refuses an empty ref, so the grant could not land")
	}
	if got.Tags != "admin-grant" {
		t.Errorf("tags = %q, want admin-grant — the bucket decides trial vs real money", got.Tags)
	}
	if got.Decimal == "" {
		t.Error("no amount crossed")
	}
	// The receipt cites COMMERCE's entry, not the key we sent it. Echoing the ref
	// back reads like an id and identifies nothing in commerce's books.
	if txID != "fe_from_commerce" {
		t.Errorf("transactionId = %q, want the ledger entry commerce returned", txID)
	}
	// 70.00 USD, rendered both ways the receipt carries.
	if before != 7000 || after != 7000 {
		t.Errorf("balances = (%d, %d), want (7000, 7000)", before, after)
	}
	if exact != "70000000000000000000" {
		t.Errorf("balanceExact = %q, want the 18-decimal rendering — Minor() would answer cents and understate by 10^16", exact)
	}
}

// TestSplitDeployGrantAddressesAMember: the old HTTP leg refused a
// member-addressed grant, because commerce's HTTP deposit is org-keyed and
// crediting the pool would have put the money where that member cannot spend it.
// client.CreditIn carries the subject, so that refusal is a false negative.
func TestSplitDeployGrantAddressesAMember(t *testing.T) {
	var got creditCall
	servePlaneCredit(t, &got, "0")

	if _, _, _, _, err := deposit(t, "hanzo", "hanzo/alice", 5000); err != nil {
		t.Fatalf("member-addressed grant refused: %v", err)
	}
	if got.Org != "hanzo" || got.Subject != "hanzo/alice" {
		t.Errorf("credited %s/%s, want hanzo/(hanzo/alice) — the member's own account, not the pool", got.Org, got.Subject)
	}
}

// TestSplitDeployGrantFailsRatherThanReportingSuccess: with no peer at all there
// is nowhere to fall back TO. A deployment that runs no commerce cannot credit
// anybody, and ErrNoPeer must fail the grant rather than be read as absence.
func TestSplitDeployGrantFailsRatherThanReportingSuccess(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", sock.Dir(t))
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	_, txID, _, _, err := deposit(t, "lux", "lux", 5000)
	if err == nil {
		t.Fatalf("a grant with no ledger anywhere reported success (txID=%q)", txID)
	}
}
