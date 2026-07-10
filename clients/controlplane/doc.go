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
//  2. Each voter INDEPENDENTLY re-aggregates the collected legs and re-composes
//     the cert; no voter — not even the proposer — is trusted for the cert. It
//     then runs a STRUCTURAL well-formedness check on the cert it composed ITSELF
//     (verifyOwnCertStructure — QuasarCert.Verify checks the triple is present
//     and the signer count is consistent, NOT signatures). An externally received
//     cert must instead go through the cryptographic quasar verify
//     (VerifyUnderPolicy) — increment-2.
//  3. One pod = one voter = one share. Share custody is 1:1 and structurally
//     enforced (custody.go); a second share for the same node is refused.
//  4. Honest voters refuse to sign (Round1) a block whose placement ops violate
//     control-plane invariants (policy.go) — the policy gate runs BEFORE the
//     crypto, so an invariant-violating block cannot even collect commitments.
//     Displacement of a LIVE shard writer is fail-closed: only out-of-band
//     proven-death authorizes it; a proposer-written release is not consent, and
//     membership removal never manufactures proven-dead.
//  5. Commit-reveal (Round1 commitment) + an ALL-Round1 barrier deny a rushing
//     voter any adaptive advantage: no Round2 share is released until a quorum of
//     Round1 commitments is locked, and each revealed share must open its own
//     commitment. Round1 commitments are proof-of-possession authenticated (as
//     Round2 legs are), so one node cannot forge a quorum of spoofed commitments
//     to cross the barrier early.
//  6. Aggregation dedupes by signer index and rejects rogue/unregistered legs
//     (proof-of-possession) before counting quorum, and the BFT quorum
//     (>2N/3) makes two conflicting digests at one height mathematically
//     unable to both finalize. checkQuorumSafety asserts the 2q>N+f intersection
//     bound at construction so this can never silently regress.
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
//	ShareCustody  seam in custody.go. The per-pod IDENTITY key is now a REAL
//	              random ML-DSA-65 keypair with asymmetric PoP verify (seam a,
//	              landed). Still stub: it is in-memory (not KMS-sealed) and the
//	              z-share is seed-derived. Real drop-in: KMS-sealed per-pod share
//	              custody (KMSSecret CRD) + write-fence — seam b.
//	Transport     seam in transport.go. Stub: in-memory broadcast bus. Real
//	              drop-in: ZAP messaging.
//	ControlDB     seam in placement.go. Stub: in-memory map. Real drop-in:
//	              the control.db persistence backend.
//
// # Adversarial review (red → blue) — what holds, what is deferred
//
// A red-team pass drove all eight priority vectors (red_exploits_test.go). The
// NO-FORK CORE HELD under a hostile async transport: two honest voters may
// compose byte-different certs at one height, but they apply the IDENTICAL block
// (the RSM consumes the block, not the cert), and 2·quorum > N+f forces any two
// quorums to intersect in a correct voter — so state cannot fork. Four class-A
// breaks (pure orchestration/policy, independent of the crypto) were found and
// CLOSED: the forged-release / ungated-membership DOUBLE-WRITE (now fail-closed
// displacement), the forgeable anti-rush barrier (now Round1 proof-of-possession),
// and the apply-boundary fork gate (now ParentRoot-checked); the self-composed
// cert check is documented structural-only.
//
// CLASS-B — PoP DISCHARGED (seam a): the per-pod IDENTITY key is now a fresh
// random ML-DSA-65 keypair (custody.go), never seed-derived, so proof-of-
// possession is unforgeable. The byzantine-safety tests are therefore now
// MEANINGFUL and GREEN: the two TestRed_B_* cases are un-skipped and assert the
// closed defence, and TestSafety_RogueAndForgedLegs_Rejected forges a
// correctly-derived leg (a valid ML-DSA signature under an attacker-generated
// key, not a bit-flip) and still rejects it. RESIDUAL (closes in seam c): the
// z-share / threshold-signing material is still seed-derived, so the cert crypto
// (SignatureCore / composer) stays stub and ProductionBCCSigningReady() stays
// false until the real cert lands.
//
// # Containment (closed)
//
// Three independent controls now hold this package out of any real write path
// while its crypto is stub, on top of the build-tag isolation described above:
//
//  1. CI guard — .github/workflows/containment.yml runs on every PR: a grep
//     failing the build if any Dockerfile/Makefile/script/workflow anywhere in
//     the repo passes `-tags controlplane` to go build/vet/run, PLUS a positive
//     proof that `go build ./...` (no tag) links clients/controlplane into no
//     cmd/ main and that `go build ./clients/controlplane/...` (no tag) still
//     matches zero buildable packages.
//  2. Runtime fail-closed assert — containment.go's mustHarnessOnly panics the
//     instant this package's stub crypto is touched (package-import-time for
//     the PartialZVerifier registration in signer.go's init, construction-time
//     for NewSigner/NewStubComposer) UNLESS ProductionBCCSigningReady() is true
//     (hardcoded false; flips only when increment-2's real crypto replaces the
//     stubs) OR the calling binary is a go-test harness (testing.Testing() —
//     the Go toolchain's own signal, not a flag or env var this package reads,
//     so a `go build`/`go run` serve binary can never satisfy it). Proven by a
//     real subprocess in TestContainment_NonHarnessProcessRefuses
//     (containment_test.go), not just in-process logic.
//  3. External-cert seam — CertComposer.Compose returns selfComposedCert (an
//     unexported wrapper only Compose can produce), and verifyOwnCertStructure
//     accepts ONLY that type — never a bare *quasar.QuasarCert. An externally-
//     received cert (deserialized off the wire on a future recovery/light-
//     client/gossip path) has no way to become a selfComposedCert; the ONLY
//     exported seam for such a cert is VerifyExternalCert (driver.go), which is
//     intentionally unimplemented and fails closed today rather than silently
//     falling back to the structural check. Locked from a black-box vantage
//     (exported-surface-only) by TestExternalCert_MustNotAdmitThroughStructuralCheck
//     (external_cert_test.go).
//
// # Increment-2 security work (before this guards a real KMS shard lease)
//
//   - Real distributed DKG across pods (not in-process, not seed-derived shares)
//   - [DONE — seam a] asymmetric ML-DSA-65 identity keys
//     (github.com/luxfi/crypto/mldsa), so leg/commit proof-of-possession is
//     unforgeable and the byzantine-safety suite regains meaning (the two
//     TestRed_B_* cases are un-skipped and green).
//   - Authenticated graceful handoff (holder self-resignation) and a KMS
//     write-fence: reassigning a LIVE writer requires proof its prior lease epoch
//     was revoked at the data plane FIRST (fence-before-reassign), tied to the
//     strict-`<` epoch-CAS the data plane needs anyway.
//   - RSM-level authorization re-verification (thread the registry into Apply so
//     a future recovery path re-checks displacement authorization, not just
//     ordering + invariants).
//   - VerifyExternalCert's real implementation: route to the cryptographic
//     quasar VerifyUnderPolicy / VerifyWithRealKeys — never QuasarCert.Verify —
//     the moment any recovery/light-client/gossip path needs to accept a peer's
//     cert. The seam and its fail-closed default already exist (see above); only
//     the crypto call is missing.
//   - Flip ProductionBCCSigningReady() to true in the SAME change that deletes
//     the stub types it currently guards (acceptBindingsOnly, stubSignatureCore,
//     stubComposer) — never independently of them.
package controlplane
