package datastorestore

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// This is a CGO/sqlite integration test (real datastore); it runs in CI where
// the sqlite driver + luxcpp libs link. It proves the production issuance store
// is idempotent on the deterministic id and that treasury.Mint round-trips
// through it with a real backend.

func nsDB(parent context.Context, ns string) *datastore.Datastore {
	return datastore.New(nscontext.WithNamespace(parent, ns))
}

var seed = []byte("hanzo-husd-org-derivation-test-seed-0001")

var cfg = husd.Config{
	ChainID: 36962, RPCURL: "http://rpc", TokenAddress: "0xToken",
	TreasuryKey: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", Decimals: 18,
}

func authCtx() context.Context { return context.Background() } // ungated → allowed

func TestStore_CreateIfAbsent_Idempotent(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")
	s := New(db)

	iss := &treasury.Issuance{
		Id: treasury.IssuanceID("k1"), IdemKey: "k1", OrgID: "acme", OrgAddress: "0xabc",
		AmountCents: 2550, Bucket: treasury.BucketPrepaid, Reason: "topup", ChainID: 36962,
		TokenAddr: "0xToken", Status: treasury.StatusPending,
	}

	created, existing, err := s.CreateIfAbsent(context.Background(), iss)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	// Second call, same id: must NOT create, must return the existing row.
	created2, existing2, err := s.CreateIfAbsent(context.Background(), &treasury.Issuance{
		Id: treasury.IssuanceID("k1"), IdemKey: "k1", OrgID: "acme", Status: treasury.StatusPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("second create with same id reported created=true")
	}
	if existing2 == nil || existing2.AmountCents != 2550 || existing2.Bucket != treasury.BucketPrepaid {
		t.Fatalf("existing row not returned faithfully: %+v", existing2)
	}
	_ = existing

	// Update transitions status in place; Get reflects it.
	iss.Status = treasury.StatusMinted
	iss.TxHash = "0xtx"
	if err := s.Update(context.Background(), iss); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), treasury.IssuanceID("k1"))
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Status != treasury.StatusMinted || got.TxHash != "0xtx" {
		t.Fatalf("update not persisted: %+v", got)
	}
}

// ByTxHash resolves a minted tx back to its issuance (the indexer's lookup), and
// returns nil for a tx no issuance minted (an external payin).
func TestStore_ByTxHash(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	s := New(nsDB(c, "system"))

	iss := &treasury.Issuance{
		Id: treasury.IssuanceID("k1"), IdemKey: "k1", OrgID: "acme", Subject: "acme/alice",
		AmountCents: 2550, Bucket: treasury.BucketCredit, Test: true, ChainID: 36962,
		TokenAddr: "0xToken", Status: treasury.StatusPending,
	}
	if _, _, err := s.CreateIfAbsent(context.Background(), iss); err != nil {
		t.Fatal(err)
	}
	iss.Status = treasury.StatusMinted
	iss.TxHash = "0xminthash" // treasury.Mint stores lowercased; the store keeps that contract
	if err := s.Update(context.Background(), iss); err != nil {
		t.Fatal(err)
	}

	// Lookup is case-insensitive on the query (matches the lowercased stored form).
	got, err := s.ByTxHash(context.Background(), "0xMintHash")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Subject != "acme/alice" || got.Bucket != treasury.BucketCredit || !got.Test {
		t.Fatalf("ByTxHash wrong: %+v", got)
	}

	miss, err := s.ByTxHash(context.Background(), "0xother")
	if err != nil {
		t.Fatal(err)
	}
	if miss != nil {
		t.Fatalf("ByTxHash for unknown tx returned %+v, want nil", miss)
	}
}

func TestStore_Get_Absent(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	s := New(nsDB(c, "acme"))
	got, err := s.Get(context.Background(), treasury.IssuanceID("nope"))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("absent id returned %+v", got)
	}
}

// treasury.Mint through the REAL store: two mints with the same key → one row,
// one on-chain transfer, second is a replay.
func TestMint_ThroughDatastore_Idempotent(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	var mu sync.Mutex
	var transfers int
	xfer := func(_ context.Context, _ blockchain.TokenTransfer) (string, error) {
		mu.Lock()
		transfers++
		mu.Unlock()
		return "0xhash1", nil
	}
	tr := treasury.New(cfg, seed, New(db), treasury.WithTransfer(xfer))

	req := treasury.MintRequest{OrgID: "acme", AmountCents: 500, Bucket: treasury.BucketCredit, Reason: "welcome", IdemKey: "welcome:acme"}
	r1, err := tr.Mint(authCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := tr.Mint(authCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if transfers != 1 {
		t.Fatalf("want 1 on-chain transfer, got %d", transfers)
	}
	if !r2.Replayed || r2.TxHash != r1.TxHash {
		t.Fatalf("replay wrong: %+v", r2)
	}
}
