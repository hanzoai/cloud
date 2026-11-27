// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// NodeConfig configures an autonomous HIP-1350 P2P mesh node.
type NodeConfig struct {
	Identity              *Identity
	Role                  NodeRole
	NATMode               NATReachability
	Models                []ModelCapability
	Hardware              HardwareMetadata
	AnnouncementTTLSec    int64
	Policy                VerifyPolicy
	HanzoRegistryDisabled bool
	// InitialSeq optionally sets a minimum sequence floor for announcements
	// published by this node's identity.
	InitialSeq uint64
	// SequenceStore optionally persists per-identity announcement sequences
	// across process restarts so reusing an Identity never rolls back sequences.
	SequenceStore SequenceStore
	// MaintenanceInterval optionally overrides how frequently the background
	// lifecycle loop checks for expiring self-announcements and expired records.
	MaintenanceInterval time.Duration
	// RogueHijackMode, when true on an adversarial test node, responds to
	// routed requests with tampered/hijacked payloads to measure impact under
	// the unverified negative-control regime.
	RogueHijackMode bool
	// Now allows deterministic clock injection in tests. Defaults to time.Now().Unix().
	Now func() int64
}

// NodeTelemetry exposes thread-safe counters for benchmarking and observability.
type NodeTelemetry struct {
	PeerID                PeerID          `json:"peer_id"`
	Role                  NodeRole        `json:"role"`
	NATMode               NATReachability `json:"nat_mode"`
	RoutingTableSize      int             `json:"routing_table_size"`
	VerifiedAnnounceCount int             `json:"verified_announce_count"`
	ActiveRelayClients    int             `json:"active_relay_clients"`
	HanzoCalls            int64           `json:"hanzo_calls"`
	RPCsSent              int64           `json:"rpcs_sent"`
	RPCsReceived          int64           `json:"rpcs_received"`
	BytesSent             int64           `json:"bytes_sent"`
	BytesReceived         int64           `json:"bytes_received"`
	GossipMessages        int64           `json:"gossip_messages"`
	RelayForwards         int64           `json:"relay_forwards"`
	RoutingFailovers      int64           `json:"routing_failovers"`
	UDPProbesSent         int64           `json:"udp_probes_sent"`
	UDPProbesReceived     int64           `json:"udp_probes_received"`
	StoreStats            StoreStats      `json:"store_stats"`
}

// RouteResult contains the verified InferenceResponse plus real routing telemetry.
type RouteResult struct {
	Response         InferenceResponse `json:"response"`
	RoutingLatencyMs float64           `json:"routing_latency_ms"`
	Attempts         int               `json:"attempts"`
	Failovers        int               `json:"failovers"`
	UsedRelay        bool              `json:"used_relay"`
	RelayPeerID      PeerID            `json:"relay_peer_id,omitempty"`
	Hijacked         bool              `json:"hijacked"`
}

// Node is a full autonomous P2P discovery, Kademlia DHT, gossip, circuit-relay,
// and model-routing node operating over real loopback TLS 1.3 and UDP sockets.
type Node struct {
	ID        *Identity
	Config    NodeConfig
	Table     *RoutingTable
	Store     *DHTStore
	tlsCert   tls.Certificate
	bootNonce string

	mu               sync.RWMutex
	renewMu          sync.Mutex
	tcpListener      net.Listener
	udpConn          net.PacketConn
	tcpAddr          string
	udpAddr          string
	relayAddrs       []string
	seq              uint64
	selfAnnounce     CapabilityAnnouncement
	relaySessions    map[PeerID]*relaySession // populated when NATMode == NATRelayServer
	natRelays        map[PeerID]net.Conn      // outbound relay tunnels when NATMode == NATRelayed
	providerFailures map[PeerID]int           // temporary routing failure counts (cleared on recovery)

	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
	wg     sync.WaitGroup

	hanzoCalls        atomic.Int64
	rpcsSent          atomic.Int64
	rpcsReceived      atomic.Int64
	bytesSent         atomic.Int64
	bytesReceived     atomic.Int64
	gossipMessages    atomic.Int64
	relayForwards     atomic.Int64
	routingFailovers  atomic.Int64
	udpProbesSent     atomic.Int64
	udpProbesReceived atomic.Int64
	reqCounter        atomic.Uint64
}

// NewNode creates an unstarted Node with a self-certifying Ed25519 identity
// and mutual-TLS 1.3 certificate.
func NewNode(cfg NodeConfig) (*Node, error) {
	id := cfg.Identity
	if id == nil {
		var err error
		id, err = GenerateIdentity(rand.Reader)
		if err != nil {
			return nil, err
		}
		cfg.Identity = id
	}
	if cfg.Role == "" {
		cfg.Role = RoleRouter
	}
	if cfg.NATMode == "" {
		cfg.NATMode = NATDirect
	}
	if cfg.AnnouncementTTLSec <= 0 {
		cfg.AnnouncementTTLSec = 300
	}
	if cfg.Policy.MaxClockSkewSec == 0 && !cfg.Policy.DisableVerification {
		cfg.Policy = DefaultVerifyPolicy()
	}

	seqFloor := cfg.InitialSeq
	if idSeq := id.LastSeq(); idSeq > seqFloor {
		seqFloor = idSeq
	}
	if cfg.SequenceStore != nil {
		persisted, err := cfg.SequenceStore.LoadSequence(id.PeerID)
		if err != nil {
			return nil, fmt.Errorf("p2pnet: load sequence for %s: %w", id.PeerID, err)
		}
		if persisted > seqFloor {
			seqFloor = persisted
		}
	}
	id.ObserveSeq(seqFloor)

	cert, err := GeneratePeerTLSCert(id)
	if err != nil {
		return nil, err
	}
	var bn [4]byte
	_, _ = rand.Read(bn[:])
	n := &Node{
		ID:               id,
		Config:           cfg,
		Table:            NewRoutingTable(id.PeerID, DefaultK),
		Store:            NewDHTStore(cfg.Policy),
		tlsCert:          cert,
		bootNonce:        hex.EncodeToString(bn[:]),
		seq:              seqFloor,
		relaySessions:    make(map[PeerID]*relaySession),
		natRelays:        make(map[PeerID]net.Conn),
		providerFailures: make(map[PeerID]int),
	}
	return n, nil
}

func (n *Node) nowUnix() int64 {
	if n.Config.Now != nil {
		return n.Config.Now()
	}
	return time.Now().Unix()
}

func (n *Node) nextReqID() string {
	seq := n.reqCounter.Add(1)
	return fmt.Sprintf("%s-%s-%d", string(n.ID.PeerID)[:10], n.bootNonce, seq)
}

func (n *Node) currentSeq() uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if idSeq := n.ID.LastSeq(); idSeq > n.seq {
		return idSeq
	}
	return n.seq
}

func (n *Node) nextAnnouncementSeq() (uint64, error) {
	n.mu.Lock()
	next := n.ID.AdvanceSeq(n.seq)
	n.seq = next
	n.mu.Unlock()
	if n.Config.SequenceStore != nil {
		if err := n.Config.SequenceStore.SaveSequence(n.ID.PeerID, next); err != nil {
			return 0, fmt.Errorf("p2pnet: save sequence %d for %s: %w", next, n.ID.PeerID, err)
		}
	}
	return next, nil
}

func (n *Node) observeSelfSeq(seq uint64) {
	if seq == 0 {
		return
	}
	n.mu.Lock()
	if seq > n.seq {
		n.seq = seq
	}
	n.ID.ObserveSeq(seq)
	cur := n.seq
	n.mu.Unlock()
	if n.Config.SequenceStore != nil {
		_ = n.Config.SequenceStore.SaveSequence(n.ID.PeerID, cur)
	}
}

func (n *Node) verifySelfSignedAnnouncement(ann CapabilityAnnouncement) bool {
	if ann.PeerID != n.ID.PeerID || ann.Seq == 0 {
		return false
	}
	pub, err := DecodePublicKeyHex(ann.PublicKey)
	if err != nil || !pub.Equal(n.ID.PublicKey) {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimSpace(ann.Signature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	payload, err := CanonicalAnnouncementBytes(ann)
	if err != nil {
		return false
	}
	return ed25519.Verify(n.ID.PublicKey, payload, sig)
}

func (n *Node) announcementRenewLead() int64 {
	lead := n.Config.AnnouncementTTLSec / 2
	if lead < 1 {
		lead = 1
	}
	return lead
}

func (n *Node) maintenanceInterval() time.Duration {
	if n.Config.MaintenanceInterval > 0 {
		return n.Config.MaintenanceInterval
	}
	if n.Config.Now != nil {
		return 50 * time.Millisecond
	}
	d := time.Duration(n.Config.AnnouncementTTLSec) * time.Second / 4
	if d < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if d > 15*time.Second {
		return 15 * time.Second
	}
	return d
}

// maybeRenewSelfAnnouncement checks whether this node's active capability
// announcement is approaching or past its TTL and re-signs a fresh monotonic
// announcement if so. Returns true if a renewal was performed.
func (n *Node) maybeRenewSelfAnnouncement() bool {
	if n.closed.Load() {
		return false
	}
	n.renewMu.Lock()
	defer n.renewMu.Unlock()
	if n.closed.Load() {
		return false
	}
	if len(n.Multiaddrs()) == 0 && len(n.RelayMultiaddrs()) == 0 {
		return false
	}
	now := n.nowUnix()
	ann := n.SelfAnnouncement()
	lead := n.announcementRenewLead()
	if ann.PeerID != "" && now+lead < ann.ExpiresAt {
		return false
	}
	_, err := n.RefreshSelfAnnouncement()
	return err == nil
}

func (n *Node) maintenanceLoop() {
	defer n.wg.Done()
	ticker := time.NewTicker(n.maintenanceInterval())
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			if n.closed.Load() {
				return
			}
			now := n.nowUnix()
			n.Store.PruneExpired(now)
			if n.Config.NATMode == NATRelayed {
				relCtx, cancel := context.WithTimeout(n.ctx, defaultRPCTimeout)
				n.maintainRelays(relCtx, 2)
				cancel()
			}
			if n.maybeRenewSelfAnnouncement() {
				pubCtx, cancel := context.WithTimeout(n.ctx, defaultRPCTimeout)
				n.PublishAnnouncement(pubCtx)
				cancel()
			}
		}
	}
}

