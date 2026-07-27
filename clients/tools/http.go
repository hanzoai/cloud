package tools

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	maxName  = 128
	maxURL   = 2048
	maxBatch = 256
)

// ── GET /v1/tools — discovery (all sources, activated flags) ────────────────────

func listTools(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	tools := Default().List(c.Context(), Scope{Org: p.Org, Project: p.Project})
	srcFilter := Source(strings.TrimSpace(c.Query("source")))
	activatedOnly := c.Query("activated") == "true"
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if srcFilter != "" && t.Source != srcFilter {
			continue
		}
		if activatedOnly && !t.Activated {
			continue
		}
		out = append(out, t)
	}
	return c.JSON(http.StatusOK, map[string]any{"tools": out})
}

// ── POST /v1/tools/mcp — the unified MCP JSON-RPC surface ───────────────────────

type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// mcp is the single JSON-RPC endpoint spanning EVERY source. Org-gated at the top:
// no validated principal → 403, so a client-forged X-Org-Id with no credential can
// never reach a tool. tools/list returns only the caller's ACTIVATED, dispatchable
// tools (the agent-facing set); tools/call dispatches through the registry's ONE
// per-principal plane (activation gate → price gate → dispatch).
func mcp(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var req mcpRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.JSON(http.StatusOK, rpcError(nil, -32700, "parse error: "+err.Error()))
	}
	switch req.Method {
	case "initialize":
		return c.JSON(http.StatusOK, rpcResult(req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"serverInfo":      map[string]any{"name": "hanzo-tools", "version": "1.0.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}))
	case "ping":
		return c.JSON(http.StatusOK, rpcResult(req.ID, map[string]any{}))
	case "tools/list":
		return c.JSON(http.StatusOK, rpcResult(req.ID, map[string]any{"tools": mcpToolList(s, c, p)}))
	case "tools/call":
		return mcpToolCall(s, c, p, req)
	default:
		return c.JSON(http.StatusOK, rpcError(req.ID, -32601, "method not found: "+req.Method))
	}
}

// mcpToolList projects the ACTIVATED, dispatchable tools into the MCP tool shape.
func mcpToolList(s *cloud.Service[state], c *zip.Ctx, p Principal) []map[string]any {
	all := Default().List(c.Context(), Scope{Org: p.Org, Project: p.Project})
	out := make([]map[string]any, 0, len(all))
	for _, t := range all {
		if !t.Activated || !t.Dispatchable {
			continue
		}
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return out
}

// mcpToolCall dispatches one tool through the registry. Authorization failures
// (unactivated) map to HTTP 403 and payment failures to 402 — the SAME contract the
// rest of the plane uses — while an unknown tool / runtime error stays a JSON-RPC
// error object (HTTP 200), the MCP convention. One metered unit + one audit record.
func mcpToolCall(s *cloud.Service[state], c *zip.Ctx, p Principal, req mcpRequest) error {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	body, _ := json.Marshal(req.Params)
	_ = json.Unmarshal(body, &params)
	if params.Name == "" {
		return c.JSON(http.StatusOK, rpcError(req.ID, -32602, "params.name is required"))
	}

	out, err := Default().Dispatch(c.Context(), p, params.Name, params.Arguments)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownTool):
			return c.JSON(http.StatusOK, rpcError(req.ID, -32601, "unknown tool: "+params.Name))
		case errors.Is(err, ErrNotActivated):
			audrecord(s, c, p.Org, params.Name, "denied", http.StatusForbidden)
			return zip.ErrForbidden("tool not activated for this org/project: " + params.Name)
		case errors.Is(err, ErrNotDispatchable):
			return zip.Errorf(http.StatusUnprocessableEntity, "tool is not dispatchable: %s", params.Name)
		case errors.Is(err, ErrPaymentRequired), errors.Is(err, ErrChargerUnset):
			audrecord(s, c, p.Org, params.Name, "payment_required", http.StatusPaymentRequired)
			return zip.Errorf(http.StatusPaymentRequired, "payment required for tool: %s", params.Name)
		default:
			audrecord(s, c, p.Org, params.Name, "error", http.StatusFailedDependency)
			return c.JSON(http.StatusOK, rpcError(req.ID, -32000, err.Error()))
		}
	}
	meterUnit(s, c)
	audrecord(s, c, p.Org, params.Name, "ok", http.StatusOK)
	text, _ := json.Marshal(out)
	return c.JSON(http.StatusOK, rpcResult(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(text)}},
	}))
}

// ── activation API (task 5) ─────────────────────────────────────────────────────

