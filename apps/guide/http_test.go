package guide

import (
	"context"
	"encoding/json"
	"github.com/zap-proto/zip"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestStepViewCarriesJourneyStep pins the stepView projection: every JourneyStep
// field must appear in stepView with the same json tag and type. stepView spells
// the fields out instead of embedding — embedding made the published schema claim
// a nested JourneyStep property the wire (which PROMOTES embedded fields) never
// carries — and this is what keeps the spelled-out copy from silently missing a
// field JourneyStep gains later.
func TestStepViewCarriesJourneyStep(t *testing.T) {
	js := reflect.TypeFor[JourneyStep]()
	sv := reflect.TypeFor[stepView]()
	for f := range js.Fields() {
		g, ok := sv.FieldByName(f.Name)
		if !ok {
			t.Fatalf("stepView is missing JourneyStep field %s", f.Name)
		}
		if g.Type != f.Type || g.Tag.Get("json") != f.Tag.Get("json") {
			t.Fatalf("stepView.%s is (%s, json:%q), want JourneyStep's (%s, json:%q)",
				f.Name, g.Type, g.Tag.Get("json"), f.Type, f.Tag.Get("json"))
		}
	}
}

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

// TestHTTPOverviewDefault: a fresh org gets the seeded base blueprint (the historic
// playbook) with progress and the first available step as next.
func TestHTTPOverviewDefault(t *testing.T) {
	app := newApp(t)
	r := req(t, app, http.MethodGet, "/v1/guide", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("overview want 200, got %d (%s)", r.Code, r.Body)
	}
	v := decode[overviewView](t, r.Body)
	if v.Version != "1" || v.Custom {
		t.Fatalf("fresh org should use the seeded base blueprint, got version=%q custom=%v", v.Version, v.Custom)
	}
	if v.Progress.Total != len(v.Steps) || v.Progress.Done != 0 {
		t.Fatalf("fresh progress want {done:0,total:%d}, got %+v", len(v.Steps), v.Progress)
	}
	if v.Progress.Next != "incorporate" {
		t.Fatalf("next want incorporate, got %q", v.Progress.Next)
	}
	// incorporate is the journey root (available); gsuite is blocked by incorporate.
	byID := map[string]stepView{}
	for _, s := range v.Steps {
		byID[s.ID] = s
	}
	if !byID["incorporate"].Available {
		t.Fatal("incorporate (the journey root) must be available")
	}
	if byID["gsuite"].Available || len(byID["gsuite"].BlockedBy) == 0 {
		t.Fatalf("gsuite must be blocked by incorporate, got %+v", byID["gsuite"])
	}
	if !byID["incorporate"].Automatable {
		t.Fatal("incorporate binds company_form and must be automatable")
	}
}

// TestDocumentPutsAcceptYAML pins the WIRE FACT the two untyped document PUTs
// rest on: their body is a raw YAML-or-JSON document (Parse, sigs.k8s.io/yaml),
// so a YAML body answers 200 and becomes active. A typed In is decoded as JSON
// before the handler sees it, so typing either route flips this test to a 4xx —
// the exact silent wire change the registration-site refusals forbid.
func TestDocumentPutsAcceptYAML(t *testing.T) {
	app := newApp(t)
	org := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u-acme"}

	yaml := []byte("version: yaml-1\nsteps:\n- id: s1\n  title: First\n- id: s2\n  title: Second\n  deps: [s1]\n")
	if r := reqRaw(t, app, http.MethodPut, "/v1/guide/curriculum", org, yaml); r.Code != http.StatusOK {
		t.Fatalf("YAML curriculum PUT want 200, got %d (%s)", r.Code, r.Body)
	}
	v := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if !v.Custom || v.Version != "yaml-1" || len(v.Steps) != 2 {
		t.Fatalf("YAML curriculum not active: custom=%v version=%q steps=%d", v.Custom, v.Version, len(v.Steps))
	}

	bp := []byte("version: yaml-bp\nsteps:\n- id: one\n  title: One\n")
	r := reqRaw(t, app, http.MethodPut, "/v1/guide/blueprint", superHdr, bp)
	if r.Code != http.StatusOK {
		t.Fatalf("YAML blueprint PUT want 200, got %d (%s)", r.Code, r.Body)
	}
	if put := decode[blueprintView](t, r.Body); put.Blueprint.Version != "yaml-bp" {
		t.Fatalf("YAML blueprint not saved, got version %q", put.Blueprint.Version)
	}
}

// TestHTTPTransitionsAndGating: marking a blocked step is 409; the dependency chain
// unlocks as upstream steps complete; skip and reset are ungated.
func TestHTTPTransitionsAndGating(t *testing.T) {
	app := newApp(t)

	// password-manager is blocked by gsuite → 409 with blockedBy.
	r := req(t, app, http.MethodPost, "/v1/guide/steps/password-manager/done", "acme", nil)
	if r.Code != http.StatusConflict {
		t.Fatalf("blocked done want 409, got %d (%s)", r.Code, r.Body)
	}
	var conflict struct {
		BlockedBy []string `json:"blockedBy"`
	}
	_ = json.Unmarshal(r.Body, &conflict)
	if len(conflict.BlockedBy) != 1 || conflict.BlockedBy[0] != "gsuite" {
		t.Fatalf("409 must name the blocker, got %v", conflict.BlockedBy)
	}

	// Walk the chain: incorporate (root) → gsuite unlocks → password-manager unlocks.
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/incorporate/done", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("done incorporate want 200, got %d (%s)", r.Code, r.Body)
	}
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/gsuite/done", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("done gsuite want 200, got %d (%s)", r.Code, r.Body)
	}
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/password-manager/done", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("done password-manager after unlock want 200, got %d (%s)", r.Code, r.Body)
	}

	// Overview now reflects three done.
	v := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if v.Progress.Done != 3 {
		t.Fatalf("done want 3, got %d", v.Progress.Done)
	}

	// Skip is ungated even on a blocked step; reset returns to todo.
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/slack/skip", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("skip want 200, got %d", r.Code)
	}
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/gsuite/reset", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("reset want 200, got %d", r.Code)
	}
	// After reset, gsuite is todo again → password-manager re-blocks.
	v = decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	for _, s := range v.Steps {
		if s.ID == "password-manager" && s.Available {
			t.Fatal("password-manager must re-block after gsuite reset")
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

// TestTypedStepOpsFailClosed pins the ungated pair — the two step transitions that
// are TYPED ops — to the same refusals the untyped gated pair makes. A typed op
// receives only a context, so the org it acts on comes from cloud.Bridge rather
// than the request; without a validated principal there is nothing parked and the
// op must refuse rather than act on an empty tenant. The step id comes from the
// URL, so an id the journey does not contain is 404, not a silent write.
func TestTypedStepOpsFailClosed(t *testing.T) {
	app := newApp(t)
	for _, verb := range []string{"skip", "reset"} {
		if r := req(t, app, http.MethodPost, "/v1/guide/steps/incorporate/"+verb, "", nil); r.Code != http.StatusForbidden {
			t.Fatalf("%s without a principal want 403, got %d (%s)", verb, r.Code, r.Body)
		}
		if r := req(t, app, http.MethodPost, "/v1/guide/steps/ghost/"+verb, "acme", nil); r.Code != http.StatusNotFound {
			t.Fatalf("%s of an unknown step want 404, got %d (%s)", verb, r.Code, r.Body)
		}
	}
	// The URL is the addressing authority: a body naming another step cannot
	// redirect the write, because zip binds the path over the body.
	r := req(t, app, http.MethodPost, "/v1/guide/steps/slack/skip", "acme", map[string]any{"id": "incorporate"})
	if r.Code != http.StatusOK {
		t.Fatalf("skip want 200, got %d (%s)", r.Code, r.Body)
	}
	for _, s := range decode[overviewView](t, r.Body).Steps {
		if s.ID == "incorporate" && s.State != StateTodo {
			t.Fatalf("a body id must never redirect the write: incorporate is %q", s.State)
		}
		if s.ID == "slack" && s.State != StateSkipped {
			t.Fatalf("the URL names the step to skip: slack is %q", s.State)
		}
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
			{"id": "s2", "title": "Second", "deps": []string{"s1"}},
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
		{"id": "a", "title": "A", "deps": []string{"b"}},
		{"id": "b", "title": "B", "deps": []string{"a"}},
	}}
	if r := req(t, app, http.MethodPut, "/v1/guide/curriculum", "acme", bad); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cyclic put want 422, got %d (%s)", r.Code, r.Body)
	}
	v = decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if v.Version != "org-1" {
		t.Fatalf("a rejected PUT must not corrupt the active curriculum, got %q", v.Version)
	}

	// DELETE reverts to the seeded base blueprint.
	if r := req(t, app, http.MethodDelete, "/v1/guide/curriculum", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("delete curriculum want 200, got %d", r.Code)
	}
	v = decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if v.Custom || v.Version != "1" {
		t.Fatalf("delete should revert to the seeded base, got version=%q custom=%v", v.Version, v.Custom)
	}
}

