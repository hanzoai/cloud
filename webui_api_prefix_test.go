package cloud

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPIPrefixNeverRendersHTML pins the house rule: we version at /v1/ and
// never serve an /api/ prefix. Before this, /api/… matched no route, fell
// through to the SPA, and returned 200 text/html — so a caller on the wrong
// prefix saw a console page instead of an error, and /api/<nonsense> answered
// 200 too. Anything under /api/ must be a real 404, never the shell.
func TestAPIPrefixNeverRendersHTML(t *testing.T) {
	h := &consoleHandler{index: []byte("<!DOCTYPE html><title>shell</title>")}
	for _, p := range []string{"/api/", "/api/v1/user", "/api/health", "/api/totally-made-up-nonsense"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404 — /api/ must never fall through to the SPA", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<!DOCTYPE html") {
			t.Errorf("%s: returned the SPA shell; /api/ must return a real error", p)
		}
	}
}
