// Package trust is your trust centre: the controls you publish, the coverage
// they compute to, the documents a reviewer asks for, and who you send data to.
//
// It serves /v1/trust/*. Any organization can publish one — this is a product,
// not a page — and the deployment's own is simply the first tenant. What
// separates the two is where their controls come from, and that is the only
// difference anywhere in the surface:
//
//   - the DEPLOYMENT's inventory is github.com/hanzoai/trust's controls.json,
//     compiled into the goja bundle, gated at build time by goja/check.mjs, and
//     unreachable from any request. Adding one is a commit.
//   - every OTHER organization authors rows in its own Base/SQLite file, held to
//     the SAME validator, folded by the SAME arithmetic.
//
// So nobody's coverage — including ours — is a number anyone typed. It is a fold
// over an inventory, against the whole published clause list of each framework,
// and an `absent` control still names the clause it would satisfy without ever
// moving a count.
//
// WRAP, DON'T REWRITE (HIP-0106). The logic is TypeScript in
// github.com/hanzoai/trust, bundled for goja; this package is the host. It is
// apps/captable's shape — the reusable apps/goja Base binding, one SQLite file
// per tenant, one transaction per request — with two tenant-bound host globals
// this capability needs and captable does not (goja.BaseConfig.Bind):
//
//	__own()    is this tenant the one whose inventory is compiled in
//	__audit(q) the platform's audit trail, ALREADY bound to this tenant
//
// TWO ENDPOINTS. /v1/trust/* is the caller's OWN centre, resolved from the
// validated bearer. /v1/trust/published/:org is what a visitor with no
// credential reads, and it is a different question, not a weaker check: a
// published trust centre is a public document addressed by a public name, the
// way a site is addressed by its slug. It answers only for an organization that
// has published, it never carries a gated document's address, and it reaches no
// other route.
//
// NEITHER ENDPOINT SERVES BYTES. A document is metadata here — title, kind, date,
// and whether it is released. The bytes and the grant belong to apps/dataroom,
// which already does per-page view tracking; this surface says what exists and
// what it takes to read it, which is the part a reviewer can check.
package trust
