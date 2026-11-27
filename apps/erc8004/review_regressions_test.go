// SPDX-License-Identifier: Apache-2.0 OR MIT

package erc8004

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"golang.org/x/crypto/sha3"
)

func TestRegisterAgentRejectsMaxUint64AndNeverOverwritesExistingToken(t *testing.T) {
	domain := DefaultDomain()
	now := int64(1_760_000_000)
	reg := NewIdentityRegistry(domain, func() int64 { return now })
	owner1, _ := GenerateKeyPair()
	ctrl1, _ := GenerateKeyPair()
	attacker, _ := GenerateKeyPair()
	attackerCtrl, _ := GenerateKeyPair()

	// Register legitimate token 1.
	ag1, _, err := reg.RegisterAgent(signAgent(t, domain, owner1, AgentIdentity{
		OwnerAddress:     owner1.Address(),
		ControllerPubKey: ctrl1.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://legit-1",
		Capabilities:     []string{"audit"},
	}))
	if err != nil || ag1.TokenID != 1 {
		t.Fatalf("register legit agent 1: ag=%+v err=%v", ag1, err)
	}

	// Attacker tries explicit TokenID = math.MaxUint64 to wrap nextTokenID -> MUST be rejected.
	maxReq := signAgent(t, domain, attacker, AgentIdentity{
		TokenID:          math.MaxUint64,
		OwnerAddress:     attacker.Address(),
		ControllerPubKey: attackerCtrl.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://wrap-attack",
		Capabilities:     []string{"evil"},
	})
	if _, _, err := reg.RegisterAgent(maxReq); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for TokenID=math.MaxUint64, got %v", err)
	}

	// Register near-max explicit ID (MaxUint64 - 1), then automatic allocation gets MaxUint64,
	// then subsequent automatic allocation fails cleanly without wrapping to 0 or overwriting token 1.
	nearMaxReq := signAgent(t, domain, attacker, AgentIdentity{
		TokenID:          math.MaxUint64 - 1,
		OwnerAddress:     attacker.Address(),
		ControllerPubKey: attackerCtrl.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://near-max",
		Capabilities:     []string{"a"},
	})
	if _, _, err := reg.RegisterAgent(nearMaxReq); err != nil {
		t.Fatalf("register near-max token: %v", err)
	}
	lastAuto, _, err := reg.RegisterAgent(signAgent(t, domain, attacker, AgentIdentity{
		OwnerAddress:     attacker.Address(),
		ControllerPubKey: attackerCtrl.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://last-auto",
		Capabilities:     []string{"b"},
	}))
	if err != nil || lastAuto.TokenID != math.MaxUint64 {
		t.Fatalf("expected last automatic token to be MaxUint64, got %+v err=%v", lastAuto, err)
	}
	if _, _, err := reg.RegisterAgent(signAgent(t, domain, attacker, AgentIdentity{
		OwnerAddress:     attacker.Address(),
		ControllerPubKey: attackerCtrl.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://post-exhaust",
		Capabilities:     []string{"c"},
	})); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected allocator exhaustion ErrInvalidRequest, got %v", err)
	}

	// Verify token 1 was NEVER overwritten.
	still1, err := reg.GetAgent(ag1.AgentID)
	if err != nil || !SameAddress(still1.OwnerAddress, owner1.Address()) {
		t.Fatalf("token 1 owner tampered: %+v err=%v", still1, err)
	}
}

