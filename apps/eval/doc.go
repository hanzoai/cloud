// Package eval is scoring a model on your own data, with a judge you choose.
//
// /v1/eval: datasets, dataset items, evaluators, score configs, runs, scores
// and traces, per org. Native, and nothing proxies to the retired observability
// console.
//
// Storage split (CTO directive), two orthogonal stores this package composes:
//   - metastore  (store.go)     — Hanzo Base/SQLite, per-org config/metadata:
//     datasets, dataset items, evaluators, rubrics, dataset-run defs.
//   - telemetry  (telemetry.go) — apps/datastore, append-only event stream:
//     traces + scores-as-events (all AI observability). Optional at Mount; when
//     no datastore is wired the run still scores, but trace/score persistence is
//     honestly skipped (logged), never faked.
//
// The run orchestrator drives a REAL evaluation: for each ACTIVE dataset item it
// (1) calls the model-under-test through the runner, (2) records a trace, (3)
// calls the LLM-as-judge through the runner, (4) validates + records the score,
// (5) updates the durable run record. The runner is PLUGGABLE (EvalRunner):
// today an in-process gateway runner (the caller's bearer → loopback
// /v1/chat/completions); a DigitalOcean-backed runner can drop in behind the same
// interface without touching the store/API/FE.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is c.Org()
// — the value SanitizeIdentity minted from the VALIDATED bearer owner (HIP-0026)
// — and NEVER a client-supplied X-Org-Id/X-Project-Id header. Every metastore
// query filters WHERE org=?; every telemetry read binds org as a named parameter.
//
// Surface (all org-scoped; /v1 only):
//
//	POST   /v1/eval/datasets         create/upsert a dataset            -> Dataset
//	GET    /v1/eval/datasets         list the org's datasets            -> {data:[…]}
//	GET    /v1/eval/datasets/:name   dataset detail + item count        -> Dataset
//	DELETE /v1/eval/datasets/:name   delete a dataset (+ its items)
//	POST   /v1/eval/datasets/:name/items  create/upsert an item         -> DatasetItem
//	GET    /v1/eval/datasets/:name/items  list a dataset's items (limit) -> {data:[…]}
//	POST   /v1/eval/evaluators       create/upsert an evaluator         -> Evaluator
//	GET    /v1/eval/evaluators       list the org's evaluators          -> {data:[…]}
//	POST   /v1/eval/rubrics          create/upsert a rubric             -> ScoreConfig
//	GET    /v1/eval/rubrics          list the org's rubrics             -> {data:[…]}
//	POST   /v1/eval/scores           record a score event               -> ScoreView
//	GET    /v1/eval/scores           list score events (filters+limit)  -> {data:[…]}
//	GET    /v1/eval/traces           list traces (filters+limit)        -> {data:[…]}
//	POST   /v1/eval/runs             run a dataset through model+judge   -> runSummary
//	GET    /v1/eval/runs             list run records (datasetName)     -> {data:[…]}
//	GET    /v1/eval/metrics          the org's AI overview board        -> board
//
// Order 145: binds /v1/eval/* BEFORE the AI subsystem's /v1/* catch-all (150),
// the same slot product uses. serve.go auto-registers GET /v1/eval/health.
package eval
