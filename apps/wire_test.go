package apps

import "testing"

// frozen is the EXACT subsystem mount sequence of the PRE-refactor binary —
// name, OwnsHealth, and whether it has a Shutdown — one row per mounted spec, in
// mount order. It was captured empirically from the pre-refactor cloud binary by
// cmd/dumpregistry (a throwaway tool that blank-imported the old init()-registry
// and replayed the legacy MountAll bubble-sort: ascending order-int, ties broken by
// the non-stable sort over init-registration order). The "was order N" trailing
// comment records each spec's deleted order-int for provenance.
//
// This is the SOLE guardian of mount order now that the order-ints are gone:
// Wire() must reproduce this sequence byte-for-byte (the func pointers aside), so a
// reorder, drop, add, or flag change in the composition root fails HERE. Captured on
// origin/main @c504d2b: 68 specs from the live init()-registry, PLUS the two the
// wave-2 external bumps (ai v1.805.2, o11y v1.5.12) stopped self-registering — ai
// (@150 catch-all) and the hanzoai/o11y module wildcard (@70), which main currently
// DROPS and this PR restores. The module wildcard (order 70) is now folded in as the
// terminal sub-mount of the in-repo o11y read plane (order 69), so "o11y" is ONE spec.
var frozen = []struct {
	name        string
	ownsHealth  bool
	hasShutdown bool
}{
	{"pubsub", false, true},          // was order 5
	{"kafka", false, true},           // was order 6
	{"agentskills", false, false},    // was order 8
	{"flags", true, true},            // was order 9; native engine: /v1/flags health + store shutdown
	{"kms", true, false},             // was order 10
	{"metrics", false, false},        // was order 40
	{"ingress", false, true},         // was order 42
	{"account", false, false},        // was order 48
	{"iam", false, false},            // was order 50
	{"base", true, true},             // was order 60; per-org embed added Shutdown (#298)
	{"o11y", false, true},            // ONE observability subsystem (was co-owned orders 69+70): read plane + the hanzoai/o11y module wildcard folded in as MountO11y's terminal sub-mount. OwnsHealth=false keeps /v1/o11y/health the generic always-ok route the module co-entry used to trigger.
	{"authz", false, false},          // was order 70
	{"commerce", false, false},       // was order 100
	{"licensing", false, false},      // was order 110
	{"plan", true, false},            // was order 111; enable id normalized plans->plan (routes stay /v1/plans/*)
	{"pricing", true, false},         // was order 112
	{"storage", true, false},         // was order 118
	{"provisioning", false, false},   // was order 120
	{"billing", false, false},        // was order 121
	{"rollingcap", false, false},     // rolling spend-cap gate (after billing); golden drifted — refrozen
	{"account-bridge", false, false}, // was order 122
	{"do", false, false},             // was order 123
	{"platform", true, false},        // was order 124
	{"projects", false, false},       // was order 125
	{"dns", false, false},            // new: /v1/dns zone plane (after projects)
	{"domain", false, false},         // new: Hanzo Domains registrar (/v1/domain), after dns
	{"prompts", false, false},        // was order 126
	{"agents", false, true},          // was order 127
	{"link", false, true},            // new: unified AI login manager (/v1/links), after agents
	{"wallets", false, true},         // was order 127
	{"x402", false, true},            // new: x402 pay-per-use settlement (after wallets)
	{"paas", true, false},            // was order 128
	{"deploy", true, false},          // after paas (release seam), before functions
	{"functions", false, false},      // was order 128
	{"tracker", false, false},        // was order 129
	{"templates", false, false},      // was order 129
	{"framework", false, true},       // was order 129
	{"knowledge", false, false},      // was order 130
	{"content", false, true},         // new: marketing content loop (after knowledge)
	{"catalogsync", false, true},     // new: reverse loop (product.created → render) after content
	{"ml", true, false},              // was order 130
	{"usage", false, false},          // was order 131
	{"leaderboard", false, true},     // new: gamified usage analytics (after usage), owns opt-in SQLite (Shutdown)
	{"crm", false, false},            // was order 131
	{"marketing", false, true},       // new: marketing domain fold (after crm)
	{"ads", false, true},             // new: ads domain fold (after crm)
	{"validators", false, true},      // new: NFT-gated node provisioning (after ads); golden refrozen
	{"social", false, true},          // new: /v1/social fold (after crm)
	{"analytics", true, false},       // was order 132
	{"git", false, false},            // was order 132
	{"sync", false, true},            // /v1/sync engine (owns per-org DB handles → Shutdown)
	{"visor", false, false},          // was order 133
	{"captable", false, true},        // was order 133
	{"code", false, true},            // was order 134
	{"zero-trust", false, false},     // was order 134
	{"dataroom", true, true},         // was order 134
	{"graph", false, false},          // was order 135
	{"security", true, true},         // was order 136
	{"integrations", false, true},    // was order 137
	{"cloudflare", false, false},     // new: /v1/cloudflare edge plane (after integrations)
	{"sbom", true, false},            // was order 137
	{"team", false, true},            // was order 138
	{"settings", false, true},        // was order 138
	{"notify", true, false},          // was order 139
	{"channels", false, true},        // new: /v1/channels transport plane (after notify; must mount after integrations so RegisterIngress installs before webhooks emit)
	{"gateway", false, false},        // was order 139
	{"entitlements", false, true},    // was order 139
	{"exec", false, false},           // was order 140
	{"websearch", false, false},      // was order 141
	{"world", false, true},           // was order 142
	{"runtime", false, false},        // was order 143; was "bot" until the transport was named for what it is
	{"authors", false, true},         // was order 143
	{"bots", false, false},           // was order 143
	{"audit", false, false},          // was order 144
	{"affiliates", false, false},     // was order 144
	{"sign", true, true},             // was order 145
	{"product", false, false},        // was order 145
	{"evals", false, false},          // was order 145
	{"benchmark", false, false},      // benchmark plane (after evals, before treasury)
	{"research", false, true},        // R&D evidence plane (after benchmark); Shutdown closes per-org stores
	{"treasury", false, true},        // was order 146
	{"admin", false, false},          // was order 146
	{"admission", false, true},       // launch-control gate: composes flags (registry+seed+mode route+Enforce); Shutdown closes the registry store
	{"tasks", false, false},          // was order 147; platform cron folded in as a sub-mount of tasks.Mount (was a separate entry)
	{"automations", false, true},     // was order 148; connectorruntime (POST /v1/automations/connectors/:id/run) folded in as a sub-mount of automations.Mount
	{"tools", false, true},           // new: unified tool plane (after automations)
	{"marketplace", false, true},     // new: marketplace over the tool plane (after tools)
	{"referrals", false, false},      // was order 149
	{"guide", false, true},           // new: Business AI Guide (after referrals, before ai)
	{"company", false, true},         // new: Hanzo Company formation state machine (after guide)
	{"agent", false, false},          // new: /v1/agent tool-calling round (before zen/ai catch-all)
	{"zen", false, false},            // zen* claim middleware before ai's catch-all (hip-00NN)
	{"ai", false, false},             // was order 150
	{"plugins", false, false},        // was order 900
}

