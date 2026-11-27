// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	maxRPCFrameBytes  = 4 << 20 // 4 MiB safety cap per framed RPC message.
	defaultRPCTimeout = 2500 * time.Millisecond
)

// RPCType enumerates wire-level P2P RPC operations.
type RPCType string

const (
	RPCPing             RPCType = "PING"
	RPCFindNode         RPCType = "FIND_NODE"
	RPCPutAnnouncement  RPCType = "PUT_ANNOUNCEMENT"
	RPCFindProviders    RPCType = "FIND_PROVIDERS"
	RPCGossipSync       RPCType = "GOSSIP_SYNC"
	RPCInferRequest     RPCType = "INFER_REQUEST"
	RPCRelayRegister    RPCType = "RELAY_REGISTER"
	RPCRelayRegisterAck RPCType = "RELAY_REGISTER_ACK"
	RPCRelayForward     RPCType = "RELAY_FORWARD"
)

// RPCEnvelope is the length-prefixed JSON wire frame exchanged over mutual-TLS 1.3.
type RPCEnvelope struct {
	RequestID string          `json:"request_id"`
	Type      RPCType         `json:"type"`
	FromPeer  PeerID          `json:"from_peer"`
	ToPeer    PeerID          `json:"to_peer,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// PingRequest / PingResponse perform authenticated liveness probes.
type PingRequest struct {
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
}

type PingResponse struct {
	Nonce             string            `json:"nonce"`
	PeerID            PeerID            `json:"peer_id"`
	Timestamp         int64             `json:"timestamp"`
	VerificationClass VerificationClass `json:"verification_class"`
}

// FindNodeRequest / FindNodeResponse implement Kademlia FIND_NODE.
type FindNodeRequest struct {
	TargetHash string `json:"target_hash"` // 64-char hex [32]byte
	Count      int    `json:"count"`
}

type FindNodeResponse struct {
	Peers []PeerInfo `json:"peers"`
}

// PutAnnouncementRequest / PutAnnouncementResponse store a signed capability ad.
type PutAnnouncementRequest struct {
	Announcement CapabilityAnnouncement `json:"announcement"`
}

type PutAnnouncementResponse struct {
	Stored               bool                    `json:"stored"`
	Reason               string                  `json:"reason,omitempty"`
	Verified             bool                    `json:"verified"`
	ExistingAnnouncement *CapabilityAnnouncement `json:"existing_announcement,omitempty"`
}

// FindProvidersRequest / FindProvidersResponse implement Kademlia FIND_PROVIDERS.
type FindProvidersRequest struct {
	ModelID string `json:"model_id"`
	Count   int    `json:"count"`
}

type FindProvidersResponse struct {
	Providers    []CapabilityAnnouncement `json:"providers,omitempty"`
	ClosestPeers []PeerInfo               `json:"closest_peers,omitempty"`
}

// GossipSyncRequest / GossipSyncResponse exchange verified announcements & peers.
type GossipSyncRequest struct {
	Announcements []CapabilityAnnouncement `json:"announcements,omitempty"`
	Peers         []PeerInfo               `json:"peers,omitempty"`
}

type GossipSyncResponse struct {
	Announcements []CapabilityAnnouncement `json:"announcements,omitempty"`
	Peers         []PeerInfo               `json:"peers,omitempty"`
	AcceptedCount int                      `json:"accepted_count"`
	RejectedCount int                      `json:"rejected_count"`
}

// RelayRegisterRequest opens a circuit-relay v2 reservation for a NAT peer.
type RelayRegisterRequest struct {
	Announcement CapabilityAnnouncement `json:"announcement"`
}

type RelayRegisterResponse struct {
	RelayPeerID PeerID `json:"relay_peer_id"`
	CircuitAddr string `json:"circuit_addr"`
	Accepted    bool   `json:"accepted"`
	ExpiresAt   int64  `json:"expires_at"`
}

// RelayForwardRequest wraps an inner RPCEnvelope destined for a NAT peer.
type RelayForwardRequest struct {
	TargetPeer PeerID      `json:"target_peer"`
	Inner      RPCEnvelope `json:"inner"`
}

// InferenceRequest asks a model-serving provider peer to execute inference.
type InferenceRequest struct {
	RequestID       string `json:"request_id"`
	ModelID         string `json:"model_id"`
	PromptTokens    int    `json:"prompt_tokens"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	PromptDigest    string `json:"prompt_digest"`
	ClientPeerID    PeerID `json:"client_peer_id"`
	Timestamp       int64  `json:"timestamp"`
}

