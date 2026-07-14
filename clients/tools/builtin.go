package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// full-cloud-control (task 3). Every cloud /v1 route becomes a Tool, so a per-user
// token does over MCP exactly what that user may do over HTTP. Dispatch REPLAYS the
// caller's own credential through the SAME Fiber app + middleware chain
// (SanitizeIdentity → BillingGate → the route handler), so authorization is the
// SAME IAM check — never a parallel one. This is the zapface in-process pattern
// generalized to a tool source.
//
// Billing note: a builtin dispatch bills TWICE by design — the tools-plane unit
// (the HTTP layer's meter, product "tools") AND the replayed route's own gate
// (its product, e.g. a provision fee). They are distinct ledger lines, not a
// double-charge of one unit; set CLOUD_TOOLS_FEE_CENTS=0 to make the orchestration
// unit free while the underlying route still charges.

// builtinProvider generates + dispatches tools from the live route table. It holds
// the app so it enumerates routes lazily (at request time), after every subsystem
// has mounted its routes.
type builtinProvider struct {
	app *zip.App
}

func newBuiltinProvider(app *zip.App) *builtinProvider { return &builtinProvider{app: app} }

func (p *builtinProvider) Source() Source { return SourceBuiltin }

// dispatchMethods are the HTTP verbs exposed as tools (read + write). HEAD/OPTIONS
// are transport, not capabilities.
var dispatchMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true,
}

// route is one enumerated /v1 route reduced to what a tool needs.
type route struct {
	method string
	path   string   // fiber path template, e.g. /v1/agents/:ref/run
	params []string // path param names in order
}

// enumerate returns the exposable /v1 routes from the live Fiber stack: concrete
// verbs only, /v1 only, no wildcards (proxy/static), and never the tool plane's own
// endpoints (recursion). This is the ONE route→tool projection, shared by List and
// Dispatch so a tool name resolves identically both ways.
func (p *builtinProvider) enumerate() []route {
	if p.app == nil {
		return nil
	}
	var out []route
	seen := map[string]bool{}
	for _, r := range p.app.Fiber().GetRoutes(true) {
		m := strings.ToUpper(r.Method)
		if !dispatchMethods[m] {
			continue
		}
		if !strings.HasPrefix(r.Path, "/v1/") {
			continue
		}
		if strings.Contains(r.Path, "*") { // wildcard proxy/static — not a discrete capability.
			continue
		}
		if strings.HasPrefix(r.Path, "/v1/tools/") { // never expose the tool plane through itself.
			continue
		}
		if strings.HasSuffix(r.Path, "/health") {
			continue
		}
		key := m + " " + r.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, route{method: m, path: r.Path, params: append([]string(nil), r.Params...)})
	}
	return out
}

func (p *builtinProvider) List(ctx context.Context, scope Scope) ([]Tool, error) {
	routes := p.enumerate()
	out := make([]Tool, 0, len(routes))
	for _, r := range routes {
		out = append(out, Tool{
			Name:         routeToolName(r.method, r.path),
			Source:       SourceBuiltin,
			Description:  r.method + " " + r.path,
			Schema:       routeSchema(r),
			Dispatchable: true,
		})
	}
	return out, nil
}

// Dispatch resolves the tool to its route, fills path params + query + body from
// args, and replays the request in-process with the caller's credential. The full
// middleware chain runs, so the route's own auth/billing enforce identically to a
// direct HTTP call.
func (p *builtinProvider) Dispatch(ctx context.Context, pr Principal, name string, args map[string]any) (any, error) {
	var target *route
	for _, r := range p.enumerate() {
		if routeToolName(r.method, r.path) == name {
			rr := r
			target = &rr
			break
		}
	}
	if target == nil {
		return nil, ErrUnknownTool
	}

	// Fill the concrete path from the params in args.
	concrete := target.path
	for _, param := range target.params {
		val, _ := args[param].(string)
		concrete = strings.Replace(concrete, ":"+param, url.PathEscape(val), 1)
	}
	// Optional query string.
	if q, ok := args["query"].(map[string]any); ok && len(q) > 0 {
		vals := url.Values{}
		for k, v := range q {
			vals.Set(k, toStr(v))
		}
		concrete += "?" + vals.Encode()
	}
	// Optional JSON body (write verbs only).
	var body io.Reader
	hasBody := false
	if target.method != http.MethodGet && args["body"] != nil {
		b, err := json.Marshal(args["body"])
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		hasBody = true
	}

	req := newRequestWithContext(ctx, target.method, concrete, body)
	for k, v := range pr.credential {
		req.Header.Set(k, v)
	}
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.app.Fiber().Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxMCPResponse))

	out := map[string]any{"status": resp.StatusCode}
	var parsed any
	if json.Unmarshal(raw, &parsed) == nil {
		out["body"] = parsed
	} else {
		out["body"] = string(raw)
	}
	return out, nil
}

// routeToolName is the deterministic tool name for a route, used by both List and
// Dispatch. Flat, lowercase, "_"-separated (the automations tool-name style), path
// params kept by name: GET /v1/agents/:ref/run → cloud_get_agents_ref_run.
func routeToolName(method, path string) string {
	slug := strings.TrimPrefix(path, "/v1/")
	slug = strings.ReplaceAll(slug, "/", "_")
	slug = strings.ReplaceAll(slug, ":", "")
	slug = strings.Trim(slug, "_")
	return "cloud_" + strings.ToLower(method) + "_" + slug
}

// httptest.NewRequestWithContext is a thin helper mirroring httptest.NewRequest but
// carrying ctx, so a client disconnect cancels the in-process replay.
func newRequestWithContext(ctx context.Context, method, target string, body io.Reader) *http.Request {
	return httptest.NewRequest(method, target, body).WithContext(ctx)
}

// routeSchema builds the JSON-Schema for a route tool: each path param is a
// required string; query + body are optional objects.
func routeSchema(r route) json.RawMessage {
	props := map[string]any{}
	required := make([]string, 0, len(r.params))
	for _, param := range r.params {
		props[param] = map[string]any{"type": "string", "description": "path parameter :" + param}
		required = append(required, param)
	}
	props["query"] = map[string]any{"type": "object", "description": "query string parameters"}
	if r.method != http.MethodGet {
		props["body"] = map[string]any{"type": "object", "description": "JSON request body"}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	b, _ := json.Marshal(schema)
	return b
}

func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return strings.Trim(string(b), `"`)
	}
}
