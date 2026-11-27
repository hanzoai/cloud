// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ModelCapability describes one AI model served by a peer.
type ModelCapability struct {
	ModelID                  string  `json:"model_id"`
	MaxContextTokens         int     `json:"max_context_tokens"`
	EstTokPerSec             float64 `json:"est_tok_per_sec"`
	BaseLatencyMs            float64 `json:"base_latency_ms"`
	PriceMicroUSDPer1MTokens int64   `json:"price_micro_usd_per_1m_tokens"`
}

// HardwareMetadata describes self-declared compute metadata.
// HardwareAttested is always false unless an external TEE quote verifier
// confirms HardwareAttestationHash (VerificationClassHardwareAttestation).
type HardwareMetadata struct {
	GPUModel                string `json:"gpu_model,omitempty"`
	GPUCount                int    `json:"gpu_count,omitempty"`
	VRAMGB                  int    `json:"vram_gb,omitempty"`
	Region                  string `json:"region,omitempty"`
	Jurisdiction            string `json:"jurisdiction,omitempty"`
	HardwareAttestationHash string `json:"hardware_attestation_hash,omitempty"`
	HardwareAttested        bool   `json:"hardware_attested"`
}

// CapabilityAnnouncement is a cryptographically authenticated peer and model
// capability advertisement distributed via Kademlia DHT and gossip.
type CapabilityAnnouncement struct {
	ProtocolVersion   string            `json:"protocol_version"`
	PeerID            PeerID            `json:"peer_id"`
	PublicKey         string            `json:"public_key"`
	Addrs             []string          `json:"addrs,omitempty"`
	RelayAddrs        []string          `json:"relay_addrs,omitempty"`
	NATMode           NATReachability   `json:"nat_mode"`
	Role              NodeRole          `json:"role"`
	Models            []ModelCapability `json:"models,omitempty"`
	Hardware          HardwareMetadata  `json:"hardware"`
	Seq               uint64            `json:"seq"`
	Nonce             string            `json:"nonce"`
	IssuedAt          int64             `json:"issued_at"`
	ExpiresAt         int64             `json:"expires_at"`
	Signature         string            `json:"signature"`
	VerificationClass VerificationClass `json:"verification_class"`
}

// unsignedAnnouncement is the exact canonical projection covered by Ed25519.
type unsignedAnnouncement struct {
	ProtocolVersion string            `json:"protocol_version"`
	PeerID          PeerID            `json:"peer_id"`
	PublicKey       string            `json:"public_key"`
	Addrs           []string          `json:"addrs,omitempty"`
	RelayAddrs      []string          `json:"relay_addrs,omitempty"`
	NATMode         NATReachability   `json:"nat_mode"`
	Role            NodeRole          `json:"role"`
	Models          []ModelCapability `json:"models,omitempty"`
	Hardware        HardwareMetadata  `json:"hardware"`
	Seq             uint64            `json:"seq"`
	Nonce           string            `json:"nonce"`
	IssuedAt        int64             `json:"issued_at"`
	ExpiresAt       int64             `json:"expires_at"`
}

// VerifyPolicy configures fail-closed capability announcement checks.
// DisableVerification MUST remain false in production; it exists solely to
// execute the honest negative-control benchmark regime.
type VerifyPolicy struct {
	MaxClockSkewSec     int64
	MaxTTLSec           int64
	DisableVerification bool
}

// DefaultVerifyPolicy returns the strict fail-closed verification policy.
func DefaultVerifyPolicy() VerifyPolicy {
	return VerifyPolicy{
		MaxClockSkewSec:     DefaultMaxClockSkewSec,
		MaxTTLSec:           3600,
		DisableVerification: false,
	}
}

// CanonicalAnnouncementBytes returns the domain-separated canonical byte
// sequence signed by the announcing peer's Ed25519 key.
func CanonicalAnnouncementBytes(ann CapabilityAnnouncement) ([]byte, error) {
	u := unsignedAnnouncement{
		ProtocolVersion: ann.ProtocolVersion,
		PeerID:          ann.PeerID,
		PublicKey:       ann.PublicKey,
		Addrs:           ann.Addrs,
		RelayAddrs:      ann.RelayAddrs,
		NATMode:         ann.NATMode,
		Role:            ann.Role,
		Models:          ann.Models,
		Hardware:        ann.Hardware,
		Seq:             ann.Seq,
		Nonce:           ann.Nonce,
		IssuedAt:        ann.IssuedAt,
		ExpiresAt:       ann.ExpiresAt,
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(domainCapabilityAnnounce)+len(raw))
	out = append(out, domainCapabilityAnnounce...)
	out = append(out, raw...)
	return out, nil
}

// AnnouncementDigest returns a deterministic SHA-256 hex digest of the full
// signed announcement (used for gossip deduplication).
func AnnouncementDigest(ann CapabilityAnnouncement) string {
	b, err := CanonicalAnnouncementBytes(ann)
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write(b)
	h.Write([]byte(ann.Signature))
	return hex.EncodeToString(h.Sum(nil))
}

