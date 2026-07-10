package husdledger

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/bucket"
	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	txutil "github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// fakeChain simulates the HUSD chain for the migration proof: the treasury mint
// credits the org address AND records a Transfer the projector reads back, so
// MigrateOrg's snapshot→mint→project→offset→reconcile runs end to end over a REAL
// sqlite ledger with NO chain.
type fakeChain struct {
	n        int
	byTx     map[string]husdindex.Transfer
	balances map[string]*big.Int
}

func newFakeChain() *fakeChain {
	return &fakeChain{byTx: map[string]husdindex.Transfer{}, balances: map[string]*big.Int{}}
}
func (f *fakeChain) mint(_ context.Context, t blockchain.TokenTransfer) (string, error) {
	f.n++
	h := fmt.Sprintf("0xmig%02d", f.n)
	f.byTx[h] = husdindex.Transfer{From: "0xtreasury", To: t.To, ValueWei: t.AmountWei, TxHash: h, LogIndex: 0, Block: uint64(f.n)}
	if f.balances[t.To] == nil {
		f.balances[t.To] = big.NewInt(0)
	}
	f.balances[t.To] = new(big.Int).Add(f.balances[t.To], t.AmountWei)
	return h, nil
}
func (f *fakeChain) BlockNumber(context.Context) (uint64, error) { return uint64(f.n + 100), nil }
func (f *fakeChain) TransfersTo(_ context.Context, addrs []string, from, to uint64) ([]husdindex.Transfer, error) {
	watch := map[string]bool{}
	for _, a := range addrs {
		watch[a] = true
	}
	var out []husdindex.Transfer
	for _, tr := range f.byTx {
		if tr.Block >= from && tr.Block <= to && watch[tr.To] {
			out = append(out, tr)
		}
	}
	return out, nil
}
func (f *fakeChain) TransfersInTx(_ context.Context, txHash string, addrs []string) ([]husdindex.Transfer, error) {
	watch := map[string]bool{}
	for _, a := range addrs {
		watch[a] = true
	}
	if tr, ok := f.byTx[txHash]; ok && watch[tr.To] {
		return []husdindex.Transfer{tr}, nil
	}
	return nil, nil
}
func (f *fakeChain) BalanceOf(_ context.Context, addr string) (*big.Int, error) {
	if v, ok := f.balances[addr]; ok {
		return v, nil
	}
	return big.NewInt(0), nil
}

func seedLegacyDeposit(t *testing.T, c ae.Context, org, subject string, cents int64, tag string) {
	t.Helper()
	db := nsDB(c, org)
	d := transaction.New(db)
	d.Type = transaction.Deposit
	d.DestinationId = subject
	d.DestinationKind = transaction.IAMUserKind
	d.Currency = currency.Type("usd")
	d.Amount = currency.Cents(cents)
	d.Tags = tag
	d.Test = true
	d.Metadata = Map{"legacy": true}
	if err := d.Create(); err != nil {
		t.Fatal(err)
	}
}

func splitFor(c ae.Context, org, subject string) bucket.Split {
	raw, _ := txutil.GetRawByCurrency(nscontext.WithNamespace(context.Background(), org), subject, transaction.IAMUserKind, "usd", true)
	return bucket.Compute(raw, subject, time.Now())
}

