// This file is HAND-AUTHORED. It is the SOURCE OF TRUTH for the fleet.
//
// Apps is every subsystem that ships as its own binary, in the order the host
// loads them. That order is NOT what decides which app a path reaches: the router
// resolves nested static prefixes by SPECIFICITY, and mount order breaks ties only
// between EQUAL patterns (ai before zen, at the tail). Five facts per app and no more — name,
// the absolute paths it answers, whether it must already be running when the
// first request arrives, whether the host should take traffic at all without
// it, and whether a customer is shown it — because that is the whole of what the
// light host needs to know (cmd/cloud links this package and zip and NOTHING
// else). What an app DOES lives in the app's own binary (plugin/<name>/main.go),
// which states its Mount/Shutdown/OwnsHealth/Price once, where they are used.
//
// The fourth fact is Vital, and it earns its place here rather than in the app
// because readiness is the HOST's answer: the host is what a probe reaches, what
// a Service routes to, and the only process that can see one child missing while
// the other 111 serve. See App.Vital — exactly one row sets it.
//
// The fifth is Stage (HIP-0139 §8), and it is here for the same shape of reason:
// who is shown a capability is decided once, about the capability, and read by
// three things that must not each decide it — the weave that stamps x-stage, the
// public rule that keeps a beta operation out of every generated client, and the
// refusal at Serve that answers 404 on a beta prefix for an org without the flag.
// Empty is ga. See App.Stage.
//
// This list was the composition root once removed (apps.Wire()); that root is
// gone. Editing an app is now two coordinated edits with no generator between
// them: a row HERE (the host's view) and plugin/<name>/main.go (the app's view).
// plugin/gen-app-cmds reads THIS list to scaffold a new app's main and to VALIDATE
// that the two never drift — every row has a plugin/<name> serving exactly it, and
// no plugin/<name> app-binary is missing from this list. manifest/order_test.go
// freezes the sequence so a reorder is a decision, never an accident — but what a
// reorder can actually change is narrow, and this header used to overstate it: it
// claimed account's /v1/commerce/topup/wallet "must precede" commerce's
// /v1/commerce. Measured, it does not — registering commerce FIRST still delivers
// /v1/commerce/topup/wallet to account, because the deeper prefix is the more
// specific one. Which app answers a path is pinned by
// TestEveryServedPathReachesTheAppThatServesIt (manifest/router_test.go), which
// asks the real router built from these rows; the freeze guards the sequence, not
// the routing.
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
	{Name: "skills", Prefixes: []string{"/.well-known/agent-skills/:skill/SKILL.md", "/.well-known/agent-skills/index.json"}},
	{Name: "flags", Prefixes: []string{"/v1/flags"}},
	{Name: "kms", Prefixes: []string{"/v1/kms"}},
	// /v1/logs and /v1/traces are metrics' own ingestion + query doors (see
	// plugin/metrics/openapi.json); unnamed here they fell to whichever row held
	// the bare "/v1" remainder, which serves none of them.
	{Name: "metrics", Prefixes: []string{"/v1/logs", "/v1/metrics", "/v1/traces"}},
	{Name: "ingress", Prefixes: []string{"/v1/ingress"}},
	{Name: "account", Prefixes: []string{"/v1/appearance", "/v1/avatar", "/v1/commerce/topup/rails", "/v1/commerce/topup/wallet", "/v1/csrf", "/v1/embed", "/v1/keys", "/v1/orgs"}},
	// The three root /.well-known documents are named EXACTLY, one prefix each, and
	// naming them at all is new: OIDC discovery and JWKS live at the ISSUER root by
	// spec (RFC 8414 / OIDC Discovery 1.0), so before iam was grafted the only thing
	// it could declare here was /.well-known/*, which would have taken the whole
	// subtree from skills and from anything else that ever lands under it. A
	// grafted child declares the addresses its router actually holds, so the host can
	// route the three and nothing more. They were in manifest/router_test.go's
	// `unreachable` ledger until now — a relying party's FIRST call, reaching no app.
	{Name: "iam", Prefixes: []string{"/.well-known/jwks", "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration", "/login/oauth", "/v1/iam"}},
	// One prefix for the org's Base, because everything it serves is under it now.
	// The table wire used to need a second one at /rest/v1, outside /v1 by a REST
	// client's convention rather than by ours; Base moved it beneath the mount
	// prefix, so it is /v1/base/rest/{collection} and this row covers it.
	{Name: "base", Prefixes: []string{"/v1/base", "/v1/waitlist"}},
	// /v1/summary is the PUBLIC platform status document (apps/o11y/summary.go),
	// the outward projection of the fleet health probes o11y already runs. It has
	// to be listed here or the host never routes it to this app and it falls to
	// commerce's bare "/v1", which does not serve it.
	// /ws/query_progress is the websocket form of the query-progress read. It sits
	// outside /v1/o11y because the Upgrade handshake is a transport concern, so no
	// other prefix here reaches it: unlisted, the fleet published the address and
	// routed it nowhere.
	{Name: "o11y", Prefixes: []string{"/v1/o11y", "/v1/sentinel", "/v1/summary", "/ws/query_progress"}, Eager: true},
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
	// /v1/cart is the first step of a sale, and the fleet published the last three
	// without it: /v1/store/:storeid/{authorize,capture,charge} have always been
	// served here, while the cart they operate on had no address at all. The
	// capability was never missing — hanzoai/commerce implements the whole noun —
	// only the route was, so this row is what makes the documented flow completable
	// rather than a new product. Named here or it falls to ai's bare "/v1"
	// remainder, whose prepaid balance gate would make filling a basket require the
	// balance the basket exists to create.
	// /v1/billing/topup is the stem, not /v1/billing/topup/token, because commerce
	// serves BOTH top-up doors and a prefix owns its whole subtree — naming the stem
	// states that once instead of twice. The saved-card door was added beside the
	// token one but never named here, so the fleet published it and routed it to
	// ai's bare "/v1" remainder, whose prepaid balance gate would have made topping
	// up require the balance the top-up exists to create — the same trap /v1/cart
	// describes above, on the door that funds it.
	// The customer's own ledger — transactions, credit-balance, accounts (and its
	// /:id/members child, which the accounts prefix covers) — is named here because
	// naming it in mount.go is only half an address. mount.go says what the APP will
	// answer; this row says what the ROUTER may hand it, and a leaf missing here never
	// reaches commerce at all: it falls to the "/v1" remainder on ai's row and answers
	// ai's bare 404. That is indistinguishable from an unmounted route from outside,
	// which is what made this bug survive a correct mount — the binary held the route
	// and the host never delivered to it. credit-balance is its own entry and not
	// covered by credits: they are sibling prefixes, not parent and child.
	{Name: "commerce", Prefixes: []string{"/_/commerce", "/v1/commerce", "/v1/billing/accounts", "/v1/billing/alerts", "/v1/billing/credit-balance", "/v1/billing/credits", "/v1/billing/crypto", "/v1/billing/invoices", "/v1/billing/methods", "/v1/billing/mode", "/v1/billing/payouts", "/v1/billing/plans", "/v1/billing/portal/methods", "/v1/billing/recharge", "/v1/billing/settings", "/v1/billing/subscribe/card", "/v1/billing/subscriptions", "/v1/billing/tier", "/v1/billing/topup", "/v1/billing/transactions", "/v1/billing/usage/rollup", "/v1/billing/webhooks", "/v1/billing/wire", "/v1/cart", "/v1/catalog/entries", "/v1/catalog/models", "/v1/catalog/seed", "/v1/commerce/admin/catalog", "/v1/commerce/catalog", "/v1/commerce/collection", "/v1/commerce/currencies", "/v1/commerce/disclosure", "/v1/commerce/discount", "/v1/commerce/movie", "/v1/commerce/note", "/v1/commerce/product", "/v1/commerce/return", "/v1/commerce/saleschannel", "/v1/commerce/stocklocation", "/v1/commerce/submission", "/v1/commerce/subscriber", "/v1/commerce/tokentransaction", "/v1/commerce/transfer", "/v1/commerce/variant", "/v1/commerce/wallet", "/v1/commerce/watchlist", "/v1/commerce/webhook", "/v1/payments", "/v1/plans/entries", "/v1/plans/seed", "/v1/store"}},
	{Name: "licensing", Prefixes: []string{"/v1/licensing"}, Stage: Beta},
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
	{Name: "provisioning", Prefixes: []string{"/v1/instances", "/v1/s3", "/v1/search/query", "/v1/vector"}},
	{Name: "billing", Prefixes: []string{"/v1/billing/balance", "/v1/billing/usage", "/v1/finance/balance", "/v1/finance/credits", "/v1/finance/invoices", "/v1/finance/ledger", "/v1/finance/payment-methods", "/v1/finance/usage"}},
	{Name: "rollingcap", Prefixes: []string{"/v1/rollingcap"}},
	// The free lane's ceiling, beside the priced lane's. rollingcap bounds how fast
	// a caller may burn their OWN money; allowance bounds how much of OUR compute a
	// caller with no money may take. Sibling questions, one row each.
	{Name: "allowance", Prefixes: []string{"/v1/allowance"}},
	// ONE PREFIX, because every route this app serves is now under it (HIP-0139
	// §3.1). It used to name six families of /v1/platform beside seven flat roots —
	// /v1/builds, /v1/environments, /v1/pipelines, /v1/releases, /v1/run, /v1/runner
	// and /v1/git-webhook — and the six were spelled out only because projects held
	// /v1/platform/sites and the two rows had to be separated by specificity.
	// projects no longer claims anything under here, so the subtree has one owner
	// and the row says so once. /v1/platform/ci answers 501 and is covered by this
	// prefix like any other leaf: an address the fleet publishes and routes nowhere
	// is the defect this table exists to prevent, and a 501 that names what is
	// missing is a better answer than commerce's bare-"/v1" 404.
	//
	// /v1/platform/hook is where the FORGE delivers a push (apps/platform hook.go).
	// It is platform's because the deploy trigger is: the door that used to take
	// these deliveries was in git's process, where that trigger is nil, so it
	// answered every push 204 and built nothing.
	{Name: "platform", Prefixes: []string{"/v1/platform"}},
	{Name: "projects", Prefixes: []string{"/v1/projects"}},
	{Name: "dns", Prefixes: []string{"/v1/dns"}},
	{Name: "domain", Prefixes: []string{"/v1/domain"}},
	{Name: "prompts", Prefixes: []string{"/v1/prompts"}},
	// /v1/coding is the coding engine's app-facing door, and it is on THIS row
	// rather than a row of its own because the engine is in this process and
	// cannot be anywhere else: it needs the live session store the run streams
	// into, the durable tasks engine, and the in-memory mailbox a routed run is
	// handed through — all three of which are agents'. A separate app would put a
	// socket between the run and its own mailbox. A second address on one app is
	// the ordinary shape here (tasks answers /tasks and /v1/tasks).
	{Name: "agents", Prefixes: []string{"/v1/agent", "/v1/agents", "/v1/coding"}},
	{Name: "link", Prefixes: []string{"/v1/links"}, Stage: Beta},
	{Name: "wallets", Prefixes: []string{"/v1/wallets"}, Stage: Beta},
	{Name: "x402", Prefixes: []string{"/v1/x402"}, Stage: Beta},
	{Name: "deploy", Prefixes: []string{"/v1/deploy/account/can-i", "/v1/deploy/applications", "/v1/deploy/callback", "/v1/deploy/clusters", "/v1/deploy/gitops", "/v1/deploy/health", "/v1/deploy/login", "/v1/deploy/logout", "/v1/deploy/projects", "/v1/deploy/reconcile", "/v1/deploy/session/userinfo", "/v1/deploy/settings", "/v1/deploy/stream/applications", "/v1/deploy/version"}},
	{Name: "functions", Prefixes: []string{"/v1/functions"}},
	{Name: "todo", Prefixes: []string{"/v1/todo"}},
	{Name: "templates", Prefixes: []string{"/v1/templates"}},
	{Name: "blueprint", Prefixes: []string{"/v1/blueprint"}},
	{Name: "framework", Prefixes: []string{"/v1/framework"}, Stage: Beta},
	{Name: "knowledge", Prefixes: []string{"/v1/kb/connectors", "/v1/kb/graph", "/v1/kb/import", "/v1/kb/search"}},
	{Name: "help", Prefixes: []string{"/v1/help"}},
	{Name: "content", Prefixes: []string{"/v1/content"}, Stage: Beta},
	{Name: "catalogsync", Prefixes: []string{"/v1/catalogsync"}, Eager: true},
	{Name: "webhooks", Prefixes: []string{"/v1/webhooks"}},
	{Name: "ml", Prefixes: []string{"/v1/ml/health", "/v1/ml/models"}, Stage: Beta},
	// risk owns /v1/risk OUTRIGHT — the per-organisation model plane that decides
	// AND learns. It shares no prefix with the row above: `ml` is model SERVING
	// (InferenceServices, predict) and it is live with customers on it, so the two
	// products are two names. This row used to list /v1/ml leaves, which would have
	// made /v1/ml/models mean "models you serve" and "models that learn" at once.
	//
	// One row and not two, and that is the sharpest structural fact here: the model
	// is IN-PROCESS MUTABLE STATE (per-tenant mass counters). A row is a BINARY, so
	// if one process learned and another scored, the two would hold different
	// counters and answer one question two ways — with no error and no log. One
	// owner of the state, one row.
	//
	// label is the GROUND-TRUTH plane: what turned out to be fraud, who said so,
	// and when they could first have said it. It addresses under /v1/risk because
	// the ADDRESS IS THE PRODUCT — openapi.Product reads an operation's product
	// tag off the first /v1 segment and nothing else — and ground truth is part of
	// the risk product, not of the KServe model-SERVING product the row above
	// carries. An earlier cut of this row said /v1/ml/labels, which is the same
	// mistake the row above already records having made: it would have filed seven
	// compliance operations into a live product with four paths and different
	// customers on it.
	//
	// IT DOES NOT HAVE TO PRECEDE risk, whose prefix is the bare /v1/risk. This row
	// used to say it must, that the router takes the first prefix that matches, and
	// that TestSpecificPrefixesPrecede pinned it. No such test exists — it never
	// did — and the rule is not the router's: with the bare /v1/risk registered
	// FIRST and all three specific prefixes after it, the live router still delivers
	// /v1/risk/labels here, /v1/risk/reference to reference and /v1/risk/datasets to
	// dataset. zip resolves NESTED static prefixes by SPECIFICITY; mount order
	// decides only between EQUAL patterns, which is what the ai-before-zen note
	// below is actually about.
	//
	// What DOES hold this is TestEveryServedPathReachesTheAppThatServesIt
	// (manifest/router_test.go): it builds the real router from these rows and asks
	// it, per published path, which app receives the request. That is an oracle over
	// every row at once, so no row needs a rule of its own to remember.
	//
	// It is its own subsystem rather than a leaf of the decision plane because its
	// WRITERS are mostly not that plane — commerce adjudicates the dispute, the
	// compliance face closes the case, an analyst files the review — and its record
	// is a compliance record with a retention clock of its own. A separate row also
	// means a separate per-tenant file, so no second process ever opens the
	// decision plane's single-writer store.
	{Name: "label", Prefixes: []string{"/v1/risk/labels"}, Stage: Beta},
	// reference is the LOOKUP DATA a decision consults and cannot derive:
	// disposable-email domains, datacentre and Tor ranges, issuer prefixes, and how
	// current the designation lists the screening engine holds are. Same address
	// rule as label, for the same reason — openapi.Product reads the product off
	// the first /v1 segment, and these six operations are the risk product's, not
	// the KServe model-SERVING product's. An earlier cut said /v1/ml/reference,
	// which would have filed them into a live product with four paths and
	// different customers on it.
	//
	// Its position relative to risk is likewise free — see the label row above.
	{Name: "reference", Prefixes: []string{"/v1/risk/reference"}, Stage: Beta},
	// /v1/risk/health is this app's own REAL probe (OwnsHealth), which the generic
	// always-ok liveness route would otherwise shadow.
	{Name: "risk", Prefixes: []string{"/v1/risk"}, Stage: Beta},
	// dataset is the RECORD of what a model was fitted on: a versioned, immutable
	// snapshot of one tenant's own event surface. Same address rule as label and
	// reference, for the third time — openapi.Product reads the product off the
	// first /v1 segment, and these seven operations are the risk product's. This row
	// said /v1/ml/datasets, which published them as part of the KServe model-SERVING
	// plane: one prefix, two products, and `ml` counted 14 operations that were seven
	// serving ops and seven dataset ops. The rows here feed the risk model, which
	// learns in-process from the org's own events, and never KServe.
	//
	// It is its own subsystem rather than a leaf of the decision plane because it
	// shares no state with a scorer — no in-memory model, no ring, no single-writer
	// file — and its tenant boundary is a different value: risk holds an in-process
	// per-org model, this holds a qualified `<brand>/<org>` KEY into a columnar
	// store. One package holding two tenancy models is the shape a privilege bug
	// grows in, so they are two rows claiming two disjoint sets of leaves, and zip
	// refuses two owners for one prefix at compose time.
	//
	// IT DOES NOT NEED TO PRECEDE risk, and that is MEASURED, not assumed: with the
	// bare /v1/risk registered FIRST, the live router still delivers
	// /v1/risk/datasets here and /v1/risk/labels to label. zip resolves NESTED
	// static prefixes by SPECIFICITY, so mount order decides only between EQUAL
	// patterns — which is why ai-before-zen (below) is a real decision and this is
	// not. So this row stays where it already sat: reordering a frozen mount
	// sequence to satisfy a rule the router does not apply would be churn, and the
	// four /v1/risk apps are contiguous either way.
	{Name: "dataset", Prefixes: []string{"/v1/risk/datasets"}, Stage: Beta},
	{Name: "usage", Prefixes: []string{"/v1/usage"}},
	{Name: "leaderboard", Prefixes: []string{"/v1/usage/activity", "/v1/usage/leaderboard", "/v1/usage/rollup/backfill"}},
	{Name: "crm", Prefixes: []string{"/v1/crm"}, Stage: Beta},
	{Name: "marketing", Prefixes: []string{"/v1/marketing"}, Stage: Beta},
	{Name: "ads", Prefixes: []string{"/v1/ads"}, Stage: Beta},
	{Name: "campaign", Prefixes: []string{"/v1/campaign"}, Stage: Beta},
	{Name: "validators", Prefixes: []string{"/v1/validators"}, Stage: Beta},
	{Name: "social", Prefixes: []string{"/v1/social"}, Stage: Beta},
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
	// /v1/insights/health are GET lenses. /v1/todo is NOT here and never was:
	// the todo product owns that name (its row is above, and it wins the prefix).
	//
	// "/v1/event.js" is its OWN prefix and cannot be folded into "/v1/event": a
	// prefix owns segments, and ".js" is part of this one's single segment rather
	// than a child of it, so the ingest door's claim stops short of the tag. It is
	// the hosted tag — the script every instrumented surface loads before it can
	// emit a single beacon — and unclaimed it fell to ai's bare "/v1", which answers
	// a 404 that reads to a browser as a broken script tag rather than as a routing
	// mistake. That was invisible for as long as plugin/analytics/openapi.json went
	// unregenerated: the path was in the router and not in the artifact this table
	// is checked against, so the check had nothing to disagree with.
	{Name: "analytics", Prefixes: []string{"/v1/analytics", "/v1/errors", "/v1/replay", "/v1/event", "/v1/event.js", "/v1/insights/events", "/v1/insights/health"}},
	{Name: "git", Prefixes: []string{"/explore", "/git", "/v1/git"}},
	{Name: "sync", Prefixes: []string{"/v1/sync"}},
	{Name: "visor", Prefixes: []string{"/v1/clusters", "/v1/compute/bots", "/v1/compute/regions", "/v1/compute/sizes", "/v1/fleet", "/v1/gpus", "/v1/k8s/clusters", "/v1/k8s/nodes", "/v1/machines"}},
	{Name: "captable", Prefixes: []string{"/v1/captable"}, Stage: Beta},
	{Name: "code", Prefixes: []string{"/v1/code"}},
	// lsp lives UNDER code, at /v1/code/lsp, because they are two reads of one
	// repository: code is the static index, lsp the live language server that
	// resolves through dependencies. One home for code intelligence means one
	// place to look for it, in the document and in the MCP tool list alike.
	//
	// The nesting is not a routing hazard, it is how routing works: nested static
	// prefixes resolve by SPECIFICITY, so /v1/code/lsp beats code's /v1/code and
	// both beat ai's bare "/v1" — the same relation storage's /v1/s3/buckets has
	// to provisioning's /v1/s3. Adjacency in this list is documentation.
	{Name: "lsp", Prefixes: []string{"/v1/code/lsp"}},
	// zt held "/v1/edge/nodes" — a top-level name for something that was never a
	// product. Four unrelated things wore "edge": the on-device inference runtime
	// (hanzoai/edge, a binary a customer runs on their own machine, so it has no cloud
	// prefix and never should), the public catalogue cache, the gateway's policy role,
	// and THESE — ZT fabric edge-routers, which are the nodes of an overlay network and
	// are now addressed as such at "/v1/networks/routers". A prefix belongs to a product
	// a customer calls, so "edge" gets none: /v1/edge 404s at every depth, and that is
	// the right answer rather than a missing product.
	{Name: "zt", Prefixes: []string{"/v1/mesh/services", "/v1/networks"}},
	{Name: "share", Prefixes: []string{"/v1/share"}, Stage: Beta},
	{Name: "dataroom", Prefixes: []string{"/v1/dataroom"}, Stage: Beta},
	{Name: "explorer", Prefixes: []string{"/v1/indexers", "/v1/oracles"}, Stage: Beta},
	{Name: "security", Prefixes: []string{"/v1/security"}, Stage: Beta},
	{Name: "integrations", Prefixes: []string{"/v1/connectors", "/v1/integrations"}},
	// The browser tag config is served by the projects app, which holds both the
	// handler and the project store it reads (apps/projects/tagdoor.go); it is under
	// that app's prefix, so this row does not name it. A prefix must be claimed
	// exactly once — two apps claiming one panics the host build.
	{Name: "destinations", Prefixes: []string{"/v1/destinations"}},
	{Name: "cloudflare", Prefixes: []string{"/v1/cloudflare"}},
	{Name: "sbom", Prefixes: []string{"/v1/sbom"}, Stage: Beta},
	// /collaborator is team's SECOND plane and it is app-level on purpose: the Team
	// front derives both the Y.js WebSocket (GET /collaborator) and the markup
	// snapshot RPC (POST /collaborator/rpc/{documentId}) from COLLABORATOR_URL, not
	// from the /v1/team base. Unnamed here they fell past every prefix to the
	// console the host serves at "/", so the collaborative editor got the HTML shell
	// and the typed RPC — published in openapi.yaml and therefore in every generated
	// SDK and the MCP tool list — reached no app at all.
	{Name: "team", Prefixes: []string{"/collaborator", "/v1/team"}},
	// /meet is the native call client, embedded in meet's own binary and served
	// from the same origin as its API — the same one-binary/one-origin shape
	// tasks has just above. The API side is the whole /v1/meet subtree now that
	// there are three routes under it and the client reads two of them; naming
	// each leaf was a list that had to be edited every time a route was added,
	// and an unnamed leaf falls to whichever row holds the bare remainder.
	{Name: "meet", Prefixes: []string{"/meet", "/v1/meet"}, Stage: Beta},
	{Name: "settings", Prefixes: []string{"/v1/settings"}},
	{Name: "prefs", Prefixes: []string{"/v1/prefs"}},
	{Name: "notify", Prefixes: []string{"/v1/notify"}},
	{Name: "channels", Prefixes: []string{"/v1/channels"}},
	{Name: "gateway", Prefixes: []string{"/v1/gateway"}},
	{Name: "entitlements", Prefixes: []string{"/v1/entitlements", "/v1/orgs/:org/entitlements"}},
	{Name: "exec", Prefixes: []string{"/v1/download", "/v1/exec", "/v1/files", "/v1/upload"}},
	{Name: "sandboxes", Prefixes: []string{"/v1/sandboxes"}},
	{Name: "websearch", Prefixes: []string{"/v1/websearch", "/v1/scrape"}},
	{Name: "crawl", Prefixes: []string{"/v1/crawl"}},
	// Beside the two surfaces that read the web, because it measures the same web
	// one layer up: websearch asks what a query returns, crawl reads one page, and
	// seo asks what a phrase is worth and where a domain places for it.
	{Name: "seo", Prefixes: []string{"/v1/seo"}, Stage: Beta},
	{Name: "index", Prefixes: []string{"/v1/index"}},
	{Name: "catalog", Prefixes: []string{"/v1/catalog"}},
	// The product TAXONOMY — categories, tags and display order — beside catalog
	// rather than inside it, and beside commerce rather than inside it. catalog is
	// the deployed-sites corpus and commerce's `product` is a priced SKU; this is
	// navigation copy, most of which is not purchasable and has no price.
	{Name: "taxonomy", Prefixes: []string{"/v1/taxonomy"}, Stage: Beta},
	{Name: "world", Prefixes: []string{"/v1/world"}, Stage: Beta},
	// web3 is named for the domain and serves none of it under /v1/web3, so the
	// /v1/<name> default would cover nothing it registers — the apps/plan defect.
	// The three prefixes are its whole surface (chains, rpc, tokens).
	{Name: "web3", Prefixes: []string{"/v1/chains", "/v1/rpc", "/v1/tokens"}, Stage: Beta},
	{Name: "bot", Prefixes: []string{"/v1/bot/connect", "/v1/bot/nodes", "/v1/bot/peer/invoke"}, Stage: Beta},
	{Name: "authors", Prefixes: []string{"/v1/admin/authors", "/v1/authors"}, Stage: Beta},
	// bots is the headless bot: the run control plane at /v1/bots AND the door to
	// @hanzo/bot, the service that executes a run, whose own ops paths it relays at
	// /v1/bot. Those were two apps (bots, runtime) until the split was measured for
	// what it was — a LANGUAGE boundary (Go surface, TS executor), not a product
	// boundary. One product answers for one thing, so it holds both prefixes.
	//
	// /v1/bot here is the BARE prefix: bot's three deeper prefixes above still win
	// on it by specificity, which is the only reason two apps could ever share it.
	// That sharing is the remaining defect, and it is bot's to fix by vacating —
	// its product is connected machines, not a bot.
	{Name: "bots", Prefixes: []string{"/v1/bot", "/v1/bots"}, Stage: Beta},
	{Name: "audit", Prefixes: []string{"/v1/audit"}},
	{Name: "affiliates", Prefixes: []string{"/v1/admin/affiliates", "/v1/admin/referrals", "/v1/affiliates"}, Stage: Beta},
	{Name: "esign", Prefixes: []string{"/v1/esign"}, Stage: Beta},
	{Name: "product", Prefixes: []string{"/v1/search/indexes", "/v1/search/stats", "/v1/vector/collections", "/v1/vector/stats"}},
	// search is the QUERY surface — hybrid keyword+semantic over the org's own
	// corpora at POST /v1/search. Instance CRUD lives under /v1/instances/search
	// (provisioning); the deeper product prefixes above still win by longest match.
	{Name: "search", Prefixes: []string{"/v1/search"}},
	{Name: "evals", Prefixes: []string{"/v1/evals"}},
	{Name: "benchmark", Prefixes: []string{"/v1/benchmark"}, Stage: Beta},
	{Name: "research", Prefixes: []string{"/v1/research"}, Stage: Beta},
	{Name: "experiments", Prefixes: []string{"/v1/experiments"}, Stage: Beta},
	{Name: "books", Prefixes: []string{"/v1/books/accounts", "/v1/books/ask", "/v1/books/bank/exchange", "/v1/books/bank/import", "/v1/books/bank/token", "/v1/books/bank/sync", "/v1/books/bank/transactions", "/v1/books/bank/unreconciled", "/v1/books/export", "/v1/books/gl", "/v1/books/inbox", "/v1/books/metrics", "/v1/books/pnl", "/v1/books/questions", "/v1/books/rules", "/v1/books/scan", "/v1/books/sync", "/v1/books/transactions", "/v1/books/position", "/v1/books/trial", "/v1/books/vendors"}, Stage: Beta},
	{Name: "treasury", Prefixes: []string{"/v1/admin/treasury", "/v1/finance/accounts", "/v1/finance/treasury"}},
	{Name: "admin", Prefixes: []string{"/v1/admin"}},
	{Name: "admission", Prefixes: []string{"/v1/flags/waitlist"}},
	{Name: "tasks", Prefixes: []string{"/tasks", "/v1/tasks"}},
	{Name: "tel", Prefixes: []string{"/v1/tel"}},
	{Name: "automations", Prefixes: []string{"/v1/auto"}},
	{Name: "flow", Prefixes: []string{"/v1/flow"}},
	{Name: "engine", Prefixes: []string{"/v1/engine"}},
	{Name: "registry", Prefixes: []string{"/v1/registry"}},
	// The tool plane also serves the CALLER's own tools — its connectors, skills,
	// agents, and the external MCP servers it enabled — which are rows and could
	// never have been in a build-time catalogue. It used to be the fleet's single
	// "open" app for that reason, and zip refused a second. Nothing marks it now:
	// the host forwards the caller's OWN tools/list to EVERY subsystem, so each one
	// answers for this caller out of its own registry and its own rows, and being
	// asked per caller is no longer a privilege one app holds.
	{Name: "tools", Prefixes: []string{"/v1/mcp/servers", "/v1/plugins", "/v1/skills", "/v1/tools"}},
	{Name: "marketplace", Prefixes: []string{"/v1/marketplace"}, Stage: Beta},
	{Name: "referrals", Prefixes: []string{"/v1/admin/referrals/bonuses", "/v1/admin/referrals/sweep", "/v1/referrals"}, Stage: Beta},
	{Name: "guide", Prefixes: []string{"/v1/guide"}, Stage: Beta},
	{Name: "company", Prefixes: []string{"/v1/company"}, Stage: Beta},
	{Name: "compliance", Prefixes: []string{"/v1/compliance"}, Stage: Beta},
	{Name: "legal", Prefixes: []string{"/v1/legal"}, Stage: Beta},
	{Name: "ask", Prefixes: []string{"/v1/ask"}},
	{Name: "translate", Prefixes: []string{"/v1/translate"}, Stage: Beta},
	// ai owns the /v1 REMAINDER: the OpenAI-compatible surface
	// (/v1/chat/completions, /v1/models, /v1/embeddings, /v1/responses,
	// /v1/audio/*, /v1/messages, …) is served by ai's own /v1/* catch-all
	// (hanzoai/ai mount), so whichever row holds "/v1" decides whether that
	// surface exists at all. Every deeper prefix above still wins; ai takes only
	// what nobody named.
	//
	// Vital, and the ONLY app that is: "/v1" is not one subsystem's prefix, it is
	// the product API's remainder, so a pod serving without ai answers 503 to
	// every model, completion and embedding call while its 111 siblings look
	// perfect. That is not a degraded deployment, it is one there is no reason to
	// route to — which is exactly what Vital says and all it says. The pod stays
	// up and keeps reporting why (App.Vital).
	{Name: "ai", Prefixes: []string{"/v1"}, Vital: true},
	// zen serves only CO-RESIDENT: its mount is a Claim middleware on ai's router
	// (apps/zen), routing zen-SKU requests and Next()ing the rest. It therefore
	// routes NO prefix of its own — see App.Coresident. The row exists because
	// every plugin/<name> binary must have one (gen-app-cmds bijection).
	// Gates, not Prefixes: zen ROUTES nothing and WRAPS ai's "/v1". Stating it
	// here is what gives plugin/zen a grant to install the Claim on, without
	// re-making the routing claim that Coresident exists to remove.
	{Name: "zen", Coresident: true, Gates: []string{"/v1"}},
	{Name: "plugins", Prefixes: []string{"/v1/admin/plugins"}},
}
