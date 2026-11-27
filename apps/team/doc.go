// Package team is your org's shared space: documents edited together, files,
// seats, and agents as teammates.
//
// It is Hanzo Team: IAM sign-in and space selection, the transactor WebSocket a
// space's documents ride, collaborative editing on /v1/team/collaborator, per-space
// files, the wallet page with its plan + seats, and the org's agents projected in as
// bot members.
//
// The published surface is the typed ops themselves (plugin/team/openapi.json) —
// there is no second route table here to drift out of date.
//
// TENANT ISOLATION. The org (tenant key) is NEVER a client-supplied header on any
// data path:
//
//   - transactor: org = the `extra.org` claim of the HS256 space token in the
//     :token path segment, minted by selectWorkspace and VERIFIED (token.Decode
//     verify=true) against SERVER_SECRET before the WebSocket upgrade. Every docs
//     SQLite file lives at {DataDir}/team/spaces/orgs/<org>/projects/<ws>/docs.db.
//   - account RPC: org = the `extra.org` claim of the HS256 session token in the
//     bearer/cookie, likewise VERIFIED. Every account-store query filters by org;
//     selectWorkspace resolves the space scoped to (org, slug) so a foreign
//     tenant's slug is unresolvable.
//   - bots read routes: org = principal.Org(c) — the value the identity
//     middleware minted from the VALIDATED IAM owner claim (HIP-0026) — and never
//     a client X-Org-Id.
//
// The session-token org is itself minted from the VERIFIED IAM `owner` claim at
// the OAuth callback, so the chain IAM owner → session token → space token →
// docs path is signed end-to-end and a client can forge none of it.
//
// Order 138: binds /v1/team/* before the AI subsystem's /v1/* catch-all (150).
package team
