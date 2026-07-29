// This file is HAND-AUTHORED. It is the SOURCE OF TRUTH for the fleet.
//
// Apps is every subsystem that ships as its own binary, in mount order — which
// IS the routing order: the host loads them in this sequence and the router
// takes the first prefix that matches. Three facts per app and no more — name,
// the absolute paths it answers, and whether it must already be running when the
// first request arrives — because that is the whole of what the light host needs
// to know (cmd/cloud links this package and zip and NOTHING else). What an app
// DOES lives in the app's own binary (plugin/<name>/main.go), which states its
// Mount/Shutdown/OwnsHealth/Price once, where they are used.
//
// This list was the composition root once removed (apps.Wire()); that root is
// gone. Editing an app is now two coordinated edits with no generator between
// them: a row HERE (the host's view) and plugin/<name>/main.go (the app's view).
// plugin/gen-app-cmds reads THIS list to scaffold a new app's main and to VALIDATE
// that the two never drift — every row has a plugin/<name> serving exactly it, and
// no plugin/<name> app-binary is missing from this list. Order is deliberate: a
// shallower prefix registered earlier wins (account's /v1/iam/keys must precede
// iam's /v1/iam), and manifest/order_test.go freezes the sequence so a reorder
// is a decision, never an accident.
package manifest

