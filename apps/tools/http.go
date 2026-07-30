package tools

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

const (
	maxName  = 128
	maxURL   = 2048
	maxBatch = 256
)

// ── GET /v1/tools — discovery (all sources, activated flags) ────────────────────

// toolQuery narrows the discovery listing. Both fields are query parameters and
// both are optional.
//
// Activated is a STRING and not a bool on purpose: this route has always tested
// the raw query value against the literal "true", so `?activated=1` and a bare
// `?activated` have always meant "no filter". A bool field would make zip's
// binder read both as true, which is a different set of tools for the same URL.
type toolQuery struct {
	// Source keeps only tools from one source — connector, function, zap-service,
	// agent, skill or mcp. Empty keeps every source.
	Source string `json:"source"`
	// Activated keeps only the tools activated for the caller's org and project,
	// and only when it is exactly the string "true".
	Activated string `json:"activated"`
}

// toolList is a page of tools. It is never null: a caller with no tools gets an
// empty array.
type toolList struct {
	// Tools is every tool the caller may see, deduplicated by name with source
	// precedence applied.
	Tools []Tool `json:"tools"`
}

// ListTools lists every tool the caller's org and project can reach, from every
// source, each flagged with whether it is activated. This is the discovery
// surface: one flat set of names spanning connector actions, user functions,
// zap-service routes, agents, skills and the org's own external MCP servers,
// deduplicated by name so the highest-precedence source wins a collision. It
// lists; it does not call — dispatch is POST /v1/tools/call.
func (o toolOps) listTools(ctx context.Context, in *toolQuery) (*toolList, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	tools := Default().List(ctx, scope)
	srcFilter := Source(strings.TrimSpace(in.Source))
	activatedOnly := in.Activated == "true"
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
	return &toolList{Tools: out}, nil
}

// ── POST /v1/tools/call — the DYNAMIC half of the tool plane ───────────────────

// toolCall names a tool and the arguments to run it with.
type toolCall struct {
	// Name is the tool to run, exactly as GET /v1/tools reports it.
	Name string `json:"name"`
	// Arguments is the tool's own input object, passed through verbatim to
	// whichever source owns it.
	Arguments map[string]any `json:"arguments"`
}

// toolResult is what the tool returned.
type toolResult struct {
	// Name is the tool that ran.
	Name string `json:"name"`
	// Result is the tool's own output, verbatim — its shape is the tool's, not
	// this plane's.
	Result any `json:"result"`
}

// CallTool runs one of the caller's activated tools and answers with its output.
//
// This is the door onto the tool plane's DYNAMIC half — the half no build-time
// catalogue can hold, because it is per-tenant: an org's connected connector
// actions, its authored skills, its agents and functions, and the tools of every
// external MCP server it registered. A tool's existence, its price and its
// activation are all rows, not code, so they cannot be known until the caller is.
//
// One policy, the registry's: resolve by precedence, refuse an unactivated tool
// 403, settle a priced one through the x402 seam or fail closed 402, then
// dispatch to the winning source bound to the caller's own (org, project). One
// metered unit, one audit record. A caller can only ever dispatch its own tools.
//
// Discovery is GET /v1/tools — ?activated=true for the callable set.
//
// Example: {"name": "slack_post_message", "arguments": {"channel": "#general", "text": "hi"}}
func (o toolOps) callTool(ctx context.Context, in *toolCall) (*toolResult, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !validToolName(name) {
		return nil, zip.ErrBadRequest("name is required and must be a tool name")
	}
	out, err := Default().Dispatch(ctx, p, name, in.Arguments)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownTool):
			return nil, zip.ErrNotFound("unknown tool: " + name)
		case errors.Is(err, ErrNotActivated):
			o.audit(ctx, "tools.call", p.Org, name, "denied", http.StatusForbidden)
			return nil, zip.ErrForbidden("tool not activated for this org/project: " + name)
		case errors.Is(err, ErrNotDispatchable):
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "tool is not dispatchable: %s", name)
		case errors.Is(err, ErrPaymentRequired), errors.Is(err, ErrChargerUnset):
			o.audit(ctx, "tools.call", p.Org, name, "payment_required", http.StatusPaymentRequired)
			return nil, zip.Errorf(http.StatusPaymentRequired, "payment required for tool: %s", name)
		default:
			o.audit(ctx, "tools.call", p.Org, name, "error", http.StatusFailedDependency)
			return nil, zip.Errorf(http.StatusFailedDependency, "tool call failed: %v", err)
		}
	}
	o.meter(ctx)
	o.audit(ctx, "tools.call", p.Org, name, "ok", http.StatusOK)
	return &toolResult{Name: name, Result: out}, nil
}

