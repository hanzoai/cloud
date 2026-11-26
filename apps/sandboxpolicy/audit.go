// SPDX-License-Identifier: Apache-2.0

package sandboxpolicy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultAuditRingCapacity is the bounded per-tenant audit ring size.
	DefaultAuditRingCapacity = 4096
	// MaxAuditRingCapacity bounds per-tenant memory footprint.
	MaxAuditRingCapacity = 65536
	// GenesisAuditHash is the 64-hex-char zero digest anchoring sequence 1.
	GenesisAuditHash = "0000000000000000000000000000000000000000000000000000000000000000"
)

// RingOverflowPolicy governs bounded ring behavior when unconsumed audit events
// fill the per-tenant ring buffer.
type RingOverflowPolicy string

const (
	// RingOverwriteOldest retains the latest N events in a circular ring buffer
	// while tracking evicted count (default for continuous live telemetry).
	RingOverwriteOldest RingOverflowPolicy = "overwrite_oldest"
	// RingDropNewest simulates non-blocking eBPF perf/ringbuf backpressure where
	// new events are dropped when consumers lag behind ring capacity.
	RingDropNewest RingOverflowPolicy = "drop_newest"
)

// AuditEvent is a tamper-evident, hash-chained enforcement telemetry record
// strictly scoped to a single tenant.
type AuditEvent struct {
	// Seq is the monotonic per-tenant event sequence number (1-indexed).
	Seq uint64 `json:"seq"`
	// TenantID is the server-stamped authenticated tenant owning the sandbox.
	TenantID string `json:"tenantId"`
	// SandboxID identifies the Visor sandbox emitting this event.
	SandboxID string `json:"sandboxId"`
	// PolicyID identifies the evaluated TracingPolicy.
	PolicyID string `json:"policyId"`
	// PolicyVersion records the exact compiled policy version evaluated.
	PolicyVersion uint64 `json:"policyVersion"`
	// Timestamp is the UTC event timestamp.
	Timestamp time.Time `json:"timestamp"`
	// Domain identifies the enforcement domain ("syscall", "process", or "network").
	Domain Domain `json:"domain"`
	// ProcessLineage lists the ancestor binary/command chain leading to the event.
	ProcessLineage []string `json:"processLineage,omitempty"`
	// RuleID identifies the matched rule (or "default_deny" / "fail_closed_no_policy").
	RuleID string `json:"ruleId"`
	// Action is the enforcement decision ("ALLOW", "AUDIT_ALERT", "DENY", or "SIGKILL").
	Action Decision `json:"action"`
	// Errno is the POSIX errno or signal applied (0, 1=EPERM, 9=SIGKILL).
	Errno int `json:"errno"`
	// Subject summarizes the evaluated syscall, normalized binary path, or normalized ip:port.
	Subject string `json:"subject"`
	// Reason records the human-readable rule rationale.
	Reason string `json:"reason,omitempty"`
	// PrevHash is the SHA-256 hex digest of the preceding event (or GenesisAuditHash for seq 1).
	PrevHash string `json:"prevHash"`
	// Hash is the deterministic domain-separated SHA-256 hex digest over this event and PrevHash.
	Hash string `json:"hash"`
}

// ComputeAuditEventHash computes the canonical SHA-256 digest of an AuditEvent.
func ComputeAuditEventHash(ev AuditEvent) string {
	h := sha256.New()
	h.Write([]byte("hanzo-sandboxpolicy-audit-v1\x00"))
	writeU64 := func(v uint64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], v)
		h.Write(b[:])
	}
	writeStr := func(s string) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(s)))
		h.Write(b[:])
		h.Write([]byte(s))
	}
	writeU64(ev.Seq)
	writeStr(ev.TenantID)
	writeStr(ev.SandboxID)
	writeStr(ev.PolicyID)
	writeU64(ev.PolicyVersion)
	writeU64(uint64(ev.Timestamp.UTC().UnixNano()))
	writeStr(string(ev.Domain))
	writeU64(uint64(len(ev.ProcessLineage)))
	for _, p := range ev.ProcessLineage {
		writeStr(p)
	}
	writeStr(ev.RuleID)
	writeStr(string(ev.Action))
	writeU64(uint64(ev.Errno))
	writeStr(ev.Subject)
	writeStr(ev.Reason)
	writeStr(ev.PrevHash)
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyAuditChain validates that every event's Hash matches ComputeAuditEventHash
// and every contiguous pair links via PrevHash == prev.Hash and Seq == prev.Seq+1.
func VerifyAuditChain(events []AuditEvent) error {
	for i, ev := range events {
		expected := ComputeAuditEventHash(ev)
		if ev.Hash != expected {
			return fmt.Errorf("audit chain tamper at seq=%d: hash mismatch", ev.Seq)
		}
		if ev.Seq == 1 && ev.PrevHash != GenesisAuditHash {
			return fmt.Errorf("audit chain genesis mismatch at seq=1")
		}
		if i > 0 {
			prev := events[i-1]
			if ev.Seq != prev.Seq+1 {
				return fmt.Errorf("audit chain sequence gap: %d -> %d", prev.Seq, ev.Seq)
			}
			if ev.PrevHash != prev.Hash {
				return fmt.Errorf("audit chain link broken between seq=%d and seq=%d", prev.Seq, ev.Seq)
			}
		}
	}
	return nil
}

