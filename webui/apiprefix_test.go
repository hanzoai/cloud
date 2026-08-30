package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestAPIPrefixNeverRendersHTML pins the house rule: we version at /v1/ and
// never serve an /api/ prefix. Before this, /api/… matched no route, fell
// through to the SPA, and returned 200 text/html — so a caller on the wrong
// prefix saw a console page instead of an error, and /api/<nonsense> answered
// 200 too. Anything under /api/ must be a real 404, never the shell.
func TestAPIPrefixNeverRendersHTML(t *testing.T) {
	h := &consoleHandler{fsys: fstest.MapFS{
		"index.html": {Data: []byte("<!DOCTYPE html><title>shell</title>")},
	}}
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

// The OAuth login namespace must never be answered with the SPA.
//
// This is the bug it exists for: IAM's authorization endpoint redirects a
// browser to /login/oauth/authorize, iam claims that prefix, and the catch-all
// answered anything under it that iam did not serve. `hanzo auth login` against
// api.hanzo.ai was handed the console shell, which rendered "No such page", and
// the sign-in ended with 200 at every hop — so neither the CLI nor the browser
// could tell it had failed rather than arrived.
//
// A 404 here is the honest answer: it says nothing serves that address, which a
// relying party can act on. HTML is the one answer it cannot.
func TestOAuthLoginNamespaceNeverRendersHTML(t *testing.T) {
	h := &consoleHandler{fsys: fstest.MapFS{
		"index.html": {Data: []byte("<!DOCTYPE html><title>shell</title>")},
	}}
	for _, p := range []string{
		"/login/oauth",
		"/login/oauth/authorize",
		"/login/oauth/authorize?client_id=hanzo-cli&response_type=code",
		"/login/oauth/anything-iam-does-not-serve",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404 — an OAuth client must not receive the SPA", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<!DOCTYPE html") {
			t.Errorf("%s: returned the console shell, which is what ended the sign-in", p)
		}
	}
}

// And the console's OWN routes still fall through, so the fix is scoped to the
// protocol namespace rather than to everything under /login.
func TestConsoleRoutesOutsideTheOAuthNamespaceStillRender(t *testing.T) {
	h := &consoleHandler{fsys: fstest.MapFS{
		"index.html": {Data: []byte("<!DOCTYPE html><title>shell</title>")},
	}}
	for _, p := range []string{"/models", "/settings", "/deploy"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200 — a console route must still reach the SPA", p, rec.Code)
		}
	}
}
