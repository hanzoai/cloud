package auto

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
)

// invoke.go decomplects tool dispatch from its callers. ONE core (dispatchTool)
// resolves a tool name to its connector action and runs it with a RunContext whose
// credential Token is pinned to the VALIDATED org.
//
// Two callers reach it, and neither is an HTTP endpoint of this subsystem's own:
//
//   - connectorToolProvider.Dispatch — the unified tool plane, which is how a
//     connector action is reached from POST /v1/tools/call and therefore from the
//     fleet's one agent MCP server.
//   - InvokeTool — the in-process client a sibling subsystem (the Business AI guide)
//     uses to act as an org without an HTTP hop, metering + auditing with no HTTP
//     context.
//
// Both share the SAME dispatch, per-org concurrency bound, credential scope, meter
// (one unit) and audit record — so they can never diverge on what a tool does or
// on who is allowed to run it.

// Dispatch sentinels let each caller map a failure onto its own error convention
// (the JSON-RPC one to -32601/-32005, the in-process one to a returned error)
// without duplicating the resolve/limit/run logic.
var (
	errUnknownTool = errors.New("unknown tool")
	errToolBusy    = errors.New("too many concurrent tool calls for this org")
	// ErrNotMounted is returned by InvokeTool before the subsystem has mounted.
	ErrNotMounted = errors.New("auto: not mounted")
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

// InvokeTool runs a single MCP tool as principal `org`, in-process — the same
// dispatch, credential scope, per-org concurrency bound, metering, and audit as
// POST /v1/tools/call, minus the HTTP hop. It is the client a sibling
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

// auditToolCall appends the tool-call audit record from the in-process client (no
// HTTP context, so no actor sub/email/ip — the org is the attributable actor).
// Nil recorder → no-op. Mirrors auditRun.
func auditToolCall(s *cloud.Service[state], ctx context.Context, org, tool, result string, status int) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: org},
		Action:   "automations.tool.call",
		Resource: audit.Resource{Type: "automations", ID: tool},
		Auth:     audit.AuthContext{Method: "in-process"},
		Outcome:  audit.Outcome{Result: result, Status: status},
	}
	if _, err := s.State.audit.Append(ctx, rec); err != nil {
		s.Log.Warn("audit append failed", "err", err, "action", "automations.tool.call")
	}
}