func (n *Node) recordProviderFailure(peerID PeerID) {
	if peerID == "" {
		return
	}
	n.mu.Lock()
	n.providerFailures[peerID]++
	n.mu.Unlock()
}

func (n *Node) clearProviderFailure(peerID PeerID) {
	if peerID == "" {
		return
	}
	n.mu.Lock()
	delete(n.providerFailures, peerID)
	n.mu.Unlock()
}

func (n *Node) providerFailureCount(peerID PeerID) int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.providerFailures[peerID]
}

// sanitizeRemotePeerInfo clears transport-claimed liveness and health telemetry
// on PeerInfo received from untrusted peers over gossip or DHT RPCs. Only local
// authenticated probes/handshakes may establish LivenessVerified.
func sanitizeRemotePeerInfo(pi PeerInfo) PeerInfo {
	cp := ClonePeerInfo(pi)
	cp.LivenessVerified = false
	cp.RTTMs = 0
	cp.ConsecutiveFailures = 0
	return cp
}

// Start binds real loopback sockets (TLS 1.3 TCP + UDP probe socket for direct
// and relay-server nodes), begins serving P2P RPCs, and schedules the
// lifecycle-managed announcement renewal loop. NATRelayed nodes do not open a
// direct TCP listener; they communicate outbound via circuit relays.
func (n *Node) Start(parentCtx context.Context) error {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	n.ctx, n.cancel = context.WithCancel(parentCtx)

	if n.Config.NATMode != NATRelayed {
		tlsLn, err := tls.Listen("tcp4", "127.0.0.1:0", ServerTLSConfig(n.tlsCert))
		if err != nil {
			return fmt.Errorf("p2pnet: listen tls: %w", err)
		}
		udpConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			_ = tlsLn.Close()
			return fmt.Errorf("p2pnet: listen udp: %w", err)
		}

		n.mu.Lock()
		n.tcpListener = tlsLn
		n.udpConn = udpConn
		n.tcpAddr = tlsLn.Addr().String()
		n.udpAddr = udpConn.LocalAddr().String()
		n.mu.Unlock()

		n.wg.Add(2)
		go n.acceptLoop(tlsLn)
		go n.udpLoop(udpConn)

		// Sign and store self capability announcement immediately.
		if _, err := n.RefreshSelfAnnouncement(); err != nil {
			n.Shutdown()
			return err
		}
	}

	n.wg.Add(1)
	go n.maintenanceLoop()
	return nil
}

// Shutdown immediately terminates all listeners, relay sessions, and tunnels,
// simulating a clean or abrupt node removal.
func (n *Node) Shutdown() {
	if !n.closed.CompareAndSwap(false, true) {
		return
	}
	if n.cancel != nil {
		n.cancel()
	}
	n.mu.Lock()
	ln := n.tcpListener
	uc := n.udpConn
	sessions := make([]*relaySession, 0, len(n.relaySessions))
	for _, s := range n.relaySessions {
		sessions = append(sessions, s)
	}
	n.relaySessions = make(map[PeerID]*relaySession)
	natConns := make([]net.Conn, 0, len(n.natRelays))
	for _, c := range n.natRelays {
		natConns = append(natConns, c)
	}
	n.natRelays = make(map[PeerID]net.Conn)
	n.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	if uc != nil {
		_ = uc.Close()
	}
	for _, s := range sessions {
		s.close()
	}
	for _, c := range natConns {
		_ = c.Close()
	}
	n.wg.Wait()
}

// IsClosed reports whether the node has been shut down.
func (n *Node) IsClosed() bool {
	return n.closed.Load()
}

// Multiaddrs returns the node's direct TCP and UDP/QUIC-v1 multiaddr strings.
func (n *Node) Multiaddrs() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var out []string
	if n.tcpAddr != "" {
		if host, portStr, err := net.SplitHostPort(n.tcpAddr); err == nil {
			var port int
			_, _ = fmt.Sscanf(portStr, "%d", &port)
			out = append(out, FormatMultiaddr("tcp", host, port, n.ID.PeerID))
		}
	}
	if n.udpAddr != "" {
		if host, portStr, err := net.SplitHostPort(n.udpAddr); err == nil {
			var port int
			_, _ = fmt.Sscanf(portStr, "%d", &port)
			out = append(out, FormatMultiaddr("quic-v1", host, port, n.ID.PeerID))
		}
	}
	return out
}

// RelayMultiaddrs returns the node's active circuit-relay v2 addresses.
func (n *Node) RelayMultiaddrs() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return append([]string(nil), n.relayAddrs...)
}

// PeerInfo returns a PeerInfo snapshot describing this node.
func (n *Node) PeerInfo() PeerInfo {
	return PeerInfo{
		PeerID:           n.ID.PeerID,
		PublicKey:        n.ID.PublicKeyHex(),
		KeyHash:          n.ID.KeyHash,
		Addrs:            n.Multiaddrs(),
		RelayAddrs:       n.RelayMultiaddrs(),
		NATMode:          n.Config.NATMode,
		Role:             n.Config.Role,
		LastSeen:         n.nowUnix(),
		LivenessVerified: true,
	}
}

// RefreshSelfAnnouncement signs a fresh monotonic CapabilityAnnouncement and
// stores it locally in the node's DHTStore.
func (n *Node) RefreshSelfAnnouncement() (CapabilityAnnouncement, error) {
	addrs := n.Multiaddrs()
	relays := n.RelayMultiaddrs()
	if len(addrs) == 0 && len(relays) == 0 {
		return CapabilityAnnouncement{}, ErrMissingEndpoints
	}
	nonce, err := GenerateNonce(rand.Reader)
	if err != nil {
		return CapabilityAnnouncement{}, err
	}
	now := n.nowUnix()

	seq, err := n.nextAnnouncementSeq()
	if err != nil {
		return CapabilityAnnouncement{}, err
	}

	ann, err := SignCapabilityAnnouncement(n.ID, CapabilityAnnouncement{
		ProtocolVersion: ProtocolVersion,
		Addrs:           addrs,
		RelayAddrs:      relays,
		NATMode:         n.Config.NATMode,
		Role:            n.Config.Role,
		Models:          append([]ModelCapability(nil), n.Config.Models...),
		Hardware:        n.Config.Hardware,
		Seq:             seq,
		Nonce:           nonce,
		IssuedAt:        now,
		ExpiresAt:       now + n.Config.AnnouncementTTLSec,
	})
	if err != nil {
		return CapabilityAnnouncement{}, err
	}
	if err := n.Store.PutAnnouncement(ann, "", true, now); err != nil {
		return CapabilityAnnouncement{}, err
	}
	n.mu.Lock()
	n.selfAnnounce = ann
	n.mu.Unlock()
	return ann, nil
}

// SelfAnnouncement returns this node's latest signed CapabilityAnnouncement.
func (n *Node) SelfAnnouncement() CapabilityAnnouncement {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return CloneAnnouncement(n.selfAnnounce)
}

// HanzoCalls returns the count of Hanzo-operated control-plane calls (always 0
// in decentralized P2P mode).
func (n *Node) HanzoCalls() int64 {
	return n.hanzoCalls.Load()
}

// Telemetry returns a thread-safe snapshot of node networking and DHT metrics.
func (n *Node) Telemetry() NodeTelemetry {
	n.mu.RLock()
	relayClients := len(n.relaySessions)
	n.mu.RUnlock()
	return NodeTelemetry{
		PeerID:                n.ID.PeerID,
		Role:                  n.Config.Role,
		NATMode:               n.Config.NATMode,
		RoutingTableSize:      n.Table.Size(),
		VerifiedAnnounceCount: len(n.Store.ListAnnouncements(n.nowUnix())),
		ActiveRelayClients:    relayClients,
		HanzoCalls:            n.hanzoCalls.Load(),
		RPCsSent:              n.rpcsSent.Load(),
		RPCsReceived:          n.rpcsReceived.Load(),
		BytesSent:             n.bytesSent.Load(),
		BytesReceived:         n.bytesReceived.Load(),
		GossipMessages:        n.gossipMessages.Load(),
		RelayForwards:         n.relayForwards.Load(),
		RoutingFailovers:      n.routingFailovers.Load(),
		UDPProbesSent:         n.udpProbesSent.Load(),
		UDPProbesReceived:     n.udpProbesReceived.Load(),
		StoreStats:            n.Store.Stats(),
	}
}

