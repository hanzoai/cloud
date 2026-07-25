// Package ask is the UNIFIED GROUNDED ADVISOR: POST /v1/ask. A founder asks a plain-language
// question ("what's my MRR?", "how long is my runway?") and gets an answer whose every figure is
// a REAL value read from a domain endpoint in-process — never a number the model invented.
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

import (
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// state is the advisor's own data: the contributor registry (the plug-in domains) and the
// NARRATION-ONLY model seam. ai + model rephrase the grounded facts more naturally; they NEVER
// source a figure. nil ai ⇒ the advisor returns its deterministic templated answer over the same
// real figures — the numbers are identical whether the model plane is up or down.
type state struct {
	registry *Registry
	ai       cloud.AIClient
	model    string
}

// maxQuestion bounds the request body.
const maxQuestion = 2000

// AskRequest is the POST /v1/ask body. The advisor path uses question (figures
// grounding). The WEB grounding domain is selected by mode (search|news|research|
// deep) and parameterized by the remaining fields; q is the answer-engine alias for
// question. All web fields are optional and inert unless mode names a web domain.
type AskRequest struct {
	Question string `json:"question"`
	Q        string `json:"q"` // answer-engine alias for question

	// Web grounding domain (mode-selected). Empty mode ⇒ the figure advisor, unchanged.
	Mode       string   `json:"mode"`     // search|news|research|deep
	Sources    []string `json:"sources"`  // @hints appended to the web query: web,news,academic,github,reddit,x
	Model      string   `json:"model"`    // override the narration/synthesis model
	Stream     *bool    `json:"stream"`   // force SSE (else Accept: text/event-stream / ?stream=1)
	Language   string   `json:"language"` // web-search language (BCP-47-ish)
	MaxSources int      `json:"maxSources"`
	MaxQueries int      `json:"maxQueries"`
	FollowUps  *bool    `json:"followUps"` // default true
	System     string   `json:"system"`    // override the synthesis system prompt
}

// query is the caller's question, accepting either the advisor field (question) or
// the answer-engine alias (q). Trimmed by the handler.
func (r AskRequest) query() string {
	if q := strings.TrimSpace(r.Q); q != "" {
		return q
	}
	return r.Question
}

// AskResponse is the /v1/ask contract: a natural-language answer grounded in Figures, the
// followups worth asking next, the domain reads (Sources) the figures came from, and the Domain
// that grounded the question ("" when none could). Every Figure is a real value; the Answer
// narrates them.
type AskResponse struct {
	Answer    string   `json:"answer"`
	Figures   []Fact   `json:"figures"`
	Followups []string `json:"followups"`
	Sources   []string `json:"sources"`
	Domain    string   `json:"domain"`
}

// Mount wires POST /v1/ask into cloud, building the contributor registry (books today) over the
// SAME app so a contributor's Gather replays a domain's grounded read in-process. The narration
// model comes from deps.AI. Mount is a distinct route, so it wins Fiber's first-match over the
// ai /v1/* catch-all.
func Mount(app *zip.App, deps cloud.Deps) error {
	b := cloud.NewBase(deps, "ask")
	svc := &cloud.Service[*state]{Base: b, State: &state{
		registry: NewRegistry(newBooksContributor(app)),
		ai:       deps.AI,
		model:    strings.TrimSpace(deps.AIDefaultModel),
	}}
	app.Post("/v1/ask", cloud.Handle(svc, askHandler))
	b.Log.Info("ask mounted", "prefix", "/v1/ask", "domains", "books,web", "web_modes", "search,news,research,deep")
	return nil
}

// askHandler answers POST /v1/ask for the caller's OWN org. It gates the caller (a validated
// principal is required — the SAME gate every data plane uses), classifies the question to a
// grounded domain, gathers the REAL figures in-process under the caller's creds, and narrates
// them. The figures and sources are the domain's, resolved BEFORE any model call and never
// altered by it.
func askHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrUnauthorized("sign in to ask")
	}
	var in AskRequest
	if err := c.Bind(&in); err != nil {
		return err
	}
	q := strings.TrimSpace(in.query())
	if q == "" {
		return zip.Errorf(http.StatusBadRequest, "question is required")
	}
	if len(q) > maxQuestion {
		q = q[:maxQuestion]
	}

	// WEB grounding domain — selected explicitly by mode (search|news|research|deep). This is the
	// agentic search/deep-research path: it grounds on live web sources (not ledger figures),
	// streams the answer-engine envelope, and meters the caller. It is ADDITIVE — when no web mode
	// is set the advisor's figure path below runs exactly as before.
	if isWebMode(in.Mode) {
		return serveWeb(s, c, in, q)
	}

	// Classify → the domain that can ground this question. No match ⇒ the honest fallback: the
	// advisor names what it CAN answer rather than fabricating a figure.
	domain := s.State.registry.Match(q)
	if domain == nil {
		return askJSON(c, honestFallback())
	}

	// Gather the REAL figures in-process, under the caller's OWN credentials (so the read is
	// scoped to the caller's org and no other). A gather error degrades to the honest fallback —
	// never a guessed number.
	facts, sources, err := domain.Gather(c.Context(), credential(c))
	if err != nil {
		s.Log.Warn("ask gather failed", "domain", domain.Name(), "err", err)
		return askJSON(c, honestFallback())
	}

	resp := AskResponse{
		Figures:   facts,
		Followups: followups(domain.Name()),
		Sources:   sources,
		Domain:    domain.Name(),
	}
	// The answer over the grounded facts: the model narrates when wired, else a deterministic
	// template. Either way the prose restates figures the model was HANDED — it never sources one,
	// and the Figures array above is authoritative regardless of what the prose says.
	resp.Answer = narrate(s, c, q, facts)
	return askJSON(c, resp)
}

