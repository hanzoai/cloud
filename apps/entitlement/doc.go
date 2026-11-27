// Package entitlements is what your org may run: what the plan grants, and which of
// those products are switched on.
//
// Both authorities live here, one endpoint each, and the package's whole discipline
// is that they are never braided.
//
// Surface (/v1 only):
//
//	GET  /v1/entitlement            -> per-app booleans from the org's PLAN (projection.go)
//	GET  /v1/entitlement/orgs/:org  -> { "enabled": ["engine","chat",...] }
//	POST /v1/entitlement/orgs/:org  { "add":[...], "remove":[...] }  -> { "enabled":[...] }
//
// TWO AUTHORITIES, NEVER BRAIDED.
//   - ENABLEMENT (this store): which products the org has toggled on. The org's
//     intent. Durable SQLite — the deployment's own "entitlements" — keyed
//     (org, product).
//   - ENTITLEMENT (commerce): which products the org's plan/subscription grants.
//     The billing truth. Read via deps.Commerce.CheckEntitlement at WRITE time.
//
// A product may only be ENABLED if it is ENTITLED — so a non-super-admin can only
// switch on what the org already pays for; enabling never spends new money (a plan
// upgrade happens in commerce, not here). DISABLING is always allowed (turning a
// product off is never gated). A SUPER ADMIN (owner==AdminOrg) BYPASSES the commerce
// gate — the operator can comp/grant any product to any org — and may target ANY :org.
//
// ORG SCOPING mirrors apps/kms (/v1/kms/orgs/:org), and so does the address:
// {:org} must equal the
// caller's VALIDATED org (c.Org()), unless the caller is a super admin (principal.Super,
// minted only for owner==AdminOrg by SanitizeIdentity — never client-forgeable), who
// may act on any org. A bearer-less forge (X-Org-Id restored, no X-User-Id) fails the
// principal.Validated gate → 403. There is no path a caller reads or writes another
// org's entitlements.
package entitlement