func TestSnapshotRejectsDeletedRevocationEvenWithUnchangedTxLogAndStateRoot(t *testing.T) {
	domain := DefaultDomain()
	now := int64(1_760_000_000)
	clock := func() int64 { return now }
	svc := NewService(domain, clock, nil)
	actors := newTestActors(t)

	ag, _, err := svc.Identity.RegisterAgent(signAgent(t, domain, actors.owner, AgentIdentity{
		OwnerAddress:     actors.owner.Address(),
		ControllerPubKey: actors.controller.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://rev-snap",
		Capabilities:     []string{"sandbox.read"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	grant := signDelegation(t, domain, actors.owner, DelegationGrant{
		AgentID:          ag.AgentID,
		OwnerAddress:     actors.owner.Address(),
		DelegateAddress:  actors.delegate.Address(),
		DelegatePubKey:   actors.delegate.CompressedPubKeyHex(),
		TaskScope:        "snap-rev",
		AllowedTools:     []string{"sandbox.read"},
		AllowedResources: []string{"sandbox://org/*"},
		MaxSpendAtomic:   10000,
		MaxPerCallAtomic: 5000,
		Asset:            "USDC",
		IssuedAt:         now,
		ExpiresAt:        now + 3600,
		Nonce:            1,
	})
	if _, err := svc.Identity.RegisterDelegation(grant); err != nil {
		t.Fatal(err)
	}
	revSig, err := actors.owner.SignDigest(RevocationDigest(domain, ag.AgentID, grant.DelegationID, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Identity.RevokeDelegation(ag.AgentID, grant.DelegationID, revSig, 1); err != nil {
		t.Fatal(err)
	}

	snap := svc.Identity.ExportSnapshot(svc.Reputation)
	var buf bytes.Buffer
	if err := WriteSnapshotJSONL(&buf, snap); err != nil {
		t.Fatal(err)
	}

	// Strip the `revocation` JSONL row while keeping TxLog and StateRoot untouched.
	var stripped bytes.Buffer
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, `"type":"revocation"`) {
			continue
		}
		if strings.TrimSpace(line) != "" {
			stripped.WriteString(line)
			stripped.WriteByte('\n')
		}
	}
	tampered, err := ReadSnapshotJSONL(&stripped)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshotOffline(tampered); !errors.Is(err, ErrSnapshotTampered) {
		t.Fatalf("expected ErrSnapshotTampered when revocation row deleted, got %v", err)
	}
	if _, _, err := ImportSnapshot(tampered, clock); !errors.Is(err, ErrSnapshotTampered) {
		t.Fatalf("expected ImportSnapshot to reject snapshot with deleted revocation, got %v", err)
	}

	// Even if an attacker ALSO recomputes EntityRoot in the header after stripping the revocation,
	// TxLog commitment verification must STILL reject with ErrSnapshotTampered!
	recomputed := ComputeSnapshotEntityRoot(tampered)
	tampered.EntityRoot = "0x" + hex.EncodeToString(recomputed[:])
	if err := VerifySnapshotOffline(tampered); !errors.Is(err, ErrSnapshotTampered) {
		t.Fatalf("expected ErrSnapshotTampered even with recomputed entityRoot when revocation missing from TxLog, got %v", err)
	}
}

func TestUpdateAgentRejectsCrossAgentReplayUnderSharedOwner(t *testing.T) {
	domain := DefaultDomain()
	now := int64(1_760_000_000)
	clock := func() int64 { return now }
	reg := NewIdentityRegistry(domain, clock)

	owner, _ := GenerateKeyPair()
	ctrlA, _ := GenerateKeyPair()
	ctrlB, _ := GenerateKeyPair()
	newCtrlA, _ := GenerateKeyPair()

	agA, _, err := reg.RegisterAgent(signAgent(t, domain, owner, AgentIdentity{
		OwnerAddress:     owner.Address(),
		ControllerPubKey: ctrlA.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://agent-a-v1",
		Capabilities:     []string{"a"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	agB, _, err := reg.RegisterAgent(signAgent(t, domain, owner, AgentIdentity{
		OwnerAddress:     owner.Address(),
		ControllerPubKey: ctrlB.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://agent-b-v1",
		Capabilities:     []string{"b"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	// Owner signs a legitimate v2 update targeting Agent A.
	updA := *agA
	updA.Version = 2
	updA.ControllerPubKey = newCtrlA.CompressedPubKeyHex()
	updA.ControllerAddress = newCtrlA.Address()
	updA.MetadataURI = "ipfs://agent-a-v2"
	updA.OwnerSignature, err = owner.SignDigest(updA.UpdateDigest(domain))
	if err != nil {
		t.Fatal(err)
	}

	// Replay the exact signed update against Agent B by changing only AgentID (and clearing TokenID).
	replayed := updA
	replayed.AgentID = agB.AgentID
	replayed.TokenID = 0
	if _, _, err := reg.UpdateAgent(replayed); err == nil {
		t.Fatal("expected cross-agent update replay against Agent B to be rejected")
	}

	// Verify Agent B is untouched.
	gotB, err := reg.GetAgent(agB.AgentID)
	if err != nil || gotB.Version != 1 || gotB.MetadataURI != "ipfs://agent-b-v1" || !SameAddress(gotB.ControllerAddress, ctrlB.Address()) {
		t.Fatalf("Agent B was unexpectedly modified: %+v err=%v", gotB, err)
	}

	// Legitimate update against Agent A succeeds and passes snapshot verification!
	updatedA, _, err := reg.UpdateAgent(updA)
	if err != nil || updatedA.Version != 2 || !SameAddress(updatedA.ControllerAddress, newCtrlA.Address()) {
		t.Fatalf("legitimate UpdateAgent(A) failed: ag=%+v err=%v", updatedA, err)
	}
	snap := reg.ExportSnapshot(nil)
	if err := VerifySnapshotOffline(snap); err != nil {
		t.Fatalf("VerifySnapshotOffline failed after UpdateAgent: %v", err)
	}
}

func TestRecordWorkReceiptRequiresVerifiedPaymentEvidence(t *testing.T) {
	domain := DefaultDomain()
	now := int64(1_760_000_000)
	svc := NewService(domain, func() int64 { return now }, nil)
	actors := newTestActors(t)

	ag, _, err := svc.Identity.RegisterAgent(signAgent(t, domain, actors.owner, AgentIdentity{
		OwnerAddress:     actors.owner.Address(),
		ControllerPubKey: actors.controller.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://paid-work",
		Capabilities:     []string{"audit"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	grant := signDelegation(t, domain, actors.owner, DelegationGrant{
		AgentID:          ag.AgentID,
		OwnerAddress:     actors.owner.Address(),
		DelegateAddress:  actors.delegate.Address(),
		DelegatePubKey:   actors.delegate.CompressedPubKeyHex(),
		TaskScope:        "work",
		AllowedTools:     []string{"audit"},
		AllowedResources: []string{"sandbox://org/*"},
		MaxSpendAtomic:   100000,
		MaxPerCallAtomic: 50000,
		Asset:            "USDC",
		IssuedAt:         now,
		ExpiresAt:        now + 3600,
		Nonce:            1,
	})
	if _, err := svc.Identity.RegisterDelegation(grant); err != nil {
		t.Fatal(err)
	}

	// 1. Non-hex PaymentTxHash = "not-a-transaction" MUST be rejected.
	bogusWR := signWorkReceipt(t, domain, actors.delegate, actors.client, WorkReceipt{
		ReceiptID:       "wr-bogus",
		AgentID:         ag.AgentID,
		DelegationID:    grant.DelegationID,
		ClientAddress:   actors.client.Address(),
		WorkerAddress:   actors.delegate.Address(),
		PaymentTxHash:   "not-a-transaction",
		AmountAtomic:    999_999_999,
		Asset:           "USDC",
		ToolCallsDigest: Keccak256Hex([]byte("t")),
		TaskOutputHash:  Keccak256Hex([]byte("o")),
		CompletedAt:     now,
	})
	if _, err := svc.Reputation.RecordWorkReceipt(bogusWR); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for non-txhash payment, got %v", err)
	}

	// 2. Well-formed 32-byte hash without verified settlement evidence MUST be rejected.
	unverifiedWR := signWorkReceipt(t, domain, actors.delegate, actors.client, WorkReceipt{
		ReceiptID:       "wr-unverified",
		AgentID:         ag.AgentID,
		DelegationID:    grant.DelegationID,
		ClientAddress:   actors.client.Address(),
		WorkerAddress:   actors.delegate.Address(),
		PaymentTxHash:   Keccak256Hex([]byte("invented-tx-no-settlement")),
		AmountAtomic:    50000,
		Asset:           "USDC",
		ToolCallsDigest: Keccak256Hex([]byte("t")),
		TaskOutputHash:  Keccak256Hex([]byte("o")),
		CompletedAt:     now,
	})
	if _, err := svc.Reputation.RecordWorkReceipt(unverifiedWR); !errors.Is(err, ErrUnpaidWorkReceipt) {
		t.Fatalf("expected ErrUnpaidWorkReceipt for unsettled payment hash, got %v", err)
	}

	// 3. Real settled payment via PaymentLimitEnforcer, but inflated amount in WorkReceipt -> rejected.
	payRec, err := svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "auth-real-1",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.client.Address(),
		AmountAtomic:    10000,
		Asset:           "USDC",
		TaskRef:         "t1",
		Nonce:           1,
		Timestamp:       now,
	}))
	if err != nil {
		t.Fatal(err)
	}
	inflatedWR := signWorkReceipt(t, domain, actors.delegate, actors.client, WorkReceipt{
		ReceiptID:       "wr-inflated",
		AgentID:         ag.AgentID,
		DelegationID:    grant.DelegationID,
		ClientAddress:   actors.client.Address(),
		WorkerAddress:   actors.delegate.Address(),
		PaymentTxHash:   payRec.PaymentTxHash,
		AmountAtomic:    999_999, // inflated beyond 10000
		Asset:           "USDC",
		ToolCallsDigest: Keccak256Hex([]byte("t")),
		TaskOutputHash:  Keccak256Hex([]byte("o")),
		CompletedAt:     now,
	})
	if _, err := svc.Reputation.RecordWorkReceipt(inflatedWR); !errors.Is(err, ErrWorkReceiptMismatch) {
		t.Fatalf("expected ErrWorkReceiptMismatch for inflated work receipt amount, got %v", err)
	}

	// 4. Matching receipt succeeds once, and reusing the same PaymentTxHash a second time is rejected.
	validWR := signWorkReceipt(t, domain, actors.delegate, actors.client, WorkReceipt{
		ReceiptID:       "wr-valid-1",
		AgentID:         ag.AgentID,
		DelegationID:    grant.DelegationID,
		ClientAddress:   actors.client.Address(),
		WorkerAddress:   actors.delegate.Address(),
		PaymentTxHash:   payRec.PaymentTxHash,
		AmountAtomic:    payRec.AmountAtomic,
		Asset:           payRec.Asset,
		ToolCallsDigest: Keccak256Hex([]byte("t")),
		TaskOutputHash:  Keccak256Hex([]byte("o")),
		CompletedAt:     now,
	})
	if _, err := svc.Reputation.RecordWorkReceipt(validWR); err != nil {
		t.Fatalf("valid RecordWorkReceipt failed: %v", err)
	}
	dupPayWR := signWorkReceipt(t, domain, actors.delegate, actors.client, WorkReceipt{
		ReceiptID:       "wr-dup-tx",
		AgentID:         ag.AgentID,
		DelegationID:    grant.DelegationID,
		ClientAddress:   actors.client.Address(),
		WorkerAddress:   actors.delegate.Address(),
		PaymentTxHash:   payRec.PaymentTxHash,
		AmountAtomic:    payRec.AmountAtomic,
		Asset:           payRec.Asset,
		ToolCallsDigest: Keccak256Hex([]byte("t2")),
		TaskOutputHash:  Keccak256Hex([]byte("o2")),
		CompletedAt:     now,
	})
	if _, err := svc.Reputation.RecordWorkReceipt(dupPayWR); !errors.Is(err, ErrReplayDetected) {
		t.Fatalf("expected ErrReplayDetected when reusing PaymentTxHash, got %v", err)
	}
}

func TestSnapshotMigrationPreservesSpendCapsAndReplayGuards(t *testing.T) {
	domain := DefaultDomain()
	now := int64(1_760_000_000)
	clock := func() int64 { return now }
	svc := NewService(domain, clock, nil)
	actors := newTestActors(t)

	ag, _, err := svc.Identity.RegisterAgent(signAgent(t, domain, actors.owner, AgentIdentity{
		OwnerAddress:     actors.owner.Address(),
		ControllerPubKey: actors.controller.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://spend-mig",
		Capabilities:     []string{"sandbox.read"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	grant := signDelegation(t, domain, actors.owner, DelegationGrant{
		AgentID:          ag.AgentID,
		OwnerAddress:     actors.owner.Address(),
		DelegateAddress:  actors.delegate.Address(),
		DelegatePubKey:   actors.delegate.CompressedPubKeyHex(),
		TaskScope:        "cap-mig",
		AllowedTools:     []string{"sandbox.read"},
		AllowedResources: []string{"sandbox://org/*"},
		MaxSpendAtomic:   25000,
		MaxPerCallAtomic: 25000,
		Asset:            "USDC",
		IssuedAt:         now,
		ExpiresAt:        now + 3600,
		Nonce:            1,
	})
	if _, err := svc.Identity.RegisterDelegation(grant); err != nil {
		t.Fatal(err)
	}

	toolReq := signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "tool-pre-mig-1",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://org/a",
		InputHash:       "0x01",
		Nonce:           1,
		Timestamp:       now,
	})
	if _, err := svc.Gate.Tools.Execute(toolReq, nil); err != nil {
		t.Fatalf("pre-migration tool execute: %v", err)
	}

	payReq := signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "pay-pre-mig-1",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.client.Address(),
		AmountAtomic:    25000, // exhausts full cap!
		Asset:           "USDC",
		TaskRef:         "full-cap",
		Nonce:           1,
		Timestamp:       now,
	})
	if _, err := svc.Gate.Payments.AuthorizePayment(payReq); err != nil {
		t.Fatalf("pre-migration payment auth: %v", err)
	}

	// Export -> JSONL -> Read -> Import -> construct fresh EnforcementGate(importedReg, nil).
	snap := svc.Identity.ExportSnapshot(svc.Reputation)
	var buf bytes.Buffer
	if err := WriteSnapshotJSONL(&buf, snap); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadSnapshotJSONL(&buf)
	if err != nil {
		t.Fatal(err)
	}
	importedReg, _, err := ImportSnapshot(decoded, clock)
	if err != nil {
		t.Fatalf("ImportSnapshot: %v", err)
	}
	importedGate := NewEnforcementGate(importedReg, nil)

	// 1. Exhausted spend cap MUST reject any new payment after migration!
	extraPay := signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "pay-post-mig-2",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.client.Address(),
		AmountAtomic:    1,
		Asset:           "USDC",
		TaskRef:         "over-cap",
		Nonce:           2,
		Timestamp:       now,
	})
	if _, err := importedGate.Payments.AuthorizePayment(extraPay); !errors.Is(err, ErrSpendLimitExceeded) {
		t.Fatalf("expected ErrSpendLimitExceeded after migration, got %v", err)
	}
	if importedGate.Payments.Spent(grant.DelegationID) != 25000 {
		t.Fatalf("expected migrated spent=25000, got %d", importedGate.Payments.Spent(grant.DelegationID))
	}

	// 2. Previously accepted tool request MUST be rejected as replay after migration!
	if _, err := importedGate.Tools.Execute(toolReq, nil); !errors.Is(err, ErrReplayDetected) {
		t.Fatalf("expected ErrReplayDetected on replayed tool request after migration, got %v", err)
	}

	// 3. Previously accepted payment request MUST be rejected as replay after migration!
	if _, err := importedGate.Payments.AuthorizePayment(payReq); !errors.Is(err, ErrReplayDetected) {
		t.Fatalf("expected ErrReplayDetected on replayed payment auth after migration, got %v", err)
	}
}

func TestCanonicalEIP712TypedDataVector(t *testing.T) {
	// Independent clean-room EIP-712 Solidity abi.encode reference implementation
	// using only sha3.NewLegacyKeccak256 (zero package helpers).
	keccak := func(chunks ...[]byte) []byte {
		h := sha3.NewLegacyKeccak256()
		for _, c := range chunks {
			_, _ = h.Write(c)
		}
		return h.Sum(nil)
	}
	to32 := func(b []byte) [32]byte {
		var out [32]byte
		copy(out[:], b)
		return out
	}
	uint64Word := func(v uint64) []byte {
		w := make([]byte, 32)
		binary.BigEndian.PutUint64(w[24:], v)
		return w
	}
	addrWord := func(hexAddr string) []byte {
		raw, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(hexAddr), "0x"))
		if err != nil || len(raw) != 20 {
			t.Fatalf("bad addr %q", hexAddr)
		}
		w := make([]byte, 32)
		copy(w[12:], raw)
		return w
	}

	domain := EIP712Domain{
		Name:              "ERC-8004 Agent Registry",
		Version:           "1",
		ChainID:           31337,
		VerifyingContract: "0x8004000000000000000000000000000000000001",
	}
	domainTypeHash := keccak([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	domainSep := keccak(
		domainTypeHash,
		keccak([]byte(domain.Name)),
		keccak([]byte(domain.Version)),
		uint64Word(domain.ChainID),
		addrWord(domain.VerifyingContract),
	)

	// 1. BumpRevocationEpoch vector.
	agentID := "eip155:31337:0x8004000000000000000000000000000000000001:42"
	newEpoch := uint64(7)
	bumpTypeHash := keccak([]byte("BumpRevocationEpoch(string agentId,uint64 newEpoch)"))
	bumpStructHash := keccak(
		bumpTypeHash,
		keccak([]byte(agentID)),
		uint64Word(newEpoch),
	)
	wantBumpDigest := to32(keccak([]byte{0x19, 0x01}, domainSep, bumpStructHash))
	gotBumpDigest := EpochBumpDigest(domain, agentID, newEpoch)
	if gotBumpDigest != wantBumpDigest {
		t.Fatalf("EpochBumpDigest mismatch:\n got: %x\nwant: %x", gotBumpDigest, wantBumpDigest)
	}

	// Sign the independently computed digest and verify BumpRevocationEpoch accepts it.
	owner, _ := GenerateKeyPair()
	ctrl, _ := GenerateKeyPair()
	reg := NewIdentityRegistry(domain, func() int64 { return 1_760_000_000 })
	ag, _, err := reg.RegisterAgent(signAgent(t, domain, owner, AgentIdentity{
		TokenID:          42,
		OwnerAddress:     owner.Address(),
		ControllerPubKey: ctrl.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://eip712-vector",
		Capabilities:     []string{"eip712"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if ag.AgentID != agentID {
		t.Fatalf("unexpected canonical agentId: got %s want %s", ag.AgentID, agentID)
	}
	sig, err := owner.SignDigest(wantBumpDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.BumpRevocationEpoch(agentID, newEpoch, sig); err != nil {
		t.Fatalf("BumpRevocationEpoch rejected canonical EIP-712 signature: %v", err)
	}

	// 2. RevokeDelegation vector.
	delID := "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	revTypeHash := keccak([]byte("RevokeDelegation(string agentId,string delegationId,uint64 nonce)"))
	revStructHash := keccak(
		revTypeHash,
		keccak([]byte(agentID)),
		keccak([]byte(delID)),
		uint64Word(99),
	)
	wantRevDigest := to32(keccak([]byte{0x19, 0x01}, domainSep, revStructHash))
	if got := RevocationDigest(domain, agentID, delID, 99); got != wantRevDigest {
		t.Fatalf("RevocationDigest mismatch:\n got: %x\nwant: %x", got, wantRevDigest)
	}
}

func TestEnforcerSeparatesCallerIDsFromDelegationReplayNamespaces(t *testing.T) {
	domain := DefaultDomain()
	now := int64(1_760_000_000)
	svc := NewService(domain, func() int64 { return now }, nil)

	ownerA, _ := GenerateKeyPair()
	ctrlA, _ := GenerateKeyPair()
	delA, _ := GenerateKeyPair()
	ownerB, _ := GenerateKeyPair()
	ctrlB, _ := GenerateKeyPair()
	delB, _ := GenerateKeyPair()
	payee, _ := GenerateKeyPair()

	agA, _, err := svc.Identity.RegisterAgent(signAgent(t, domain, ownerA, AgentIdentity{
		OwnerAddress: ownerA.Address(), ControllerPubKey: ctrlA.CompressedPubKeyHex(), MetadataURI: "ipfs://a", Capabilities: []string{"r"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	agB, _, err := svc.Identity.RegisterAgent(signAgent(t, domain, ownerB, AgentIdentity{
		OwnerAddress: ownerB.Address(), ControllerPubKey: ctrlB.CompressedPubKeyHex(), MetadataURI: "ipfs://b", Capabilities: []string{"r"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	grantA := signDelegation(t, domain, ownerA, DelegationGrant{
		AgentID: agA.AgentID, OwnerAddress: ownerA.Address(), DelegateAddress: delA.Address(), DelegatePubKey: delA.CompressedPubKeyHex(),
		TaskScope: "a", AllowedTools: []string{"sandbox.read"}, AllowedResources: []string{"sandbox://a/*"},
		MaxSpendAtomic: 10000, MaxPerCallAtomic: 5000, Asset: "USDC", IssuedAt: now, ExpiresAt: now + 3600, Nonce: 1,
	})
	grantB := signDelegation(t, domain, ownerB, DelegationGrant{
		AgentID: agB.AgentID, OwnerAddress: ownerB.Address(), DelegateAddress: delB.Address(), DelegatePubKey: delB.CompressedPubKeyHex(),
		TaskScope: "b", AllowedTools: []string{"sandbox.read"}, AllowedResources: []string{"sandbox://b/*"},
		MaxSpendAtomic: 10000, MaxPerCallAtomic: 5000, Asset: "USDC", IssuedAt: now, ExpiresAt: now + 3600, Nonce: 1,
	})
	if _, err := svc.Identity.RegisterDelegation(grantA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Identity.RegisterDelegation(grantB); err != nil {
		t.Fatal(err)
	}

	// Delegate A attempts to poison Delegate B's nonce-1 key and also uses caller-local ID "req-1"/"auth-1".
	for i, reqID := range []string{fmt.Sprintf("tool:%s:1", grantB.DelegationID), "req-1"} {
		if _, err := svc.Gate.Tools.Execute(signToolReq(t, domain, delA, ToolActionRequest{
			RequestID: reqID, DelegationID: grantA.DelegationID, AgentID: agA.AgentID, DelegateAddress: delA.Address(),
			Tool: "sandbox.read", Resource: "sandbox://a/f", InputHash: "0x01", Nonce: uint64(i + 1), Timestamp: now,
		}), nil); err != nil {
			t.Fatalf("delA tool %q: %v", reqID, err)
		}
	}
	for i, authID := range []string{fmt.Sprintf("pay:%s:1", grantB.DelegationID), "auth-1"} {
		if _, err := svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, delA, PaymentAuthRequest{
			AuthID: authID, DelegationID: grantA.DelegationID, AgentID: agA.AgentID, DelegateAddress: delA.Address(),
			Payee: payee.Address(), AmountAtomic: 100, Asset: "USDC", TaskRef: "t", Nonce: uint64(i + 1), Timestamp: now,
		})); err != nil {
			t.Fatalf("delA pay %q: %v", authID, err)
		}
	}

	// Delegate B's nonce-1 tool call & payment auth (with identical caller-local IDs "req-1"/"auth-1") MUST succeed!
	if _, err := svc.Gate.Tools.Execute(signToolReq(t, domain, delB, ToolActionRequest{
		RequestID: "req-1", DelegationID: grantB.DelegationID, AgentID: agB.AgentID, DelegateAddress: delB.Address(),
		Tool: "sandbox.read", Resource: "sandbox://b/f", InputHash: "0x01", Nonce: 1, Timestamp: now,
	}), nil); err != nil {
		t.Fatalf("delB tool was falsely blocked by delA: %v", err)
	}
	if _, err := svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, delB, PaymentAuthRequest{
		AuthID: "auth-1", DelegationID: grantB.DelegationID, AgentID: agB.AgentID, DelegateAddress: delB.Address(),
		Payee: payee.Address(), AmountAtomic: 100, Asset: "USDC", TaskRef: "t", Nonce: 1, Timestamp: now,
	})); err != nil {
		t.Fatalf("delB payment was falsely blocked by delA: %v", err)
	}
}

func TestPendingRevocationScopedToSigningAgent(t *testing.T) {
	domain := DefaultDomain()
	now := int64(1_760_000_000)
	reg := NewIdentityRegistry(domain, func() int64 { return now })

	victimOwner, _ := GenerateKeyPair()
	victimCtrl, _ := GenerateKeyPair()
	victimDel, _ := GenerateKeyPair()
	attackerOwner, _ := GenerateKeyPair()
	attackerCtrl, _ := GenerateKeyPair()

	victimAg, _, err := reg.RegisterAgent(signAgent(t, domain, victimOwner, AgentIdentity{
		OwnerAddress: victimOwner.Address(), ControllerPubKey: victimCtrl.CompressedPubKeyHex(), MetadataURI: "ipfs://victim", Capabilities: []string{"r"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	attackerAg, _, err := reg.RegisterAgent(signAgent(t, domain, attackerOwner, AgentIdentity{
		OwnerAddress: attackerOwner.Address(), ControllerPubKey: attackerCtrl.CompressedPubKeyHex(), MetadataURI: "ipfs://attacker", Capabilities: []string{"r"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	// Victim signs grant off-chain before registration.
	victimGrant := signDelegation(t, domain, victimOwner, DelegationGrant{
		AgentID: victimAg.AgentID, OwnerAddress: victimOwner.Address(), DelegateAddress: victimDel.Address(), DelegatePubKey: victimDel.CompressedPubKeyHex(),
		TaskScope: "victim-task", AllowedTools: []string{"sandbox.read"}, AllowedResources: []string{"sandbox://v/*"},
		MaxSpendAtomic: 10000, MaxPerCallAtomic: 5000, Asset: "USDC", IssuedAt: now, ExpiresAt: now + 3600, Nonce: 1,
	})

	// Attacker tries to pre-revoke victimGrant.DelegationID using their own agent's signature!
	atkSig, err := attackerOwner.SignDigest(RevocationDigest(domain, attackerAg.AgentID, victimGrant.DelegationID, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.RevokeDelegation(attackerAg.AgentID, victimGrant.DelegationID, atkSig, 1); err != nil {
		t.Fatalf("attacker self-agent revocation setup: %v", err)
	}

	// Victim MUST still be able to register and use victimGrant!
	if _, err := reg.RegisterDelegation(victimGrant); err != nil {
		t.Fatalf("attacker pre-revocation blocked victim RegisterDelegation: %v", err)
	}
	if revoked, reason := reg.IsRevoked(victimGrant.DelegationID, victimAg.AgentID, victimGrant.RevocationEpoch); revoked {
		t.Fatalf("victim grant falsely marked revoked by attacker: %s", reason)
	}

	// Victim's own pre-revocation for a leaked grant on victimAg MUST still block registration!
	leakedGrant := signDelegation(t, domain, victimOwner, DelegationGrant{
		AgentID: victimAg.AgentID, OwnerAddress: victimOwner.Address(), DelegateAddress: victimDel.Address(), DelegatePubKey: victimDel.CompressedPubKeyHex(),
		TaskScope: "leaked", AllowedTools: []string{"sandbox.read"}, AllowedResources: []string{"sandbox://v/*"},
		MaxSpendAtomic: 10000, MaxPerCallAtomic: 5000, Asset: "USDC", IssuedAt: now, ExpiresAt: now + 3600, Nonce: 2,
	})
	vicRevSig, _ := victimOwner.SignDigest(RevocationDigest(domain, victimAg.AgentID, leakedGrant.DelegationID, 1))
	if _, err := reg.RevokeDelegation(victimAg.AgentID, leakedGrant.DelegationID, vicRevSig, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.RegisterDelegation(leakedGrant); !errors.Is(err, ErrDelegationRevoked) {
		t.Fatalf("expected own pre-revocation to block leakedGrant with ErrDelegationRevoked, got %v", err)
	}
}
