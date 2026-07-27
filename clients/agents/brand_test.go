package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
)

// brand_test.go is the guard that stops the leak recurring.
//
// Hanzo serves the enso and zen families under its own names. An upstream family
// name reaching a customer publishes which base sits behind a Hanzo model, so it
// is a defect wherever it appears — an API payload, a UI string, a model list, a
// log a customer can read.
//
// The tests below drive the agents registry over real HTTP with the WORST-CASE
// inputs (an operator who configured an upstream default, a caller who POSTs an
// upstream model, a row already in the database) and assert that no upstream
// family name comes back out of ANY of it.

// scanUpstream fails the test if any upstream family name appears anywhere in
// body. It scans the raw bytes rather than a decoded field, so a name leaking
// through a message, a nested object or a field nobody thought to check is
// caught just the same.
func scanUpstream(t *testing.T, what string, body []byte) {
	t.Helper()
	for _, family := range []string{"deepseek", "qwen", "glm-", "kimi", "minimax"} {
		if bytes.Contains(bytes.ToLower(body), []byte(family)) {
			t.Errorf("BRAND LEAK: %s response contains upstream family %q\n%s", what, family, body)
		}
	}
}

// TestNoUpstreamNameOnTheWire is the regression guard. It walks every
// customer-visible read of the agents registry and scans the raw response.
func TestNoUpstreamNameOnTheWire(t *testing.T) {
	// The adversarial deployment: an operator who set CLOUD_AI_DEFAULT_MODEL to an
	// upstream name, and a gateway whose catalog serves upstream names — exactly
	// the configuration that produced the live leak.
	ai := &catalogAI{content: "pong", ids: []string{"enso", "enso-flash", "deepseek-v4-flash", "glm-5.2"}}
	app := mountAppModel(t, ai, "deepseek-v4-flash")

	// 1. An agent created with NO model. The configured default is an upstream
	//    name; normalization must still store and answer the Hanzo name.
	code, body := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "defaulted", "instructions": "be terse"})
	if code != http.StatusCreated {
		t.Fatalf("create defaulted: want 201, got %d (%s)", code, body)
	}
	scanUpstream(t, "POST /v1/agents (defaulted)", body)
	var created agentView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Model != cloud.DefaultModel {
		t.Fatalf("defaulted agent model = %q, want %q", created.Model, cloud.DefaultModel)
	}

	// 2. A caller who explicitly POSTs an upstream model the gateway really does
	//    serve. It is accepted (not a 400 — the name was ours to leak, not theirs
	//    to be punished for) but normalized, so it never enters the registry.
	code, body = do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "pinned", "model": "deepseek-v4-flash"})
	if code != http.StatusCreated {
		t.Fatalf("create pinned: want 201, got %d (%s)", code, body)
	}
	scanUpstream(t, "POST /v1/agents (explicit upstream model)", body)

	// 3. PATCH to an upstream model is normalized the same way.
	code, body = do(t, app, http.MethodPatch, "/v1/agents/pinned", "acme",
		map[string]any{"model": "glm-5.2"})
	if code != http.StatusOK {
		t.Fatalf("patch: want 200, got %d (%s)", code, body)
	}
	scanUpstream(t, "PATCH /v1/agents/:ref", body)

	// 4. A run, and the run history and activity feed that record it.
	if code, body = do(t, app, http.MethodPost, "/v1/agents/defaulted/run", "acme",
		map[string]any{"input": "hi"}); code != http.StatusOK {
		t.Fatalf("run: want 200, got %d (%s)", code, body)
	}
	scanUpstream(t, "POST /v1/agents/:ref/run", body)

	// 5. Every remaining read surface.
	for _, path := range []string{
		"/v1/agents",
		"/v1/agents/defaulted",
		"/v1/agents/pinned",
		"/v1/agents/defaulted/runs",
		"/v1/agents/activity",
		"/v1/agents/metrics",
	} {
		code, body := do(t, app, http.MethodGet, path, "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d (%s)", path, code, body)
		}
		scanUpstream(t, "GET "+path, body)
	}
}

