// Package probe measures what zip's typed-op machinery can actually bind, at
// the zip version this module pins. It exists because `zip.Get[In, Out]` is
// advertised as ONE op projected into three surfaces (REST · OpenAPI · MCP),
// and cloud's 792 raw routes were slated to migrate onto it. Before migrating
// them, this measures the projection against the route shapes cloud ACTUALLY
// has: a path param (/v1/agents/sessions/:id), query filters (?live&host=), and
// an org that must come from the validated principal rather than the caller.
//
// The result gates the migration, so it is a test, not a memo: it fails the
// build if the answer changes.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// The In/Out shapes /v1/agents/sessions would use, mirroring the real handlers
// (sessions.go: idParam(c) + principal.Org(c)).
type sessionKey struct {
	ID string `json:"id"`
}

type sessionView struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type sessionFilter struct {
	Live bool   `json:"live"`
	Host string `json:"host"`
}

type sessionList struct {
	Sessions []sessionView `json:"sessions"`
}

// orgKey is the context key a principal→context bridge would park the VALIDATED
// org under. It is a context value, never an In field: an In field would let the
// caller assert its own org.
type orgKey struct{}

// bound records what one typed handler actually received.
type bound struct {
	key    sessionKey
	filter sessionFilter
	org    string
	ctx    string
}

// freeAddr reserves a port so parallel packages cannot collide.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func serve(t *testing.T, app *zip.App) string {
	t.Helper()
	addr := freeAddr(t)
	go func() { _ = app.Listen("http://" + addr) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			c.Close()
			t.Cleanup(func() { _ = app.Shutdown() })
			return "http://" + addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("app never listened on %s", addr)
	return ""
}

