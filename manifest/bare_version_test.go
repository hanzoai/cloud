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

package manifest

import (
	"regexp"
	"testing"
)

// bareVersion matches a prefix that is nothing but a version root: /v1, /v2, /v10.
var bareVersion = regexp.MustCompile(`^/v[0-9]+$`)

// bareVersionExempt are the ONLY apps allowed to claim a bare version root, each
// because it is structurally broad rather than because it never got narrowed:
//
//	zen — a MIDDLEWARE, not a route owner (apps/zen/zen.go): it inspects every
//	      /v1 request, claims the ones whose model is a zen SKU, and calls
//	      c.Next() for the rest. It cannot enumerate paths because it dispatches
//	      on a BODY field, not a path.
//	ai  — the catch-all the zen contract falls through to. Its router registers
//	      ~41 top-level namespaces; enumerating them here is the right end state
//	      but a single omission silently 404s a customer-facing route, which is
//	      exactly the outage this file now guards against.
//
// The set is asserted CLOSED below: adding a third app here is a decision someone
// has to make on purpose, in a test diff, not a prefix someone shortens by habit.
var bareVersionExempt = map[string]bool{"zen": true, "ai": true}

// TestNoBareVersionPrefix keeps a version root from being claimed wholesale.
//
// A bare "/v1" claims every path under that version — including paths another app
// owns and paths that DO NOT EXIST YET. Two concrete costs: commerce held "/v1"
// and so silently overlapped billing, catalog, projects, agent, agents and kms;
// and no "/v2/commerce" could ever have coexisted with it, because the bare claim
// would have swallowed the whole of v2's sibling namespace the moment it appeared.
//
// Declare what you answer. Versioned namespaces then coexist by construction:
// /v1/commerce and /v2/commerce are simply two rows.
func TestNoBareVersionPrefix(t *testing.T) {
	for _, a := range Apps {
		for _, pre := range a.Prefixes {
			if !bareVersion.MatchString(pre) {
				continue
			}
			if bareVersionExempt[a.Name] {
				continue
			}
			t.Errorf("app %q claims the bare version root %q — declare the paths it answers instead "+
				"(a bare root also blocks a future %s/<sibling> from ever coexisting)", a.Name, pre, pre)
		}
	}
}

// TestBareVersionExemptionIsClosed freezes the exemption set. If it grows, that is
// a routing decision and it should be visible as one — not arrive as a quiet
// shortcut in someone's app row.
func TestBareVersionExemptionIsClosed(t *testing.T) {
	want := map[string]bool{"zen": true, "ai": true}
	if len(bareVersionExempt) != len(want) {
		t.Fatalf("exemption set has %d entries, want %d: %v", len(bareVersionExempt), len(want), bareVersionExempt)
	}
	for n := range want {
		if !bareVersionExempt[n] {
			t.Errorf("expected %q to remain exempt", n)
		}
	}
	// And every exempt app must actually still exist, so the list cannot rot.
	have := map[string]bool{}
	for _, a := range Apps {
		have[a.Name] = true
	}
	for n := range bareVersionExempt {
		if !have[n] {
			t.Errorf("exempt app %q is no longer in the manifest — drop it from the exemption", n)
		}
	}
}
