package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
)

// catalogAI is a fake AIClient that ALSO implements types.ModelLister, so it
// exercises the create/update-time model validation. Its Models() is the
// gateway's served catalog; ChatCompletion lets a defaulted agent actually run.
type catalogAI struct {
	content string
	ids     []string
}

func (c *catalogAI) ChatCompletion(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{Content: c.content}, nil
}

func (c *catalogAI) Models(_ context.Context) ([]string, error) { return c.ids, nil }

func (c *catalogAI) Embed(_ context.Context, _ *types.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

// TestHTTPCreateModelValidation proves agent-create rejects a model outside the
// gateway's served catalog with a clean 400 (instead of the customer's reported
// run-time 502 for e.g. claude-sonnet-4-5), accepts a catalog model, and — when
// the model is omitted — stores the deployment default so the agent is still
// runnable. Update is guarded identically.
func TestHTTPCreateModelValidation(t *testing.T) {
	const defaultModel = "deepseek-v4-flash"
	ai := &catalogAI{content: "pong", ids: []string{"zen-flash", "deepseek-v4-flash"}}
	app := mountAppModel(t, ai, defaultModel)

	// A model this gateway never serves → a clean 400 at create (was a run-time 502).
	code, body := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "bad", "model": "claude-sonnet-4-5"})
	if code != http.StatusBadRequest {
		t.Fatalf("non-catalog model want 400, got %d (%s)", code, body)
	}

	// A catalog model is accepted.
	if code, body := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "good", "model": "zen-flash"}); code != http.StatusCreated {
		t.Fatalf("catalog model want 201, got %d (%s)", code, body)
	}

	// An OMITTED model falls back to the deployment default, so the agent is
	// created AND runnable — not a 400. This deployment's default is an UPSTREAM
	// name, so what actually lands is cloud.DefaultModel: the brand boundary holds
	// even against an operator who misconfigured CLOUD_AI_DEFAULT_MODEL.
	code, body = do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "defaulted", "instructions": "be terse"})
	if code != http.StatusCreated {
		t.Fatalf("omitted model want 201 (defaulted), got %d (%s)", code, body)
	}
	var created agentView
	_ = json.Unmarshal(body, &created)
	if created.Model != cloud.DefaultModel {
		t.Fatalf("omitted model must store the Hanzo default %q, got %q", cloud.DefaultModel, created.Model)
	}
	// The defaulted agent runs (its stored default model is a real catalog model).
	if code, body := do(t, app, http.MethodPost, "/v1/agents/defaulted/run", "acme",
		map[string]any{"input": "hi"}); code != http.StatusOK {
		t.Fatalf("defaulted agent run want 200, got %d (%s)", code, body)
	}

	// PATCH is guarded identically: a non-catalog model → 400; a catalog model → 200.
	if code, body := do(t, app, http.MethodPatch, "/v1/agents/good", "acme",
		map[string]any{"model": "gpt-9-imaginary"}); code != http.StatusBadRequest {
		t.Fatalf("update to non-catalog model want 400, got %d (%s)", code, body)
	}
	if code, body := do(t, app, http.MethodPatch, "/v1/agents/good", "acme",
		map[string]any{"model": "deepseek-v4-flash"}); code != http.StatusOK {
		t.Fatalf("update to catalog model want 200, got %d (%s)", code, body)
	}
}

// TestCreateModelValidationFailsOpen proves the guard NEVER blocks a create when
// the catalog cannot be enumerated: an AIClient that is not a ModelLister (the
// disabled/RPC clients, and the plain test fakes) skips validation entirely, so a
// create with any model still succeeds. Validation is a UX guard, not a gate.
func TestCreateModelValidationFailsOpen(t *testing.T) {
	// fakeAI (from agents_test.go) implements ChatCompletion only — NOT ModelLister.
	app := mountApp(t, &fakeAI{content: "ok"})
	if code, body := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "any", "model": "some-unlisted-model"}); code != http.StatusCreated {
		t.Fatalf("non-lister AI must skip validation (fail-open), want 201, got %d (%s)", code, body)
	}
}