func (n *Node) acceptLoop(ln net.Listener) {
	defer n.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if n.closed.Load() {
				return
			}
			continue
		}
		n.wg.Add(1)
		go n.handleIncomingConn(conn)
	}
}

func (n *Node) handleIncomingConn(conn net.Conn) {
	defer n.wg.Done()
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = conn.Close()
		}
	}()

	_ = conn.SetDeadline(time.Now().Add(defaultRPCTimeout))
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.Handshake(); err != nil {
			return
		}
	}
	remotePeer, err := remotePeerIDFromTLS(conn)
	if err != nil {
		return
	}

	req, recvd, err := readFrame(conn)
	if err != nil {
		return
	}
	n.bytesReceived.Add(recvd)
	n.rpcsReceived.Add(1)

	if req.Type == RPCRelayRegister {
		keepOpen = n.handleRelayRegister(conn, remotePeer, req)
		return
	}

	resp := n.handleRPC(n.ctx, remotePeer, req)
	sent, err := writeFrame(conn, resp)
	if err == nil {
		n.bytesSent.Add(sent)
	}
}

func (n *Node) handleRelayRegister(conn net.Conn, remotePeer PeerID, req RPCEnvelope) bool {
	if n.Config.NATMode != NATRelayServer {
		resp := RPCEnvelope{
			RequestID: req.RequestID,
			Type:      RPCRelayRegisterAck,
			FromPeer:  n.ID.PeerID,
			ToPeer:    remotePeer,
			Error:     ErrRelayUnavailable.Error(),
		}
		if sent, err := writeFrame(conn, resp); err == nil {
			n.bytesSent.Add(sent)
		}
		return false
	}

	var rreq RelayRegisterRequest
	if err := json.Unmarshal(req.Payload, &rreq); err != nil {
		return false
	}
	circuitAddr := FormatCircuitMultiaddr(n.ID.PeerID, remotePeer)
	now := n.nowUnix()

	// Accept initial reservation (NAT peer will immediately refresh its signed
	// announcement with circuitAddr if not already embedded).
	if rreq.Announcement.PeerID != "" {
		if rreq.Announcement.PeerID != remotePeer {
			resp := RPCEnvelope{
				RequestID: req.RequestID,
				Type:      RPCRelayRegisterAck,
				FromPeer:  n.ID.PeerID,
				ToPeer:    remotePeer,
				Error:     ErrPeerIDMismatch.Error(),
			}
			if sent, err := writeFrame(conn, resp); err == nil {
				n.bytesSent.Add(sent)
			}
			return false
		}
		_ = n.Store.PutAnnouncement(rreq.Announcement, remotePeer, true, now)
	}

	ackPayload, _ := json.Marshal(RelayRegisterResponse{
		RelayPeerID: n.ID.PeerID,
		CircuitAddr: circuitAddr,
		Accepted:    true,
		ExpiresAt:   now + n.Config.AnnouncementTTLSec,
	})
	ack := RPCEnvelope{
		RequestID: req.RequestID,
		Type:      RPCRelayRegisterAck,
		FromPeer:  n.ID.PeerID,
		ToPeer:    remotePeer,
		Payload:   ackPayload,
	}
	sent, err := writeFrame(conn, ack)
	if err != nil {
		return false
	}
	n.bytesSent.Add(sent)
	_ = conn.SetDeadline(time.Time{})

	sess := &relaySession{peerID: remotePeer, conn: conn}
	n.mu.Lock()
	if n.closed.Load() {
		n.mu.Unlock()
		return false
	}
	if prev, ok := n.relaySessions[remotePeer]; ok {
		prev.close()
	}
	n.relaySessions[remotePeer] = sess
	n.mu.Unlock()

	n.Table.UpsertPeer(PeerInfo{
		PeerID:           remotePeer,
		RelayAddrs:       []string{circuitAddr},
		NATMode:          NATRelayed,
		Role:             RoleProvider,
		LastSeen:         now,
		LivenessVerified: true,
	})
	return true
}

func (n *Node) udpLoop(pc net.PacketConn) {
	defer n.wg.Done()
	buf := make([]byte, 4096)
	for {
		nr, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if n.closed.Load() {
				return
			}
			continue
		}
		n.bytesReceived.Add(int64(nr))
		var probe UDPProbe
		if err := json.Unmarshal(buf[:nr], &probe); err != nil {
			continue
		}
		if err := verifyUDPProbe(probe); err != nil {
			continue
		}
		n.udpProbesReceived.Add(1)
		if probe.Kind != "PING" {
			continue
		}
		pong, err := signUDPProbe(n.ID, UDPProbe{
			Kind:      "PONG",
			Nonce:     probe.Nonce,
			Timestamp: n.nowUnix(),
		})
		if err != nil {
			continue
		}
		raw, err := json.Marshal(pong)
		if err != nil {
			continue
		}
		if nw, err := pc.WriteTo(raw, addr); err == nil {
			n.bytesSent.Add(int64(nw))
		}
	}
}

// ProbeUDP sends a signed UDP datagram to peer's QUIC-ready UDP socket and
// verifies the signed PONG reply (VerificationClassNetworkLivenessProbe).
func (n *Node) ProbeUDP(ctx context.Context, peer PeerInfo) (float64, error) {
	udpHP, ok := peer.PrimaryUDPAddr()
	if !ok {
		return 0, ErrMissingEndpoints
	}
	nonce, err := GenerateNonce(rand.Reader)
	if err != nil {
		return 0, err
	}
	ping, err := signUDPProbe(n.ID, UDPProbe{
		Kind:      "PING",
		Nonce:     nonce,
		Timestamp: n.nowUnix(),
	})
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(ping)
	if err != nil {
		return 0, err
	}
	conn, err := net.DialTimeout("udp4", udpHP, 500*time.Millisecond)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	deadline, has := ctx.Deadline()
	if !has {
		deadline = time.Now().Add(800 * time.Millisecond)
	}
	_ = conn.SetDeadline(deadline)

	start := time.Now()
	nw, err := conn.Write(raw)
	if err != nil {
		return 0, err
	}
	n.bytesSent.Add(int64(nw))
	n.udpProbesSent.Add(1)

	buf := make([]byte, 4096)
	nr, err := conn.Read(buf)
	if err != nil {
		return 0, err
	}
	n.bytesReceived.Add(int64(nr))
	rttMs := sinceMs(start)

	var pong UDPProbe
	if err := json.Unmarshal(buf[:nr], &pong); err != nil {
		return 0, err
	}
	if err := verifyUDPProbe(pong); err != nil {
		return 0, err
	}
	if pong.Kind != "PONG" || pong.Nonce != nonce || pong.PeerID != peer.PeerID {
		return 0, ErrPeerIDMismatch
	}
	n.Table.MarkAlive(peer.PeerID, rttMs, n.nowUnix())
	n.clearProviderFailure(peer.PeerID)
	return rttMs, nil
}

func (n *Node) handleRPC(ctx context.Context, remotePeer PeerID, req RPCEnvelope) RPCEnvelope {
	return n.handleRPCWithRelay(ctx, remotePeer, "", req)
}

