# LLM.md — apps/world

Guidance for apps/world. Moved from the root LLM.md; cross-cutting rules stay in the root.

## /v1/world — one product, one owner, and the two wires the document cannot carry

`apps/world` is the SINGLE cloud owner of `/v1/world` (`manifest/apps.go`, one
row, one prefix). It serves six operations: `GET /v1/world` (the entry point),
`GET/PUT /v1/world/pipeline`, `GET /v1/world/news`, `GET /v1/world/limits`, and
`GET /v1/world/stream` — five typed, one refused for the wire fact that SSE has
no `Out` (`untypedByDesign`, prose via `spec.Describe`). `/v1/world/health` is
not world's: it is the fleet liveness route `serve.go` registers for every app.

**Two more wires answer under this prefix and cloud does not route them.**
`universe/infra/k8s/ingress/routes.yaml` router `api-hanzo-ai-world-gw`
(priority 100) carves `/v1/world/mcp` and `/v1/world/zap` off the cloud catch-all
straight to `world-gw.hanzo.svc:9999`, rewriting to the gw's native `/mcp` and
`/zap`. Measured: `initialize` on the MCP endpoint answers 200 unauthenticated
(`serverInfo: hanzo-world`), `tools/list` is fail-closed JSON-RPC `-32001`, and
`/v1/world/zap` is `401 missing_token`.

The generated document CANNOT declare those two, and that is correct, not a gap:
`spec.Describe` renders prose only for a route this router actually serves
(`sdk/spec/register.go` — "the registry still cannot add an operation"), which is
the property that stops the document claiming something nothing answers. So
`GET /v1/world` names them instead. That op is the ONLY place in the product's
own surface where those addresses appear — `index.go`, gated by
`TestIndexNamesEveryWireCompletely`. It deliberately does not restate the REST
operation list; `GET /v1/openapi.json` stays the one enumeration of those.

**An op that IS the prefix must be declared absolute on the app.** `zip.Get(g,
"")` composes to `/v1/world/`, a different route, so `Mount` takes
`cloud.ZipApp(app)` and registers absolute paths — the same reason and the same
form as `apps/pricing` and `apps/plan`. A group for the leaves plus an app-level
exception for the one bare op would be two idioms for one job.

### Do NOT declare ops that forward to hanzoai/world's REST data plane

`hanzoai/world` is labelled TypeScript by GitHub because of its frontend, but it
is a **Go module** (`module github.com/hanzoai/world`, 150 Go files) whose
`internal/world/routes.go` registers 117 routes, including the AI-plane read
surface its own `handlers_worldgw.go` names: `events`, `conflicts`, `infra`,
`vessel`, `news`, `markets`, `feeds`.

Those routes are **deployed nowhere.** `world.hanzo.ai` runs **v2.4.37** (its
`/v1/world/version`) while that surface landed after it — `package.json` on main
is 2.4.60 — so `GET world.hanzo.ai/v1/world/events` returns the Go server's own
catch-all body, `{"error":"Not found: /v1/world/events"}`. Declaring cloud ops
that forward there would publish operations into `openapi.yaml`, every generated
SDK and every MCP tool list that answer 404 in production: the same dark hole the
`api-hanzo-ai-catalog` router opened under `/v1/models` and `/v1/pricing`, which
cost 17 documented-but-uncallable operations. Re-home that surface only after the
upstream ships it, and prove the upstream answers before declaring anything.
