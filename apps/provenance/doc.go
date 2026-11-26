// SPDX-License-Identifier: Apache-2.0

// Package provenance implements clean-room verifiable build provenance for
// agent-produced artifacts on the Hanzo forge (HIP-1349).
//
// It combines four cryptographic building blocks using only the Go standard
// library and the zip HTTP framework (zero external cryptographic dependencies):
//
//  1. SLSA v1.2 in-toto Statements (slsa.go): canonical
//     https://in-toto.io/Statement/v1 envelopes carrying
//     https://slsa.dev/provenance/v1.2 predicates that bind artifact SHA-256
//     digests to exact source repository URIs, Git commit/tree revisions,
//     agent/builder identities, and pinned toolchain digests.
//  2. DSSE Signing & Fulcio-Style Short-Lived X.509 Certificates (dsse.go):
//     canonical DSSEv1 Pre-Authentication Encoding (PAE) over
//     application/vnd.in-toto+json payloads using Ed25519 and ECDSA P-256,
//     plus short-lived code-signing X.509 certificates carrying OIDC SAN
//     identity claims and Fulcio-compatible OID extensions verified at
//     transparency-log integrated time.
//  3. Append-Only RFC-6962 Merkle Transparency Log (tlog.go): clean-room
//     Rekor-style transparency log with domain-separated SHA-256 leaf (0x00)
//     and interior (0x01) hashing, inclusion proof generation and verification,
//     consistency proof verification across checkpoints, and Ed25519 Signed
//     Tree Heads (STHs) that prevent equivocation and backdating.
//  4. Fail-Closed Verification Policy Engine & Typed HTTP Surface
//     (verifier.go, http.go): enforces artifact digest integrity, PAE and
//     certificate/key authenticity, builder/source/toolchain pins, transparency
//     log inclusion and consistency, and key/identity revocation, mounted on
//     zip.App at /v1/provenance with strict authenticated caller/org isolation.
package provenance