func TestMigrateOrg_ZeroDrift_BucketsPreserved(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	const org = "acme"
	// The addressbook derives addresses from Organization records, so acme must
	// exist. Put (not Create) — the org Validator requires FullName; Put persists
	// the row directly, which is all the addressbook enumeration needs.
	o := organization.New(datastore.New(context.Background()))
	o.Name = org
	o.FullName = org
	if err := o.Put(); err != nil {
		t.Fatalf("create org: %v", err)
	}

	// Legacy DB ledger (pre-migration): alice = $30 credit + $20 prepaid − $5 usage;
	// bob = $10 prepaid. All in the test partition.
	seedLegacyDeposit(t, c, org, "acme/alice", 3000, "starter-credit") // credit
	seedLegacyDeposit(t, c, org, "acme/alice", 2000, "topup")          // prepaid
	seedLegacyDeposit(t, c, org, "acme/bob", 1000, "topup")            // prepaid
	// $5 non-GPU usage by alice draws credits-first → alice credit 2500, prepaid 2000.
	wdb := nsDB(c, org)
	w := transaction.New(wdb)
	w.Type = transaction.Withdraw
	w.SourceId = "acme/alice"
	w.SourceKind = transaction.IAMUserKind
	w.Currency = currency.Type("usd")
	w.Amount = 500
	w.Test = true
	if err := w.Create(); err != nil {
		t.Fatal(err)
	}

	preAlice := splitFor(c, org, "acme/alice")
	if int64(preAlice.CreditsRemaining) != 2500 || int64(preAlice.PrepaidBalance) != 2000 {
		t.Fatalf("pre alice split unexpected: %+v", preAlice)
	}
	// org DB total = alice(4500) + bob(1000) = 5500.

	f := newFakeChain()
	svc := New(testCfg, testSeed, WithMintTransfer(f.mint), WithIndexReader(f), WithBalanceReader(f))
	if !svc.Enabled() {
		t.Fatal("service disabled")
	}

	// Dry-run: snapshot only, mints nothing.
	dry, err := svc.MigrateOrg(context.Background(), org, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.DBBalanceCents != 5500 || dry.MintedCents != 0 || !dry.DryRun {
		t.Fatalf("dry run: %+v", dry)
	}

	// Execute.
	res, err := svc.MigrateOrg(context.Background(), org, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reconciled {
		t.Fatalf("NOT reconciled: %+v", res)
	}
	if res.DBBalanceCents != 5500 || res.ChainBalanceCents != 5500 || res.PostDBCents != 5500 {
		t.Fatalf("reconcile amounts wrong: %+v", res)
	}

	// Buckets preserved exactly: alice still 2500 credit / 2000 prepaid, bob 1000 prepaid.
	postAlice := splitFor(c, org, "acme/alice")
	if int64(postAlice.CreditsRemaining) != 2500 || int64(postAlice.PrepaidBalance) != 2000 || int64(postAlice.Balance) != 4500 {
		t.Fatalf("post alice split drifted: %+v (want 2500c/2000p)", postAlice)
	}
	postBob := splitFor(c, org, "acme/bob")
	if int64(postBob.PrepaidBalance) != 1000 || int64(postBob.CreditsRemaining) != 0 {
		t.Fatalf("post bob split drifted: %+v", postBob)
	}

	// Idempotent: a second execute replays mints + dedups offsets → zero drift, no change.
	res2, err := svc.MigrateOrg(context.Background(), org, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Reconciled || res2.ChainBalanceCents != 5500 {
		t.Fatalf("re-migration drifted: %+v", res2)
	}
	if int64(splitFor(c, org, "acme/alice").Balance) != 4500 {
		t.Fatal("re-migration changed alice balance")
	}
}

func TestOrgLedgerSpendable_And_DriftReconcile(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	const org = "beta"
	seedLegacyDeposit(t, c, org, "beta/u", 5000, "topup")
	wdb := nsDB(c, org)
	w := transaction.New(wdb)
	w.Type = transaction.Withdraw
	w.SourceId = "beta/u"
	w.SourceKind = transaction.IAMUserKind
	w.Currency = currency.Type("usd")
	w.Amount = 2000
	w.Test = true
	if err := w.Create(); err != nil {
		t.Fatal(err)
	}
	// spendable = 5000 − 2000 = 3000.
	got, err := orgLedgerSpendableCents(org, true)
	if err != nil || got != 3000 {
		t.Fatalf("orgLedgerSpendableCents=%d err=%v, want 3000", got, err)
	}
	// If the chain still shows the full $50 mint, drift = 2000 (settle).
	drift, settle := husd.SettlementDrift(5000, got, 1)
	if drift != 2000 || !settle {
		t.Fatalf("drift=%d settle=%v, want 2000/true", drift, settle)
	}
	_ = treasury.BucketPrepaid
}
