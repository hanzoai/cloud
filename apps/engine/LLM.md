# apps/engine — Hanzo Engine on /v1/engine (typed passthrough to the serving runtime)

Hanzo Engine is the inference runtime product: github.com/hanzoai/engine, the
Rust LLM engine (`hanzo serve` — the OpenAI- and Anthropic-compatible server,
quantization, multimodality, the built-in web UI). cloud does not reimplement
it. This app is the product-repo model with an HTTP seam — apps/flow's
posture — a typed read lens over the engine deployment's own management
plane. cloud adds IAM auth and the unified projection (OpenAPI, MCP tool, CLI
command, SDK method — all from the four typed ops).

## The served slice (4 typed ops, 0 untyped)

Every op below was proven against a LIVE hanzo-server (the engine binary
serving a real model on this box) before it shipped, and live_test.go
re-proves the loop on demand.

| op | upstream call |
|---|---|
| GET /v1/engine/status | /health + build.git_revision from /v1/system/info (honest reachability lens) |
| GET /v1/engine/models | GET /v1/models (the server's model table, load status included) |
| GET /v1/engine/model?model= | POST /v1/models/status (a READ on this plane; the id rides our query, the upstream body) |
| GET /v1/engine/system | GET /v1/system/info (OS/CPU/memory/accelerator inventory + build capabilities) |

Responses relay the product's own payloads VERBATIM (`engineResult`, a
RawMessage relay — the flowResult pattern): a model list is the server's
OpenAI-style envelope with its load-status extension, the system report is
its SystemInfo document. This plane does not remodel the product's shapes and
therefore cannot drift from them.

Model ids are Hugging Face repo paths or local paths — they carry slashes —
so the one-model op addresses by QUERY, never a path segment.

## Inference is NOT here

The fleet's ONE inference door is the OpenAI-compatible /v1 surface (apps/ai
+ the zen claim), where requests are metered and billed. A second completion
door under /v1/engine would split billing, so it deliberately does not exist —
the ledger pins it.

## Tenancy

The engine deployment is ONE shared runtime with no per-org primitive, so
every read here is a deployment-global platform fact: the gate is
authentication (validated principal or 403, before any upstream byte), and
there is no org scoping because there are no per-org rows to scope.

It reads `principal.ValidatedFrom(ctx)` — the bit `cloud.Bridge` parks beside
the org — and NOT `principal.OrgFrom`, which answers with an org or refuses:
that would 403 a signed-in caller whose token names no home org (a machine
token, or one minted before IAM's `orgs` claim), which on a tenant-less plane
is exactly the operator this lens exists for. `TestOrgLessButValidatedIsServed`
is that half of the gate and `TestNoPrincipalIs403AndNoUpstreamByte` the other,
so neither can be narrowed silently. The gate therefore does NOT take the
pinned `cloud.Request` escape hatch (`typed_request_gate_test.go`) — reaching
for the request here would recompute `principal.Validated(c)`, the same answer
by the longer way. That same
fact is why every MUTATION the product's server exposes (models/unload,
models/reload, models/tune, re_isq, system/doctor) is REFUSED: an org-scoped
route onto a shared runtime hands each tenant every other tenant's
availability. Mutations arrive when engines are per-org instances, not
before.

Config: ENGINE_UPSTREAM (default `http://engine.hanzo.svc.cluster.local:36900`
— 36900 is the port svc/engine actually exposes; 1234 is standalone
`hanzo serve`'s default and is right on a dev box), ENGINE_API_KEY (KMS-synced platform
credential, rides `Authorization: Bearer` upstream; a bare `hanzo serve`
enforces none). Upstream 401/403 → caller sees 503 (deployment fault, never a
caller-auth bug); upstream 5xx → 502; unreachable → 503.

## The refused intent (and why)

hanzoai/openapi authored 22 paths for this product and deleted them as
UNSERVED (commit d86248f: every probe answered a route-level 404 everywhere;
engine.hanzo.ai was a Cloudflare catch-all answering 200 for every path). That
spec described a GPU cluster manager — clusters, jobs, Ray, pipelines, fleet
GPU inventory, serve-endpoint CRUD — which this product has never been.
`intentRefused` in typed_wire_test.go is the closed ledger, measured (each
family 404s on the live router and is absent from the document):

- clusters — the cluster plane: /v1/clusters (apps/visor) merges managed
  clusters with the BYO fleet registry (apps/fleet).
- jobs — /v1/finetune/jobs (the hanzoai/ai broker); the engine runs no job queue.
- ray — no Ray operator backs the fleet; the engine is a single process.
- pipelines — no backend; the engine executes inference, not DAGs.
- gpus — fleet-wide inventory needs the cluster plane; the engine's own host
  devices are served at /v1/engine/system.
- serve/endpoints — /v1/ml/models (kserve InferenceService) owns serving-
  endpoint CRUD; the engine's own table is the /v1/engine/models lens.
- models/unload (+reload/tune/re_isq), system/doctor, chat — real upstream
  surface refused here: shared-runtime mutations, load diagnostics on shared
  capacity, and a second unmetered inference door. See the ledger prose.

## Growing the slice

1. Prove the upstream op against a live hanzo-server (the bar: statuses and
   shapes measured, not read from the product's docs).
2. Add the typed op + extend the fake upstream in typed_wire_test.go with the
   measured wire.
3. Delete the family's `intentRefused` row — the test forces this ordering.
4. `make -C apps/engine describe` and re-weave (check).

An opt-in end-to-end test drives the WHOLE mounted app against a real server:
`ENGINE_E2E_UPSTREAM=http://127.0.0.1:1234 make -C apps/engine test`
(live_test.go; skipped when the env is absent, so CI needs no GPU box).
