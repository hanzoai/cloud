// Package search is one ranked result set over everything your org has stored.
//
// It answers "what is RELEVANT" over a tenant's own data: it owns no store and
// fuses the three retrieval stores the platform already runs — the lexical index
// (apps/index), the vector index (apps/knowledge) and the org's own repositories
// (apps/code) — into one ranked result set.
//
// THIS IS THE ENTRY POINT. A caller asks here and does not choose a backend. The
// other three addresses stay exactly what they are — each backend's own surface, with
// the shape and the vocabulary that backend's clients need — and none of them is
// an address a caller should have to pick between. /v1/index speaks the
// Meilisearch dialect because a Meilisearch client repoints by changing one host;
// /v1/code/search answers spans with repo and line because that is what a coding
// agent wants; /v1/knowledge/search is the KB's own semantic read. Fusing them is
// not deleting them.
//
// IT IS MOUNTED. manifest/apps.go carries the row and plugin/search is built, so
// POST /v1/search reaches the wire. This comment said the opposite for as long as
// that was true and was never re-read when the row landed — the same rot the
// money refusals had, and the reason a claim about the fleet belongs in a test
// rather than a paragraph.
//
// WHAT IS STILL TRUE, and is the real problem: a caller has to know which of
// /v1/search, /v1/index/indexes/:uid/search, /v1/knowledge/search and
// /v1/code/search holds their answer, and gets a different request shape and a
// different score scale from each. This package exists to end that and has not
// yet: it fuses the lexical and vector legs, and the other two endpoints still
// answer beside it.
//
// WHAT BELONGS HERE. A query whose honest answer has a SCORE. A query whose
// honest answer has a TRUTH VALUE — the definition of a symbol, the callers of a
// function, a dependency edge — belongs to /v1/code (apps/code) and must not be
// forced through a relevance-ranked shape: a definition is not 0.87 relevant, it
// either is the definition or it is not.
//
// NOT HERE: /v1/websearch/search. That searches the PUBLIC WEB, not the
// customer's data. It has a different tenancy model (no org-scoped corpus), a
// different cost model (per-call to an external provider) and a different failure
// mode. It stays separate — do not fold it in.
//
// DEGRADATION IS THE CONTRACT. Every response names every backend it consulted
// and that backend's status. A leg that is down produces results from the
// surviving legs plus an explicit `degraded` entry carrying the error — never a
// silent empty. This is not a nicety: a silent empty is indistinguishable from a
// query the corpus genuinely has no answer for, so a leg that stops working is
// invisible for exactly as long as nobody thinks to ask it directly.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
package search