var Apps = []App{
	{Name: "pubsub", Prefixes: []string{"/v1/pubsub"}, Eager: true},
	{Name: "kafka", Prefixes: []string{"/v1/kafka"}, Eager: true},
	{Name: "agentskills", Prefixes: []string{"/.well-known/agent-skills/:skill/SKILL.md", "/.well-known/agent-skills/index.json"}},
	{Name: "flags", Prefixes: []string{"/v1/flags"}},
	{Name: "kms", Prefixes: []string{"/v1/kms"}},
	// /v1/logs and /v1/traces are metrics' own ingestion + query doors (see
	// plugin/metrics/openapi.json); unnamed here they fell to whichever row held
	// the bare "/v1" remainder, which serves none of them.
	{Name: "metrics", Prefixes: []string{"/v1/logs", "/v1/metrics", "/v1/traces"}},
	{Name: "ingress", Prefixes: []string{"/v1/ingress"}},
	{Name: "account", Prefixes: []string{"/v1/commerce/topup/rails", "/v1/commerce/topup/wallet", "/v1/csrf", "/v1/embed-status", "/v1/iam/keys", "/v1/iam/onboard", "/v1/keys"}},
	{Name: "iam", Prefixes: []string{"/login/oauth", "/v1/iam"}},
	{Name: "base", Prefixes: []string{"/v1/base", "/v1/collections", "/v1/waitlist"}},
	{Name: "o11y", Prefixes: []string{"/v1/o11y", "/v1/sentry"}, Eager: true},
	{Name: "authz", Prefixes: []string{"/v1/authz/check", "/v1/authz/health", "/v1/authz/policies", "/v1/authz/readyz"}},
	// Commerce owns its published FAMILIES, never bare "/v1". As "/v1" this row was
	// the fleet's route of last resort: every path no app named deeper — the whole
	// OpenAI-compatible surface among them — landed on commerce and answered its
	// 404. The "/v1" remainder is ai's row now, at the tail. Each subtree here is
	// DEEPER than the sibling that shares its stem, because a static prefix outranks
	// a sibling wildcard regardless of mount order: catalog keeps its bare
	// /v1/catalog, plan keeps the rest of /v1/plans/*, and account-bridge keeps the
	// /v1/commerce/* and /v1/billing/* per-tenant data bridges the console calls.
	// This is NOT commerce.Prefixes imported (that would re-fatten the host): the
	// app states its fail-closed set once (apps/commerce/mount.go); this row states
	// what the ROUTER may hand it, and router_test.go's oracle keeps the two honest.
	{Name: "commerce", Prefixes: []string{"/_/commerce", "/v1/billing/auto-recharge", "/v1/billing/webhooks", "/v1/catalog/entries", "/v1/catalog/models", "/v1/catalog/seed", "/v1/commerce/admin/catalog", "/v1/commerce/catalog", "/v1/commerce/currencies", "/v1/commerce/deposits", "/v1/commerce/tenant", "/v1/commerce/webhooks", "/v1/plans/entries", "/v1/plans/seed", "/v1/store"}},
	{Name: "licensing", Prefixes: []string{"/v1/licensing"}},
	{Name: "plan", Prefixes: []string{"/v1/plans"}},
	{Name: "pricing", Prefixes: []string{"/v1/admin/catalog", "/v1/admin/enablement", "/v1/enablement", "/v1/pricing", "/v1/pricing-policy"}},
	{Name: "storage", Prefixes: []string{"/v1/s3"}},
	{Name: "provisioning", Prefixes: []string{"/v1/datastore", "/v1/docdb", "/v1/kv", "/v1/s3", "/v1/search", "/v1/sql", "/v1/vector"}},
	{Name: "billing", Prefixes: []string{"/v1/billing/balance", "/v1/billing/gpu-charge", "/v1/billing/gpu-eligibility", "/v1/billing/payment-methods", "/v1/billing/usage", "/v1/finance/balance", "/v1/finance/credits", "/v1/finance/invoices", "/v1/finance/ledger", "/v1/finance/payment-methods", "/v1/finance/usage"}},
	{Name: "rollingcap", Prefixes: []string{"/v1/rollingcap"}},
	{Name: "account-bridge", Prefixes: []string{"/v1/billing", "/v1/commerce"}},
	{Name: "do", Prefixes: []string{"/v1/load-balancers", "/v1/vpcs"}},
	{Name: "platform", Prefixes: []string{"/v1/builds", "/v1/environments", "/v1/pipelines", "/v1/platform/fleet", "/v1/platform/health", "/v1/platform/projects", "/v1/releases", "/v1/run", "/v1/runner"}},
	{Name: "projects", Prefixes: []string{"/v1/platform/sites", "/v1/projects", "/v1/sites"}},
	{Name: "dns", Prefixes: []string{"/v1/dns"}},
	{Name: "domain", Prefixes: []string{"/v1/domain"}},
	{Name: "prompts", Prefixes: []string{"/v1/prompts"}},
	{Name: "agents", Prefixes: []string{"/v1/agents"}},
	{Name: "link", Prefixes: []string{"/v1/links"}},
	{Name: "wallets", Prefixes: []string{"/v1/wallets"}},
	{Name: "x402", Prefixes: []string{"/v1/x402"}},
	{Name: "deploy", Prefixes: []string{"/v1/deploy/account/can-i", "/v1/deploy/applications", "/v1/deploy/callback", "/v1/deploy/clusters", "/v1/deploy/gitops", "/v1/deploy/health", "/v1/deploy/login", "/v1/deploy/logout", "/v1/deploy/projects", "/v1/deploy/reconcile", "/v1/deploy/session/userinfo", "/v1/deploy/settings", "/v1/deploy/stream/applications", "/v1/deploy/version"}},
	{Name: "functions", Prefixes: []string{"/v1/functions"}},
	{Name: "tracker", Prefixes: []string{"/v1/tracker"}},
	{Name: "templates", Prefixes: []string{"/v1/templates"}},
	{Name: "blueprint", Prefixes: []string{"/v1/blueprint"}},
	{Name: "framework", Prefixes: []string{"/v1/framework"}},
	{Name: "knowledge", Prefixes: []string{"/v1/kb/connectors", "/v1/kb/graph", "/v1/kb/import", "/v1/kb/search"}},
	{Name: "help", Prefixes: []string{"/v1/help"}},
	{Name: "content", Prefixes: []string{"/v1/content"}},
	{Name: "catalogsync", Prefixes: []string{"/v1/catalogsync"}, Eager: true},
	{Name: "webhooks", Prefixes: []string{"/v1/webhooks"}},
	{Name: "ml", Prefixes: []string{"/v1/ml/health", "/v1/ml/models", "/v1/train/experiments", "/v1/train/health", "/v1/train/jobs"}},
	{Name: "usage", Prefixes: []string{"/v1/usage"}},
	{Name: "leaderboard", Prefixes: []string{"/v1/usage/activity", "/v1/usage/leaderboard", "/v1/usage/rollup/backfill"}},
	{Name: "crm", Prefixes: []string{"/v1/crm"}},
	{Name: "marketing", Prefixes: []string{"/v1/marketing"}},
	{Name: "ads", Prefixes: []string{"/v1/ads"}},
	{Name: "campaign", Prefixes: []string{"/v1/campaign"}},
	{Name: "validators", Prefixes: []string{"/v1/validators"}},
	{Name: "social", Prefixes: []string{"/v1/social"}},
	// The INGESTION doors are load-bearing, not decorative: apps/analytics/event.go's
	// `doors` table serves /v1/event, /v1/insights/e, /v1/analytics and /v1/analytics/batch,
	// and every beacon the products emit lands on one of them. Listing only the read
	// endpoints (as this row did) sent every write to commerce's bare "/v1" catch-all,
	// which does not serve them — 405, silently, for every event in the fleet. The row was
	// harmless while each app called its own routes(); it became the router when the
	// mega-build died, so a missing prefix is now an outage. Bare "/v1/analytics" covers
	// the batch door and the four read lenses; "/v1/event" covers the Team SPA's
	// /v1/event/collect suffix. /v1/tracker is NOT here: apps/tracker owns that name.
	{Name: "analytics", Prefixes: []string{"/v1/analytics", "/v1/errors", "/v1/event", "/v1/insights/e", "/v1/insights/events", "/v1/insights/health"}},
	{Name: "git", Prefixes: []string{"/explore", "/git", "/v1/git"}},
	{Name: "sync", Prefixes: []string{"/v1/sync"}},
	{Name: "visor", Prefixes: []string{"/v1/agent-bindings", "/v1/clusters", "/v1/compute/bots", "/v1/compute/regions", "/v1/compute/sizes", "/v1/fleet", "/v1/gpus", "/v1/k8s/clusters", "/v1/k8s/nodes", "/v1/machines"}},
	{Name: "venue", Prefixes: []string{"/v1/cloud"}},
	{Name: "captable", Prefixes: []string{"/v1/captable"}},
	{Name: "code", Prefixes: []string{"/v1/code"}},
	{Name: "zero-trust", Prefixes: []string{"/v1/edge/nodes", "/v1/mesh/services", "/v1/networks"}},
	{Name: "share", Prefixes: []string{"/v1/share"}},
	{Name: "dataroom", Prefixes: []string{"/v1/dataroom"}},
	{Name: "graph", Prefixes: []string{"/v1/indexers", "/v1/oracles"}},
	{Name: "security", Prefixes: []string{"/v1/security"}},
	{Name: "integrations", Prefixes: []string{"/v1/connector/github/webhook", "/v1/connectors", "/v1/integrations"}},
	{Name: "destinations", Prefixes: []string{"/v1/destinations"}},
	{Name: "cloudflare", Prefixes: []string{"/v1/cloudflare"}},
	{Name: "sbom", Prefixes: []string{"/v1/sbom"}},
	{Name: "team", Prefixes: []string{"/v1/team"}},
	{Name: "meet", Prefixes: []string{"/v1/meet/getToken", "/v1/meet/health"}},
	{Name: "settings", Prefixes: []string{"/v1/settings"}},
	{Name: "prefs", Prefixes: []string{"/v1/prefs"}},
	{Name: "notify", Prefixes: []string{"/v1/notify"}},
	{Name: "channels", Prefixes: []string{"/v1/channels"}},
	{Name: "gateway", Prefixes: []string{"/v1/gateway"}},
	{Name: "entitlements", Prefixes: []string{"/v1/entitlements", "/v1/orgs/:org/entitlements"}},
	{Name: "exec", Prefixes: []string{"/v1/download", "/v1/exec", "/v1/files", "/v1/upload"}},
	{Name: "websearch", Prefixes: []string{"/v1/websearch", "/v1/scrape"}},
	{Name: "crawl", Prefixes: []string{"/v1/crawl"}},
	{Name: "index", Prefixes: []string{"/v1/index"}},
	{Name: "catalog", Prefixes: []string{"/v1/catalog"}},
	{Name: "world", Prefixes: []string{"/v1/world"}},
	{Name: "bot", Prefixes: []string{"/v1/bot/connect", "/v1/bot/nodes", "/v1/bot/peer/invoke"}},
	{Name: "runtime", Prefixes: []string{"/v1/bot"}},
	{Name: "authors", Prefixes: []string{"/v1/admin/authors", "/v1/authors"}},
	{Name: "bots", Prefixes: []string{"/v1/bots"}},
	{Name: "audit", Prefixes: []string{"/v1/audit"}},
	{Name: "affiliates", Prefixes: []string{"/v1/admin/affiliates", "/v1/admin/referrals", "/v1/affiliates"}},
	{Name: "esign", Prefixes: []string{"/v1/esign"}},
	{Name: "product", Prefixes: []string{"/v1/search-docs/indexes", "/v1/search-docs/stats", "/v1/vector/collections", "/v1/vector/stats"}},
	{Name: "evals", Prefixes: []string{"/v1/evals"}},
	{Name: "benchmark", Prefixes: []string{"/v1/benchmark"}},
	{Name: "research", Prefixes: []string{"/v1/research"}},
	{Name: "experiments", Prefixes: []string{"/v1/experiments"}},
	{Name: "books", Prefixes: []string{"/v1/books/accounts", "/v1/books/ask", "/v1/books/balance-sheet", "/v1/books/bank/exchange", "/v1/books/bank/import", "/v1/books/bank/link-token", "/v1/books/bank/sync", "/v1/books/bank/transactions", "/v1/books/bank/unreconciled", "/v1/books/export", "/v1/books/gl", "/v1/books/inbox", "/v1/books/metrics", "/v1/books/pnl", "/v1/books/questions", "/v1/books/rules", "/v1/books/scan", "/v1/books/sync", "/v1/books/transactions", "/v1/books/trial-balance", "/v1/books/vendors"}},
	{Name: "treasury", Prefixes: []string{"/v1/admin/treasury", "/v1/finance/accounts", "/v1/finance/treasury"}},
	{Name: "admin", Prefixes: []string{"/v1/admin"}},
	{Name: "admission", Prefixes: []string{"/v1/flags/waitlist"}},
	{Name: "tasks", Prefixes: []string{"/tasks", "/v1/tasks"}},
	{Name: "automations", Prefixes: []string{"/v1/automations"}},
	{Name: "tools", Prefixes: []string{"/v1/tools"}},
	{Name: "marketplace", Prefixes: []string{"/v1/marketplace"}},
	{Name: "referrals", Prefixes: []string{"/v1/admin/referrals/bonuses", "/v1/admin/referrals/sweep", "/v1/referrals"}},
	{Name: "guide", Prefixes: []string{"/v1/guide"}},
	{Name: "company", Prefixes: []string{"/v1/company"}},
	{Name: "compliance", Prefixes: []string{"/v1/compliance"}},
	{Name: "legal", Prefixes: []string{"/v1/legal"}},
	{Name: "agent", Prefixes: []string{"/v1/agent"}},
	{Name: "ask", Prefixes: []string{"/v1/ask"}},
	{Name: "translate", Prefixes: []string{"/v1/translate"}},
	// ai owns the /v1 REMAINDER: the OpenAI-compatible surface
	// (/v1/chat/completions, /v1/models, /v1/embeddings, /v1/responses,
	// /v1/audio/*, /v1/messages, …) is served by ai's own /v1/* catch-all
	// (hanzoai/ai mount), so whichever row holds "/v1" decides whether that
	// surface exists at all. Every deeper prefix above still wins; ai takes only
	// what nobody named.
	{Name: "ai", Prefixes: []string{"/v1"}},
	// zen serves only CO-RESIDENT: its mount is a Claim middleware on ai's
	// router (apps/zen), routing zen-SKU requests and Next()ing the rest — a
	// contract a per-process prefix cannot express, since a proxied request
	// never falls through to the next candidate. Behind ai's identical "/v1"
	// this row is deliberately shadowed on the light host; it exists because
	// every plugin/<name> binary must have its manifest row (gen-app-cmds
	// bijection).
	{Name: "zen", Prefixes: []string{"/v1"}},
	{Name: "plugins", Prefixes: []string{"/v1/admin/plugins"}},
}
