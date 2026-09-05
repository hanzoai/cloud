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

package ai

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"

	flagsplane "github.com/hanzoai/cloud/client/flag"
)

// cap.go is the gate's TRAILING-WINDOW spend ceiling: the burst limit that resets
// continuously, so usage older than the window drops out of the sum and there is no
// reset boundary to queue up against.
//
// It composes three things and owns none of them: the caller's plan tier, the
// ledger's windowed sum, and the per-tier ceilings, which are platform switches
// admin.hanzo.ai already renders and edits. It registers no route and opens no
// store.
//
// IT IS HERE BECAUSE THIS IS THE ONLY PROCESS IT CAN WORK IN. It was an app of its
// own — apps/rollingcap, a manifest row claiming /v1/rollingcap that nothing was
// registered behind — and being an app is precisely what made it dead. Every app in
// this fleet is its own child process, so `rollingcap` handed its verdict to the ai
// module by writing a package global in ITS child, which ai's child cannot see; and
// nothing ever ran it anyway, because a row with no route is a lazy child no request
// ever starts. The cap has therefore never applied to a completion. A policy belongs
// with the gate that asks it, not in a process that can only shout across a boundary.
//
// Both of the reads it needs already cross that boundary the way this package
// crosses it for everything else: cloud.TierReader is the co-resident commerce
// lookup ai already installs, and the windowed sum is commerce's own plane op — the
// one whose doc names this cap as its twin, so a program that qualifies an org on
// its spend and the gate that stops that org spending read one number.

// capWindow is the trailing window in hours; 0 turns the ceiling off everywhere.
const capWindow = "ai_rolling_window_hours"

// capFallback is the ceiling (US cents) for any caller whose tier names no ceiling
// of its own — pay-as-you-go, empty, and unseeded names alike. It closes the burst
// hole for callers with no subscription, who would otherwise be the only uncapped
// ones. 0 means opt-in: nothing changes until an admin sets it, and then every
// caller has a ceiling.
const capFallback = "ai_rolling_cap_cents_default"

// capSeed is each plan's ceiling in US cents, mirroring @hanzo/plans
// subscription.json (developer $0.75 / pro $2.50 / plus $12 / max $25). Both the
// commerce tier taxonomy (free/starter/pro/enterprise) and the plan ids
// (developer/pro/plus/max) are seeded, so whichever name commerce answers with
// resolves. An unseeded name reads 0 and falls to capFallback.
//
// These are the DEFAULTS the switch registry carries; the flag store is
// authoritative the moment an admin edits one.
// defaultWindowHours is the trailing window this ceiling sums over when no
// operator has set one. It is named because it is now stated twice — in the
// definition the board renders, and as the fallback the read carries.
const defaultWindowHours = 3

var capSeed = map[string]int{
	"free": 75, "developer": 75, "starter": 250,
	"pro": 250, "plus": 1200, "max": 2500, "enterprise": 0,
}

func capOf(tier string) string { return "ai_rolling_cap_cents_" + strings.ToLower(tier) }

func init() {
	flags.Register(flags.Def{
		Key: capWindow, Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(defaultWindowHours),
		Label: "AI rolling-cap window (hours)",
		Desc:  "Trailing window the per-plan AI-spend ceiling sums over; it resets continuously. 0 disables the ceiling globally.",
	})
	flags.Register(flags.Def{
		Key: capFallback, Category: "Gateway", Type: flags.TypeInt, Default: "0",
		Label: "AI rolling cap ¢ — default (all other tiers)",
		Desc:  "Fallback ceiling (US cents) within the window for any caller whose tier names none — pay-as-you-go, empty, or unknown. 0 = uncapped (opt-in).",
	})
	for tier, cents := range capSeed {
		flags.Register(flags.Def{
			Key: capOf(tier), Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(cents),
			Label: "AI rolling cap ¢ — " + tier,
			Desc:  "Ceiling (US cents) on a " + tier + " plan's AI spend within the rolling window. 0 = uncapped.",
		})
	}
}

// overCap reports whether this caller has already spent their plan's ceiling inside
// the trailing window. It is the ai module's rolling-cap hook.
//
// Every step that cannot be answered FAILS OPEN by returning the error rather than a
// verdict: the gate reads an error as admit, which is the right direction here
// because the caller has money and is paying — a commerce blip must never 429 them.
// The three quiet no-caps (window off, no ceiling for this plan, no fallback) return
// false with no error, which is the same admission for a different reason.
func overCap(ctx context.Context, subject, namespace string) (bool, error) {
	// ASKED OF THE APP THAT OWNS THE STORE. flags.Int read this process's own
	// registry, which only the flags plugin ever fills, so the ceiling ran on the
	// compiled 3 whatever an operator had set — the same shape as the rolling cap
	// that never reached a completion.
	window := flagsplane.Int(ctx, capWindow, defaultWindowHours)
	if window <= 0 {
		return false, nil
	}
	cents, err := capFor(ctx, subject, namespace)
	if err != nil || cents <= 0 {
		return false, err
	}
	spent, err := spentSince(ctx, namespace, time.Now().Add(-time.Duration(window)*time.Hour).Unix())
	if err != nil {
		return false, err
	}
	return spent >= int64(cents), nil
}

// capFor resolves this caller's ceiling in US cents: their plan's, else the
// fallback, else 0 for uncapped.
//
// A tier this deployment cannot look up is not a special case — it reads as the
// empty name, whose switch is unregistered and therefore 0, so it takes the same
// fallback an unseeded plan does. There is one path for "no ceiling of your own".
func capFor(ctx context.Context, subject, namespace string) (int, error) {
	tier := ""
	if read := cloud.TierReader(); read != nil {
		name, err := read(ctx, subject, namespace)
		if err != nil {
			return 0, err
		}
		tier = name
	}
	if cents := flagsplane.Int(ctx, capOf(tier), capSeed[tier]); cents > 0 {
		return cents, nil
	}
	return flagsplane.Int(ctx, capFallback, 0), nil
}

// spentSince asks the process that owns the ledger for the org's consumption since
// a moment, in US cents.
//
// It crosses the plane for the reason every money read in this package does: the
// prepaid ledger is a per-org SQLite store with ONE writer, and that writer is
// whichever process mounted commerce. Reading commerce's own windowed sum — rather
// than paging its debits and adding them up here — is also what keeps this ceiling
// and the credit programs that qualify on spend from disagreeing about the amount.
//
// The sum leaves the ledger already at cent precision, so taking the floor of it
// here restores the integer exactly; it is not a rounding.
func spentSince(ctx context.Context, org string, since int64) (int64, error) {
	out, err := commercepeer.FinanceSpend(cloud.For(ctx, org), &client.SpendIn{Since: since})
	if err != nil {
		return 0, fmt.Errorf("plane spend read: %w", err)
	}
	if out == nil {
		return 0, fmt.Errorf("plane spend read: commerce answered nothing")
	}
	return out.Consumed.FloorMinor()
}
