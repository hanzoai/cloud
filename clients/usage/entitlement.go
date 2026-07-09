// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Analytics entitlement contract.
//
// This file is the ONE place cloud maps a Hanzo plan to the access that gates the
// RICH per-org usage-analytics surface. It does NOT introduce a second datastore
// and does NOT touch the metering WRITE path (the hanzoai/ai router still writes
// every AI call into the ONE global hanzo.cloud_usage ledger; the org column is the
// row-level tenant filter). It gates who may READ the rich lens over that ledger.
//
// The split it enforces:
//   - BASIC own-org usage (GET /v1/usage/summary) is ALWAYS allowed — every tenant
//     can read its own footprint. Never gated here.
//   - RICH per-org analytics/insights (retention window, exports, per-provider/BYO/
//     fee breakdowns — GET /v1/usage/analytics) is a PAID entitlement, gated here.
//
// The values are NOT hardcoded: they are resolved from the @hanzo/plans catalog
// (the single source of truth) via the canonical `analytics.*` entitlement
// vocabulary, so the numbers live in exactly one place and are read, never
// duplicated.
//
// Contract (keys to add to hanzoai/plans entitlements.schema.json):
//
//	analytics.datastore       boolean  access to the rich per-org analytics surface
//	analytics.retention_days  int      how far back the tenant may query
//	analytics.export          boolean  data export allowed
//
// FOLLOW-UP (documented, coordinated — not built here). Per-request ENFORCEMENT
// needs the caller's effective plan id (org -> plan), which is a subscription
// lookup owned by the billing plane (commerce /v1/billing/subscriptions); the
// gateway principal carries org/project but NO plan claim today, and no org->plan
// resolver exists in clients/plan or clients/billing yet. Until it lands,
// ResolveAnalyticsAccess takes an explicit plan id and GET /v1/usage/analytics/access
// echoes any plan's access so dashboards self-config against the live catalog, and
// GET /v1/usage/analytics reads ?plan= for its gate. Wiring the resolver into the
// handler (swap the ?plan= line for the resolved caller-org plan) is the rollout
// step — the gate logic is unchanged by it.
package usage

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/clients/plan"
)

// AnalyticsAccess is the resolved analytics entitlement for one caller. Datastore
// grants the rich per-org analytics/datastore surface; RetentionDays caps how far
// back the tenant may query; Export allows data export.
type AnalyticsAccess struct {
	Datastore     bool // analytics.datastore
	RetentionDays int  // analytics.retention_days
	Export        bool // analytics.export
}

// FreeAnalyticsAccess is the fail-closed floor: basic own-org usage only — no rich
// datastore surface, a 7-day query window, no export. It is applied whenever no
// plan can be resolved (unknown plan, or the catalog is unreachable), so a catalog
// outage degrades to exactly Free — never above.
var FreeAnalyticsAccess = AnalyticsAccess{Datastore: false, RetentionDays: 7, Export: false}

// AnalyticsAccessFromEntitlements maps a resolved `analytics.*` entitlement block
// onto AnalyticsAccess. Pure and total: any key absent from the block keeps the
// Free-floor value, so a partial catalog can only ever NARROW access (never widen
// past the floor). Wrong-typed values are ignored (kept at floor).
func AnalyticsAccessFromEntitlements(ent map[string]any) AnalyticsAccess {
	out := FreeAnalyticsAccess
	if b, ok := ent["analytics.datastore"].(bool); ok {
		out.Datastore = b
	}
	if v, ok := entInt(ent["analytics.retention_days"]); ok {
		out.RetentionDays = v
	}
	if b, ok := ent["analytics.export"].(bool); ok {
		out.Export = b
	}
	return out
}

// ResolveAnalyticsAccess resolves analytics access for a plan id from the
// @hanzo/plans catalog (the single source of truth). An empty id short-circuits to
// FreeAnalyticsAccess (the floor) WITHOUT a catalog round-trip, so an unauthenticated
// or plan-less caller fails closed and CI stays catalog-independent. On any
// resolution error it returns FreeAnalyticsAccess together with the error, so
// callers fail closed while logging the degradation.
func ResolveAnalyticsAccess(ctx context.Context, planID string) (AnalyticsAccess, error) {
	if strings.TrimSpace(planID) == "" {
		return FreeAnalyticsAccess, nil
	}
	ent, err := plan.Entitlements(ctx, planID)
	if err != nil {
		return FreeAnalyticsAccess, err
	}
	return AnalyticsAccessFromEntitlements(ent), nil
}

// entInt coerces an entitlement numeric value to int. Catalog values arrive as JSON
// numbers (float64) through the goja bundle; int/int64/json.Number are handled for
// direct callers. Returns false for absent or non-numeric values (mirrors
// clients/world's coercion — world's copy is unexported, so this local one keeps
// the usage package self-contained).
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
