package guide

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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
	StepID      string `json:"stepId"`
	Title       string `json:"title"`
	Detail      string `json:"detail,omitempty"`
	Rationale   string `json:"rationale"`
	Automatable bool   `json:"automatable"`
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
	Next            string       `json:"next"`
	Suggestions     []suggestion `json:"suggestions"`
	Narrative       string       `json:"narrative,omitempty"`
	Funnel          Funnel       `json:"funnel"`
	Recommendations []string     `json:"recommendations"`
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

// suggest answers GET /v1/guide/suggest: the dynamic next-best quests, grounded in
// the org's real progress + funnel, with a best-effort AI narrative on top.
func suggest(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	cur, states, sugg, funnel, err := buildSuggestions(s, c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
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
	resp.Narrative = narrate(s, c, org, groundingText(cur, states, funnel, sugg))
	return c.JSON(http.StatusOK, resp)
}

// chatRequest is the POST /v1/guide/chat body.
type chatRequest struct {
	Message string `json:"message"`
}

// chatResponse is the POST /v1/guide/chat body: the AI's grounded reply plus the
// same deterministic candidates so the UI can offer a "Do it for me" on the AI-ready
// ones (which call the gated /do endpoint — chat never runs an action itself).
type chatResponse struct {
	Reply       string       `json:"reply"`
	Suggestions []suggestion `json:"suggestions"`
	Funnel      Funnel       `json:"funnel"`
}

// chat answers POST /v1/guide/chat: the founder asks the Business AI about their
// launch journey and gets a grounded reply + the current candidate quests. It reads
// and advises only — it never executes a step (see the security note at the top).
func chat(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var in chatRequest
	if err := c.Bind(&in); err != nil {
		return err
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		return zip.ErrBadRequest("message is required")
	}
	if len(msg) > maxChatMessage {
		msg = msg[:maxChatMessage]
	}
	cur, states, sugg, funnel, err := buildSuggestions(s, c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	prompt := chatPrompt(groundingText(cur, states, funnel, sugg), msg)
	reply := narrate(s, c, org, prompt)
	if strings.TrimSpace(reply) == "" {
		reply = fallbackReply(sugg) // honest deterministic reply when the AI plane is down
	}
	return c.JSON(http.StatusOK, chatResponse{Reply: reply, Suggestions: sugg, Funnel: funnel})
}

// narrate runs ONE grounded AI completion for the caller, billed to the caller's own
// payer (principal.Ledger) and scoped to the caller's own org — so a suggestion/chat
// can never spend another tenant's budget. Returns "" when no AI plane is wired or
// the call errors (the caller falls back to the deterministic output). A free
// function (Go forbids methods on the external cloud.Service) — the ONE
// billing-scoped call site both handlers share.
func narrate(s *cloud.Service[state], c *zip.Ctx, org, prompt string) string {
	if s.State.ai == nil || strings.TrimSpace(prompt) == "" {
		return ""
	}
	payer := principal.Ledger(c)
	res, err := s.State.ai.ChatCompletion(c.Context(), &cloud.ChatRequest{
		Model:      s.State.model,
		Prompt:     prompt,
		Org:        org,
		BillingOrg: payer,
	})
	if err != nil || res == nil {
		return ""
	}
	return clipDraft(res.Content)
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
