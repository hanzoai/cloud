// Package workflow is Hanzo Workflow: durable workflows that survive a crash, with
// every run visible and replayable.
//
// The engine is event-sourced, exactly-once and crash-recovering, previously
// fronted by a standalone tasksd + cluster Service; this mounts its HTTP + UI
// surface natively onto the unified cloud binary per HIP-0106 — the follow-up named in cloud's durable.go
// ("consolidating that surface into cloud").
//
// ONE ENGINE. cloud already embeds the single in-process durable engine in
// durable.go (installDurableIngest → cloud.EmbeddedTasks), shared by ai's durable
// ingest. This subsystem does NOT Embed a second engine; it mounts that ONE
// engine's HTTP handlers on the shared zip mux, so the Workflow product (console/
// studio) reads the SAME durable state as ai ingest — one engine, one binary,
// one way. The engine is created after UseAll, so the surface resolves it
// LAZILY per request (503 until it is live).
//
// Surface (all under /v1/workflow/*; the studio is its own image on tasks.hanzo.ai):
//
//	/v1/workflow/health                    generic liveness (cloud's per-subsystem contract)
//	/v1/workflow/settings                  capability flags (open bootstrap)
//	/v1/workflow/cluster[/health]          cluster status (open probes)
//	/v1/workflow/namespaces|nexus|...      engine JSON API (identity-gated)
//	/v1/workflow/mcp                       MCP tool surface (identity-gated)
//	/v1/workflow/events                    SSE realtime stream (identity-gated)
//
// Identity: cloud's gateway validates the IAM JWT and mints X-Org-Id / X-User-Id
// (HIP-0026). gate resolves those two through sdk/principal — the ONE place the
// cloud data plane turns a request into an org — and refuses (403, never the
// unscoped store) anything that decision refuses, then threads the validated org
// into the engine via tasks/pkg/auth.WithIdentity, so per-(org,ns) shard scoping
// applies.
package workflow
