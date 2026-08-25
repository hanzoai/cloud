package o11y

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	"github.com/zap-proto/zip"
)

// heard records what the stand-in runtime was asked for. It is written from the
// serving goroutine — the reverse-proxy backing below runs one — so it locks.
type heard struct {
	mu  sync.Mutex
	got []string
}

func (h *heard) note(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, s)
}

func (h *heard) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.got...)
}

// A RUNTIME THAT ANSWERS BY NAME, AND A CONSOLE UNDER IT.
//
// This is the pinned hanzoai/o11y runtime's routing SHAPE, and the shape is what
// these tests are about: every API route is registered at its FULL public path,
// and one terminal console route sits beneath them all
// (query-service/app/server.go createPublicServer, pkg/web/routerweb). So an
// address the runtime does not serve does not 404 — it reaches the console, which
// answers the SPA shell with a 200. A caller reading JSON gets markup and a
// success code.
//
// The console's own rule is reproduced too: it refuses /v1 (routerweb.isAPI), so
// a miss INSIDE the API plane is an honest 404 and only a miss outside it becomes
// the shell.
func standInRuntime(h *heard) *zip.App {
	a := zip.New(zip.Config{DisableStartupMessage: true, Logger: luxlog.New("runtime")})
	a.Get(runtimeSessions, func(c *zip.Ctx) error {
		h.note(c.Method() + " " + c.Path() + queryOf(c))
		c.SetHeader("X-Runtime-Answered", "llm-sessions")
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(http.StatusOK, []byte(sessionsBody))
	})
	a.All("/*", func(c *zip.Ctx) error {
		h.note(c.Method() + " " + c.Path() + queryOf(c))
		if p := c.Path(); p == "/v1" || strings.HasPrefix(p, "/v1/") {
			return zip.ErrNotFound("404 page not found")
		}
		c.SetHeader("Content-Type", "text/html; charset=utf-8")
		return c.Bytes(http.StatusOK, []byte(consoleShell))
	})
	return a
}

func queryOf(c *zip.Ctx) string {
	if q := c.Fiber().Request().URI().QueryString(); len(q) > 0 {
		return "?" + string(q)
	}
	return ""
}

const (
	// runtimeSessions is where hanzoai/o11y registers the conversations list
	// (pkg/apiserver/o11yapiserver/llmobs.go). Spelled out rather than read from
	// sessionsRoute: a stand-in that took its address from the code under test
	// would move with it and prove nothing.
	runtimeSessions = "/v1/o11y/llm/sessions"

	sessionsBody = `{"status":"success","data":{"items":[],"offset":0,"limit":50}}`
	consoleShell = `<!doctype html><html><head><base href="/"></head><body></body></html>`
)

// backings are the two shapes runtimeHandler ever takes, and they read DIFFERENT
// fields of the request they are handed:
//
//   - in-process is what buildEmbeddedHandler installs — server.PublicHandler()
//     is adaptor.FiberApp over the runtime's router, and fiber's adaptor copies
//     r.RequestURI into the fasthttp request. An empty one erases the path.
//   - reverse proxy is cloud's own newHandler, and httputil routes off r.URL.
//
// A request correct in one field and blank in the other serves on one backing and
// falls to the console on the other, so both run every assertion.
var backingNames = []string{"in-process", "reverse proxy"}

func backings(t *testing.T, h *heard) map[string]http.Handler {
	t.Helper()
	direct := adaptor.FiberApp(standInRuntime(h).Fiber())
	srv := httptest.NewServer(direct)
	t.Cleanup(srv.Close)
	proxy, err := newHandler(srv.URL)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	return map[string]http.Handler{"in-process": direct, "reverse proxy": proxy}
}

// sessionsApp mounts the cloud-native reads exactly as mountScope registers them,
// over one backing, with the composer's Bridge at the root as cloud.App installs it.
func sessionsApp(t *testing.T, backing http.Handler) *zip.App {
	t.Helper()
	prev := runtimeHandler
	runtimeHandler = gate(backing)
	t.Cleanup(func() { runtimeHandler = prev })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	mountScope(app)
	return app
}