// InferenceRequestDigest computes a canonical domain-separated SHA-256 digest
// over the semantic execution parameters of req so providers cryptographically
// bind signed InferenceResponses to the exact request executed.
func InferenceRequestDigest(req InferenceRequest) string {
	raw, _ := json.Marshal(struct {
		RequestID       string `json:"request_id"`
		ModelID         string `json:"model_id"`
		PromptTokens    int    `json:"prompt_tokens"`
		MaxOutputTokens int    `json:"max_output_tokens"`
		PromptDigest    string `json:"prompt_digest"`
	}{
		RequestID:       req.RequestID,
		ModelID:         req.ModelID,
		PromptTokens:    req.PromptTokens,
		MaxOutputTokens: req.MaxOutputTokens,
		PromptDigest:    req.PromptDigest,
	})
	h := sha256.New()
	h.Write([]byte("hanzo-p2pnet-inference-req-v1\x00"))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// InferenceResponse is signed by the serving provider's Ed25519 key.
type InferenceResponse struct {
	RequestID         string            `json:"request_id"`
	ModelID           string            `json:"model_id"`
	RequestDigest     string            `json:"request_digest"`
	ClientPeerID      PeerID            `json:"client_peer_id,omitempty"`
	RequestTimestamp  int64             `json:"request_timestamp,omitempty"`
	ServedByPeerID    PeerID            `json:"served_by_peer_id"`
	ServedByPublicKey string            `json:"served_by_public_key"`
	ViaRelayPeerID    PeerID            `json:"via_relay_peer_id,omitempty"`
	TokensGenerated   int               `json:"tokens_generated"`
	TTFTMs            float64           `json:"ttft_ms"`
	TotalLatencyMs    float64           `json:"total_latency_ms"`
	CostMicroUSD      int64             `json:"cost_micro_usd"`
	OutputDigest      string            `json:"output_digest"`
	Timestamp         int64             `json:"timestamp"`
	Signature         string            `json:"signature"`
	VerificationClass VerificationClass `json:"verification_class"`
}

type unsignedInferenceResponse struct {
	RequestID         string  `json:"request_id"`
	ModelID           string  `json:"model_id"`
	RequestDigest     string  `json:"request_digest"`
	ClientPeerID      PeerID  `json:"client_peer_id,omitempty"`
	RequestTimestamp  int64   `json:"request_timestamp,omitempty"`
	ServedByPeerID    PeerID  `json:"served_by_peer_id"`
	ServedByPublicKey string  `json:"served_by_public_key"`
	ViaRelayPeerID    PeerID  `json:"via_relay_peer_id,omitempty"`
	TokensGenerated   int     `json:"tokens_generated"`
	TTFTMs            float64 `json:"ttft_ms"`
	TotalLatencyMs    float64 `json:"total_latency_ms"`
	CostMicroUSD      int64   `json:"cost_micro_usd"`
	OutputDigest      string  `json:"output_digest"`
	Timestamp         int64   `json:"timestamp"`
}

func canonicalInferenceResponseBytes(resp InferenceResponse) ([]byte, error) {
	u := unsignedInferenceResponse{
		RequestID:         resp.RequestID,
		ModelID:           resp.ModelID,
		RequestDigest:     resp.RequestDigest,
		ClientPeerID:      resp.ClientPeerID,
		RequestTimestamp:  resp.RequestTimestamp,
		ServedByPeerID:    resp.ServedByPeerID,
		ServedByPublicKey: resp.ServedByPublicKey,
		ViaRelayPeerID:    resp.ViaRelayPeerID,
		TokensGenerated:   resp.TokensGenerated,
		TTFTMs:            resp.TTFTMs,
		TotalLatencyMs:    resp.TotalLatencyMs,
		CostMicroUSD:      resp.CostMicroUSD,
		OutputDigest:      resp.OutputDigest,
		Timestamp:         resp.Timestamp,
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(domainInferenceResponse)+len(raw))
	out = append(out, domainInferenceResponse...)
	out = append(out, raw...)
	return out, nil
}

// SignInferenceResponse signs resp with the serving provider's Ed25519 identity.
func SignInferenceResponse(id *Identity, resp InferenceResponse) (InferenceResponse, error) {
	resp.ServedByPeerID = id.PeerID
	resp.ServedByPublicKey = id.PublicKeyHex()
	payload, err := canonicalInferenceResponseBytes(resp)
	if err != nil {
		return InferenceResponse{}, err
	}
	resp.Signature = hex.EncodeToString(ed25519.Sign(id.PrivateKey, payload))
	resp.VerificationClass = VerificationClassCryptoPeerSignature
	return resp, nil
}

// VerifyInferenceResponse validates the serving provider's Ed25519 signature
// and PeerID binding on resp.
func VerifyInferenceResponse(resp InferenceResponse, expectedPeer PeerID) error {
	pub, err := DecodePublicKeyHex(resp.ServedByPublicKey)
	if err != nil {
		return err
	}
	if err := VerifyPeerIDBinding(resp.ServedByPeerID, pub); err != nil {
		return err
	}
	if expectedPeer != "" && resp.ServedByPeerID != expectedPeer {
		return fmt.Errorf("%w: inference served by %s expected %s", ErrPeerIDMismatch, resp.ServedByPeerID, expectedPeer)
	}
	sig, err := hex.DecodeString(resp.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrInvalidSignature
	}
	payload, err := canonicalInferenceResponseBytes(resp)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return ErrInvalidSignature
	}
	return nil
}

