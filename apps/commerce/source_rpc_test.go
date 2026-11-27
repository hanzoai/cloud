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
	"github.com/hanzoai/cloud/sdk/network"
	"github.com/zap-proto/zip"
)

// The balance op answers where the money came from beside how much there is, and the
// two parts add up to it.
func TestPlaneBalanceSplitsCashFromCredit(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	ctx := context.Background()
	for _, in := range []money.DepositInput{
		{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "grant-1"},
		{Org: "acme", Subject: "acme", Amount: money.FromCents(2000), Ref: "sq_1", Cash: true},
	} {
		if _, err := fin.Deposit(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	got, err := planeBalance(cloud.For(ctx, "acme"), &client.BalanceIn{Currency: "usd"})
	if err != nil {
		t.Fatal(err)
	}
	total, _ := got.Amount.FloorMinor()
	cash, _ := got.Cash.FloorMinor()
	credit, _ := got.Credit.FloorMinor()
	if total != 2500 || cash != 2000 || credit != 500 {
		t.Fatalf("balance = %d (cash %d, credit %d); want 2500 (2000, 500)", total, cash, credit)
	}
}

// Source reads every deposit, counts the named ones as payments and leaves the rest
// credit; a dry run moves nothing, apply moves the unspent part once, and a name that
// is not a deposit here refuses the call.
func TestPlaneSourceRecordsNamedPayments(t *testing.T) {
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	ctx := context.Background()
	wire, err := fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(20000), Ref: "grant-w",
		Notes: "Admin grant by z@hanzo.ai: Wire received out of band"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "grant-c",
		Notes: "Admin grant by z@hanzo.ai: comp"}); err != nil {
		t.Fatal(err)
	}
	if err := fin.RecordUsage(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Ref: "m1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := planeSource(cloud.For(ctx, "acme"), &client.SourceIn{}); err == nil {
		t.Fatal("a caller that is not a SuperAdmin sourced deposits")
	}
	acme := zip.WithCaller(ctx, zip.Caller{Org: "acme", Admin: true})
	paid := []client.SourcePaid{{Entry: wire, Basis: "admin grant (prepaid): Wire received out of band"}}

	dry, err := planeSource(acme, &client.SourceIn{Paid: paid})
	if err != nil {
		t.Fatal(err)
	}
	cash := map[string]bool{}
	for _, d := range dry.Deposits {
		cash[d.Ref] = d.Cash
	}
	if len(dry.Deposits) != 2 || !cash["grant-w"] || cash["grant-c"] || len(dry.Wallets) != 1 || dry.Wallets[0].Posted != "" {
		t.Fatalf("dry run = %+v", dry)
	}
	if c, _ := dry.Wallets[0].Cash.FloorMinor(); c != 19000 {
		t.Fatalf("dry run cash = %d, want 19000 (the wire less the usage it paid for, cash first)", c)
	}
	if f, _ := fin.Funds(ctx, "acme", "acme", false); f.Cash.Sign() != 0 {
		t.Fatalf("a dry run moved money: %+v", f)
	}

	done, err := planeSource(acme, &client.SourceIn{Paid: paid, Apply: true})
	if err != nil || len(done.Wallets) != 1 || done.Wallets[0].Posted == "" {
		t.Fatalf("apply = %+v, %v", done, err)
	}
	f, _ := fin.Funds(ctx, "acme", "acme", false)
	if f.Cash.Cents() != 19000 || f.Credit.Cents() != 500 {
		t.Fatalf("after apply cash %d credit %d; want 19000 / 500", f.Cash.Cents(), f.Credit.Cents())
	}
	org, err := planeCash(acme, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := org.Paid.FloorMinor(); p != 20000 || !org.Books {
		t.Fatalf("org cash = %+v", org)
	}

	_, err = planeSource(acme, &client.SourceIn{Paid: []client.SourcePaid{{Entry: "dep_nope", Basis: "x"}}})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
		t.Fatalf("an unknown entry = %v, want 400", err)
	}
}

// The notes a settlement path writes name their processor reference; nothing else does.
func TestSettlementNote(t *testing.T) {
	for memo, want := range map[string]string{
		"Top-up via square (ref: rzx12WDuSBL47AT4txk5pp2MMNQZY)": "rzx12WDuSBL47AT4txk5pp2MMNQZY",
		"settlement inv_async":                               "inv_async",
		"Admin grant by z@hanzo.ai: settlement inv_async":    "",
		"Minted by app_platform: Top-up via square (ref: x)": "",
		"commerce balance backfill":                          "",
	} {
		m := settlementNote.FindStringSubmatch(memo)
		got := ""
		if m != nil {
			got = m[1] + m[2]
		}
		if got != want {
			t.Errorf("settlementNote(%q) = %q, want %q", memo, got, want)
		}
	}
}

// The balance op reads the books this network writes. Off mainnet a deposit is
// sandboxed wherever it is made, so the read must be too, or a testnet buyer is told
// they hold nothing; on mainnet sandbox money stays out of the spendable balance.
func TestPlaneBalanceReadsTheNetworksBooks(t *testing.T) {
	for _, tc := range []struct {
		net  network.Network
		want int64
	}{
		{network.Testnet, 500},
		{network.Devnet, 500},
		{network.Mainnet, 0},
	} {
		t.Run(string(tc.net), func(t *testing.T) {
			t.Setenv(network.NetworkEnv, string(tc.net))
			fin := finance.New(finance.Local(t.TempDir(), testMaster))
			finance.Publish(fin)
			t.Cleanup(func() { finance.Publish(nil) })
			ctx := context.Background()
			if _, err := fin.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "sq_sandbox", Cash: true, Test: true}); err != nil {
				t.Fatal(err)
			}
			got, err := planeBalance(cloud.For(ctx, "acme"), &client.BalanceIn{Currency: "usd"})
			if err != nil {
				t.Fatal(err)
			}
			if total, _ := got.Amount.FloorMinor(); total != tc.want {
				t.Fatalf("%s balance after a $5 sandbox deposit = %d cents, want %d", tc.net, total, tc.want)
			}
		})
	}
}
