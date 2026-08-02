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
// shallower prefix registered earlier wins (account's /v1/commerce/topup/wallet
// must precede commerce's /v1/commerce), and manifest/order_test.go freezes the
// sequence so a reorder is a decision, never an accident.
//
// account no longer names anything under /v1/iam. It used to — deprecated key
// aliases and an onboard handler sitting inside IAM's prefix — and those were
// unreachable in production, because api.hanzo.ai routes /v1/iam/* to IAM. Since
// iam is GRAFTED rather than relayed through a wildcard, a duplicate address is
// refused at compose time instead of being decided by registration order.
package manifest

var Apps = []App{
	{Name: "pubsub", Prefixes: []string{"/v1/pubsub"}, Eager: true},
	{Name: "kafka", Prefixes: []string{"/v1/kafka"}, Eager: true},
	{Name: "mq", Prefixes: []string{"/v1/mq"}},
	{Name: "agentskills", Prefixes: []string{"/.well-known/agent-skills/:skill/SKILL.md", "/.well-known/agent-skills/index.json"}},
	{Name: "flags", Prefixes: []string{"/v1/flags"}},
	{Name: "kms", Prefixes: []string{"/v1/kms"}},
	// /v1/logs and /v1/traces are metrics' own ingestion + query doors (see
	// plugin/metrics/openapi.json); unnamed here they fell to whichever row held
	// the bare "/v1" remainder, which serves none of them.
	{Name: "metrics", Prefixes: []string{"/v1/logs", "/v1/metrics", "/v1/traces"}},
	{Name: "ingress", Prefixes: []string{"/v1/ingress"}},
	{Name: "account", Prefixes: []string{"/v1/commerce/topup/rails", "/v1/commerce/topup/wallet", "/v1/csrf", "/v1/embed", "/v1/keys", "/v1/orgs"}},
	// The three root /.well-known documents are named EXACTLY, one prefix each, and
	// naming them at all is new: OIDC discovery and JWKS live at the ISSUER root by
	// spec (RFC 8414 / OIDC Discovery 1.0), so before iam was grafted the only thing
	// it could declare here was /.well-known/*, which would have taken the whole
	// subtree from agentskills and from anything else that ever lands under it. A
	// grafted child declares the addresses its router actually holds, so the host can
	// route the three and nothing more. They were in manifest/router_test.go's
	// `unreachable` ledger until now — a relying party's FIRST call, reaching no app.
	{Name: "iam", Prefixes: []string{"/.well-known/jwks", "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration", "/login/oauth", "/v1/iam"}},
	{Name: "base", Prefixes: []string{"/v1/base", "/v1/collections", "/v1/waitlist"}},
	// /v1/summary is the PUBLIC platform status document (apps/o11y/summary.go),
	// the outward projection of the fleet health probes o11y already runs. It has
	// to be listed here or the host never routes it to this app and it falls to
	// commerce's bare "/v1", which does not serve it.
	{Name: "o11y", Prefixes: []string{"/v1/o11y", "/v1/sentry", "/v1/summary"}, Eager: true},
	{Name: "authz", Prefixes: []string{"/v1/authz/check", "/v1/authz/health", "/v1/authz/policies", "/v1/authz/readyz"}},
	// Commerce owns its published FAMILIES, never bare "/v1". As "/v1" this row was
	// the fleet's route of last resort: every path no app named deeper — the whole
	// OpenAI-compatible surface among them — landed on commerce and answered its
	// 404. The "/v1" remainder is ai's row now, at the tail. Each subtree here is
	// DEEPER than the sibling that shares its stem, because a static prefix outranks
	// a sibling wildcard regardless of mount order: catalog keeps its bare
	// /v1/catalog and plan keeps the rest of /v1/plans/*. Nobody claims the bare
	// /v1/billing or /v1/commerce REMAINDER — every leaf either row serves is named
	// deeper here or on billing's row below, so the remainder is surface no app
	// answers and claiming it would only re-create the catch-all that swallowed them.
	// This is NOT commerce.Prefixes imported (that would re-fatten the host): the
	// app states its fail-closed set once (apps/commerce/mount.go); this row states
	// what the ROUTER may hand it, and router_test.go's oracle keeps the two honest.
	{Name: "commerce", Prefixes: []string{"/_/commerce", "/v1/billing/credits", "/v1/billing/recharge", "/v1/billing/invoices", "/v1/billing/settings", "/v1/billing/payouts", "/v1/billing/plans", "/v1/billing/alerts", "/v1/billing/subscribe/card", "/v1/billing/subscriptions", "/v1/billing/mode", "/v1/billing/topup/token", "/v1/billing/webhooks", "/v1/catalog/entries", "/v1/catalog/models", "/v1/catalog/seed", "/v1/commerce/admin/catalog", "/v1/commerce/catalog", "/v1/commerce/currencies", "/v1/commerce/deposits", "/v1/commerce/tenant", "/v1/commerce/webhooks", "/v1/plans/entries", "/v1/plans/seed", "/v1/store"}},
	{Name: "licensing", Prefixes: []string{"/v1/licensing"}},
	{Name: "plan", Prefixes: []string{"/v1/plans"}},
	{Name: "pricing", Prefixes: []string{"/v1/admin/catalog", "/v1/admin/enablement", "/v1/enablement", "/v1/pricing"}},
	// storage is the S3 DATA plane (buckets, objects, health); provisioning below
	// PROVISIONS an s3 resource and answers /v1/s3 + /v1/s3/{name}. Both rows once
	// read "/v1/s3" — one prefix, two owners — so whichever mounted first took the
	// other's routes with it, and provisioning's /v1/s3/{name} matched
	// /v1/s3/buckets and /v1/s3/health besides. Naming the deeper prefixes storage
	// actually serves lets longest-prefix match separate them, which is exactly how
	// the same pair already works for /v1/vector (provisioning) against
	// /v1/vector/collections (product). No route moves.
	{Name: "storage", Prefixes: []string{"/v1/s3/buckets", "/v1/s3/health"}},
	{Name: "provisioning", Prefixes: []string{"/v1/datastore", "/v1/docdb", "/v1/kv", "/v1/s3", "/v1/search", "/v1/sql", "/v1/vector"}},
	{Name: "billing", Prefixes: []string{"/v1/billing/balance", "/v1/billing/gpu/charge", "/v1/billing/gpu/eligibility", "/v1/billing/methods", "/v1/billing/usage", "/v1/finance/balance", "/v1/finance/credits", "/v1/finance/invoices", "/v1/finance/ledger", "/v1/finance/payment-methods", "/v1/finance/usage"}},
	{Name: "rollingcap", Prefixes: []string{"/v1/rollingcap"}},
	{Name: "do", Prefixes: []string{"/v1/balancers", "/v1/vpcs"}},
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
	// The INGESTION door is load-bearing, not decorative: apps/analytics/event.go's
	// `doors` table serves /v1/event, and every beacon the products emit lands on it.
	// Listing only the read endpoints (as this row did) sent every write to commerce's
	// bare "/v1" catch-all, which does not serve them — 405, silently, for every event
	// in the fleet. The row was harmless while each app called its own routes(); it
	// became the router when the mega-build died, so a missing prefix is now an outage.
	//
	// "/v1/event" is the ONE canonical ingest door — the product, team, PostHog and
	// Sentry-envelope wires ALL arrive on it, dispatched by SHAPE. The PostHog wire's
	// own path is gone: insights.hanzo.ai's /e, /batch and /capture rewrite onto
	// /v1/event, so no caller moved. "/v1/insights/e" is gone from this row with it.
	// A prefix here is a claim that this app ANSWERS the path, and analytics does not
	// — the door is retired (retiredDoors, apps/analytics/doors_test.go), which that
	// package defines as absent from EVERY surface. Listing it bought a stale beacon
	// nothing it can use: the path 404s either way, and the row's only other effect
	// was to keep the retirement invisible in the one table that states what the
	// fleet serves. The other four prefixes are READ ONLY: bare "/v1/analytics" now
	// carries only the four lenses (overview, timeseries, top, health) — the ingest
	// aliases under it are retired — and /v1/errors, /v1/insights/events and
	// /v1/insights/health are GET lenses. /v1/tracker is NOT here and never was:
	// the tracker product owns that name (its row is above, and it wins the prefix).
	{Name: "analytics", Prefixes: []string{"/v1/analytics", "/v1/errors", "/v1/event", "/v1/insights/events", "/v1/insights/health"}},
	{Name: "git", Prefixes: []string{"/explore", "/git", "/v1/git"}},
	{Name: "sync", Prefixes: []string{"/v1/sync"}},
	{Name: "visor", Prefixes: []string{"/v1/clusters", "/v1/compute/bots", "/v1/compute/regions", "/v1/compute/sizes", "/v1/fleet", "/v1/gpus", "/v1/k8s/clusters", "/v1/k8s/nodes", "/v1/machines"}},
	{Name: "venue", Prefixes: []string{"/v1/cloud"}},
	{Name: "captable", Prefixes: []string{"/v1/captable"}},
	{Name: "code", Prefixes: []string{"/v1/code"}},
	// zt held "/v1/edge/nodes" — a top-level name for something that was never a
	// product. Four unrelated things wore "edge": the on-device inference runtime
	// (hanzoai/edge, a binary a customer runs on their own machine, so it has no cloud
	// prefix and never should), the public catalogue cache, the gateway's policy role,
	// and THESE — ZT fabric edge-routers, which are the nodes of an overlay network and
	// are now addressed as such at "/v1/networks/routers". A prefix belongs to a product
	// a customer calls, so "edge" gets none: /v1/edge 404s at every depth, and that is
	// the right answer rather than a missing product.
	{Name: "zt", Prefixes: []string{"/v1/mesh/services", "/v1/networks"}},
	{Name: "share", Prefixes: []string{"/v1/share"}},
	{Name: "dataroom", Prefixes: []string{"/v1/dataroom"}},
	{Name: "graph", Prefixes: []string{"/v1/indexers", "/v1/oracles"}},
	{Name: "security", Prefixes: []string{"/v1/security"}},
	{Name: "integrations", Prefixes: []string{"/v1/connector/github/webhook", "/v1/connectors", "/v1/integrations"}},
	{Name: "destinations", Prefixes: []string{"/v1/destinations"}},
	{Name: "cloudflare", Prefixes: []string{"/v1/cloudflare"}},
	{Name: "sbom", Prefixes: []string{"/v1/sbom"}},
	// /collaborator is team's SECOND plane and it is app-level on purpose: the Team
	// front derives both the Y.js WebSocket (GET /collaborator) and the markup
	// snapshot RPC (POST /collaborator/rpc/{documentId}) from COLLABORATOR_URL, not
	// from the /v1/team base. Unnamed here they fell past every prefix to the
	// console the host serves at "/", so the collaborative editor got the HTML shell
	// and the typed RPC — published in openapi.yaml and therefore in every generated
	// SDK and the MCP tool list — reached no app at all.
	{Name: "team", Prefixes: []string{"/collaborator", "/v1/team"}},
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
	{Name: "product", Prefixes: []string{"/v1/search/indexes", "/v1/search/stats", "/v1/vector/collections", "/v1/vector/stats"}},
	{Name: "evals", Prefixes: []string{"/v1/evals"}},
	{Name: "benchmark", Prefixes: []string{"/v1/benchmark"}},
	{Name: "research", Prefixes: []string{"/v1/research"}},
	{Name: "experiments", Prefixes: []string{"/v1/experiments"}},
	{Name: "books", Prefixes: []string{"/v1/books/accounts", "/v1/books/ask", "/v1/books/bank/exchange", "/v1/books/bank/import", "/v1/books/bank/token", "/v1/books/bank/sync", "/v1/books/bank/transactions", "/v1/books/bank/unreconciled", "/v1/books/export", "/v1/books/gl", "/v1/books/inbox", "/v1/books/metrics", "/v1/books/pnl", "/v1/books/questions", "/v1/books/rules", "/v1/books/scan", "/v1/books/sync", "/v1/books/transactions", "/v1/books/position", "/v1/books/trial", "/v1/books/vendors"}},
	{Name: "treasury", Prefixes: []string{"/v1/admin/treasury", "/v1/finance/accounts", "/v1/finance/treasury"}},
	{Name: "admin", Prefixes: []string{"/v1/admin"}},
	{Name: "admission", Prefixes: []string{"/v1/flags/waitlist"}},
	{Name: "tasks", Prefixes: []string{"/tasks", "/v1/tasks"}},
	{Name: "automations", Prefixes: []string{"/v1/automations"}},
	{Name: "flow", Prefixes: []string{"/v1/flow"}},
	{Name: "engine", Prefixes: []string{"/v1/engine"}},
	{Name: "registry", Prefixes: []string{"/v1/registry"}},
	{Name: "auto", Prefixes: []string{"/v1/auto"}},
	// Open: the tool plane also serves the CALLER's own tools — its connectors,
	// skills, agents, and the external MCP servers it enabled — which are rows and
	// cannot be in a build-time catalogue. The host asks it per caller on a
	// tools/list that names one. It is the only open app in the fleet, and zip
	// refuses a second.
	{Name: "tools", Open: true, Prefixes: []string{"/v1/mcp/servers", "/v1/plugins", "/v1/skills", "/v1/tools"}},
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
	// zen serves only CO-RESIDENT: its mount is a Claim middleware on ai's router
	// (apps/zen), routing zen-SKU requests and Next()ing the rest. It therefore
	// routes NO prefix of its own — see App.Coresident. The row exists because
	// every plugin/<name> binary must have one (gen-app-cmds bijection).
	{Name: "zen", Coresident: true},
	{Name: "plugins", Prefixes: []string{"/v1/admin/plugins"}},
}