// AuditRingStats summarizes bounded ring buffer telemetry for one tenant.
type AuditRingStats struct {
	// Capacity is the maximum retained events in the per-tenant ring buffer.
	Capacity int `json:"capacity"`
	// Retained is the current number of events retained in the ring buffer.
	Retained int `json:"retained"`
	// TotalEmitted is the total number of events submitted to the audit ring.
	TotalEmitted uint64 `json:"totalEmitted"`
	// Dropped is the count of events dropped due to ring backpressure (RingDropNewest).
	Dropped uint64 `json:"dropped"`
	// Evicted is the count of oldest events overwritten upon ring wrap-around (RingOverwriteOldest).
	Evicted uint64 `json:"evicted"`
	// LastSeq is the most recent appended sequence number.
	LastSeq uint64 `json:"lastSeq"`
	// HeadHash is the SHA-256 digest of the most recent appended event.
	HeadHash string `json:"headHash"`
}

// TenantAuditRing is a concurrency-safe, bounded, SHA-256 hash-chained ring
// buffer strictly belonging to a single tenant.
type TenantAuditRing struct {
	mu       sync.Mutex
	capacity int
	overflow RingOverflowPolicy
	buf      []AuditEvent
	head     int // index of oldest retained entry
	count    int // number of valid entries in buf
	nextSeq  uint64
	lastHash string
	emitted  uint64
	dropped  uint64
	evicted  uint64
}

// NewTenantAuditRing constructs a bounded audit ring for a single tenant.
func NewTenantAuditRing(capacity int, overflow RingOverflowPolicy) *TenantAuditRing {
	if capacity <= 0 {
		capacity = DefaultAuditRingCapacity
	}
	if capacity > MaxAuditRingCapacity {
		capacity = MaxAuditRingCapacity
	}
	if overflow == "" {
		overflow = RingOverwriteOldest
	}
	return &TenantAuditRing{
		capacity: capacity,
		overflow: overflow,
		buf:      make([]AuditEvent, capacity),
		nextSeq:  1,
		lastHash: GenesisAuditHash,
	}
}

// Append records an AuditEvent into the ring, computing its hash chain link.
// Returns (event, appended). When overflow == RingDropNewest and the ring is
// full, returns (zero, false) and increments Dropped without advancing the chain.
func (r *TenantAuditRing) Append(ev AuditEvent) (AuditEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.emitted++
	if r.count == r.capacity && r.overflow == RingDropNewest {
		r.dropped++
		return AuditEvent{}, false
	}

	ev.Seq = r.nextSeq
	ev.Timestamp = ev.Timestamp.UTC()
	if len(ev.ProcessLineage) > MaxLineageDepth {
		ev.ProcessLineage = append([]string(nil), ev.ProcessLineage[:MaxLineageDepth]...)
	} else if len(ev.ProcessLineage) > 0 {
		ev.ProcessLineage = append([]string(nil), ev.ProcessLineage...)
	}
	ev.PrevHash = r.lastHash
	ev.Hash = ComputeAuditEventHash(ev)

	if r.count < r.capacity {
		idx := (r.head + r.count) % r.capacity
		r.buf[idx] = ev
		r.count++
	} else {
		// RingOverwriteOldest: evict oldest slot and advance head.
		r.buf[r.head] = ev
		r.head = (r.head + 1) % r.capacity
		r.evicted++
	}
	r.nextSeq++
	r.lastHash = ev.Hash
	return ev, true
}

// Drain removes up to n oldest events from the ring (used to simulate consumer
// draining under backpressure benchmarks) and returns the number drained.
func (r *TenantAuditRing) Drain(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || r.count == 0 {
		return 0
	}
	if n > r.count {
		n = r.count
	}
	for i := 0; i < n; i++ {
		r.buf[r.head] = AuditEvent{}
		r.head = (r.head + 1) % r.capacity
	}
	r.count -= n
	return n
}

// Snapshot returns retained events in chronological (oldest->newest) order,
// optionally filtered by sandboxID and truncated to the latest limit entries.
func (r *TenantAuditRing) Snapshot(sandboxID string, limit int) []AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil
	}
	out := make([]AuditEvent, 0, r.count)
	for i := 0; i < r.count; i++ {
		ev := r.buf[(r.head+i)%r.capacity]
		if sandboxID != "" && ev.SandboxID != sandboxID {
			continue
		}
		if len(ev.ProcessLineage) > 0 {
			ev.ProcessLineage = append([]string(nil), ev.ProcessLineage...)
		}
		out = append(out, ev)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Stats returns bounded ring counters for telemetry and backpressure analysis.
func (r *TenantAuditRing) Stats() AuditRingStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return AuditRingStats{
		Capacity:     r.capacity,
		Retained:     r.count,
		TotalEmitted: r.emitted,
		Dropped:      r.dropped,
		Evicted:      r.evicted,
		LastSeq:      r.nextSeq - 1,
		HeadHash:     r.lastHash,
	}
}

// ErrAuditChainTampered indicates a cryptographic hash-chain integrity failure.
var ErrAuditChainTampered = errors.New("sandboxpolicy: audit chain verification failed")
