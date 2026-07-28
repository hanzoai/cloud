package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

// invoke.go decomplects tool dispatch from its front doors. ONE core (dispatchTool)
// resolves a tool name to its connector action and runs it with a RunContext whose
// credential Token is pinned to the VALIDATED org; TWO doors call it:
//
//   - mcpToolCall — the HTTP JSON-RPC door (POST /v1/automations/mcp tools/call),
//     metering + auditing with the request context.
//   - InvokeTool  — the in-process door a sibling subsystem (the Business AI guide)
//     uses to act through the per-principal MCP plane without an HTTP hop, metering
//     + auditing with no HTTP context.
//
// Both doors share the SAME dispatch, per-org concurrency bound, credential scope,
// meter (one unit), and audit record — so they can never diverge on what a tool
// does or on who is allowed to run it.

// Dispatch sentinels let each door map a failure onto its own error convention
// (the JSON-RPC door to -32601/-32005, the in-process door to a returned error)
// without duplicating the resolve/limit/run logic.
var (
	errUnknownTool = errors.New("unknown tool")
	errToolBusy    = errors.New("too many concurrent tool calls for this org")
	// ErrNotMounted is returned by InvokeTool before the subsystem has mounted.
	ErrNotMounted = errors.New("automations: not mounted")
)

// dispatchTool is the ONE tool-execution core. It resolves name → (connector,
// action), bounds per-org concurrency, and runs the action with a RunContext whose
// Token is bound to (org, connector) — so a caller can reach no other tenant's and
// no other provider's secret. org MUST be the caller's VALIDATED principal.
func dispatchTool(ctx context.Context, org, name string, args map[string]any) (any, error) {
	connector, action, ok := resolveTool(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errUnknownTool, name)
	}
	_, act, err := lookupAction(connector, action)
	if err != nil {
		return nil, err
	}
	if !orgRunLimiter.acquire(org) {
		return nil, errToolBusy
	}
	defer orgRunLimiter.release(org)
	return act.Run(ctx, RunContext{
		Org:   org,
		Input: args,
		Token: func(secretName string) ([]byte, error) {
			return tokenSource(ctx, org, connector, secretName)
		},
	})
}

// mcpToolCall is the HTTP JSON-RPC door: it dispatches a tool through the shared
// core and shapes the result/error as JSON-RPC. RunContext is pinned to the
// VALIDATED org by dispatchTool, so a caller can never invoke a tool against
// another tenant's credentials. One metered unit + one audit record per successful
// call; a failed/blocked call is audited as an error and NOT billed.
func mcpToolCall(s *cloud.Service[state], c *zip.Ctx, org string, req mcpRequest) error {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	body, _ := json.Marshal(req.Params)
	_ = json.Unmarshal(body, &p)

	out, err := dispatchTool(c.Context(), org, p.Name, p.Arguments)
	if err != nil {
		switch {
		case errors.Is(err, errUnknownTool):
			return c.JSON(http.StatusOK, mcpErrorObj(req.ID, -32601, err.Error()))
		case errors.Is(err, errToolBusy):
			return c.JSON(http.StatusOK, mcpErrorObj(req.ID, -32005, err.Error()))
		default:
			auditEvent(s, c, org, "automations.mcp.call", p.Name, "error", http.StatusFailedDependency)
			return c.JSON(http.StatusOK, mcpErrorObj(req.ID, -32000, err.Error()))
		}
	}
	meterUnit(s, org, c)
	auditEvent(s, c, org, "automations.mcp.call", p.Name, "ok", http.StatusOK)

	text, _ := json.Marshal(out)
	return c.JSON(http.StatusOK, mcpResultObj(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(text)}},
	}))
}

// InvokeTool runs a single MCP tool as principal `org`, in-process — the same
// dispatch, credential scope, per-org concurrency bound, metering, and audit as
// POST /v1/automations/mcp tools/call, minus the HTTP hop. It is the seam a sibling
// subsystem (the Business AI guide) uses to act through the per-principal MCP
// plane. `org` MUST be the caller's VALIDATED principal.Org: the dispatch pins
// every credential and effect to it, so an in-process caller can never exceed that
// org's authority. Returns ErrNotMounted before the subsystem has mounted.
func InvokeTool(ctx context.Context, org, name string, args map[string]any) (any, error) {
	if mounted == nil {
		return nil, ErrNotMounted
	}
	if org == "" || !validOrg(org) {
		return nil, fmt.Errorf("a validated org is required")
	}
	out, err := dispatchTool(ctx, org, name, args)
	if err != nil {
		auditToolCall(mounted, ctx, org, name, "error", http.StatusFailedDependency)
		return nil, err
	}
	meterRun(mounted, org)
	auditToolCall(mounted, ctx, org, name, "ok", http.StatusOK)
	return out, nil
}

// ToolExists reports whether name resolves to a registered "<connector>_<action>"
// tool — the cheap pre-check a caller uses to give an honest error before dispatch.
func ToolExists(name string) bool {
	_, _, ok := resolveTool(name)
	return ok
}

// auditToolCall appends the MCP tool-call audit record from the in-process door (no
// HTTP context, so no actor sub/email/ip — the org is the attributable actor).
// Nil recorder → no-op. Mirrors auditRun.
func auditToolCall(s *cloud.Service[state], ctx context.Context, org, tool, result string, status int) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: org},
		Action:   "automations.mcp.call",
		Resource: audit.Resource{Type: "automations", ID: tool},
		Auth:     audit.AuthContext{Method: "in-process"},
		Outcome:  audit.Outcome{Result: result, Status: status},
	}
	if _, err := s.State.audit.Append(ctx, rec); err != nil {
		s.Log.Warn("audit append failed", "err", err, "action", "automations.mcp.call")
	}
}
