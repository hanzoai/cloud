// Package flags is feature flags: ship it dark, then turn it on for who you pick.
//
// Define a flag, target it by property or percentage rollout, and evaluate it
// for an identity — plus the platform switches operators flip to change how the
// deployment behaves without a redeploy.
//
// Definitions live in per-(org, project) SQLite (cloud.OrgDB —
// {DataDir}/orgs/{org}/projects/{project}/flags.db, encrypted at rest via cek) and
// evaluation runs in-process through the embedded hanzo-flags evaluator
// (github.com/hanzoai/flags/go) with PostHog-compatible semantics: rollout hash,
// full property-operator set, variants, payloads. Stateless and scalable by
// construction — no KV, no network hop, every pod evaluates from its own hot
// in-memory copy of the definitions.
//
// TWO surfaces, ONE engine:
//
//   - /v1/flag — the product API (org-scoped via the gateway principal): evaluate
//     flags for a distinct_id + properties, manage definitions, read the activity
//     log. The verdict carries each flag's state, variant and payload.
//
//   - the PLATFORM switches — the launch/ops knobs the SuperAdmin flips from
//     admin.hanzo.ai (registry below). They evaluate from the reserved
//     platform/platform store through the same engine. A switch with no stored
//     definition falls back env -> literal default — exactly the pre-engine
//     behavior, zero regression; the first cockpit write creates the definition
//     and takes effect within one cache TTL (default 15s), no redeploy.
//
// FAIL-SAFE. When a store read fails, evaluation degrades to env fallback ->
// literal default and the HTTP surface says so honestly — never fail-wrong.
//
// ── THE POLICY PRIMITIVE ─────────────────────────────────────────────────────────
//
// This engine IS Hanzo's runtime decision primitive: (Principal, context) -> verdict,
// evaluated in-process, stateless, hot. It knows ONLY flags — definitions, evaluation,
// and the platform-switch registry. It has ZERO knowledge of any specific policy that
// rides on it: no host→service map, no waitlist, no service registry. Those COMPOSE
// this engine from the OUTSIDE, one-way (they import flags; flags imports none of them):
//
//   - the launch waitlist gate (apps/admission) — a service's mode IS the switch
//     waitlist.<svc>; admission owns the host→service registry + Enforce and reads the
//     mode through flags.Bool. It USED to live in this package; extracting it is the
//     PROOF the engine composes its tenants rather than absorbing them.
//   - authz        (access policy)          — (Principal, resource+action) -> allow/deny
//   - entitlements (product-access policy)   — (Principal, feature/plan)    -> granted/denied
//
// Every one is (Principal, context) -> verdict. Folding them onto this evaluator makes
// Policy ONE composable primitive with one audit log and one hot-apply path. authz and
// entitlements are NOT built on it yet — this note only names the target so the client
// stays visible; the launch waitlist (now external) is the first, proven composition.
package flags