// TestHTTPPerOrgIsolation: one org's progress never bleeds into another's (physical
// per-org store).
func TestHTTPPerOrgIsolation(t *testing.T) {
	app := newApp(t)
	// incorporate is the journey root (no dependencies) — a clean single completion.
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/incorporate/done", "acme", nil); r.Code != http.StatusOK {
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
		return map[string]any{"doc": "Company-1", "tool": tool}, nil
	}
	mounted.State.toolOK = func(string) bool { return true }

	// incorporate is the journey root (no deps) and binds company_form — do it directly.
	r := req(t, app, http.MethodPost, "/v1/guide/steps/incorporate/do", "acme", nil)
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

	// The step is now done and stays done on the next read.
	v := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	for _, s := range v.Steps {
		if s.ID == "incorporate" && s.State != StateDone {
			t.Fatalf("incorporate must be done after do, got %q", s.State)
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

	// do on a blocked step is 409 (slack is gated by gsuite, still todo).
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/slack/do", "acme", nil); r.Code != http.StatusConflict {
		t.Fatalf("do on blocked slack want 409, got %d", r.Code)
	}
}

// TestDoStreamsSSE pins the SECOND wire fact POST /v1/guide/steps/{id}/do stays an
// untyped handler for: asked for a stream, it answers text/event-stream and writes
// the agent's actions as SSE frames as they happen. A typed op answers exactly one
// JSON value, so typing this route would replace the stream with a single body —
// the silent wire change the registration-site refusal forbids. The 409 half of the
// same refusal is pinned above (blocked /do) and in TestHTTPTransitionsAndGating;
// this is the half nothing else covered.
//
// Both triggers are pinned, because wantsSSE accepts either: the Accept header and
// the ?stream=1 alias the browser fetch path uses.
func TestDoStreamsSSE(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		header     map[string]string
	}{
		{"accept-header", "/v1/guide/steps/incorporate/do", map[string]string{"Accept": "text/event-stream"}},
		{"stream-query", "/v1/guide/steps/incorporate/do?stream=1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newApp(t)
			mounted.State.ai = &fakeAI{content: "positioning copy"}
			mounted.State.model = "zen"
			mounted.State.invoke = func(_ context.Context, _, tool string, _ map[string]any) (any, error) {
				return map[string]any{"doc": "Company-1", "tool": tool}, nil
			}
			mounted.State.toolOK = func(string) bool { return true }

			rq := httptest.NewRequest(http.MethodPost, tc.path, nil)
			rq.Header.Set("X-Org-Id", "acme")
			rq.Header.Set("X-User-Id", "u-acme")
			for k, v := range tc.header {
				rq.Header.Set(k, v)
			}
			resp, err := app.Test(rq, zip.TestConfig{Timeout: 0})
			if err != nil {
				t.Fatalf("Test POST %s: %v", tc.path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
				t.Fatalf("stream Content-Type want text/event-stream, got %q", ct)
			}
			body, _ := io.ReadAll(resp.Body)
			// The frames: at least the plan the agent opens with and the end frame
			// carrying the terminal state — the shape a single JSON body cannot have.
			for _, want := range []string{"event: plan\ndata: ", "event: end\ndata: "} {
				if !strings.Contains(string(body), want) {
					t.Fatalf("SSE body missing %q frame, got:\n%s", want, body)
				}
			}
			if !strings.Contains(string(body), `"state":"done"`) {
				t.Fatalf("end frame must carry the terminal state, got:\n%s", body)
			}
		})
	}
}

