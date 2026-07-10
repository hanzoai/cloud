//go:build controlplane

package controlplane

// cert_test.go — standalone crypto tests for the seam (c) real certificate.
// These exercise ComposeControlPlaneCert / VerifyControlPlaneCert directly
// (independent of the ceremony wiring) and are the acceptance suite for the
// independent-sig weighted-quorum cert: a real cert verifies under policy; a
// cert missing a leg, below quorum, or carrying a forged / rogue / wrong-context
// signature is REJECTED with the exact typed error.

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"github.com/luxfi/consensus/protocol/quasar"
	"github.com/luxfi/crypto/mldsa"
	pulsar "github.com/luxfi/pulsar/pkg/pulsar"
)

// certTestPods mints n pods each with a fresh ML-DSA-65 identity keypair,
// returning the public key set, the private keys, and the node order.
func certTestPods(t *testing.T, n int) (map[pulsar.NodeID]*mldsa.PublicKey, map[pulsar.NodeID]*mldsa.PrivateKey, []pulsar.NodeID) {
	t.Helper()
	keys := map[pulsar.NodeID]*mldsa.PublicKey{}
	privs := map[pulsar.NodeID]*mldsa.PrivateKey{}
	order := make([]pulsar.NodeID, 0, n)
	for i := 0; i < n; i++ {
		node := NodeIDFromName(fmt.Sprintf("cloud-%d", i))
		sk, err := mldsa.GenerateKey(rand.Reader, mldsa.MLDSA65)
		if err != nil {
			t.Fatalf("keygen %d: %v", i, err)
		}
		keys[node] = sk.PublicKey
		privs[node] = sk
		order = append(order, node)
	}
	return keys, privs, order
}

func certTestPosition() certPosition {
	return certPosition{
		NetworkID:  0x48414e5a, // "HANZ"
		ChainID:    0x43504c4e, // "CPLN"
		Epoch:      1,
		Height:     7,
		Round:      0,
		BlockHash:  hash32("cp-cert-test-block", []byte("shard-X->cloud-1")),
		ParentRoot: hash32("cp-cert-test-parent", []byte("genesis")),
	}
}

// signQuorum has the first k pods each independently sign the canonical cert
// message under the DISTINCT cert context.
func signQuorum(t *testing.T, pos certPosition, keys map[pulsar.NodeID]*mldsa.PublicKey, privs map[pulsar.NodeID]*mldsa.PrivateKey, order []pulsar.NodeID, quorumWeight uint64, k int) map[pulsar.NodeID][]byte {
	t.Helper()
	msg, err := CertSigningMessage(pos, keys, quorumWeight)
	if err != nil {
		t.Fatalf("cert message: %v", err)
	}
	sigs := map[pulsar.NodeID][]byte{}
	for i := 0; i < k; i++ {
		node := order[i]
		sig, err := privs[node].SignCtxDeterministic(msg, certContext)
		if err != nil {
			t.Fatalf("sign %d: %v", i, err)
		}
		sigs[node] = sig
	}
	return sigs
}

// TestControlPlaneCert_VerifiesUnderPolicy — the happy path: a 4-of-5 quorum of
// independent ML-DSA-65 signatures composes a real ConsensusCert that
// VerifyConsensusCert accepts under the control-plane weighted-sig-set policy.
func TestControlPlaneCert_VerifiesUnderPolicy(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 4)

	cert, err := ComposeControlPlaneCert(pos, keys, sigs, quorum)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if err := VerifyControlPlaneCert(pos, keys, quorum, cert); err != nil {
		t.Fatalf("verify a real quorum cert: %v", err)
	}
	// The cert routes through the shipped Gen-3 verifier — confirm it is a
	// weighted-sig-set leg, not a structural pass.
	if len(cert.Evidence) != 1 || cert.Evidence[0].Mode != quasar.EvidenceWeightedSigSet {
		t.Fatalf("cert is not a single weighted-sig-set leg: %+v", cert.Evidence)
	}
}

// TestControlPlaneCert_FullQuorumVerifies — all 5 sign; still verifies (weight
// above the floor).
func TestControlPlaneCert_FullQuorumVerifies(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 5)
	cert, err := ComposeControlPlaneCert(pos, keys, sigs, quorum)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if err := VerifyControlPlaneCert(pos, keys, quorum, cert); err != nil {
		t.Fatalf("verify full quorum: %v", err)
	}
}

// TestControlPlaneCert_Deterministic — two honest composers over the same inputs
// produce the byte-identical evidence payload (deterministic ML-DSA + permissionless
// assembly), so every voter converges on the same cert.
func TestControlPlaneCert_Deterministic(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 4)
	a, err := ComposeControlPlaneCert(pos, keys, sigs, quorum)
	if err != nil {
		t.Fatalf("compose a: %v", err)
	}
	b, err := ComposeControlPlaneCert(pos, keys, sigs, quorum)
	if err != nil {
		t.Fatalf("compose b: %v", err)
	}
	if !bytes.Equal(a.Evidence[0].Payload, b.Evidence[0].Payload) {
		t.Fatal("honest composers diverged on the cert payload (non-deterministic)")
	}
	if a.RequiredLegsRoot != b.RequiredLegsRoot || a.ValidatorSetRoot != b.ValidatorSetRoot {
		t.Fatal("honest composers diverged on the cert header roots")
	}
}

// TestControlPlaneCert_BelowQuorumRejected — fewer than quorum signatures cannot
// compose a cert (fail-closed at assembly).
func TestControlPlaneCert_BelowQuorumRejected(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 3) // only 3
	if _, err := ComposeControlPlaneCert(pos, keys, sigs, quorum); !errors.Is(err, ErrInsufficientCertSigners) {
		t.Fatalf("below quorum: want ErrInsufficientCertSigners, got %v", err)
	}
}

