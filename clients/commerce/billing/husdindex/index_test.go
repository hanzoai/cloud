package husdindex

import (
	"context"
	"math/big"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
)

// --- fakes ---

type fakeReader struct {
	head      uint64
	transfers []Transfer // full set; TransfersTo filters by [from,to] and watched addr
}

func (r *fakeReader) BlockNumber(context.Context) (uint64, error) { return r.head, nil }
func (r *fakeReader) TransfersTo(_ context.Context, addrs []string, from, to uint64) ([]Transfer, error) {
	watch := map[string]bool{}
	for _, a := range addrs {
		watch[a] = true
	}
	var out []Transfer
	for _, t := range r.transfers {
		if t.Block >= from && t.Block <= to && watch[t.To] {
			out = append(out, t)
		}
	}
	return out, nil
}

type fakeLedger struct{ credits map[string]Credit }

func newFakeLedger() *fakeLedger { return &fakeLedger{credits: map[string]Credit{}} }
func (l *fakeLedger) Credit(_ context.Context, c Credit) error {
	l.credits[c.DedupKey] = c // idempotent on DedupKey
	return nil
}
func (l *fakeLedger) totalFor(org string) int64 {
	var sum int64
	for _, c := range l.credits {
		if c.OrgID == org {
			sum += c.AmountCents
		}
	}
	return sum
}

type fakeLookup map[string]*treasury.Issuance

func (m fakeLookup) ByTxHash(_ context.Context, tx string) (*treasury.Issuance, error) {
	return m[tx], nil
}

type fakeCursor struct{ last uint64 }

func (c *fakeCursor) Last(context.Context) (uint64, error)   { return c.last, nil }
func (c *fakeCursor) Save(_ context.Context, b uint64) error { c.last = b; return nil }

type fakeBook map[string]string // addr(lower) -> org

func (b fakeBook) Addresses(context.Context) ([]string, error) {
	out := make([]string, 0, len(b))
	for a := range b {
		out = append(out, a)
	}
	return out, nil
}
func (b fakeBook) OrgFor(addr string) (string, bool) { o, ok := b[addr]; return o, ok }

// --- helpers ---

func wei(t *testing.T, cents int64) *big.Int {
	w, err := husd.CentsToWei(cents, 18)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

const hanzoAddr = "0xe31e41e468893c44a4011d80b3315f1c362ba565"
const acmeAddr = "0x1234567890123456789012345678901234567890"

func TestSync_ProjectsTaggedCredits(t *testing.T) {
	ctx := context.Background()
	reader := &fakeReader{
		head: 100,
		transfers: []Transfer{
			// A treasury MINT to hanzo (credit bucket) — block 10.
			{From: "0xtreasury", To: hanzoAddr, ValueWei: wei(t, 2550), TxHash: "0xmint", LogIndex: 0, Block: 10},
			// An EXTERNAL transfer to acme (no issuance → prepaid) — block 20.
			{From: "0xsomeone", To: acmeAddr, ValueWei: wei(t, 1000), TxHash: "0xext", LogIndex: 1, Block: 20},
			// Beyond safeHead (98 > 95) — must NOT be scanned this run.
			{From: "0xtreasury", To: hanzoAddr, ValueWei: wei(t, 999), TxHash: "0xfuture", LogIndex: 0, Block: 98},
		},
	}
	ledger := newFakeLedger()
	lookup := fakeLookup{"0xmint": &treasury.Issuance{OrgID: "hanzo", Bucket: treasury.BucketCredit, AmountCents: 2550}}
	cursor := &fakeCursor{}
	book := fakeBook{hanzoAddr: "hanzo", acmeAddr: "acme"}

	ix := NewIndexer(reader, ledger, lookup, cursor, book, Config{Decimals: 18, Confirmations: 5})
	n, err := ix.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("projected %d, want 2 (future tx excluded by confirmations)", n)
	}
	// hanzo mint → credit:husd tag, 2550c.
	mint := ledger.credits["0xmint:0"]
	if mint.OrgID != "hanzo" || mint.AmountCents != 2550 || mint.Tag != "credit:husd" {
		t.Fatalf("mint credit wrong: %+v", mint)
	}
	// acme external → husd (prepaid) tag, 1000c.
	ext := ledger.credits["0xext:1"]
	if ext.OrgID != "acme" || ext.AmountCents != 1000 || ext.Tag != "husd" {
		t.Fatalf("external credit wrong: %+v", ext)
	}
	if cursor.last != 95 {
		t.Fatalf("cursor=%d, want 95 (head 100 - 5 confirmations)", cursor.last)
	}
}

