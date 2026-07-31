package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

const (
	maxName  = 128
	maxURL   = 2048
	maxBatch = 256
)

// ── GET /v1/tools — discovery (all sources, activated flags) ────────────────────

// ToolFilter narrows discovery to one source and/or to the activated set.
type ToolFilter struct {
	// Source keeps only tools from that source (builtin, mcp, connector, ...).
	Source string `json:"source"`
	// Activated keeps only the tools activated for the caller's (org, project).
	Activated bool `json:"activated"`
}

// ToolList is the discovery answer.
type ToolList struct {
	// Tools is every tool matching the filter, from every source.
	Tools []Tool `json:"tools"`
}

// listTools returns every tool resolvable in the caller's (org, project) — from
// every source — each carrying whether it is activated, optionally narrowed to
// one source and/or to the activated set.
//
// Example: {"source": "mcp", "activated": true}
func (o ops) listTools(ctx context.Context, in *ToolFilter) (*ToolList, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	tools := Default().List(ctx, Scope{Org: p.Org, Project: p.Project})
	srcFilter := Source(strings.TrimSpace(in.Source))
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if srcFilter != "" && t.Source != srcFilter {
			continue
		}
		if in.Activated && !t.Activated {
			continue
		}
		out = append(out, t)
	}
	return &ToolList{Tools: out}, nil
}

// principalOf resolves the VALIDATED caller for a typed op off the request
// cloud.Bridge parked. It fails closed off the HTTP path, where there is no
// request and so no identity to run a tool as.
func principalOf(ctx context.Context) (Principal, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return Principal{}, zip.ErrForbidden("a validated principal is required")
	}
	p, ok := PrincipalFrom(c)
	if !ok {
		return Principal{}, zip.ErrForbidden("a validated principal is required")
	}
	return p, nil
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

// Activation is the set of tools switched on for a (org, project).
type Activation struct {
	// Enabled is every activated tool name; empty, never null, when none are.
	Enabled []string `json:"enabled"`
}

// getActivation returns the tool names activated for the caller's (org, project).
func (o ops) getActivation(ctx context.Context, _ *None) (*Activation, error) {
	s := o.s
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	enabled, err := s.State.activation.List(ctx, p.Org, p.Project)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list activation: %v", err)
	}
	if enabled == nil {
		enabled = []string{}
	}
	return &Activation{Enabled: enabled}, nil
}

// ActivationRequest is a batch of activation toggles.
type ActivationRequest struct {
	// Activate names the tools to switch on for the caller's (org, project).
	Activate []string `json:"activate"`
	// Deactivate names the tools to switch off.
	Deactivate []string `json:"deactivate"`
}

// putActivation applies a batch of toggles for the caller's (org, project) — the
// ONE write path that turns skills, plugins and connectors on and off — and
// returns the resulting activated set. At most 256 toggles per call.
//
// Example: {"activate": ["search"], "deactivate": ["shell"]}
// Response: {"enabled": ["search"]}
func (o ops) putActivation(ctx context.Context, in *ActivationRequest) (*Activation, error) {
	s := o.s
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	org, project := p.Org, p.Project
	if len(in.Activate)+len(in.Deactivate) > maxBatch {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "too many toggles (max %d)", maxBatch)
	}
	for _, name := range in.Activate {
		if !validToolName(name) {
			return nil, zip.ErrBadRequest("invalid tool name: " + name)
		}
		if err := s.State.activation.Activate(ctx, org, project, name, "", p.User); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "activate: %v", err)
		}
	}
	for _, name := range in.Deactivate {
		if err := s.State.activation.Deactivate(ctx, org, project, name); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "deactivate: %v", err)
		}
	}
	enabled, err := s.State.activation.List(ctx, org, project)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list activation: %v", err)
	}
	if enabled == nil {
		enabled = []string{}
	}
	audctx(s, ctx, org, "activation", "ok", http.StatusOK)
	return &Activation{Enabled: enabled}, nil
}

// ── external MCP servers (task 2) ───────────────────────────────────────────────

