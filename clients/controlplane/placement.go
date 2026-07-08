//go:build controlplane

package controlplane

import (
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// PlacementKind enumerates the control-plane mutations a block can carry.
type PlacementKind uint8

const (
	// OpAssignLease grants Resource to Holder under LeaseID. Idempotent
	// re-grant of an identical lease is allowed; granting over a different
	// live holder is refused by the invariant check.
	OpAssignLease PlacementKind = iota + 1
	// OpReleaseLease releases Holder's lease on Resource. Required before a
	// live shard writer can be reassigned.
	OpReleaseLease
	// OpReassignShardWriter moves the write lease on a secret shard from From
	// to To. The security-critical op: valid only with a matching release of
	// From in the same block, or a proven-dead From (policy.go).
	OpReassignShardWriter
	// OpSetMembership adds (Member=true) or removes (Member=false) Node from
	// the control-plane voter set. Removal marks the node proven-dead.
	OpSetMembership
)

func (k PlacementKind) String() string {
	switch k {
	case OpAssignLease:
		return "assign-lease"
	case OpReleaseLease:
		return "release-lease"
	case OpReassignShardWriter:
		return "reassign-shard-writer"
	case OpSetMembership:
		return "set-membership"
	default:
		return "unknown"
	}
}

// PlacementOp is one control-plane mutation. It is a plain value: a block is an
// ordered list of these, and the canonical payload root binds them.
//
// Fields are interpreted per Kind (a small tagged union — three concrete op
// shapes did not justify separate types):
//
//	OpAssignLease         Resource, Holder, LeaseID
//	OpReleaseLease        Resource, Holder, LeaseID
//	OpReassignShardWriter Resource(=shard), From, To
//	OpSetMembership       Node, Member
type PlacementOp struct {
	Kind     PlacementKind
	Resource string // resource / shard identifier
	Holder   string // lease holder (assign/release)
	LeaseID  string // lease identity (assign/release)
	From     string // current writer (reassign)
	To       string // new writer (reassign)
	Node     string // membership subject
	Member   bool   // membership target state
}

// canonicalBytes is the injective, order-preserving encoding of an op used for
// the payload root. Every field is length-prefixed so no two distinct ops
// encode to the same bytes.
func (o PlacementOp) canonicalBytes() []byte {
	var b []byte
	b = append(b, byte(o.Kind))
	for _, s := range []string{o.Resource, o.Holder, o.LeaseID, o.From, o.To, o.Node} {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(s)))
		b = append(b, l[:]...)
		b = append(b, s...)
	}
	if o.Member {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	return b
}

// PlacementBlock is the candidate the proposer broadcasts. Height + Proposer +
// ParentRoot + the ordered ops fully determine the payload root, hence the
// round digest. Two blocks that differ in any op differ in their digest and so
// cannot cross-replay a certificate.
type PlacementBlock struct {
	Height     uint64
	Round      uint32 // ceremony round/view (bumped on proposer rotation)
	Proposer   string
	ParentRoot [32]byte
	Ops        []PlacementOp
}

// PayloadRoot is the 48-byte canonical commitment to the block's ops, fed to
// quasar.ComputeRoundDigest. Deterministic and injective over the op list.
func (b *PlacementBlock) PayloadRoot() [48]byte {
	h := sha512.New384()
	var u64 [8]byte
	binary.BigEndian.PutUint64(u64[:], b.Height)
	h.Write(u64[:])
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], b.Round)
	h.Write(u32[:])
	h.Write([]byte(b.Proposer))
	h.Write(b.ParentRoot[:])
	binary.BigEndian.PutUint32(u32[:], uint32(len(b.Ops)))
	h.Write(u32[:])
	for _, op := range b.Ops {
		ob := op.canonicalBytes()
		binary.BigEndian.PutUint32(u32[:], uint32(len(ob)))
		h.Write(u32[:])
		h.Write(ob)
	}
	var out [48]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ----------------------------------------------------------------------------
// Placement RSM — the replicated state machine each voter applies certified
// blocks into, in Finalized() order, deterministically.
// ----------------------------------------------------------------------------

// ControlDB is the persistence seam for the RSM (the control.db backend).
// Stubbed in-memory here; the real backend is a later increment. Writes are
// whole-state snapshots keyed by height so recovery is point-in-time.
type ControlDB interface {
	// Persist records the state produced by applying up to (and including)
	// height. Returns an error the RSM surfaces without applying in memory —
	// fail-secure: a persistence failure halts apply rather than diverging
	// memory from disk.
	Persist(height uint64, snapshot []byte) error
	// LastHeight is the highest persisted height (0 if empty).
	LastHeight() (uint64, error)
}

