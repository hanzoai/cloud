//go:build controlplane

package controlplane

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/luxfi/crypto/mldsa"
	pulsar "github.com/luxfi/pulsar/pkg/pulsar"
)

// rekeyContext is the FIPS 204 §5.2 domain-separation context for a key-rotation
// authorization signature. DISTINCT from popContext and certContext so a
// rotation authorization can never be cross-protocol-confused with a leg PoP or
// a cert signature.
var rekeyContext = []byte("hanzo/controlplane/rekey/v1")

// Share is a sealed per-pod signing share. Exactly one is issued per pod.
//
// The Index is the pod's 1-based party index. secret is the stub z-share source
// (still seed-derived; replaced by KMS-sealed custody in seam b + the real cert
// material in seam c). idKey is the pod's ML-DSA-65 identity keypair — a genuine
// asymmetric secret, NOT derived from any public value — used to prove
// possession on every leg it emits. In the real ShareCustody the secret key is
// KMS-sealed and the signer runs inside the custody boundary; here it is
// in-memory but never seed-derived (seam a).
type Share struct {
	Index  uint32
	Node   pulsar.NodeID
	secret [32]byte          // stub z-share source (seed-derived; seam c replaces it)
	idKey  *mldsa.PrivateKey // per-pod ML-DSA-65 identity key (seam a); becomes the cert-signing key (seam c)
}

// ShareCustody yields exactly ONE share for ONE pod. This is where "one pod =
// one voter = one share" is rooted: a custody handle is 1:1 with a pod and
// exposes a single share. A pod cannot obtain a second share from its custody.
type ShareCustody interface {
	// Open returns this pod's single share. Idempotent: repeated calls return
	// the identical share.
	Open() Share
	// Node is this pod's stable identity.
	Node() pulsar.NodeID
}

// memCustody is the in-memory ShareCustody stub. Real impl: KMS-sealed.
type memCustody struct {
	share Share
}

// NodeIDFromName derives a stable node id from a human-readable pod name.
func NodeIDFromName(name string) pulsar.NodeID {
	var id pulsar.NodeID
	sum := sha256.Sum256([]byte("cp-node:" + name))
	copy(id[:], sum[:])
	return id
}

// NewMemCustody issues a single sealed share for pod `name` at party `index`.
// The z-share `secret` is still derived from the cluster seed (stub threshold
// material, replaced in seam c). The identity key is a FRESH, RANDOM ML-DSA-65
// keypair (crypto/rand) — NEVER derived from the public seed — so no party's
// proof-of-possession key can be reconstructed from public inputs. That single
// property is the keystone of seam (a): it is exactly what makes the
// byzantine-safety suite meaningful (TestRed_B_* / TestSafety_RogueAndForgedLegs).
//
// Panics if key generation fails: an unusable RNG is a fatal harness/host
// condition, never adversarial input.
func NewMemCustody(seed []byte, name string, index uint32) ShareCustody {
	node := NodeIDFromName(name)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], index)
	secret := sha256.Sum256(append(append([]byte("cp-share:"), seed...), idx[:]...))
	idKey, err := mldsa.GenerateKey(rand.Reader, mldsa.MLDSA65)
	if err != nil {
		panic("controlplane: ML-DSA-65 identity keygen failed: " + err.Error())
	}
	return &memCustody{share: Share{Index: index, Node: node, secret: secret, idKey: idKey}}
}

func (m *memCustody) Open() Share         { return m.share }
func (m *memCustody) Node() pulsar.NodeID { return m.share.Node }

// ----------------------------------------------------------------------------
// Validator registry — the membership + identity source of truth the driver
// consults to enforce one-pod-one-share and proof-of-possession.
// ----------------------------------------------------------------------------