func TestSync_Idempotent_NoDoubleCredit(t *testing.T) {
	ctx := context.Background()
	reader := &fakeReader{
		head: 50,
		transfers: []Transfer{
			{From: "0xt", To: hanzoAddr, ValueWei: wei(t, 500), TxHash: "0xa", LogIndex: 0, Block: 10},
		},
	}
	ledger := newFakeLedger()
	book := fakeBook{hanzoAddr: "hanzo"}
	lookup := fakeLookup{"0xa": &treasury.Issuance{OrgID: "hanzo", Bucket: treasury.BucketPrepaid}}

	ix := NewIndexer(reader, ledger, lookup, &fakeCursor{}, book, Config{Decimals: 18, Confirmations: 0})
	if _, err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	// Re-scan from scratch (fresh cursor) — the same transfer projects again but
	// the ledger dedups on DedupKey.
	ix2 := NewIndexer(reader, ledger, lookup, &fakeCursor{}, book, Config{Decimals: 18, Confirmations: 0})
	if _, err := ix2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledger.totalFor("hanzo"); got != 500 {
		t.Fatalf("double credit: hanzo total=%d, want 500", got)
	}
	if len(ledger.credits) != 1 {
		t.Fatalf("want 1 unique credit, got %d", len(ledger.credits))
	}
}

func TestSync_ReconcilesToOnChainBalance(t *testing.T) {
	// The projected indexed balance for an org must equal what balanceOf would
	// report on chain — exact to the cent.
	ctx := context.Background()
	transfers := []Transfer{
		{From: "0xt", To: hanzoAddr, ValueWei: wei(t, 2550), TxHash: "0x1", LogIndex: 0, Block: 5},
		{From: "0xt", To: hanzoAddr, ValueWei: wei(t, 100), TxHash: "0x2", LogIndex: 0, Block: 6},
		{From: "0xt", To: hanzoAddr, ValueWei: wei(t, 1), TxHash: "0x3", LogIndex: 0, Block: 7},
	}
	reader := &fakeReader{head: 20, transfers: transfers}
	ledger := newFakeLedger()
	ix := NewIndexer(reader, ledger, fakeLookup{}, &fakeCursor{}, fakeBook{hanzoAddr: "hanzo"}, Config{Decimals: 18, Confirmations: 0})
	if _, err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	indexed := ledger.totalFor("hanzo")

	// On-chain balance = sum of the same transfers (fake balance reader).
	var total *big.Int = big.NewInt(0)
	for _, tr := range transfers {
		total.Add(total, tr.ValueWei)
	}
	onchain, err := OnChainBalanceCents(ctx, fakeBalance{hanzoAddr: total}, hanzoAddr, 18)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != onchain {
		t.Fatalf("RECONCILE MISMATCH: indexed=%d, on-chain=%d", indexed, onchain)
	}
	if indexed != 2651 {
		t.Fatalf("indexed=%d, want 2651", indexed)
	}
}

type fakeBalance map[string]*big.Int

func (b fakeBalance) BalanceOf(_ context.Context, addr string) (*big.Int, error) {
	if v, ok := b[addr]; ok {
		return v, nil
	}
	return big.NewInt(0), nil
}

