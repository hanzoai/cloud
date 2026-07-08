// World plan enforcement contract.
//
// This file is the ONE place cloud maps a Hanzo World plan to the limits that
// gate the /v1/world data plane. The values are NOT hardcoded here: they are
// resolved from the @hanzo/plans catalog (the single source of truth) via the
// canonical `world.*` entitlement vocabulary, so the numbers live in exactly one
// place — subscription.json in hanzoai/plans — and are read, never duplicated.
//
// Contract (keys defined in hanzoai/plans entitlements.schema.json):
//
//	world.api_rate_limit  requests/minute  REST /v1/world/*        (-1 = unlimited)
//	world.mcp_rate_limit  requests/minute  MCP surface             (-1 = unlimited)
//	world.max_alerts      count            saved OSINT alert rules (-1 = unlimited)
//	world.model_api       boolean          /v1/world/model + SSE stream gate
//
// Tier snapshot (sourced from the catalog, shown for reference only):
//
//	           api/min   mcp/min   alerts   model_api
//	world-free      60        30        3   denied
//	world-pro     6000      3000       -1   granted
//	world-team   30000     15000       -1   granted
//	world-enter     -1        -1       -1   granted (contact sales)
//
// ENFORCEMENT SEAM. Two callers consume this contract:
//   - this subsystem's /v1/world/{news,pipeline,stream} handlers (rate limiting +
//     the SSE model_api gate), and
//   - the /v1/world/model planet-scale engine (feat/world-model-engine), whose
//     pro-tier gate calls ResolveWorldLimits and checks WorldLimits.ModelAPI.
// Both MUST resolve through ResolveWorldLimits so the policy stays single-sourced.
//
// FOLLOW-UP (documented, coordinated — not built here). Per-request ENFORCEMENT
// needs the caller's effective plan id (org -> plan), which is a subscription
// lookup owned by the billing plane (commerce /v1/billing/subscriptions); the
// gateway principal carries org/project but no plan claim today. Until that
// org->plan resolver lands, ResolveWorldLimits takes an explicit plan id and
// GET /v1/world/limits echoes any plan's limits so agents/dashboards self-config
// against the live catalog. Wiring the rate limiter into the handlers is the
// rollout step, coordinated with the feat/world-model-engine gate to avoid two
// enforcement paths.
package world

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/clients/plan"
	"github.com/zap-proto/zip"
)

// WorldLimits is the resolved World plan contract for one caller: the enforcement
// inputs both the /v1/world handlers and the /v1/world/model gate read. Rate
// limits are requests/minute; -1 means unlimited.
type WorldLimits struct {
	APIRateLimit int  // world.api_rate_limit
	MCPRateLimit int  // world.mcp_rate_limit
	MaxAlerts    int  // world.max_alerts
	ModelAPI     bool // world.model_api — /v1/world/model + SSE stream access
}

// FreeWorldLimits is the fail-closed floor: the limits applied when no plan can
// be resolved (unknown plan, or the catalog is unreachable). It equals the
// world-free tier so a catalog outage degrades to exactly Free — never above,
// and never granting the model/stream API.
var FreeWorldLimits = WorldLimits{APIRateLimit: 60, MCPRateLimit: 30, MaxAlerts: 3, ModelAPI: false}

// WorldLimitsFromEntitlements maps a resolved `world.*` entitlement block onto
// WorldLimits. Pure and total: any key absent from the block keeps the Free-floor
// value, so a partial catalog can only ever narrow access. Exported so the
// /v1/world/model gate can derive limits from an already-resolved entitlement map
// without a second catalog round-trip.
func WorldLimitsFromEntitlements(ent map[string]any) WorldLimits {
	out := FreeWorldLimits
	if v, ok := entInt(ent["world.api_rate_limit"]); ok {
		out.APIRateLimit = v
	}
	if v, ok := entInt(ent["world.mcp_rate_limit"]); ok {
		out.MCPRateLimit = v
	}
	if v, ok := entInt(ent["world.max_alerts"]); ok {
		out.MaxAlerts = v
	}
	if b, ok := ent["world.model_api"].(bool); ok {
		out.ModelAPI = b
	}
	return out
}

// ResolveWorldLimits resolves the World limits for a plan id straight from the
// @hanzo/plans catalog. An empty id resolves the free tier. On any resolution
// error it returns FreeWorldLimits together with the error so callers can log the
// degradation while still failing closed.
func ResolveWorldLimits(ctx context.Context, planID string) (WorldLimits, error) {
	if strings.TrimSpace(planID) == "" {
		planID = "world-free"
	}
	ent, err := plan.Entitlements(ctx, planID)
	if err != nil {
		return FreeWorldLimits, err
	}
	return WorldLimitsFromEntitlements(ent), nil
}

// getLimits serves GET /v1/world/limits?plan=<id> — the machine-readable contract
// echo. It returns the resolved limits for the requested plan (default world-free)
// so agents and the dashboard configure themselves against the live catalog
// instead of hardcoding tier numbers. Always 200 with the fail-closed floor on a
// resolution error, so a catalog blip never breaks the client.
func (s *service) getLimits(c *zip.Ctx) error {
	planID := strings.TrimSpace(c.Query("plan"))
	if planID == "" {
		planID = "world-free"
	}
	lim, err := ResolveWorldLimits(c.Context(), planID)
	if err != nil {
		s.log.Warn("world limits resolve failed; serving free floor", "plan", planID, "err", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"plan": planID,
		"unit": "requests/minute",
		"limits": map[string]any{
			"apiRateLimit": lim.APIRateLimit,
			"mcpRateLimit": lim.MCPRateLimit,
			"maxAlerts":    lim.MaxAlerts,
			"modelApi":     lim.ModelAPI,
		},
	})
}

// entInt coerces an entitlement numeric value to int. Catalog values arrive as
// JSON numbers (float64) through the goja bundle; int and json.Number are handled
// for direct/other callers. Returns false for absent or non-numeric values.
func entInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}