// Lease is a resource grant.
type Lease struct {
	Holder  string
	LeaseID string
}

// PlacementState is the deterministic control-plane state. All maps are
// applied in canonical op order so every honest voter converges byte-for-byte.
type PlacementState struct {
	Leases      map[string]Lease  // resource -> lease
	ShardWriter map[string]string // shard -> current writer
	Membership  map[string]bool   // node -> is-member
	ProvenDead  map[string]bool   // node -> proven dead
}

// NewPlacementState returns an empty state.
func NewPlacementState() *PlacementState {
	return &PlacementState{
		Leases:      map[string]Lease{},
		ShardWriter: map[string]string{},
		Membership:  map[string]bool{},
		ProvenDead:  map[string]bool{},
	}
}

// Snapshot is the canonical, deterministic serialization of the state — used
// for cross-voter convergence checks and ControlDB persistence.
func (s *PlacementState) Snapshot() []byte {
	h := sha512.New()
	writeKV := func(tag string, kv map[string]string) {
		h.Write([]byte(tag))
		keys := make([]string, 0, len(kv))
		for k := range kv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			h.Write([]byte(k))
			h.Write([]byte{0})
			h.Write([]byte(kv[k]))
			h.Write([]byte{0})
		}
	}
	leaseFlat := map[string]string{}
	for r, l := range s.Leases {
		leaseFlat[r] = l.Holder + "\x1f" + l.LeaseID
	}
	writeKV("leases", leaseFlat)
	writeKV("shardwriter", s.ShardWriter)
	boolFlat := func(m map[string]bool) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			if v {
				out[k] = "1"
			} else {
				out[k] = "0"
			}
		}
		return out
	}
	writeKV("membership", boolFlat(s.Membership))
	writeKV("provendead", boolFlat(s.ProvenDead))
	return h.Sum(nil)
}

// clone returns a deep copy so apply is transactional: an invalid op leaves the
// prior state untouched (fail-secure — a partially-applied block never lands).
func (s *PlacementState) clone() *PlacementState {
	c := NewPlacementState()
	for k, v := range s.Leases {
		c.Leases[k] = v
	}
	for k, v := range s.ShardWriter {
		c.ShardWriter[k] = v
	}
	for k, v := range s.Membership {
		c.Membership[k] = v
	}
	for k, v := range s.ProvenDead {
		c.ProvenDead[k] = v
	}
	return c
}

var errNilBlock = errors.New("controlplane: nil block")

// apply mutates the state by the block's ops after the invariant gate passes.
// Ops apply in list order; the invariant gate (CheckInvariants) has already
// verified the whole block against the pre-state, so mutation cannot fail here.
func (s *PlacementState) apply(b *PlacementBlock) {
	for _, op := range b.Ops {
		switch op.Kind {
		case OpAssignLease:
			s.Leases[op.Resource] = Lease{Holder: op.Holder, LeaseID: op.LeaseID}
			// A shard's write lease also records the writer.
			s.ShardWriter[op.Resource] = op.Holder
		case OpReleaseLease:
			delete(s.Leases, op.Resource)
			if s.ShardWriter[op.Resource] == op.Holder {
				delete(s.ShardWriter, op.Resource)
			}
		case OpReassignShardWriter:
			s.ShardWriter[op.Resource] = op.To
			s.Leases[op.Resource] = Lease{Holder: op.To, LeaseID: op.To + ":writer"}
		case OpSetMembership:
			s.Membership[op.Node] = op.Member
			if !op.Member {
				s.ProvenDead[op.Node] = true
			} else {
				delete(s.ProvenDead, op.Node)
			}
		}
	}
}

// ----------------------------------------------------------------------------
// In-memory ControlDB stub.
// ----------------------------------------------------------------------------

type memControlDB struct {
	last uint64
	byH  map[uint64][]byte
}

// NewMemControlDB returns an in-memory ControlDB stub for the harness.
func NewMemControlDB() ControlDB {
	return &memControlDB{byH: map[uint64][]byte{}}
}

func (m *memControlDB) Persist(height uint64, snapshot []byte) error {
	if height <= m.last && m.last != 0 {
		return fmt.Errorf("controlplane: non-monotonic persist height %d <= %d", height, m.last)
	}
	cp := make([]byte, len(snapshot))
	copy(cp, snapshot)
	m.byH[height] = cp
	m.last = height
	return nil
}

func (m *memControlDB) LastHeight() (uint64, error) { return m.last, nil }
