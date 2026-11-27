// Package answer is a researched answer to a hard question, with its sources
// cited.
//
// It is the native answer engine: the bounded plan → search → read → rank →
// synthesize → cite → follow-up loop behind POST /v1/ask when a `mode`
// (search|news|research|deep) selects web grounding. It is a clean-room Hanzo
// implementation (NOT derived from any AGPL reference).
//
// ONE HOME, ONE ENDPOINT. This package is the loop's only home; /v1/ask is its
// only endpoint. `mode` is a VALUE handed to that endpoint — "deep research" is a
// mode, never a second route. The package registers no routes of its own:
// clients/ask owns the endpoint and delegates web modes here.
//
// THE SIX VALUES, one home each:
//
//	plan       → plan()        → []topic              (≤5 topics × 3–5 todos, best-effort)
//	search     → websearch.Search → []websearch.Result (in-process, keyless)
//	rank       → rank()        → []Source             (dedupe URL, host cap, relevance)
//	read       → read()        → []Source (enriched)  (the ONE crawl, apps/crawl)
//	survey     → survey()      → []Source             (search+read applied to a plan, ITERATED)
//	synthesize → synthesize()  → string               (streamed through the Sink)
//
// BOUNDED. The fast modes make ≤3 LLM calls (1 plan + 1 synthesis + 1 follow-up)
// over one gathering pass. A survey adds ONE decision call per extra round (two on
// a round the model answers unreadably), itself bounded by mode.rounds (hard-capped
// at maxRounds), mode.deadline, mode.tokenCeiling, saturation, and the client still
// being connected. It is never an open agent loop.
//
// GROUNDED, AND ONLY GROUNDED. Two properties hold against pages we did not
// author: the synthesis prompt fences every source with a per-request nonce, so a
// crawled page cannot print itself a source number the report then cites; and every
// markdown link in the answer is checked against the gathered set before it reaches
// the client, so a citation always points at a page THIS request fetched. Neither
// is a prompt instruction — a prompt is advice to a model, these are properties of
// the text that leaves the process. See ground.go.
//
// ONE REVENUE DEBIT, NOT ONE DEBIT. Every answer debits the resolved payer once
// through the per-org Meter (Base.Bill): the mode's flat fee, which is the
// product price. That is the only REVENUE charge — but not the only charge. The AI
// plane this engine is handed is itself metered (build.go wraps it in
// meteredAIClient), so each internal completion also debits the payer per token
// against the same balance. Which layer should price /v1/ask is an open decision,
// recorded here rather than claimed away.
package answer