// TestBlueprintPatchMergeNullVsAbsent pins the WIRE FACT the untyped
// PATCH /v1/guide/blueprint/:collection/:id rests on: its body is a JSON
// merge-patch, so an ABSENT key changes nothing while an EXPLICIT null clears
// the item's field — two different requests with two different effects. A typed
// In cannot tell them apart: encoding/json decodes both `{}` and
// `{"enabled": null}` into a nil *bool, so typing this route would collapse
// "re-enable" and "change nothing" into one answer. This is the same discipline
// TestDocumentPutsAcceptYAML gives the two document PUTs — the refusal cannot
// rot into a stale claim.
func TestBlueprintPatchMergeNullVsAbsent(t *testing.T) {
	app := newApp(t)
	inJourney := func() bool {
		return hasStep(decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body), "gsuite")
	}
	patch := func(raw string) {
		t.Helper()
		if r := reqRaw(t, app, http.MethodPatch, "/v1/guide/blueprint/steps/gsuite", superHdr, []byte(raw)); r.Code != http.StatusOK {
			t.Fatalf("merge-patch %s want 200, got %d (%s)", raw, r.Code, r.Body)
		}
	}

	if !inJourney() {
		t.Fatal("baseline journey must contain gsuite")
	}
	// enabled:false disables the step — it drops from every org's journey.
	patch(`{"enabled": false}`)
	if inJourney() {
		t.Fatal("enabled:false must drop the step from the journey")
	}
	// An ABSENT key merges nothing: the disable stands.
	patch(`{}`)
	if inJourney() {
		t.Fatal("an absent key must leave the disable in place")
	}
	// An EXPLICIT null CLEARS enabled, and a nil enabled reads as ENABLED
	// (absence == on), so the step returns to the journey.
	patch(`{"enabled": null}`)
	if !inJourney() {
		t.Fatal("an explicit null must clear the disable — a nil enabled reads as enabled")
	}
}
