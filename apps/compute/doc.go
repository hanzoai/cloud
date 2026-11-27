// Package compute is the compute you rent from Hanzo: machines, GPUs and clusters
// — launch one, resize it, tear it down.
//
// It serves the tenant's machines, GPUs and DOKS clusters as clean REST off the
// unified cloud binary, fronting Visor (the cloud OS at visor.hanzo.svc that
// OWNS compute). It exists so
// the console's Machines / GPUs / Clusters pages read real per-org compute from
// ONE place (api.hanzo.ai/v1/*) instead of the god-mode /paas admin proxy that
// 501s until a service token is wired.
//
// This subsystem OWNS no compute state — Visor does. It is a thin, tenant-scoped
// translator: it maps Visor's verb-style + resell endpoints to the clean REST the
// console already speaks, and re-shapes Visor's objects into the exact JSON the
// console normalizers consume (see types.go). It never fabricates: a GPU row is a
// real GPU machine's accelerator, a cluster is real node pools, and telemetry
// Visor does not carry is honestly omitted (renders "—"), not invented.
//
// THE SURFACE DESCRIBES ITSELF. Every route with a request/response shape is a
// TYPED op (zip.Get/Post/Delete[In, Out]) registered in Mount, so the route, its
// In/Out schema, the prose on its handler and the doc comment on every field are
// ONE declaration projected into REST, /.well-known/openapi.json, the /mcp tool
// list and the CLI. There is deliberately no route table in this comment: a
// hand-kept list is a second copy, and the one that used to live here had already
// drifted — it was missing the whole /v1/compute/fleet and /v1/compute/k8s
// surface. Read Mount, or ask the running deployment.
//
// Five routes stay raw, each because it has no shape to state rather than because
// nobody got to it: the two launches are polymorphic on the wire (a dryRun quote
// or a created resource), the two catalog reads pass Visor's payload through
// verbatim, and the bot action verb streams the agent's answer back untouched.
// Each carries a note at its registration.
//
// The tenant (principal.Org) is passed to Visor as ?owner=<org>, so a caller
// can only ever read or mutate their OWN tenant's compute; the org is taken from
// the validated IAM owner claim, never a client field.
package compute