func (n *Node) handleRPCWithRelay(ctx context.Context, remotePeer PeerID, viaRelay PeerID, req RPCEnvelope) RPCEnvelope {
	resp := RPCEnvelope{
		RequestID: req.RequestID,
		Type:      req.Type,
		FromPeer:  n.ID.PeerID,
		ToPeer:    remotePeer,
	}
	now := n.nowUnix()

	switch req.Type {
	case RPCPing:
		var p PingRequest
		_ = json.Unmarshal(req.Payload, &p)
		out, _ := json.Marshal(PingResponse{
			Nonce:             p.Nonce,
			PeerID:            n.ID.PeerID,
			Timestamp:         now,
			VerificationClass: VerificationClassNetworkLivenessProbe,
		})
		resp.Payload = out

	case RPCFindNode:
		var f FindNodeRequest
		if err := json.Unmarshal(req.Payload, &f); err != nil {
			resp.Error = err.Error()
			return resp
		}
		var target [32]byte
		if b, err := hex.DecodeString(f.TargetHash); err == nil && len(b) == 32 {
			copy(target[:], b)
		} else {
			target = PeerKeyHash(remotePeer)
		}
		count := f.Count
		if count <= 0 || count > DefaultK {
			count = DefaultK
		}
		peers := n.Table.ClosestPeers(target, count)
		peers = append(peers, n.PeerInfo())
		out, _ := json.Marshal(FindNodeResponse{Peers: peers})
		resp.Payload = out

	case RPCPutAnnouncement:
		var p PutAnnouncementRequest
		if err := json.Unmarshal(req.Payload, &p); err != nil {
			resp.Error = err.Error()
			return resp
		}
		err := n.IngestAnnouncement(p.Announcement, remotePeer, false)
		res := PutAnnouncementResponse{Stored: err == nil, Verified: err == nil}
		if err != nil {
			res.Reason = err.Error()
			resp.Error = err.Error()
			if errors.Is(err, ErrSequenceRollback) && p.Announcement.PeerID == remotePeer {
				if prev, ok := n.Store.LastAnnouncement(remotePeer); ok {
					res.ExistingAnnouncement = &prev
				}
			}
		}
		out, _ := json.Marshal(res)
		resp.Payload = out

	case RPCFindProviders:
		var f FindProvidersRequest
		if err := json.Unmarshal(req.Payload, &f); err != nil {
			resp.Error = err.Error()
			return resp
		}
		n.maybeRenewSelfAnnouncement()
		count := f.Count
		if count <= 0 || count > DefaultK {
			count = DefaultK
		}
		provs := n.Store.ProvidersForModel(f.ModelID, now)
		closest := n.Table.ClosestPeers(ModelKeyHash(f.ModelID), count)
		out, _ := json.Marshal(FindProvidersResponse{
			Providers:    provs,
			ClosestPeers: closest,
		})
		resp.Payload = out

	case RPCGossipSync:
		n.gossipMessages.Add(1)
		var g GossipSyncRequest
		if err := json.Unmarshal(req.Payload, &g); err != nil {
			resp.Error = err.Error()
			return resp
		}
		n.maybeRenewSelfAnnouncement()
		accepted, rejected := 0, 0
		for _, ann := range g.Announcements {
			if ann.PeerID == n.ID.PeerID {
				if n.verifySelfSignedAnnouncement(ann) && ann.Seq >= n.currentSeq() {
					n.observeSelfSeq(ann.Seq)
					_, _ = n.RefreshSelfAnnouncement()
				}
				continue
			}
			if n.Store.HasDigest(ann) {
				continue
			}
			if err := n.IngestAnnouncement(ann, remotePeer, false); err == nil {
				accepted++
			} else {
				rejected++
			}
		}
		for _, pi := range g.Peers {
			if pi.PeerID != "" && pi.PeerID != n.ID.PeerID && (len(pi.Addrs) > 0 || len(pi.RelayAddrs) > 0) {
				// Only upsert peer entries whose PeerID binds to their PublicKey when verification is enabled.
				if !n.Config.Policy.DisableVerification && pi.PublicKey != "" {
					pub, err := DecodePublicKeyHex(pi.PublicKey)
					if err != nil || VerifyPeerIDBinding(pi.PeerID, pub) != nil {
						continue
					}
				}
				n.Table.UpsertPeerHint(sanitizeRemotePeerInfo(pi))
			}
		}
		out, _ := json.Marshal(GossipSyncResponse{
			Announcements: n.Store.ListAnnouncements(now),
			Peers:         append(n.Table.AllPeers(), n.PeerInfo()),
			AcceptedCount: accepted,
			RejectedCount: rejected,
		})
		resp.Payload = out

	case RPCRelayForward:
		var rf RelayForwardRequest
		if err := json.Unmarshal(req.Payload, &rf); err != nil {
			resp.Error = err.Error()
			return resp
		}
		n.mu.RLock()
		sess, ok := n.relaySessions[rf.TargetPeer]
		n.mu.RUnlock()
		if !ok {
			resp.Error = ErrRelayUnavailable.Error()
			return resp
		}
		n.relayForwards.Add(1)
		fwdResp, sent, recvd, err := sess.forward(ctx, rf.Inner)
		n.bytesSent.Add(sent)
		n.bytesReceived.Add(recvd)
		if err != nil {
			n.mu.Lock()
			if cur, exists := n.relaySessions[rf.TargetPeer]; exists && cur == sess {
				delete(n.relaySessions, rf.TargetPeer)
			}
			n.mu.Unlock()
			resp.Error = err.Error()
			return resp
		}
		out, _ := json.Marshal(fwdResp)
		resp.Payload = out

	case RPCInferRequest:
		var ireq InferenceRequest
		if err := json.Unmarshal(req.Payload, &ireq); err != nil {
			resp.Error = err.Error()
			return resp
		}
		iresp, err := n.executeInference(ireq, viaRelay)
		if err != nil {
			resp.Error = err.Error()
			return resp
		}
		out, _ := json.Marshal(iresp)
		resp.Payload = out

	default:
		resp.Error = fmt.Sprintf("p2pnet: unsupported rpc type %q", req.Type)
	}
	return resp
}

// IngestAnnouncement verifies and stores an incoming CapabilityAnnouncement,
// requiring locally established liveness before exempting an unknown peer from
// Sybil admission limits, and updates the Kademlia routing table.
func (n *Node) IngestAnnouncement(ann CapabilityAnnouncement, senderID PeerID, livenessVerified bool) error {
	now := n.nowUnix()
	if !n.Config.Policy.DisableVerification {
		// Only locally established liveness (never gossiped membership or
		// remote-claimed flags) exempts an unknown peer from Sybil admission checks.
		if _, alreadyKnown := n.Store.GetAnnouncement(ann.PeerID, now); !alreadyKnown && !livenessVerified && senderID != "" {
			if pi, ok := n.Table.GetPeer(ann.PeerID); ok && pi.LivenessVerified && pi.ConsecutiveFailures == 0 {
				livenessVerified = true
			}
		}
	}
	if err := n.Store.PutAnnouncement(ann, senderID, livenessVerified, now); err != nil {
		return err
	}
	n.clearProviderFailure(ann.PeerID)
	if ann.PeerID != n.ID.PeerID {
		n.Table.UpsertPeer(PeerInfo{
			PeerID:           ann.PeerID,
			PublicKey:        ann.PublicKey,
			Addrs:            ann.Addrs,
			RelayAddrs:       ann.RelayAddrs,
			NATMode:          ann.NATMode,
			Role:             ann.Role,
			LastSeen:         now,
			LivenessVerified: livenessVerified,
		})
	}
	return nil
}

// SubmitAnnouncement validates an externally submitted announcement (e.g., via
// HTTP or adversarial test harness), enforcing cryptographic verification,
// replay/sequence checks, and a real mutual-TLS 1.3 liveness probe for unknown
// or locally unverified newcomer peers to defeat Sybil fake-identity floods
// fail-closed.
func (n *Node) SubmitAnnouncement(ctx context.Context, ann CapabilityAnnouncement, senderID PeerID) error {
	now := n.nowUnix()
	livenessOK := false
	if !n.Config.Policy.DisableVerification {
		if err := VerifyAnnouncementStateless(ann, now, n.Config.Policy); err != nil {
			// Record rejection inside Store as well for exact telemetry parity.
			return n.Store.PutAnnouncement(ann, senderID, false, now)
		}
		_, knownAnn := n.Store.GetAnnouncement(ann.PeerID, now)
		pi, knownPeer := n.Table.GetPeer(ann.PeerID)
		locallyLive := knownPeer && pi.LivenessVerified && pi.ConsecutiveFailures == 0
		if !knownAnn && !locallyLive && ann.PeerID != n.ID.PeerID {
			probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			defer cancel()
			candidate := PeerInfo{
				PeerID:     ann.PeerID,
				PublicKey:  ann.PublicKey,
				KeyHash:    PeerKeyHash(ann.PeerID),
				Addrs:      ann.Addrs,
				RelayAddrs: ann.RelayAddrs,
				NATMode:    ann.NATMode,
				Role:       ann.Role,
			}
			if _, err := n.Ping(probeCtx, candidate); err != nil {
				sybilErr := fmt.Errorf("%w: newcomer %s failed mutual-tls liveness probe: %v", ErrSybilFloodDetected, ann.PeerID, err)
				n.Store.RecordRejection(sybilErr)
				return sybilErr
			}
			livenessOK = true
		} else if locallyLive {
			livenessOK = true
		}
	}
	return n.IngestAnnouncement(ann, senderID, livenessOK)
}