// ── activation API (task 5) ─────────────────────────────────────────────────────

// activationSet is the activated-tool set for one (org, project). It is never
// null: a scope with nothing activated gets an empty array.
type activationSet struct {
	// Enabled is every tool name activated for the caller's org and project.
	Enabled []string `json:"enabled"`
}

// GetActivation reports which tools are switched on for the caller's org and
// project. Activation is what makes a tool dispatchable and what makes it visible
// to an agent, so this is the set the MCP tool list is drawn from — every other
// tool in the registry is discoverable but refused at call time.
func (o toolOps) getActivation(ctx context.Context, _ *noInput) (*activationSet, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	enabled, err := o.s.State.activation.List(ctx, scope.Org, scope.Project)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list activation: %v", err)
	}
	if enabled == nil {
		enabled = []string{}
	}
	return &activationSet{Enabled: enabled}, nil
}

// activationReq is a batch of activation toggles. Activate is applied first, so a
// name in both lists ends up deactivated.
type activationReq struct {
	// Activate switches these tool names on for the caller's org and project.
	Activate []string `json:"activate"`
	// Deactivate switches these tool names off.
	Deactivate []string `json:"deactivate"`
}

// PutActivation switches tools on and off for the caller's org and project, and
// answers with the resulting activated set. It is the ONE write path that turns
// skills, plugins and connectors into callable tools — an unactivated tool is
// listed by discovery but refused 403 at dispatch. Activate is applied before
// Deactivate, so a name in both lists ends up off. More than 256 toggles in one
// request is refused 413.
//
// Example: {"activate": ["cloud_get_ping"], "deactivate": []}
func (o toolOps) putActivation(ctx context.Context, in *activationReq) (*activationSet, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.Activate)+len(in.Deactivate) > maxBatch {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "too many toggles (max %d)", maxBatch)
	}
	actor := callerOf(ctx)
	for _, name := range in.Activate {
		if !validToolName(name) {
			return nil, zip.ErrBadRequest("invalid tool name: " + name)
		}
		if err := o.s.State.activation.Activate(ctx, scope.Org, scope.Project, name, "", actor); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "activate: %v", err)
		}
	}
	for _, name := range in.Deactivate {
		if err := o.s.State.activation.Deactivate(ctx, scope.Org, scope.Project, name); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "deactivate: %v", err)
		}
	}
	enabled, err := o.s.State.activation.List(ctx, scope.Org, scope.Project)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list activation: %v", err)
	}
	if enabled == nil {
		enabled = []string{}
	}
	o.audit(ctx, "tools.call", scope.Org, "activation", "ok", http.StatusOK)
	return &activationSet{Enabled: enabled}, nil
}

// ── external MCP servers (task 2) ───────────────────────────────────────────────

// mcpServerList is the caller org's registered external MCP servers. It is never
// null: an org with none gets an empty array.
type mcpServerList struct {
	// Servers is every external MCP server this org has registered. No secret
	// VALUE is ever included — only whether one is set.
	Servers []MCPServer `json:"servers"`
}

