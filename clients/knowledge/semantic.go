package knowledge

import "context"

// semantic.go is the vector leg's ONE export. /v1/search (clients/search) fuses
// this with the lexical leg (clients/index) and must reach the SAME per-org
// collection, the SAME embedding model, and the SAME payload filter that
// /v1/kb/search reaches — so it calls the identical searchDoc rather than growing
// a second retrieval path against the same store. Everything org-scoping and
// tenant-isolating stays in index.go; this file only widens its visibility.

// Hit is one semantic result. It is the retrieval hit shape verbatim (a type
// alias, not a copy) so the wire contract cannot drift between /v1/kb/search and
// /v1/search.
type Hit = hit

// SemanticReq is an org-scoped vector query. Org is set by the CALLER from a
// validated principal — never from a client field — exactly as searchReq requires.
type SemanticReq struct {
	Org      string
	Query    string
	Project  string
	DocTypes []string
	Limit    int
}

// Semantic runs the org-scoped vector leg. It returns an error (never a silent
// empty) when the store or the embedding gateway is unreachable, so the caller can
// report WHICH backend failed and why: the fail-empty behaviour that hid a
// five-day vector outage belongs to the surface's degradation contract, not here.
func Semantic(ctx context.Context, r SemanticReq) ([]Hit, error) {
	return index().searchDoc(ctx, searchReq{
		org:      r.Org,
		query:    r.Query,
		limit:    r.Limit,
		project:  r.Project,
		doctypes: sanitizeDocTypes(r.DocTypes),
	})
}

// SemanticReady reports whether the vector leg is configured (an embedding client
// and a store endpoint). A deployment without one is DISABLED, which the surface
// reports distinctly from DEGRADED — "never provisioned" and "provisioned and
// broken" are different operational facts and must not share a status.
func SemanticReady() bool { return index().enabled() }
