// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	// DefaultK is the Kademlia k-bucket replication & lookup parameter.
	DefaultK = 20
	// DefaultAlpha is the Kademlia iterative lookup concurrency parameter.
	DefaultAlpha = 3
	// DefaultFailureThreshold is the number of consecutive RPC failures before
	// a peer is evicted from the routing table.
	DefaultFailureThreshold = 2
	// DefaultMaxNewPeersPerSenderWindow bounds unprobed Sybil peer injection
	// bursts per sender within an admission window.
	DefaultMaxNewPeersPerSenderWindow = 24
)

// PeerInfo captures Kademlia routing-table metadata for one peer.
type PeerInfo struct {
	PeerID              PeerID          `json:"peer_id"`
	PublicKey           string          `json:"public_key,omitempty"`
	KeyHash             [32]byte        `json:"-"`
	Addrs               []string        `json:"addrs,omitempty"`
	RelayAddrs          []string        `json:"relay_addrs,omitempty"`
	NATMode             NATReachability `json:"nat_mode"`
	Role                NodeRole        `json:"role"`
	LastSeen            int64           `json:"last_seen"`
	RTTMs               float64         `json:"rtt_ms"`
	LivenessVerified    bool            `json:"liveness_verified"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
}

// PrimaryTCPAddr returns the first TCP multiaddr host:port, if any.
func (p PeerInfo) PrimaryTCPAddr() (string, bool) {
	for _, a := range p.Addrs {
		if hp, _, ok := ParseTCPMultiaddr(a); ok {
			return hp, true
		}
	}
	return "", false
}

// PrimaryUDPAddr returns the first UDP/QUIC multiaddr host:port, if any.
func (p PeerInfo) PrimaryUDPAddr() (string, bool) {
	for _, a := range p.Addrs {
		if hp, _, ok := ParseUDPMultiaddr(a); ok {
			return hp, true
		}
	}
	return "", false
}

// ClonePeerInfo deep-copies slices on PeerInfo.
func ClonePeerInfo(p PeerInfo) PeerInfo {
	cp := p
	if p.Addrs != nil {
		cp.Addrs = append([]string(nil), p.Addrs...)
	}
	if p.RelayAddrs != nil {
		cp.RelayAddrs = append([]string(nil), p.RelayAddrs...)
	}
	return cp
}

// RoutingTable implements a thread-safe 256-bucket XOR-metric Kademlia table.
type RoutingTable struct {
	mu               sync.RWMutex
	selfID           PeerID
	selfHash         [32]byte
	k                int
	failureThreshold int
	buckets          [256][]PeerInfo
}

// NewRoutingTable constructs a Kademlia routing table rooted at selfID.
func NewRoutingTable(selfID PeerID, k int) *RoutingTable {
	if k <= 0 {
		k = DefaultK
	}
	return &RoutingTable{
		selfID:           selfID,
		selfHash:         PeerKeyHash(selfID),
		k:                k,
		failureThreshold: DefaultFailureThreshold,
	}
}

func (rt *RoutingTable) bucketIndex(targetHash [32]byte) int {
	cpl := CommonPrefixLen(rt.selfHash, targetHash)
	if cpl >= 256 {
		return 255
	}
	return cpl
}

// UpsertPeer inserts or refreshes a peer in its XOR k-bucket. Self is ignored.
func (rt *RoutingTable) UpsertPeer(info PeerInfo) bool {
	if info.PeerID == "" || info.PeerID == rt.selfID {
		return false
	}
	cp := ClonePeerInfo(info)
	cp.KeyHash = PeerKeyHash(cp.PeerID)

	rt.mu.Lock()
	defer rt.mu.Unlock()

	idx := rt.bucketIndex(cp.KeyHash)
	bucket := rt.buckets[idx]
	for i := range bucket {
		if bucket[i].PeerID == cp.PeerID {
			if cp.PublicKey == "" {
				cp.PublicKey = bucket[i].PublicKey
			}
			if len(cp.Addrs) == 0 {
				cp.Addrs = bucket[i].Addrs
			}
			if len(cp.RelayAddrs) == 0 {
				cp.RelayAddrs = bucket[i].RelayAddrs
			}
			if cp.RTTMs == 0 {
				cp.RTTMs = bucket[i].RTTMs
			}
			if !cp.LivenessVerified {
				cp.LivenessVerified = bucket[i].LivenessVerified
				if cp.ConsecutiveFailures == 0 {
					cp.ConsecutiveFailures = bucket[i].ConsecutiveFailures
				}
			}
			// Move refreshed entry to tail (most recently seen).
			bucket = append(bucket[:i], bucket[i+1:]...)
			bucket = append(bucket, cp)
			rt.buckets[idx] = bucket
			return true
		}
	}

	if len(bucket) < rt.k {
		rt.buckets[idx] = append(bucket, cp)
		return true
	}

	// Evict the oldest peer with failures if bucket is full; otherwise evict oldest unverified peer.
	evictIdx := -1
	for i := range bucket {
		if bucket[i].ConsecutiveFailures > 0 {
			evictIdx = i
			break
		}
	}
	if evictIdx == -1 && cp.LivenessVerified {
		for i := range bucket {
			if !bucket[i].LivenessVerified {
				evictIdx = i
				break
			}
		}
	}
	if evictIdx >= 0 {
		bucket = append(bucket[:evictIdx], bucket[evictIdx+1:]...)
		bucket = append(bucket, cp)
		rt.buckets[idx] = bucket
		return true
	}
	return false
}

// UpsertPeerHint inserts an unverified peer hint learned from gossip or remote
// DHT peer lists. If the peer is already known with endpoints (or locally
// liveness-verified), its validated addresses and metadata are preserved so an
// arbitrary neighbor cannot poison live direct or relay routes.
func (rt *RoutingTable) UpsertPeerHint(info PeerInfo) bool {
	if info.PeerID == "" || info.PeerID == rt.selfID {
		return false
	}
	cp := ClonePeerInfo(info)
	cp.KeyHash = PeerKeyHash(cp.PeerID)
	cp.LivenessVerified = false
	cp.RTTMs = 0
	cp.ConsecutiveFailures = 0

	rt.mu.Lock()
	defer rt.mu.Unlock()

	idx := rt.bucketIndex(cp.KeyHash)
	bucket := rt.buckets[idx]
	for i := range bucket {
		if bucket[i].PeerID == cp.PeerID {
			existing := bucket[i]
			// Never let an unverified hint replace an existing peer's known endpoints
			// or identity metadata. Only backfill missing fields.
			if existing.PublicKey == "" && cp.PublicKey != "" {
				existing.PublicKey = cp.PublicKey
			}
			if len(existing.Addrs) == 0 && len(existing.RelayAddrs) == 0 {
				existing.Addrs = cp.Addrs
				existing.RelayAddrs = cp.RelayAddrs
				if existing.NATMode == "" {
					existing.NATMode = cp.NATMode
				}
				if existing.Role == "" {
					existing.Role = cp.Role
				}
			}
			bucket[i] = existing
			rt.buckets[idx] = bucket
			return true
		}
	}

	if len(bucket) < rt.k {
		rt.buckets[idx] = append(bucket, cp)
		return true
	}

	// Evict the oldest peer with failures if bucket is full; never evict verified peers for an unverified hint.
	for i := range bucket {
		if bucket[i].ConsecutiveFailures > 0 {
			bucket = append(bucket[:i], bucket[i+1:]...)
			bucket = append(bucket, cp)
			rt.buckets[idx] = bucket
			return true
		}
	}
	return false
}

// GetPeer looks up a peer by ID.
func (rt *RoutingTable) GetPeer(id PeerID) (PeerInfo, bool) {
	if id == "" || id == rt.selfID {
		return PeerInfo{}, false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	idx := rt.bucketIndex(PeerKeyHash(id))
	for _, p := range rt.buckets[idx] {
		if p.PeerID == id {
			return ClonePeerInfo(p), true
		}
	}
	return PeerInfo{}, false
}

// RemovePeer unconditionally removes id from the routing table.
func (rt *RoutingTable) RemovePeer(id PeerID) bool {
	if id == "" {
		return false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	idx := rt.bucketIndex(PeerKeyHash(id))
	bucket := rt.buckets[idx]
	for i := range bucket {
		if bucket[i].PeerID == id {
			rt.buckets[idx] = append(bucket[:i], bucket[i+1:]...)
			return true
		}
	}
	return false
}

// MarkFailure increments a peer's failure count and evicts it once it hits the
// configured threshold. Returns true if the peer was evicted.
func (rt *RoutingTable) MarkFailure(id PeerID) bool {
	if id == "" {
		return false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	idx := rt.bucketIndex(PeerKeyHash(id))
	bucket := rt.buckets[idx]
	for i := range bucket {
		if bucket[i].PeerID == id {
			bucket[i].ConsecutiveFailures++
			if bucket[i].ConsecutiveFailures >= rt.failureThreshold {
				rt.buckets[idx] = append(bucket[:i], bucket[i+1:]...)
				return true
			}
			rt.buckets[idx] = bucket
			return false
		}
	}
	return false
}

// MarkAlive resets a peer's failure count and updates RTT/LastSeen.
func (rt *RoutingTable) MarkAlive(id PeerID, rttMs float64, nowUnix int64) {
	if id == "" {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	idx := rt.bucketIndex(PeerKeyHash(id))
	bucket := rt.buckets[idx]
	for i := range bucket {
		if bucket[i].PeerID == id {
			bucket[i].ConsecutiveFailures = 0
			bucket[i].LivenessVerified = true
			bucket[i].LastSeen = nowUnix
			if rttMs > 0 {
				if bucket[i].RTTMs == 0 {
					bucket[i].RTTMs = rttMs
				} else {
					bucket[i].RTTMs = 0.7*bucket[i].RTTMs + 0.3*rttMs
				}
			}
			rt.buckets[idx] = bucket
			return
		}
	}
}

// ClosestPeers returns up to count peers ordered by exact 256-bit XOR distance
// to target.
func (rt *RoutingTable) ClosestPeers(target [32]byte, count int) []PeerInfo {
	if count <= 0 {
		count = rt.k
	}
	rt.mu.RLock()
	all := make([]PeerInfo, 0, rt.k*2)
	for i := range rt.buckets {
		for _, p := range rt.buckets[i] {
			all = append(all, ClonePeerInfo(p))
		}
	}
	rt.mu.RUnlock()

	sort.Slice(all, func(i, j int) bool {
		di := XORDistance(all[i].KeyHash, target)
		dj := XORDistance(all[j].KeyHash, target)
		cmp := CompareDistance(di, dj)
		if cmp != 0 {
			return cmp < 0
		}
		return all[i].PeerID < all[j].PeerID
	})
	if len(all) > count {
		all = all[:count]
	}
	return all
}

// AllPeers returns a deterministic PeerID-sorted snapshot of all known peers.
func (rt *RoutingTable) AllPeers() []PeerInfo {
	rt.mu.RLock()
	out := make([]PeerInfo, 0, rt.k*2)
	for i := range rt.buckets {
		for _, p := range rt.buckets[i] {
			out = append(out, ClonePeerInfo(p))
		}
	}
	rt.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

// Size returns the total number of peers currently in the routing table.
func (rt *RoutingTable) Size() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	n := 0
	for i := range rt.buckets {
		n += len(rt.buckets[i])
	}
	return n
}

// DHTStore stores verified CapabilityAnnouncements, tracks monotonic sequence
// numbers and anti-replay nonces, enforces Sybil admission limits, and indexes
// model providers.
type DHTStore struct {
	mu                    sync.RWMutex
	policy                VerifyPolicy
	announcements         map[PeerID]CapabilityAnnouncement
	lastAnnouncement      map[PeerID]CapabilityAnnouncement
	digests               map[string]struct{}
	removedDigests        map[PeerID]string
	highestSeq            map[PeerID]uint64
	seenNonces            map[PeerID]map[string]struct{}
	senderNewPeerWindow   map[PeerID]int
	senderWindowStart     int64
	maxNewPeersPerSender  int
	rejectionsByReason    map[string]int64
	verifyOps             atomic.Int64
	acceptedAnnouncements atomic.Int64
	rejectedAnnouncements atomic.Int64
}

// NewDHTStore creates a verified DHT announcement store.
func NewDHTStore(policy VerifyPolicy) *DHTStore {
	return &DHTStore{
		policy:               policy,
		announcements:        make(map[PeerID]CapabilityAnnouncement),
		lastAnnouncement:     make(map[PeerID]CapabilityAnnouncement),
		digests:              make(map[string]struct{}),
		removedDigests:       make(map[PeerID]string),
		highestSeq:           make(map[PeerID]uint64),
		seenNonces:           make(map[PeerID]map[string]struct{}),
		senderNewPeerWindow:  make(map[PeerID]int),
		maxNewPeersPerSender: DefaultMaxNewPeersPerSenderWindow,
		rejectionsByReason:   make(map[string]int64),
	}
}

// HasDigest reports whether an identical signed announcement is already cached.
func (s *DHTStore) HasDigest(ann CapabilityAnnouncement) bool {
	d := AnnouncementDigest(ann)
	if d == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.digests[d]
	return ok
}

// PutAnnouncement verifies ann fail-closed (unless policy.DisableVerification
// is explicitly set for the negative-control benchmark) and stores it.
// senderID identifies the gossip/RPC neighbor relaying ann (empty for local).
// livenessOK indicates whether the announced peer passed a local liveness check
// or is an already-known peer; unknown peers exceeding the per-sender Sybil
// burst quota without local liveness verification are rejected with
// ErrSybilFloodDetected.
func (s *DHTStore) PutAnnouncement(ann CapabilityAnnouncement, senderID PeerID, livenessOK bool, nowUnix int64) error {
	s.verifyOps.Add(1)
	if err := VerifyAnnouncementStateless(ann, nowUnix, s.policy); err != nil {
		s.recordReject(err)
		return err
	}
	digest := AnnouncementDigest(ann)

	s.mu.Lock()
	defer s.mu.Unlock()

	_, active := s.announcements[ann.PeerID]
	// Safe rehydration: if an identical previously-verified announcement was
	// removed while still valid (not expired), allow restoring that exact
	// record without tripping nonce/sequence replay guards. Any modified or
	// lower-sequence announcement continues to fail closed.
	isRehydration := !active && digest != "" && s.removedDigests[ann.PeerID] == digest

	if !s.policy.DisableVerification && !isRehydration {
		// Anti-replay nonce check.
		if nonces, ok := s.seenNonces[ann.PeerID]; ok {
			if _, used := nonces[ann.Nonce]; used {
				s.recordRejectLocked(ErrReplayNonce)
				return ErrReplayNonce
			}
		}
		// Monotonic sequence check.
		if prevSeq, ok := s.highestSeq[ann.PeerID]; ok && ann.Seq <= prevSeq {
			s.recordRejectLocked(ErrSequenceRollback)
			return fmt.Errorf("%w: peer %s seq %d <= %d", ErrSequenceRollback, ann.PeerID, ann.Seq, prevSeq)
		}
		// Sybil burst & unprobed admission guard.
		if !active && !livenessOK && senderID != "" {
			if s.senderWindowStart == 0 || nowUnix-s.senderWindowStart >= 60 {
				s.senderWindowStart = nowUnix
				s.senderNewPeerWindow = make(map[PeerID]int)
			}
			s.senderNewPeerWindow[senderID]++
			if s.senderNewPeerWindow[senderID] > s.maxNewPeersPerSender {
				s.recordRejectLocked(ErrSybilFloodDetected)
				return fmt.Errorf("%w: sender %s exceeded %d unprobed peers/min", ErrSybilFloodDetected, senderID, s.maxNewPeersPerSender)
			}
		}
	}

	cp := CloneAnnouncement(ann)
	if s.policy.DisableVerification && cp.VerificationClass == "" {
		cp.VerificationClass = "unverified_negative_control"
	}
	s.announcements[cp.PeerID] = cp
	s.lastAnnouncement[cp.PeerID] = cp
	delete(s.removedDigests, cp.PeerID)
	if digest != "" {
		s.digests[digest] = struct{}{}
	}
	if cp.Seq > s.highestSeq[cp.PeerID] {
		s.highestSeq[cp.PeerID] = cp.Seq
	}
	if cp.Nonce != "" {
		if s.seenNonces[cp.PeerID] == nil {
			s.seenNonces[cp.PeerID] = make(map[string]struct{})
		}
		s.seenNonces[cp.PeerID][cp.Nonce] = struct{}{}
	}
	s.acceptedAnnouncements.Add(1)
	return nil
}

func (s *DHTStore) recordReject(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRejectLocked(err)
}

// RecordRejection records a fail-closed rejection detected at the transport or
// liveness-probe layer (such as a failed Sybil admission probe).
func (s *DHTStore) RecordRejection(err error) {
	if err == nil {
		return
	}
	s.recordReject(err)
}

func (s *DHTStore) recordRejectLocked(err error) {
	s.rejectedAnnouncements.Add(1)
	reason := RejectReason(err)
	s.rejectionsByReason[reason]++
}

// GetAnnouncement returns the active announcement for peerID if valid at nowUnix.
func (s *DHTStore) GetAnnouncement(peerID PeerID, nowUnix int64) (CapabilityAnnouncement, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ann, ok := s.announcements[peerID]
	if !ok {
		return CapabilityAnnouncement{}, false
	}
	if !s.policy.DisableVerification && nowUnix > ann.ExpiresAt {
		return CapabilityAnnouncement{}, false
	}
	return CloneAnnouncement(ann), true
}

// LastAnnouncement returns the highest-sequence verified announcement ever
// stored for peerID (even if expired or removed), used for cryptographic
// self-sequence catch-up after an operator restarts a peer identity.
func (s *DHTStore) LastAnnouncement(peerID PeerID) (CapabilityAnnouncement, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ann, ok := s.lastAnnouncement[peerID]
	if !ok {
		return CapabilityAnnouncement{}, false
	}
	return CloneAnnouncement(ann), true
}

// RemoveAnnouncement removes peerID's active announcement while recording its
// digest so an identical still-valid verified record can be safely rehydrated
// later if the peer becomes reachable again.
func (s *DHTStore) RemoveAnnouncement(peerID PeerID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ann, ok := s.announcements[peerID]; ok {
		if d := AnnouncementDigest(ann); d != "" {
			delete(s.digests, d)
			s.removedDigests[peerID] = d
		}
		delete(s.announcements, peerID)
	}
}

// ListAnnouncements returns all non-expired announcements sorted by PeerID.
func (s *DHTStore) ListAnnouncements(nowUnix int64) []CapabilityAnnouncement {
	s.mu.RLock()
	out := make([]CapabilityAnnouncement, 0, len(s.announcements))
	for _, ann := range s.announcements {
		if !s.policy.DisableVerification && nowUnix > ann.ExpiresAt {
			continue
		}
		out = append(out, CloneAnnouncement(ann))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

// ProvidersForModel returns all non-expired announcements advertising modelID.
func (s *DHTStore) ProvidersForModel(modelID string, nowUnix int64) []CapabilityAnnouncement {
	s.mu.RLock()
	out := make([]CapabilityAnnouncement, 0, 8)
	for _, ann := range s.announcements {
		if !s.policy.DisableVerification && nowUnix > ann.ExpiresAt {
			continue
		}
		if _, ok := ann.SupportsModel(modelID); ok {
			out = append(out, CloneAnnouncement(ann))
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

// PruneExpired evicts all expired announcements at nowUnix and returns count removed.
func (s *DHTStore) PruneExpired(nowUnix int64) int {
	if s.policy.DisableVerification {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for pid, ann := range s.announcements {
		if nowUnix > ann.ExpiresAt {
			delete(s.announcements, pid)
			removed++
		}
	}
	return removed
}

// StoreStats snapshots verification and rejection counters.
type StoreStats struct {
	VerifyOps          int64            `json:"verify_ops"`
	Accepted           int64            `json:"accepted"`
	Rejected           int64            `json:"rejected"`
	RejectionsByReason map[string]int64 `json:"rejections_by_reason"`
}

// Stats returns a thread-safe snapshot of store verification metrics.
func (s *DHTStore) Stats() StoreStats {
	s.mu.RLock()
	byReason := make(map[string]int64, len(s.rejectionsByReason))
	for k, v := range s.rejectionsByReason {
		byReason[k] = v
	}
	s.mu.RUnlock()
	return StoreStats{
		VerifyOps:          s.verifyOps.Load(),
		Accepted:           s.acceptedAnnouncements.Load(),
		Rejected:           s.rejectedAnnouncements.Load(),
		RejectionsByReason: byReason,
	}
}
