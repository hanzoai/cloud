// Package reference is the lookup data a risk decision needs but cannot derive:
// which email domains hand out throwaway inboxes, which addresses belong to a
// datacentre or a Tor exit, which card scheme an issuer prefix belongs to, which
// browsers the fleet sees everywhere, and how current the designation lists the
// screening engine holds actually are.
//
// EVERY SET IS A VERSION, AND EVERY ANSWER NAMES IT. A version is the content
// digest of what was taken, so the same data is the same version whoever fetched
// it, and a decision can record one string that an auditor resolves back to a
// publisher, a licence and a date. A set that has never loaded REFUSES rather
// than answering "not listed", because a jurisdiction list that answers "not
// listed" because it was never loaded is indistinguishable from a clean world —
// the discipline luxfi/aml pkg/reference states and this plane generalises.
//
// FRESHNESS IS ITSELF A RISK SIGNAL, so it is on the wire: every answer carries
// the version, when its oldest contributing publisher was current, how old that
// is, and whether it is past the bound. A stale set still answers — yesterday's
// list beats none — and says that it did.
//
// TWO PLANES, TWO STORES, ONE PRECEDENCE. The Hanzo-maintained BASELINE lives in
// the shared warehouse (store.go) and its tables have NO tenant column at all:
// there is nowhere in the shape for an organisation to go, so a cross-tenant
// write is unrepresentable rather than merely forbidden. A tenant's OWN allow
// and deny entries live in that organisation's own SQLite file (override.go),
// reached through the one entry point a validated org has. Resolution is
// override first, then baseline; first hit wins.
//
// WHAT MAY BE IN THE BASELINE. Data someone else published under terms we hold —
// every source in the catalog states its licence — and aggregates over fleet
// traffic that pass a k-anonymity floor no single organisation can reach alone.
// Nothing derived from one organisation's rows, ever. Where a source we would
// want needs a licence we do not have, the catalog declares it as a CLIENT that
// refuses, because an unlicensed set and an absent one look identical from the
// outside and only one of them is a decision.
//
// Surface (/v1 only):
//
//	GET    /v1/reference            every set, its version and its freshness
//	GET    /v1/reference/{set}      one set, plus this org's overrides
//	PUT    /v1/reference/{set}      write this org's overrides
//	DELETE /v1/reference/{set}      clear one of this org's overrides
//	POST   /v1/reference/resolve    resolve keys, naming the version consulted
//	POST   /v1/reference/refresh    take a new version of a set (SuperAdmin)
package reference
