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
	global      bool // receives the bare *zip.App — see AppSpec.Global
}{
	{"pubsub", false, true, false},          // was order 5
	{"kafka", false, true, false},           // was order 6
	{"agentskills", false, false, false},    // was order 8
	{"flags", true, true, false},            // was order 9; native engine: /v1/flags health + store shutdown
	{"kms", true, false, false},             // was order 10
	{"metrics", false, false, true},         // was order 40
	{"ingress", false, true, false},         // was order 42
	{"account", false, false, false},        // was order 48
	{"iam", false, false, false},            // was order 50
	{"base", true, true, false},             // was order 60; per-org embed added Shutdown (#298)
	// hasShutdown flipped true->false when o11y became a PLUGIN (cloud.PluginSpec,
	// its own cmd/o11y binary). Deliberate and load-bearing, not drift: the host no
	// longer owns any o11y resource to close. The collector/sink/Datastore moved into
	// the child, which flushes them in its OWN app.OnShutdown, and zip.Load registers
	// the host-side hook that stops the child. A Shutdown on this spec would now be a
	// host closing something it does not have. Name/OwnsHealth/Global are UNCHANGED —
	// position, health routing and the app-wide grant are all still pinned here.
	{"o11y", false, false, true},            // ONE observability subsystem (was co-owned orders 69+70), now out-of-process. OwnsHealth=false keeps /v1/o11y/health the generic always-ok route, which Serve registers before MountAll and therefore ahead of the plugin's /v1/o11y/* mount.
	{"authz", false, false, true},           // was order 70
	{"commerce", false, false, true},        // was order 100
	{"licensing", false, false, true},       // was order 110
	{"plan", true, false, false},            // was order 111; enable id normalized plans->plan (routes stay /v1/plans/*)
	{"pricing", true, false, false},         // was order 112
	{"storage", true, false, false},         // was order 118
	{"provisioning", false, false, false},   // was order 120
	{"billing", false, false, false},        // was order 121
	{"rollingcap", false, false, false},     // rolling spend-cap gate (after billing); golden drifted — refrozen
	{"account-bridge", false, false, false}, // was order 122
	{"do", false, false, false},             // was order 123
	{"platform", true, true, false},         // was order 124
	{"projects", false, true, false},        // was order 125
	{"dns", false, false, false},            // new: /v1/dns zone plane (after projects)
	{"domain", false, false, false},         // new: Hanzo Domains registrar (/v1/domain), after dns
	{"prompts", false, false, false},        // was order 126
	{"agents", false, true, false},          // was order 127
	{"link", false, true, false},            // new: unified AI login manager (/v1/links), after agents
	{"wallets", false, true, false},         // was order 127
	{"x402", false, true, false},            // new: x402 pay-per-use settlement (after wallets)
	{"paas", true, false, false},            // was order 128
	{"deploy", true, false, false},          // after paas (release seam), before functions
	{"functions", false, false, false},      // was order 128
	{"tracker", false, false, false},        // was order 129
	{"templates", false, false, false},      // was order 129
	{"blueprint", true, false, false},       // new: OSS-template compute-cost basis /v1/blueprint (after templates); owns health, embedded content → no Shutdown
	{"framework", false, true, false},       // was order 129
	{"knowledge", false, false, false},      // was order 130
	{"help", false, false, false},           // new: Hanzo Support public plane /v1/help (after knowledge; framework lane with a companion subsystem, no store → no Shutdown)
	{"content", false, true, false},         // new: marketing content loop (after knowledge)
	{"catalogsync", false, true, false},     // new: reverse loop (product.created → render) after content
	{"webhooks", false, true, false},        // new: platform-global /v1/webhooks registry + bus-driven dispatcher (after catalogsync); owns per-org stores + worker pool → Shutdown
	{"ml", true, false, false},              // was order 130
	{"usage", false, false, false},          // was order 131
	{"leaderboard", false, true, false},     // new: gamified usage analytics (after usage), owns opt-in SQLite (Shutdown)
	{"crm", false, false, false},            // was order 131
	{"marketing", false, true, false},       // new: marketing domain fold (after crm)
	{"ads", false, true, false},             // new: ads domain fold (after crm)
	{"campaign", false, true, false},        // new: /v1/campaign GTM orchestration (after ads); fans out to channels
	{"validators", false, true, false},      // new: NFT-gated node provisioning (after ads); golden refrozen
	{"social", false, true, false},          // new: /v1/social fold (after crm)
	{"analytics", true, false, false},       // was order 132
	{"git", false, false, false},            // was order 132
	{"sync", false, true, false},            // /v1/sync engine (owns per-org DB handles → Shutdown)
	{"visor", false, false, false},          // was order 133
	{"venue", false, false, false},          // new: /v1/cloud connect-a-cloud-account plane (after visor); folds discovered clusters into the fleet
	{"captable", false, true, false},        // was order 133
	{"code", false, true, false},            // was order 134
	{"zero-trust", false, false, false},     // was order 134
	{"share", false, false, false},          // ngrok-native public sharing (/v1/share/*)
	{"dataroom", true, true, false},         // was order 134
	{"graph", false, false, false},          // was order 135
	{"security", true, true, false},         // was order 136
	{"integrations", false, true, false},    // was order 137
	{"destinations", false, true, false},    // new: /v1/destinations CDP fan-out (after integrations, before cloudflare)
	{"cloudflare", false, false, false},     // new: /v1/cloudflare edge plane (after integrations)
	{"sbom", true, false, false},            // was order 137
	{"team", false, true, false},            // was order 138
	{"settings", false, true, false},        // was order 138
	{"prefs", false, true, false},           // new: per-user preference plane (after settings); Shutdown closes the store
	{"notify", true, false, false},          // was order 139
	{"channels", false, true, false},        // new: /v1/channels transport plane (after notify; must mount after integrations so RegisterIngress installs before webhooks emit)
	{"gateway", false, false, false},        // was order 139
	{"entitlements", false, true, false},    // was order 139
	{"exec", false, false, false},           // was order 140
	{"websearch", false, false, false},      // was order 141
	{"index", true, true, false},            // new: in-binary full-text index (Meilisearch dialect), after websearch
	{"world", false, true, false},           // was order 142
	{"runtime", false, false, false},        // was order 143; was "bot" until the transport was named for what it is
	{"authors", false, true, false},         // was order 143
	{"bots", false, false, false},           // was order 143
	{"audit", false, false, false},          // was order 144
	{"affiliates", false, false, false},     // was order 144
	{"esign", true, true, false},            // was order 145; renamed sign->esign
	{"product", false, false, false},        // was order 145
	{"evals", false, false, false},          // was order 145
	{"benchmark", false, false, false},      // benchmark plane (after evals, before treasury)
	{"research", false, true, false},        // R&D evidence plane + /research board (HIP-0512), arena sibling after benchmark; Shutdown closes per-org stores
	{"experiments", false, true, false},     // unified A/B EXPERIMENT primitive: composes flags(assign)+analytics(measure)+research(evidence); Shutdown closes the registry stores
	{"books", false, true, false},           // AI bookkeeper (/v1/books, per-org SQLite); Shutdown closes org stores
	{"treasury", false, true, false},        // was order 146
	{"admin", false, false, false},          // was order 146
	{"admission", false, true, false},       // launch-control gate: composes flags (registry+seed+mode route+Enforce); Shutdown closes the registry store
	{"tasks", false, false, false},          // was order 147; platform cron folded in as a sub-mount of tasks.Mount (was a separate entry)
	{"automations", false, true, false},     // was order 148; connectorruntime (POST /v1/automations/connectors/:id/run) folded in as a sub-mount of automations.Mount
	{"tools", false, true, false},           // new: unified tool plane (after automations)
	{"marketplace", false, true, false},     // new: marketplace over the tool plane (after tools)
	{"referrals", false, false, false},      // was order 149
	{"guide", false, true, false},           // new: Business AI Guide (after referrals, before ai)
	{"company", false, true, false},         // new: Hanzo Company formation state machine (after guide)
	{"compliance", true, true, false},       // new: Hanzo Compliance — KYC/KYB + accreditation + audit posture (after company)
	{"legal", true, true, false},            // new: Hanzo Legal — template + generation engine + e-sign/filing (after compliance)
	{"agent", false, false, true},           // new: /v1/agent tool-calling round (before zen/ai catch-all)
	{"ask", false, false, false},            // new: unified grounded advisor /v1/ask (before zen/ai catch-all)
	{"translate", false, true, false},       // new: /v1/translate, two tiers over one endpoint (HIP-0516); Shutdown closes the per-org translation memories
	{"zen", false, false, false},            // zen* claim middleware before ai's catch-all (hip-00NN)
	{"ai", false, false, true},              // was order 150
	{"plugins", false, false, false},        // was order 900
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
		// Global hands a subsystem the bare *zip.App, and with it the ability to
		// gate every route in the binary. Freezing it here means a new grant cannot
		// arrive as a quiet field on one line of a 128-entry literal.
		if s.Global != w.global {
			t.Errorf("position %d (%s): Global = %v, frozen = %v — an app-wide capability changed", i, s.Name, s.Global, w.global)
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