var (
	// ErrShareIndexTaken is returned when a party index is already bound to a
	// different node — a second pod claiming an occupied share slot.
	ErrShareIndexTaken = errors.New("controlplane: party index already bound to another node")
	// ErrNodeHasShare is the one-pod-two-shares guard: a node already holds a
	// share and may not register a second.
	ErrNodeHasShare = errors.New("controlplane: node already holds a share (one pod = one share)")
	// errMissingIdentityKey rejects a share with no ML-DSA identity keypair — a
	// node with no verifiable identity could neither authenticate its own legs
	// nor have them attributed, so it must never enter the registry.
	errMissingIdentityKey = errors.New("controlplane: share has no ML-DSA identity key")
	// ErrIdentityKeyConflict is the FIRST-WRITER-WINS guard (RED R1): a node
	// already bound to one ML-DSA identity key may NOT be re-registered under a
	// DIFFERENT key. A silent overwrite would be a rogue-key swap — an attacker
	// re-registering a live validator with its own key would then have its forged
	// legs and cert signatures accepted, since the whole cert's rogue-key
	// resistance rests on idVerifier being the trusted key source. Idempotent
	// re-registration of the IDENTICAL key is allowed; a sanctioned rotation goes
	// through Rekey (self-authorized under the current key).
	ErrIdentityKeyConflict = errors.New("controlplane: node already bound to a different identity key (first-writer-wins; rotate via an authorized Rekey)")
	// ErrRekeyUnknownNode rejects a rotation of a node with no registered key.
	ErrRekeyUnknownNode = errors.New("controlplane: rekey of a node with no registered identity key")
	// ErrRekeyUnauthorized rejects a rotation whose authorization signature does
	// not verify under the node's CURRENT registered key.
	ErrRekeyUnauthorized = errors.New("controlplane: rekey authorization does not verify under the current identity key")
)

// ValidatorRegistry pins the bijection party-index <-> node and holds each
// node's registered identity credential. It is the structural enforcement of
// share isolation: registration is 1:1 in both directions, so no node can hold
// two shares and no share can be held by two nodes.
type ValidatorRegistry struct {
	byIndex    map[uint32]pulsar.NodeID
	byNode     map[pulsar.NodeID]uint32
	idVerifier map[pulsar.NodeID]*mldsa.PublicKey // registered ML-DSA-65 identity public key
}

// NewValidatorRegistry returns an empty registry.
func NewValidatorRegistry() *ValidatorRegistry {
	return &ValidatorRegistry{
		byIndex:    map[uint32]pulsar.NodeID{},
		byNode:     map[pulsar.NodeID]uint32{},
		idVerifier: map[pulsar.NodeID]*mldsa.PublicKey{},
	}
}

// Register binds a share's (index, node, identity) into the registry, refusing
// any violation of the 1:1 invariant. The registered credential is the pod's
// ML-DSA-65 identity PUBLIC key; verification is asymmetric, so a peer that does
// not hold the matching secret key cannot forge a leg attributed to this node.
func (r *ValidatorRegistry) Register(s Share) error {
	if s.idKey == nil || s.idKey.PublicKey == nil {
		return errMissingIdentityKey
	}
	if existing, ok := r.byIndex[s.Index]; ok && existing != s.Node {
		return fmt.Errorf("%w: index %d", ErrShareIndexTaken, s.Index)
	}
	if idx, ok := r.byNode[s.Node]; ok && idx != s.Index {
		return fmt.Errorf("%w: node holds index %d, tried %d", ErrNodeHasShare, idx, s.Index)
	}
	// First-writer-wins on the identity key (RED R1): a node already bound to a
	// key may not be silently re-bound to a DIFFERENT one. Idempotent
	// re-registration of the identical key is fine (crash-restart / re-sync).
	if cur, ok := r.idVerifier[s.Node]; ok && !pubKeyEqual(cur, s.idKey.PublicKey) {
		return fmt.Errorf("%w: node %x", ErrIdentityKeyConflict, s.Node[:8])
	}
	r.byIndex[s.Index] = s.Node
	r.byNode[s.Node] = s.Index
	r.idVerifier[s.Node] = s.idKey.PublicKey
	return nil
}

// rekeyTBS is the to-be-signed preimage authorizing a key rotation: bound to the
// node and the NEW public key. Only the holder of the node's CURRENT secret key
// can produce a signature over it, so a rotation is self-authorized.
func rekeyTBS(node pulsar.NodeID, newPub *mldsa.PublicKey) []byte {
	h := sha256.New()
	h.Write([]byte("cp-rekey-tbs-v1"))
	h.Write(node[:])
	h.Write(newPub.Bytes())
	return h.Sum(nil)
}

