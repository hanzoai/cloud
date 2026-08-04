package manifest

import "testing"

// frozen is the EXACT subsystem sequence the fleet mounts — one name per app, in
// mount order, which IS the routing order (the host loads them in this order and
// the router takes the first prefix that matches). It descends from the
// pre-refactor init()-registry sequence (captured empirically on origin/main
// @c504d2b and carried through apps.Wire()); apps.Wire() is gone and manifest.Apps
// is now the hand-authored source, so the freeze lives HERE.
//
// This is the SOLE guardian of mount order. manifest.Apps must reproduce this
// sequence exactly, so a reorder, drop, or add in the source fails HERE — an
// order change becomes a deliberate edit to this list, never an accident on one
// line of a 112-entry literal. Per-app OwnsHealth/Shutdown/App moved to each
// plugin/<app>/main.go with the composition root; a change to one of those is now a
// one-line diff in that app's own file, where it is reviewed in context.
var frozen = []string{
	"pubsub", "kafka", "mq", "skills", "flags", "kms", "metrics",
	"ingress", "account", "iam", "base", "o11y", "authz",
	"commerce", "licensing", "plan", "pricing", "storage", "provisioning",
	"billing", "rollingcap", "do", "platform", "projects",
	"dns", "domain", "prompts", "agents", "link", "wallets",
	"x402", "deploy", "functions", "tracker", "templates", "blueprint",
	"framework", "knowledge", "help", "content", "catalogsync", "webhooks",
	"ml", "label", "reference", "risk", "dataset", "usage", "leaderboard", "crm", "marketing", "ads",
	"campaign", "validators", "social", "analytics", "git", "sync",
	"visor", "venue", "captable", "code", "zt", "share",
	"dataroom", "graph", "security", "integrations", "destinations", "cloudflare",
	"sbom", "team", "meet", "settings", "prefs", "notify",
	"channels", "gateway", "entitlements", "exec", "websearch", "crawl",
	"index", "catalog", "world", "bot", "runtime", "authors",
	"bots", "audit", "affiliates", "esign", "product", "evals",
	"benchmark", "research", "experiments", "books", "treasury", "admin",
	"admission", "tasks", "automations", "flow", "engine", "registry", "auto", "tools", "marketplace", "referrals",
	"guide", "company", "compliance", "legal", "agent", "ask",
	// ai precedes zen — a DECISION, not drift: both claim "/v1", equal patterns
	// resolve by mount order, and the /v1 remainder (the OpenAI-compatible
	// surface) must land on ai. zen's row is deliberately shadowed on the light
	// host (see its apps.go comment).
	"translate", "ai", "zen", "plugins",
}

// TestAppsOrderMatchesFrozen proves the source's mount order is byte-identical to
// the frozen sequence, position by position.
func TestAppsOrderMatchesFrozen(t *testing.T) {
	if len(Apps) != len(frozen) {
		t.Fatalf("manifest.Apps has %d apps, frozen sequence has %d", len(Apps), len(frozen))
	}
	for i, a := range Apps {
		if a.Name != frozen[i] {
			t.Errorf("position %d: Apps = %q, frozen = %q (mount ORDER changed)", i, a.Name, frozen[i])
		}
	}
}

// TestAppsNoDuplicateName guards that every subsystem name is unique: a name maps
// 1:1 to an enable id and to a plugin binary, so a duplicate would route two
// specs under one id.
func TestAppsNoDuplicateName(t *testing.T) {
	seen := map[string]int{}
	for _, a := range Apps {
		seen[a.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("subsystem %q listed %d times (each enable id must be unique)", name, n)
		}
	}
}
