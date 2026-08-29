package manifest

import "testing"

// frozen is the EXACT subsystem sequence the fleet mounts — one name per app, in
// the order the host loads them. That order decides a route only between EQUAL
// patterns (ai before zen, below): NESTED static prefixes resolve by SPECIFICITY,
// so /v1/risk/labels reaches label whether or not the bare /v1/risk was registered
// first, and the app a path reaches is pinned by the router oracle
// (TestEveryServedPathReachesTheAppThatServesIt) rather than by this sequence.
//
// It descends from the
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
	"pubsub", "kv", "kafka", "amqp", "mq", "skills", "flags", "kms", "metrics",
	"ingress", "account", "iam", "base", "o11y", "authz",
	"commerce", "licensing", "plan", "pricing", "s3", "space", "provisioning",
	"billing", "allowance", "platform", "projects",
	"dns", "domain", "prompt", "agents", "link", "wallet",
	"x402", "deploy", "functions", "todo", "template", "blueprint",
	"framework", "knowledge", "graph", "help", "content", "webhook",
	"ml", "label", "reference", "risk", "dataset", "usage", "leaderboard", "marketing", "ad",
	"campaign", "validator", "social", "standing", "event", "git", "sync",
	"visor", "captable", "code", "lsp", "network", "share",
	"dataroom", "explorer", "security", "integrations", "destination", "cloudflare",
	"sbom", "team", "meet", "settings", "pref", "notify",
	"channels", "gateway", "entitlement", "exec", "sandbox", "websearch", "crawl", "seo",
	"index", "catalog", "taxonomy", "world", "web3", "bot", "node", "author",
	"audit", "affiliate", "esign", "search", "eval",
	"benchmark", "research", "experiment", "books", "treasury", "admin",
	"admission", "tasks", "tel", "auto", "flow", "engine", "registry", "tools", "marketplace", "referral",
	// `agent` is GONE from this sequence on purpose: it was a second app beside
	// `agents`, one concept with two plugins and a pair of names differing by an
	// `s`. Its surface (/v1/agent and its presets/conversations) is mounted by
	// agents now, so there is one app, one plugin and one name. This edit is the
	// deliberate one this freeze exists to demand.
	//
	// `product` is gone for the neighbouring reason: it held no store, so it was
	// not a capability but a proxy sitting on two other capabilities' roots. Its
	// four reads went to the apps that own those roots — the Meilisearch pair to
	// search, the Qdrant pair to provisioning's operator surface — and the app,
	// its plugin and its row went with them.
	//
	// `rollingcap` and `catalogsync` are gone for the SAME reason carried one step
	// further. Neither opened a store, and neither answered a path either — each
	// claimed a /v1/<name> nothing was ever registered behind. They held rows only
	// because a row is how the host starts a process, and in this fleet starting a
	// process is exactly what neither of them could survive: rollingcap set a hook
	// the `ai` module reads out of a package global, and a global set in its own
	// child is invisible in ai's; catalogsync called content.EnsureCatalogAsset,
	// which refuses whenever content's own singleton is nil, and it is nil in every
	// process but content's. So the cap moved into apps/ai, beside the gate that
	// reads it, and the catalog loop — open at BOTH ends, since commerce publishes
	// product.created only when PUBSUB_URL is set and nothing sets it — was deleted
	// rather than rehomed.
	"guide", "company", "compliance", "trust", "legal", "ask",
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