// VerifyInferenceResponseForRequest validates the serving provider's Ed25519
// signature and PeerID binding AND cryptographically binds the signed response
// to the complete outstanding request (RequestID, ModelID, PromptTokens,
// MaxOutputTokens, PromptDigest, and any caller-populated ClientPeerID and
// Timestamp) to prevent circuit-relay tampering or replay.
func VerifyInferenceResponseForRequest(resp InferenceResponse, req InferenceRequest, expectedPeer PeerID) error {
	if err := VerifyInferenceResponse(resp, expectedPeer); err != nil {
		return err
	}
	if resp.RequestID == "" || resp.RequestID != req.RequestID {
		return fmt.Errorf("%w: response request_id %q != expected %q", ErrResponseMismatch, resp.RequestID, req.RequestID)
	}
	if resp.ModelID == "" || resp.ModelID != req.ModelID {
		return fmt.Errorf("%w: response model_id %q != expected %q", ErrResponseMismatch, resp.ModelID, req.ModelID)
	}
	expectedDigest := InferenceRequestDigest(req)
	if resp.RequestDigest == "" || resp.RequestDigest != expectedDigest {
		return fmt.Errorf("%w: response request_digest %q != expected %q", ErrResponseMismatch, resp.RequestDigest, expectedDigest)
	}
	if req.ClientPeerID != "" && resp.ClientPeerID != req.ClientPeerID {
		return fmt.Errorf("%w: response client_peer_id %q != expected %q", ErrResponseMismatch, resp.ClientPeerID, req.ClientPeerID)
	}
	if req.Timestamp != 0 && resp.RequestTimestamp != req.Timestamp {
		return fmt.Errorf("%w: response request_timestamp %d != expected %d", ErrResponseMismatch, resp.RequestTimestamp, req.Timestamp)
	}
	return nil
}

// UDPProbe is a signed UDP datagram used on the QUIC-ready UDP probe socket
// for zero-RTT liveness verification.
type UDPProbe struct {
	Kind      string `json:"kind"` // "PING" or "PONG"
	PeerID    PeerID `json:"peer_id"`
	PublicKey string `json:"public_key"`
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}

func canonicalUDPProbeBytes(p UDPProbe) ([]byte, error) {
	raw, err := json.Marshal(struct {
		Kind      string `json:"kind"`
		PeerID    PeerID `json:"peer_id"`
		PublicKey string `json:"public_key"`
		Nonce     string `json:"nonce"`
		Timestamp int64  `json:"timestamp"`
	}{
		Kind:      p.Kind,
		PeerID:    p.PeerID,
		PublicKey: p.PublicKey,
		Nonce:     p.Nonce,
		Timestamp: p.Timestamp,
	})
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(domainUDPProbe)+len(raw))
	out = append(out, domainUDPProbe...)
	out = append(out, raw...)
	return out, nil
}

