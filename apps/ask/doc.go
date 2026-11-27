// Package ask is a plain-language question about your business, answered with real numbers.
//
// It is the UNIFIED GROUNDED ADVISOR behind POST /v1/ask. A founder asks a
// plain-language question ("what's my MRR?", "how long is my runway?") and gets
// an answer whose every figure is a REAL value read from a domain endpoint
// in-process — never a number the model invented.
//
// ONE AND ONE WAY. /v1/ask is DISTINCT from /v1/chat/completions (the ai subsystem's RAW model
// completions) and from /v1/agent (the tool-calling orchestrator). Raw model → /v1/chat/completions;
// grounded advisor → /v1/ask. The advisor routes a question to the domain(s) that can ground it,
// reads the REAL figures from each domain's own endpoint in-process, hands the model the EXACT
// figures, and returns the grounded answer + the figures + the domain reads that backed them.
//
// THE FLOW.
//
//	question → registry.Match (which domain grounds this?) → Contributor.Gather (replay the
//	         domain's grounded READ in-process, under the caller's OWN creds) → the REAL facts
//	         → narrate the facts with the model (prose only, never a number) → {answer, figures,
//	           followups, sources, domain}
//
// GROUNDING CONTRACT (non-negotiable). Every figure in the answer is a real domain read; the
// model only NARRATES the figures it is handed and can never override one — the figures array is
// the Contributor's, computed BEFORE any model call and returned unaltered. If no domain can
// ground the question, the advisor says so honestly rather than guessing. Per-tenant isolation is
// inherited from the in-process replay carrying the caller's creds (agent.go's pattern): a
// question can only ever surface the caller's own org's data.
package ask
