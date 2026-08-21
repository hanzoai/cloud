package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// An address that EXISTS for another method must answer 405 with Allow, never 404.
//
// This is the defect measured on api.hanzo.ai: `GET /v1/chat/completions` answered
// `404 not found` although the route is registered, served and working — because
// the terminal catch-all matches every method at every path, so fiber's own
// 405-with-Allow computation never runs. Twice that 404 was escalated as a missing
// route on the model plane, and the route was fine both times.
func TestAnExistingAddressAnswers405WithAllow(t *testing.T) {
	app := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	post := func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "yes"}) }
	app.Post("/v1/chat/completions", post)
	app.Get("/v1/models", post)
	if err := Mount(app, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	h, err := Handler(nil, app.MCP, routerAllow(app))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	for _, tc := range []struct {
		name, method, path, allow string
		want                      int
	}{
		{"post-only route reached by GET", http.MethodGet, "/v1/chat/completions", "POST", http.StatusMethodNotAllowed},
		// fiber auto-registers HEAD alongside GET, so HEAD is genuinely allowed
		// here and the header says so. The answer is the ROUTER's, not a guess.
		{"get-only route reached by DELETE", http.MethodDelete, "/v1/models", "GET, HEAD", http.StatusMethodNotAllowed},
		{"no such address stays 404", http.MethodGet, "/v1/zzz-does-not-exist", "", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("%s %s: status %d, want %d (body %q)", tc.method, tc.path, w.Code, tc.want, w.Body.String())
			}
			if got := w.Header().Get("Allow"); got != tc.allow {
				t.Fatalf("%s %s: Allow %q, want %q", tc.method, tc.path, got, tc.allow)
			}
		})
	}
}

// The terminal catch-all must be excluded from the answer, or every address in the
// fleet reports every method as allowed and the 404 case disappears entirely.
func TestTheCatchAllIsNotAnAllowedMethod(t *testing.T) {
	app := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	if err := Mount(app, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if got := routerAllow(app)("/v1/anything"); len(got) != 0 {
		t.Fatalf("a router serving only the catch-all claims %v; it must claim nothing", got)
	}
}

// With no router to ask, the handler makes no claim about the address — which is
// the behaviour every caller had before Allow existed.
func TestWithNoRouterToAskItStays404(t *testing.T) {
	app := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	app.Post("/v1/chat/completions", func(c *zip.Ctx) error { return nil })
	h, err := Handler(nil, app.MCP, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}