func (n *Node) executeInference(req InferenceRequest, viaRelay PeerID) (InferenceResponse, error) {
	start := time.Now()
	reqDigest := InferenceRequestDigest(req)
	if n.Config.RogueHijackMode {
		// Adversarial rogue node simulates a routing hijack under the unverified negative control.
		resp := InferenceResponse{
			RequestID:         req.RequestID,
			ModelID:           req.ModelID,
			RequestDigest:     reqDigest,
			ClientPeerID:      req.ClientPeerID,
			RequestTimestamp:  req.Timestamp,
			ServedByPeerID:    n.ID.PeerID,
			ServedByPublicKey: n.ID.PublicKeyHex(),
			ViaRelayPeerID:    viaRelay,
			TokensGenerated:   0,
			TTFTMs:            0.1,
			TotalLatencyMs:    0.1,
			CostMicroUSD:      0,
			OutputDigest:      "HIJACKED_CORRUPT_OUTPUT",
			Timestamp:         n.nowUnix(),
			VerificationClass: "unverified_negative_control",
		}
		return SignInferenceResponse(n.ID, resp)
	}

	capModel, ok := CapabilityAnnouncement{Models: n.Config.Models}.SupportsModel(req.ModelID)
	if !ok {
		return InferenceResponse{}, fmt.Errorf("p2pnet: model %q not hosted on peer %s", req.ModelID, n.ID.PeerID)
	}
	tokens := req.MaxOutputTokens
	if tokens <= 0 {
		tokens = 128
	}

	// Deterministic lightweight token hash generation so every inference request
	// does real CPU work and yields real measured provider-local first-token & total latency.
	seed := sha256.Sum256([]byte(req.RequestID + ":" + req.ModelID + ":" + req.PromptDigest))
	h := seed
	// First token time.
	h = sha256.Sum256(h[:])
	ttftMs := sinceMs(start)
	if ttftMs < 0.01 {
		ttftMs = 0.01
	}
	for i := 1; i < tokens; i++ {
		var buf [40]byte
		copy(buf[:32], h[:])
		binary.BigEndian.PutUint64(buf[32:], uint64(i))
		h = sha256.Sum256(buf[:])
	}
	totalMs := sinceMs(start)
	if totalMs < ttftMs {
		totalMs = ttftMs
	}

	totalTokens := req.PromptTokens + tokens
	if totalTokens <= 0 {
		totalTokens = tokens
	}
	costMicroUSD := (int64(totalTokens) * capModel.PriceMicroUSDPer1MTokens) / 1_000_000
	if costMicroUSD == 0 && capModel.PriceMicroUSDPer1MTokens > 0 {
		costMicroUSD = 1
	}

	resp := InferenceResponse{
		RequestID:        req.RequestID,
		ModelID:          req.ModelID,
		RequestDigest:    reqDigest,
		ClientPeerID:     req.ClientPeerID,
		RequestTimestamp: req.Timestamp,
		ViaRelayPeerID:   viaRelay,
		TokensGenerated:  tokens,
		TTFTMs:           ttftMs,
		TotalLatencyMs:   totalMs,
		CostMicroUSD:     costMicroUSD,
		OutputDigest:     hex.EncodeToString(h[:16]),
		Timestamp:        n.nowUnix(),
	}
	return SignInferenceResponse(n.ID, resp)
}

// callPeer dials peer directly over mutual-TLS 1.3 or via a circuit relay when
// the peer is behind NAT. Returns the RPC response, used relay peer ID (if any),
// and measured RTT in milliseconds.
func (n *Node) callPeer(ctx context.Context, peer PeerInfo, req RPCEnvelope) (RPCEnvelope, PeerID, float64, error) {
	return n.callPeerWithSkipRelays(ctx, peer, req, nil)
}

func (n *Node) callPeerWithSkipRelays(ctx context.Context, peer PeerInfo, req RPCEnvelope, skipRelays map[PeerID]bool) (RPCEnvelope, PeerID, float64, error) {
	if err := ctx.Err(); err != nil {
		return RPCEnvelope{}, "", 0, err
	}
	req.FromPeer = n.ID.PeerID
	req.ToPeer = peer.PeerID
	if req.RequestID == "" {
		req.RequestID = n.nextReqID()
	}

	// 1. Try direct TCP+TLS 1.3 if peer advertises a TCP multiaddr.
	if hp, ok := peer.PrimaryTCPAddr(); ok {
		start := time.Now()
		expected := peer.PeerID
		if n.Config.Policy.DisableVerification {
			expected = ""
		}
		conn, err := dialPeerTLS(ctx, hp, n.tlsCert, expected)
		if err == nil {
			defer conn.Close()
			sent, werr := writeFrame(conn, req)
			if werr == nil {
				n.bytesSent.Add(sent)
				n.rpcsSent.Add(1)
				resp, recvd, rerr := readFrame(conn)
				if rerr == nil {
					n.bytesReceived.Add(recvd)
					rttMs := sinceMs(start)
					n.Table.MarkAlive(peer.PeerID, rttMs, n.nowUnix())
					n.clearProviderFailure(peer.PeerID)
					return resp, "", rttMs, nil
				}
				err = rerr
			} else {
				err = werr
			}
		}
		if ctx.Err() != nil {
			return RPCEnvelope{}, "", 0, ctx.Err()
		}
		if len(peer.RelayAddrs) == 0 {
			n.Table.MarkFailure(peer.PeerID)
			return RPCEnvelope{}, "", 0, err
		}
	}

	// 2. Fallback / primary path for NAT-relayed peers via circuit-relay v2.
	relayAddrs := append([]string(nil), peer.RelayAddrs...)
	if len(relayAddrs) > 1 {
		sort.SliceStable(relayAddrs, func(i, j int) bool {
			ri, _, okI := ParseCircuitMultiaddr(relayAddrs[i])
			rj, _, okJ := ParseCircuitMultiaddr(relayAddrs[j])
			if !okI || !okJ {
				return okI
			}
			pi, _ := n.Table.GetPeer(ri)
			pj, _ := n.Table.GetPeer(rj)
			return pi.ConsecutiveFailures < pj.ConsecutiveFailures
		})
	}

	var lastErr error = ErrRelayUnavailable
	for _, ra := range relayAddrs {
		if err := ctx.Err(); err != nil {
			return RPCEnvelope{}, "", 0, err
		}
		relayID, targetID, ok := ParseCircuitMultiaddr(ra)
		if !ok || targetID != peer.PeerID {
			continue
		}
		if skipRelays != nil && skipRelays[relayID] {
			continue
		}
		relayInfo, found := n.resolvePeer(relayID)
		if !found {
			continue
		}
		relayHP, ok := relayInfo.PrimaryTCPAddr()
		if !ok {
			continue
		}
		start := time.Now()
		conn, err := dialPeerTLS(ctx, relayHP, n.tlsCert, relayID)
		if err != nil {
			if ctx.Err() != nil {
				return RPCEnvelope{}, "", 0, ctx.Err()
			}
			n.Table.MarkFailure(relayID)
			lastErr = err
			continue
		}
		fwdPayload, _ := json.Marshal(RelayForwardRequest{
			TargetPeer: peer.PeerID,
			Inner:      req,
		})
		outer := RPCEnvelope{
			RequestID: n.nextReqID(),
			Type:      RPCRelayForward,
			FromPeer:  n.ID.PeerID,
			ToPeer:    relayID,
			Payload:   fwdPayload,
		}
		sent, werr := writeFrame(conn, outer)
		if werr != nil {
			_ = conn.Close()
			if ctx.Err() != nil {
				return RPCEnvelope{}, "", 0, ctx.Err()
			}
			n.Table.MarkFailure(relayID)
			lastErr = werr
			continue
		}
		n.bytesSent.Add(sent)
		n.rpcsSent.Add(1)
		outerResp, recvd, rerr := readFrame(conn)
		_ = conn.Close()
		if rerr != nil {
			if ctx.Err() != nil {
				return RPCEnvelope{}, "", 0, ctx.Err()
			}
			n.Table.MarkFailure(relayID)
			lastErr = rerr
			continue
		}
		n.bytesReceived.Add(recvd)
		if outerResp.Error != "" {
			lastErr = errors.New(outerResp.Error)
			continue
		}
		var innerResp RPCEnvelope
		if err := json.Unmarshal(outerResp.Payload, &innerResp); err != nil {
			lastErr = err
			continue
		}
		rttMs := sinceMs(start)
		n.Table.MarkAlive(relayID, rttMs, n.nowUnix())
		n.Table.MarkAlive(peer.PeerID, rttMs, n.nowUnix())
		n.clearProviderFailure(peer.PeerID)
		return innerResp, relayID, rttMs, nil
	}

	if ctx.Err() != nil {
		return RPCEnvelope{}, "", 0, ctx.Err()
	}
	n.Table.MarkFailure(peer.PeerID)
	return RPCEnvelope{}, "", 0, lastErr
}

func (n *Node) resolvePeer(id PeerID) (PeerInfo, bool) {
	p, inTable := n.Table.GetPeer(id)
	if inTable && p.LivenessVerified && p.ConsecutiveFailures == 0 && (len(p.Addrs) > 0 || len(p.RelayAddrs) > 0) {
		return p, true
	}
	if ann, ok := n.Store.GetAnnouncement(id, n.nowUnix()); ok && (len(ann.Addrs) > 0 || len(ann.RelayAddrs) > 0) {
		return PeerInfo{
			PeerID:     ann.PeerID,
			PublicKey:  ann.PublicKey,
			KeyHash:    PeerKeyHash(ann.PeerID),
			Addrs:      ann.Addrs,
			RelayAddrs: ann.RelayAddrs,
			NATMode:    ann.NATMode,
			Role:       ann.Role,
		}, true
	}
	if inTable {
		return p, true
	}
	return PeerInfo{}, false
}

