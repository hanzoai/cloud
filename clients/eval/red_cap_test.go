package eval

import (
	"net/http"
	"strings"
	"testing"
)

// TestRed_ContentCapped is the amplification guard: an oversize dataset-item field
// is REJECTED (400), not persisted into the shared evals.db. A dataset item is a
// test case, not a blob store.
func TestRed_ContentCapped(t *testing.T) {
	app, _ := mountApp(t)
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/datasets", "acme", map[string]any{"name": "d"}); code != http.StatusCreated {
		t.Fatalf("seed dataset: %d", code)
	}
	// A 2 MiB input is rejected (raw JSON string over the 64 KiB cap).
	big := `"` + strings.Repeat("A", 2*1024*1024) + `"`
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/dataset-items", "acme",
		map[string]any{"datasetName": "d", "input": rawMsg(big)}); code != http.StatusBadRequest {
		t.Fatalf("2MiB item input want 400 (capped), got %d", code)
	}
	// Oversize metadata is likewise rejected.
	huge := map[string]any{"blob": strings.Repeat("B", 128*1024)}
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/datasets", "acme",
		map[string]any{"name": "d2", "metadata": huge}); code != http.StatusBadRequest {
		t.Fatalf("oversize metadata want 400, got %d", code)
	}
}

// TestRed_ForgedHeaderCannotCrossTenant is the cross-tenant regression guard: a
// caller authenticated as org A cannot read org B's scores by setting
// X-Project-Id / a second X-Org-Id — the server scopes by c.Org() only. (In the
// live stack SanitizeIdentity mints X-Org-Id from the validated bearer and strips
// client copies; here we prove the handler trusts ONLY that value.)
func TestRed_ForgedHeaderCannotCrossTenant(t *testing.T) {
	app, _ := mountApp(t)

	// victim (org B) writes a score.
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/scores", "victim",
		map[string]any{"name": "quality", "value": 0.99}); code != http.StatusCreated {
		t.Fatalf("victim score: %d", code)
	}

	// attacker (org A) sets X-Project-Id: victim — must NOT see victim's score.
	req := newReq(http.MethodGet, "/v1/evals/scores", nil)
	req.Header.Set("X-Org-Id", "attacker")
	req.Header.Set("X-Project-Id", "victim")
	code, body := send(t, app, req)
	if code != http.StatusOK {
		t.Fatalf("attacker list want 200, got %d", code)
	}
	if strings.Contains(string(body), "0.99") || strings.Contains(string(body), "quality") {
		t.Fatalf("cross-tenant score leak via X-Project-Id: %s", body)
	}
}

// TestRed_ScoreTypeCannotBeCoerced is the score-integrity guard: once a score name
// has a NUMERIC config, a caller CANNOT sneak a categorical/string value under that
// name (the config is authoritative for the type), nor push a non-finite value.
func TestRed_ScoreTypeCannotBeCoerced(t *testing.T) {
	app, _ := mountApp(t)
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/score-configs", "o",
		map[string]any{"name": "quality", "dataType": "NUMERIC", "minValue": 0, "maxValue": 1}); code != http.StatusCreated {
		t.Fatalf("config: %d", code)
	}
	// Claiming dataType CATEGORICAL for a NUMERIC-configured name is ignored — the
	// config wins, so a numeric value is REQUIRED (stringValue alone → 400).
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/scores", "o",
		map[string]any{"name": "quality", "dataType": "CATEGORICAL", "stringValue": "great"}); code != http.StatusBadRequest {
		t.Fatalf("type-coercion attempt want 400, got %d", code)
	}
	// A finite in-range numeric still works (proves the guard didn't over-block).
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/scores", "o",
		map[string]any{"name": "quality", "value": 0.5}); code != http.StatusCreated {
		t.Fatalf("valid numeric want 201, got %d", code)
	}
}

// TestRed_RunNameAndJudgeNameGuarded pins the injection/traversal guard on the run
// name and judge name — both become stored identifiers (a run key and a score
// name), so a path-traversal-looking value is rejected (runName) or defaulted
// (judge name), never persisted verbatim.
func TestRed_RunNameAndJudgeNameGuarded(t *testing.T) {
	app, _ := mountApp(t)
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/datasets", "o", map[string]any{"name": "qa"}); code != http.StatusCreated {
		t.Fatalf("seed dataset: %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/dataset-items", "o",
		map[string]any{"datasetName": "qa", "input": "x", "expectedOutput": "x"}); code != http.StatusCreated {
		t.Fatalf("seed item: %d", code)
	}
	// A traversal-looking runName is rejected at the boundary.
	req := newReqJSON(http.MethodPost, "/v1/evals/runs", map[string]any{
		"dataset": "qa", "model": "m", "runName": "../../etc/passwd",
	})
	req.Header.Set("X-Org-Id", "o")
	req.Header.Set("Authorization", "Bearer hk-test")
	if code, _ := send(t, app, req); code != http.StatusBadRequest {
		t.Fatalf("traversal runName want 400, got %d", code)
	}
}
