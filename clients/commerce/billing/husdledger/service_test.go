package husdledger

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/bucket"
	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/treasury/datastorestore"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// This is a CGO/sqlite integration test (real datastore); it runs in CI. It
// proves the PRODUCTION projection: an on-chain HUSD Transfer, resolved to its
// off-chain issuance, is written by the real ledgerStore as an idempotent,
// bucket-tagged Deposit that GET /v1/billing/*/balance (via txutil + bucket)
// reads back — chain-sourced balance, exact to the cent.

func nsDB(parent context.Context, ns string) *datastore.Datastore {
	return datastore.New(nscontext.WithNamespace(parent, ns))
}

var testSeed = []byte("hanzo-husd-org-derivation-test-seed-0001")

var testCfg = husd.Config{
	ChainID: 36962, RPCURL: "http://rpc", TokenAddress: "0xToken",
	TreasuryKey: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", Decimals: 18,
}

// --- fakes: a chain reader that serves ONE minted tx's Transfer log ---

type fakeReader struct {
	head  uint64
	byTx  map[string][]husdindex.Transfer
	all   []husdindex.Transfer
}

func (r *fakeReader) BlockNumber(context.Context) (uint64, error) { return r.head, nil }
func (r *fakeReader) TransfersTo(_ context.Context, addrs []string, from, to uint64) ([]husdindex.Transfer, error) {
	watch := map[string]bool{}
	for _, a := range addrs {
		watch[a] = true
	}
	var out []husdindex.Transfer
	for _, t := range r.all {
		if t.Block >= from && t.Block <= to && watch[t.To] {
			out = append(out, t)
		}
	}
	return out, nil
}
func (r *fakeReader) TransfersInTx(_ context.Context, txHash string, addrs []string) ([]husdindex.Transfer, error) {
	watch := map[string]bool{}
	for _, a := range addrs {
		watch[a] = true
	}
	var out []husdindex.Transfer
	for _, t := range r.byTx[txHash] {
		if watch[t.To] {
			out = append(out, t)
		}
	}
	return out, nil
}

type fakeBook map[string]string // lower(addr) -> org

func (b fakeBook) Addresses(context.Context) ([]string, error) {
	out := make([]string, 0, len(b))
	for a := range b {
		out = append(out, a)
	}
	return out, nil
}
func (b fakeBook) OrgFor(addr string) (string, bool) { o, ok := b[addr]; return o, ok }

func weiFor(t *testing.T, cents int64) *big.Int {
	w, err := husd.CentsToWei(cents, 18)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestProjectTx_WritesReadableLedgerCredit(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	const org = "acme"
	const subject = "acme/alice"
	const cents = int64(2550)
	orgAddr, err := treasury.AddressForOrg(testSeed, org)
	if err != nil {
		t.Fatal(err)
	}

	// 1) Mint through the REAL system-namespace issuance store (records the
	//    off-chain bucket + subject the projection needs), fake transfer → 0xmint.
	issStore := datastorestore.New(nsDB(c, systemNamespace))
	tr := treasury.New(testCfg, testSeed, issStore, treasury.WithTransfer(
		func(context.Context, blockchain.TokenTransfer) (string, error) { return "0xmint", nil }))
	rc, err := tr.Mint(context.Background(), treasury.MintRequest{
		OrgID: org, Subject: subject, AmountCents: cents, Bucket: treasury.BucketCredit,
		Reason: "welcome", Test: true, IdemKey: "welcome:acme:alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rc.TxHash != "0xmint" {
		t.Fatalf("mint tx=%s, want 0xmint", rc.TxHash)
	}

	// 2) Build the indexer with the REAL ledgerStore + REAL issuance lookup and a
	//    fake chain reader that serves the mint's Transfer log.
	mint := husdindex.Transfer{From: "0xtreasury", To: orgAddr, ValueWei: weiFor(t, cents), TxHash: "0xmint", LogIndex: 0, Block: 10}
	reader := &fakeReader{head: 100, byTx: map[string][]husdindex.Transfer{"0xmint": {mint}}, all: []husdindex.Transfer{mint}}
	ix := husdindex.NewIndexer(reader, ledgerStore{}, issStore, &cursorStore{chainID: testCfg.ChainID}, fakeBook{orgAddr: org},
		husdindex.Config{Decimals: 18, Confirmations: 1})

	// 3) Synchronous projection of the minted tx.
	n, err := ix.ProjectTx(context.Background(), "0xmint")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ProjectTx projected %d, want 1", n)
	}

	// 4) The balance read (chain-sourced) reflects the credit for the SUBJECT, in
	//    the Test partition, classified into the Credit bucket.
	raw, err := util.GetRawByCurrency(nsDB(c, org).Context, subject, "iam-user", "usd", true)
	if err != nil {
		t.Fatal(err)
	}
	split := bucket.Compute(raw, subject, time.Now())
	if int64(split.CreditsRemaining) != cents || int64(split.Balance) != cents {
		t.Fatalf("balance split wrong: creditsRemaining=%d balance=%d, want %d", split.CreditsRemaining, split.Balance, cents)
	}

	// 5) Idempotent: a background Sync over the same block must NOT double-credit.
	if _, err := ix.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw2, _ := util.GetRawByCurrency(nsDB(c, org).Context, subject, "iam-user", "usd", true)
	split2 := bucket.Compute(raw2, subject, time.Now())
	if int64(split2.Balance) != cents {
		t.Fatalf("double credit after Sync: balance=%d, want %d", split2.Balance, cents)
	}
}
