# Reproducible Evaluation & Private Qualification Protocol (`hanzoai/cloud#59`)

All public commands run offline in Go (`CGO_ENABLED=0`, zero modifications to `go.mod`) and emit deterministic, aggregate-only JSON receipts matching `results/acceptance_schema.json`.

---

## 1. Quick CI & Full Race Verification

From repository root (`hanzoai/cloud`):

```bash
# Fast CI check (~10-12s under -race)
go test -short -race -count=1 ./research/ensoroutellm ./research/ensoroutellmsim

# Full verification including 5-fold LiveBench acceptance eval (with nested 3-fold inner CV) and E2E CLI build
go vet ./research/ensoroutellm ./research/ensoroutellmsim
go test -race -count=1 -timeout 5m ./research/ensoroutellm ./research/ensoroutellmsim
```

---

## 2. Reproduce Public LiveBench Acceptance Benchmark

Uses canonical `hanzoai/ai/conf/models.yaml` (exact LF snapshot of `hanzoai/ai@7d0b017:conf/models.yaml`, overridable via `-models-yaml`) and the pinned content-free LiveBench evaluation corpus (`n = 318` questions across 11 models = 3,498 objective verifier judgments):

```bash
go run ./research/ensoroutellmsim \
  -mode acceptance \
  -models-yaml research/ensoroutellm/testdata/hanzo_models_snapshot.yaml \
  -pricing-json research/ensoroutellm/testdata/hanzo_pricing_snapshot.json \
  -corpus research/ensoroutellm/testdata/livebench_public_features.jsonl \
  -seed 20261134 \
  -perf-iters 2000 \
  -out research/ensoroutellm/testdata/acceptance_results.json
```

To rebuild `livebench_public_features.jsonl` from scratch directly from pinned HuggingFace LiveBench parquet revisions (verifying every SHA-256 digest in `testdata/dataset_manifest.json`):

```bash
python3 research/ensoroutellm/scripts/prepare_livebench_eval.py \
  --cache-dir /tmp/livebench-cache \
  --out research/ensoroutellm/testdata/livebench_public_features.jsonl
```

---

## 3. Private Internal Qualification (`hanzoai/enso-bench`)

Hanzo's internal runner can evaluate confidential corpora without distributing labels or model lists to forks. Point `-models-yaml` directly at a live `hanzoai/ai/conf/models.yaml` checkout (including internal Zen6 models) and `-corpus` at any private JSONL file whose rows follow:

```json
{
  "query_id": "internal-001",
  "dataset": "enso-bench",
  "category": "code",
  "task": "systems",
  "prompt": "...",
  "est_input_tokens": 640,
  "est_output_tokens": 384,
  "arm_outcomes": {"zen6-coder": 1.0, "zen6-flash": 0.0, "zen6-max": 1.0},
  "arm_latencies_ms": {"zen6-coder": 2400, "zen6-flash": 650, "zen6-max": 5100}
}
```

Notes for private runs:
- When `-corpus` is passed explicitly without `-pricing-json`, the CLI automatically omits the LiveBench pricing snapshot so private runs consume `models.yaml` cleanly (pass `-pricing-json /path/to/live_pricing.json` only if you want external OpenRouter/alias rate overrides).
- Rows may supply either `prompt` or `messages` (multi-turn text is combined for `ShippingClassify` and `ExtractFeatures`).
- `arm_outcomes` may be sparse per query; each router is restricted per query to models that have ground-truth labels on that query, and `routed_queries` / `unrouted_queries` are reported explicitly.
- Matched-budget tolerances are selected via full 3-fold stratified inner out-of-fold CV with a 1-SE spend safety bound (`mean + 1*SE <= baseline spend`), strictly inside each training fold and never peeking at held-out outer test folds.
- Supplying `arm_latencies_ms` overrides catalog latency priors with measured wall-clock latencies.

Run:

```bash
go run ./research/ensoroutellmsim \
  -mode acceptance \
  -models-yaml /path/to/hanzoai/ai/conf/models.yaml \
  -corpus /path/to/private_enso_bench.jsonl \
  -seed 20261134 \
  -out /tmp/enso_private_aggregate_receipt.json
```

The output receipt is strictly **aggregate-only** (`aggregate_only: true`): it contains zero prompts, zero query IDs, zero per-item outcome maps, and zero concrete model IDs (verified by `TestAggregateOnlyReceiptContainsNoPrivateDataAndSupportsCustomPrivateCatalogs`, including a canary model-ID leak check).

---

## 4. Synthetic Unit-Fixture Suite (Unit Tests Only)

Synthetic data is retained exclusively for deterministic unit tests and capability-gate regression checks:

```bash
go run ./research/ensoroutellmsim -mode synthetic -seed 20261134 -out research/ensoroutellm/testdata/results.json
```
