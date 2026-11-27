# Quality-Constrained Enso Model Routing Benchmark & Reference Scorer (`hanzoai/cloud#59`)

**Issues / Tracking**: [hanzoai/cloud#34](https://github.com/hanzoai/cloud/issues/34), [hanzoai/cloud#27](https://github.com/hanzoai/cloud/issues/27), [HIP-1345 §2.6](https://github.com/hanzoai/hips/blob/main/HIPs/hip-1345-agent-aware-model-serving-and-cache-reuse.md), [HIP-0510](https://github.com/hanzoai/hips/blob/main/HIPs/hip-0510-enso-learned-router.md)  
**Primary Paper**: I. Ong et al., *"RouteLLM: Learning to Route LLMs with Preference Data,"* ICLR 2025 ([arXiv:2406.18665](https://arxiv.org/abs/2406.18665))  
**Scope & Ownership**: Experimental benchmark harness and candidate scorer in `hanzoai/cloud/research/`. Reusable production routing belongs in the canonical Enso router in `hanzoai/ai/router` (`POST /route` adapter or in-process scorer); this package does not maintain a duplicate production router or duplicate pricing registry.

---

## 1. Acceptance Contract Coverage

| Requirement | Implementation in `research/ensoroutellm` |
| --- | --- |
| **Public evaluation** | Real objective ground-truth evaluations from pinned **LiveBench** (`Apache-2.0` repo, `n = 318` questions x `11` models = `3,498` evaluations across `coding`, `language`, `instruction_following`). Content-free Go feature vectors (full IEEE-754 float64 precision) + `ShippingClassify` tags + scores committed (`testdata/livebench_public_features.jsonl`) with zero third-party prompt text redistribution and deterministic rebuild script (`scripts/prepare_livebench_eval.py`). |
| **Model catalog** | Parses canonical `hanzoai/ai/conf/models.yaml` directly via stdlib parser (`models_yaml.go`, exact LF snapshot of `hanzoai/ai@7d0b017:conf/models.yaml`, overridable with `-models-yaml`) plus optional external pricing snapshot JSON (`-pricing-json`, customer token rates from `hanzoai/ai/conf/models.yaml` and `hanzoai/ai/controllers/model_pricing.go`, omitted automatically when `-corpus` is overridden). Excludes non-chat entries (`kai`, `zen-scribe`, embeddings, image/video, hidden/`router:*`) and defaults unspecified capabilities (`ZeroRetention`, `Tools`, `Vision`) fail-closed to `false`. |
| **Baseline** | Port of shipping `hanzoai/ai/router` (`shipping_baseline.go`: `ShippingClassify`, `ShippingPolicy.ForTask`, `ShippingClient.RouteDecisionFor`) evaluated alongside a cross-validated single-arm budget baseline (`best_single_budget_arm_cv`) and two shipping-router modes: unmodified `conf/models.yaml` (`hanzo_ai_router_canonical`) and a catalog-agnostic pool-adapted specialist policy (`hanzo_ai_router_pool_adapted`). |
| **Architecture** | Zero `go.mod` edits; zero duplicate production pricing table; `EngineAdapter` / `NewEnsoEngineHTTPHandler` adapts the candidate scorer directly to `hanzoai/ai/router/client.go`'s `/route` and `/route/observe` contract with bounded observation buffering and fail-closed `Slo.MaxCost` handling. |
| **Quality** | 5-fold stratified CV reporting out-of-fold accuracy at nested-CV matched budget (tolerance selected strictly via 3-fold stratified inner OOF CV with a 1-SE spend safety bound, never peeking at held-out outer test folds; paired 500-resample bootstrap 95% CIs), 10-bin reliability calibration (`ECE`, `Brier`), and 12-point Pareto frontier (`APGR` normalized on `[weak_ratio, 1.0]`, `CPT80`, `CPT90` on standard `usd / strong_usd` spend). |
| **Performance** | Live CPU routing overhead quantiles (`mean`, `p50`, `p95`, `p99` ns, unbatched `batchSize=1` across all paths) and sustained throughput (`1`- and `8`-goroutine QPS) at the corpus median prompt size (`1,856` bytes), comparing `shipping_heuristic`, `candidate_hybrid`, and `candidate_with_ledger` (exercising the in-memory ledger mutex), plus end-to-end model latency quantiles (`p50/p95/p99` ms). |
| **Reliability** | Automated 14-check catalog-agnostic fault-injection suite (`RunReliabilityAudit`) covering budget exhaustion (`402 tenant_ledger` + `422` SLO including `$0/$0` free-arm awareness), unavailable/disconnected models (`UnavailableModels` + `Allow` floor), capability mismatch (`vision`, `forced_tool_choice`, `context_window`, non-chat exclusion), and fallback behavior (HTTP `500`, timeout, cold-start, strict quality floor). |
| **Reproducibility** | Deterministic `SplitMix64` seeds (`20261134`), LF-normalized SHA-256 digests + `.gitattributes`, pinned HuggingFace revisions (`testdata/dataset_manifest.json`, enforced by `TestAcceptanceSchemaAndManifestContract`), and single-command CLI (`results/PROTOCOL.md`). |
| **Private qualification** | `-corpus <private.jsonl>` + `-models-yaml <path>` emits strictly aggregate-only JSON (`aggregate_only: true`, schema `results/acceptance_schema.json`) with zero prompts, zero query IDs, zero per-item labels, and zero concrete model IDs (`TestAggregateOnlyReceiptContainsNoPrivateDataAndSupportsCustomPrivateCatalogs`, including canary model-ID leak test). Supports sparse per-query `arm_outcomes` and tracks `routed_queries` / `unrouted_queries` explicitly. |
| **Production integration** | Gated via promote-or-keep utility check (`RetrainOrgPromoteOrKeep`) and wired cleanly into `hanzoai/ai/router` only after internal `hanzoai/enso-bench` qualification passes. |

---

## 2. Public Evaluation Results (`LiveBench`, `n = 318`, `11` Canonical Hanzo Models)

Evaluated via 5-fold stratified cross-validation (`seed = 20261134`, `go1.26.11 linux/amd64`). Raw aggregate receipt committed at [`testdata/acceptance_results.json`](testdata/acceptance_results.json) (exact parity verified on all deterministic metrics against the raw LiveBench prompt JSONL).

Out-of-fold calibration & Pareto metrics (`enso_hybrid`, standard `usd / strong_usd` spend scale, normalized APGR): **`ECE = 0.0168`**, **`Brier = 0.1901`**, **`APGR = 0.9815`**, **`CPT(80%) = 3.01%`**, **`CPT(90%) = 5.47%`**.

| Routing Policy | Accuracy | 95% Bootstrap CI | Retention vs Strong | USD / 1k Queries | USD Savings | Model p50 / p95 / p99 ms | Unique Arms |
| --- | ---: | :---: | ---: | ---: | ---: | :---: | ---: |
| `always_strong (o1)` | 69.88% | [65.53%, 74.26%] | 100.00% | 29.1714 | 0.00% | 7306 / 13962 / 13962 | 1 |
| `always_weak (mistral-small)` | 37.38% | [32.63%, 42.00%] | 53.50% | 0.1590 | 99.45% | 1867 / 3659 / 3659 | 1 |
| `random_router` | 55.11% | [49.40%, 60.17%] | 78.86% | 5.1913 | 82.20% | 4848 / 10068 / 13962 | 11 |
| `best_single_budget_arm_cv` | 62.65% | [57.46%, 67.23%] | 89.66% | 3.2561 | 88.84% | 4848 / 9456 / 9456 | 2 |
| `hanzo_ai_router_canonical` | 49.39% | [44.74%, 54.32%] | 70.67% | 4.8622 | 83.33% | 3804 / 7388 / 7388 | 1 |
| `hanzo_ai_router_pool_adapted` | 63.52% | [58.77%, 68.26%] | 90.91% | 5.6740 | 80.55% | 4848 / 9456 / 9456 | 3 |
| `routellm_mf_rel(tol=0.04)` | 70.95% | [66.22%, 75.69%] | 101.53% | 12.0321 | 58.75% | 7306 / 13962 / 13962 | 5 |
| `enso_hybrid_rel(tol=0.00)` | 73.74% | [69.57%, 77.74%] | 105.53% | 16.9078 | 42.04% | 7306 / 13962 / 13962 | 5 |
| `enso_hybrid_rel(tol=0.01)` | **74.13%** | [69.59%, 78.47%] | **106.09%** | 15.4281 | 47.11% | 7306 / 13962 / 13962 | 5 |
| `enso_hybrid_rel(tol=0.04)` | 72.03% | [67.46%, 76.59%] | 103.08% | 12.0888 | 58.56% | 7306 / 13962 / 13962 | 5 |
| **`enso_hybrid_rel(tol=0.10)`** | **71.27%** | [66.32%, 75.56%] | **102.00%** | **5.4490** | **81.32%** | 6565 / 7970 / 13962 | 7 |
| `enso_hybrid_rel(tol=0.16)` | 68.34% | [63.93%, 72.62%] | 97.80% | 1.5960 | 94.53% | 4130 / 7102 / 7306 | 6 |

### Matched-Budget Comparison vs. Shipping `hanzoai/ai/router` (Nested 3-Fold Stratified Inner CV + 1-SE Rule)

To avoid post-selection optimism, matched-budget tolerances are calibrated strictly inside each outer training fold via full 3-fold stratified inner out-of-fold CV using a one-standard-error spend safety bound (`mean + 1*SE <= baseline spend`) before evaluating once on the held-out outer test fold:

- **vs. `hanzo_ai_router_canonical` (`conf/models.yaml` verbatim):** `enso_hybrid_nested_cv(med_tol=0.12)` delivers **`+22.08 pp`** (`95% CI [+16.38, +27.55] pp`) at **`-8.13%`** lower spend (`4.4669` vs `4.8622` USD/1k, `budget_matched=true`).
  - *Baseline disclosure:* On this public 11-model LiveBench overlap, Hanzo's proprietary Zen defaults in `hanzoai/ai@7d0b017:conf/models.yaml` (`zen6-coder`, `zen6-max`, `zen6`, `zen6-flash`, `zen-vl`) are absent from LiveBench public judgments, so `hanzo_ai_router_canonical` falls back to its first servable general fallback (`gpt-4o`, `unique_models_selected = 1`). That is why `hanzo_ai_router_pool_adapted` and `best_single_budget_arm_cv` are reported alongside it as stronger apples-to-apples public baselines.
- **vs. `hanzo_ai_router_pool_adapted` (pool-mapped specialist heuristic across 3 models):** `enso_hybrid_nested_cv(med_tol=0.12)` delivers **`+6.57 pp`** (`95% CI [+1.61, +11.78] pp`) at **`-7.81%`** lower spend (`5.2308` vs `5.6740` USD/1k, `budget_matched=true`).
- **vs. `best_single_budget_arm_cv` (training-fold best single model under budget):** The candidate achieves `70.10%` (`5.2308` USD/1k) and `68.34%` (`1.5960` USD/1k, `tol=0.16`) against `62.65%` (`3.2561` USD/1k) for the strongest single budget model.
- **Why multi-arm routing beats `always_strong (o1)` at `tol <= 0.10`:** LiveBench exposes task specialization across models (`o3-mini` leads `LCB_generation`, `claude-3-7-sonnet` leads `coding_completion` and `plot_unscrambling`, `fireworks/deepseek-v3` leads `paraphrase`, `gpt-4o` leads `typos`, and `o1` dominates `connections`). A per-arm calibrated predictor routes each query to its specialist instead of overpaying for a single model.

### Live CPU Routing Overhead & Reliability (`go1.26.11 linux/amd64`, Median Prompt `1,856` Bytes, Unbatched `batchSize=1`)

| Path | Mean Overhead | p50 | p95 | p99 | Throughput (1g / 8g) |
| --- | ---: | ---: | ---: | ---: | ---: |
| `shipping_heuristic` (`1,856`-byte corpus-median prompts) | 14.2 us | 10.0 us | 38.8 us | 56.9 us | 70.3k / 272.4k qps |
| `candidate_hybrid` (`1,856`-byte corpus-median prompts) | 119.6 us | 107.5 us | 231.2 us | 335.0 us | 8.36k / 37.5k qps |
| `candidate_with_ledger` (includes in-memory ledger mutex) | 123.2 us | 110.5 us | 239.6 us | 347.7 us | 8.11k / 32.1k qps |

- **Reliability audit (`RunReliabilityAudit`):** **`14 / 14` checks passed** across budget exhaustion (`402` + `422`), unavailable models (failover + `Allow` floor), capability mismatch (`vision`, `tools`, `context_window`, non-chat exclusion), and fallback behavior (HTTP `/route` `500`, timeout, cold-start, strict quality floor).

---

## 3. Limitations & Synthetic Fixture Scope

1. **Public LiveBench Coverage, Fixed Output Tokens & Metadata Priors:** LiveBench's public judgment split with matching active question parquets covers `318` tasks across `coding`, `language`, and `instruction_following` (no `math` or `reasoning` public overlap across these 11 models). Public cost estimates use fixed per-category `est_output_tokens` (`512` coding, `256` other) rather than model-specific completion+reasoning token lengths (so reasoning-token amplification on `o1`/`o3-mini`/`deepseek-r1` is not reflected in public USD/1k estimates). Token rates (`input_per_million`, `output_per_million`) come verbatim from `hanzoai/ai@7d0b017` (`conf/models.yaml` and `controllers/model_pricing.go`), while latency/capability fields in `hanzo_pricing_snapshot.json` are benchmark-harness priors (documented in `estimated_priors_note`). Private `hanzoai/enso-bench` runs omit `hanzo_pricing_snapshot.json` automatically unless `-pricing-json` is passed explicitly and use measured `arm_latencies_ms` when provided.
2. **Long-Context Feature Scan & Research Ledger:** `ExtractFeatures` scans the full prompt+messages on CPU (fine for median `~1.8 KB` prompts at `~120 us`, whereas 128k-token prompts should cap the scan window prior to production graduation in `hanzoai/ai`). The in-memory `EnsoRouter.ledger` (`sync.RWMutex`) exists only for offline simulation and unit tests; concurrent 8-goroutine lock overhead is measured explicitly (`candidate_with_ledger`). Production graduation into `hanzoai/ai/router` should emit to `hanzoai/ai`'s existing asynchronous `RoutingEvent` table instead of an in-process map lock.
3. **Terse OOD Prompts:** Surface + hashed-bigram CPU features (`FeatureDim = 24`) can underestimate difficulty on short plain-English riddles lacking structural cues unless paired with tight tolerances or Kai decision escalation.
4. **Synthetic Fixture Scope:** `DefaultCatalog()` and `GenerateStandardCorpus()` exist exclusively for deterministic unit tests (`routellm_test.go`) and are not used for Acceptance Contract qualification.

---

## 4. Clean Integration Path Into `hanzoai/ai`

Do not maintain a second production router in `hanzoai/cloud`. Once Hanzo's internal private qualification (`-corpus /path/to/enso-bench.jsonl`) confirms measurable gains over the shipping router:

1. **Zero-downtime shadow / HTTP hook:** Serve `EngineAdapter` (or stateless `NewEnsoEngineHTTPHandler`) behind `hanzoai/ai/router.Client.Endpoint` (`POST /route` & `POST /route/observe`); existing `Client.routeEngine` timeout (`150ms`) and `Allow`-guarded heuristic fallback protect production automatically.
2. **In-process graduation:** Move `ExtractFeatures` (with a bounded prefix/suffix scan window for 128k+ prompts) + `TrainedWeights.PredictQualityByKind` directly into `hanzoai/ai/router` behind `StrategyEnso`, reading model metadata from `controllers.GetModelConfig()` (`conf/models.yaml`) and retraining asynchronously from the existing content-free `RoutingEvent` table under `RetrainOrgPromoteOrKeep`.

---

## 5. File Map

| Path | Description |
| --- | --- |
| [`models_yaml.go`](models_yaml.go) | Stdlib parser for canonical `hanzoai/ai/conf/models.yaml` + pricing snapshot projection |
| [`shipping_baseline.go`](shipping_baseline.go) | Port of `hanzoai/ai/router` (`Classify`, `Policy`, `RoutingPolicy`) + bounded `/route` & `/route/observe` HTTP adapter |
| [`public_eval.go`](public_eval.go) | JSONL corpus loader, stratified CV, nested 3-fold OOF matched-budget + bootstrap CIs, live CPU perf quantiles, catalog-agnostic 14-check reliability audit |
| [`catalog.go`](catalog.go) | `ModelArm`, cost/latency estimators, narrow-only `FoldPolicies`, `SplitMix64` RNG, synthetic unit-test fixture |
| [`gates.go`](gates.go) | `ValidateRequestSemantics`, deterministic capability/retention/outage/budget gates, thinking-block sanitization |
| [`features.go`](features.go) | Zero-allocation 24-dim CPU content-free `ExtractFeatures` |
| [`router.go`](router.go) | Bilinear MF + Newton-IRLS Logistic + 3-fold OOF Platt trainer, `EnsoRouter`, content-free ledger, lock-free-eval promote-or-keep gate |
| [`acceptance_test.go`](acceptance_test.go) | Acceptance Contract test suite (YAML loader hardening, `hanzoai/ai/router` parity, adapter bounds, schema & manifest SHA-256 contract, aggregate-only privacy + canary model-ID leak tests, LiveBench CV & drift lock) |
| [`routellm_test.go`](routellm_test.go) | 20 unit/security/concurrency/gate test suites |
| [`scripts/prepare_livebench_eval.py`](scripts/prepare_livebench_eval.py) | Reproducible pinned LiveBench downloader & feature extractor |
| [`testdata/dataset_manifest.json`](testdata/dataset_manifest.json) | Pinned HuggingFace SHAs, parquet SHA-256 digests, and `hanzoai/ai@7d0b017` LF config provenance |
| [`results/acceptance_schema.json`](results/acceptance_schema.json) | Strict JSON Schema (`additionalProperties: false`) for aggregate-only receipts |
| [`results/PROTOCOL.md`](results/PROTOCOL.md) | Offline reproduction and internal private qualification commands |
| [`../ensoroutellmsim/main.go`](../ensoroutellmsim/main.go) | CLI runner (`-mode acceptance \| extract-features \| synthetic`) |
