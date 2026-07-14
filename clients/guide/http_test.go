package guide

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestHTTPGateRequiresPrincipal: every data route refuses a request with no
// validated principal (403) — a forged X-Org-Id with no bearer never reaches org
// data.
func TestHTTPGateRequiresPrincipal(t *testing.T) {
	app := newApp(t)
	for _, path := range []string{"/v1/guide", "/v1/guide/curriculum", "/v1/guide/actions"} {
		if r := req(t, app, http.MethodGet, path, "", nil); r.Code != http.StatusForbidden {
			t.Fatalf("GET %s without principal want 403, got %d (%s)", path, r.Code, r.Body)
		}
	}
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/positioning/done", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("POST done without principal want 403, got %d", r.Code)
	}
}

// TestHTTPOverviewDefault: a fresh org gets the built-in default with progress and
// the first available step as next.
func TestHTTPOverviewDefault(t *testing.T) {
	app := newApp(t)
	r := req(t, app, http.MethodGet, "/v1/guide", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("overview want 200, got %d (%s)", r.Code, r.Body)
	}
	v := decode[overviewView](t, r.Body)
	if v.Version != "builtin-1" || v.Custom {
		t.Fatalf("fresh org should use builtin default, got version=%q custom=%v", v.Version, v.Custom)
	}
	if v.Progress.Total != len(v.Steps) || v.Progress.Done != 0 {
		t.Fatalf("fresh progress want {done:0,total:%d}, got %+v", len(v.Steps), v.Progress)
	}
	if v.Progress.Next != "positioning" {
		t.Fatalf("next want positioning, got %q", v.Progress.Next)
	}
	// positioning available; landing blocked by positioning.
	byID := map[string]stepView{}
	for _, s := range v.Steps {
		byID[s.ID] = s
	}
	if !byID["positioning"].Available {
		t.Fatal("positioning must be available")
	}
	if byID["landing"].Available || len(byID["landing"].BlockedBy) == 0 {
		t.Fatalf("landing must be blocked by positioning, got %+v", byID["landing"])
	}
	if !byID["positioning"].Automatable {
		t.Fatal("positioning binds content_generate and must be automatable")
	}
}

// TestHTTPTransitionsAndGating: marking a blocked step is 409; the dependency chain
// unlocks as upstream steps complete; skip and reset are ungated.
func TestHTTPTransitionsAndGating(t *testing.T) {
	app := newApp(t)

	// landing is blocked by positioning → 409 with blockedBy.
	r := req(t, app, http.MethodPost, "/v1/guide/steps/landing/done", "acme", nil)
	if r.Code != http.StatusConflict {
		t.Fatalf("blocked done want 409, got %d (%s)", r.Code, r.Body)
	}
	var conflict struct {
		BlockedBy []string `json:"blockedBy"`
	}
	_ = json.Unmarshal(r.Body, &conflict)
	if len(conflict.BlockedBy) != 1 || conflict.BlockedBy[0] != "positioning" {
		t.Fatalf("409 must name the blocker, got %v", conflict.BlockedBy)
	}

	// Complete positioning → landing unlocks.
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/positioning/done", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("done positioning want 200, got %d (%s)", r.Code, r.Body)
	}
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/landing/done", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("done landing after unlock want 200, got %d (%s)", r.Code, r.Body)
	}

	// Overview now reflects two done and moved next forward.
	v := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if v.Progress.Done != 2 {
		t.Fatalf("done want 2, got %d", v.Progress.Done)
	}

	// Skip is ungated even on a blocked step; reset returns to todo.
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/referral/skip", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("skip want 200, got %d", r.Code)
	}
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/positioning/reset", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("reset want 200, got %d", r.Code)
	}
	// After reset, positioning is todo again → landing re-blocks.
	v = decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	for _, s := range v.Steps {
		if s.ID == "landing" && s.Available {
			t.Fatal("landing must re-block after positioning reset")
		}
	}
}

// TestHTTPUnknownStep: acting on a step id not in the curriculum is 404.
func TestHTTPUnknownStep(t *testing.T) {
	app := newApp(t)
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/ghost/done", "acme", nil); r.Code != http.StatusNotFound {
		t.Fatalf("unknown step want 404, got %d (%s)", r.Code, r.Body)
	}
}

