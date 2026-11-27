// Package auditlog is your org's tamper-evident audit trail: every security-relevant
// event, hash-chained and readable.
//
// It mounts the ORG-SCOPED surface GET /v1/audit — an org admin's read of THEIR OWN
// organization's events off the same store the AuditTrail middleware writes and the
// admin god-view (/v1/admin/audit) reads.
//
// WHY THIS EXISTS. An enterprise buyer's own compliance team must be able to see
// "what happened in MY org" — the audit trail is table stakes for SOC 2 / ISO / a
// security review — WITHOUT being a fleet operator. The pre-existing surface was
// admin-ONLY (/v1/admin/audit, s.guard → SuperAdmin), so a normal org owner had
// no audit route at all. This adds exactly the customer-facing, org-scoped read.
//
// TENANT ISOLATION (the whole point). The org is the VALIDATED IAM owner claim
// (principal.OrgFrom — the org cloud.Bridge parked from the trusted X-Org-Id the
// identity middleware minted from the caller's verified bearer, HIP-0026; NEVER a
// client-supplied header, and NEVER an In field, which is caller-supplied).
// Filter.Org is PINNED server-side to that org and the request type carries no org
// field at all, so a caller can only ever read its OWN org's events — the per-org
// READ twin of the admin god-view. Fail-closed: no validated principal → 401, no
// store → 501.
//
// The tamper-evidence GLOBAL verify (whole-chain hash walk) stays admin-only — it
// is a fleet property that would cross tenants — but every row carries its own
// hash/prevHash so an org admin still sees the chain linkage of their events.
//
// Registered as "auditlog" (NOT "audit") + order 144: the name diverges from the
// /v1/audit route so serve.go's generic GET /v1/<name>/health liveness route parks
// at /v1/auditlog/health and never shadows the real trail. Order 144 binds
// /v1/audit before the ai subsystem's /v1/* catch-all (150).
package auditlog
