// Package samples is the fleet's compute-utilization time series: ONE table, ONE
// writer client, ONE read face that every compute source feeds.
//
// The fleet already reports lifecycle, liveness and static inventory — the Visor
// machines, the BYO workers that dialed in, the BYO clusters, the agent
// run-targets. None of it answers "how hot is this GPU / how loaded is this
// machine, over time, per org": hanzo.compute_usage is spend/lifecycle-shaped
// (org, app, project, kind, event, machine_id, size, price_cents, ts) and carries
// no cpu/mem/gpu, and o11y's samples_v4 carries no org. An agent run-target does
// carry a real Spec + Metrics, but only the LAST one — a snapshot on the row, not
// a series. This package is the missing plane, and it is the ONLY one: sources
// append here rather than growing private telemetry tables.
//
// It is deliberately a LEAF. It depends on the datastore client (ai/object) and the
// tenancy vocabulary (sdk/principal) and nothing else in cloud, so every
// compute source — apps/agents, apps/visor, ml — can import it without a
// cycle (apps/links imports apps/agents, and apps/agents imports this).
//
//   - Record(ctx, Sample)  append one utilization sample (samples.go)
//   - Series(ctx, Query)   an org's samples over a bounded window (read.go)
//   - Latest(ctx, org)     each unit's most recent sample, for a fleet board
//
// ISOLATION: org is the ONLY tenant key. It leads every statement as a BOUND
// parameter (never interpolated), and a blank or oversized org fails CLOSED on
// both the write and the read — a caller for org A can never write into, or read,
// org B's series. The caller MUST pass an org it already validated server-side
// (principal.Org / a verified claim), never a raw client header.
//
// AVAILABILITY: the warehouse is never in a caller's critical path. With no
// datastore configured, Record is a no-op that returns nil — a heartbeat, a
// session or a board must never fail because the time series is absent.
package samples
