package websearch

// A route is one of the ways into a search and not the only one. zip records a
// typed op and its route's fiber handler as two fields of one entry and wraps
// only the second, so a control handed to Group or composed through With runs
// for REST and for nothing else — while MCP, the call plane and the graph invoke
// the op directly and the depth-0 identity middleware has already authenticated
// whoever is calling.
//
// POST /v1/websearch runs the SAME bought meta-search GET /v1/websearch/search
// runs, so it spends the caller's balance. A page the caller never visited can
// send their browser here on a simple cross-origin POST — text/plain, no
// preflight — and the cookie they already hold pays for it. Nothing leaks: the
// answer is unreadable cross-origin. What moves is money.
//
// Every row is PAIRED — a refusal is asserted beside the call that must get
// through — so a suite that only asked "did something refuse" cannot pass
// against an operation that refuses everyone.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// seamApp mounts the real surface — the real Mount, so the registrations under
// test are the ones that ship — with cloud.Bridge installed as serve.go installs
// it, which is what parks the request every seam reads its caller from. The
// counter is what the ENGINE was asked, so "refused with nothing bought" is
// measured rather than assumed.
func seamApp(t *testing.T) (*zip.App, *int32) {
	t.Helper()
	var bought int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&bought, 1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, bingFixture)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBSEARCH_ENGINES", "bing")
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	t.Setenv("WEBSEARCH_API_KEY", "k")
	// No cache, so every admitted call really asks the engine and the counter
	// below is the number of times somebody was PAID. With the default TTL a
	// second caller of the same query is served from memory and the count
	// stops measuring money.
	t.Setenv("WEBSEARCH_CACHE_TTL", "0")
	t.Setenv(account.KeyEnv, testCSRFKey)
	app := zip.New(zip.Config{Logger: luxlog.New("seam"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app, &bought
}

// The four ways in, each carrying the same query.
//
// The content type on REST and on the plane is the one a no-preflight fetch
// sends. zip does not read it, which is the point: nothing about the encoding is
// a control, so "a page cannot send JSON cross-origin" defends nothing.

// Each seam asks a DISTINCT query so the answer cache cannot collapse two calls
// into one purchase, and the engine counter stays a count of money.

func restReq() *http.Request {
	r := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(`{"q":"rest"}`))
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	return r
}

func mcpReq() *http.Request {
	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"search_web","arguments":{"q":"mcp"}}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(frame))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func graphReq() *http.Request {
	b, _ := json.Marshal(map[string]any{"query": `mutation { search_web(q: "graph") { query } }`})
	r := httptest.NewRequest(http.MethodPost, zip.GraphPath, strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// planeReq carries NO BODY, which is the cheapest form of the whole exploit and
// the only one a page can send here: zip decodes nothing when there is nothing
// to decode, so the operation runs on a zero input. A JSON body would be refused
// by the ZAP decoder before the operation, which would measure the encoding
// rather than the control.
func planeReq() *http.Request {
	r := httptest.NewRequest(http.MethodPost, zip.CallPath+"search_web", nil)
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	return r
}

// asVisitor drives a request the way a cross-site page can: the cookie the
// browser already holds, and nothing the page had to be able to read to obtain.
func asVisitor(t *testing.T, app *zip.App, r *http.Request, extra map[string]string) (int, string) {
	t.Helper()
	r.Header.Set("Cookie", "hanzo_iam_token=whatever")
	r.Header.Set("X-Org-Id", "acme")
	r.Header.Set("X-User-Id", "u-acme")
	for k, v := range extra {
		r.Header.Set(k, v)
	}
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("%s %s: %v", r.Method, r.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// namesTheControl is what a refusal has to say. A 403 alone would also be the
// answer to "unknown operation", to "not signed in" and to a typo in the id, so
// a test that accepted any 403 would pass with no control in the binary at all.
func namesTheControl(body string) bool { return strings.Contains(body, "CSRF") }

// TestACookieAloneCannotSearch. On every way in, a request carrying only the
// ambient cookie is refused and no engine is asked; the same request presenting
// a credential is served.
func TestACookieAloneCannotSearch(t *testing.T) {
	app, bought := seamApp(t)

	for _, s := range []struct {
		name string
		req  func() *http.Request
		// JSON-RPC and the graph answer 200 and carry the refusal in the frame.
		status int
	}{
		{"REST", restReq, http.StatusForbidden},
		{"MCP", mcpReq, 0},
		{"graph", graphReq, 0},
		{"call plane", planeReq, http.StatusForbidden},
	} {
		st, body := asVisitor(t, app, s.req(), nil)
		if s.status != 0 && st != s.status {
			t.Errorf("%s on a cookie alone = %d %s, want %d", s.name, st, body, s.status)
		}
		if !namesTheControl(body) {
			t.Errorf("%s on a cookie alone = %d %s — that search spends the caller's balance", s.name, st, body)
			continue
		}
		t.Logf("%s on a cookie alone: %d %.90s", s.name, st, body)
	}
	if got := atomic.LoadInt32(bought); got != 0 {
		t.Fatalf("the engine was asked %d times on a cookie alone, want 0 — that is the caller's money", got)
	}

	// A caller that PRESENTED a credential cannot be forged into, so it pays no
	// price for the control. Every way in, so no row above measured
	// unreachability.
	if st, body := asVisitor(t, app, restReq(), map[string]string{"Authorization": "Bearer t"}); st != http.StatusOK {
		t.Fatalf("REST with a bearer = %d %s, want 200 — the control must cost an API client nothing", st, body)
	}
	if st, body := asVisitor(t, app, mcpReq(), map[string]string{"Authorization": "Bearer t"}); !strings.Contains(body, "example.com") {
		t.Fatalf("MCP with a bearer was refused: %d %s", st, body)
	}
	// The plane's zero input carries no query, so the operation it reaches
	// refuses for that and spends nothing. Which is the pairing: past the
	// control, into the operation, refused by the operation's OWN rule — so the
	// plane row above measured the control and not unreachability.
	if st, body := asVisitor(t, app, planeReq(), map[string]string{"Authorization": "Bearer t"}); st != http.StatusBadRequest || !strings.Contains(body, "q required") {
		t.Fatalf("the plane with a bearer = %d %s, want 400 q required", st, body)
	}
	if got := atomic.LoadInt32(bought); got != 2 {
		t.Fatalf("the engine was asked %d times by the credentialed callers, want 2 — "+
			"the rows above measured unreachability", got)
	}
}

// TestTheTokenTheControlAsksForIsAccepted. The refusal above names a token; this
// obtains that token the way a browser does — from a same-origin response a
// cross-site page cannot read — and requires it to work. Without this row the
// control could be "refuse every cookie", which would be a broken surface rather
// than a defended one.
func TestTheTokenTheControlAsksForIsAccepted(t *testing.T) {
	app, bought := seamApp(t)
	if err := account.Use(app, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	st, body := asVisitor(t, app, httptest.NewRequest(http.MethodGet, "/v1/account/csrf", nil), nil)
	if st != http.StatusOK {
		t.Fatalf("mint token = %d %s, want 200", st, body)
	}
	var mint struct {
		Token string `json:"csrfToken"`
	}
	if err := json.Unmarshal([]byte(body), &mint); err != nil || mint.Token == "" {
		t.Fatalf("no token in %s", body)
	}

	held := map[string]string{"X-CSRF-Token": mint.Token}
	if st, body = asVisitor(t, app, restReq(), held); st != http.StatusOK {
		t.Fatalf("REST with the minted token = %d %s, want 200", st, body)
	}
	if st, body = asVisitor(t, app, mcpReq(), held); !strings.Contains(body, "example.com") {
		t.Fatalf("MCP with the minted token was refused: %d %s", st, body)
	}
	if got := atomic.LoadInt32(bought); got != 2 {
		t.Fatalf("the engine was asked %d times by two attested callers, want 2", got)
	}

	// A token minted for somebody else is not this caller's.
	r := restReq()
	r.Header.Set("Cookie", "hanzo_iam_token=whatever")
	r.Header.Set("X-Org-Id", "other")
	r.Header.Set("X-User-Id", "u-other")
	r.Header.Set("X-CSRF-Token", mint.Token)
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("other org: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another org's token = %d, want 403 — the token is bound to who it was minted for", resp.StatusCode)
	}
}
