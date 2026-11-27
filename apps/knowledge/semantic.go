package knowledge

import "context"

// semantic.go is the vector leg's ONE export. /v1/search (apps/search) fuses
// this with the lexical leg (apps/index) and must reach the SAME per-org store,
// the SAME embedding model, and the SAME reach that /v1/knowledge/search reaches —
// so it calls the identical searchDoc rather than growing a second retrieval path
// against the same store. Everything org-scoping and tenant-isolating stays in
// index.go, store.go and retrieve.go's present; this file only widens its
// visibility.
//
// The passages live in knowledge's own per-org file, which only the process that
// mounted knowledge holds. Called from any other process, both functions here
// answer that there is no vector leg — mounted_gate_test.go records it.

// Hit is one semantic result. It is the retrieval hit shape verbatim (a type
// alias, not a copy) so the wire contract cannot drift between /v1/knowledge/search and
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
	// Subject is the asking person as the validated principal names them, or ""
	// for the org itself. It reaches the store's filter and nothing else.
	Subject string
}

// Semantic runs the org-scoped vector leg. It returns an error — never a silent
// empty — when the query cannot be embedded or the store cannot be read, so the
// caller can report WHICH backend failed and why (index.go states the contract).
// Each hit is answered from its document as it reads now (present), and its Text
// is its matching passage of that document, which is what a reranker should
// score: the whole of a long page would be cut at the reranker's window, before
// the passage that matched.
func Semantic(ctx context.Context, r SemanticReq) ([]Hit, error) {
	req := searchReq{
		org:      r.Org,
		query:    r.Query,
		limit:    r.Limit,
		project:  r.Project,
		doctypes: sanitizeDocTypes(r.DocTypes),
		subject:  r.Subject,
	}
	hits, err := idx.searchDoc(ctx, req)
	if err != nil {
		return nil, err
	}
	return present(ctx, req, hits)
}

// SemanticReady reports whether the vector leg is here: knowledge is mounted in
// this process with an ai plane client. A deployment without one is DISABLED,
// which the surface reports distinctly from DEGRADED — "never provisioned" and
// "provisioned and broken" are different operational facts and must not share a
// status.
func SemanticReady() bool { return idx.enabled() }
