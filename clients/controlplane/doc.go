//go:build controlplane

// Package controlplane is Stage-1 increment-1 of the Hanzo cloud
// consensus-plugin platform: a FULL-BFT byzantine ceremony driver for the
// control plane, built against stable published luxfi interfaces plus stubs
// for the one cryptographic implementation that is still being forward-ported.
//
// It is gated behind the `controlplane` build tag and is NOT wired into the
// serving path. Nothing in the default cloud build imports it; it compiles,
// vets, and tests only under `-tags controlplane`. This is the protocol driver
// and its in-process N-voter harness — real ZAP transport, real KMS share
// custody, and the full red-team suite are subsequent increments.
//
// # Threat model
//
//	Assets     — the control-plane placement/lease/membership state: which pod
//	             holds which secret shard, which node is a live consensus voter,
//	             which resource is leased to whom. Corrupting this state lets an
//	             adversary redirect a secret-shard writer, double-write a shard
//	             (breaking KMS share isolation), or forge membership.
//	Adversaries — up to f = floor((N-1)/3) byzantine voters (equivocation,
//	             rushing, share double-submission, rogue keys, invariant-
//	             violating proposals) plus a network that reorders/drops.
//	Surface    — the ceremony messages (proposal, Round1 commitment, Round2
//	             share) exchanged over an abstract transport, and the aggregated
//	             certificate each voter must independently accept before apply.
//
// # Design (defense in depth, no single control sufficient)
//
//  1. Every finalized block carries a fully-aggregated triple-PQ QuasarCert.
//     A block with anything less never applies.
//  2. Each voter INDEPENDENTLY re-aggregates the collected legs, re-composes
//     the cert, and runs QuasarCert.Verify before applying. No voter — not even
//     the proposer — is trusted for the cert; every voter recomputes it.
//  3. One pod = one voter = one share. Share custody is 1:1 and structurally
//     enforced (custody.go); a second share for the same node is refused.
//  4. Honest voters refuse to sign (Round1) a block whose placement ops violate
//     control-plane invariants (policy.go) — the policy gate runs BEFORE the
//     crypto, so an invariant-violating block cannot even collect commitments.
//  5. Commit-reveal (Round1 commitment) + an ALL-Round1 barrier deny a rushing
//     voter any adaptive advantage: no Round2 share is released until every
//     Round1 commitment is locked, and each revealed share must open its own
//     commitment.
//  6. Aggregation dedupes by signer index and rejects rogue/unregistered legs
//     (proof-of-possession) before counting quorum, and the BFT quorum
//     (>2N/3) makes two conflicting digests at one height mathematically
//     unable to both finalize.
//
// # Why not the base quasar engine on the write path
//
// github.com/luxfi/consensus/protocol/quasar ships NewEngine/Submit/Finalized/
// IsFinalized/SetProfile — the byzantine CRYPTO and a single-process Certifier.
// That Certifier holds the signer (all shares) in ONE process and regenerates
// the cert itself; using it as the ceremony authority would collapse the share
// isolation this package exists to guarantee (the same reason wave_signer.
// RunRound must never be on a write path). So this driver is the byzantine
// finality authority: it consumes quasar's published VALUES and pure functions
// — RoundDigest (ComputeRoundDigest), QuasarCert (Verify), and the Polaris
// composition surface — and exposes the SAME finality contract shape
// (Submit / Finalized / IsFinalized) the engine defines, with the multi-voter
// ceremony the engine lacks.
//
// # Stub boundaries (drop-in seams for later increments)
//
// Each seam is an interface with a deterministic in-package stub today and a
// documented published/real implementation later. The BYZANTINE CEREMONY LOGIC
// around every seam (barrier, quorum, dedup, proof-of-possession enforcement,
// independent verification, policy gate, deterministic apply) is REAL and
// tested — only the primitives below are stubbed:
//
//	RoundSigner   github.com/luxfi/pulsar/pkg/pulsar.RoundSigner (PUBLISHED
//	              interface). Stub: signer.go stubSigner (deterministic
//	              commit-reveal, no real lattice). Real drop-in: the
//	              protocol/quasar/pulsar PulsarRoundSigner cert-profile impl
//	              being forward-ported by the cryptographer.
//	CertComposer  seam in signer.go. Stub: structurally-valid triple-gate
//	              QuasarCert. Real drop-in: quasar.ComposePolaris over the
//	              four real threshold legs (BLS/Pulsar/Corona/Magnetar).
//	              (Verification likewise moves from the structural
//	              QuasarCert.Verify to the cryptographic VerifyWithRealKeys.)
//	ShareCustody  seam in custody.go. Stub: in-memory sealed shares. Real
//	              drop-in: KMS-sealed per-pod share custody (KMSSecret CRD).
//	Transport     seam in transport.go. Stub: in-memory broadcast bus. Real
//	              drop-in: ZAP messaging.
//	ControlDB     seam in placement.go. Stub: in-memory map. Real drop-in:
//	              the control.db persistence backend.
package controlplane