// RegisterWithRelay opens a persistent outbound mutual-TLS 1.3 circuit-relay
// reservation on relayPeer so NAT-behind-firewall nodes can serve requests
// without a public listener or central registry.
func (n *Node) RegisterWithRelay(ctx context.Context, relayPeer PeerInfo) (string, error) {
	relayHP, ok := relayPeer.PrimaryTCPAddr()
	if !ok {
		return "", ErrMissingEndpoints
	}
	n.mu.RLock()
	if _, already := n.natRelays[relayPeer.PeerID]; already {
		n.mu.RUnlock()
		return FormatCircuitMultiaddr(relayPeer.PeerID, n.ID.PeerID), nil
	}
	n.mu.RUnlock()

	conn, err := dialPeerTLS(ctx, relayHP, n.tlsCert, relayPeer.PeerID)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(RelayRegisterRequest{})
	req := RPCEnvelope{
		RequestID: n.nextReqID(),
		Type:      RPCRelayRegister,
		FromPeer:  n.ID.PeerID,
		ToPeer:    relayPeer.PeerID,
		Payload:   payload,
	}
	sent, err := writeFrame(conn, req)
	if err != nil {
		_ = conn.Close()
		return "", err
	}
	n.bytesSent.Add(sent)
	n.rpcsSent.Add(1)

	ack, recvd, err := readFrame(conn)
	if err != nil {
		_ = conn.Close()
		return "", err
	}
	n.bytesReceived.Add(recvd)
	if ack.Error != "" {
		_ = conn.Close()
		return "", errors.New(ack.Error)
	}
	var rresp RelayRegisterResponse
	if err := json.Unmarshal(ack.Payload, &rresp); err != nil || !rresp.Accepted {
		_ = conn.Close()
		return "", ErrRelayUnavailable
	}
	_ = conn.SetDeadline(time.Time{})

	n.mu.Lock()
	if n.closed.Load() {
		n.mu.Unlock()
		_ = conn.Close()
		return "", errors.New("p2pnet: node closed")
	}
	n.natRelays[relayPeer.PeerID] = conn
	if !containsAddr(n.relayAddrs, rresp.CircuitAddr) {
		n.relayAddrs = append(n.relayAddrs, rresp.CircuitAddr)
	}
	n.mu.Unlock()

	verifiedRelay := ClonePeerInfo(relayPeer)
	verifiedRelay.LivenessVerified = true
	verifiedRelay.LastSeen = n.nowUnix()
	verifiedRelay.ConsecutiveFailures = 0
	n.Table.UpsertPeer(verifiedRelay)
	n.wg.Add(1)
	go n.serveNATRelayLoop(relayPeer.PeerID, rresp.CircuitAddr, conn)
	return rresp.CircuitAddr, nil
}

func (n *Node) serveNATRelayLoop(relayID PeerID, circuitAddr string, conn net.Conn) {
	defer n.wg.Done()
	defer func() {
		_ = conn.Close()
		n.mu.Lock()
		delete(n.natRelays, relayID)
		filtered := n.relayAddrs[:0]
		for _, a := range n.relayAddrs {
			if a != circuitAddr {
				filtered = append(filtered, a)
			}
		}
		n.relayAddrs = append([]string(nil), filtered...)
		n.mu.Unlock()
		n.Table.MarkFailure(relayID)
	}()

	for {
		req, recvd, err := readFrame(conn)
		if err != nil {
			return
		}
		n.bytesReceived.Add(recvd)
		n.rpcsReceived.Add(1)
		resp := n.handleRPCWithRelay(n.ctx, req.FromPeer, relayID, req)
		sent, err := writeFrame(conn, resp)
		if err != nil {
			return
		}
		n.bytesSent.Add(sent)
	}
}

