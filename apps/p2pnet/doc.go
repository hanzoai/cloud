// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package p2pnet implements a decentralized peer discovery and model-routing
// protocol for HIP-1350 using self-certifying Ed25519 peer identities,
// authenticated capability announcements, an XOR-metric Kademlia DHT, gossip
// dissemination, mutual-TLS 1.3 loopback transport, UDP/QUIC-ready datagram
// probes, and circuit-relay NAT traversal.
//
// Zero central registration is required: any node can join via arbitrary
// bootstrap peers, discover providers for AI models, and continue routing
// requests after all bootstrap and intermediate routing nodes are removed.
//
// Proof Hygiene:
//   - VerificationClassCryptoPeerSignature ("crypto_peer_signature") proves
//     Ed25519 key control, PeerID-to-public-key binding, payload integrity,
//     freshness (TTL + clock-skew bounds), monotonic sequencing, and
//     anti-replay nonce uniqueness.
//   - VerificationClassNetworkLivenessProbe ("network_liveness_probe") proves
//     current network reachability over an authenticated mutual-TLS 1.3 or
//     signed UDP probe handshake.
//   - VerificationClassHardwareAttestation ("hardware_attestation") is an
//     optional external TEE quote field and is never claimed unless externally
//     attested.
//
// Package p2pnet imports only the Go standard library, github.com/zap-proto/zip,
// and github.com/luxfi/log, preserving full host independence.
package p2pnet
