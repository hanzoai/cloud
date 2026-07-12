package treasury

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
)

// ---- fakes (CGO-free: no datastore, no chain) ----

type fakeStore struct {
	mu      sync.Mutex
	m       map[string]*Issuance
	creates int
}

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]*Issuance{}} }

func (f *fakeStore) CreateIfAbsent(_ context.Context, iss *Issuance) (bool, *Issuance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ex, ok := f.m[iss.Id]; ok {
		cp := *ex
		return false, &cp, nil
	}
	cp := *iss
	f.m[iss.Id] = &cp
	f.creates++
	return true, iss, nil
}

func (f *fakeStore) Update(_ context.Context, iss *Issuance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *iss
	f.m[iss.Id] = &cp
	return nil
}

func (f *fakeStore) Get(_ context.Context, id string) (*Issuance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ex, ok := f.m[id]; ok {
		cp := *ex
		return &cp, nil
	}
	return nil, nil
}

type fakeTransfer struct {
	mu    sync.Mutex
	calls []blockchain.TokenTransfer
	hash  string
	err   error
	delay time.Duration
}

func (f *fakeTransfer) fn(_ context.Context, t blockchain.TokenTransfer) (string, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.calls = append(f.calls, t)
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if f.hash != "" {
		return f.hash, nil
	}
	return "0xhash", nil
}

func (f *fakeTransfer) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

var testCfg = husd.Config{
	ChainID:      36962,
	RPCURL:       "http://localhost:19630/ext/bc/C/rpc",
	TokenAddress: "0xc57b7eCE2Ce2E74ef3Bc08Cfd5f5Fb41B6Ad4D66",
	TreasuryKey:  "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
	Decimals:     18,
}

// authorized ctx (a service-token / SuperAdmin request would carry this).
func authCtx() context.Context {
	return mintauth.WithAuthorized(mintauth.WithGate(context.Background()))
}