func TestSync_EmptyBookAdvancesCursor(t *testing.T) {
	reader := &fakeReader{head: 10}
	cursor := &fakeCursor{}
	ix := NewIndexer(reader, newFakeLedger(), fakeLookup{}, cursor, fakeBook{}, Config{Confirmations: 0})
	if _, err := ix.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cursor.last != 10 {
		t.Fatalf("cursor=%d, want 10 (advanced past empty range)", cursor.last)
	}
}

// fakeTxReader extends fakeReader with single-tx (receipt) reads, so ProjectTx —
// the synchronous just-minted projection — can be exercised without a chain.
type fakeTxReader struct {
	*fakeReader
	byTx map[string][]Transfer // txHash -> its Transfer logs
}

func (r *fakeTxReader) TransfersInTx(_ context.Context, txHash string, addrs []string) ([]Transfer, error) {
	watch := map[string]bool{}
	for _, a := range addrs {
		watch[a] = true
	}
	var out []Transfer
	for _, t := range r.byTx[txHash] {
		if watch[t.To] {
			out = append(out, t)
		}
	}
	return out, nil
}

// ProjectTx must credit the subject + bucket + test partition from the issuance,
// and must be idempotent WITH a later Sync over the same block (one dedup key).
func TestProjectTx_SubjectTest_AndIdempotentWithSync(t *testing.T) {
	ctx := context.Background()
	mint := Transfer{From: "0xtreasury", To: hanzoAddr, ValueWei: wei(t, 2550), TxHash: "0xmint", LogIndex: 0, Block: 10}
	base := &fakeReader{head: 100, transfers: []Transfer{mint}}
	reader := &fakeTxReader{fakeReader: base, byTx: map[string][]Transfer{"0xmint": {mint}}}
	ledger := newFakeLedger()
	// Issuance: org hanzo, sub-user "hanzo/alice", credit bucket, test-mode.
	lookup := fakeLookup{"0xmint": &treasury.Issuance{
		OrgID: "hanzo", Subject: "hanzo/alice", Bucket: treasury.BucketCredit, AmountCents: 2550, Test: true,
	}}
	book := fakeBook{hanzoAddr: "hanzo"}
	ix := NewIndexer(reader, ledger, lookup, &fakeCursor{}, book, Config{Decimals: 18, Confirmations: 5})

	// Synchronous projection right after the mint.
	n, err := ix.ProjectTx(ctx, "0xmint")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ProjectTx projected %d, want 1", n)
	}
	c := ledger.credits["0xmint:0"]
	if c.OrgID != "hanzo" || c.Subject != "hanzo/alice" || c.AmountCents != 2550 || c.Tag != "credit:husd" || !c.Test {
		t.Fatalf("projected credit wrong: %+v", c)
	}

	// The background Sync later scans the same block — must NOT double-credit.
	if _, err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ledger.totalFor("hanzo"); got != 2550 {
		t.Fatalf("double credit after Sync: hanzo total=%d, want 2550", got)
	}
	if len(ledger.credits) != 1 {
		t.Fatalf("want 1 unique credit, got %d", len(ledger.credits))
	}
}

// A transfer with no issuance (external payin) → prepaid, credited to the org
// slug subject, test=false (fail-closed: unknown money is real prepaid money).
func TestProject_ExternalPayin_PrepaidToOrgSlug(t *testing.T) {
	ctx := context.Background()
	reader := &fakeReader{head: 50, transfers: []Transfer{
		{From: "0xexchange", To: acmeAddr, ValueWei: wei(t, 1000), TxHash: "0xpayin", LogIndex: 3, Block: 10},
	}}
	ledger := newFakeLedger()
	ix := NewIndexer(reader, ledger, fakeLookup{}, &fakeCursor{}, fakeBook{acmeAddr: "acme"}, Config{Decimals: 18, Confirmations: 0})
	if _, err := ix.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	c := ledger.credits["0xpayin:3"]
	if c.OrgID != "acme" || c.Subject != "acme" || c.Tag != "husd" || c.Test {
		t.Fatalf("external payin credit wrong: %+v", c)
	}
}
