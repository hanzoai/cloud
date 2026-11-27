// Package cloudflare is your Cloudflare account, managed from Hanzo: zones,
// Pages, Workers, Workers AI, R2, KV, D1 and AI Search.
//
// It is the per-org Cloudflare asset plane for the unified Hanzo Cloud binary —
// the first-class /v1/cloudflare/* surface (sibling of /v1/dns and /v1/domain)
// that drives an org's Zones/Analytics, Pages, Workers, Workers AI, R2, KV, and
// D1 through the SAME per-org, KMS-sealed API token the org
// connected via apps/provider. Connecting the provider stays on the
// integrations plane (/v1/provider/cloudflare/{connect,callback}); MANAGING the
// resources is this first-class plane — "how you connected" and "what you manage"
// are separated, one concern each. Every call drives Cloudflare with the org's own
// scoped token, so the platform never reaches Cloudflare with a global env token —
// one token, one custody boundary, one org.
//
// Workers AI is the one exception to pure passthrough: an /ai/run is INFERENCE, so
// it meters through the SAME unified usage/billing spine (cloud.AIMeterProvider) and
// emits to the SAME gen_ai o11y span plane as every other model call — at the thin
// BYO fee, since the org's own token already paid Cloudflare for the compute. There
// is no Cloudflare-specific usage or o11y path.
//
// TENANT ISOLATION (the crown jewel). Every handler resolves the caller's org from
// the VALIDATED principal (principal.Org → the X-Org-Id the identity boundary minted
// from a verified credential, HIP-0026 / SanitizeIdentity), NEVER from a body or
// query field. The org is then the ONLY input to token custody: the per-org token is
// read in-process through the ONE client provider.TokenFor, which keys KMS on that
// org (/orgs/{org}/integrations/cloudflare/api_token). So a request can ONLY ever
// address its own org's Cloudflare account:
//   - no validated principal ⟹ principal.Org fails ⟹ 403 (a forged X-Org-Id with no
//     bearer is refused by the identity boundary, then again here);
//   - a non-SuperAdmin bearer has X-Org-Id pinned to its own owner (SanitizeIdentity),
//     so it cannot name another org;
//   - cross-org token reach is structurally impossible — the token path is derived
//     from the validated org, not from any caller-controlled field.
//
// The token rides ONLY the Authorization header on the outbound Cloudflare request;
// it is never logged, echoed in an error, or stored by this subsystem.
//
// FAIL-CLOSED. An org that has not connected Cloudflare, an unmounted integrations
// plane, or a KMS that is not Ready each yield an error and a 503 — never another
// org's data and never a silent success.
//
// AI SEARCH NAMESPACES. An org's AI Search instances live in ONE namespace, derived
// from the validated org (aisearch.go aiSearchNamespace) and never named by a caller,
// so no instance this plane creates lands in Cloudflare's `default` namespace and a
// namespace never changes in place. The namespace is a Cloudflare grouping, not
// Hanzo's authorization or payer boundary: who may act and who pays are decided by
// the validated principal before Cloudflare is reached, and search queries — which
// need per-document ACLs a namespace cannot express — are not relayed here at all.
// LLM.md beside this file has the replacement procedure for moving an index.
package cloudflare
