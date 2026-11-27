// Package analytics is product analytics: send an event, read back who did what.
//
// It is the product-event plane: it owns the ingest endpoint every Hanzo client
// posts to, lands each event in the `hanzo` warehouse, and serves the per-org
// read lenses — KPIs, time series, rankings, captured errors — over what it
// wrote.
//
// It is BOTH halves, and that is deliberate: one write core (ingestEvents) behind
// N endpoints (event.go), and the read lenses over the same warehouse, so a fact
// is admitted, stamped with the SERVER-resolved tenant, and read back through one
// vocabulary. Two lenses share one warehouse:
//
//   - LLM lens (REAL today): hanzo.cloud_usage, the live per-org usage ledger the
//     cloud o11y path already writes (requests, tokens, spend, models, errors).
//   - Web/commerce lens: event.fact (signal='act') on the o11y-owned event plane — what this
//     package's own endpoints ingest (as facts, landed by the sink in warehouse.go).
//
// The accepted batch is also handed to registered SINKS (forward.go) — apps/
// destinations forwards it to the org's connected ad platforms. analytics never
// imports a consumer; the client is one-way and fail-soft.
//
// ONE datastore client. This package does NOT open its own connection: it reads
// through apps/datastore, the leaf that holds the warehouse connection for the
// whole binary and opens it from the environment on first use. DRY: one transport,
// one pool, one set of KMS-injected DATASTORE_* creds — never hard-coded, never a
// second design. (It used to reach the connection through ai/object's Bootstrap;
// that path cost 1,933 packages to call four functions, which is why the connection
// moved to a leaf importing only orm/datastore.)
//
// TENANT ISOLATION is the security bar and is enforced SERVER-SIDE on every
// request. The org is c.Org() — the value SanitizeIdentity minted from the
// VALIDATED bearer owner claim (HIP-0026), never a client header — AND every
// request must carry a validated principal (c.User() set, which SanitizeIdentity
// sets ONLY for a verified bearer), exactly as apps/s3 does. Every
// datastore query binds the org POSITIONALLY (query.go llmWhere/eventsWhere), so
// a maxpower token can NEVER read another org's analytics.
//
// Surface (all org-scoped; /v1 only):
//
//	READ
//	GET  /v1/event/overview     per-org KPIs (llm real; web/commerce honest-empty)
//	GET  /v1/event/timeseries   requests/tokens/spend over time (hour|day buckets)
//	GET  /v1/event/top          top models (real) + products + behavior lenses
//	                                (topPages/topReferrers/topSources over the events lens)
//	GET  /v1/event/errors                 the caller org's most recently captured errors
//	GET  /v1/event/economic               the caller org's payments with other orgs, by year
//	POST /v1/event/economic/dispute       the caller org disputes one of them, amending nothing
//	GET  /v1/event/insights/events        the caller org's most recent product events
//	GET  /v1/event/insights/health        the insights surface is serving
//	GET  /v1/event/health       subsystem health (datastore connectivity + lens tables)
//
//	WRITE (the ingest endpoint — see endpoints, event.go)
//	POST /v1/event                  the canonical wire (object | array | {batch:[…]});
//	                                decodeEvent sniffs the PostHog wire here too
//
// The seven reads and the dispute above /v1/event/health are TYPED ops, so each
// publishes its prose, its In/Out schema, an MCP tool and a CLI command.
// /v1/event/health and the ingest endpoints are untyped and cannot be typed without
// moving their wire; routes (below) names each one's blocker where it is registered.
//
// Registered as id "event" with cloud.HealthOwner + order 132: it serves its
// OWN /v1/event/health (below), and cloud.HealthOwner makes serve.go skip the
// generic GET /v1/<name>/health so the always-ok route never shadows the real
// probe — the same flag the kms/paas/s3 subsystems use. Order 132 binds
// /v1/event/* before the ai subsystem's /v1/* catch-all (150).
package event