func TestMint_Success(t *testing.T) {
	store := newFakeStore()
	xfer := &fakeTransfer{hash: "0xdeadbeef"}
	tr := New(testCfg, testSeed, store, WithTransfer(xfer.fn))

	rc, err := tr.Mint(authCtx(), MintRequest{
		OrgID: "hanzo", AmountCents: 2550, Bucket: BucketPrepaid, Reason: "topup:test", IdemKey: "k1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rc.TxHash != "0xdeadbeef" || rc.Replayed {
		t.Fatalf("bad receipt %+v", rc)
	}
	// The mint must target the org's DERIVED address, for the DERIVED amount.
	wantAddr, _ := AddressForOrg(testSeed, "hanzo")
	wantWei, _ := husd.CentsToWei(2550, 18)
	if xfer.count() != 1 {
		t.Fatalf("want 1 transfer, got %d", xfer.count())
	}
	call := xfer.calls[0]
	if call.To != wantAddr {
		t.Errorf("mint To=%s, want derived %s", call.To, wantAddr)
	}
	if call.AmountWei.Cmp(wantWei) != 0 {
		t.Errorf("mint amountWei=%s, want %s", call.AmountWei, wantWei)
	}
	if call.TreasuryKey != testCfg.TreasuryKey || call.TokenAddress != testCfg.TokenAddress {
		t.Error("mint did not use configured treasury key / token")
	}
	// Issuance recorded as minted with the tx.
	iss, _ := store.Get(context.Background(), IssuanceID("k1"))
	if iss == nil || iss.Status != StatusMinted || iss.TxHash != "0xdeadbeef" {
		t.Fatalf("issuance not recorded minted: %+v", iss)
	}
}

func TestMint_IdempotentReplay(t *testing.T) {
	store := newFakeStore()
	xfer := &fakeTransfer{hash: "0xabc"}
	tr := New(testCfg, testSeed, store, WithTransfer(xfer.fn))

	req := MintRequest{OrgID: "acme", AmountCents: 100, Bucket: BucketCredit, Reason: "welcome", IdemKey: "same"}
	r1, err := tr.Mint(authCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := tr.Mint(authCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if xfer.count() != 1 {
		t.Fatalf("replay minted again: %d transfers", xfer.count())
	}
	if !r2.Replayed || r2.TxHash != r1.TxHash {
		t.Fatalf("replay receipt wrong: %+v", r2)
	}
	if store.creates != 1 {
		t.Fatalf("want 1 issuance row, got %d", store.creates)
	}
}

func TestMint_ConcurrentSameKey_ExactlyOneOnChain(t *testing.T) {
	store := newFakeStore()
	xfer := &fakeTransfer{hash: "0x1", delay: 20 * time.Millisecond}
	tr := New(testCfg, testSeed, store, WithTransfer(xfer.fn))

	const n = 25
	var wg sync.WaitGroup
	var mu sync.Mutex
	var minted, replayed, inflight int
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, err := tr.Mint(authCtx(), MintRequest{
				OrgID: "raceorg", AmountCents: 500, Bucket: BucketPrepaid, Reason: "topup", IdemKey: "racekey",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrInFlight):
				inflight++
			case err != nil:
				t.Errorf("unexpected err: %v", err)
			case rc.Replayed:
				replayed++
			default:
				minted++
			}
		}()
	}
	wg.Wait()

	if xfer.count() != 1 {
		t.Fatalf("EXACTLY ONE on-chain mint required, got %d", xfer.count())
	}
	if store.creates != 1 {
		t.Fatalf("want 1 issuance row, got %d", store.creates)
	}
	if minted != 1 {
		t.Fatalf("want exactly 1 fresh mint, got minted=%d replayed=%d inflight=%d", minted, replayed, inflight)
	}
	if minted+replayed+inflight != n {
		t.Fatalf("accounting lost calls: %d+%d+%d != %d", minted, replayed, inflight, n)
	}
}

func TestMint_RequiresAuthority(t *testing.T) {
	store := newFakeStore()
	xfer := &fakeTransfer{}
	tr := New(testCfg, testSeed, store, WithTransfer(xfer.fn))

	// Gated (inbound HTTP) but NOT authorized → refused, no chain call.
	gated := mintauth.WithGate(context.Background())
	if _, err := tr.Mint(gated, MintRequest{OrgID: "x", AmountCents: 100, Bucket: BucketCredit, IdemKey: "k"}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("gated+unauthorized: want ErrNotAuthorized, got %v", err)
	}
	if xfer.count() != 0 {
		t.Fatal("unauthorized mint reached the chain")
	}
	if store.creates != 0 {
		t.Fatal("unauthorized mint wrote an issuance")
	}

	// Ungated (cron/migration) is allowed.
	if _, err := tr.Mint(context.Background(), MintRequest{OrgID: "x", AmountCents: 100, Bucket: BucketCredit, IdemKey: "k2"}); err != nil {
		t.Fatalf("ungated mint should be allowed: %v", err)
	}
	// Gated + authorized is allowed.
	if _, err := tr.Mint(authCtx(), MintRequest{OrgID: "x", AmountCents: 100, Bucket: BucketCredit, IdemKey: "k3"}); err != nil {
		t.Fatalf("authorized mint should be allowed: %v", err)
	}
}

func TestMint_FailClosedUnconfigured(t *testing.T) {
	store := newFakeStore()
	xfer := &fakeTransfer{}
	// No token/key.
	tr := New(husd.Config{ChainID: 36962, Decimals: 18}, testSeed, store, WithTransfer(xfer.fn))
	_, err := tr.Mint(authCtx(), MintRequest{OrgID: "x", AmountCents: 100, Bucket: BucketCredit, IdemKey: "k"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
	if xfer.count() != 0 || store.creates != 0 {
		t.Fatal("unconfigured mint had side effects")
	}
}

func TestMint_Validation(t *testing.T) {
	tr := New(testCfg, testSeed, newFakeStore(), WithTransfer((&fakeTransfer{}).fn))
	cases := []MintRequest{
		{OrgID: "", AmountCents: 100, Bucket: BucketCredit, IdemKey: "k"},
		{OrgID: "x", AmountCents: 0, Bucket: BucketCredit, IdemKey: "k"},
		{OrgID: "x", AmountCents: -5, Bucket: BucketCredit, IdemKey: "k"},
		{OrgID: "x", AmountCents: 100, Bucket: "bogus", IdemKey: "k"},
		{OrgID: "x", AmountCents: 100, Bucket: BucketCredit, IdemKey: "  "},
	}
	for i, c := range cases {
		if _, err := tr.Mint(authCtx(), c); err == nil {
			t.Errorf("case %d (%+v): want validation error", i, c)
		}
	}
}

func TestMint_FailedRetryReclaims(t *testing.T) {
	store := newFakeStore()
	xfer := &fakeTransfer{err: errors.New("rpc down")}
	tr := New(testCfg, testSeed, store, WithTransfer(xfer.fn))

	req := MintRequest{OrgID: "x", AmountCents: 100, Bucket: BucketPrepaid, Reason: "topup", IdemKey: "retry"}
	if _, err := tr.Mint(authCtx(), req); err == nil {
		t.Fatal("want transfer error")
	}
	iss, _ := store.Get(context.Background(), IssuanceID("retry"))
	if iss.Status != StatusFailed {
		t.Fatalf("failed submit must mark issuance failed, got %q", iss.Status)
	}
	// Now the chain recovers; the SAME key retries and succeeds (reclaims the row).
	xfer.err = nil
	xfer.hash = "0xrecovered"
	rc, err := tr.Mint(authCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if rc.TxHash != "0xrecovered" || rc.Replayed {
		t.Fatalf("retry receipt wrong: %+v", rc)
	}
	if store.creates != 1 {
		t.Fatalf("retry created a second row: %d", store.creates)
	}
}

func TestBucketLedgerTag(t *testing.T) {
	// These tags MUST classify back to the same bucket via billing/bucket.DepositKind:
	//   "credit:husd" -> Credit (grant), "husd" -> Prepaid (real money).
	if BucketCredit.LedgerTag() != "credit:husd" {
		t.Errorf("credit tag=%q", BucketCredit.LedgerTag())
	}
	if BucketPrepaid.LedgerTag() != "husd" {
		t.Errorf("prepaid tag=%q", BucketPrepaid.LedgerTag())
	}
	if !BucketCredit.Valid() || !BucketPrepaid.Valid() || Bucket("x").Valid() {
		t.Error("bucket validity wrong")
	}
}

func TestIssuanceID_Deterministic(t *testing.T) {
	if IssuanceID("a") != IssuanceID("a") {
		t.Fatal("non-deterministic id")
	}
	if IssuanceID("a") == IssuanceID("b") {
		t.Fatal("distinct keys collided")
	}
}

func TestMint_SubjectDefaultsAndTestPropagate(t *testing.T) {
	// Empty subject → defaults to OrgID (org-pooled billing).
	store := newFakeStore()
	tr := New(testCfg, testSeed, store, WithTransfer((&fakeTransfer{hash: "0xh"}).fn))
	rc, err := tr.Mint(authCtx(), MintRequest{OrgID: "acme", AmountCents: 100, Bucket: BucketCredit, IdemKey: "pooled", Test: true})
	if err != nil {
		t.Fatal(err)
	}
	if rc.Subject != "acme" || !rc.Test {
		t.Fatalf("pooled: subject=%q test=%v, want acme/true", rc.Subject, rc.Test)
	}
	iss, _ := store.Get(context.Background(), IssuanceID("pooled"))
	if iss.Subject != "acme" || !iss.Test {
		t.Fatalf("pooled issuance subject=%q test=%v", iss.Subject, iss.Test)
	}

	// Explicit per-user subject is preserved; live (test=false) partition.
	rc2, err := tr.Mint(authCtx(), MintRequest{OrgID: "acme", Subject: "acme/alice", AmountCents: 100, Bucket: BucketPrepaid, IdemKey: "peruser", Test: false})
	if err != nil {
		t.Fatal(err)
	}
	if rc2.Subject != "acme/alice" || rc2.Test {
		t.Fatalf("per-user: subject=%q test=%v, want acme/alice/false", rc2.Subject, rc2.Test)
	}
	// The on-chain address is still per-ORG (both mints target acme's address).
	acmeAddr, _ := AddressForOrg(testSeed, "acme")
	if rc.OrgAddress != acmeAddr || rc2.OrgAddress != acmeAddr {
		t.Fatalf("subjects must share the org address: %s vs %s vs %s", rc.OrgAddress, rc2.OrgAddress, acmeAddr)
	}
}

func TestAddressForKey_RoundTrip(t *testing.T) {
	// A derived account's private key must recover its own address.
	acct, err := DeriveAccount(testSeed, "roundtrip-org")
	if err != nil {
		t.Fatal(err)
	}
	got, err := AddressForKey(acct.PrivateKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	if got != acct.Address {
		t.Fatalf("AddressForKey=%s, want %s", got, acct.Address)
	}
}
