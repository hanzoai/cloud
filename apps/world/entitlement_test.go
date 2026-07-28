package world

import (
	"context"
	"encoding/json"
	"testing"
)

// TestWorldLimitsFromEntitlements pins the plan-key -> limits contract. Inputs
// mirror the world.* entitlement blocks the @hanzo/plans catalog derives for each
// tier; a missing key must keep the Free-floor value (access only narrows).
func TestWorldLimitsFromEntitlements(t *testing.T) {
	cases := []struct {
		name string
		ent  map[string]any
		want WorldLimits
	}{
		{
			name: "world-free: reads only, model denied",
			ent:  map[string]any{"world.api_rate_limit": 60.0, "world.mcp_rate_limit": 30.0, "world.max_alerts": 3.0},
			want: WorldLimits{APIRateLimit: 60, MCPRateLimit: 30, MaxAlerts: 3, ModelAPI: false},
		},
		{
			name: "world-pro: model granted",
			ent:  map[string]any{"world.api_rate_limit": 6000.0, "world.mcp_rate_limit": 3000.0, "world.max_alerts": -1.0, "world.model_api": true},
			want: WorldLimits{APIRateLimit: 6000, MCPRateLimit: 3000, MaxAlerts: -1, ModelAPI: true},
		},
		{
			name: "world-enterprise: all unlimited, model granted",
			ent:  map[string]any{"world.api_rate_limit": -1.0, "world.mcp_rate_limit": -1.0, "world.max_alerts": -1.0, "world.model_api": true},
			want: WorldLimits{APIRateLimit: -1, MCPRateLimit: -1, MaxAlerts: -1, ModelAPI: true},
		},
		{
			name: "empty entitlements -> free floor",
			ent:  map[string]any{},
			want: FreeWorldLimits,
		},
		{
			name: "explicit model_api:false is honored",
			ent:  map[string]any{"world.model_api": false, "world.api_rate_limit": 6000.0},
			want: WorldLimits{APIRateLimit: 6000, MCPRateLimit: FreeWorldLimits.MCPRateLimit, MaxAlerts: FreeWorldLimits.MaxAlerts, ModelAPI: false},
		},
		{
			name: "json.Number values coerce",
			ent:  map[string]any{"world.api_rate_limit": json.Number("6000"), "world.model_api": true},
			want: WorldLimits{APIRateLimit: 6000, MCPRateLimit: FreeWorldLimits.MCPRateLimit, MaxAlerts: FreeWorldLimits.MaxAlerts, ModelAPI: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WorldLimitsFromEntitlements(tc.ent)
			if got != tc.want {
				t.Fatalf("WorldLimitsFromEntitlements = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestResolveWorldLimits_FailsClosed verifies that when the plans subsystem is
// not mounted (host nil in this test binary), resolution returns the Free floor
// AND a non-nil error — a catalog outage degrades to Free, never above, and never
// silently grants the model API.
func TestResolveWorldLimits_FailsClosed(t *testing.T) {
	got, err := ResolveWorldLimits(context.Background(), "world-pro")
	if err == nil {
		t.Fatal("expected error when plans subsystem unmounted, got nil")
	}
	if got != FreeWorldLimits {
		t.Fatalf("expected Free floor on resolution failure, got %+v", got)
	}
	if got.ModelAPI {
		t.Fatal("fail-closed violated: model API granted on resolution failure")
	}
}

func TestEntInt(t *testing.T) {
	if v, ok := entInt(6000.0); !ok || v != 6000 {
		t.Fatalf("float64: got (%d,%v)", v, ok)
	}
	if _, ok := entInt("nope"); ok {
		t.Fatal("string must not coerce")
	}
	if _, ok := entInt(nil); ok {
		t.Fatal("nil must not coerce")
	}
}
