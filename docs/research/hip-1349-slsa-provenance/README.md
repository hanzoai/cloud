# HIP-1349 Research Dossier: Verifiable Agent-Produced Artifacts (SLSA v1.2 Provenance & Sigstore Model)

- **Tracking Issue**: [`hanzoai/cloud#32`](https://github.com/hanzoai/cloud/issues/32) (`p2, bounty, research, hip-1349, test`)
- **Canonical Owning Repository**: `hanzoai/cloud` (`apps/provenance`, `cmd/provenance-bench`)
- **Disposition Target**: `MERGE` (bounded clean-room implementation, zero new external dependencies, full race/security/regression verification, raw benchmark receipts in [`results.json`](./results.json))

---

## 1. Primary References, Architecture Summary & License Audit

### Primary Specifications & Upstream References
1. **SLSA v1.2 Specification (Tracks & Provenance v1.2)**: <https://slsa.dev/spec/v1.2/tracks> and <https://slsa.dev/spec/v1.2/provenance>
   - Defines Build Track levels (Build L1–L3), canonical `https://in-toto.io/Statement/v1` envelope structure, and `https://slsa.dev/provenance/v1.2` predicate splitting caller-controlled `buildDefinition.externalParameters` from platform-attested `runDetails.builder` and `runDetails.metadata`.
2. **in-toto Attestation Framework & DSSE v1 Protocol**: <https://github.com/in-toto/attestation> and <https://github.com/secure-systems-lab/dsse/blob/master/protocol.md>
   - Defines canonical Pre-Authentication Encoding (`PAE(payloadType, payload) = "DSSEv1" SP LEN(type) SP type SP LEN(body) SP body`) to prevent payload-type confusion and cross-protocol signature replay.
3. **Sigstore (Cosign, Fulcio, Rekor)**: <https://github.com/sigstore/cosign>, <https://github.com/sigstore/fulcio>, <https://github.com/sigstore/rekor>
   - Keyless signing architecture: an ephemeral key pair signs a DSSE envelope, an OIDC-authenticated CA (Fulcio) issues a short-lived X.509 code-signing certificate carrying SAN identity claims and Fulcio OIDs (`1.3.6.1.4.1.57264.1.*`), and an append-only RFC 6962 Merkle transparency log (Rekor) stamps a monotonic `IntegratedTime` and returns an inclusion proof + Signed Tree Head (STH). Offline verifiers validate the X.509 chain at `IntegratedTime` rather than current wall-clock time.
4. **RFC 6962 — Certificate Transparency Merkle Trees**: <https://datatracker.ietf.org/doc/html/rfc6962>
   - Defines domain-separated leaf (`SHA-256(0x00 || leaf)`) and interior (`SHA-256(0x01 || left || right)`) hashes preventing second-preimage attacks, plus $O(\log N)$ inclusion proofs and consistency proofs detecting split-view equivocation.

### Clean-Room License & Dependency Audit

| Reference / Dependency | Upstream License | Imported Code? | Audit Result |
|---|---|---|---|
| SLSA v1.2 Specification | Apache-2.0 | None (spec-only clean-room Go) | **Clear** |
| DSSE v1 Protocol Specification | Apache-2.0 | None (spec-only clean-room Go) | **Clear** |
| Sigstore Cosign / Fulcio / Rekor | Apache-2.0 | None (zero external Sigstore SDK deps) | **Clear** |
| RFC 6962 Merkle Tree Specification | IETF Trust | None (clean-room Go stdlib `crypto/sha256`) | **Clear** |
| `apps/provenance` implementation | Apache-2.0 | Go stdlib + existing `github.com/zap-proto/zip` & `github.com/luxfi/log` | **Zero new `go.mod` / `go.sum` entries** |

No third-party Sigstore, TUF, protobuf, or Trillian packages were added to `go.mod`. Every cryptographic primitive (`crypto/ed25519`, `crypto/ecdsa` P-256, `crypto/x509`, `crypto/sha256`, `crypto/subtle`, `encoding/asn1`) comes strictly from the Go 1.27.1 standard library.

---

## 2. Reproducible Baseline & Evaluation Corpus

To comply with `PR_CONVERGENCE.md` (public reproducibility on real artifacts rather than synthetic-only claims), [`cmd/provenance-bench`](../../../cmd/provenance-bench/main.go) evaluates **12 real repository artifacts** totaling **68,633,041 bytes** (`65.45 MiB`), anchored to the actual compiled `cmd/cloud` Go ELF binary (`bin/cloud`, `68,374,667` bytes), real repository manifests (`go.mod`, `go.sum`, `Makefile`, `README.md`, `apps/apps.go`, `apps/template/catalog.json`), and the 5 source files of `apps/provenance/`. The toolchain digest is hashed directly from the on-disk Go compiler binary (`go1.27.1`, `17,142,188` bytes, SHA-256 `30969f97...5df4624`), and source revision pins are read from `git rev-parse HEAD` (`0f3125493818da9f324b83c6ae62dea2297be530`) and `HEAD^{tree}` (`29b42374a792e1deab0e814285c2657702eacac7`).

### Exact Reproduction Command
```bash
CGO_ENABLED=0 go build -o /tmp/cloud ./cmd/cloud && \
go run ./cmd/provenance-bench \
  -repo . \
  -binary /tmp/cloud \
  -iterations 250 \
  -small-files 1000 \
  -seed 1349 \
  -out docs/research/hip-1349-slsa-provenance/results.json && \
rm -f /tmp/cloud
```

- **Host environment**: `linux/amd64`, 2 vCPUs, `go1.27.1`, OS-thread-pinned monotonic timing (`runtime.LockOSThread()`), 250 measured iterations per operation.
- **Unsigned baseline**: streaming SHA-256 over `bin/cloud` (`68.37 MB`) using a constant `32 KiB` I/O buffer (`provenance.DigestFile`).

---

## 3. Falsifiable Hypothesis, Metrics & Expected Failure Modes

### Hypothesis
> **H1**: Replacing unsigned artifact digests on the Hanzo forge with a clean-room SLSA v1.2 + DSSEv1 + Fulcio-style short-lived X.509 certificate + RFC 6962 Merkle transparency log pipeline adds **< 1.0 ms** p50 attestation latency and **< 1.0 ms** p50 verification latency (**< 0.5%** of baseline binary SHA-256 hashing cost), adds **< 5 KiB** serialized proof overhead per bundle, and refuses **100.0%** (16/16) of artifact-tamper, signer-substitution, source/toolchain-substitution, DSSE-tamper, replay, Merkle-equivocation, expiry, and revocation attack vectors fail-closed.

### Falsification Criteria
H1 is falsified if:
1. Any of the 16 adversarial tamper or unauthorized-substitution vectors is accepted or returns an ambiguous/wrong refusal code.
2. Keyless attestation or verification p50 exceeds `1.0 ms` or bundle overhead exceeds `8 KiB` for a single release binary.
3. Any concurrent write/verify path races under `go test -race` or leaks tenant bundles across IAM org boundaries.

### Expected Failure Modes (Negative Regimes)
1. **Inline raw blob signing anti-pattern**: Embedding raw build binaries inline inside base64 DSSE payloads causes catastrophic bundle amplification ($>5{,}000\times$ at 16 MiB) and memory/latency spikes versus digest-only SLSA v1.2 attestation.
2. **Per-file signing of tiny intermediate build artifacts**: Issuing ephemeral X.509 certs and appending individual transparency log entries for thousands of tiny intermediate object files explodes latency, bundle bytes, and Merkle tree growth compared to a single multi-subject SLSA v1.2 statement at the release boundary.

---

## 4. Bounded Implementation (`apps/provenance` & `apps/apps.go`)

The clean-room implementation lives in [`apps/provenance/`](../../../apps/provenance/) and mounts cleanly into [`apps/apps.go`](../../../apps/apps.go) (`Wire()`) with **zero host package imports** (verified by `TestAppsImportNothingOfTheirHost`):

- [`apps/provenance/slsa.go`](../../../apps/provenance/slsa.go): Canonical `https://in-toto.io/Statement/v1` + `https://slsa.dev/provenance/v1.2` types, strict structural/hex/Git-revision validation, deep `Clone()` isolation, and streaming SHA-256 (`DigestReader`/`DigestFile`).
- [`apps/provenance/dsse.go`](../../../apps/provenance/dsse.go): Exact DSSEv1 `PAE(payloadType, payload)`, Ed25519 + ECDSA P-256 `KeyPair` signers with SPKI SHA-256 KeyIDs, `CanonicalLogLeaf` envelope-to-tlog commitment, and `EphemeralCA` issuing short-lived X.509 code-signing certs carrying SAN identities and Fulcio OIDs (`1.3.6.1.4.1.57264.1.{1,8,9,12,13}`).
- [`apps/provenance/tlog.go`](../../../apps/provenance/tlog.go): Concurrency-safe append-only RFC 6962 Merkle transparency log with domain-separated `0x00`/`0x01` SHA-256 hashing, $O(\log N)$ level-cached inclusion and consistency proof generation, standalone verification (`VerifyInclusionProof`, `VerifyConsistencyProof`, `VerifySignedTreeHead`), and monotonic integrated-time enforcement refusing backdated entries.
- [`apps/provenance/verifier.go`](../../../apps/provenance/verifier.go): `Bundle`, `AttestStatement`, and fail-closed `VerifyBundle` policy engine enforcing all 11 cryptographic and policy checks with 13 distinct typed sentinel errors.
- [`apps/provenance/http.go`](../../../apps/provenance/http.go) + [`apps/provenance/zipdoc_gen.go`](../../../apps/provenance/zipdoc_gen.go): 8 typed `zip.App` routes under `/v1/provenance` (`GET /health`, `GET /checkpoint`, `GET /consistency`, `POST /attest`, `POST /verify`, `GET /bundles`, `GET /bundles/:id`, `POST /revocations`) with zero `http.NewServeMux()`, strict `principal.Acting`/`Caller` org-binding, `url:"-"` on every POST body field preventing query-string hijacking, and fail-closed HTTP `422 Unprocessable Entity` refusals on verification failure.

---

## 5. Correctness, Concurrency, Security & Regression Evidence

### Verification Commands Executed
- `gofmt -l apps/provenance cmd/provenance-bench apps/apps.go` -> clean (0 unformatted files).
- `go vet ./apps/provenance/... ./cmd/provenance-bench/... ./apps/...` -> pass (0 warnings).
- `go test -race -count=1 -v ./apps/provenance/... ./cmd/provenance-bench/...` -> **all tests pass with 0 data races**:
  - Canonical DSSEv1 PAE vector test.
  - Happy paths across Ed25519 keyless, ECDSA P-256 keyless, and direct pinned Ed25519 key modes.
  - **Exhaustive RFC 6962 Merkle verification**: 561 inclusion proofs + 561 consistency proofs across all `(m, n)` pairs for tree sizes `1..33`, domain separation second-preimage check, backdating refusal, and malformed proof refusals (trailing hashes, truncated paths, rollback, split-view).
  - **Adversarial refusal suite**: 100% refusal across all 16 attack vectors with exact error classification.
  - **Concurrency under `-race`**: 8-worker concurrent `TransparencyLog.Append` + `VerifyInclusionProof` (128 entries) and 6-worker concurrent multi-tenant HTTP `POST /v1/provenance/attest` + `POST /v1/provenance/verify` + `GET /v1/provenance/bundles` confirming zero cross-org leakage.
  - **HTTP wire & security boundary tests**: OpenAPI `zipdoc` ledger (8/8 routes described), `401`/`403` unauthenticated/no-org refusals, `404` cross-org isolation on `GET /bundles/:id` and `POST /verify`, `url:"-"` query-hijack immunity, and live org-scoped certificate serial revocation -> `422 REVOKED_SIGNER`.
- `CGO_ENABLED=0 go test ./apps/...` -> pass (including `TestAppsImportNothingOfTheirHost`).
- `CGO_ENABLED=0 go build ./cmd/cloud` -> pass (`68.37 MB` static binary).

### Security Review Posture (`PR_CONVERGENCE.md`)
- **Authentication & Tenant Isolation**: Org and principal identity are sourced exclusively from `principal.Acting` and `zip.CallerOf(ctx).User`; untrusted JSON fields (`org`, `builderIdentity`) are never read. Server mints the OIDC SAN (`https://hanzo.ai/principals/<org>/<user>`) directly into the X.509 cert and SLSA statement. Bundles and revocations are partitioned by org; cross-org reads/verifies answer `404`.
- **Input Validation & Query-Hijack Defense**: All POST body fields carry `url:"-"` so zip's URL binder cannot override JSON fields from query parameters. Subject count (max 64), toolchain count (max 64), field lengths (max 4096), digest format (strict 64 lowercase hex), Git revision format (40/64 lowercase hex), and JSON trailing bytes are all strictly bounded.
- **Cryptographic Hygiene**: Constant-time digest/root comparisons (`crypto/subtle`), RFC 6962 `0x00`/`0x01` domain separation, canonical DSSEv1 PAE, strict key-type/algorithm matching, CA-as-leaf rejection, certificate verification strictly at transparency-log `IntegratedTime`, and deep-copy isolation on all stored/returned structs.

---

## 6. Before/After Measurements, Negative Regimes & HIP-1349 Integration Recommendation

All measurements below are taken verbatim from [`docs/research/hip-1349-slsa-provenance/results.json`](./results.json) (250 iterations, `linux/amd64`, Go 1.27.1, primary artifact `bin/cloud` = `68,374,667` bytes).

### 6.1 Performance: Unsigned Baseline vs. SLSA v1.2 + DSSE + Fulcio-Style Cert + Merkle TLog

| Workload | Min (µs) | p50 (µs) | p95 (µs) | p99 (µs) | Throughput | Alloc / op | Bundle Size |
|---|---:|---:|---:|---:|---:|---:|---:|
| **Unsigned SHA-256 Baseline** (`bin/cloud`, 65.2 MiB) | 223,429.74 | **235,605.60** | 267,275.70 | 300,356.83 | 4.17 ops/s | 33,176 B | 0 B |
| **Ed25519 Keyless Attest** (Cert + DSSE + TLog) | 366.31 | **411.09** | 622.30 | 1,585.90 | 2,193.33 att/s | 46,278 B | **4,479 B** |
| **Ed25519 Keyless Verify** (STH + Inclusion + X.509 + PAE + Policy) | 331.88 | **386.27** | 540.24 | 1,126.19 | 2,405.27 ver/s | 21,230 B | — |
| **Ed25519 Keyless Verify + Pinned Checkpoint** (1→250 consistency, 8 hashes) | 435.18 | **487.93** | 627.20 | 1,068.84 | 1,970.75 ver/s | 22,414 B | — |
| **ECDSA P-256 Keyless Attest** (Cert + DSSE + TLog) | 330.49 | **429.24** | 925.36 | 1,309.20 | 2,056.44 att/s | 54,575 B | **4,637 B** |
| **ECDSA P-256 Keyless Verify** | 320.15 | **401.84** | 585.74 | 1,238.67 | 2,316.43 ver/s | 22,792 B | — |
| **Ed25519 Direct Pinned Key Attest** (No X.509 CA) | 91.12 | **107.63** | 191.83 | 555.47 | 8,120.12 att/s | 14,797 B | **3,457 B** |
| **Ed25519 Direct Pinned Key Verify** | 177.65 | **213.36** | 293.78 | 526.41 | 4,406.92 ver/s | 10,232 B | — |

**Key takeaway**: Full keyless SLSA v1.2 attestation adds **0.41 ms** p50 (**0.17%** of the `235.61 ms` SHA-256 disk-hash baseline) and **4.48 KiB** (**0.0065%**) of storage overhead on a 65.2 MiB Go binary — confirming **H1** with >2× headroom.

### 6.2 Adversarial Tamper & Unauthorized-Substitution Matrix (16/16 Refused — 100.0%)

| ID | Category | Attack Vector | Expected & Actual Code | Refused |
|---|---|---|---|---|
| `V01` | `artifact_tamper` | 1-bit flip in real `bin/cloud` ELF binary | `DIGEST_MISMATCH` | **Yes** |
| `V02` | `signer_substitution` | Valid Ed25519 DSSE signature from rogue key | `UNTRUSTED_SIGNER` | **Yes** |
| `V03` | `signer_substitution` | Trusted CA cert minted for unauthorized OIDC SAN | `UNTRUSTED_SIGNER` | **Yes** |
| `V04` | `signer_substitution` | Rogue Root CA cert claiming authorized SAN | `CERT_INVALID` | **Yes** |
| `V05` | `builder_substitution` | Disallowed `runDetails.builder.id` runner URI | `BUILDER_MISMATCH` | **Yes** |
| `V06` | `source_substitution` | Unauthorized source repository URI | `SOURCE_MISMATCH` | **Yes** |
| `V07` | `source_substitution` | Unpinned Git commit SHA substitution | `SOURCE_MISMATCH` | **Yes** |
| `V08` | `toolchain_tamper` | Modified Go compiler SHA-256 digest | `TOOLCHAIN_MISMATCH` | **Yes** |
| `V09` | `dsse_tamper` | DSSE payload-type confusion (`application/json`) | `PAYLOAD_TYPE_MISMATCH` | **Yes** |
| `V10` | `dsse_tamper` | Tampered DSSE PAE signature (even after re-logging) | `INVALID_SIGNATURE` | **Yes** |
| `V11` | `replay_attack` | Valid `bin/cloud` bundle replayed against `go.mod` | `ARTIFACT_NOT_IN_SUBJECT` | **Yes** |
| `V12` | `tlog_tamper` | 1-bit flip in RFC 6962 Merkle inclusion audit path | `TLOG_INVALID` | **Yes** |
| `V13` | `tlog_tamper` | Forked transparency log violating pinned STH consistency | `TLOG_INVALID` | **Yes** |
| `V14` | `cert_expiry` | Short-lived cert expired before tlog `IntegratedTime` | `CERT_INVALID` | **Yes** |
| `V15` | `revocation` | Revoked X.509 certificate serial number | `REVOKED_SIGNER` | **Yes** |
| `V16` | `revocation` | Revoked signer SPKI KeyID | `REVOKED_SIGNER` | **Yes** |

### 6.3 Negative Regimes (Documented Cost Cliffs & Anti-Patterns)

#### Negative Regime A: Inline Raw Blob Signing vs. Digest-Only SLSA v1.2 Attestation

| Blob Size | Digest-Only Attest (µs) | Digest-Only Bundle (B) | Inline Blob DSSE (µs) | Inline Blob Size (B) | Size Amplification | Latency Ratio |
|---:|---:|---:|---:|---:|---:|---:|
| 64 KiB | 404.07 | 3,798 | 557.28 | 88,560 | 23.32× | 1.38× |
| 1 MiB | 3,839.80 | 3,868 | 13,599.73 | 1,399,280 | 361.76× | 3.54× |
| 4 MiB | 22,547.93 | 3,868 | 83,787.66 | 5,593,584 | 1,446.12× | 3.72× |
| **16 MiB** | **58,807.45** | **3,939** | **431,455.32** | **22,370,800** | **5,679.31×** | **7.34×** |

**Recommendation**: NEVER sign raw artifacts inline in DSSE envelopes. Always stream SHA-256 digests (`DigestReader`/`DigestFile`) and store detached 4.5 KiB SLSA v1.2 bundles alongside artifacts in Hanzo S3.

#### Negative Regime B: Per-File Signing vs. Multi-Subject Batching (1,000 Tiny 512 B Intermediate Files)

| Strategy (1,000 Files) | Total Latency | Total Bundle Bytes | TLog Entries | Speedup / Reduction |
|---|---:|---:|---:|---|
| Per-file individual keyless attestation | 509.82 ms | 4,145,682 B | 1,000 | Baseline (anti-pattern) |
| **Single multi-subject SLSA v1.2 statement** | **10.81 ms** | **166,298 B** | **1** | **47.15× faster · 24.93× smaller · 1,000× less tlog growth** |

**Recommendation**: Attest at release/package boundaries using multi-subject SLSA v1.2 statements rather than signing every intermediate compiler object file individually.

### 6.4 HIP-1349 Integration, Rollout & Rollback Plan
- **Canonical Architecture Alignment**: Preserves `vfs -> Hanzo S3 -> Visor` without introducing parallel storage or auth planes. `apps/provenance` mounts cleanly inside `hanzoai/cloud` at `/v1/provenance` using `principal.Acting` (Hanzo IAM boundary). Detached bundles (`~4.5 KiB` JSON) can be stored as S3 sidecars (`<object>.slsa.json`) and gated by Cloud deploy/Visor admission before execution.
- **Production Hardening Path (Next Phase)**: Back `EphemeralCA` root key and `TransparencyLog` STH signer with `clients/kms` (`CLOUD_KMS_MASTER_KEY_REF`), persist transparency log leaves and org bundles in `cek`-encrypted SQLite/Postgres or S3-backed append-only storage, and publish periodic STH checkpoints to external witness auditors.
- **Rollout & Rollback**: Mounted declaratively in `apps.Wire()` with zero database migrations and zero changes to existing routes or `go.mod`. Rollback is a single-line removal of the `provenance` entry in `apps.Wire()` with zero impact on any other subsystem.