// ServerList is the org's registered external MCP servers.
type ServerList struct {
	// Servers is every server the caller's org registered; empty, never null.
	Servers []MCPServer `json:"servers"`
}

// listServers returns the external MCP servers the caller's org has registered.
// The auth secret is never returned — only whether one is sealed.
func (o ops) listServers(ctx context.Context, _ *None) (*ServerList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := s.State.servers.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list servers: %v", err)
	}
	if servers == nil {
		servers = []MCPServer{}
	}
	return &ServerList{Servers: servers}, nil
}

// CreateServerRequest registers one external MCP server for the caller's org.
type CreateServerRequest struct {
	// Name identifies the server, required, at most 128 characters.
	Name string `json:"name"`
	// URL is the server endpoint; it is SSRF-validated here and again at connect.
	URL string `json:"url"`
	// AuthHeader is the header name the secret is sent in.
	AuthHeader string `json:"authHeader"`
	// Secret is the auth secret VALUE. It is sealed in KMS and never stored in
	// SQLite nor returned by any read.
	Secret string `json:"secret"`
}

// createServer registers an org's external MCP server. The auth secret VALUE is
// sealed in KMS (per-org ref); SQLite keeps only the URL + header name + a
// has-secret flag. The URL is SSRF-validated at the boundary; the dialer re-checks
// at connect time (DNS-rebinding defense).
//
// Example: {"name": "acme-mcp", "url": "https://mcp.acme.com/v1", "authHeader": "Authorization", "secret": "Bearer s3cr3t"}
func (o ops) createServer(ctx context.Context, in *CreateServerRequest) (*MCPServer, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	url := strings.TrimSpace(in.URL)
	if name == "" || len(name) > maxName {
		return nil, zip.ErrBadRequest("name is required (<=128 chars)")
	}
	if len(url) > maxURL {
		return nil, zip.ErrBadRequest("url too long")
	}
	if err := validateServerURL(url); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	hasSecret := in.Secret != ""
	if hasSecret && s.State.kms == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "KMS not configured; refusing to store an MCP server secret")
	}
	created, err := s.State.servers.Create(ctx, MCPServer{
		Org: org, Name: name, URL: url, AuthHeader: strings.TrimSpace(in.AuthHeader), HasSecret: hasSecret,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create server: %v", err)
	}
	if hasSecret {
		if err := s.State.kms.PutSecret(ctx, authRef(org, created.ID), []byte(in.Secret)); err != nil {
			_, _ = s.State.servers.Delete(ctx, org, created.ID)
			return nil, zip.Errorf(http.StatusInternalServerError, "seal server secret: %v", err)
		}
	}
	audctx(s, ctx, org, "server:"+created.ID, "created", http.StatusCreated)
	return &created, nil
}

// ServerRef addresses one of the caller org's registered MCP servers.
type ServerRef struct {
	// ID is the server id from the path, as returned by create.
	ID string `json:"id"`
}

// deleteServer removes one of the caller org's external MCP servers and answers
// 204. A server belonging to another org reads as not found.
//
// Example: {"id": "srv_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) deleteServer(ctx context.Context, in *ServerRef) (*struct{}, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	removed, err := s.State.servers.Delete(ctx, org, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete server: %v", err)
	}
	if !removed {
		return nil, zip.ErrNotFound("server not found")
	}
	audctx(s, ctx, org, "server:"+id, "deleted", http.StatusOK)
	return nil, nil
}

// tenant resolves the org — the tenant-isolation KEY — that cloud.Bridge carried
// across the typed seam from the validated IAM owner claim. Never an In field: an
// In field is what the caller says about itself.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
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

// audctx is audrecord for a typed op: it takes the request back off the context
// cloud.Bridge parked it in, and is a no-op off the HTTP path.
func audctx(s *cloud.Service[state], ctx context.Context, org, resourceID, result string, status int) {
	if c, ok := cloud.Request(ctx); ok {
		audrecord(s, c, org, resourceID, result, status)
	}
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
