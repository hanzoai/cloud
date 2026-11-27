// Package engine is Hanzo Engine: which models the serving runtime has loaded,
// and the GPUs under it.
//
// It is the serving runtime behind Hanzo's models — what it serves and what it
// runs on, read through /v1/engine.
//
// PRODUCT-REPO MODEL. The product lives in github.com/hanzoai/engine (Rust —
// the LLM inference engine: `hanzo-engine serve`, the OpenAI- and Anthropic-
// compatible server, quantization, multimodality). This subsystem does NOT
// reimplement any of it: every op is a TYPED PASSTHROUGH to the engine
// deployment over an HTTP client, the posture apps/flow takes for its Python
// product. cloud adds IAM auth and the unified surface (OpenAPI/MCP/CLI/SDK
// projection).
//
// THE HONEST SLICE. hanzoai/openapi once authored 22 paths for this product —
// GPU clusters, jobs, Ray, pipelines, fleet GPU inventory, serve endpoints —
// and deleted them as UNSERVED (nothing answered them anywhere). The engine is
// not a cluster manager and never served that surface; what it genuinely
// answers today is its own management plane, and that is what mounts here:
// the models the runtime serves with their load state, one model's state, the
// host's device inventory (the real GPUs under the runtime), and a
// reachability lens. Cluster/job/Ray/pipeline
// intent stays refused — those live on the cluster plane (/v1/compute/clusters,
// /v1/ml) where they are real — and the ledger is a measured gate
// (typed_wire_test.go intentRefused), not a comment.
//
// INFERENCE IS NOT HERE. The fleet's ONE inference endpoint is the OpenAI-
// compatible /v1 surface (apps/ai + the zen claim), where requests are
// metered and billed. This plane is the runtime's management lens; opening a
// second, unmetered completion endpoint under /v1/engine would split billing, so
// it deliberately does not exist.
//
// MODEL LIFECYCLE, READ-ONLY. The engine deployment is one shared runtime with
// no per-org primitive, so every read here is a platform fact and every
// MUTATION the product's server does expose (model load/unload/reload, tune,
// requantize, doctor) is refused: an org-scoped route onto a shared runtime
// would hand each tenant every other tenant's availability. Mutations arrive
// when engines are per-org instances, not before.
//
// FAIL-CLOSED. No validated principal → 403 before any upstream byte. An
// upstream that refuses the platform credential (its 401/403) is a deployment
// fault, not the caller's — reported 503. An unreachable upstream is 503.
package engine
