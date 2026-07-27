// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

// Package rollingcap installs the ai gate's rolling-window AI-spend cap — the
// Anthropic-style burst limit that resets continuously (usage older than the window
// drops out of the trailing sum, so there is no fixed reset boundary).
//
// It composes three co-resident GLOBALS and owns no state of its own:
//
//	aiobject.TierReader()  — the caller's commerce plan tier   (installed by apps/ai.go)
//	finance.Current()      — the per-org ledger's windowed sum  (published by cloud.BuildDeps)
//	flags.Int(key)         — the admin-editable per-tier caps    (the platform-switch registry)
//
// The two knobs are platform switches, so admin.hanzo.ai renders and edits them live
// through the existing /v1/admin/flags cockpit — this package adds NO bespoke admin
// UI. Mounting registers no routes; it only installs the hook. It sits in its own
// package (not the root cloud package) because it imports clients/flags, and flags
// imports the root cloud package — the wiring must live above that edge.
package rollingcap

import (
	"context"
	"strconv"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/flags"
)

// capWindowKey is the trailing-window size (hours); 0 disables the cap globally.
const capWindowKey = "ai_rolling_window_hours"

// capDefaultKey is the FALLBACK cap (cents) applied to any caller whose plan tier has
// no specific cap set — including pay-as-you-go, empty/unknown tiers, and unseeded
// names. It closes the burst-spend hole for non-subscription users (a pay-as-you-go
// caller would otherwise be uncapped). Default 0 = opt-in: no behavior change until an
// admin sets it in the cockpit, at which point EVERY caller gets a rolling ceiling.
const capDefaultKey = "ai_rolling_cap_cents_default"

// capSeed maps a plan tier to its rolling-cap default in US cents, mirroring
// @hanzo/plans subscription.json (developer $0.75 / pro $2.50 / plus $12 / max $25).
// Both the commerce tier taxonomy (free/starter/pro/enterprise) and the plan ids
// (developer/pro/plus/max) are seeded, so whichever name commerce returns resolves;
// an unseeded tier reads 0 = uncapped, i.e. fail-open, consistent with the gate.
// These are the runtime DEFAULTS; the flag store is authoritative once an admin edits.
var capSeed = map[string]int{
	"free": 75, "developer": 75, "starter": 250,
	"pro": 250, "plus": 1200, "max": 2500, "enterprise": 0,
}

func capKey(tier string) string { return "ai_rolling_cap_cents_" + strings.ToLower(tier) }

func init() {
	flags.Register(flags.Def{
		Key: capWindowKey, Category: "Gateway", Type: flags.TypeInt, Default: "3",
		Label: "AI rolling-cap window (hours)",
		Desc:  "Trailing window the per-plan AI-spend cap sums over (Anthropic-style; resets continuously). 0 disables the cap globally.",
	})
	flags.Register(flags.Def{
		Key: capDefaultKey, Category: "Gateway", Type: flags.TypeInt, Default: "0",
		Label: "AI rolling cap ¢ — default (all other tiers)",
		Desc:  "Fallback max AI spend (US cents) within the window for any caller whose tier has no specific cap — pay-as-you-go, empty, or unknown. 0 = uncapped (opt-in).",
	})
	for tier, cents := range capSeed {
		flags.Register(flags.Def{
			Key: capKey(tier), Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(cents),
			Label: "AI rolling cap ¢ — " + tier,
			Desc:  "Max AI spend (US cents) for a " + tier + " plan within the rolling window. 0 = uncapped.",
		})
	}
}

// Mount installs the rolling-cap reader. It registers no routes — the platform
// switches (registered in init) surface in the admin cockpit on their own. No-op when
// the tier or finance layer is not co-resident (standalone / split deploy): ai's hook
// stays nil, behavior unchanged. Runs after MountAll's prerequisites — the composition
// root's installBilling has already installed the globals it reads.
func Mount(_ *zip.App, _ cloud.Deps) error {
	tier := aiobject.TierReader()
	fin := finance.Current()
	if tier == nil || fin == nil {
		return nil // money/tier layer not co-resident → no rolling cap
	}
	aiobject.SetRollingCapReader(func(ctx context.Context, subject, namespace string) (bool, error) {
		window := flags.Int(capWindowKey)
		if window <= 0 {
			return false, nil // cap disabled globally
		}
		name, err := tier(ctx, subject, namespace)
		if err != nil {
			return false, err // fail open — a commerce blip must not 429 a paying caller
		}
		capCents := flags.Int(capKey(name))
		if capCents <= 0 {
			// No tier-specific cap (pay-as-you-go / empty / unseeded name) → the
			// default fallback closes the hole for non-subscription callers.
			capCents = flags.Int(capDefaultKey)
		}
		if capCents <= 0 {
			return false, nil // neither a tier cap nor a default → uncapped
		}
		since := time.Now().Add(-time.Duration(window) * time.Hour).Unix()
		spent, err := fin.SumUsageSince(ctx, namespace, false, since)
		if err != nil {
			return false, err // fail open
		}
		return spent >= int64(capCents), nil
	})
	return nil
}
