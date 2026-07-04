package automations

import (
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// MCP — the HIP-0300 JSON-RPC 2.0 tool surface at POST /v1/automations/mcp. Every
// connector action is exposed as a tool named "<connector>_<action>", so /v1/agents
// can invoke a connector as an agent tool. GATED: a request without a validated
// principal is refused 403 (never the unscoped tool plane); tools/call dispatches to
// the action's Run with RunContext bound to the caller's VALIDATED org — the same
// isolation boundary the durable activity uses.

// mcpRequest is the JSON-RPC 2.0 request envelope (mirrors tasks/pkg/tasks/mcp.go).
type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// mcp is the single JSON-RPC endpoint. Org-gated at the top: no validated principal
// → 403, so a client-forged X-Org-Id with no bearer can never reach a tool.
func (s *svc) mcp(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	var req mcpRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.JSON(http.StatusOK, mcpErrorObj(nil, -32700, "parse error: "+err.Error()))
	}
	switch req.Method {
	case "initialize":
		return c.JSON(http.StatusOK, mcpResultObj(req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"serverInfo":      map[string]any{"name": "hanzo-automations", "version": "1.0.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}))
	case "ping":
		return c.JSON(http.StatusOK, mcpResultObj(req.ID, map[string]any{}))
	case "tools/list":
		return c.JSON(http.StatusOK, mcpResultObj(req.ID, map[string]any{"tools": mcpTools()}))
	case "tools/call":
		return s.mcpToolCall(c, org, req)
	default:
		return c.JSON(http.StatusOK, mcpErrorObj(req.ID, -32601, "method not found: "+req.Method))
	}
}

// mcpToolCall dispatches a tool to its connector action's Run. RunContext.Org and
// the Token closure are pinned to the VALIDATED org — a caller can never invoke a
// tool against another tenant's credentials. One metered unit + one audit record per
// call.
func (s *svc) mcpToolCall(c *zip.Ctx, org string, req mcpRequest) error {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	body, _ := json.Marshal(req.Params)
	_ = json.Unmarshal(body, &p)

	connector, action, ok := resolveTool(p.Name)
	if !ok {
		return c.JSON(http.StatusOK, mcpErrorObj(req.ID, -32601, "unknown tool: "+p.Name))
	}
	_, act, err := lookupAction(connector, action)
	if err != nil {
		return c.JSON(http.StatusOK, mcpErrorObj(req.ID, -32601, err.Error()))
	}

	// Per-org concurrency bound (LOW-2): the tool executes SYNCHRONOUSLY here, so a
	// burst of core.delay calls would otherwise pin a goroutine each for up to the
	// delay cap. Held across Run, released after.
	if !orgRunLimiter.acquire(org) {
		return c.JSON(http.StatusOK, mcpErrorObj(req.ID, -32005, "too many concurrent tool calls for this org"))
	}
	defer orgRunLimiter.release(org)

	ctx := c.Context()
	rc := RunContext{
		Org:   org,
		Input: p.Arguments,
		Token: func(secretName string) ([]byte, error) {
			return tokenSource(ctx, org, connector, secretName)
		},
	}

	// Meter + audit AFTER Run, deriving the outcome from the real result (LOW-1): a
	// failed / SSRF-blocked / not-connected call is audited as an error and is NOT
	// billed as a successful unit.
	out, err := act.Run(ctx, rc)
	if err != nil {
		s.auditEvent(c, org, "automations.mcp.call", p.Name, "error", http.StatusFailedDependency)
		return c.JSON(http.StatusOK, mcpErrorObj(req.ID, -32000, err.Error()))
	}
	s.meterUnit(org, c)
	s.auditEvent(c, org, "automations.mcp.call", p.Name, "ok", http.StatusOK)

	text, _ := json.Marshal(out)
	return c.JSON(http.StatusOK, mcpResultObj(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(text)}},
	}))
}

// mcpTools is the tool catalogue: every connector action, name "<connector>_<action>",
// with an input schema derived from its Props. Stable order (sorted).
func mcpTools() []map[string]any {
	tools := make([]map[string]any, 0, 16)
	for _, c := range sortedConnectors() {
		for _, a := range sortedActions(c) {
			tools = append(tools, map[string]any{
				"name":        c.Name + "_" + a.Name,
				"description": a.Description,
				"inputSchema": propsToSchema(a.Props),
			})
		}
	}
	return tools
}

// propsToSchema derives a JSON-Schema object from an action's Props — the ONE
// mapping from PropSpec to the wire schema, shared by every tool.
func propsToSchema(props []PropSpec) map[string]any {
	properties := make(map[string]any, len(props))
	required := make([]string, 0, len(props))
	for _, p := range props {
		properties[p.Name] = map[string]any{"type": jsonType(p.Type), "description": p.Description}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

// jsonType normalizes a PropSpec type to a JSON-Schema type (default "string").
func jsonType(t string) string {
	switch t {
	case "number", "boolean", "object", "array", "string":
		return t
	default:
		return "string"
	}
}

// resolveTool maps a "<connector>_<action>" tool name back to its (connector,action)
// pair. Connector and action names both contain underscores, so an unambiguous
// resolution walks the registry rather than splitting the string.
func resolveTool(name string) (connector, action string, ok bool) {
	for cn, c := range registry {
		for an := range c.Actions {
			if cn+"_"+an == name {
				return cn, an, true
			}
		}
	}
	return "", "", false
}

func mcpResultObj(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func mcpErrorObj(id any, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}
