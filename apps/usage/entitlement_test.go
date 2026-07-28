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

package usage

import (
	"context"
	"testing"
)

// TestAnalyticsAccessFromEntitlements pins the pure catalog->access mapping. No
// catalog, no I/O — plain maps in, exact struct out. The floor can only ever be
// narrowed by a partial or wrong-typed block.
func TestAnalyticsAccessFromEntitlements(t *testing.T) {
	tests := []struct {
		name string
		ent  map[string]any
		want AnalyticsAccess
	}{
		{
			name: "empty map keeps the fail-closed free floor",
			ent:  map[string]any{},
			want: FreeAnalyticsAccess, // {false, 7, false}
		},
		{
			name: "nil map keeps the fail-closed free floor",
			ent:  nil,
			want: FreeAnalyticsAccess,
		},
		{
			name: "full paid block grants everything",
			ent: map[string]any{
				"analytics.datastore":      true,
				"analytics.retention_days": 365,
				"analytics.export":         true,
			},
			want: AnalyticsAccess{Datastore: true, RetentionDays: 365, Export: true},
		},
		{
			name: "float64 retention (goja JSON number) coerces to int",
			ent: map[string]any{
				"analytics.datastore":      true,
				"analytics.retention_days": float64(90),
			},
			want: AnalyticsAccess{Datastore: true, RetentionDays: 90, Export: false},
		},
		{
			name: "partial block narrows only the present keys",
			ent: map[string]any{
				"analytics.datastore": true,
			},
			// retention + export stay at the floor.
			want: AnalyticsAccess{Datastore: true, RetentionDays: 7, Export: false},
		},
		{
			name: "wrong types are ignored, floor is kept",
			ent: map[string]any{
				"analytics.datastore":      "yes",      // not a bool
				"analytics.retention_days": "forever",  // not a number
				"analytics.export":         float64(1), // not a bool
			},
			want: FreeAnalyticsAccess,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AnalyticsAccessFromEntitlements(tt.ent)
			if got != tt.want {
				t.Fatalf("AnalyticsAccessFromEntitlements(%v) = %+v, want %+v", tt.ent, got, tt.want)
			}
		})
	}
}

// TestResolveAnalyticsAccess_EmptyPlanIsFreeFloor asserts the empty-plan short
// circuit: no catalog round-trip, exact free floor, nil error — the CI-safe,
// fail-closed default a plan-less caller resolves to.
func TestResolveAnalyticsAccess_EmptyPlanIsFreeFloor(t *testing.T) {
	got, err := ResolveAnalyticsAccess(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveAnalyticsAccess(\"\") error = %v, want nil", err)
	}
	if got != FreeAnalyticsAccess {
		t.Fatalf("ResolveAnalyticsAccess(\"\") = %+v, want %+v", got, FreeAnalyticsAccess)
	}
	// The floor MUST deny the paid surface and cap the window at 7 days.
	if got.Datastore || got.Export || got.RetentionDays != 7 {
		t.Fatalf("free floor is not fail-closed: %+v", got)
	}
}
