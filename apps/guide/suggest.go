package guide

import (
	"github.com/hanzoai/cloud/internal/shorten"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// suggest.go is the Business AI's DYNAMIC "what to do next". Instead of only the
// static linear next step, it composes three things the guide already has —
//
//   - the RECONCILED state (snapshotFor runs the detect.go signals, so an
//     auto-detected step is already terminal and drops out of the candidates),
//   - the ANALYTICS funnel + its GTM recommendations (gtm.go), and
//   - a deterministic LEVERAGE ranking of the available quests,
//
// then lets the embedded AI narrate it (best-effort, grounded ONLY in those real
// quests + numbers). Two read-only surfaces:
//
//	GET  /v1/guide/suggest        the ranked next-best quests + a grounded narrative
//	POST /v1/guide/chat  {message} the founder chats with the Business AI about the journey
//
// SECURITY BOUNDARY: suggest and chat NEVER run an action. They read the caller's
// own org state, advise, and surface which quests are AI-ready — the ONLY path that
// actually executes a step is POST /v1/guide/steps/:id/do, which is dependency-gated,
// audited, and metered. So the chat/suggest agent cannot be made to run an action
// (cross-tenant or otherwise) or to spend anything beyond one grounded completion
// billed to the caller's own payer. The user message is length-bounded.

// maxChatMessage bounds a chat message so a caller cannot amplify the AI prompt.
const maxChatMessage = 4 * 1024

// suggestion is one ranked next-best quest the Business AI recommends.
type suggestion struct {
	// StepID is the checklist step being recommended — the id every step route
	// takes, so a caller can act on the suggestion directly.
	StepID string `json:"stepId"`
	// Title is the step's own one-line quest.
	Title string `json:"title"`
	// Detail is the step's own prose — what it asks for.
	Detail string `json:"detail,omitempty"`
	// Rationale is why this step is being suggested NOW, written for the person
	// reading it. It explains the ranking, not the step.
	Rationale string `json:"rationale"`
	// Automatable is true when the step names a tool, so the Business AI can do it
	// rather than only describe it.
	Automatable bool `json:"automatable"`
	// Unlocks is how many downstream steps completing this one immediately makes
	// available (its leverage) — the primary ranking key.
	Unlocks int `json:"unlocks"`
}

// rankSuggestions returns the available, non-terminal steps ranked best-first: the
// quests that unblock the most downstream work come first (highest leverage), then
// authoring order as the stable tiebreak. It is PURE over the curriculum + the
// reconciled states (the caller folds the detect signals in first), so it is
// exhaustively unit-testable and can never run an effect.
func rankSuggestions(cur Curriculum, states map[string]State) []suggestion {
	out := make([]suggestion, 0, len(cur.Steps))
	order := make(map[string]int, len(cur.Steps))
	for i, s := range cur.Steps {
		order[s.ID] = i
	}
	for _, s := range cur.Steps {
		if terminal(stateOf(states, s.ID)) || !cur.Available(states, s.ID) {
			continue
		}
		sg := suggestion{
			StepID:      s.ID,
			Title:       s.Title,
			Detail:      s.Detail,
			Automatable: strings.TrimSpace(s.Tool) != "",
			Unlocks:     cur.unlocks(states, s.ID),
		}
		sg.Rationale = rationaleFor(sg)
		out = append(out, sg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Unlocks != out[j].Unlocks {
			return out[i].Unlocks > out[j].Unlocks
		}
		return order[out[i].StepID] < order[out[j].StepID]
	})
	return out
}

// unlocks counts the non-terminal steps that would become AVAILABLE the moment id is
// completed: a dependent whose ONLY remaining blocker is id. That is the honest
// "leverage" of a quest — completing it immediately opens that many next quests.
func (c Curriculum) unlocks(states map[string]State, id string) int {
	n := 0
	for _, s := range c.Steps {
		if terminal(stateOf(states, s.ID)) {
			continue
		}
		blocked := c.BlockedBy(states, s.ID)
		if len(blocked) == 1 && blocked[0] == id {
			n++
		}
	}
	return n
}

// rationaleFor is the grounded, templated reason a quest is a good next move — no
// fabrication, derived from its real leverage + whether the Business AI can run it.
func rationaleFor(s suggestion) string {
	var parts []string
	if s.Unlocks == 1 {
		parts = append(parts, "unblocks 1 next step")
	} else if s.Unlocks > 1 {
		parts = append(parts, fmt.Sprintf("unblocks %d next steps", s.Unlocks))
	}
	if s.Automatable {
		parts = append(parts, "the Business AI can do it for you")
	}
	if len(parts) == 0 {
		return "Ready to start now."
	}
	joined := strings.Join(parts, "; ")
	return strings.ToUpper(joined[:1]) + joined[1:] + "."
}

// suggestResponse is the /v1/guide/suggest body.
type suggestResponse struct {
	// Next is the id of the single next step the static journey names — the
	// linear answer the ranked Suggestions refine.
	Next string `json:"next"`
	// Suggestions are the available, non-terminal quests ranked best-first by how
	// much downstream work each unblocks.
	Suggestions []suggestion `json:"suggestions"`
	// Narrative is the AI's grounded prose over those quests and numbers. Absent
	// when no AI plane is wired or the completion failed — never fabricated.
	Narrative string `json:"narrative,omitempty"`
	// Funnel is the org's trailing-window traffic → signups → orders.
	Funnel Funnel `json:"funnel"`
	// Recommendations are the next-best GTM actions derived from that funnel.
	Recommendations []string `json:"recommendations"`
}

// buildSuggestions loads the caller's reconciled snapshot + funnel and computes the
// deterministic ranked candidates. It is the shared grounding both suggest and chat
// reason over (DRY).
func buildSuggestions(s *cloud.Service[state], ctx context.Context, org string) (Curriculum, map[string]State, []suggestion, Funnel, error) {
	_, cur, _, rows, err := snapshotFor(s, ctx, org)
	if err != nil {
		return Curriculum{}, nil, nil, Funnel{}, err
	}
	states := stateMap(rows)
	funnel := analyticsFunnel(ctx, org)
	return cur, states, rankSuggestions(cur, states), funnel, nil
}

// Suggest returns the caller org's next-best quests: the available, non-terminal
// steps of its journey ranked by how much downstream work each unblocks, each with
// the grounded reason it is a good next move and whether the Business AI can run
// it, plus the org's funnel and the GTM recommendations derived from it. A
// best-effort AI narrative over exactly those quests and numbers is included when
// an AI plane is wired. READ-ONLY: it advises and never runs a step — the only
// executing path is POST /v1/guide/steps/{id}/do.
func (o ops) suggest(ctx context.Context, _ *cloud.Unit) (*suggestResponse, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	cur, states, sugg, funnel, err := buildSuggestions(o.s, ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	resp := suggestResponse{
		Next:            cur.Next(states),
		Suggestions:     sugg,
		Funnel:          funnel,
		Recommendations: funnel.recommend(),
	}
	// Best-effort AI narrative — billed to the CALLER's own payer, grounded only in
	// the quests + numbers above. A missing/erroring AI plane just omits the prose;
	// the deterministic suggestions + recommendations always return.
	resp.Narrative = narrate(o.s, ctx, org, groundingText(cur, states, funnel, sugg))
	return &resp, nil
}

// chatRequest is the POST /v1/guide/chat body.
type chatRequest struct {
	// Message is the founder's question for the Business AI. Required; trimmed,
	// and clipped to 4 KiB so a caller cannot amplify the AI prompt.
	Message string `json:"message"`
}

// chatResponse is the POST /v1/guide/chat body: the AI's grounded reply plus the
// same deterministic candidates so the UI can offer a "Do it for me" on the AI-ready
// ones (which call the gated /do endpoint — chat never runs an action itself).
type chatResponse struct {
	// Reply is the coach's answer, grounded only in the quests and funnel below.
	// When no AI plane is reachable it is the deterministic reply naming the top
	// real quest — never silence, never invention.
	Reply string `json:"reply"`
	// Suggestions are the current candidate quests, ranked best-first.
	Suggestions []suggestion `json:"suggestions"`
	// Funnel is the org's trailing-window traffic → signups → orders.
	Funnel Funnel `json:"funnel"`
}

// Chat answers a founder's question about their launch journey as the Business AI
// coach: it grounds the reply in the org's REAL progress, its ranked available
// quests and its analytics funnel, and returns those candidate quests alongside so
// the caller can act on one. READ-ONLY — it advises and never runs a step, so it
// cannot be talked into performing an action; the only executing path is POST
// /v1/guide/steps/{id}/do. One AI completion per call, billed to the caller's own
// payer.
//
// Example: {"message": "what should I do next to get my first customers?"}
func (o ops) chat(ctx context.Context, in *chatRequest) (*chatResponse, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		return nil, zip.ErrBadRequest("message is required")
	}
	if len(msg) > maxChatMessage {
		msg = msg[:maxChatMessage]
	}
	cur, states, sugg, funnel, err := buildSuggestions(o.s, ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	prompt := chatPrompt(groundingText(cur, states, funnel, sugg), msg)
	reply := narrate(o.s, ctx, org, prompt)
	if strings.TrimSpace(reply) == "" {
		reply = fallbackReply(sugg) // honest deterministic reply when the AI plane is down
	}
	return &chatResponse{Reply: reply, Suggestions: sugg, Funnel: funnel}, nil
}

// narrate runs ONE grounded AI completion for the caller, billed to the caller's own
// payer (ledgerOf) and scoped to the caller's own org — so a suggestion/chat can
// never spend another tenant's budget. Returns "" when no AI plane is wired or the
// call errors (the caller falls back to the deterministic output). A free function
// (Go forbids methods on the external cloud.Service) — the ONE billing-scoped call
// site both ops share, and therefore the ONE place the payer is resolved.
func narrate(s *cloud.Service[state], ctx context.Context, org, prompt string) string {
	if s.State.ai == nil || strings.TrimSpace(prompt) == "" {
		return ""
	}
	res, err := s.State.ai.ChatCompletion(ctx, &cloud.ChatRequest{
		Model:      s.State.model,
		Prompt:     prompt,
		Org:        org,
		BillingOrg: ledgerOf(ctx),
	})
	if err != nil || res == nil {
		return ""
	}
	return shorten.Trim(res.Content, maxDraftOutput)
}

// groundingText renders the compact, grounded state the AI reasons over: progress,
// the top candidate quests (id · title · why · AI-ready), and the funnel + its GTM
// recommendations. Pure + bounded, so the prompt is testable and can't be amplified.
func groundingText(cur Curriculum, states map[string]State, funnel Funnel, sugg []suggestion) string {
	var b strings.Builder
	done, total, pct := cur.Counts(states)
	fmt.Fprintf(&b, "Launch progress: %d of %d quests complete (%d%%).\n", done, total, pct)
	b.WriteString("The available next quests, best-first:\n")
	top := sugg
	if len(top) > maxSuggestForPrompt {
		top = top[:maxSuggestForPrompt]
	}
	if len(top) == 0 {
		b.WriteString("- (none — the journey is complete)\n")
	}
	for _, sg := range top {
		ai := ""
		if sg.Automatable {
			ai = " [the Business AI can do this]"
		}
		fmt.Fprintf(&b, "- %s: %s (%s)%s\n", sg.StepID, sg.Title, sg.Rationale, ai)
	}
	if funnel.Available {
		fmt.Fprintf(&b, "Funnel (last %d days): %d visitors, %d signups, %d orders.\n",
			funnel.WindowDays, funnel.Visitors, funnel.Signups, funnel.Orders)
	} else {
		b.WriteString("Funnel: no analytics yet.\n")
	}
	for _, r := range funnel.recommend() {
		fmt.Fprintf(&b, "Data note: %s\n", r)
	}
	return b.String()
}

// maxSuggestForPrompt bounds how many candidate quests the AI prompt enumerates.
const maxSuggestForPrompt = 6

// chatPrompt frames the founder's message against the grounded state as a launch
// coach who recommends only from the real quests and never invents products/steps.
func chatPrompt(grounding, message string) string {
	return "You are the Hanzo Business AI, a launch coach for a founder building an " +
		"agentic company. Here is their current state:\n\n" + grounding +
		"\nThe founder asks: \"" + message + "\"\n\n" +
		"Answer concisely as their coach, grounded ONLY in the quests and data above. " +
		"If they should tackle a specific quest, name it and say whether you can do it " +
		"for them. Never invent a product, feature, or step that is not listed above."
}

// fallbackReply is the honest deterministic reply used when the AI plane is
// unavailable — it names the top real quest and its grounded rationale.
func fallbackReply(sugg []suggestion) string {
	if len(sugg) == 0 {
		return "You've completed every quest — your agentic company is launched. Revisit any quest to keep iterating."
	}
	top := sugg[0]
	msg := "I'd tackle \"" + top.Title + "\" next — " + strings.ToLower(top.Rationale[:1]) + top.Rationale[1:]
	if top.Automatable {
		msg += " Say the word and I'll do it for you."
	}
	return msg
}
