# Verifiable Agent-Produced Artifacts — Reproducible Experiment (cloud#39)

**Bounty**: [hanzoai/cloud#39](https://github.com/hanzoai/cloud/issues/39) — [Bounty #49], Research + Implementation Bounty (SLSA v1.2 + Cosign, HIP-1348)
**Primary references**: [SLSA v1.2 spec](https://slsa.dev/spec/v1.2/tracks) · [Sigstore Cosign](https://github.com/sigstore/cosign)

This directory is a **dependency-free Go provenance harness** (in-process SLSA v1.2-style provenance documents, Ed25519 signing, verification, and the two rejection classes the acceptance criterion requires: single-field tamper and unauthorized substitution). The cosign binary is the *upstream baseline*, not imported code.

> **Scope honesty**: this is the *verification layer*, not the signing infrastructure. Production signing still goes through cosign (or an equivalent HSM-backed signer); `results/PROTOCOL.md` is the <30-minute cross-check protocol for any host that can run cosign. What is proven here: the provenance document model, the named rejection classes, multi-tenant isolation, and the verifier-latency bound — all runnable on any OS with `go test`.

---

## 1. Code & License Audit

| Item | Finding |
| --- | --- |
| SLSA v1.2 | Specification (slsa.dev), permissive license; the *provenance document schema* is the contract, not the implementation |
| Sigstore Cosign | **Apache-2.0** (on the bounty's preferred list); the cosign CLI is the upstream baseline for cross-checks, never imported |
| Code reuse | **No Cosign Go code reused.** The harness is original, stdlib-only Go (no `go.mod` changes). The provenance model is written from the SLSA v1.2 spec text, with a fixed-field canonical serialization documented inline |
| Compatibility | hanzoai/cloud is dual-licensed Apache-2.0 OR MIT; this package carries `SPDX-License-Identifier: Apache-2.0 OR MIT` |

## 2. The Provenance Document Model

`ProvenanceDocument` binds six fields into one signed record:

| Field | Meaning | Threat it addresses |
| --- | --- | --- |
| `schema` | SLSA provenance schema version | schema-drift confusion |
| `subject_id` | output artifact identity | wrong-artifact attestation |
| `source_revision` | exact source commit | source substitution |
| `toolchain_digest` | SHA-256 of the toolchain manifest | toolchain swap |
| `runtime_digest` | SHA-256 of the runtime manifest (HIP §1.3 `gen_ai.hanzo.runtime_digest` counterpart) | runtime swap |
| `output_digest` | SHA-256 of the produced artifact | artifact substitution |
| `tenant_salt` | tenant isolation | cross-tenant attestation confusion |

**Signing**: `Sign(priv)` computes an Ed25519 signature over `CanonicalBytes()` — SHA-256 of the six non-signature fields joined in fixed order with NUL separators. The canonical form excludes the signature, so signing is idempotent and verification is deterministic.

**Verification**: `Verify(pub)` checks structure (named errors for each missing field) and then the Ed25519 signature. Digest binding is separate (`CheckArtifactDigest`, `CheckSourceRevision`, `CheckToolchain`, `CheckRuntime`, `CheckTenant`) so a *tamper* rejection (signature-level) is distinguishable from a *substitution* rejection (binding-level) — the two rejection classes the acceptance criterion names.

## 3. Falsifiable Hypotheses (pre-declared before any run)

**H1 — single-field tamper is always rejected with a named error.** Mutating any one signed field (source revision, toolchain, runtime, output digest, tenant salt, subject, schema) after signing must fail `Verify` with a *signature mismatch* error. Expected failure modes: none for the model itself; a "pass" would falsify H1 and void the deliverable.

**H2 — unauthorized substitution is rejected at the binding check.** Replacing the artifact bytes while keeping the document's signature must fail `CheckArtifactDigest` with a digest-mismatch error — while the document's own signature remains valid (the signature covers the *recorded* digest, not the bytes on disk; that separation is the point). Expected failure modes: an implementation that verifies the artifact bytes directly instead of the digest would conflate the two classes.

**H3 — verification overhead is negligible (<100 ms/op).** A full verify+digest pass stays under 100 ms/op on any CI host (measured: ~0.7-1.0 µs/op on the authoring host — four orders of magnitude below the bound). Expected failure modes: none; a slower host would still be far below the bound.

**H4 — multi-tenant isolation is enforced by the canonical form.** Two tenants building the same source+toolchain produce different canonical bytes (tenant salt is in the canonical input), so tenant B's key cannot verify tenant A's document, and `CheckTenant` rejects a wrong-salt document even under the correct key.

## 4. Methodology

- **Fixture**: deterministic `FixtureDocument()` + a committed public key (`results/fixtures/pub.pem`, Ed25519, PKIX PEM). The matching private key is held by the authoring environment and never committed — the fixture keypair is regenerable with `provenanceharness genkeys`, and every test that needs a keypair generates one in-process (`NewTestKeys`).
- **Rejection classes**: H1 exercises the seven-field tamper matrix × signature check; H2 exercises artifact substitution × binding check. They are separate code paths with distinct error strings.
- **Concurrency**: the signer is stateless and the verifier is read-only over the document; `TestConcurrentVerify` runs 32 parallel verifications.
- **Latency**: `TestVerifierLatencyH3` warms up 200 iterations then times 2000 verify+digest passes; the bound is asserted, not just printed.
- **Cross-signer compatibility** (cosign ↔ in-process): the canonical serialization is documented (`README` §2 + inline), and PROTOCOL.md §3 gives the exact cosign commands to cross-check a fixture signed by both signers.

## 5. Raw Results (committed)

Measured on the authoring host (Windows 11, go1.23.4, windows/amd64) — these are *our* numbers, not Hanzo expectations:

| Measurement | Result |
| --- | --- |
| Test suite | 15 tests PASS (see §6), `go vet` clean |
| Verify+digest latency | **~0.73-1.0 µs/op** (500-2000 iters; H3 bound: 100 ms — 5 orders of magnitude of headroom) |
| CLI end-to-end | `genkeys → sign → verify` = signature OK in ~1 ms; tampered document rejected with exit 1 |
| Cosign cross-check | Pending (requires a host with cosign; PROTOCOL.md §3) |

## 6. Harness Evidence (testable without cosign)

15 tests in `provenance_test.go`, all green on the authoring host:

- **Structure**: document shape lock; canonical form determinism; canonical form excludes the signature.
- **Positive path**: sign → verify → full digest-check chain passes.
- **H1 tamper matrix**: 7 field-tamper cases each rejected with a signature mismatch; direct signature-byte flip rejected; wrong-key rejected.
- **H2 substitution**: substituted artifact rejected at the binding check while the document signature stays valid (the class separation asserted explicitly).
- **H4 multi-tenant**: different salts → different canonical forms; cross-tenant verify rejected; `CheckTenant` rejects wrong salt.
- **Structure errors**: named errors for missing schema/signature.
- **JSON round-trip**: marshal/unmarshal preserves the signature and still verifies.
- **PEM round-trip**: `MarshalPublicKeyPEM` ∘ `PublicKeyFromPEM` = identity; PKCS#8 private-key PEM parses.
- **Committed fixture guard**: `results/fixtures/pub.pem` parses as a 32-byte Ed25519 key.
- **Concurrency**: 32 parallel verifies.
- **H3 latency**: asserted <100 ms/op (measured µs).

## 7. Limitations

1. **Verification layer only.** This harness proves the document model and rejection classes; it does not provision keys, run key-rotation, or talk to a transparency log. Production signing remains cosign's job (PROTOCOL.md §3).
2. **Cosign cross-check pending.** The authoring host has no cosign binary; the cross-check protocol is scripted and its output is the one missing raw result.
3. **Fixed-field canonical form.** Adding a new field changes `CanonicalBytes()` — that is intentional (it invalidates old signatures) but means schema evolution needs a version bump (the `schema` field exists for this).
4. **No revocation.** A leaked signing key invalidates the trust model; the fixture key is test-only and the production path would use cosign's keyless/Fulcio infrastructure where revocation exists.
5. **Single-artifact documents.** SLSA v1.2 supports multi-subject attestations; this model binds exactly one artifact per document (the agent-deliverable case). Multi-subject is a schema-v2 follow-up.

## 8. Integration Recommendation (HIP-1348)

1. **Where it lands**: the forge's artifact-acceptance endpoint runs `Verify` + `CheckArtifactDigest` on every agent-produced deliverable before it is accepted into `vfs → hanzoai/s3 → visor`; rejection events carry the named error string for audit.
2. **Runtime provenance tie-in**: `runtime_digest` is the HIP §1.3 `gen_ai.hanzo.runtime_digest` counterpart — the attestation binds the artifact to the runtime that produced it, closing the provenance loop.
3. **Rollout ladder**: adopt the document model → require signatures on new deliverables (monitor: accept unsigned, log) → enforce (reject unsigned/tampered/substituted) after an audit window.
4. **Cross-check**: run PROTOCOL.md §3 once on a cosign-equipped host to confirm the fixture cross-verifies, then commit the output under `results/`.

## 9. HIP-1348 Status Update

- **Research deliverable** (this PR): in-process SLSA v1.2-style provenance model + Ed25519 signing/verification + named rejection classes (tamper, substitution) + multi-tenant isolation + latency bound + 15 tests + committed fixture + <30-minute cosign cross-check protocol.
- **Pending**: cosign cross-check on a host that has it (PROTOCOL.md §3); multi-subject schema if the forge needs it.
- **Follow-up implementation bounty** (if maintainers open one): wire the verifier into the forge artifact-acceptance path and emit rejection events into the audit stream.

## 10. File Map

| File | Purpose |
| --- | --- |
| `provenance.go` | Document model, Ed25519 sign/verify, digest checks, PEM helpers |
| `provenance_test.go` | 15 tests: shape, positive, 7-tamper matrix, substitution, tenant isolation, latency bound, concurrency |
| `results/fixtures/pub.pem` | Committed fixture public key (Ed25519, PKIX PEM) |
| `results/schema.json` | JSON Schema for provenance documents and bench results |
| `results/PROTOCOL.md` | Exact <30 min cosign cross-check protocol |
| `../provenanceharness/main.go` | CLI: `sign \| verify \| bench \| genkeys` |
