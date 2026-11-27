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
	"testing"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/apps/flags"
)

// The handover is on as shipped, for every class, so the apps serve the Free plan as
// they always have; the premium switch is the one the owner turns off to make a third
// party's model prepaid in the apps as it is on the API. A class with no switch follows
// the plan and is handed over.
func TestTheHandoverIsOnAsShippedAndOnlyPremiumHasASwitch(t *testing.T) {
	for _, class := range []string{limits.ClassPremium, limits.ClassOurs, limits.ClassFree, ""} {
		if !handover(context.Background(), "acme", class) {
			t.Errorf("handover(%q) is off with no switch set", class)
		}
	}
	if len(flags.SeedHandover) != 1 || !flags.SeedHandover[limits.ClassPremium] {
		t.Errorf("seeds %v, want premium alone, on", flags.SeedHandover)
	}
	if got := flags.HandoverKey(limits.ClassPremium); got != "ai_handover_premium" {
		t.Errorf("the premium switch is %q", got)
	}
}
