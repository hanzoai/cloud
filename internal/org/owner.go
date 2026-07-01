// Package org places each organization's data on exactly one replica of the
// unified cloud binary, and replicates it, WITHOUT any coordinator.
//
// In Hanzo the ORGANIZATION is the tenant boundary — identity, billing, and
// per-org SQLite (HIP-0302) are all org-scoped, so "tenant" and "org" are the
// same thing; this package speaks only of orgs. It answers one question,
// identically on every replica: "which replica is the single writer for org X's
// databases?" — via Rendezvous (Highest-Random-Weight) hashing over the live
// membership set. No election, no lock service, no service discovery.
//
// Ownership is PER-ORG: the owning replica writes ALL of an org's databases (its
// root db plus every per-project and per-user db), so an org's data has locality
// and intra-org consistency and moves as a single unit on failover. Reads are
// served from any replica's SeaweedFS-synced copy (see Replicator). A single
// mega-org that outgrows one writer is a future sub-shard concern (hash org+shard);
// per-org is the correct, simple default and the natural isolation boundary.
package org

import (
	"crypto/sha256"
	"encoding/binary"
)

// Member is one replica in the live membership set. ID must be stable for the
// life of the replica (e.g. its pod name / a persistent node id): HRW weights
// derive from it, so a changing ID would reshuffle ownership needlessly. Addr is
// the reachable address for write-forwarding (host:port).
type Member struct {
	ID   string
	Addr string
}

// Owner returns the replica that owns the writer for orgID, or ok=false when
// members is empty (fail-closed — never a wrong writer). Deterministic: the same
// (orgID, members) yields the same Owner on every replica, independent of the
// order members are supplied in.
func Owner(orgID string, members []Member) (Member, bool) {
	if len(members) == 0 {
		return Member{}, false
	}
	best := members[0]
	bestScore := weight(orgID, best.ID)
	for _, m := range members[1:] {
		s := weight(orgID, m.ID)
		// Tie-break on ID so the result is order-independent; a 64-bit score tie
		// is astronomically unlikely but must still resolve stably.
		if s > bestScore || (s == bestScore && m.ID < best.ID) {
			best, bestScore = m, s
		}
	}
	return best, true
}

// IsOwner reports whether selfID owns the writer for orgID under members — the
// per-write hot-path check every replica runs.
func IsOwner(orgID, selfID string, members []Member) bool {
	o, ok := Owner(orgID, members)
	return ok && o.ID == selfID
}

// Replicas returns the top-n members for orgID by descending HRW weight: the
// owner first, then the ordered failover successors. On owner loss the next
// replica in this list becomes owner with no recomputation, so read replicas can
// pre-warm the SeaweedFS copy for the orgs they are next-in-line to own.
func Replicas(orgID string, members []Member, n int) []Member {
	if n <= 0 || len(members) == 0 {
		return nil
	}
	type scored struct {
		m Member
		s uint64
	}
	all := make([]scored, len(members))
	for i, m := range members {
		all[i] = scored{m, weight(orgID, m.ID)}
	}
	// Insertion by descending score with stable ID tie-break (small N).
	for i := 1; i < len(all); i++ {
		for j := i; j > 0; j-- {
			a, b := all[j], all[j-1]
			less := a.s > b.s || (a.s == b.s && a.m.ID < b.m.ID)
			if !less {
				break
			}
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	if n > len(all) {
		n = len(all)
	}
	out := make([]Member, n)
	for i := 0; i < n; i++ {
		out[i] = all[i].m
	}
	return out
}

// weight is the HRW score for (org, member): the first 8 bytes of
// SHA-256(orgID || 0x00 || memberID) as a big-endian uint64. The NUL separator
// stops ("ab","c") and ("a","bc") from colliding; SHA-256 gives a uniform,
// stable distribution over arbitrary org/member strings.
func weight(orgID, memberID string) uint64 {
	h := sha256.New()
	_, _ = h.Write([]byte(orgID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(memberID))
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}
