# apps/flow — Hanzo Flow on /v1/flow (typed passthrough to the product service)

Hanzo Flow is the visual AI workflow product: github.com/hanzoai/flow, a
Python/FastAPI service (the builder UI, the graph engine, the component
library). cloud does not reimplement it. This app is the product-repo model
with an HTTP seam — the same posture apps/iam takes for its Go product, except
the product is Python so the mount is a typed passthrough to the service
instead of an in-process handler. cloud adds IAM auth, the org boundary, and
the unified projection (OpenAPI, MCP tool, CLI command, SDK method — all from
the eight typed ops).

## The served slice (8 typed ops, 0 untyped)

Every op below was proven against a LIVE flow v1.8.2 server before it shipped —
create → list → get → patch → run (a real graph execution returning the run's
outputs) → run records → delete, all answered by the product, not by a stub.

| op | upstream call |
|---|---|
| GET /v1/flow/status | /health + /v1/version composed (honest reachability lens) |
| GET /v1/flow/workflows | GET /v1/flows/?get_all=false&folder_id={org project} (paged) |
| POST /v1/flow/workflows | POST /v1/flows/ with folder_id pinned server-side |
| GET /v1/flow/workflows/{id} | GET /v1/flows/{id} after the ownership gate |
| PATCH /v1/flow/workflows/{id} | PATCH /v1/flows/{id} after the ownership gate |
| DELETE /v1/flow/workflows/{id} | DELETE /v1/flows/{id} after the ownership gate |
| POST /v1/flow/runs | POST /v1/run/{id}?stream=false (sync execution, ≤300s) |
| GET /v1/flow/runs?workflow= | GET /v1/monitor/builds?flow_id= (component build records) |

Responses relay the product's own payloads VERBATIM (`flowResult`, a
RawMessage relay — the cfResult pattern): a workflow is the product's FlowRead,
a page is its paginated envelope, a run is its RunResponse. This plane does not
remodel the product's shapes and therefore cannot drift from them.

## Tenancy

The flow service is a single shared deployment reached with ONE platform
credential (FLOW_API_KEY, KMS-synced env — exec's CODE_EXEC_API_KEY pattern),
so the org boundary is enforced in THIS app on the product's own project
primitive: each org's workflows live in a flow project named by the org id
(resolved and cached per process; created on first use). The org comes from the
validated principal (principal.Org), never an In field. Creates pin the project
server-side; every per-workflow op passes the `owned` gate — a foreign or
unknown id is the same 404, so existence never leaks. typed_wire_test.go's
TestWorkflowsAreOrgScoped measures all of it.

Config: FLOW_UPSTREAM (default `http://flow.hanzo.svc.cluster.local:7860`),
FLOW_API_KEY. Upstream 401/403 → caller sees 503 (deployment fault, never a
caller-auth bug); upstream 5xx → 502; unreachable → 503.

## The refused intent (and why)

hanzoai/openapi authored 87 paths for this product and deleted them as
UNSERVED (commit d86248f: every probe answered a route-level 404 everywhere).
Much of that spec was Activepieces-shaped — pieces, app-connections, triggers,
store-entries — surface this product has never had; the rest is product
surface not yet proven against a real backend. Neither ships as a route:
`intentRefused` in typed_wire_test.go is the closed ledger, measured (each
family 404s on the live router and is absent from the document), so reviving a
family is a deliberate edit to the ledger plus a proven op, never a drive-by.

Families and reasons (full prose in the ledger):

- pieces / app-connections / trigger-events / store-entries / templates — not
  this product's primitives; the piece runtime is apps/automations, connectors
  are /v1/integrations, KV is /v1/kv, templates are /v1/templates.
- folders/projects — internal here BY DESIGN: they ARE the tenant boundary;
  exposing them would let a caller address another org's project.
- users — product user table is service-internal; identity is IAM.
- ai-providers — deployment-global upstream today; per-org custody needs the
  integrations KMS seam first.
- webhooks — needs a public ingress contract (signatures, replay) first.
- mcp — overlaps apps/tools' MCP catalog; composition is task-level work.

## Growing the slice

1. Prove the upstream op against a live flow server (the bar: statuses and
   shapes measured, not read from the product's docs).
2. Add the typed op + extend the fake upstream in typed_wire_test.go with the
   measured wire.
3. Delete the family's `intentRefused` row — the test forces this ordering.
4. `make -C apps/flow describe` and re-weave (check).

An opt-in end-to-end test drives the WHOLE mounted app against a real server:
`FLOW_E2E_UPSTREAM=http://127.0.0.1:7860 FLOW_E2E_KEY=sk-… make -C apps/flow test`
(live_test.go; skipped when the env is absent, so CI needs no Python service).

## Product-repo health (2026-07)

Reviving the product surfaced that hanzoai/flow's API layer had been
import-stripped on main (NameError at request time on core routes). Repaired in
the product repo, measured green afterwards: settings (os/logger/copy2/
AGENTIC_VARIABLES/sanitize_database_url imports; root_path and
allow_custom_components fields), main.py (DeploymentGuardError), flows.py /
projects.py / monitor.py / endpoints.py (route-helper imports), vertex_builds
+ message crud (Flow import), folder/utils (guard import), api/utils/core
(uuid, session_scope), tracing/native (UUID alias + the dropped `resolved`
collection loop). The flow repo's own suite is the gate for those; this app's
fake upstream mirrors the measured wire of the repaired server.