// Rekey rotates a bound node's identity key to newPub, authorized by authSig — a
// signature under the node's CURRENT registered key over rekeyTBS(node, newPub).
// This is the ONLY sanctioned way to change a bound key (Register is
// first-writer-wins); it is the registry action a consensus-ordered
// OpRekeyValidator applies. Self-authorized: an attacker without the current
// secret key cannot forge the authorization, so it cannot rotate a validator's
// key. Refuses an unknown node, a nil key, or an unauthorized signature.
func (r *ValidatorRegistry) Rekey(node pulsar.NodeID, newPub *mldsa.PublicKey, authSig []byte) error {
	if newPub == nil {
		return errMissingIdentityKey
	}
	cur, ok := r.idVerifier[node]
	if !ok {
		return fmt.Errorf("%w: %x", ErrRekeyUnknownNode, node[:8])
	}
	if !cur.VerifySignatureCtx(rekeyTBS(node, newPub), authSig, rekeyContext) {
		return ErrRekeyUnauthorized
	}
	r.idVerifier[node] = newPub
	return nil
}

// pubKeyEqual reports whether two ML-DSA public keys are byte-identical.
func pubKeyEqual(a, b *mldsa.PublicKey) bool {
	if a == nil || b == nil {
		return a == b
	}
	return bytes.Equal(a.Bytes(), b.Bytes())
}

// Size is the number of registered voters.
func (r *ValidatorRegistry) Size() int { return len(r.byNode) }

// IndexOf returns the party index registered for a node.
func (r *ValidatorRegistry) IndexOf(node pulsar.NodeID) (uint32, bool) {
	idx, ok := r.byNode[node]
	return idx, ok
}

// NodeOf returns the node registered for a party index.
func (r *ValidatorRegistry) NodeOf(index uint32) (pulsar.NodeID, bool) {
	n, ok := r.byIndex[index]
	return n, ok
}

// partialTBS is the to-be-signed preimage a leg's author binds its identity
// signature over — session, nonce, party, and the revealed share. Binding the
// share means the identity signature cannot be lifted onto a different leg.
func partialTBS(p pulsar.Partial) []byte {
	h := sha256.New()
	h.Write([]byte("cp-partial-pop"))
	h.Write(p.SessionID[:])
	h.Write(p.NonceID[:])
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], p.PartyID)
	h.Write(idx[:])
	h.Write(p.ZShare)
	return h.Sum(nil)
}

// round1TBS is the to-be-signed preimage for a Round1 commitment's proof-of-
// possession: session, nonce, party, and the z-share commitment. Binding the
// commitment to the sender's party means a node cannot stamp another node's
// identity onto a forged commitment to inflate the anti-rush barrier.
func round1TBS(sid, nid [32]byte, partyID uint32, commit []byte) []byte {
	h := sha256.New()
	h.Write([]byte("cp-round1-pop"))
	h.Write(sid[:])
	h.Write(nid[:])
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], partyID)
	h.Write(idx[:])
	h.Write(commit)
	return h.Sum(nil)
}

// popContext is the FIPS 204 §5.2 domain-separation context bound into every
// control-plane proof-of-possession signature, preventing a leg's ML-DSA
// signature from being replayed into any other ML-DSA context. The per-leg TBS
// preimage (round1TBS / partialTBS) already separates Round1 commitments from
// Round2 legs by tag, so one context suffices.
var popContext = []byte("hanzo/controlplane/pop/v1")

// signPoP produces a proof-of-possession over a leg using the pod's ML-DSA-65
// identity private key, via the FIPS 204 §5.2 DETERMINISTIC variant: the
// signature is a pure function of (sk, tbs, context), so honest voters are
// byte-reproducible and the signing path carries no RNG dependency. The secret
// key never leaves the custody boundary — only the signature is emitted.
func signPoP(idKey *mldsa.PrivateKey, tbs []byte) ([]byte, error) {
	return idKey.SignCtxDeterministic(tbs, popContext)
}

// VerifyPoP checks a leg's proof-of-possession: the author MUST be registered
// (rejecting rogue/unregistered keys) AND the ML-DSA-65 signature MUST verify
// under the registered PUBLIC key with the control-plane context (rejecting
// forgeries and lifted signatures). Verification is asymmetric, so an attacker
// without the node's secret key cannot produce an accepted leg — this is what
// makes the byzantine-safety suite meaningful. Runs before a leg counts toward
// quorum.
func (r *ValidatorRegistry) VerifyPoP(author pulsar.NodeID, tbs, sig []byte) bool {
	pub, ok := r.idVerifier[author]
	if !ok {
		return false // rogue / unregistered key
	}
	return pub.VerifySignatureCtx(tbs, sig, popContext)
}
