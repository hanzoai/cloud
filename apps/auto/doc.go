// Package auto is workflows that run themselves, on a schedule or a webhook.
//
// An org authors flows — a trigger and a tree of connector actions — and this
// runs them durably at /v1/auto and keeps their run history.
//
// It composes THREE existing clients rather than reinventing them:
//
//   - apps/integrations — per-org connector credentials (KMS-sealed). Connectors
//     reach a token ONLY through provider.TokenFor, never KMS directly.
//   - cloud.EmbeddedTasks — the ONE shared in-process durable engine. A flow runs
//     as a durable workflow in the OWNER's namespace (engine.go).
//   - sdk/principal     — the ONE tenant gate. Every data handler resolves the
//     org from principal.Org; a client-forged X-Org-Id with no bearer is refused.
//
// Surface (all under /v1/auto/*, all org-gated except the compose-root
// generic GET /v1/auto/health). A ✓ marks a TYPED op — one registry entry
// from which the REST route, the OpenAPI operation, the MCP tool, the CLI command
// and every generated SDK method follow; the four without one are routes and
// nothing else, and each names the wire fact that keeps it there at its handler:
//
//	✓ GET    /v1/auto/provider             the connector catalogue (org-gated)
//	✓ POST   /v1/auto/provider/:id/run     run ONE provider action written in JavaScript (script,
//	                                                sub-mounted below — typed there, and the reason this
//	                                                prefix serves 27 routes while routes() registers 26)
//	✓ GET    /v1/auto/flows                  list flows
//	✓ POST   /v1/auto/flows                  create a flow (+ initial draft version)
//	✓ GET    /v1/auto/flows/:id              flow + latest version
//	✓ PATCH  /v1/auto/flows/:id              update flow metadata
//	✓ DELETE /v1/auto/flows/:id              delete a flow (+ versions + runs)
//	✓ GET    /v1/auto/flows/:id/versions     list versions
//	✓ POST   /v1/auto/flows/:id/versions     create a draft version
//	  POST   /v1/auto/flows/:id/operations   apply a FlowOperation — TWO Out shapes
//	✓ POST   /v1/auto/flows/:id/run          start a durable run
//	✓ POST   /v1/auto/flows/:id/enable       enable (POLLING → CreateSchedule)
//	✓ POST   /v1/auto/flows/:id/disable      disable (POLLING → DeleteSchedule)
//	✓ GET    /v1/auto/automations            list automations (?q=, ?sort=name|next|updated)
//	✓ POST   /v1/auto/automations            create an automation and arm its schedule
//	✓ GET    /v1/auto/automations/:id        one automation
//	✓ PATCH  /v1/auto/automations/:id        change it, or enable/disable it
//	✓ DELETE /v1/auto/automations/:id        delete it, its schedule and its runs
//	✓ POST   /v1/auto/automations/:id/run    start one run now (one at a time)
//	✓ GET    /v1/auto/automations/:id/runs   its runs, each linking its Dev run
//	✓ POST   /v1/auto/automations/:id/runs/:run/review  post or discard a held post
//	✓ GET    /v1/auto/templates              the starter automations
//	✓ GET    /v1/auto/runs                   list runs
//	✓ GET    /v1/auto/runs/:id               run detail (refreshed from engine)
//	  POST   /v1/auto/runs/:id/resume        resume a paused run — arbitrary JSON in
//	  POST   /v1/auto/hooks/:source/:event   inbound event sink — raw-byte dedupe
package auto