// TestWireOrderMatchesFrozen proves the composition root's mount order is
// byte-identical to the legacy init()-registry's, position by position.
func TestWireOrderMatchesFrozen(t *testing.T) {
	wire := Wire()
	if len(wire) != len(frozen) {
		t.Fatalf("Wire() has %d specs, frozen sequence has %d", len(wire), len(frozen))
	}
	for i, s := range wire {
		w := frozen[i]
		if s.Name != w.name {
			t.Errorf("position %d: Wire() = %q, frozen = %q (mount ORDER changed)", i, s.Name, w.name)
		}
		if s.OwnsHealth != w.ownsHealth {
			t.Errorf("position %d (%s): OwnsHealth = %v, frozen = %v", i, s.Name, s.OwnsHealth, w.ownsHealth)
		}
		if (s.Shutdown != nil) != w.hasShutdown {
			t.Errorf("position %d (%s): hasShutdown = %v, frozen = %v", i, s.Name, s.Shutdown != nil, w.hasShutdown)
		}
		if s.Mount == nil {
			t.Errorf("position %d (%s): Mount is nil", i, s.Name)
		}
	}
}

// TestWireNoDuplicateEnablement guards that every subsystem name is unique: a name
// maps 1:1 to an enable id, so a duplicate would mount two specs under one id. (The
// former o11y co-ownership was collapsed — the module wildcard is now a sub-mount of
// the in-repo o11y read plane — so there is no longer any exempt duplicate.)
func TestWireNoDuplicateEnablement(t *testing.T) {
	seen := map[string]int{}
	for _, s := range Wire() {
		seen[s.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("subsystem %q wired %d times (each enable id must be unique)", name, n)
		}
	}
}