func getActivation(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	enabled, err := s.State.activation.List(c.Context(), org, principal.Project(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list activation: %v", err)
	}
	if enabled == nil {
		enabled = []string{}
	}
	return c.JSON(http.StatusOK, map[string]any{"enabled": enabled})
}

type activationReq struct {
	Activate   []string `json:"activate"`
	Deactivate []string `json:"deactivate"`
}

// putActivation applies a batch of toggles for (org, project) — the ONE write path
// hanzo.chat / hanzo.app calls to turn skills/plugins/connectors on and off.
func putActivation(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	project := principal.Project(c)
	var body activationReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if len(body.Activate)+len(body.Deactivate) > maxBatch {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "too many toggles (max %d)", maxBatch)
	}
	for _, name := range body.Activate {
		if !validToolName(name) {
			return zip.ErrBadRequest("invalid tool name: " + name)
		}
		if err := s.State.activation.Activate(c.Context(), org, project, name, "", c.User()); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "activate: %v", err)
		}
	}
	for _, name := range body.Deactivate {
		if err := s.State.activation.Deactivate(c.Context(), org, project, name); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "deactivate: %v", err)
		}
	}
	enabled, err := s.State.activation.List(c.Context(), org, project)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list activation: %v", err)
	}
	if enabled == nil {
		enabled = []string{}
	}
	audrecord(s, c, org, "activation", "ok", http.StatusOK)
	return c.JSON(http.StatusOK, map[string]any{"enabled": enabled})
}

// ── external MCP servers (task 2) ───────────────────────────────────────────────

func listServers(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	servers, err := s.State.servers.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list servers: %v", err)
	}
	if servers == nil {
		servers = []MCPServer{}
	}
	return c.JSON(http.StatusOK, map[string]any{"servers": servers})
}

type createServerReq struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	AuthHeader string `json:"authHeader"`
	Secret     string `json:"secret"`
}

// createServer registers an org's external MCP server. The auth secret VALUE is
// sealed in KMS (per-org ref); SQLite keeps only the URL + header name + a
// has-secret flag. The URL is SSRF-validated at the boundary; the dialer re-checks
// at connect time (DNS-rebinding defense).
func createServer(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body createServerReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	body.Name = strings.TrimSpace(body.Name)
	body.URL = strings.TrimSpace(body.URL)
	if body.Name == "" || len(body.Name) > maxName {
		return zip.ErrBadRequest("name is required (<=128 chars)")
	}
	if len(body.URL) > maxURL {
		return zip.ErrBadRequest("url too long")
	}
	if err := validateServerURL(body.URL); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	hasSecret := body.Secret != ""
	if hasSecret && s.State.kms == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "KMS not configured; refusing to store an MCP server secret")
	}
	created, err := s.State.servers.Create(c.Context(), MCPServer{
		Org: org, Name: body.Name, URL: body.URL, AuthHeader: strings.TrimSpace(body.AuthHeader), HasSecret: hasSecret,
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create server: %v", err)
	}
	if hasSecret {
		if err := s.State.kms.PutSecret(c.Context(), authRef(org, created.ID), []byte(body.Secret)); err != nil {
			_, _ = s.State.servers.Delete(c.Context(), org, created.ID)
			return zip.Errorf(http.StatusInternalServerError, "seal server secret: %v", err)
		}
	}
	audrecord(s, c, org, "server:"+created.ID, "created", http.StatusCreated)
	return c.JSON(http.StatusCreated, created)
}

func deleteServer(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := strings.TrimSpace(c.Param("id"))
	removed, err := s.State.servers.Delete(c.Context(), org, id)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete server: %v", err)
	}
	if !removed {
		return zip.ErrNotFound("server not found")
	}
	audrecord(s, c, org, "server:"+id, "deleted", http.StatusOK)
	return c.NoContent(http.StatusNoContent)
}

// ── shared helpers ──────────────────────────────────────────────────────────────

func rpcResult(id, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func rpcError(id any, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

// validToolName bounds an activation target: the flat tool-name shape every source
// emits ([a-z0-9._:/-] plus "_"), so a hostile name can't become a store-key trick.
func validToolName(name string) bool {
	if name == "" || len(name) > maxName {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == ':', r == '/':
		default:
			return false
		}
	}
	return true
}

func meterUnit(s *cloud.Service[state], c *zip.Ctx) {
	s.Bill.Meter(principal.Ledger(c), principal.Project(c), meterKind,
		cloud.ResourceFeeCents(feeEnvPrefix, meterKind), c.RequestID(), cloud.ClientIP(c))
}

// audrecord appends one audit record. Nil recorder → no-op.
func audrecord(s *cloud.Service[state], c *zip.Ctx, org, resourceID, result string, status int) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    "tools.call",
		Resource:  audit.Resource{Type: "tools", ID: resourceID},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: c.IsAdmin()},
		Outcome:   audit.Outcome{Result: result, Status: status},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  cloud.ClientIP(c),
		RequestID: c.RequestID(),
	}
	if _, err := s.State.audit.Append(c.Context(), rec); err != nil {
		s.Log.Warn("audit append failed", "err", err)
	}
}
