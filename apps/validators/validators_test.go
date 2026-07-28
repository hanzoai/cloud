package validators

import (
	"context"
	"encoding/hex"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/crypto"
	"github.com/luxfi/geth/common"
)

// TestMain sets a throwaway KMS master key so cek opens the store encrypted on an
// encryption-capable build (resolved once per process; env-independent order).
func TestMain(m *testing.M) {
	_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	os.Exit(m.Run())
}

// ── the ownership-verify test (the required one) ─────────────────────────────

// TestRecoverSigner_OwnershipProof proves the wallet-control half of the
// entitlement: a caller signs the EXACT challenge message (EIP-191 personal_sign
// shape), and the server recovers the SAME address that signed it. This is the
// mechanism that binds a claim to a real wallet — the wallet whose address must
// then own the on-chain GenesisNFT.
func TestRecoverSigner_OwnershipProof(t *testing.T) {
	// A deterministic test wallet (fixed key → fixed address), so the assertion
	// is stable and reviewable.
	key, err := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	if err != nil {
		t.Fatalf("test key: %v", err)
	}
	want := common.Address(crypto.PubkeyToAddress(key.PublicKey))

	org := "gda-capital"
	tokenID := uint64(42)
	nonce := "deadbeefcafef00d"
	msg := challengeMessage(org, tokenID, nonce)

	// Sign the EIP-191 digest exactly as a wallet's personal_sign would, then
	// re-shape v to the 27/28 form window.ethereum returns (recoverSigner
	// normalizes it back).
	sig, err := crypto.Sign(personalSignHash(msg), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig[64] += 27 // emulate wallet personal_sign v ∈ {27,28}

	got, err := recoverSigner(msg, "0x"+hex.EncodeToString(sig))
	if err != nil {
		t.Fatalf("recoverSigner: %v", err)
	}
	if got != want {
		t.Fatalf("recovered %s, want %s", got.Hex(), want.Hex())
	}

	// Tamper detection 1: a signature over a DIFFERENT message must not recover
	// to the same wallet (so a signature for slot 42 can't claim slot 7).
	other := challengeMessage(org, 7, nonce)
	if bad, err := recoverSigner(other, "0x"+hex.EncodeToString(sig)); err == nil && bad == want {
		t.Fatalf("signature for slot 42 wrongly validated slot 7")
	}

	// Tamper detection 2: a flipped signature byte must not recover to the wallet.
	tampered := make([]byte, len(sig))
	copy(tampered, sig)
	tampered[10] ^= 0xff
	if bad, err := recoverSigner(msg, "0x"+hex.EncodeToString(tampered)); err == nil && bad == want {
		t.Fatalf("tampered signature wrongly recovered to the wallet")
	}

	// Malformed inputs fail closed.
	if _, err := recoverSigner(msg, "0xdead"); err == nil {
		t.Fatalf("short signature should error")
	}
	if _, err := recoverSigner(msg, "not-hex"); err == nil {
		t.Fatalf("non-hex signature should error")
	}
}

// TestChallengeMessage_Deterministic pins the signed message shape: the server
// MUST reconstruct byte-identically to what the challenge endpoint issued, else
// recovery yields a different address and every claim fails.
func TestChallengeMessage_Deterministic(t *testing.T) {
	a := challengeMessage("lux", 5, "abc")
	b := challengeMessage("lux", 5, "abc")
	if a != b {
		t.Fatal("challengeMessage not deterministic")
	}
	for _, want := range []string{"Lux Validator Onboarding", "Organization: lux", "Validator slot: 5", "Nonce: abc"} {
		if !strings.Contains(a, want) {
			t.Fatalf("message missing %q:\n%s", want, a)
		}
	}
	// Distinct org / slot / nonce ⇒ distinct message (no cross-binding).
	if challengeMessage("lux", 5, "abc") == challengeMessage("zoo", 5, "abc") {
		t.Fatal("org not bound into the message")
	}
	if challengeMessage("lux", 5, "abc") == challengeMessage("lux", 6, "abc") {
		t.Fatal("slot not bound into the message")
	}
}

// TestValidatorTier pins the tier bound (tokenId 1..N are validator slots).
func TestValidatorTier(t *testing.T) {
	r, err := newNFTReader("http://unused", GenesisNFTContract, 100)
	if err != nil {
		t.Fatalf("newNFTReader: %v", err)
	}
	for _, id := range []uint64{1, 50, 100} {
		if !r.isValidatorTier(id) {
			t.Fatalf("token %d should be validator tier", id)
		}
	}
	for _, id := range []uint64{0, 101, 500} {
		if r.isValidatorTier(id) {
			t.Fatalf("token %d should NOT be validator tier", id)
		}
	}
}

// TestOwnerOfABIPacking proves the on-chain read path is well-formed: ownerOf
// packs to the canonical 4-byte selector + a 32-byte tokenId. (The live call is
// exercised by TestLiveOwnerOf, network-gated.)
func TestOwnerOfABIPacking(t *testing.T) {
	r, err := newNFTReader("http://unused", GenesisNFTContract, 100)
	if err != nil {
		t.Fatalf("newNFTReader: %v", err)
	}
	data, err := r.parsed.Pack("ownerOf", new(big.Int).SetUint64(1))
	if err != nil {
		t.Fatalf("pack ownerOf: %v", err)
	}
	if len(data) != 36 {
		t.Fatalf("ownerOf calldata = %d bytes, want 36 (4 selector + 32 arg)", len(data))
	}
	// keccak256("ownerOf(uint256)")[:4] = 0x6352211e — the canonical ERC-721 selector.
	if got := hex.EncodeToString(data[:4]); got != "6352211e" {
		t.Fatalf("ownerOf selector = %s, want 6352211e", got)
	}
}

// ── store ────────────────────────────────────────────────────────────────────

func tempStore(t *testing.T) *Store {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "validators.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestStore_ClaimSlot(t *testing.T) {
	st := tempStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	slot := Slot{TokenID: 7, Org: "gda", Wallet: "0xabc", NodeID: "NodeID-1", KMSRef: "orgs/gda/validators/7",
		CRName: "val-gda-7", Namespace: "lux-validators", Status: "provisioning", CreatedAt: now, UpdatedAt: now}

	if _, err := st.ClaimSlot(ctx, slot); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Idempotent re-claim by the same org returns the existing row.
	got, err := st.ClaimSlot(ctx, slot)
	if err != nil || got.NodeID != "NodeID-1" {
		t.Fatalf("idempotent re-claim: got %+v err %v", got, err)
	}
	// A DIFFERENT org claiming the same slot is a conflict (defense-in-depth).
	other := slot
	other.Org = "attacker"
	if _, err := st.ClaimSlot(ctx, other); err != errConflict {
		t.Fatalf("cross-org claim: want errConflict, got %v", err)
	}
	// Status advances.
	if err := st.SetSlotStatus(ctx, 7, "node_created", time.Now().Unix()); err != nil {
		t.Fatalf("set status: %v", err)
	}
	after, _ := st.GetSlot(ctx, 7)
	if after.Status != "node_created" {
		t.Fatalf("status = %q, want node_created", after.Status)
	}
	// Org listing is tenant-scoped.
	list, _ := st.ListSlots(ctx, "gda", 100)
	if len(list) != 1 {
		t.Fatalf("list gda = %d, want 1", len(list))
	}
	if none, _ := st.ListSlots(ctx, "attacker", 100); len(none) != 0 {
		t.Fatalf("attacker should see 0 slots, got %d", len(none))
	}
}

func TestStore_EnqueueRegistration_OwnerGated(t *testing.T) {
	st := tempStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	reg := Registration{ID: "vreg_1", TokenID: 7, Org: "gda", NodeID: "NodeID-1",
		Status: "pending_owner_approval", CreatedAt: now, UpdatedAt: now}
	saved, err := st.EnqueueRegistration(ctx, reg)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if saved.Status != "pending_owner_approval" {
		t.Fatalf("registration must be owner-gated, got status %q", saved.Status)
	}
	// Idempotent per token (a retried provision never double-queues).
	dup := reg
	dup.ID = "vreg_2"
	again, _ := st.EnqueueRegistration(ctx, dup)
	if again.ID != "vreg_1" {
		t.Fatalf("re-enqueue should return original vreg_1, got %s", again.ID)
	}
	all, _ := st.ListRegistrations(ctx, "gda", 100)
	if len(all) != 1 {
		t.Fatalf("registrations = %d, want 1 (idempotent)", len(all))
	}
}

func TestStore_Challenge_SingleUse(t *testing.T) {
	st := tempStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := st.PutChallenge(ctx, "n1", "gda", now.Add(time.Minute).Unix(), now.Unix()); err != nil {
		t.Fatalf("put: %v", err)
	}
	// First consume succeeds; second fails (single-use).
	if err := st.ConsumeChallenge(ctx, "n1", "gda", now.Unix()); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := st.ConsumeChallenge(ctx, "n1", "gda", now.Unix()); err != errChallenge {
		t.Fatalf("replay: want errChallenge, got %v", err)
	}
	// Wrong org cannot consume another org's nonce.
	_ = st.PutChallenge(ctx, "n2", "gda", now.Add(time.Minute).Unix(), now.Unix())
	if err := st.ConsumeChallenge(ctx, "n2", "zoo", now.Unix()); err != errChallenge {
		t.Fatalf("cross-org consume: want errChallenge, got %v", err)
	}
	// Expired nonce cannot be consumed.
	_ = st.PutChallenge(ctx, "n3", "gda", now.Add(-time.Second).Unix(), now.Add(-time.Minute).Unix())
	if err := st.ConsumeChallenge(ctx, "n3", "gda", now.Unix()); err != errChallenge {
		t.Fatalf("expired consume: want errChallenge, got %v", err)
	}
}

// ── keygen + KMS seal ────────────────────────────────────────────────────────

// fakeKMS is an in-memory cloud.KMSClient for sealing tests.
type fakeKMS struct {
	store map[string][]byte
	fail  bool
}

func (f *fakeKMS) PutSecret(_ context.Context, ref string, v []byte) error {
	if f.fail {
		return errChallenge // any error
	}
	if f.store == nil {
		f.store = map[string][]byte{}
	}
	f.store[ref] = append([]byte(nil), v...)
	return nil
}
func (f *fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) { return f.store[ref], nil }
func (f *fakeKMS) Sign(context.Context, string, []byte) ([]byte, error)    { return nil, nil }

func TestGenerateStakingIdentity_SelfCheckAndSeal(t *testing.T) {
	id, err := generateStakingIdentity()
	if err != nil {
		t.Fatalf("generate (with self-check): %v", err)
	}
	if id.NodeID == "" || !strings.HasPrefix(id.NodeID, "NodeID-") {
		t.Fatalf("NodeID = %q, want a NodeID-… value", id.NodeID)
	}
	if len(id.BLSPubkeyHex) == 0 {
		t.Fatal("BLS pubkey hex empty")
	}
	// Exactly the 5 luxd staking artifacts, all non-empty.
	if len(id.artifacts) != len(stakingArtifacts) {
		t.Fatalf("artifacts = %d, want %d", len(id.artifacts), len(stakingArtifacts))
	}
	for _, name := range stakingArtifacts {
		if len(id.artifacts[name]) == 0 {
			t.Fatalf("artifact %q empty", name)
		}
	}
	// Two identities are distinct (fresh randomness per call).
	id2, _ := generateStakingIdentity()
	if id2.NodeID == id.NodeID {
		t.Fatal("two generated identities share a NodeID (not fresh)")
	}

	// Seal into KMS: every artifact lands under the org-scoped base ref, never
	// plaintext in the caller.
	kms := &fakeKMS{}
	base := kmsStakingBaseRef("gda", 42)
	if err := id.seal(context.Background(), kms, base); err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, name := range stakingArtifacts {
		if _, ok := kms.store[base+"/"+name]; !ok {
			t.Fatalf("artifact %q not sealed at %s/%s", name, base, name)
		}
	}
	// Fail closed: a KMS error refuses the whole seal (no partial identity).
	if err := id.seal(context.Background(), &fakeKMS{fail: true}, base); err == nil {
		t.Fatal("seal must fail closed on KMS error")
	}
	// Nil KMS is refused (never plaintext fallback).
	if err := id.seal(context.Background(), nil, base); err == nil {
		t.Fatal("seal must refuse a nil KMS")
	}
}

func TestKMSStakingBaseRef(t *testing.T) {
	if got := kmsStakingBaseRef("gda", 42); got != "orgs/gda/validators/42" {
		t.Fatalf("kmsStakingBaseRef = %q", got)
	}
}

// ── mainnet-safety guards (the non-negotiable gate) ──────────────────────────

func TestProvisionGuards_NeverTouchesMainnet(t *testing.T) {
	// crName is always val-<org>-<tokenId>, never a live-luxd identity.
	name := crName("gda-capital", 42)
	if name != "val-gda-capital-42" {
		t.Fatalf("crName = %q", name)
	}
	if reservedNameRE.MatchString(name) {
		t.Fatalf("crName %q matched the reserved luxd pattern", name)
	}
	// Even a hostile org that folds to "luxd" cannot escape val- prefix.
	if n := crName("luxd", 0); reservedNameRE.MatchString(n) {
		t.Fatalf("crName(%q) = %q matched reserved pattern", "luxd", n)
	}

	// Guard 1: reserved namespaces are refused.
	for _, ns := range []string{"lux-mainnet", "lux-testnet", "lux-devnet", "lux-system", "default"} {
		p := &k8sProvisioner{dyn: nil, cfg: crConfig{Group: "node.lux.cloud", Namespace: ns}}
		if _, _, err := p.guard("gda", 42); err == nil {
			t.Fatalf("guard admitted reserved namespace %q", ns)
		}
	}
	// Guard 2: the legacy/shim groups are refused (only a dedicated group is ok).
	for _, g := range []string{"lux.network", "lux.cloud"} {
		p := &k8sProvisioner{cfg: crConfig{Group: g, Namespace: "lux-validators"}}
		if _, _, err := p.guard("gda", 42); err == nil {
			t.Fatalf("guard admitted forbidden group %q", g)
		}
	}
	// The allowed configuration passes and yields the isolated identity.
	p := &k8sProvisioner{cfg: crConfig{Group: "node.lux.cloud", Namespace: "lux-validators"}}
	name, ns, err := p.guard("gda", 42)
	if err != nil || ns != "lux-validators" || name != "val-gda-42" {
		t.Fatalf("allowed guard: name=%q ns=%q err=%v", name, ns, err)
	}
}

func TestProvisioner_UnavailableFailsHonestly(t *testing.T) {
	p := &k8sProvisioner{dyn: nil, initErr: "no cluster", cfg: crConfig{Group: "node.lux.cloud", Namespace: "lux-validators"}}
	if p.Available() {
		t.Fatal("nil dyn must be unavailable")
	}
	if _, _, err := p.Provision(context.Background(), provisionRequest{Org: "gda", TokenID: 1}); err == nil {
		t.Fatal("Provision on an unavailable cluster must error (honest), not fake success")
	}
}

func TestImageRefSplit(t *testing.T) {
	cases := map[string][2]string{
		"ghcr.io/luxfi/node:v1.36.15": {"ghcr.io/luxfi/node", "v1.36.15"},
		"ghcr.io/luxfi/node":          {"ghcr.io/luxfi/node", "latest"},
	}
	for ref, want := range cases {
		if r, tag := imageRepo(ref), imageTag(ref); r != want[0] || tag != want[1] {
			t.Fatalf("%s → (%s,%s), want (%s,%s)", ref, r, tag, want[0], want[1])
		}
	}
}

// ── live, network-gated (skipped by default / in -short) ─────────────────────

// TestLiveOwnerOf reads ownerOf on the REAL ETH-mainnet GenesisNFT to prove the
// on-chain read path works end-to-end. Skipped in -short and when
// VALIDATORS_LIVE_ETH is unset, so CI stays hermetic; run manually with:
//
//	VALIDATORS_LIVE_ETH=1 go test ./clients/validators/ -run TestLiveOwnerOf -v
func TestLiveOwnerOf(t *testing.T) {
	if testing.Short() || os.Getenv("VALIDATORS_LIVE_ETH") == "" {
		t.Skip("live ETH read: set VALIDATORS_LIVE_ETH=1 to run")
	}
	rpc := envOr("VALIDATORS_ETH_RPC", "https://ethereum-rpc.publicnode.com")
	r, err := newNFTReader(rpc, GenesisNFTContract, 100)
	if err != nil {
		t.Fatalf("newNFTReader: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	owner, err := r.ownerOf(ctx, 1)
	if err != nil {
		t.Fatalf("live ownerOf(1): %v", err)
	}
	if owner == (common.Address{}) {
		t.Fatal("live ownerOf(1) returned the zero address")
	}
	t.Logf("live ownerOf(1) on %s = %s", GenesisNFTContract, owner.Hex())
}
