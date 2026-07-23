package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerServesBoard proves the embedded board is served: the root returns the
// dashboard HTML, an unknown path falls back to it (SPA behavior), and a non-GET is
// refused. This is the serving contract cloud mounts under /research.
func TestHandlerServesBoard(t *testing.T) {
	h := http.StripPrefix("/research", Handler())

	get := func(pathStr string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, pathStr, nil))
		return w
	}

	// Root serves the board.
	w := get("/research")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /research = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"R&amp;D Ops Board", "/v1/research/projects", "seed snapshot"} {
		if !strings.Contains(body, want) {
			t.Errorf("board HTML missing %q", want)
		}
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	// Deep link / unknown path falls back to the board shell, not 404.
	if w := get("/research/enso-arena"); w.Code != http.StatusOK {
		t.Errorf("SPA fallback GET /research/enso-arena = %d, want 200", w.Code)
	}

	// Non-GET is refused.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/research", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /research = %d, want 405", w.Code)
	}
}
