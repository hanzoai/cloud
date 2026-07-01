// Package tenant resolves, deterministically and without any coordinator, which
// replica of the unified cloud binary OWNS the single writer for a tenant's
// per-tenant SQLite (HIP-0302). Every replica computes the SAME answer from the
// SAME membership set, so there is no election, no lock service, no discovery.
//
// This is the load-bearing primitive of the horizontally-scalable OSS cloud:
// N stateless copies of the binary behind one load balancer, each able to say
// "who writes tenant T?" identically. The owner holds T's DB open (SQLite WAL =
// 1 writer + N readers) and streams the WAL to SeaweedFS/S3 (HIP-0107); every
// other replica serves reads from the S3-synced copy or forwards strong writes
// to the owner.
//
// Ownership uses Rendezvous (Highest-Random-Weight) hashing, not a hash ring:
// HRW gives minimal reshuffling on membership change (only the tenants owned by
// a departed replica move, and they spread evenly over the survivors) with no
// virtual-node bookkeeping — a pure function of (tenant, members).
package tenant

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// Member is one replica in the live membership set. ID must be stable for the
// life of the replica (e.g. the pod name or a persistent node id) — HRW weights
// are derived from it, so a changing ID would reshuffle ownership needlessly.
type Member struct {
	ID   string // stable replica identity (pod name / node id)
	Addr string // reachable address for write-forwarding (host:port)
}

// Owner returns the replica that owns the writer for tenantID, or ok=false when
// members is empty. Deterministic: same (tenantID, members) → same Owner on
// every replica, regardless of member order.
func Owner(tenantID string, members []Member) (Member, bool) {
	if len(members) == 0 {
		return Member{}, false
	}
	best := members[0]
	bestScore := weight(tenantID, best.ID)
	for _, m := range members[1:] {
		s := weight(tenantID, m.ID)
		// Tie-break on ID so the result is independent of slice order; ties are
		// astronomically unlikely with a 64-bit score but must still be stable.
		if s > bestScore || (s == bestScore && m.ID < best.ID) {
			best, bestScore = m, s
		}
	}
	return best, true
}

// IsOwner reports whether selfID owns the writer for tenantID under members.
// The hot-path check every replica runs per write request.
func IsOwner(tenantID, selfID string, members []Member) bool {
	o, ok := Owner(tenantID, members)
	return ok && o.ID == selfID
}

// Replicas returns the top-n members for tenantID by descending HRW weight —
// the owner first, then the ordered failover successors. On owner loss, the
// next replica in this list becomes owner with no recomputation of the rest,
// so read replicas can pre-warm the S3 copy for their likely-next tenants.
func Replicas(tenantID string, members []Member, n int) []Member {
	if n <= 0 || len(members) == 0 {
		return nil
	}
	type scored struct {
		m Member
		s uint64
	}
	all := make([]scored, len(members))
	for i, m := range members {
		all[i] = scored{m, weight(tenantID, m.ID)}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s > all[j].s
		}
		return all[i].m.ID < all[j].m.ID
	})
	if n > len(all) {
		n = len(all)
	}
	out := make([]Member, n)
	for i := 0; i < n; i++ {
		out[i] = all[i].m
	}
	return out
}

// weight is the HRW score for (tenant, member): the first 8 bytes of
// SHA-256(tenantID || 0x00 || memberID) as a big-endian uint64. The NUL
// separator prevents ("ab","c") and ("a","bc") from colliding. SHA-256 gives a
// uniform, stable distribution across arbitrary tenant/member strings.
func weight(tenantID, memberID string) uint64 {
	h := sha256.New()
	_, _ = h.Write([]byte(tenantID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(memberID))
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}
