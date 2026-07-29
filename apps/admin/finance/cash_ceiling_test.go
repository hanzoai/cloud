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

package finance

import "testing"

// TestDailyCashCeilingDefaultsToDisarmed is the property that makes shipping the
// cash breaker safe: every ambiguous input must resolve to 0 (= disarmed),
// because this value gates ALL paid inference. A typo in a ConfigMap should cost
// a day of unguarded spend, never a fleet-wide outage — so the failure direction
// is "allow", always.
func TestDailyCashCeilingDefaultsToDisarmed(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		val  string
		want int64
	}{
		{"unset", false, "", 0},
		{"empty", true, "", 0},
		{"blank", true, "   ", 0},
		{"garbage", true, "not-a-number", 0},
		{"dollars not cents (a plausible typo)", true, "200.00", 0},
		{"negative", true, "-1", 0},
		{"zero is explicit disarm", true, "0", 0},
		{"a real ceiling", true, "20000", 20000},
		{"whitespace tolerated", true, "  20000  ", 20000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(cashCeilingEnv, tc.val)
			}
			if got := dailyCashCeilingCents(); got != tc.want {
				t.Fatalf("dailyCashCeilingCents() = %d, want %d (env %q set=%v)", got, tc.want, tc.val, tc.set)
			}
		})
	}
}
