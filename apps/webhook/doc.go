// Package webhooks is how your app hears about events: register an endpoint,
// pick the events, get each one delivered and signed.
//
// It is the platform-global webhook layer (HIP-0106) — ONE registry plus ONE
// dispatcher that delivers ANY event on the platform bus to org-registered HTTP
// subscribers. It supersedes commerce's local, billing-scoped delivery path — the
// webhook surface is /v1/webhook, top-level, never under /v1/billing, and it fans out
// EVERY platform event, not just commerce's.
//
// TWO ORTHOGONAL HALVES.
//
//   - REGISTRY (api.go, store.go) — /v1/webhook CRUD. An org manages ONLY its own
//     endpoints; each org's registry is a physically separate {DataDir}/orgs/{slug}/
//     webhooks.db (the apps/books per-org SQLite idiom via cloud.OrgStore), so one
//     tenant can never read or mutate another's. The org is the VALIDATED principal
//     (principal.Org, gateway-minted X-Org-Id), exactly like apps/notify — 401 for
//     an unauthenticated caller.
//   - DISPATCHER (dispatch.go, match.go) — a durable JetStream consumer on the platform
//     bus (apps/pubsub), over the streams dispatch.go names: COMMERCE (commerce.>, owned
//     by hanzoai/commerce) and the event plane (event.>, owned by apps/analytics, which
//     publishes it). It resolves each event's org from the envelope, matches ONLY that
//     org's active subscriptions (NATS subject-wildcard semantics), and POSTs each match
//     with a fresh HMAC-SHA256 signature and a bounded retry ladder. Org isolation is by
//     construction: the store lookup is per-org, so B's endpoint can never receive A's
//     event.
//
// CONSUMER ONLY. This package publishes nothing and owns no stream. It once held the
// publish half of the event plane too — its own stream name, its own envelope — which
// made two subsystems owners of one subject space; JetStream answers that with
// "subjects overlap with an existing stream", so the second owner to arrive delivers
// nothing, on every stream, forever. The producer lives with the data now
// (event.PublishEvents).
//
// FAIL-SOFT MOUNT. The registry always mounts. The dispatcher is best-effort: a down bus
// ⇒ background reconnect-retry. A messaging fault never crashes the process.
package webhook
