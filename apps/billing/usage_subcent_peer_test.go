package billing

// usage_subcent_peer_test.go — a sub-cent debit on the PEER branch must not fail
// the read.
//
// The existing sub-cent test (usage_coresident_test.go) publishes a fake finance
// and exercises the LOCAL branch, where cents come from finance.UsageRow.Cents and
// nothing can error. Production runs the other branch: plugin/billing links no
// ledger, so finance.Current() is nil and the rows arrive over the plane from
// commerce as client.Money. That branch called Money.Minor(), which REFUSES an
// amount finer than a cent instead of rounding behind the caller — and a
// per-token AI charge is routinely finer than a cent.
//
// One such row failed the whole page, and usage() (billing.go:458) tests err
// before coResident, so the customer got 502 "billing upstream unreachable" with
// nothing upstream involved. That is the 2026-08-03 balance bug exactly;
// balance.go and apps/ai were converted to FloorMinor then, this row was missed.
//
// Measured on the live fleet 2026-08-06, same org, same ledger, same process:
//   GET /v1/billing/balance -> 200 {"balance":14989388,...}   (FloorMinor)
//   GET /v1/billing/usage   -> 502 "billing upstream unreachable"  (Minor)
// Only the call differed, which is what makes this a code fix and not config.

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/zap-proto/zip"
)

// subCentCommerce serves the commerce plane socket and answers the usage op with a
// page whose debits are finer than a cent — the shape a real AI ledger holds.
func subCentCommerce(t *testing.T, rows []client.UsageRow) {
	t.Helper()
	app := zip.New(zip.Config{AppName: "commerce"})
	zip.Post[struct{}, client.UsageRows](app, "/finance/usage",
		func(context.Context, *struct{}) (*client.UsageRows, error) {
			return &client.UsageRows{Rows: rows}, nil
		}, zip.WithOperationID(client.FinanceUsage))
	go func() { _ = app.Listen(zip.SocketPath("commerce")) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	path := zip.SocketPath("commerce")
	for range 400 {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never began listening — this test would otherwise pass by never reaching the peer", path)
}

// TestCoResidentUsage_SubCentPeerRowIsNotAnOutage is the regression. A page of
// sub-cent debits must come back as a page, not as an error the caller renders
// as a dead upstream.
func TestCoResidentUsage_SubCentPeerRowIsNotAnOutage(t *testing.T) {
	planeDir(t)
	finance.Publish(nil) // no local ledger — force the peer branch, as in prod
	t.Cleanup(func() { finance.Publish(nil) })

	rows := []client.UsageRow{
		{ID: "tiny-1", Model: "zen-1", Amount: client.Money{Decimal: "0.0025", Currency: "USD"}, CreatedAt: 1_700_000_000},
		{ID: "tiny-2", Model: "zen-1", Amount: client.Money{Decimal: "0.00007", Currency: "USD"}, CreatedAt: 1_700_000_100},
		{ID: "whole", Model: "gpt-x", Amount: client.Money{Decimal: "1.50", Currency: "USD"}, CreatedAt: 1_700_000_200},
	}
	subCentCommerce(t, rows)

	body, coResident, err := coResidentUsage(context.Background(), "acme", "", "")
	if err != nil {
		t.Fatalf("a sub-cent debit failed the usage read: %v\n"+
			"usage() turns this into 502 \"billing upstream unreachable\" with nothing upstream involved", err)
	}
	if !coResident {
		t.Fatal("the peer answered but the read reported fall-back — usage() would hand this to an " +
			"unconfigured commerce proxy and answer 501")
	}

	var env struct {
		Count int `json:"count"`
		Usage []struct {
			TransactionID string `json:"transactionId"`
			Amount        int64  `json:"amount"`
			Decimal       string `json:"decimal"`
		} `json:"usage"`
	}
	if uerr := json.Unmarshal(body, &env); uerr != nil {
		t.Fatalf("envelope not valid JSON: %v\n%s", uerr, body)
	}
	// Guard the iteration source before asserting over it: an empty page would make
	// every assertion below vacuously true, and an empty page is itself the bug
	// (the customer reads a blank ledger) — so it must fail here, loudly.
	if len(env.Usage) != len(rows) {
		t.Fatalf("envelope carries %d rows, want %d — the peer's page did not survive the read; "+
			"every assertion below would otherwise pass by examining nothing", len(env.Usage), len(rows))
	}

	byID := map[string]struct {
		cents   int64
		decimal string
	}{}
	for _, u := range env.Usage {
		byID[u.TransactionID] = struct {
			cents   int64
			decimal string
		}{u.Amount, u.Decimal}
	}

	// FloorMinor rounds DOWN: a sub-cent debit is 0 cents. That is correct and is
	// precisely why `decimal` must carry the exact value beside it.
	for _, id := range []string{"tiny-1", "tiny-2"} {
		got, ok := byID[id]
		if !ok {
			t.Fatalf("row %q missing from the envelope", id)
		}
		if got.cents != 0 {
			t.Errorf("%s: amount = %d cents, want 0 — the fixture must be sub-cent for this test to mean anything", id, got.cents)
		}
		if got.decimal == "" {
			t.Errorf("%s: decimal is EMPTY — the exact debit was dropped on the peer branch, so a page of "+
				"sub-cent calls totals ZERO and the customer is billed for work the statement cannot show", id)
		}
	}
	if d := byID["tiny-1"].decimal; d != "" {
		if !strings.Contains(d, "0.0025") {
			t.Errorf("tiny-1: decimal = %q, want the exact 0.0025 the ledger holds", d)
		}
	}
	// A whole-cent row must still round to its cents, unchanged.
	if got := byID["whole"]; got.cents != 150 {
		t.Errorf("whole: amount = %d cents, want 150", got.cents)
	}
	t.Logf("peer page survived: %d rows, sub-cent debits carried as decimal", env.Count)
}
