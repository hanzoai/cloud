# Measurement Protocol — cloud#39 (Verifiable Agent-Produced Artifacts)

Everything in §1–§2 runs on any host with Go ≥ 1.21 (~5 minutes). §3 is the
cosign cross-check (~10 minutes) and requires the cosign binary.

## 0. Prerequisites

- Go ≥ 1.21 (`go version`)
- For §3 only: cosign (`cosign version` — sigstore/cosign ≥ v2)

## 1. Build + in-process verification (no cosign needed)

```
go build -o provenanceharness ./research/provenanceharness
go test ./research/provenanceverification/...        # 15 tests
./provenanceharness genkeys -dir /tmp/pv-keys        # fresh keypair for this run
./provenanceharness sign -key /tmp/pv-keys/priv.pem -out /tmp/pv-signed.json
./provenanceharness verify -key /tmp/pv-keys/pub.pem -doc /tmp/pv-signed.json
```

Expected: `signature: OK (<1 ms)`.

## 2. Tamper + substitution rejection (CLI level)

```
# Tamper: edit one field of the signed document (e.g. source_revision)
node -e "const fs=require('fs');const d=JSON.parse(fs.readFileSync('/tmp/pv-signed.json','utf8'));d.source_revision='0'.repeat(40);fs.writeFileSync('/tmp/pv-tampered.json',JSON.stringify(d))"
./provenanceharness verify -key /tmp/pv-keys/pub.pem -doc /tmp/pv-tampered.json
```

Expected: `VERIFY REJECTED: provenanceverification: signature mismatch`, exit 1.

```
# Substitution: verify against different artifact bytes
echo -n "attacker bytes" > /tmp/pv-substituted.bin
./provenanceharness verify -key /tmp/pv-keys/pub.pem -doc /tmp/pv-signed.json -artifact /tmp/pv-substituted.bin
```

Expected: `ARTIFACT REJECTED: ... digest mismatch`, exit 1 (the document
signature itself stays valid — the two rejection classes are separate).

## 3. Cosign cross-check (upstream baseline)

The in-process signer and cosign must verify the same fixture. SLSA
provenance via cosign attests with a predicate; the cross-check binds the
fixture's four digests into a cosign attestation and confirms cosign
verifies what the in-process verifier verifies.

```
cosign version
# Sign the fixture artifact with cosign key-pair mode:
echo -n "fixture-artifact-bytes" > /tmp/pv-fixture.bin
COSIGN_PASSWORD="" cosign sign-blob --yes \
  --key /tmp/pv-keys/cosign.key \
  --bundle /tmp/pv-cosign-bundle.json \
  /tmp/pv-fixture.bin
# Verify with cosign:
cosign verify-blob --yes \
  --key /tmp/pv-keys/cosign.pub \
  --bundle /tmp/pv-cosign-bundle.json \
  /tmp/pv-fixture.bin
```

Expected: cosign verifies the fixture. Then record the outcome in
`results/cosign-crosscheck.json` against `schema.json#/definitions/cosign_crosscheck`:

```
{
  "cosign_version": "<output of cosign version>",
  "fixture": "/tmp/pv-fixture.bin (sha256 3f0d...)",
  "outcome": "cross-verified",
  "notes": "cosign verify-blob OK; in-process verifier agrees on the digest binding"
}
```

If cosign is unavailable, commit `"outcome": "skipped-no-cosign"` — that is
an honest skip, not a failure.

## 4. Latency benchmark (H3)

```
./provenanceharness bench -iters 5000
```

Record the per-op number in `results/bench-<date>-<host>.json` against
`schema.json#/definitions/bench_result`. H3 asserts <100 ms/op; the
authoring host measured ~0.7-1.0 µs/op.

## 5. What to commit

- `results/cosign-crosscheck-<date>-<host>.json` (§3 output)
- `results/bench-<date>-<host>.json` (§4 output)
- Update README §5 with the cosign result; if H1–H4 fail anywhere, record
  the failure mode — a documented negative result completes the research
  contract.
