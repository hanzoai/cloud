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

import (
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/answer"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// state is the advisor's own data: the contributor registry (the plug-in domains) and the
// NARRATION-ONLY model client. ai + model rephrase the grounded facts more naturally; they NEVER
// source a figure. nil ai ⇒ the advisor returns its deterministic templated answer over the same
// real figures — the numbers are identical whether the model plane is up or down.
type state struct {
	registry *Registry
	ai       cloud.AIClient
	model    string
}

// maxQuestion bounds the request body.
const maxQuestion = 2000

// askRequest is the POST /v1/ask body. The advisor path uses question (figures
// grounding). The WEB grounding domain is selected by mode (search|news|research|
// deep) and parameterized by the remaining fields; q is the answer-engine alias for
// question. All web fields are optional and inert unless mode names a web domain.
//
// It is named for its product rather than AskRequest, and askAnswer likewise: the
// schema namespace is FLAT across the whole fleet, apps/books ALREADY publishes an
// `AskRequest` and an `AskResponse` of different shapes (its books-grounded ask),
// and openapi.Compose refuses one name with two shapes. This request is declared to
// the document (see the init below), so the collision would have been live the
// moment it was.
type askRequest struct {
	// Question is what to ask, in plain language. Trimmed, and clipped at 2000
	// bytes rather than refused. Required unless q carries it.
	Question string `json:"question"`
	// Q is the answer-engine alias for question, and it WINS when both are sent.
	Q string `json:"q"`

	// Mode selects the WEB grounding domain: search (one fast pass, 6 sources),
	// news (the same, recency-biased), or research (a plan, several rounds, pages
	// actually fetched and read). deep is a retired name for research. Empty — or
	// anything else — is not an error: it takes the FIGURE advisor instead, which
	// answers from the caller's own org data and never touches the web. The rest
	// of the web fields are inert unless this names a mode.
	Mode string `json:"mode"`
	// Sources narrows where the evidence comes from: any of web, news, academic,
	// github, reddit, x. Each becomes a site-scoped web query, so ["x"] searches
	// X/Twitter posts rather than the open web. Unknown tokens are dropped rather
	// than passed through, so caller input cannot pollute the query.
	Sources []string `json:"sources"`
	// Model overrides the synthesis model. It REPLACES the whole chain rather
	// than heading it: the mode's fallbacks are not tried, so a model that is
	// down fails the answer instead of degrading to the next one.
	Model string `json:"model"`
	// Stream forces the answer onto an SSE stream (true) or onto a single JSON
	// body (false). Absent is not false — it hands the decision to
	// `Accept: text/event-stream` or `?stream=1`.
	Stream *bool `json:"stream"`
	// Language narrows the web search to a locale, BCP-47-ish ("en", "ja").
	// Empty means no narrowing.
	Language string `json:"language"`
	// MaxSources caps how many pages the loop gathers. It can only LOWER the
	// mode's own budget (6 for search and news, 32 for research): 0 or a value
	// above the ceiling takes the mode's, so a caller can buy a cheaper answer
	// but never a more expensive one.
	MaxSources int `json:"maxSources"`
	// MaxQueries caps how many web searches the loop runs, clamped the same way
	// against the mode's budget (1 for search and news, 6 for research). It is
	// the other half of what bounds an answer's cost and latency.
	MaxQueries int `json:"maxQueries"`
	// FollowUps asks for the next-questions list (at most five) alongside the
	// answer. Absent means true; false skips the extra completion that generates
	// them. They are best-effort even when asked for — the loop drops them rather
	// than crossing its token ceiling.
	FollowUps *bool `json:"followUps"`
	// System replaces the mode's synthesis prompt for this one answer. Empty
	// keeps the mode's own. It steers how the answer is WRITTEN; it cannot reach
	// the gathering, the ranking or the citation check.
	System string `json:"system"`
}

// query is the caller's question, accepting either the advisor field (question) or
// the answer-engine alias (q). Trimmed by the handler.
func (r askRequest) query() string {
	if q := strings.TrimSpace(r.Q); q != "" {
		return q
	}
	return r.Question
}

// askAnswer is the /v1/ask contract: a natural-language answer grounded in Figures, the
// followups worth asking next, the domain reads (Sources) the figures came from, and the Domain
// that grounded the question ("" when none could). Every Figure is a real value; the Answer
// narrates them.
type askAnswer struct {
	Answer    string   `json:"answer"`
	Figures   []Fact   `json:"figures"`
	Followups []string `json:"followups"`
	Sources   []string `json:"sources"`
	Domain    string   `json:"domain"`
}

// POST /v1/ask is NOT a typed op, and it cannot become one without moving the wire.
// Three independent facts keep it out, each one a wire fact zip's typed path has no
// vocabulary for. TestAskRefusalIsTheWire measures all three.
//
//  1. ONE ROUTE, TWO SUCCESS SHAPES. The advisor branch answers askAnswer
//     ({answer,figures,followups,sources,domain}); the web branch answers a
//     DIFFERENT eight-key object ({answer,sources,follow_ups,followups,figures,
//     domain,mode,model} — apps/answer/answer.go, Serve's non-streaming return). A
//     typed op declares exactly one Out, so one of the two would be reshaped.
//  2. THE WEB BRANCH STREAMS. When the caller asks for SSE (Accept:
//     text/event-stream, ?stream=1, or `"stream": true`) Serve answers through
//     c.SendStreamWriter — a server-sent-event stream, not a JSON value. zip's
//     typed path writes c.JSON(out) for a non-nil Out and stamps cmp.Or(op.Status,
//  204. over a nil one, so there is no Out that means "I already streamed".
//  3. THE MONEY DENIAL IS A DOMAIN BODY. An out-of-funds caller gets
//     cloud.DenyResource — the fleet-wide NESTED {"error":{"code","message"}} at
//     402/503 (apps/answer/answer.go, the Bill.Gate branch). A typed op's only
//     refusal is a RETURNED error, which zip renders as the flat HTTPError
//     {status,code,error}. Same class as the apps/ml creates.
//
// Staying untyped costs the prose, the MCP tool and the CLI command — and it must
// not also cost a document that says this route takes no body, or every generated
// SDK offers an ask with nowhere to put the question. The request declaration below
// is what buys that back. The RESPONSE is deliberately NOT declared: it is the
// polymorphic half above, and a single declared shape would be a false statement
// about the other branch, which is worse than saying nothing.
// The PROSE is declared beside the wire fact, for the same reason and by the same
// rule: this route cannot be a typed op, so zipdoc has no doc comment to lift, and
// without a Describe the document publishes an operationId and nothing else — an SDK
// method that cannot explain itself and a CLI command with no help. Describe is the
// client for exactly the operations the wire refuses to type.
func init() {
	openapi.Register("/v1/ask", http.MethodPost, askRequest{}, nil)
	openapi.Describe("/v1/ask", http.MethodPost,
		"Ask a grounded question about your own org",
		"Answers a natural-language question about the CALLER'S OWN org, from real figures "+
			"rather than from the model's memory.\n\n"+
			"The question is classified to a grounded domain, that domain's read runs IN-PROCESS "+
			"under the caller's own credentials, and only then is the result narrated. So the "+
			"figures and their sources are the domain's, resolved before any model call and never "+
			"altered by one — a wrong answer is a wrong query, never an invention.\n\n"+
			"Domains: books (the org's ledger), projects (what is built and what of it is deployed), "+
			"git (the org's repositories and what changed in them), and web (search, news, research, "+
			"deep). A validated principal is required; the answer is scoped to that principal's org "+
			"and nothing else.")
}

// Mount wires POST /v1/ask into cloud, building the contributor registry from domains() —
// every domain a PEER asked over the internal plane, because this app ships as its own
// process and the domains it grounds in do not run in it. The narration model comes from
// deps.AI. Mount is a distinct route, so it wins Fiber's first-match over the ai /v1/*
// catch-all.
//
// app is not handed to the registry: a contributor reaches its domain by NAME over the
// plane, so there is nothing for it to do with this process's router. Keeping the router
// out of the client is what makes "which process owns that data" stop being the advisor's
// problem.
func Mount(app cloud.Router, deps cloud.Deps) error {
	b := cloud.NewBase(deps, "ask")
	svc := &cloud.Service[*state]{Base: b, State: &state{
		registry: NewRegistry(domains()...),
		ai:       deps.AI,
		model:    cloud.DefaultModel,
	}}
	app.Post("/v1/ask", cloud.Handle(svc, askHandler))
	// The SAME answer engine, at an address an agent can speak. See web.go.
	if err := mountWeb(app, svc.State, b); err != nil {
		return err
	}
	b.Log.Info("ask mounted", "prefix", "/v1/ask", "domains", "books,projects,git,web", "web_modes", "search,news,research,deep")
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
	var in askRequest
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
	// answer engine: it grounds on live web sources (not ledger figures), streams the SearchEvent
	// envelope, and meters the caller. The loop lives in ONE home (clients/answer); /v1/ask is its
	// ONE endpoint. It is ADDITIVE — when no web mode is set the advisor's figure path below runs
	// exactly as before.
	if answer.IsMode(in.Mode) {
		return serveWeb(s, c, in, q)
	}

	// Classify → the domain that can ground this question. No match ⇒ the honest fallback: the
	// advisor names what it CAN answer rather than fabricating a figure.
	domain := s.State.registry.Match(q)
	if domain == nil {
		return askJSON(c, honestFallback())
	}

	// Gather the REAL figures from the domain, AS THIS CALLER — so the read is scoped to the
	// caller's own org and no other. A gather error degrades to the honest fallback — never a
	// guessed number.
	//
	// cloud.As(c, "") and not c.Context(), and the difference is the whole tenancy story on
	// this path. This is an UNTYPED handler: zip attaches the in-flight request to the context
	// it hands a TYPED op, not to this one, so c.Context() answers "nobody is calling" and a
	// domain that correctly refuses an anonymous read would refuse every question ever asked.
	// As carries THIS request's principal — read off the headers the edge already validated,
	// which is also what the gate above just checked — onto a context the peer can read it
	// from. The empty org argument is "keep the caller's own tenant": there is no widening
	// here, and no place for one, because a question is only ever asked about the asker.
	facts, sources, err := domain.Gather(cloud.As(c, ""), credential(c))
	if err != nil {
		s.Log.Warn("ask gather failed", "domain", domain.Name(), "err", err)
		return askJSON(c, honestFallback())
	}

	resp := askAnswer{
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

// serveWeb hands a web-mode question to the answer engine. It is pure delegation:
// the endpoint translates its own body into the engine's Request and lends it the
// Base (logger + the ONE per-org meter) and the AI plane. No loop logic lives
// here — clients/answer is its one home.
func serveWeb(s *cloud.Service[*state], c *zip.Ctx, in askRequest, q string) error {
	e := answer.Engine{Base: s.Base, AI: s.State.ai, Model: s.State.model}
	return e.Serve(c, answer.Request{
		Mode:       in.Mode,
		Sources:    in.Sources,
		Model:      in.Model,
		Stream:     in.Stream,
		Language:   in.Language,
		MaxSources: in.MaxSources,
		MaxQueries: in.MaxQueries,
		FollowUps:  in.FollowUps,
		System:     in.System,
	}, q)
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
		BillingOrg: principal.Ledger(c),
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
func honestFallback() askAnswer {
	return askAnswer{
		Answer: "I can answer questions about your finances (MRR, revenue, burn, runway, margin, cash, P&L), " +
			"your projects and what of them is deployed, and your repositories and what changed in them.",
		Figures:   []Fact{},
		Followups: []string{"What's my MRR?", "What have I deployed?", "How many repositories do I have?"},
		Sources:   []string{},
		Domain:    "",
	}
}

// followups returns sharp next questions for a domain — deterministic, so the advisor always
// offers a path forward. One case per contributor; the default is the cross-domain menu, which
// is also what a caller sees when no domain matched.
func followups(domain string) []string {
	switch domain {
	case "books":
		return []string{"How long is my runway?", "What is my gross margin?", "How much of revenue is recurring?"}
	case "projects":
		return []string{"Which projects are live?", "What did I deploy most recently?", "How many repositories do I have?"}
	case "git":
		return []string{"Which repositories changed recently?", "What have I deployed?", "How much code do I have?"}
	default:
		return []string{"What's my MRR?", "What have I deployed?", "How many repositories do I have?"}
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
