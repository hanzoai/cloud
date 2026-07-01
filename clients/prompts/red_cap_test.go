package prompts

import (
	"net/http"
	"strings"
	"testing"
)

// TestRed_PromptContentCapped is the REGRESSION GUARD for Red MED-1 (was
// TestRed_PromptContentUncapped). Content over the cap is rejected, and a prompt
// with a long append history yields a BOUNDED detail response (metadata-only
// history), so the shared prompts.db can't be amplified into a 46 MB reply.
func TestRed_PromptContentCapped(t *testing.T) {
	app := mountApp(t)

	// A 2 MiB prompt is now REJECTED (400), not persisted.
	big := strings.Repeat("A", 2*1024*1024)
	if code, _ := do(t, app, http.MethodPost, "/v1/prompts", "acme",
		map[string]any{"name": "bloat", "prompt": big}); code != http.StatusBadRequest {
		t.Fatalf("2MB prompt want 400 (capped), got %d", code)
	}

	// A valid prompt POSTed many times appends versions, but the detail response
	// stays small — history is metadata-only, content is not re-emitted per version.
	small := strings.Repeat("A", 4096)
	for i := 0; i < 30; i++ {
		if code, _ := do(t, app, http.MethodPost, "/v1/prompts", "acme",
			map[string]any{"name": "ok", "prompt": small}); code != http.StatusCreated {
			t.Fatalf("version %d create want 201, got %d", i, code)
		}
	}
	code, body := do(t, app, http.MethodGet, "/v1/prompts/ok", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("detail want 200, got %d", code)
	}
	// 30 versions of 4 KiB content would be ~120 KiB if content were echoed per
	// version; bounded metadata-only history keeps it well under 64 KiB.
	if len(body) > 64*1024 {
		t.Fatalf("detail response must be bounded (metadata-only history), got %d bytes", len(body))
	}
	t.Logf("BOUNDED: 30-version prompt detail = %d bytes (no per-version content amplification)", len(body))
}