func getSessions(t *testing.T, app *zip.App, query string, header map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/o11y/sessions"+query, nil)
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test GET /v1/o11y/sessions%s: %v", query, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestSessionsReachesTheAddressTheRuntimeServes is the address, measured.
//
// The flat path used to be rewritten onto /api/sessions before the call was handed
// over. Nothing serves that: the runtime strips no prefix, so /api/* is not an API
// address at all — it is a console one, and the console answered it with the
// shell. Driving it end to end is the only way to see that, because every hop
// reported success: the route matched, the handler ran, the runtime replied, and
// the status was 200.
//
// Three facts, and a relay can get any one right while losing the others: the
// runtime must RECEIVE the conversations address with the caller's paging intact,
// the caller must receive the runtime's bytes, and the runtime's own headers must
// arrive with them.
func TestSessionsReachesTheAddressTheRuntimeServes(t *testing.T) {
	for _, name := range backingNames {
		t.Run(name, func(t *testing.T) {
			h := new(heard)
			app := sessionsApp(t, backings(t, h)[name])

			resp := getSessions(t, app, "?limit=2&offset=1", map[string]string{
				"X-Org-Id":  "acme",
				"X-User-Id": "u_acme",
			})
			body, _ := io.ReadAll(resp.Body)

			want := "GET " + runtimeSessions + "?limit=2&offset=1"
			if got := h.all(); len(got) != 1 || got[0] != want {
				t.Errorf("runtime received %v, want [%q]\n"+
					"The paging query is the caller's and rides through unchanged; the path is "+
					"the one the runtime serves the conversations list at.", got, want)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json\n"+
					"text/html here means the call landed on the runtime's console route "+
					"instead of a route — a 200 that is the SPA shell.", ct)
			}
			if got := string(body); got != sessionsBody {
				t.Errorf("body = %q, want %q — the runtime's own envelope arrives byte for byte",
					got, sessionsBody)
			}
			if got := resp.Header.Get("X-Runtime-Answered"); got != "llm-sessions" {
				t.Errorf("X-Runtime-Answered = %q, want %q — the runtime's headers ride through too",
					got, "llm-sessions")
			}
		})
	}
}

// TestSessionsCarriesTheCallerToTheRuntime holds the identity half. The runtime
// scopes the read from X-Org-Id and refuses a request with no X-User-Id, so a
// relay that dropped those headers would answer 403 for a caller cloud had
// already validated — and would do it from behind cloud's own check, where it
// reads as an authorization decision rather than a lost header.
func TestSessionsCarriesTheCallerToTheRuntime(t *testing.T) {
	for _, name := range backingNames {
		t.Run(name, func(t *testing.T) {
			h := new(heard)
			app := sessionsApp(t, backings(t, h)[name])

			// A validated principal with no org never reaches the runtime.
			resp := getSessions(t, app, "", map[string]string{"X-User-Id": "u_nobody"})
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("org-less caller = %d, want 403", resp.StatusCode)
			}
			if got := h.all(); len(got) != 0 {
				t.Errorf("runtime received %v for an org-less caller — the refusal is at the "+
					"cloud boundary, before the request reaches the runtime", got)
			}

			// A validated, org-scoped principal is served — which it can only be if
			// the headers the runtime reads survived the hop.
			resp = getSessions(t, app, "", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"})
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("org-scoped caller = %d (%s), want 200 — the runtime reads X-User-Id "+
					"and X-Org-Id off the request it is handed",
					resp.StatusCode, strings.TrimSpace(string(body)))
			}
		})
	}
}

// TestSessionsAddressIsOneTheModuleDeclares reads the address off the composed
// router rather than trusting the constant, so a hanzoai/o11y bump that moves the
// conversations list turns this red instead of silently re-opening the console
// route. The negative controls are the spellings that used to be here.
func TestSessionsAddressIsOneTheModuleDeclares(t *testing.T) {
	served, _ := o11yOps(t)
	if !served["GET "+sessionsRoute] {
		t.Errorf("the composed router does not serve GET %s — sessions.go relays to an address "+
			"nothing answers, which the runtime's console route absorbs with a 200", sessionsRoute)
	}
	for _, dead := range []string{"GET /api/sessions", "GET /v1/o11y/api/sessions"} {
		if served[dead] {
			t.Errorf("%s is served, and this test's control assumes it is not", dead)
		}
	}
}
