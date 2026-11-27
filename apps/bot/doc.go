// Package bot is a bot doing your work on a real desktop, live, while you watch.
//
// It is the whole cloud side of the headless bot: the CONTROL PLANE for a bot run
// — a task executed on a surface (a desktop or terminal sandbox the bot drives)
// with a LIVE session, the URL the hanzo.app /vnc panel embeds to watch or attach
// — TOGETHER WITH the transport to the service that executes it, @hanzo/bot.
//
// One product, not two. The control plane and the transport to the executor were
// separate apps once (apps/runtime), which made a LANGUAGE boundary look like a
// product boundary: the surface is Go, the executor is TS, and nothing else
// distinguished them. They answer for the same thing and now live in one place.
//
// CLOUD OWNS POLICY, THE EXECUTOR OWNS THE RUN. The sandbox lives in @hanzo/bot,
// keyed in its own store under the tenant that started it; that store is the only
// thing that knows whether a run is alive. So this package keeps no second copy of
// it. It owns what a control plane owns — who you are, which org you are, and
// whether you may — and then asks the executor, which IS the registry. Copying
// that state into cloud would create a second id space agreeing with nothing:
// listing runs that do not exist and stopping runs never started.
//
// A bot run is ONE value with ONE home. It is not the bot MACHINE that hosts an
// executor (/v1/compute/bots — a machine you rent), and it is not a NODE
// you already own and connect (apps/nodes, /v1/node).
//
// Isolation: the org is the gateway-minted X-Org-Id (HIP-0026) resolved via
// principal.Org, NEVER a request field, and it is what cloud sends the executor,
// which keys every run under tenants/{org}/. Beside it rides the person's own
// hanzo.id token, which is what the executor decides the org by: it gives its
// shared service token no org. An API key, which the executor cannot validate, is
// refused here before the executor is asked (run.go, caller). A caller cannot name
// another tenant's org, so it cannot read or stop another tenant's runs; a foreign
// run id resolves under the CALLER's org, where it does not exist, and answers 404.
//
// Two faces, and the split between them is what a tenant can ACT on:
//
//   - NATIVE + TYPED, the run control plane (org-scoped; the console BotsApi
//     calls it). Both are typed ops, so each is one registry entry with five
//     projections — the REST route, the OpenAPI operation with its schema, an MCP
//     tool, a CLI command and an SDK method:
//
//     GET  /v1/bot/runs              -> {bots:[{runId,task,surface,status,sessionUrl,startedAt}]}
//     POST /v1/bot/runs/:runId/stop  -> {runId, status}
//
//     There is no launch. The bot runtime exposes no launch operation, so no
//     address here claims one; it arrives with the runtime-side operation that
//     can prove a bot boots, with the entitlement gate and the meter beside it.
//
//   - RELAYED, the executor's own operational paths at /v1/bot/runtime/*
//     (relay.go). A liveness probe is not a tenant-scoped resource, so it stays a
//     relay rather than being reimplemented in Go.
//
// The transport itself (transport.go) knows how to MOVE BYTES and nothing about
// what they mean: a caller states WHAT it wants done (a Call) and gets back a
// domain-shaped outcome — never an *http.Response, a status code, or a framing
// detail. Today those bytes move over HTTP; per HIP-0106/HIP-0120 they should move
// over ZAP, and that swap is meant to be a change to transport.go plus each
// caller's one stub, not a rewrite. apps/coding dispatches its coding tasks to the
// same executor and uses the same Call.
//
// run.go is the run plane and this package's Mount; relay.go is the executor's
// ops face; transport.go moves the bytes between them.
package bot
