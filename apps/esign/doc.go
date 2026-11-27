// Package esign is a document out for signature, signed and filed with an audit
// trail.
//
// It folds hanzoai/esign (the Documenso fork — "open-source DocuSign") into the
// unified hanzoai/cloud binary as an in-process subsystem (HIP-0106, task #100,
// epic #96). Cloud serves the e-signature surface (/v1/esign/*) ITSELF — per
// tenant, on Base/SQLite — no Next.js/Remix pod, no Prisma, no Postgres.
//
// WRAP, DON'T REWRITE — the read-WRITE variant, reusing the SAME client captable
// (the #96 pilot) established: the server-side domain (documents, recipients,
// fields, the signing flow/state machine, audit trail, completion) is ported to
// a self-contained goja bundle in github.com/hanzoai/esign; the REUSABLE
// clients/goja binding runs it and gives it PERSISTENCE over per-tenant
// Base/SQLite (__db/__newId/__now, one SQLite file per tenant, ONE transaction
// per request). This leaf adds ZERO storage glue of its own.
//
// THE HARD PART — PDF + PKI — is the one capability goja cannot provide: it is
// implemented as Go host-functions (signer.go: pdfcpu render + digitorus/pdfsign
// x509/PKCS#7 seal) and injected via the additive goja BaseConfig.HostFns as
// __pdf = { stamp, sign }. The signing-request/recipient/field/audit LOGIC and
// the seal ORCHESTRATION stay in the TS bundle; only the crypto/PDF primitive is
// Go. A real signed PDF comes out.
//
// TENANCY. Owner routes (/v1/esign/documents/*) resolve the tenant from the
// VALIDATED cloud principal, never a client header, and admit only the platform's
// Admin scope (cloud.Admin: a SuperAdmin, or an admin of that org) — a member who
// is neither is refused 403, and a sender's change a page carried with their
// cookie is refused too (cloud.CSRF). Recipient token routes
// (/v1/esign/o/:org/sign/:token) are unauthenticated capability links: the
// crypto-random token is the whole credential, and it is what selects the tenant
// DB — resolved through the cross-tenant token index (index.go) BEFORE any
// per-tenant store is opened. The :org segment is the caller's claim
// about which tenant they mean and is only checked against that answer. NewBase
// pre-routes the bundle's db to the resolved tenant, so isolation is a host
// property.
//
// ACTIVATION: esign is NOT staged — it mounts under the mount-all default (empty
// CLOUD_ENABLE), so the one binary serves /v1/esign/* from first boot. The
// standalone esign pod holds NO tenant data (its SQLite has zero documents,
// recipients and users — only operational churn), so cloud's fresh per-tenant
// Base/SQLite is authoritative from the first write, with nothing to migrate; the
// empty esign pod is retired by this fold.
package esign