// ListServers lists the external MCP servers the caller's org has registered.
// Each record carries the URL and the name of the header its credential is
// injected into; the credential VALUE lives only in KMS and is never returned,
// so hasSecret is the whole of what this surface says about it.
func (o toolOps) listServers(ctx context.Context, _ *noInput) (*mcpServerList, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := o.s.State.servers.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list servers: %v", err)
	}
	if servers == nil {
		servers = []MCPServer{}
	}
	return &mcpServerList{Servers: servers}, nil
}

// createServerReq registers one external MCP server.
type createServerReq struct {
	// Name labels the server for the org. Required, at most 128 characters.
	Name string `json:"name"`
	// URL is the server's JSON-RPC endpoint. It must be an http(s) URL naming a
	// PUBLIC host: loopback, link-local, private and cloud-metadata addresses are
	// refused here and again when the dialer connects.
	URL string `json:"url"`
	// AuthHeader is the request header the credential is injected into, e.g.
	// "Authorization". Empty means the server needs no credential.
	AuthHeader string `json:"authHeader"`
	// Secret is the credential VALUE. It is sealed into KMS under a per-org ref
	// and never stored in SQLite, never listed, and never returned.
	Secret string `json:"secret"`
}

// CreateServer registers one of the caller org's own external MCP servers, so its
// tools join the unified registry and become activatable. The credential VALUE is
// sealed in KMS under a per-org ref; the row keeps only the URL, the header name
// to inject it into, and a has-secret flag — so a secret with no KMS configured
// is refused 503 rather than stored in the clear. The URL is SSRF-validated here
// and re-checked by the dialer at connect time, which is the DNS-rebinding
// defense. Answers 201 with the stored record.
//
// Example: {"name": "myserver", "url": "https://mcp.example.com/rpc", "authHeader": "Authorization", "secret": "Bearer …"}
func (o toolOps) createServer(ctx context.Context, in *createServerReq) (*MCPServer, error) {
	org, err := tenantOf(ctx)
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
	if hasSecret && o.s.State.kms == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "KMS not configured; refusing to store an MCP server secret")
	}
	created, err := o.s.State.servers.Create(ctx, MCPServer{
		Org: org, Name: name, URL: url, AuthHeader: strings.TrimSpace(in.AuthHeader), HasSecret: hasSecret,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create server: %v", err)
	}
	if hasSecret {
		if err := o.s.State.kms.PutSecret(ctx, authRef(org, created.ID), []byte(in.Secret)); err != nil {
			_, _ = o.s.State.servers.Delete(ctx, org, created.ID)
			return nil, zip.Errorf(http.StatusInternalServerError, "seal server secret: %v", err)
		}
	}
	o.audit(ctx, "tools.call", org, "server:"+created.ID, "created", http.StatusCreated)
	return &created, nil
}

// serverRef addresses one external MCP server. The id is the path segment: the
// URL is the addressing authority.
type serverRef struct {
	// ID is the server to deregister, from the path.
	ID string `json:"id"`
}

// DeleteServer deregisters one of the caller org's external MCP servers, so its
// tools leave the registry. Scoped to the caller's org, so an id belonging to
// another tenant is a 404 and not a delete. Answers 204 with no body; a server
// this org does not have is 404.
func (o toolOps) deleteServer(ctx context.Context, in *serverRef) (*noContent, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	removed, err := o.s.State.servers.Delete(ctx, org, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete server: %v", err)
	}
	if !removed {
		return nil, zip.ErrNotFound("server not found")
	}
	o.audit(ctx, "tools.call", org, "server:"+id, "deleted", http.StatusOK)
	return nil, nil
}

// ── shared helpers ──────────────────────────────────────────────────────────────

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

// audrecord appends one audit record for a tool call. Nil recorder → no-op.
func audrecord(s *cloud.Service[state], c *zip.Ctx, org, resourceID, result string, status int) {
	audrecordAction(s, c, "tools.call", org, resourceID, result, status)
}

// audrecordAction is audrecord with the action named: the plugin builder records
// plugin.build, which is a different act on a different resource than a call.
func audrecordAction(s *cloud.Service[state], c *zip.Ctx, action, org, resourceID, result string, status int) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    action,
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