func signUDPProbe(id *Identity, p UDPProbe) (UDPProbe, error) {
	p.PeerID = id.PeerID
	p.PublicKey = id.PublicKeyHex()
	payload, err := canonicalUDPProbeBytes(p)
	if err != nil {
		return UDPProbe{}, err
	}
	p.Signature = hex.EncodeToString(ed25519.Sign(id.PrivateKey, payload))
	return p, nil
}

func verifyUDPProbe(p UDPProbe) error {
	pub, err := DecodePublicKeyHex(p.PublicKey)
	if err != nil {
		return err
	}
	if err := VerifyPeerIDBinding(p.PeerID, pub); err != nil {
		return err
	}
	sig, err := hex.DecodeString(p.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrInvalidSignature
	}
	payload, err := canonicalUDPProbeBytes(p)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return ErrInvalidSignature
	}
	return nil
}

// writeFrame writes a 4-byte big-endian length-prefixed JSON RPCEnvelope.
func writeFrame(w io.Writer, env RPCEnvelope) (int64, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return 0, err
	}
	if len(data) > maxRPCFrameBytes {
		return 0, fmt.Errorf("p2pnet: rpc frame too large (%d bytes)", len(data))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return 0, err
	}
	n, err := w.Write(data)
	return int64(4 + n), err
}

// readFrame reads a 4-byte big-endian length-prefixed JSON RPCEnvelope.
func readFrame(r io.Reader) (RPCEnvelope, int64, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return RPCEnvelope{}, 0, err
	}
	length := binary.BigEndian.Uint32(hdr[:])
	if length == 0 || length > maxRPCFrameBytes {
		return RPCEnvelope{}, 4, fmt.Errorf("p2pnet: invalid rpc frame length %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return RPCEnvelope{}, int64(4), err
	}
	var env RPCEnvelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return RPCEnvelope{}, int64(4 + length), err
	}
	return env, int64(4 + length), nil
}

// relaySession tracks one active outbound TLS 1.3 reverse tunnel from a
// NAT-behind-firewall peer to a relay server.
type relaySession struct {
	peerID PeerID
	conn   net.Conn
	callMu sync.Mutex
	closed bool
}

func (rs *relaySession) forward(ctx context.Context, inner RPCEnvelope) (RPCEnvelope, int64, int64, error) {
	rs.callMu.Lock()
	defer rs.callMu.Unlock()
	if rs.closed {
		return RPCEnvelope{}, 0, 0, ErrRelayUnavailable
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultRPCTimeout)
	}
	_ = rs.conn.SetDeadline(deadline)
	defer func() { _ = rs.conn.SetDeadline(time.Time{}) }()

	sent, err := writeFrame(rs.conn, inner)
	if err != nil {
		rs.closed = true
		_ = rs.conn.Close()
		return RPCEnvelope{}, sent, 0, err
	}
	resp, recvd, err := readFrame(rs.conn)
	if err != nil {
		rs.closed = true
		_ = rs.conn.Close()
		return RPCEnvelope{}, sent, recvd, err
	}
	return resp, sent, recvd, nil
}

func (rs *relaySession) close() {
	rs.callMu.Lock()
	defer rs.callMu.Unlock()
	if !rs.closed {
		rs.closed = true
		_ = rs.conn.Close()
	}
}

func dialPeerTLS(ctx context.Context, hostPort string, cert tls.Certificate, expectedPeer PeerID) (*tls.Conn, error) {
	dialer := &net.Dialer{Timeout: 1500 * time.Millisecond}
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}
	rawConn, err := dialer.DialContext(ctx, "tcp4", hostPort)
	if err != nil {
		return nil, err
	}
	tlsConn := tls.Client(rawConn, ClientTLSConfig(cert, expectedPeer))
	if deadline, ok := ctx.Deadline(); ok {
		_ = tlsConn.SetDeadline(deadline)
	} else {
		_ = tlsConn.SetDeadline(time.Now().Add(defaultRPCTimeout))
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func remotePeerIDFromTLS(conn net.Conn) (PeerID, error) {
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return "", errors.New("p2pnet: connection is not tls")
	}
	state := tc.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", errors.New("p2pnet: missing peer certificate")
	}
	pid, _, err := VerifyPeerCert([][]byte{state.PeerCertificates[0].Raw}, "")
	return pid, err
}