// TestHTTPCustomCurriculumReplaceAndRevert: a PUT replaces the curriculum cleanly;
// DELETE reverts to the built-in default. A malformed PUT is rejected (422) and does
// not corrupt the active curriculum.
func TestHTTPCustomCurriculumReplaceAndRevert(t *testing.T) {
	app := newApp(t)

	custom := map[string]any{
		"version": "org-1",
		"steps": []map[string]any{
			{"id": "s1", "title": "First"},
			{"id": "s2", "title": "Second", "dependencies": []string{"s1"}},
		},
	}
	if r := req(t, app, http.MethodPut, "/v1/guide/curriculum", "acme", custom); r.Code != http.StatusOK {
		t.Fatalf("put curriculum want 200, got %d (%s)", r.Code, r.Body)
	}
	v := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if !v.Custom || v.Version != "org-1" || len(v.Steps) != 2 {
		t.Fatalf("custom curriculum not active: %+v", v)
	}

	// Malformed PUT (cycle) → 422, active curriculum unchanged.
	bad := map[string]any{"version": "x", "steps": []map[string]any{
		{"id": "a", "title": "A", "dependencies": []string{"b"}},
		{"id": "b", "title": "B", "dependencies": []string{"a"}},
	}}
	if r := req(t, app, http.MethodPut, "/v1/guide/curriculum", "acme", bad); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cyclic put want 422, got %d (%s)", r.Code, r.Body)
	}
	v = decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if v.Version != "org-1" {
		t.Fatalf("a rejected PUT must not corrupt the active curriculum, got %q", v.Version)
	}

	// DELETE reverts to default.
	if r := req(t, app, http.MethodDelete, "/v1/guide/curriculum", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("delete curriculum want 200, got %d", r.Code)
	}
	v = decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if v.Custom || v.Version != "builtin-1" {
		t.Fatalf("delete should revert to builtin default, got version=%q custom=%v", v.Version, v.Custom)
	}
}

// TestHTTPPerOrgIsolation: one org's progress never bleeds into another's (physical
// per-org store).
func TestHTTPPerOrgIsolation(t *testing.T) {
	app := newApp(t)
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/positioning/done", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("acme done: %d", r.Code)
	}
	acme := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	other := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "maxpower", nil).Body)
	if acme.Progress.Done != 1 {
		t.Fatalf("acme should have 1 done, got %d", acme.Progress.Done)
	}
	if other.Progress.Done != 0 {
		t.Fatalf("maxpower must see 0 done (isolation), got %d", other.Progress.Done)
	}
}

// TestHTTPDoStepDelegatesToAgent: "do it for me" runs the Business AI through an
// injected invoke seam AS THE CALLER'S ORG, records the action, and auto-completes
// the step — the acted signal then keeps it done.
func TestHTTPDoStepDelegatesToAgent(t *testing.T) {
	app := newApp(t)
	// Inject a fake AI + MCP invoke onto the mounted service (no live plane in tests).
	var gotOrg string
	mounted.State.ai = &fakeAI{content: "positioning copy"}
	mounted.State.model = "zen"
	mounted.State.invoke = func(_ context.Context, org, tool string, args map[string]any) (any, error) {
		gotOrg = org
		return map[string]any{"doc": "Positioning-1", "tool": tool}, nil
	}
	mounted.State.toolOK = func(string) bool { return true }

	r := req(t, app, http.MethodPost, "/v1/guide/steps/positioning/do", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("do want 200, got %d (%s)", r.Code, r.Body)
	}
	var resp struct {
		State  string  `json:"state"`
		Events []event `json:"events"`
	}
	if err := json.Unmarshal(r.Body, &resp); err != nil {
		t.Fatalf("decode do: %v (%s)", err, r.Body)
	}
	if resp.State != string(StateDone) {
		t.Fatalf("do should complete the step, got state %q", resp.State)
	}
	if gotOrg != "acme" {
		t.Fatalf("agent must act as the caller's org, got %q", gotOrg)
	}

	// The step is now done, and its acted signal keeps it done on the next read.
	v := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	for _, s := range v.Steps {
		if s.ID == "positioning" && s.State != StateDone {
			t.Fatalf("positioning must be done after do, got %q", s.State)
		}
	}
	// The action is on the ledger.
	var actions struct {
		Data []ActionRecord `json:"data"`
	}
	_ = json.Unmarshal(req(t, app, http.MethodGet, "/v1/guide/actions", "acme", nil).Body, &actions)
	if len(actions.Data) != 1 || !actions.Data[0].OK {
		t.Fatalf("expected one successful action on the ledger, got %+v", actions.Data)
	}

	// do on a blocked step is 409.
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/launch/do", "acme", nil); r.Code != http.StatusConflict {
		t.Fatalf("do on blocked launch want 409, got %d", r.Code)
	}
}
