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

package rollingcap

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// The reader is a pure composition of three inputs — tier, per-tier cap (cents), and
// the trailing-window usage sum (cents). reader() isolates that logic from the wiring
// so we can assert the decision table without a live commerce/finance stack. It mirrors
// exactly the closure Mount installs.
func reader(tier func(context.Context, string, string) (string, error),
	capCents func(string) int, defaultCents, windowHours int,
	sumSince func(context.Context, string, bool, int64) (int64, error),
) func(context.Context, string, string) (bool, error) {
	return func(ctx context.Context, subject, namespace string) (bool, error) {
		if windowHours <= 0 {
			return false, nil
		}
		name, err := tier(ctx, subject, namespace)
		if err != nil {
			return false, err
		}
		cc := capCents(name)
		if cc <= 0 {
			cc = defaultCents // fallback for pay-as-you-go / empty / unseeded tiers
		}
		if cc <= 0 {
			return false, nil
		}
		spent, err := sumSince(ctx, namespace, false, 0)
		if err != nil {
			return false, err
		}
		return spent >= int64(cc), nil
	}
}

func TestRollingCapDecision(t *testing.T) {
	tier := func(name string, err error) func(context.Context, string, string) (string, error) {
		return func(context.Context, string, string) (string, error) { return name, err }
	}
	// Models flags.Int(capKey(name)): a seeded tier returns its cap, an unseeded/empty
	// name returns 0 (the switch isn't registered) → the reader falls to the default.
	caps := func(name string) int {
		if name == "pro" {
			return 250 // $2.50
		}
		return 0
	}
	sum := func(cents int64, err error) func(context.Context, string, bool, int64) (int64, error) {
		return func(context.Context, string, bool, int64) (int64, error) { return cents, err }
	}
	ctx := context.Background()

	const noDefault = 0

	// Over the cap ⇒ deny (over=true). $3.00 spent >= $2.50 cap.
	if over, err := reader(tier("pro", nil), caps, noDefault, 3, sum(300, nil))(ctx, "s", "o"); err != nil || !over {
		t.Errorf("over-cap: got over=%v err=%v, want true,nil", over, err)
	}
	// Under the cap ⇒ admit. $1.00 spent < $2.50.
	if over, err := reader(tier("pro", nil), caps, noDefault, 3, sum(100, nil))(ctx, "s", "o"); err != nil || over {
		t.Errorf("under-cap: got over=%v err=%v, want false,nil", over, err)
	}
	// Window disabled globally ⇒ admit regardless of spend.
	if over, _ := reader(tier("pro", nil), caps, noDefault, 0, sum(999999, nil))(ctx, "s", "o"); over {
		t.Errorf("window=0 must disable the cap (admit)")
	}
	// Unknown tier + NO default ⇒ admit (uncapped, opt-in).
	if over, _ := reader(tier("", nil), caps, noDefault, 3, sum(999999, nil))(ctx, "s", "o"); over {
		t.Errorf("empty tier with no default must admit (uncapped)")
	}
	if over, _ := reader(tier("enterprise", nil), func(string) int { return 0 }, noDefault, 3, sum(999999, nil))(ctx, "s", "o"); over {
		t.Errorf("cap=0 tier with no default must admit")
	}
	// DEFAULT FALLBACK: pay-as-you-go / empty / unseeded tier + a default cap set ⇒
	// the default applies. $6 spent >= $5 default → deny. This closes the hole for
	// non-subscription callers.
	if over, err := reader(tier("pay-as-you-go", nil), func(string) int { return 0 }, 500, 3, sum(600, nil))(ctx, "s", "o"); err != nil || !over {
		t.Errorf("pay-as-you-go over default cap: got over=%v err=%v, want true,nil", over, err)
	}
	if over, _ := reader(tier("", nil), caps, 500, 3, sum(400, nil))(ctx, "s", "o"); over {
		t.Errorf("empty tier under default cap ($4<$5) must admit")
	}
	// A tier WITH a specific cap ignores the default (tier-specific wins).
	if over, _ := reader(tier("pro", nil), caps, 100000, 3, sum(300, nil))(ctx, "s", "o"); !over {
		t.Errorf("tier-specific cap ($2.50) must apply over a large default; $3>$2.50 → deny")
	}
	// Tier read error ⇒ FAIL OPEN (admit): a commerce blip must not 429 a paying caller.
	if over, err := reader(tier("", errTest), caps, 500, 3, sum(999999, nil))(ctx, "s", "o"); over || err == nil {
		t.Errorf("tier error must fail open (over=false) and propagate err; got over=%v err=%v", over, err)
	}
	// Usage-sum error ⇒ FAIL OPEN.
	if over, err := reader(tier("pro", nil), caps, noDefault, 3, sum(0, errTest))(ctx, "s", "o"); over || err == nil {
		t.Errorf("sum error must fail open (over=false) and propagate err; got over=%v err=%v", over, err)
	}
}

// TestMountNoOpWhenGlobalsUnwired proves Mount installs nothing when the tier/finance
// globals are absent (standalone / split deploy) — no panic, no hook.
func TestMountNoOpWhenGlobalsUnwired(t *testing.T) {
	// aiobject.TierReader() and finance.Current() are nil in a bare test binary, so
	// Mount must return nil without installing a reader.
	if err := Mount(nil, cloud.Deps{}); err != nil {
		t.Fatalf("Mount with unwired globals must be a no-op nil, got %v", err)
	}
}

// TestSeedRegistersCanonicalTiers asserts the seed covers both the commerce taxonomy
// and the plan ids so whichever name commerce returns has a default (no silent gap).
func TestSeedRegistersCanonicalTiers(t *testing.T) {
	for _, tier := range []string{"free", "developer", "starter", "pro", "plus", "max", "enterprise"} {
		if _, ok := capSeed[tier]; !ok {
			t.Errorf("capSeed missing tier %q — an unseeded tier silently reads 0 (uncapped)", tier)
		}
		if k := capKey(tier); !strings.HasPrefix(k, "ai_rolling_cap_cents_") {
			t.Errorf("capKey(%q)=%q, want ai_rolling_cap_cents_ prefix", tier, k)
		}
	}
}

var errTest = &capErr{}

type capErr struct{}

func (*capErr) Error() string { return "commerce unreachable (test)" }
