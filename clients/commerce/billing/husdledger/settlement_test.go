package husdledger

import (
	"context"
	"math/big"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// CGO/sqlite integration test (CI): proves the Step-5 settlement wiring end to
// end with a REAL org ledger and injected chain seams — off-chain metered usage
// drives the drift, and the sweep transfers exactly that drift org→treasury,
// signed by the org's derived key, reconciling the chain to the ledger.

type fakeBalanceReader map[string]*big.Int

func (b fakeBalanceReader) BalanceOf(_ context.Context, addr string) (*big.Int, error) {
	if v, ok := b[addr]; ok {
		return v, nil
	}
	return big.NewInt(0), nil
}

type capturedTransfer struct {
	calls []blockchain.TokenTransfer
	hash  string
}

func (c *capturedTransfer) fn(_ context.Context, t blockchain.TokenTransfer) (string, error) {
	c.calls = append(c.calls, t)
	if c.hash != "" {
		return c.hash, nil
	}
	return "0xsettle", nil
}

func TestSettleOrg_SweepsDrift(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	const org = "acme"
	const subject = "acme/alice"
	orgAddr, err := treasury.AddressForOrg(testSeed, org)
	if err != nil {
		t.Fatal(err)
	}
	treasuryAddr, err := treasury.AddressForKey(testCfg.TreasuryKey)
	if err != nil {
		t.Fatal(err)
	}

	// Ledger: a $50 mint credit (projected Deposit) minus $20 metered usage
	// (Withdraw) → $30 spendable. The chain still holds the full $50 mint.
	if err := (ledgerStore{}).Credit(context.Background(), husdindex.Credit{
		OrgID: org, Subject: subject, AmountCents: 5000, Tag: "credit:husd", Test: true,
		TxHash: "0xmint", LogIndex: 0, DedupKey: "0xmint:0",
	}); err != nil {
		t.Fatal(err)
	}
	db := nsDB(c, org)
	w := transaction.New(db)
	w.Type = transaction.Withdraw
	w.SourceId = subject
	w.SourceKind = "iam-user"
	w.Currency = "usd"
	w.Amount = 2000
	w.Test = true
	if err := w.Create(); err != nil {
		t.Fatal(err)
	}

	// spendable sums to $30 in the test partition.
	if got, err := orgLedgerSpendableCents(org, true); err != nil || got != 3000 {
		t.Fatalf("orgLedgerSpendableCents=%d err=%v, want 3000", got, err)
	}

	// Enabled service with faked chain seams: chain shows the full $50 mint.
	bal := fakeBalanceReader{orgAddr: weiFor(t, 5000)}
	xfer := &capturedTransfer{hash: "0xsweep"}
	svc := New(testCfg, testSeed, WithBalanceReader(bal), WithSettleTransfer(xfer.fn))
	if !svc.Enabled() {
		t.Fatal("service not enabled")
	}

	res, err := svc.SettleOrg(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	// drift = onchain($50) − spendable($30) = $20 → settle.
	if !res.Settled || res.DriftCents != 2000 || res.OnChainCents != 5000 || res.SpendableCents != 3000 {
		t.Fatalf("settle result wrong: %+v", res)
	}
	if len(xfer.calls) != 1 {
		t.Fatalf("want 1 transfer, got %d", len(xfer.calls))
	}
	call := xfer.calls[0]
	// Swept org→treasury, signed by the ORG's derived key, for exactly the drift.
	orgAcct, _ := treasury.DeriveAccount(testSeed, org)
	if call.To != treasuryAddr {
		t.Errorf("sweep To=%s, want treasury %s", call.To, treasuryAddr)
	}
	if call.TreasuryKey != orgAcct.PrivateKeyHex() {
		t.Error("sweep NOT signed by the org's derived key")
	}
	wantWei, _ := husd.CentsToWei(2000, 18)
	if call.AmountWei.Cmp(wantWei) != 0 {
		t.Errorf("sweep amountWei=%s, want %s", call.AmountWei, wantWei)
	}
	_ = orgAddr

	// Reconcile: after the sweep the chain WOULD hold $50−$20 = $30 == spendable.
	if res.OnChainCents-res.DriftCents != res.SpendableCents {
		t.Fatalf("post-sweep chain %d != spendable %d", res.OnChainCents-res.DriftCents, res.SpendableCents)
	}

	// Idempotent: a second pass over the now-settled chain finds zero drift.
	bal[orgAddr] = weiFor(t, 3000)
	res2, err := svc.SettleOrg(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Settled || res2.DriftCents != 0 {
		t.Fatalf("second pass settled again: %+v", res2)
	}
	if len(xfer.calls) != 1 {
		t.Fatalf("second pass sent another transfer: %d total", len(xfer.calls))
	}
}