func (n *Node) relayCandidates() []PeerInfo {
	seen := make(map[PeerID]PeerInfo)
	for _, p := range n.Table.AllPeers() {
		if p.NATMode == NATRelayServer {
			seen[p.PeerID] = p
		}
	}
	for _, ann := range n.Store.ListAnnouncements(n.nowUnix()) {
		if ann.PeerID == n.ID.PeerID || ann.NATMode != NATRelayServer {
			continue
		}
		if cur, ok := seen[ann.PeerID]; !ok || (!cur.LivenessVerified && len(ann.Addrs) > 0) {
			seen[ann.PeerID] = PeerInfo{
				PeerID:     ann.PeerID,
				PublicKey:  ann.PublicKey,
				KeyHash:    PeerKeyHash(ann.PeerID),
				Addrs:      ann.Addrs,
				RelayAddrs: ann.RelayAddrs,
				NATMode:    ann.NATMode,
				Role:       ann.Role,
			}
		}
	}
	out := make([]PeerInfo, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ConsecutiveFailures != out[j].ConsecutiveFailures {
			return out[i].ConsecutiveFailures < out[j].ConsecutiveFailures
		}
		if out[i].LivenessVerified != out[j].LivenessVerified {
			return out[i].LivenessVerified
		}
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (n *Node) maintainRelays(ctx context.Context, targetCount int) {
	if n.Config.NATMode != NATRelayed {
		return
	}
	if targetCount <= 0 {
		targetCount = 2
	}
	n.mu.RLock()
	before := len(n.natRelays)
	n.mu.RUnlock()

	active := before
	if active < targetCount {
		for _, p := range n.relayCandidates() {
			n.mu.RLock()
			_, has := n.natRelays[p.PeerID]
			cur := len(n.natRelays)
			n.mu.RUnlock()
			if has || cur >= targetCount {
				continue
			}
			if _, err := n.RegisterWithRelay(ctx, p); err == nil {
				active++
			}
		}
	}
	if active == 0 {
		return
	}
	ann := n.SelfAnnouncement()
	curAddrs := n.RelayMultiaddrs()
	if active != before || !stringSlicesEqual(ann.RelayAddrs, curAddrs) {
		if _, err := n.RefreshSelfAnnouncement(); err == nil {
			n.PublishAnnouncement(ctx)
		}
	}
}

// EnsureRelays ensures a NATRelayed node has at least targetCount active
// circuit-relay reservations, automatically migrating to surviving relay peers
// from its Kademlia routing table if prior relays died, then refreshes and
// republishes its signed CapabilityAnnouncement.
func (n *Node) EnsureRelays(ctx context.Context, targetCount int) error {
	if n.Config.NATMode != NATRelayed {
		return nil
	}
	if targetCount <= 0 {
		targetCount = 2
	}

	n.mu.RLock()
	active := len(n.natRelays)
	n.mu.RUnlock()

	if active < targetCount {
		for _, p := range n.relayCandidates() {
			n.mu.RLock()
			_, has := n.natRelays[p.PeerID]
			cur := len(n.natRelays)
			n.mu.RUnlock()
			if has || cur >= targetCount {
				continue
			}
			if _, err := n.RegisterWithRelay(ctx, p); err == nil {
				active++
			}
		}
	}
	if active == 0 {
		return ErrRelayUnavailable
	}
	if _, err := n.RefreshSelfAnnouncement(); err != nil {
		return err
	}
	n.PublishAnnouncement(ctx)
	return nil
}

// Bootstrap connects to initial bootstrap peers (with zero central registry),
// establishes circuit relays if NATRelayed, runs iterative Kademlia FindNode,
// and disseminates signed announcements via gossip.
func (n *Node) Bootstrap(ctx context.Context, bootstrapPeers []PeerInfo) error {
	for _, bp := range bootstrapPeers {
		if bp.PeerID == "" || bp.PeerID == n.ID.PeerID {
			continue
		}
		n.Table.UpsertPeer(sanitizeRemotePeerInfo(bp))
	}
	if n.Config.NATMode == NATRelayed {
		if err := n.EnsureRelays(ctx, 2); err != nil {
			return err
		}
	}
	// Iterative Kademlia lookup for self key populates close and distant k-buckets.
	n.FindNode(ctx, n.ID.KeyHash)
	// Exchange signed capability announcements across discovered neighbors.
	n.GossipRound(ctx, 4)
	// Re-check relays for NAT peers now that full mesh relay servers are known.
	if n.Config.NATMode == NATRelayed {
		_ = n.EnsureRelays(ctx, 2)
		n.GossipRound(ctx, 4)
	}
	return nil
}

// Ping executes a live mutual-TLS 1.3 PING/PONG probe against peer.
func (n *Node) Ping(ctx context.Context, peer PeerInfo) (float64, error) {
	nonce, err := GenerateNonce(rand.Reader)
	if err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(PingRequest{Nonce: nonce, Timestamp: n.nowUnix()})
	resp, _, rttMs, err := n.callPeer(ctx, peer, RPCEnvelope{
		Type:    RPCPing,
		Payload: payload,
	})
	if err != nil {
		return 0, err
	}
	if resp.Error != "" {
		return 0, errors.New(resp.Error)
	}
	var presp PingResponse
	if err := json.Unmarshal(resp.Payload, &presp); err != nil {
		return 0, err
	}
	if presp.Nonce != nonce || presp.PeerID != peer.PeerID {
		return 0, ErrPeerIDMismatch
	}
	n.clearProviderFailure(peer.PeerID)
	return rttMs, nil
}

// FindNode runs an iterative XOR-metric Kademlia FIND_NODE lookup for target.
func (n *Node) FindNode(ctx context.Context, target [32]byte) []PeerInfo {
	queried := make(map[PeerID]bool)
	queried[n.ID.PeerID] = true

	for round := 0; round < 5; round++ {
		candidates := n.Table.ClosestPeers(target, DefaultK)
		var batch []PeerInfo
		for _, c := range candidates {
			if !queried[c.PeerID] {
				queried[c.PeerID] = true
				batch = append(batch, c)
				if len(batch) >= DefaultAlpha {
					break
				}
			}
		}
		if len(batch) == 0 {
			break
		}

		payload, _ := json.Marshal(FindNodeRequest{
			TargetHash: hex.EncodeToString(target[:]),
			Count:      DefaultK,
		})
		var wg sync.WaitGroup
		var mu sync.Mutex
		discoveredAny := false
		for _, peer := range batch {
			wg.Add(1)
			go func(p PeerInfo) {
				defer wg.Done()
				resp, _, _, err := n.callPeer(ctx, p, RPCEnvelope{
					Type:    RPCFindNode,
					Payload: payload,
				})
				if err != nil || resp.Error != "" {
					return
				}
				var fnr FindNodeResponse
				if err := json.Unmarshal(resp.Payload, &fnr); err != nil {
					return
				}
				for _, dp := range fnr.Peers {
					if dp.PeerID == "" || dp.PeerID == n.ID.PeerID {
						continue
					}
					if !n.Config.Policy.DisableVerification && dp.PublicKey != "" {
						pub, err := DecodePublicKeyHex(dp.PublicKey)
						if err != nil || VerifyPeerIDBinding(dp.PeerID, pub) != nil {
							continue
						}
					}
					if _, exists := n.Table.GetPeer(dp.PeerID); !exists {
						mu.Lock()
						discoveredAny = true
						mu.Unlock()
					}
					n.Table.UpsertPeerHint(sanitizeRemotePeerInfo(dp))
				}
			}(peer)
		}
		wg.Wait()
		if !discoveredAny && round > 0 {
			break
		}
	}
	return n.Table.ClosestPeers(target, DefaultK)
}

// PublishAnnouncement stores this node's latest signed announcement on the
// K closest peers to self and to each advertised model's DHT key.
func (n *Node) PublishAnnouncement(ctx context.Context) int {
	return n.publishAnnouncementPass(ctx, true)
}

func (n *Node) publishAnnouncementPass(ctx context.Context, allowCatchup bool) int {
	ann := n.SelfAnnouncement()
	if ann.PeerID == "" {
		return 0
	}
	targets := make(map[PeerID]PeerInfo)
	for _, p := range n.Table.ClosestPeers(n.ID.KeyHash, 6) {
		targets[p.PeerID] = p
	}
	for _, m := range ann.Models {
		for _, p := range n.Table.ClosestPeers(ModelKeyHash(m.ModelID), 6) {
			targets[p.PeerID] = p
		}
	}
	payload, _ := json.Marshal(PutAnnouncementRequest{Announcement: ann})
	var stored atomic.Int32
	var caughtUp atomic.Bool
	var wg sync.WaitGroup
	for _, peer := range targets {
		wg.Add(1)
		go func(p PeerInfo) {
			defer wg.Done()
			resp, _, _, err := n.callPeer(ctx, p, RPCEnvelope{
				Type:    RPCPutAnnouncement,
				Payload: payload,
			})
			if err != nil {
				return
			}
			if resp.Error == "" {
				stored.Add(1)
				return
			}
			if allowCatchup && len(resp.Payload) > 0 {
				var putResp PutAnnouncementResponse
				if uerr := json.Unmarshal(resp.Payload, &putResp); uerr == nil && putResp.ExistingAnnouncement != nil {
					prev := *putResp.ExistingAnnouncement
					if n.verifySelfSignedAnnouncement(prev) && prev.Seq >= ann.Seq {
						n.observeSelfSeq(prev.Seq)
						caughtUp.Store(true)
					}
				}
			}
		}(peer)
	}
	wg.Wait()
	if allowCatchup && caughtUp.Load() {
		if _, err := n.RefreshSelfAnnouncement(); err == nil {
			return n.publishAnnouncementPass(ctx, false)
		}
	}
	return int(stored.Load())
}

// GossipRound runs one anti-entropy gossip exchange with up to fanout peers,
// disseminating verified CapabilityAnnouncements and PeerInfo across the mesh.
func (n *Node) GossipRound(ctx context.Context, fanout int) int {
	if fanout <= 0 {
		fanout = 4
	}
	n.maybeRenewSelfAnnouncement()
	peers := n.Table.AllPeers()
	if len(peers) == 0 {
		return 0
	}
	// Deterministic-yet-spread selection across XOR space.
	if len(peers) > fanout {
		step := len(peers) / fanout
		if step < 1 {
			step = 1
		}
		selected := make([]PeerInfo, 0, fanout)
		for i := 0; i < len(peers) && len(selected) < fanout; i += step {
			selected = append(selected, peers[i])
		}
		peers = selected
	}

	now := n.nowUnix()
	reqPayload, _ := json.Marshal(GossipSyncRequest{
		Announcements: n.Store.ListAnnouncements(now),
		Peers:         append(n.Table.AllPeers(), n.PeerInfo()),
	})

	var learned atomic.Int32
	var needSelfRepublish atomic.Bool
	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(p PeerInfo) {
			defer wg.Done()
			n.gossipMessages.Add(1)
			resp, _, _, err := n.callPeer(ctx, p, RPCEnvelope{
				Type:    RPCGossipSync,
				Payload: reqPayload,
			})
			if err != nil || resp.Error != "" {
				return
			}
			var gresp GossipSyncResponse
			if err := json.Unmarshal(resp.Payload, &gresp); err != nil {
				return
			}
			for _, ann := range gresp.Announcements {
				if ann.PeerID == n.ID.PeerID {
					if n.verifySelfSignedAnnouncement(ann) && ann.Seq >= n.currentSeq() {
						n.observeSelfSeq(ann.Seq)
						needSelfRepublish.Store(true)
					}
					continue
				}
				if n.Store.HasDigest(ann) {
					continue
				}
				if err := n.IngestAnnouncement(ann, p.PeerID, false); err == nil {
					learned.Add(1)
				}
			}
			for _, dp := range gresp.Peers {
				if dp.PeerID != "" && dp.PeerID != n.ID.PeerID {
					if !n.Config.Policy.DisableVerification && dp.PublicKey != "" {
						pub, err := DecodePublicKeyHex(dp.PublicKey)
						if err != nil || VerifyPeerIDBinding(dp.PeerID, pub) != nil {
							continue
						}
					}
					n.Table.UpsertPeerHint(sanitizeRemotePeerInfo(dp))
				}
			}
		}(peer)
	}
	wg.Wait()
	if needSelfRepublish.Load() {
		if _, err := n.RefreshSelfAnnouncement(); err == nil {
			n.PublishAnnouncement(ctx)
		}
	}
	return int(learned.Load())
}

// FindProviders executes an iterative Kademlia FIND_PROVIDERS lookup for
// modelID, verifying every returned CapabilityAnnouncement locally fail-closed.
func (n *Node) FindProviders(ctx context.Context, modelID string) []CapabilityAnnouncement {
	target := ModelKeyHash(modelID)
	queried := make(map[PeerID]bool)
	queried[n.ID.PeerID] = true

	payload, _ := json.Marshal(FindProvidersRequest{
		ModelID: modelID,
		Count:   DefaultK,
	})

	var needSelfRepublish atomic.Bool
	for round := 0; round < 4; round++ {
		candidates := n.Table.ClosestPeers(target, DefaultK)
		var batch []PeerInfo
		for _, c := range candidates {
			if !queried[c.PeerID] {
				queried[c.PeerID] = true
				batch = append(batch, c)
				if len(batch) >= DefaultAlpha {
					break
				}
			}
		}
		if len(batch) == 0 {
			break
		}

		var wg sync.WaitGroup
		for _, peer := range batch {
			wg.Add(1)
			go func(p PeerInfo) {
				defer wg.Done()
				resp, _, _, err := n.callPeer(ctx, p, RPCEnvelope{
					Type:    RPCFindProviders,
					Payload: payload,
				})
				if err != nil || resp.Error != "" {
					return
				}
				var fpr FindProvidersResponse
				if err := json.Unmarshal(resp.Payload, &fpr); err != nil {
					return
				}
				for _, ann := range fpr.Providers {
					if ann.PeerID == n.ID.PeerID {
						if n.verifySelfSignedAnnouncement(ann) && ann.Seq >= n.currentSeq() {
							n.observeSelfSeq(ann.Seq)
							needSelfRepublish.Store(true)
						}
						continue
					}
					if !n.Store.HasDigest(ann) {
						_ = n.IngestAnnouncement(ann, p.PeerID, false)
					}
				}
				for _, dp := range fpr.ClosestPeers {
					if dp.PeerID != "" && dp.PeerID != n.ID.PeerID {
						if !n.Config.Policy.DisableVerification && dp.PublicKey != "" {
							pub, err := DecodePublicKeyHex(dp.PublicKey)
							if err != nil || VerifyPeerIDBinding(dp.PeerID, pub) != nil {
								continue
							}
						}
						n.Table.UpsertPeerHint(sanitizeRemotePeerInfo(dp))
					}
				}
			}(peer)
		}
		wg.Wait()
	}

	if needSelfRepublish.Load() {
		if _, err := n.RefreshSelfAnnouncement(); err == nil {
			n.PublishAnnouncement(ctx)
		}
	}

	return n.Store.ProvidersForModel(modelID, n.nowUnix())
}

// RouteInference discovers verified providers for req.ModelID via DHT + local
// cache and dispatches the inference request end-to-end with automatic failover
// across surviving direct and NAT-relayed providers. Fails closed with
// ErrNoRouteFound when no verified reachable provider exists.
func (n *Node) RouteInference(ctx context.Context, req InferenceRequest) (RouteResult, error) {
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return RouteResult{RoutingLatencyMs: sinceMs(start)}, err
	}
	if req.RequestID == "" {
		req.RequestID = n.nextReqID()
	}
	req.ClientPeerID = n.ID.PeerID
	if req.Timestamp == 0 {
		req.Timestamp = n.nowUnix()
	}

	now := n.nowUnix()
	candidates := n.Store.ProvidersForModel(req.ModelID, now)
	if len(candidates) == 0 {
		candidates = n.FindProviders(ctx, req.ModelID)
	}
	if err := ctx.Err(); err != nil {
		return RouteResult{RoutingLatencyMs: sinceMs(start)}, err
	}
	if len(candidates) == 0 {
		return RouteResult{}, ErrNoRouteFound
	}

	tried := make(map[PeerID]bool)
	attempts := 0
	failovers := 0

	for pass := 0; pass < 2; pass++ {
		if err := ctx.Err(); err != nil {
			return RouteResult{
				RoutingLatencyMs: sinceMs(start),
				Attempts:         attempts,
				Failovers:        failovers,
			}, err
		}
		if pass == 1 {
			candidates = n.FindProviders(ctx, req.ModelID)
			if err := ctx.Err(); err != nil {
				return RouteResult{
					RoutingLatencyMs: sinceMs(start),
					Attempts:         attempts,
					Failovers:        failovers,
				}, err
			}
		}
		ranked := n.rankCandidates(req.ModelID, req.RequestID, candidates)
		for _, ann := range ranked {
			if err := ctx.Err(); err != nil {
				return RouteResult{
					RoutingLatencyMs: sinceMs(start),
					Attempts:         attempts,
					Failovers:        failovers,
				}, err
			}
			if tried[ann.PeerID] {
				continue
			}
			tried[ann.PeerID] = true

			// Local shortcut if this node itself serves the model.
			if ann.PeerID == n.ID.PeerID {
				attempts++
				iresp, err := n.executeInference(req, "")
				if err == nil {
					return RouteResult{
						Response:         iresp,
						RoutingLatencyMs: sinceMs(start),
						Attempts:         attempts,
						Failovers:        failovers,
					}, nil
				}
				continue
			}

			peer := PeerInfo{
				PeerID:     ann.PeerID,
				PublicKey:  ann.PublicKey,
				KeyHash:    PeerKeyHash(ann.PeerID),
				Addrs:      ann.Addrs,
				RelayAddrs: ann.RelayAddrs,
				NATMode:    ann.NATMode,
				Role:       ann.Role,
			}
			reqPayload, _ := json.Marshal(req)
			skipRelays := make(map[PeerID]bool)
			maxRelayTries := len(peer.RelayAddrs)
			if maxRelayTries < 1 {
				maxRelayTries = 1
			}

			for try := 0; try < maxRelayTries; try++ {
				if err := ctx.Err(); err != nil {
					return RouteResult{
						RoutingLatencyMs: sinceMs(start),
						Attempts:         attempts,
						Failovers:        failovers,
					}, err
				}
				attempts++
				rpcResp, viaRelay, _, err := n.callPeerWithSkipRelays(ctx, peer, RPCEnvelope{
					Type:    RPCInferRequest,
					Payload: reqPayload,
				}, skipRelays)
				if ctx.Err() != nil {
					return RouteResult{
						RoutingLatencyMs: sinceMs(start),
						Attempts:         attempts,
						Failovers:        failovers,
					}, ctx.Err()
				}
				if err != nil || rpcResp.Error != "" {
					failovers++
					n.routingFailovers.Add(1)
					// Retain the verified announcement while recording temporary
					// provider health failure so transient errors or cancellations
					// never permanently suppress a reachable provider.
					n.recordProviderFailure(ann.PeerID)
					break
				}

				var iresp InferenceResponse
				if err := json.Unmarshal(rpcResp.Payload, &iresp); err != nil {
					failovers++
					n.routingFailovers.Add(1)
					if viaRelay != "" {
						n.Table.MarkFailure(viaRelay)
						skipRelays[viaRelay] = true
						continue
					}
					n.recordProviderFailure(ann.PeerID)
					break
				}
				if !n.Config.Policy.DisableVerification {
					verifyErr := VerifyInferenceResponseForRequest(iresp, req, ann.PeerID)
					if verifyErr == nil && viaRelay != "" && iresp.ViaRelayPeerID != "" && iresp.ViaRelayPeerID != viaRelay {
						verifyErr = fmt.Errorf("%w: response relay %s != observed relay %s", ErrResponseMismatch, iresp.ViaRelayPeerID, viaRelay)
					}
					if verifyErr != nil {
						failovers++
						n.routingFailovers.Add(1)
						if viaRelay != "" {
							// An untrusted relay may have replayed or tampered with
							// the response; penalize the relay and try alternate relays
							// without permanently removing the provider's announcement.
							n.Table.MarkFailure(viaRelay)
							skipRelays[viaRelay] = true
							continue
						}
						n.Table.MarkFailure(ann.PeerID)
						n.recordProviderFailure(ann.PeerID)
						break
					}
				}
				// Leave provider-signed response fields unmodified; relay path
				// telemetry is exposed on RouteResult.UsedRelay / RelayPeerID.
				n.clearProviderFailure(ann.PeerID)
				hijacked := strings.Contains(iresp.OutputDigest, "HIJACKED")
				return RouteResult{
					Response:         iresp,
					RoutingLatencyMs: sinceMs(start),
					Attempts:         attempts,
					Failovers:        failovers,
					UsedRelay:        viaRelay != "",
					RelayPeerID:      viaRelay,
					Hijacked:         hijacked,
				}, nil
			}
		}
	}

	return RouteResult{
		RoutingLatencyMs: sinceMs(start),
		Attempts:         attempts,
		Failovers:        failovers,
	}, ErrNoRouteFound
}

