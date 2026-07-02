package agents

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestCreateRejectsOversizedRefs: computeRef/serviceAccountId are opaque ids,
// bounded at the boundary — a multi-KB "id" must be a 400, not persisted.
func TestCreateRejectsOversizedRefs(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	huge := strings.Repeat("a", maxRef+1)
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "big", "model": "m", "computeRef": huge}); code != http.StatusBadRequest {
		t.Fatalf("oversized computeRef want 400, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "big2", "model": "m", "serviceAccountId": huge}); code != http.StatusBadRequest {
		t.Fatalf("oversized serviceAccountId want 400, got %d", code)
	}
}

// TestCreateLongRunningRequiresValidCron: a long-running agent must carry a
// parseable cron; missing/invalid schedule is a 400. A valid one is 201 and the
// mode+schedule round-trip in the view.
func TestCreateLongRunningValidation(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})

	// long-running without a schedule -> 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "m", "executionMode": "long-running"}); code != http.StatusBadRequest {
		t.Fatalf("long-running w/o schedule want 400, got %d", code)
	}
	// long-running with a bad cron -> 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "b", "model": "m", "executionMode": "long-running", "schedule": "not a cron"}); code != http.StatusBadRequest {
		t.Fatalf("long-running w/ bad cron want 400, got %d", code)
	}
	// unknown mode -> 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "c", "model": "m", "executionMode": "daemon"}); code != http.StatusBadRequest {
		t.Fatalf("unknown mode want 400, got %d", code)
	}
	// valid long-running -> 201, fields echoed.
	code, body := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "cron", "model": "m", "executionMode": "long-running",
			"schedule": "*/5 * * * *", "computeRef": "vm-1", "serviceAccountId": "acme-cron"})
	if code != http.StatusCreated {
		t.Fatalf("valid long-running want 201, got %d (%s)", code, body)
	}
	var v agentView
	_ = json.Unmarshal(body, &v)
	if v.ExecutionMode != "long-running" || v.Schedule != "*/5 * * * *" ||
		v.ComputeRef != "vm-1" || v.ServiceAccountID != "acme-cron" {
		t.Fatalf("lifecycle fields not echoed in view: %+v", v)
	}
}

// TestCreateOneShotDropsSchedule: a one-shot agent's schedule is meaningless and
// dropped, so the view carries no schedule and the scheduler will never pick it.
func TestCreateOneShotDropsSchedule(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	code, body := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "one", "model": "m", "schedule": "* * * * *"})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	var v agentView
	_ = json.Unmarshal(body, &v)
	if v.ExecutionMode != "one-shot" || v.Schedule != "" {
		t.Fatalf("one-shot must drop schedule, got mode=%q schedule=%q", v.ExecutionMode, v.Schedule)
	}
}

// TestPatchToLongRunningValidates: PATCHing an agent to long-running without a
// schedule is rejected; supplying a valid schedule in the same PATCH succeeds.
func TestPatchToLongRunningValidates(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	do(t, app, http.MethodPost, "/v1/agents", "acme", map[string]any{"name": "a", "model": "m"})

	// flip to long-running with no schedule -> 400.
	if code, _ := do(t, app, http.MethodPatch, "/v1/agents/a", "acme",
		map[string]any{"executionMode": "long-running"}); code != http.StatusBadRequest {
		t.Fatalf("patch to long-running w/o schedule want 400, got %d", code)
	}
	// flip with a schedule -> 200.
	code, body := do(t, app, http.MethodPatch, "/v1/agents/a", "acme",
		map[string]any{"executionMode": "long-running", "schedule": "0 * * * *"})
	if code != http.StatusOK {
		t.Fatalf("patch to long-running w/ schedule want 200, got %d (%s)", code, body)
	}
	var v agentView
	_ = json.Unmarshal(body, &v)
	if v.ExecutionMode != "long-running" || v.Schedule != "0 * * * *" {
		t.Fatalf("patch did not apply lifecycle: %+v", v)
	}
}