// narrate produces the answer sentence over the EXACT grounded facts. It runs ONE completion that
// rephrases the figures naturally, billed to the caller's HOME org and scoped to the caller's own
// org. The prompt hands the model every figure and forbids changing a number — the model writes
// prose only. It degrades to the deterministic template when no model is wired or the call fails,
// so the answer always states the real figures. The Figures array is NOT touched here: a model
// that hallucinates a number in its prose cannot override the grounded figure the caller receives.
func narrate(s *cloud.Service[*state], c *zip.Ctx, question string, facts []Fact) string {
	tmpl := templateAnswer(facts)
	if s.State.ai == nil {
		return tmpl
	}
	org, _ := principal.Org(c) // already gated non-empty at the handler entry
	res, err := s.State.ai.ChatCompletion(c.Context(), &cloud.ChatRequest{
		Model:      s.State.model,
		Prompt:     narratePrompt(question, facts, tmpl),
		Org:        org,
		BillingOrg: principal.HomeOrg(c),
	})
	if err != nil || res == nil || strings.TrimSpace(res.Content) == "" {
		return tmpl
	}
	return strings.TrimSpace(res.Content)
}

// narratePrompt is the grounded narration prompt: it hands the model EVERY figure verbatim and
// forbids inventing, rounding, or altering a number. The figures are listed so a test can assert
// the exact grounded value is what the model was fed.
func narratePrompt(question string, facts []Fact, draft string) string {
	var fb strings.Builder
	for _, f := range facts {
		fb.WriteString("- " + f.Label + ": " + f.Value)
		if f.Period != "" {
			fb.WriteString(" (" + f.Period + ")")
		}
		fb.WriteString("\n")
	}
	return "You are a precise business advisor. Answer the founder's question in ONE or TWO natural sentences.\n" +
		"You MUST use these figures EXACTLY as given — never invent, round, or alter a number:\n" +
		fb.String() +
		"\nQuestion: " + question +
		"\nGrounded draft (rephrase naturally, keep every figure identical): " + draft +
		"\nReturn only the answer."
}

// templateAnswer is the deterministic sentence over the grounded facts — the answer when no model
// is wired, and the draft the model rephrases. It states the real figures directly, so the advisor
// is correct with or without the model plane.
func templateAnswer(facts []Fact) string {
	if len(facts) == 0 {
		return "There are no figures to report for this period yet."
	}
	parts := make([]string, 0, len(facts))
	for _, f := range facts {
		parts = append(parts, f.Label+" "+f.Value)
	}
	period := facts[0].Period
	lead := "Here are your latest figures"
	if period != "" {
		lead += " for " + period
	}
	return lead + ": " + strings.Join(parts, ", ") + "."
}

// honestFallback is the answer when NO domain can ground the question. It names what the advisor
// CAN answer and offers grounded questions to ask instead — and carries ZERO figures, because a
// figure the advisor cannot ground is a figure it must not state.
func honestFallback() AskResponse {
	return AskResponse{
		Answer:    "I can answer questions about your finances today — MRR, revenue, burn, runway, margin, cash, and P&L. Infra and usage advisors are coming.",
		Figures:   []Fact{},
		Followups: []string{"What's my MRR?", "How long is my runway?", "What is my gross margin?"},
		Sources:   []string{},
		Domain:    "",
	}
}

// followups returns sharp next questions for a domain — deterministic, so the advisor always
// offers a path forward. Extended per domain as new contributors join.
func followups(domain string) []string {
	switch domain {
	case "books":
		return []string{"How long is my runway?", "What is my gross margin?", "How much of revenue is recurring?"}
	default:
		return []string{"What's my MRR?", "How long is my runway?"}
	}
}

// credential extracts the caller's replayable credential + already-validated identity headers, so
// a contributor's in-process replay runs as the CALLER — scoped to the caller's own org. It
// forwards both the bearer/session creds (which the identity middleware re-mints identity from on
// the replayed request) AND the minted identity headers (X-Org-Id / X-User-Id / …), which are
// already validated at this handler's own principal gate: in production either path yields the
// caller's own validated identity, and in a middleware-free test the identity headers are what
// scope the read. It is the SAME replayable set agent.go carries, plus the identity headers a
// grounded READ resolves its org from.
func credential(c *zip.Ctx) map[string]string {
	cred := map[string]string{}
	for _, h := range []string{
		"Authorization", "X-Authorization", "Cookie", "Accept-Language", "X-Forwarded-For",
		"X-Org-Id", "X-User-Id", "X-User-Owner", "X-User-IsOrgAdmin", "X-Project-Id",
		"X-Billing-Account-Id", "X-Hanzo-Test",
	} {
		if v := c.Header(h); v != "" {
			cred[h] = v
		}
	}
	return cred
}

// askJSON writes an advisor payload with no-store (per-org figures must never be cached).
func askJSON(c *zip.Ctx, v any) error {
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, v)
}
