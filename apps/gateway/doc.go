// Package gateway is live control of the policy your API applies to every
// incoming request: CORS, rate limits, cache TTL and allowed methods, changed
// without a redeploy.
//
// THE GATEWAY IS PLUMBING, AND THIS IS ITS ONE PRODUCT ENDPOINT. The gateway is the
// trust boundary — validate the IAM JWT, strip client-supplied identity, re-mint
// X-Org-Id — and it is not a network hop: it is compiled INTO the cloud binary as
// gateway.Use, and hanzoai/gateway's own routes.go states the law ("ONE routing
// source of truth = cloud's mount table, not a second map here"). Plumbing earns no
// prefix. What earns this one is the thing a customer actually calls: the runtime
// config plane for that policy, at /v1/gateway/config, and nothing else. It serves
// GET/PUT over the SAME edge.Store the EdgeCORS/EdgeRateLimit middleware and
// ScopeRateLimit read live, so an operator retunes the CORS allowlist, the pre-auth
// per-IP flood cap, or a tenant's authenticated rate ceiling with NO redeploy,
// replacing config that used to be baked into an image.
//
// TWO IAM-gated scopes (mirrors apps/pricing/enablement.go: global state is
// SuperAdmin-only, self-service is scoped to the validated tenant):
//
//   - PLATFORM policy (CORS origins, per-IP cap + window) — pre-auth edge knobs
//     with no tenant at evaluation time. Writable ONLY by a SuperAdmin
//     (principal.Super ⟺ owner == admin org). A PUT carrying any platform field is
//     routed to the platform row explicitly (PutPlatform), so it lands correctly
//     even when the SuperAdmin is org-switched to another tenant.
//   - PER-ORG policy (OrgRPM, the authenticated ceiling; CacheTTLSec + CachePaths,
//     the edge-cache TTL; Methods, the accepted-method allowlist) — a tenant's own
//     row of self-service edge config. An org admin writes its own (org from
//     principal.Org, never a raw header); a SuperAdmin may target any tenant with
//     ?org=<slug>. A member does not, and no API key does: every field governs
//     the whole org, so it is written by a signed-in admin (see writer).
//   - ORG_CAP, the most an org's ceiling may be, is the same kind of field: on a
//     tenant's row, written only by a SuperAdmin; a tenant's org_rpm only lowers.
//   - MODE, the abuse gate's posture, lives on a tenant's row but is NOT
//     self-service: a control's subject may not switch the control off, so writing
//     it requires SuperAdmin whichever row it lands on. It is the one field on this
//     surface whose scope (which org it applies to) and whose authority (who may
//     set it) are different questions.
//
// The store is the host's (host.EdgePolicy), one per process and shared with the
// edge middleware; this subsystem does not close it (the host closes it once at
// shutdown), so there is one
// store, one source of truth. Per-PROJECT rate scoping is NOT duplicated here — it
// remains ScopeRateLimit's commerce-configured domain (per (org,project,service)).
package gateway