// TestMigrateModelRewritesStoredRows proves the data migration: rows written
// before normalization existed (des/dev/vi/verify-run on the live deployment)
// are rewritten in place on store open, the rewrite is idempotent, and the
// pre-migration value is retained so the change can be reversed.
func TestMigrateModelRewritesStoredRows(t *testing.T) {
	dir := t.TempDir() + "/agents.db"
	ctx := context.Background()

	// A store holding exactly what the live registry held.
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, name := range []string{"des", "dev", "vi", "verify-run"} {
		if err := st.Create(ctx, Agent{
			ID: "agent_" + name, Org: "hanzo", Name: name,
			Model: "deepseek-v4-flash", CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	// An agent already on a Hanzo model must NOT be touched.
	if err := st.Create(ctx, Agent{
		ID: "agent_enso", Org: "hanzo", Name: "enso",
		Model: "enso-pro", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed enso: %v", err)
	}
	// Bypass the write-side normalization to plant the rows exactly as they exist
	// live, then run the migration the way a real deploy does — by opening the store.
	if _, err := st.db.Exec(`UPDATE agents SET model='deepseek-v4-flash' WHERE name<>'enso'`); err != nil {
		t.Fatalf("plant: %v", err)
	}
	if err := st.migrateModel(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	assertModels := func(when string) {
		t.Helper()
		list, err := st.List(ctx, "hanzo")
		if err != nil {
			t.Fatalf("list %s: %v", when, err)
		}
		if len(list) != 5 {
			t.Fatalf("%s: want 5 agents, got %d", when, len(list))
		}
		for _, a := range list {
			want := cloud.DefaultModel
			if a.Name == "enso" {
				want = "enso-pro" // an already-Hanzo model is left alone
			}
			if a.Model != want {
				t.Errorf("%s: agent %q model = %q, want %q", when, a.Name, a.Model, want)
			}
		}
	}
	assertModels("after migrate")

	// Idempotent: a second pass moves nothing.
	if err := st.migrateModel(); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}
	assertModels("after second migrate")

	// Reversible: the pre-migration value was retained for exactly the four rows
	// that moved, and putting it back restores them.
	var snapshots int
	if err := st.db.QueryRow(`SELECT count(*) FROM model_snapshot`).Scan(&snapshots); err != nil {
		t.Fatalf("count snapshot: %v", err)
	}
	if snapshots != 4 {
		t.Fatalf("model_snapshot has %d rows, want 4 (only the rows that moved)", snapshots)
	}
	if _, err := st.db.Exec(`UPDATE agents SET model = (SELECT model FROM model_snapshot WHERE agent_id = agents.id)
		WHERE id IN (SELECT agent_id FROM model_snapshot)`); err != nil {
		t.Fatalf("undo: %v", err)
	}
	list, err := st.List(ctx, "hanzo")
	if err != nil {
		t.Fatalf("list after undo: %v", err)
	}
	for _, a := range list {
		want := "deepseek-v4-flash"
		if a.Name == "enso" {
			want = "enso-pro"
		}
		if a.Model != want {
			t.Errorf("undo: agent %q model = %q, want %q", a.Name, a.Model, want)
		}
	}
}

// TestSeedPersonalitiesUsesHanzoModel proves the built-in crew (dev/des/vi) is
// seeded on a Hanzo model even when the deployment default is an upstream name —
// the seed path that put deepseek-v4-flash on three live agents.
func TestSeedPersonalitiesUsesHanzoModel(t *testing.T) {
	ai := &catalogAI{content: "pong", ids: []string{"enso", "deepseek-v4-flash"}}
	app := mountAppModel(t, ai, "deepseek-v4-flash")

	n, err := SeedPersonalities(context.Background(), "acme")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != len(personalities) {
		t.Fatalf("seeded %d personas, want %d", n, len(personalities))
	}
	code, body := do(t, app, http.MethodGet, "/v1/agents", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list: want 200, got %d (%s)", code, body)
	}
	scanUpstream(t, "GET /v1/agents after SeedPersonalities", body)
}
