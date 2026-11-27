// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MeshExportBundle is a cryptographically signed, self-contained snapshot of a
// node's Kademlia routing table, verified capability announcements, and
// bootstrap peer list. Any independent operator can verify it offline and
// cold-start a forked node with zero Hanzo control-plane calls.
type MeshExportBundle struct {
	ProtocolVersion   string                   `json:"protocol_version"`
	ExportedAt        int64                    `json:"exported_at"`
	ExporterPeerID    PeerID                   `json:"exporter_peer_id"`
	ExporterPublicKey string                   `json:"exporter_public_key"`
	BootstrapPeers    []PeerInfo               `json:"bootstrap_peers"`
	RoutingTable      []PeerInfo               `json:"routing_table"`
	Announcements     []CapabilityAnnouncement `json:"announcements"`
	StateDigest       string                   `json:"state_digest"`
	Signature         string                   `json:"signature"`
	VerificationClass VerificationClass        `json:"verification_class"`
}

type bundleStatePayload struct {
	ProtocolVersion string                   `json:"protocol_version"`
	ExportedAt      int64                    `json:"exported_at"`
	ExporterPeerID  PeerID                   `json:"exporter_peer_id"`
	BootstrapPeers  []PeerInfo               `json:"bootstrap_peers"`
	RoutingTable    []PeerInfo               `json:"routing_table"`
	Announcements   []CapabilityAnnouncement `json:"announcements"`
}

func computeBundleDigest(b MeshExportBundle) (string, error) {
	raw, err := json.Marshal(bundleStatePayload{
		ProtocolVersion: b.ProtocolVersion,
		ExportedAt:      b.ExportedAt,
		ExporterPeerID:  b.ExporterPeerID,
		BootstrapPeers:  b.BootstrapPeers,
		RoutingTable:    b.RoutingTable,
		Announcements:   b.Announcements,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalBundleSignBytes(b MeshExportBundle) []byte {
	msg := fmt.Sprintf("%s%s|%d|%s|%s",
		domainMeshExportBundle,
		b.ProtocolVersion,
		b.ExportedAt,
		b.ExporterPeerID,
		b.StateDigest,
	)
	return []byte(msg)
}

// ExportMeshBundle produces a deterministically ordered, Ed25519-signed
// MeshExportBundle snapshot of this node's routing table and verified DHT state.
func (n *Node) ExportMeshBundle(nowUnix int64) (MeshExportBundle, error) {
	if nowUnix <= 0 {
		nowUnix = n.nowUnix()
	}
	peers := n.Table.AllPeers()
	sort.Slice(peers, func(i, j int) bool { return peers[i].PeerID < peers[j].PeerID })

	bootstraps := make([]PeerInfo, 0, len(peers))
	for _, p := range peers {
		if p.NATMode != NATRelayed && len(p.Addrs) > 0 {
			bootstraps = append(bootstraps, p)
		}
	}
	// Include self as a reachable seed if this node has direct addresses.
	selfInfo := n.PeerInfo()
	if selfInfo.NATMode != NATRelayed && len(selfInfo.Addrs) > 0 {
		bootstraps = append(bootstraps, selfInfo)
		sort.Slice(bootstraps, func(i, j int) bool { return bootstraps[i].PeerID < bootstraps[j].PeerID })
	}

	anns := n.Store.ListAnnouncements(nowUnix)

	bundle := MeshExportBundle{
		ProtocolVersion:   ProtocolVersion,
		ExportedAt:        nowUnix,
		ExporterPeerID:    n.ID.PeerID,
		ExporterPublicKey: n.ID.PublicKeyHex(),
		BootstrapPeers:    bootstraps,
		RoutingTable:      peers,
		Announcements:     anns,
		VerificationClass: VerificationClassCryptoPeerSignature,
	}
	digest, err := computeBundleDigest(bundle)
	if err != nil {
		return MeshExportBundle{}, err
	}
	bundle.StateDigest = digest
	sig := ed25519.Sign(n.ID.PrivateKey, canonicalBundleSignBytes(bundle))
	bundle.Signature = hex.EncodeToString(sig)
	return bundle, nil
}

// VerifyMeshBundle performs strict offline cryptographic verification of a
// MeshExportBundle: exporter PeerID binding, state digest integrity, Ed25519
// bundle signature, and every contained CapabilityAnnouncement.
func VerifyMeshBundle(bundle MeshExportBundle, nowUnix int64, policy VerifyPolicy) error {
	if bundle.ProtocolVersion != ProtocolVersion {
		return ErrInvalidProtocolVersion
	}
	pub, err := DecodePublicKeyHex(bundle.ExporterPublicKey)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBundleTampered, err)
	}
	if err := VerifyPeerIDBinding(bundle.ExporterPeerID, pub); err != nil {
		return fmt.Errorf("%w: %v", ErrBundleTampered, err)
	}
	expectedDigest, err := computeBundleDigest(bundle)
	if err != nil {
		return err
	}
	if bundle.StateDigest != expectedDigest {
		return fmt.Errorf("%w: digest mismatch", ErrBundleTampered)
	}
	sig, err := hex.DecodeString(strings.TrimSpace(bundle.Signature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed signature", ErrBundleTampered)
	}
	if !ed25519.Verify(pub, canonicalBundleSignBytes(bundle), sig) {
		return fmt.Errorf("%w: signature invalid", ErrBundleTampered)
	}
	for _, ann := range bundle.Announcements {
		if err := VerifyAnnouncementStateless(ann, nowUnix, policy); err != nil {
			return fmt.Errorf("%w: invalid announcement for %s: %v", ErrBundleTampered, ann.PeerID, err)
		}
	}
	return nil
}

// ForkFromBundle verifies bundle offline, starts an isolated sovereign Node
// with HanzoRegistryDisabled=true, hydrates its Kademlia routing table and
// verified DHT announcement store from the bundle, and returns it ready to
// route requests with HanzoCalls == 0.
func ForkFromBundle(ctx context.Context, cfg NodeConfig, bundle MeshExportBundle, nowUnix int64) (*Node, error) {
	if nowUnix <= 0 {
		nowUnix = bundle.ExportedAt
	}
	policy := cfg.Policy
	if policy.MaxClockSkewSec == 0 && !policy.DisableVerification {
		policy = DefaultVerifyPolicy()
		cfg.Policy = policy
	}
	if err := VerifyMeshBundle(bundle, nowUnix, policy); err != nil {
		return nil, err
	}
	cfg.HanzoRegistryDisabled = true
	node, err := NewNode(cfg)
	if err != nil {
		return nil, err
	}
	if err := node.Start(ctx); err != nil {
		return nil, err
	}

	for _, p := range bundle.BootstrapPeers {
		p.LivenessVerified = false
		p.RTTMs = 0
		p.ConsecutiveFailures = 0
		node.Table.UpsertPeer(p)
	}
	for _, p := range bundle.RoutingTable {
		p.LivenessVerified = false
		p.RTTMs = 0
		p.ConsecutiveFailures = 0
		node.Table.UpsertPeer(p)
	}
	for _, ann := range bundle.Announcements {
		if ann.PeerID == node.ID.PeerID {
			continue
		}
		if err := node.Store.PutAnnouncement(ann, "", true, nowUnix); err != nil {
			node.Shutdown()
			return nil, errors.Join(ErrBundleTampered, err)
		}
	}
	return node, nil
}
