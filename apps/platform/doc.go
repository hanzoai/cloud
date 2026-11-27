// Package platform is Hanzo PaaS: deploy containers to your own tenant
// namespace — builds, releases, environments, logs, custom domains.
//
// It is the per-org container platform at /v1/platform — projects,
// applications, builds, deploys, environments, releases, logs and verified
// custom domains, each app reconciled into the caller's own tenant-<org>
// Kubernetes namespace.
//
// Relationship to the sibling subsystems:
//
//   - fleet.go       (/v1/platform/fleet) — the ADMIN fleet drift board: observes +
//     deploys SYSTEM Service CRs across the platform namespaces, SuperAdmin
//     only. It answers "what is the fleet running, and roll a tag."
//   - apps/projects (/v1/project)  — per-org STATIC sites (S3 hosting).
//   - apps/platform (/v1/platform)  — THIS: per-org CONTAINER apps. Users
//     create projects + applications, build them (arcd BuildKit) and deploy them
//     (operator hanzo.ai/v1 Service CR into their OWN tenant-<org> namespace).
//
// All three share the ONE deploy mechanic — write an operator CR, let the
// operator reconcile — but /v1/platform is per-tenant: every route is scoped to
// the gateway-minted, IAM-VALIDATED X-Org-Id (c.Org()); the deploy namespace is
// DERIVED from that org (tenant-<org>), never taken from the request. A tenant
// can never read, build, or deploy into another org's namespace. That is the
// red-team bar and it is structural: cross-tenant identifiers are simply not
// inputs to any handler.
//
// The handlers below run natively on zip, so the binary keeps ONE router and
// every route stays behind the SanitizeIdentity trust boundary. That router is
// the ONE source of the published contract (openapi/ projects it). The Goa
// design module beside this package (apps/platform/design — its own go.mod,
// not built or tested here) is a SECOND source and has already drifted: it
// emits 15 operations against the 30 this package registers. Do not read it as
// the contract, and do not regenerate against it.
package platform
