//go:build controlplane

package controlplane

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	pulsar "github.com/luxfi/pulsar/pkg/pulsar"
)

// Share is a sealed per-pod signing share. Exactly one is issued per pod.
//
// In the stub the secret is an in-memory 32-byte seed; the real ShareCustody
// seals it in KMS (KMSSecret CRD) and never returns raw bytes — the signer runs
// inside the custody boundary. The Index is the pod's 1-based party index in
// the threshold scheme; idKey is the pod's identity credential used for
// proof-of-possession on every leg it emits.
type Share struct {
	Index  uint32
	Node   pulsar.NodeID
	secret [32]byte // sealed share material (stub: in-memory)
	idKey  [32]byte // identity credential (stub for an ML-DSA identity key)
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

// NewMemCustody issues a single sealed share for pod `name` at party `index`,
// deriving the share secret and identity key from a cluster seed. Distinct
// (seed, index) pairs yield distinct, unlinkable share material.
func NewMemCustody(seed []byte, name string, index uint32) ShareCustody {
	node := NodeIDFromName(name)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], index)
	secret := sha256.Sum256(append(append([]byte("cp-share:"), seed...), idx[:]...))
	idKey := sha256.Sum256(append(append([]byte("cp-idkey:"), seed...), node[:]...))
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
)

// ValidatorRegistry pins the bijection party-index <-> node and holds each
// node's registered identity credential. It is the structural enforcement of
// share isolation: registration is 1:1 in both directions, so no node can hold
// two shares and no share can be held by two nodes.
type ValidatorRegistry struct {
	byIndex    map[uint32]pulsar.NodeID
	byNode     map[pulsar.NodeID]uint32
	idVerifier map[pulsar.NodeID][32]byte // registered identity credential (stub public key)
}

// NewValidatorRegistry returns an empty registry.
func NewValidatorRegistry() *ValidatorRegistry {
	return &ValidatorRegistry{
		byIndex:    map[uint32]pulsar.NodeID{},
		byNode:     map[pulsar.NodeID]uint32{},
		idVerifier: map[pulsar.NodeID][32]byte{},
	}
}

// Register binds a share's (index, node, identity) into the registry, refusing
// any violation of the 1:1 invariant. The identity credential registered is
// the pod's verification material (the stub uses the symmetric idKey; the real
// registry stores an ML-DSA identity public key and verification is asymmetric).
func (r *ValidatorRegistry) Register(s Share) error {
	if existing, ok := r.byIndex[s.Index]; ok && existing != s.Node {
		return fmt.Errorf("%w: index %d", ErrShareIndexTaken, s.Index)
	}
	if idx, ok := r.byNode[s.Node]; ok && idx != s.Index {
		return fmt.Errorf("%w: node holds index %d, tried %d", ErrNodeHasShare, idx, s.Index)
	}
	r.byIndex[s.Index] = s.Node
	r.byNode[s.Node] = s.Index
	r.idVerifier[s.Node] = s.idKey
	return nil
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

// signPoP produces a proof-of-possession over a leg using the pod's identity
// key. Stub: HMAC-SHA256 under idKey. Real: ML-DSA identity signature.
func signPoP(idKey [32]byte, tbs []byte) []byte {
	mac := hmac.New(sha256.New, idKey[:])
	mac.Write(tbs)
	return mac.Sum(nil)
}

// VerifyPoP checks a leg's proof-of-possession: the author MUST be registered
// (rejecting rogue/unregistered keys) AND the signature MUST verify under the
// registered credential (rejecting forgeries and lifted signatures). This runs
// before a leg is counted toward quorum.
func (r *ValidatorRegistry) VerifyPoP(author pulsar.NodeID, tbs, sig []byte) bool {
	cred, ok := r.idVerifier[author]
	if !ok {
		return false // rogue / unregistered key
	}
	want := signPoP(cred, tbs)
	return hmac.Equal(want, sig)
}
