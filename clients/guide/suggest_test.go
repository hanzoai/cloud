package guide

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A tiny diamond curriculum for the pure ranking tests:
//
//	a → {b, c} → d      (a unblocks 2; b and c each unblock d only when the other is done)
func diamond() Curriculum {
	return Curriculum{Version: "t", Steps: []Step{
		{ID: "a", Title: "A", Tool: "content_generate"}, // AI-ready
		{ID: "b", Title: "B", Dependencies: []string{"a"}},
		{ID: "c", Title: "C", Dependencies: []string{"a"}},
		{ID: "d", Title: "D", Dependencies: []string{"b", "c"}},
	}}
}

// TestRankSuggestions_LeverageFirst: the highest-leverage available quest ranks
// first (a unblocks b AND c), and terminal/blocked steps are excluded.
func TestRankSuggestions_LeverageFirst(t *testing.T) {
	cur := diamond()
	got := rankSuggestions(cur, map[string]State{})
	if len(got) != 1 || got[0].StepID != "a" {
		t.Fatalf("only a is available; want [a], got %v", ids(got))
	}
	if got[0].Unlocks != 2 {
		t.Fatalf("a must unblock 2 (b and c), got %d", got[0].Unlocks)
	}
	if !got[0].Automatable {
		t.Fatal("a binds content_generate → automatable")
	}
	if !strings.Contains(got[0].Rationale, "nblocks 2 next steps") {
		t.Fatalf("rationale must be grounded in leverage, got %q", got[0].Rationale)
	}

	// After a is done, b and c are both available; neither alone unblocks d (d needs
	// both), so both have Unlocks 0 and tie-break to authoring order (b before c).
	got = rankSuggestions(cur, map[string]State{"a": StateDone})
	if len(got) != 2 || got[0].StepID != "b" || got[1].StepID != "c" {
		t.Fatalf("want [b,c] in authoring order, got %v", ids(got))
	}
	for _, sg := range got {
		if sg.Unlocks != 0 {
			t.Fatalf("neither b nor c solely unblocks d, got unlocks=%d for %s", sg.Unlocks, sg.StepID)
		}
	}

	// With b done, c becomes the sole remaining blocker of d → c unblocks 1.
	got = rankSuggestions(cur, map[string]State{"a": StateDone, "b": StateDone})
	if len(got) != 1 || got[0].StepID != "c" || got[0].Unlocks != 1 {
		t.Fatalf("c must now unblock d (unlocks 1), got %v", got)
	}
}

// TestRankSuggestions_ReconciledStateDropsDone: a step marked done (as auto-detect
// would) drops out of the candidates, so suggest reflects real org state.
func TestRankSuggestions_ReconciledStateDropsDone(t *testing.T) {
	cur := diamond()
	got := rankSuggestions(cur, map[string]State{"a": StateSkipped}) // skipped is terminal
	if hasID(got, "a") {
		t.Fatal("a is terminal (skipped) — must not be suggested")
	}
	if !hasID(got, "b") || !hasID(got, "c") {
		t.Fatalf("b and c must be available after a is resolved, got %v", ids(got))
	}
}

// TestHTTPSuggest: the endpoint returns the ranked candidates + funnel, org-scoped,
// fail-closed without a principal. No AI plane in the test → a nil narrative, but the
// deterministic suggestions always return (honest degrade).
func TestHTTPSuggest(t *testing.T) {
	app := newApp(t)
	if r := req(t, app, http.MethodGet, "/v1/guide/suggest", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("suggest without principal want 403, got %d", r.Code)
	}
	r := req(t, app, http.MethodGet, "/v1/guide/suggest", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("suggest want 200, got %d (%s)", r.Code, r.Body)
	}
	var resp suggestResponse
	if err := json.Unmarshal(r.Body, &resp); err != nil {
		t.Fatalf("decode suggest: %v (%s)", err, r.Body)
	}
	// A fresh org's best next is the journey's first quest, company.
	if resp.Next != "company" {
		t.Fatalf("fresh-org next want company, got %q", resp.Next)
	}
	if len(resp.Suggestions) == 0 || resp.Suggestions[0].StepID != "company" {
		t.Fatalf("company must lead the suggestions, got %v", ids(resp.Suggestions))
	}
	// No analytics warehouse in tests → funnel unavailable + the "instrument" recommendation.
	if resp.Funnel.Available {
		t.Fatal("test has no warehouse → funnel must be unavailable")
	}
	if len(resp.Recommendations) == 0 {
		t.Fatal("an unavailable funnel must still return a grounded recommendation")
	}
}

// TestHTTPChat: the chat endpoint is fail-closed without a principal, requires a
// message, and returns a grounded (deterministic-fallback) reply + candidates. No AI
// plane → the fallback names the top real quest; chat NEVER runs an action.
func TestHTTPChat(t *testing.T) {
	app := newApp(t)
	if r := req(t, app, http.MethodPost, "/v1/guide/chat", "", map[string]any{"message": "hi"}); r.Code != http.StatusForbidden {
		t.Fatalf("chat without principal want 403, got %d", r.Code)
	}
	if r := req(t, app, http.MethodPost, "/v1/guide/chat", "acme", map[string]any{"message": "  "}); r.Code != http.StatusBadRequest {
		t.Fatalf("empty message want 400, got %d (%s)", r.Code, r.Body)
	}
	r := req(t, app, http.MethodPost, "/v1/guide/chat", "acme", map[string]any{"message": "what should I do next?"})
	if r.Code != http.StatusOK {
		t.Fatalf("chat want 200, got %d (%s)", r.Code, r.Body)
	}
	var resp chatResponse
	if err := json.Unmarshal(r.Body, &resp); err != nil {
		t.Fatalf("decode chat: %v (%s)", err, r.Body)
	}
	if strings.TrimSpace(resp.Reply) == "" {
		t.Fatal("chat must always reply (deterministic fallback when the AI plane is down)")
	}
	// The fallback names the top real quest (company).
	if !strings.Contains(resp.Reply, "Form your company") {
		t.Fatalf("fallback reply must name the top real quest, got %q", resp.Reply)
	}
	if len(resp.Suggestions) == 0 || resp.Suggestions[0].StepID != "company" {
		t.Fatalf("chat must surface the candidate quests, got %v", ids(resp.Suggestions))
	}
}

func ids(sg []suggestion) []string {
	out := make([]string, len(sg))
	for i, s := range sg {
		out[i] = s.StepID
	}
	return out
}
func hasID(sg []suggestion, id string) bool {
	for _, s := range sg {
		if s.StepID == id {
			return true
		}
	}
	return false
}
