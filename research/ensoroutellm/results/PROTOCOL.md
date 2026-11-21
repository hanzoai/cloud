# Measurement & Reproduction Protocol — `hanzoai/cloud#34` & `#27` (Enso / RouteLLM)

Everything in **§1–§3** runs offline in **< 15 seconds** on any Linux, macOS, or Windows host with Go $\ge 1.22$ (`CGO_ENABLED=0`, standard library only, zero external network or paid LLM/embedding API calls).

---

## 0. Prerequisites

- Go $\ge 1.22$ (`go version`; validated on `go1.26.11`)
- No C compiler (`CGO_ENABLED=0`), no GPU, and no `OPENAI_API_KEY` required

---

## 1. Static Analysis, Build & Full Test Suite (~10 seconds)

From the repository root:

```bash
CGO_ENABLED=0 go vet ./research/...
CGO_ENABLED=0 go test -v -count=1 ./research/...
CGO_ENABLED=0 go build -o ensoroutellmsim ./research/ensoroutellmsim
```

Expected:
- `20` test suites in `research/ensoroutellm` pass (covering all 5 `HIP-1345 §2.6` hard capability gates across 8 deterministic checks `0–7`, HTTP `400` mandatory-thinking, `NaN`/`Inf` & token-ceiling validation, overflow saturation, cross-family reasoning-block sanitization, narrow-only multi-tenant policy precedence, composite `(org, project)` / `(org, request_id)` isolation, content-free feedback ledger & opt-in training export, promote-or-keep retrain gating, 28-goroutine concurrency/hot-swap safety, sub-millisecond CPU latency, hypotheses `H1–H4`, JSON Schema validation, and bit-exact `testdata/results.json` drift lock).
- `1` end-to-end CLI build-and-JSON test passes in `research/ensoroutellmsim`.

---

## 2. Regenerate the 4-Part Benchmark Suite (~4 seconds)

```bash
./ensoroutellmsim -seed 20261134 -out research/ensoroutellm/testdata/results.json
```

This executes:
1. **Dense 5-Fold Stratified Cross-Validation** (`n = 600` tasks across `6` domains, `9` matched routing configurations, out-of-fold Platt calibration, `ECE`, `Brier`, `APGR`, `CPT(80%)`, `CPT(90%)`).
2. **Sparse Bandit Partial-Feedback 5-Fold CV** (`n = 180` queries with only `1` observed arm/query = `85.7%` missing arm labels, comparing isolated Logistic vs. `x^T W p_m` Matrix Factorization vs. Enso Hybrid).
3. **12-Scenario Capability & Security Gate Audit** (`n = 132` requests comparing `unconstrained_routellm` vs. `enso_hybrid_constrained`).
4. **H4 Out-of-Distribution Adversarial Terse-Prompt Slice** (`n = 60` short math prompts devoid of lexical/LaTeX cues).
5. **Live CPU Routing Latency Benchmark** (`5,000` `EnsoRouter.Route()` calls).

---

## 3. Verify Bit-Exact Determinism Across Runs

```bash
./ensoroutellmsim -seed 20261134 -out /tmp/enso-run-a.json
./ensoroutellmsim -seed 20261134 -out /tmp/enso-run-b.json
diff -u /tmp/enso-run-a.json /tmp/enso-run-b.json
```

Expected: empty diff (`exit 0`).
