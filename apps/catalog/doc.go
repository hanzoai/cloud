// Package catalog is one place to browse every project, app and site built here.
//
// It is the CROSS-ORG discovery lens: the corpus spans orgs, so a project is
// findable whichever org built it.
//
// It owns no store. The corpus lives in the lexical index (apps/index) — the
// same store the Meilisearch dialect serves — so relevance, paging, persistence
// and encryption at rest are the ones the platform already runs. What this
// package adds is the ONE thing the index cannot express on its own: a corpus
// that spans orgs.
//
// # How that stays safe
//
// The index pins every row to an org and every query to one org. Cross-org
// discovery is therefore not a weaker filter — it is a SECOND corpus:
//
//	PublicOrg ("~catalog")   the published, world-readable catalog. Every caller
//	                         reads it. Nobody can write it: an org id is minted
//	                         from a validated IAM owner claim and IAM org slugs
//	                         begin with an alphanumeric, so no principal can ever
//	                         BE "~catalog".
//	the caller's own org     their private projects, read with principal.Org and
//	                         nothing else — never a request field (HIP-0026).
//
// A customer's private project is a row in their own org's `catalog` index. It
// cannot appear in another tenant's results because the query that would return
// it is never run for them. Nothing PUBLISHES over HTTP either: the published
// corpus is reconciled from sources that are public by construction (sync.go),
// so no credential exists that could promote a tenant row into it. The swap
// itself is a call on the internal plane — a socket the edge router does not
// carry, reachable only from inside this deployment — so "no write route" stays
// literally true of every surface a caller can reach.
//
// Surface:
//
//	GET /v1/catalog   search + browse: ?q= &org= &kind= &archetype= &language=
//	                  &origin=template|community|third-party|product
//	                  &template=<parent id>   (lineage: what was forked from it)
//	                  &forkable=true|false &official=true|false
//	                  (absent = both; see filter)
//
// origin is the axis the two hanzo.app lanes are cut on — /templates browses
// origin=template, /community browses origin=community — so they are TWO VIEWS
// of this one corpus and not two catalogs that can disagree.
//
// There is no write route. The corpus reconciles itself (sync.go), which is why
// there is no credential that could publish into the published catalog at all.
package catalog