// rankCandidates neutrally orders verified model providers by temporary health,
// observed RTT, advertised price, and base latency with zero operator
// preference. Healthy providers within 20% of the lowest score are
// deterministically load-balanced by sha256(requestID || peerID) to prevent
// single-provider hot-spotting.
func (n *Node) rankCandidates(modelID, requestID string, anns []CapabilityAnnouncement) []CapabilityAnnouncement {
	cp := make([]CapabilityAnnouncement, len(anns))
	copy(cp, anns)
	if len(cp) <= 1 {
		return cp
	}
	scores := make(map[PeerID]float64, len(cp))
	failures := make(map[PeerID]int, len(cp))
	minHealthyScore := -1.0
	minAnyScore := -1.0
	for _, a := range cp {
		sc := n.candidateScore(modelID, a)
		fc := n.providerFailureCount(a.PeerID)
		scores[a.PeerID] = sc
		failures[a.PeerID] = fc
		if minAnyScore < 0 || sc < minAnyScore {
			minAnyScore = sc
		}
		if fc == 0 && (minHealthyScore < 0 || sc < minHealthyScore) {
			minHealthyScore = sc
		}
	}
	minScore := minHealthyScore
	if minScore < 0 {
		minScore = minAnyScore
	}
	bandCeiling := minScore * 1.20
	sort.Slice(cp, func(i, j int) bool {
		fi := failures[cp[i].PeerID]
		fj := failures[cp[j].PeerID]
		if fi != fj {
			return fi < fj
		}
		si := scores[cp[i].PeerID]
		sj := scores[cp[j].PeerID]
		inBandI := si <= bandCeiling
		inBandJ := sj <= bandCeiling
		if inBandI != inBandJ {
			return inBandI
		}
		if inBandI && inBandJ && requestID != "" {
			hi := sha256.Sum256([]byte(requestID + "|" + string(cp[i].PeerID)))
			hj := sha256.Sum256([]byte(requestID + "|" + string(cp[j].PeerID)))
			return CompareDistance(hi, hj) < 0
		}
		if si != sj {
			return si < sj
		}
		return cp[i].PeerID < cp[j].PeerID
	})
	return cp
}

func (n *Node) candidateScore(modelID string, ann CapabilityAnnouncement) float64 {
	mc, _ := ann.SupportsModel(modelID)
	priceTerm := float64(mc.PriceMicroUSDPer1MTokens) / 100000.0
	latTerm := mc.BaseLatencyMs
	if pi, ok := n.Table.GetPeer(ann.PeerID); ok && pi.RTTMs > 0 {
		rtt := pi.RTTMs
		if rtt > 20.0 {
			rtt = 20.0
		}
		latTerm += rtt * 0.25
	}
	// Slight latency penalty for relayed hops when direct peers are equally cheap.
	if ann.NATMode == NATRelayed {
		latTerm += 1.5
	}
	return priceTerm*4.0 + latTerm*0.25
}

func containsAddr(list []string, target string) bool {
	for _, a := range list {
		if a == target {
			return true
		}
	}
	return false
}

func sinceMs(start time.Time) float64 {
	d := time.Since(start)
	if d <= 0 {
		return 0.01
	}
	ms := float64(d.Microseconds()) / 1000.0
	if ms < 0.01 {
		return 0.01
	}
	return ms
}
