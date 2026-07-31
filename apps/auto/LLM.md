# apps/auto — Hanzo Auto on /v1/auto (typed passthrough to the product service)

Hanzo Auto is the durable workflow automation product: github.com/hanzoai/auto,
native Go v2 — hanzoai/base (SQLite + HTTP) for storage and listener,
hanzoai/tasks (Temporal-derived, ZAP wire) for durable execution, an embedded
React flow canvas. cloud does not reimplement it. This app is the product-repo
model with an HTTP seam — the same posture apps/flow takes for its Python
product. cloud adds IAM auth, the org boundary, and the unified projection
(OpenAPI, MCP tool, CLI command, SDK method — all from the eleven typed ops).

## The served slice (11 typed ops, 0 untyped)

Every op below was proven against a LIVE auto v2 server wired to a live tasksd
before it shipped — create → list → get → patch → publish → start → poll to
COMPLETED with the engine's real output (a webhook→set graph binds a variable;
a webhook→http graph performs a real HTTP request whose status/headers/body
land in the run output) → delete. `live_test.go` re-proves the loop on demand
(`AUTO_E2E_UPSTREAM=http://127.0.0.1:18090 make -C apps/auto test`).

| op | upstream call |
|---|---|
| GET /v1/auto/status | GET /v1/health (honest reachability lens) |
| GET /v1/auto/pieces | GET /v1/pieces (compiled-in piece catalog) |
| GET /v1/auto/flows | GET /v1/flows (org-scoped list) |
| POST /v1/auto/flows | POST /v1/flows (201 relay) |
| GET /v1/auto/flows/{id} | GET /v1/flows/{id} |
| PATCH /v1/auto/flows/{id} | PATCH /v1/flows/{id} |
| DELETE /v1/auto/flows/{id} | DELETE /v1/flows/{id} (204 relay) |
| POST /v1/auto/flows/{id}/publish | POST /v1/flows/{id}/publish (immutable version + arm triggers) |
| GET /v1/auto/runs[?flow=] | GET /v1/runs[?flowId=] |
| POST /v1/auto/runs | POST /v1/runs (202: durable dispatch to the tasks plane) |
| GET /v1/auto/runs/{id} | GET /v1/runs/{id} |

Responses relay the product's own payloads VERBATIM (`autoResult`, a
RawMessage relay — the flowResult pattern): a flow is the product's flow
record, a run is its run record. This plane does not remodel the product's
shapes and therefore cannot drift from them.

## Tenancy — the gateway-header seam

The product's auth contract is GATEWAY-MINTED identity: every route scopes its
rows by the `X-Org-Id` header and answers 401 without one (its base platform
plugin validates IAM JWTs when deployed standalone, and explicitly preserves
gateway-injected identity headers otherwise). cloud IS that gateway here:

- `caller` resolves the VALIDATED principal's org (`principal.Org`) — never an
  In field — and `send` stamps it on every upstream call. There is no other
  identity on the seam and no platform API key; the header is the contract.
- The product keeps one `projects` row per org (auto-provisioned on first
  use) and filters every flow/run/trigger/connection query by it, so foreign
  ids answer its 404 — relayed as-is, existence never leaked.
- CONSEQUENCE: the auto Service must stay CLUSTER-PRIVATE. It trusts the
  header, so cloud is its only legitimate door; exposing it directly would
  hand out org impersonation. (Same posture as the mq broker.)
- An upstream 401 means the header did not survive the seam — a deployment
  fault reported 503, never a caller-side auth bug. Upstream 5xx → 502,
  except the product's own honest 503 ("tasks worker is not available"),
  which relays with its reason: dispatch is real or refused, never faked.

## The run loop is real (and what made it real)

POST /v1/auto/runs is a DURABLE dispatch: the product's engine executes the
flow graph as a tasks workflow (one activity per node) and its terminal
Complete activity writes the output back to the run record; a failed workflow
is written back by the product's dispatch watcher. That write-back seam was
closed in hanzoai/auto@21e3ec65bf (before it, every run stayed `running`
forever — routes.CompleteRun existed with no caller because the tasks v1
client wire cannot fetch workflow results). Poll GET /v1/auto/runs/{id} to
`completed`/`failed`. Known product seam, recorded there: a product-process
restart mid-run loses the failure watcher, leaving that run `running`.

## Refused intent (the ledger is measured, not prose)

hanzoai/openapi authored 50 Activepieces-shaped paths for this product and
deleted them as unserved (d86248f). The v2 product is a ground-up Go rewrite
with a deliberately small surface; `typed_wire_test.go:intentRefused` pins
every refused family against the live router. The load-bearing refusals:

- **connections / app-connections** — the product's route exists but stores
  connection configs base64-at-rest (its recorded pre-KMS seam). Secrets live
  in KMS or they do not live: no cloud custody door until the product's KMS
  DEK derivation lands.
- **triggers / trigger-events / test-trigger** — the product serves list+fire
  but its v1 API has NO route that creates a trigger row; a fire door over
  rows nothing can make is not a loop. Runs start at POST /v1/auto/runs.
- **webhooks/{flowId}** — public ingress needs a delivery contract
  (signature, replay) that is not designed yet.
- **authentication / users / user-invitations / project-members / api-keys**
  — identity is Hanzo IAM; the product's auth is the gateway header.
- **projects / folders** — the project row IS the tenant boundary; it is not
  addressable by callers.
- **pieces/{name} registry, store-entries, templates, tables, todos,
  git-repos, ai-providers, flags, audit-events, sample-data** — v1
  Activepieces surface the v2 rewrite does not carry (each entry names where
  the capability lives when it lives anywhere).

Also NOT mounted, deliberately: the product's `/v1/brand` (deployment
branding — cloud's white-label layer owns brand per host) and `/v1/realtime`
(base's SSE stream; a streaming relay needs a projection contract typed ops
do not have yet — task-level work, not a drive-by).

## Seams and neighbors

- **Upstream**: `AUTO_UPSTREAM` (default `http://auto.hanzo.svc.cluster.local:80`,
  image listens on 8080) — the SAME env and default the knowledge lane's
  piece sync uses; one service, one name. No credential: the org header is
  the whole contract, so there is nothing for KMS to hold on this seam.
- **Deploy state**: universe's infra/k8s records the standalone auto
  Deployment/Service as removed (the pre-v2 engine was retired into
  /v1/automations); devnet/testnet manifests still run
  `ghcr.io/hanzoai/auto:latest`. Until ops re-lands the Service in the main
  cluster, /v1/auto/status answers `reachable:false` — honestly. Deploys are
  CI-only; this repo ships the door, not the cluster.
- **vs /v1/automations**: apps/automations is cloud's OWN goja piece runtime
  (in-process, ~280 JS pieces). /v1/auto is the STANDALONE product with
  durable tasks-backed execution and its own canvas. Two engines is a known
  tension; the product repo's LLM.md records the pre-v2 plan to collapse the
  standalone engine into /v1/automations, and the v2 rewrite superseded it.
  Deciding the one engine is task-level work; today each surface is honest
  about what it runs.
- **knowledge sync_piece drift, recorded**: apps/knowledge calls
  `/v1/auto/pieces/{piece}/run` with `X-Piece-Run-Secret` — an endpoint of
  the RETIRED TypeScript engine. The v2 product does not serve it; that lane
  needs re-aiming (at /v1/automations' goja runner or a v2 endpoint) before
  its long-tail connectors work again. Not this app's surface; named here so
  nobody rediscovers it.