// TestControlPlaneCert_MissingLegRejected — a cert with the required PQ leg
// stripped is rejected by the envelope (I5).
func TestControlPlaneCert_MissingLegRejected(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 4)
	cert, err := ComposeControlPlaneCert(pos, keys, sigs, quorum)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	cert.Evidence = nil // strip the required leg
	if err := VerifyControlPlaneCert(pos, keys, quorum, cert); !errors.Is(err, quasar.ErrMissingRequiredLeg) {
		t.Fatalf("missing leg: want ErrMissingRequiredLeg, got %v", err)
	}
}

// TestControlPlaneCert_ForgedSigRejected — a correctly-formed signature under an
// ATTACKER key, attributed to a legitimate validator, is rejected by the stock
// FIPS-204 verify inside the weighted-sig-set predicate.
func TestControlPlaneCert_ForgedSigRejected(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 4)

	msg, err := CertSigningMessage(pos, keys, quorum)
	if err != nil {
		t.Fatalf("cert message: %v", err)
	}
	attacker, err := mldsa.GenerateKey(rand.Reader, mldsa.MLDSA65)
	if err != nil {
		t.Fatalf("attacker keygen: %v", err)
	}
	badSig, err := attacker.SignCtxDeterministic(msg, certContext)
	if err != nil {
		t.Fatalf("attacker sign: %v", err)
	}
	sigs[order[0]] = badSig // legit validator id, attacker signature

	cert, err := ComposeControlPlaneCert(pos, keys, sigs, quorum) // assembly does not verify sigs
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if err := VerifyControlPlaneCert(pos, keys, quorum, cert); !errors.Is(err, quasar.ErrQCSigInvalid) {
		t.Fatalf("forged sig: want ErrQCSigInvalid, got %v", err)
	}
}

// TestControlPlaneCert_RogueSignerRejected — a signature from a node absent from
// the validator key set is refused at assembly (never reaches the cert).
func TestControlPlaneCert_RogueSignerRejected(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 4)

	msg, _ := CertSigningMessage(pos, keys, quorum)
	rogue := NodeIDFromName("attacker-pod")
	rogueKey, err := mldsa.GenerateKey(rand.Reader, mldsa.MLDSA65)
	if err != nil {
		t.Fatalf("rogue keygen: %v", err)
	}
	rsig, _ := rogueKey.SignCtxDeterministic(msg, certContext)
	sigs[rogue] = rsig // a node not in the registered set

	if _, err := ComposeControlPlaneCert(pos, keys, sigs, quorum); !errors.Is(err, ErrSignerNotInSet) {
		t.Fatalf("rogue signer: want ErrSignerNotInSet, got %v", err)
	}
}

// TestControlPlaneCert_WrongPositionRejected — a valid cert for one block is
// rejected when a verifier expects a different block/height (cross-position
// binding; ErrCertPositionMismatch).
func TestControlPlaneCert_WrongPositionRejected(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 4)
	cert, err := ComposeControlPlaneCert(pos, keys, sigs, quorum)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	other := pos
	other.Height = pos.Height + 1
	if err := VerifyControlPlaneCert(other, keys, quorum, cert); !errors.Is(err, ErrCertPositionMismatch) {
		t.Fatalf("wrong position: want ErrCertPositionMismatch, got %v", err)
	}
}

// TestControlPlaneCert_WrongValidatorSetRejected — a cert built for one key set
// is rejected against a different key set (validator-set root pinned by the
// verifier, I3).
func TestControlPlaneCert_WrongValidatorSetRejected(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4
	sigs := signQuorum(t, pos, keys, privs, order, quorum, 4)
	cert, err := ComposeControlPlaneCert(pos, keys, sigs, quorum)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	otherKeys, _, _ := certTestPods(t, 5) // same names, different keys → different set root
	if err := VerifyControlPlaneCert(pos, otherKeys, quorum, cert); !errors.Is(err, quasar.ErrValidatorSetRootMismatch) {
		t.Fatalf("wrong validator set: want ErrValidatorSetRootMismatch, got %v", err)
	}
}

// TestControlPlaneCert_PopContextSigRejected — proves RED finding R4: a signature
// produced under the PoP context (popContext) is NOT accepted as a cert
// signature, because cert signing uses a DISTINCT FIPS-204 context. This is the
// cross-protocol-confusion defence between the identity PoP and the cert leg.
func TestControlPlaneCert_PopContextSigRejected(t *testing.T) {
	keys, privs, order := certTestPods(t, 5)
	pos := certTestPosition()
	const quorum uint64 = 4

	msg, err := CertSigningMessage(pos, keys, quorum)
	if err != nil {
		t.Fatalf("cert message: %v", err)
	}
	if bytes.Equal(certContext, popContext) {
		t.Fatal("R4 VIOLATED: cert context must differ from the PoP context")
	}
	sigs := map[pulsar.NodeID][]byte{}
	for i := 0; i < 4; i++ {
		node := order[i]
		// Sign the correct message but under the WRONG (PoP) context.
		sig, err := privs[node].SignCtxDeterministic(msg, popContext)
		if err != nil {
			t.Fatalf("sign %d: %v", i, err)
		}
		sigs[node] = sig
	}
	cert, err := ComposeControlPlaneCert(pos, keys, sigs, quorum)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if err := VerifyControlPlaneCert(pos, keys, quorum, cert); !errors.Is(err, quasar.ErrQCSigInvalid) {
		t.Fatalf("pop-context sig accepted as cert sig (R4 broken): want ErrQCSigInvalid, got %v", err)
	}
}