// SignCapabilityAnnouncement populates identity fields and signs ann with id.
func SignCapabilityAnnouncement(id *Identity, ann CapabilityAnnouncement) (CapabilityAnnouncement, error) {
	if id == nil {
		return CapabilityAnnouncement{}, errors.New("p2pnet: nil identity")
	}
	if ann.ProtocolVersion == "" {
		ann.ProtocolVersion = ProtocolVersion
	}
	ann.PeerID = id.PeerID
	ann.PublicKey = id.PublicKeyHex()
	if ann.Seq == 0 {
		return CapabilityAnnouncement{}, ErrInvalidSequence
	}
	if strings.TrimSpace(ann.Nonce) == "" {
		return CapabilityAnnouncement{}, ErrMissingNonce
	}
	if ann.ExpiresAt <= ann.IssuedAt {
		return CapabilityAnnouncement{}, ErrAnnouncementExpired
	}
	if err := validateAnnouncementEndpoints(ann); err != nil {
		return CapabilityAnnouncement{}, err
	}
	payload, err := CanonicalAnnouncementBytes(ann)
	if err != nil {
		return CapabilityAnnouncement{}, err
	}
	sig := ed25519.Sign(id.PrivateKey, payload)
	ann.Signature = hex.EncodeToString(sig)
	ann.VerificationClass = VerificationClassCryptoPeerSignature
	return ann, nil
}

func validateAnnouncementEndpoints(ann CapabilityAnnouncement) error {
	validCount := 0
	for _, a := range ann.Addrs {
		if _, pid, ok := ParseTCPMultiaddr(a); ok {
			if pid != ann.PeerID {
				return fmt.Errorf("%w: tcp multiaddr peer %s != %s", ErrPeerIDMismatch, pid, ann.PeerID)
			}
			validCount++
			continue
		}
		if _, pid, ok := ParseUDPMultiaddr(a); ok {
			if pid != ann.PeerID {
				return fmt.Errorf("%w: udp multiaddr peer %s != %s", ErrPeerIDMismatch, pid, ann.PeerID)
			}
			validCount++
			continue
		}
		return fmt.Errorf("%w: malformed multiaddr %q", ErrMissingEndpoints, a)
	}
	for _, ra := range ann.RelayAddrs {
		_, targetID, ok := ParseCircuitMultiaddr(ra)
		if !ok {
			return fmt.Errorf("%w: malformed circuit multiaddr %q", ErrMissingEndpoints, ra)
		}
		if targetID != ann.PeerID {
			return fmt.Errorf("%w: circuit multiaddr target %s != %s", ErrPeerIDMismatch, targetID, ann.PeerID)
		}
		validCount++
	}
	if validCount == 0 {
		return ErrMissingEndpoints
	}
	return nil
}

// VerifyAnnouncementStateless performs full fail-closed stateless validation:
// protocol version, monotonic seq >= 1, nonce presence, TTL/expiry, clock skew,
// endpoint binding, PeerID-to-Ed25519 public key binding, and Ed25519 signature.
func VerifyAnnouncementStateless(ann CapabilityAnnouncement, nowUnix int64, policy VerifyPolicy) error {
	if policy.DisableVerification {
		// Explicit negative-control mode: skips cryptographic & freshness checks.
		return nil
	}
	if ann.ProtocolVersion != ProtocolVersion {
		return ErrInvalidProtocolVersion
	}
	if ann.Seq == 0 {
		return ErrInvalidSequence
	}
	if strings.TrimSpace(ann.Nonce) == "" {
		return ErrMissingNonce
	}
	if ann.ExpiresAt <= ann.IssuedAt || nowUnix > ann.ExpiresAt {
		return ErrAnnouncementExpired
	}
	maxSkew := policy.MaxClockSkewSec
	if maxSkew <= 0 {
		maxSkew = DefaultMaxClockSkewSec
	}
	if ann.IssuedAt > nowUnix+maxSkew {
		return ErrClockSkewFuture
	}
	if policy.MaxTTLSec > 0 && (ann.ExpiresAt-ann.IssuedAt) > policy.MaxTTLSec {
		return ErrAnnouncementExpired
	}
	if err := validateAnnouncementEndpoints(ann); err != nil {
		return err
	}
	pub, err := DecodePublicKeyHex(ann.PublicKey)
	if err != nil {
		return err
	}
	if err := VerifyPeerIDBinding(ann.PeerID, pub); err != nil {
		return err
	}
	sig, err := hex.DecodeString(strings.TrimSpace(ann.Signature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrInvalidSignature
	}
	payload, err := CanonicalAnnouncementBytes(ann)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return ErrInvalidSignature
	}
	return nil
}

// SupportsModel reports whether ann advertises modelID and returns its capability.
func (ann CapabilityAnnouncement) SupportsModel(modelID string) (ModelCapability, bool) {
	for _, m := range ann.Models {
		if m.ModelID == modelID {
			return m, true
		}
	}
	return ModelCapability{}, false
}

// CloneAnnouncement returns a deep copy so callers cannot mutate cached slices.
func CloneAnnouncement(ann CapabilityAnnouncement) CapabilityAnnouncement {
	cp := ann
	if ann.Addrs != nil {
		cp.Addrs = append([]string(nil), ann.Addrs...)
	}
	if ann.RelayAddrs != nil {
		cp.RelayAddrs = append([]string(nil), ann.RelayAddrs...)
	}
	if ann.Models != nil {
		cp.Models = append([]ModelCapability(nil), ann.Models...)
	}
	return cp
}

// RejectReason classifies a verification failure into a canonical threat bucket.
func RejectReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrPeerIDMismatch):
		return "peer_id_mismatch"
	case errors.Is(err, ErrInvalidSignature), errors.Is(err, ErrInvalidPublicKey):
		return "invalid_signature"
	case errors.Is(err, ErrAnnouncementExpired):
		return "expired_ttl"
	case errors.Is(err, ErrClockSkewFuture):
		return "clock_skew_future"
	case errors.Is(err, ErrSequenceRollback), errors.Is(err, ErrInvalidSequence):
		return "sequence_rollback"
	case errors.Is(err, ErrReplayNonce), errors.Is(err, ErrMissingNonce):
		return "replay_nonce"
	case errors.Is(err, ErrSybilFloodDetected):
		return "sybil_flood"
	default:
		return "malformed_announcement"
	}
}