func get(t *testing.T, url string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func rpc(t *testing.T, url, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// TestURLBinding measures whether a typed op can see its own URL.
//
// zip v1.8.2 decodes In from the request BODY only — registerTyped's fiber
// handler passes c.Body() (nil for GET) into op.invoke and nothing else. So a
// path param and a query filter never reach the handler. 16 of clients/agents'
// 25 routes carry a path param; every one of them would receive a zero In.
func TestTypedOpCannotBindURL(t *testing.T) {
	var got bound
	app := zip.New(zip.Config{AppName: "probe", OpenAPI: zip.OpenAPIConfig{Title: "cloud", Version: "v1.0.0"}})

	zip.Get[sessionKey, sessionView](app, "/v1/agents/sessions/:id",
		func(ctx context.Context, in *sessionKey) (*sessionView, error) {
			got.key = *in
			got.ctx = fmt.Sprintf("%T", ctx)
			return &sessionView{ID: in.ID, Status: "running"}, nil
		},
		zip.WithOperationID("getAgentSession"),
		zip.WithSummary("Get one agent session by id"),
		zip.WithTags("agents", "sessions"))

	zip.Get[sessionFilter, sessionList](app, "/v1/agents/sessions",
		func(ctx context.Context, in *sessionFilter) (*sessionList, error) {
			got.filter = *in
			return &sessionList{Sessions: []sessionView{}}, nil
		},
		zip.WithOperationID("listAgentSessions"),
		zip.WithSummary("List agent sessions"),
		zip.WithTags("agents", "sessions"))

	base := serve(t, app)

	_, body := get(t, base+"/v1/agents/sessions/sess-abc123", map[string]string{"X-Org-Id": "acme"})
	t.Logf("GET /v1/agents/sessions/sess-abc123 -> %s", body)
	t.Logf("  In.ID   = %q", got.key.ID)
	t.Logf("  ctx     = %s", got.ctx)

	_, _ = get(t, base+"/v1/agents/sessions?live=true&host=evo", map[string]string{"X-Org-Id": "acme"})
	t.Logf("GET /v1/agents/sessions?live=true&host=evo")
	t.Logf("  In      = %+v", got.filter)

	// THE FINDING, pinned as the current contract. These assertions say the URL
	// does NOT reach a typed handler. When they start failing, zip has gained URL
	// binding — invert them and the clients/agents migration is unblocked.
	if got.key.ID != "" {
		t.Fatalf("zip now binds path params (In.ID=%q) — UNBLOCKED: invert this test "+
			"and migrate clients/agents", got.key.ID)
	}
	if got.filter.Live || got.filter.Host != "" {
		t.Fatalf("zip now binds query params (In=%+v) — UNBLOCKED: invert this test", got.filter)
	}
	t.Log("PINNED: a typed op receives a ZERO In from a URL — no path param, no query. " +
		"16/25 clients/agents routes carry a path param, so they cannot migrate as-is.")
}

// TestProjections records the payoff side: the OpenAPI document and the MCP tool
// surface DO populate from the typed ops. Both are EMPTY today only because
// cloud registers zero typed ops (installOpenAPIRoutes/installMCP early-return
// on len(a.ops)==0). The registry works; the binding is what does not.
func TestTypedOpProjectionsPopulate(t *testing.T) {
	app := zip.New(zip.Config{AppName: "probe", OpenAPI: zip.OpenAPIConfig{Title: "cloud", Version: "v1.0.0"}})
	zip.Get[sessionKey, sessionView](app, "/v1/agents/sessions/:id",
		func(ctx context.Context, in *sessionKey) (*sessionView, error) {
			return &sessionView{ID: in.ID}, nil
		},
		zip.WithOperationID("getAgentSession"),
		zip.WithSummary("Get one agent session by id"),
		zip.WithTags("agents", "sessions"))
	base := serve(t, app)

	code, spec := get(t, base+"/.well-known/openapi.json", nil)
	var doc map[string]any
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		t.Fatalf("openapi not json: %v", err)
	}
	pretty, _ := json.MarshalIndent(doc, "", "  ")
	t.Logf("GET /.well-known/openapi.json -> %d\n%s", code, pretty)

	code, tools := rpc(t, base+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	t.Logf("POST /mcp tools/list -> %d\n%s", code, tools)

	if !strings.Contains(spec, "getAgentSession") {
		t.Errorf("OpenAPI projection missing the op")
	}
	if !strings.Contains(tools, "getAgentSession") {
		t.Errorf("MCP projection missing the op")
	}

	// The path is templated, so OpenAPI 3.1 REQUIRES a matching path-parameter
	// object. zip emits none — pinned. When this fails, the doc projection has
	// learned parameters and the emitted spec is valid.
	if strings.Contains(spec, `"in": "path"`) || strings.Contains(spec, `"in":"path"`) {
		t.Fatalf("zip now emits path parameters — UNBLOCKED: invert this test")
	}
	t.Log("PINNED: the doc declares templated path /v1/agents/sessions/{id} with NO " +
		"parameter object — an invalid OpenAPI 3.1 document that cannot tell a client about :id.")
}

// TestMCPIsUnauthenticated is the security half. A typed op is auto-published as
// an MCP tool at POST /mcp, and mcpCall runs op.invoke DIRECTLY. The handler
// receives context.Background() — it cannot see a header, so it cannot run
// cloud's authz gate (tenant(c) → 403), which every agents handler relies on.
//
// So the op answers an anonymous caller. The only way a typed handler could
// learn an org would be an org field IN the typed In — which is caller-supplied,
// i.e. a cross-tenant read. Both horns are unacceptable; this test pins them.
func TestTypedOpMCPIsAnonymous(t *testing.T) {
	var got bound
	app := zip.New(zip.Config{AppName: "probe", OpenAPI: zip.OpenAPIConfig{Title: "cloud", Version: "v1.0.0"}})
	zip.Get[sessionKey, sessionView](app, "/v1/agents/sessions/:id",
		func(ctx context.Context, in *sessionKey) (*sessionView, error) {
			got.key = *in
			if v, ok := ctx.Value(orgKey{}).(string); ok {
				got.org = v
			}
			return &sessionView{ID: in.ID, Status: "running"}, nil
		},
		zip.WithOperationID("getAgentSession"),
		zip.WithSummary("Get one agent session by id"),
		zip.WithTags("agents", "sessions"))
	base := serve(t, app)

	code, body := rpc(t, base+"/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getAgentSession","arguments":{"id":"sess-victim"}}}`,
		nil) // NO auth headers whatsoever
	t.Logf("MCP tools/call getAgentSession, anonymous -> %d\n  %s", code, body)
	t.Logf("  In.ID reached handler = %q, org = %q", got.key.ID, got.org)

	// Pinned: MCP is the ONLY projection that fills In (tool arguments arrive as
	// the body), and it runs op.invoke with no identity at all.
	if got.key.ID != "sess-victim" {
		t.Fatalf("MCP no longer reaches the handler (In.ID=%q) — the projection changed", got.key.ID)
	}
	if strings.Contains(body, "isError") {
		t.Fatalf("an authorizer refused the anonymous call — UNBLOCKED: a gate now exists, invert this test")
	}
	t.Logf("PINNED: registering a typed op publishes it at POST /mcp and it answers an "+
		"ANONYMOUS caller (org=%q, ctx=context.Background()). cloud's gate is per-handler "+
		"(tenant(c)->403) and a typed handler cannot run it.", got.org)
}

// TestPrincipalBridge measures the REMEDY for the org half, on stock zip.
//
// fiber's SetContext (already used by cloud's TracingMiddleware,
// middleware_tracing.go:108) is honored by DefaultCtx.Context(), which
// registerTyped passes to op.invoke. So middleware CAN hand the validated org to
// a typed handler through the request context — server-side, off the wire, and
// NOT as an In field. It works over REST and MCP alike, and an anonymous caller
// arrives with an empty org, which the handler refuses.
//
// This half needs no framework change. Only URL binding does.
func TestPrincipalBridgeCarriesOrg(t *testing.T) {
	var got bound
	app := zip.New(zip.Config{AppName: "probe", OpenAPI: zip.OpenAPIConfig{Title: "cloud", Version: "v1.0.0"}})

	// Stands in for principal.Org(c): read the SERVER-VALIDATED value, park it on
	// the request context. The caller cannot forge it — SanitizeIdentity strips
	// the raw header on ingress and re-injects only from validated claims.
	app.Use(func(c *zip.Ctx) error {
		if org := c.Header("X-Org-Id"); org != "" {
			c.SetContext(context.WithValue(c.Context(), orgKey{}, org))
		}
		return c.Next()
	})

	zip.Get[sessionKey, sessionView](app, "/v1/agents/sessions/:id",
		func(ctx context.Context, in *sessionKey) (*sessionView, error) {
			got.key = *in
			got.org, _ = ctx.Value(orgKey{}).(string)
			if got.org == "" {
				return nil, zip.ErrForbidden("X-Org-Id required")
			}
			return &sessionView{ID: in.ID, Status: "running"}, nil
		},
		zip.WithOperationID("getAgentSession"),
		zip.WithSummary("Get one agent session by id"),
		zip.WithTags("agents", "sessions"))
	base := serve(t, app)

	got = bound{}
	_, body := get(t, base+"/v1/agents/sessions/sess-abc", map[string]string{"X-Org-Id": "acme"})
	t.Logf("REST  GET :id (X-Org-Id: acme) -> %s ; org reached handler = %q", body, got.org)
	if got.org != "acme" {
		t.Errorf("org did NOT reach the typed handler over REST — the bridge does not work")
	}

	got = bound{}
	_, mbody := rpc(t, base+"/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getAgentSession","arguments":{"id":"sess-xyz"}}}`,
		map[string]string{"X-Org-Id": "acme"})
	t.Logf("MCP   tools/call (X-Org-Id: acme) -> %s ; org reached handler = %q", mbody, got.org)
	if got.org != "acme" {
		t.Errorf("org did NOT reach the typed handler over MCP")
	}

	got = bound{}
	_, abody := rpc(t, base+"/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getAgentSession","arguments":{"id":"sess-xyz"}}}`,
		nil)
	t.Logf("MCP   tools/call anonymous -> %s ; org reached handler = %q", abody, got.org)
	if got.org != "" {
		t.Errorf("anonymous caller carried org %q — the bridge leaks", got.org)
	}
	if !strings.Contains(abody, "isError") {
		t.Errorf("anonymous MCP call was NOT refused: %s", abody)
	}
}
