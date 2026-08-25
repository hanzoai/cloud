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
// three things that must not each decide it — the compose that stamps x-stage, the
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
// claimed account had to precede commerce because account named two prefixes under
// /v1/commerce. Measured, order never decided it — the deeper prefix is the more
// specific one and wins wherever it is registered — and those two routes are since
// deleted, so account claims nothing under another app's stem at all. Which app
// answers a path is pinned by
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
	// The second endpoint on pubsub's plane, and its own capability. A bucket holds
	// values and answers reads; nothing about it publishes or subscribes, so
	// key-value is not messaging and does not answer under messaging's name. It
	// rides the ONE embedded bus through the calls apps/pubsub exports (Bus, Org,
	// Qualify, Err) rather than running a second server — one process apart, zero
	// servers apart. NOT Eager: the endpoint is request-driven, it owns no listener
	// and no loop, and the plane it dials is another row's to start.
	{Name: "kv", Prefixes: []string{"/v1/kv"}},
	{Name: "kafka", Prefixes: []string{"/v1/kafka"}, Eager: true},
	// Eager for kafka's reason: it owns a listener. A wire adaptor that binds
	// its port only once a request arrives at /v1/amqp would never bind, since
	// its clients speak AMQP on :5672 and never call the HTTP endpoint at all.
	{Name: "amqp", Prefixes: []string{"/v1/amqp"}, Eager: true},
	{Name: "mq", Prefixes: []string{"/v1/mq"}},
	{Name: "skills", Prefixes: []string{"/.well-known/agent-skills/:skill/SKILL.md", "/.well-known/agent-skills/:product/index.json", "/.well-known/agent-skills/index.json"}},
	{Name: "flags", Prefixes: []string{"/v1/flags"}},
	{Name: "kms", Prefixes: []string{"/v1/kms"}},
	// One store, three signals, one root: the logs and traces endpoints fold under
	// /v1/metrics (HIP-1241), so the capability's routes are all under its own
	// name. The code moved with them — github.com/hanzoai/metrics is retired and
	// its endpoint now ships in github.com/hanzoai/o11y/metrics — but the capability
	// did not: a shared module is shared code, not a second owner of the address.
	{Name: "metrics", Prefixes: []string{"/v1/metrics"}},
	{Name: "ingress", Prefixes: []string{"/v1/ingress"}},
	{Name: "account", Prefixes: []string{"/v1/account"}},
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
	// One prefix, which is the whole point of the name. The Sentry product face
	// answers at /v1/o11y/sentinel and the query-progress read at
	// /v1/o11y/query_progress — both folded in the module (v1.5.67), the second
	// onto the HTTP twin it always shared a handler with, so a websocket Upgrade
	// and a long poll are one address in two protocols. The public status
	// document was the third, and it is cloud's own route at /v1/o11y/summary —
	// apps/o11y/summary.go.
	{Name: "o11y", Prefixes: []string{"/v1/o11y"}, Eager: true},
	{Name: "authz", Prefixes: []string{"/v1/authz/check", "/v1/authz/health", "/v1/authz/policies", "/v1/authz/readyz"}},
	// ONE ROOT. As "/v1" this row was the fleet's route of last resort: every path
	// no app named deeper — the whole OpenAI-compatible surface among them — landed
	// on commerce and answered its 404. The "/v1" remainder is ai's row now, at the
	// tail, and commerce claims exactly the address it is named for.
	//
	// It held twenty-three more prefixes until the /v1/billing family became
	// billing's (the row below) and the tenant-admin read came in from /_. Every one
	// of those was a leaf of somebody else's address that commerce happened to
	// answer, and each had to be named here for the same reason: unclaimed, a path
	// falls to ai's bare "/v1" remainder, whose prepaid balance gate would make
	// filling a basket — or topping up — require the balance the act exists to
	// create. Naming them was right while commerce served them. Nothing is dropped
	// by removing them, because nothing behind them is commerce's any more.
	//
	// The merchant nouns are LEAVES, not roots (HIP-1220 §1): cart, catalog,
	// payments, plans, store, the storefront resources and the processor webhook
	// intake all answer under /v1/commerce, so one claim covers them all.
	//
	// This is NOT commerce.Prefixes imported (that would re-fatten the host): the
	// app states its fail-closed set once (apps/commerce/mount.go); this row states
	// what the ROUTER may hand it, and router_test.go's oracle keeps the two honest.
	// One prefix, because a capability answers at its own name (HIP-0139 §3).
	// The merchant nouns are LEAVES: cart, catalog, payments, plans and store each
	// answer under /v1/commerce, so this row names them nowhere — one claim covers
	// them all. Those five used to be five top-level rows here, and each was here
	// for one reason: unclaimed, a path falls to ai's bare "/v1" remainder, whose
	// prepaid balance gate would make filling a basket require the balance the
	// basket exists to create.
	//
	// The twenty /v1/billing leaves that used to sit beside them are gone too, and
	// that is the second half of the same rule: /v1/billing is BILLING's address
	// (HIP-0018, HIP-1220 §2), so commerce keeps the store and publishes the plane
	// operations that answer from it while billing owns the endpoint. A row here is
	// what the ROUTER may hand an app, so vacating those leaves is what actually
	// moves the address — leaving one behind would keep delivering a billing
	// question to the app that no longer publishes it.
	{Name: "commerce", Prefixes: []string{"/v1/commerce"}},
	{Name: "licensing", Prefixes: []string{"/v1/licensing"}},
	{Name: "plan", Prefixes: []string{"/v1/plan"}},
	{Name: "pricing", Prefixes: []string{"/v1/admin/pricing", "/v1/pricing"}},
	// s3 is the S3 DATA plane (buckets, objects, health). It shared this root
	// with provisioning, which PROVISIONS an s3 resource: both rows once read
	// "/v1/s3" — one prefix, two owners — so whichever mounted first took the
	// other's routes with it. Allocation has since folded under its own name, so
	// the two no longer meet and only this row answers here.
	//
	// The app answered to "storage" until the address took the name back. Every
	// route it has ever served is under /v1/s3, s3 is a word HIP-0139 §2.5 admits
	// and the one every client already speaks, and a package called one thing
	// while its whole surface says another is the pair §7.3 closes by rename.
	{Name: "s3", Prefixes: []string{"/v1/s3/buckets", "/v1/s3/health"}},
	// provisioning allocates a store of one of seven kinds and hands back its
	// connection: one act, one store, one address (HIP-1164 §2). The row carried
	// three further roots — /v1/s3, /v1/search/query and /v1/vector — that no route
	// behind it ever registered. A routing claim with nothing behind it only takes
	// the root from the app that does answer there, so they are gone. The admin
	// prefix is the operator's whole-backend view of the vector store it allocates
	// into (apps/provisioning/inventory.go).
	{Name: "provisioning", Prefixes: []string{"/v1/admin/provisioning", "/v1/provisioning"}},
	// The customer's money endpoint owns its whole root now. It held three leaves —
	// balance, ledger, usage — beside twenty of commerce's, because the two split
	// the address and neither claimed the stem: an unnamed leaf falls to ai's bare
	// "/v1" remainder and answers ai's 404, which is indistinguishable from an
	// unmounted route, so every leaf either app served had to be spelled out.
	// HIP-0018 carries `capability: billing`, so the root is this app's; the
	// merchant half answers the same questions over the internal plane from the
	// store it still owns, and the bare stem is finally somebody's.
	{Name: "billing", Prefixes: []string{"/v1/billing"}},
	// The free lane's ceiling: how much of OUR compute a caller with no money may
	// take. The paid lane's ceiling — how fast a caller may burn their OWN money —
	// used to sit beside it as `rollingcap`, and it is not a row any more. It
	// answered no path, opened no store and installed a hook in the `ai` module,
	// so the only process it could ever take effect in is ai's own; it lives in
	// apps/ai/cap.go, where the gate that reads it runs.
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
	// It is platform's because the deploy trigger is: the endpoint that used to take
	// these deliveries was in git's process, where that trigger is nil, so it
	// answered every push 204 and built nothing.
	{Name: "platform", Prefixes: []string{"/v1/platform"}},
	{Name: "projects", Prefixes: []string{"/v1/projects"}},
	{Name: "dns", Prefixes: []string{"/v1/dns"}},
	{Name: "domain", Prefixes: []string{"/v1/domain"}},
	{Name: "prompt", Prefixes: []string{"/v1/prompt"}},
	// The coding endpoint answers at /v1/agents/coding. A coding run IS an agent run:
	// the engine is in this process because it needs the live session store the
	// run streams into, the durable tasks engine and the in-memory mailbox a routed
	// run is handed through — all three of which are agents' — so there is no store
	// boundary to split on and nothing left to give /v1/coding a root of its own.
	//
	// The conversation surface is /v1/agents/chat, under this root rather than
	// beside it. It was a second root for as long as hanzoai/agent registered its
	// four routes at one literal path; v1.0.6 takes the address from the composer
	// (hz.MountAt), and the round answers at a sub-path because POST /v1/agents is
	// already the typed create. One name, one root (HIP-1210).
	{Name: "agents", Prefixes: []string{"/v1/agents"}},
	{Name: "link", Prefixes: []string{"/v1/link"}},
	{Name: "wallet", Prefixes: []string{"/v1/wallet"}},
	{Name: "x402", Prefixes: []string{"/v1/x402"}},
	{Name: "deploy", Prefixes: []string{"/v1/deploy/account/can-i", "/v1/deploy/applications", "/v1/deploy/callback", "/v1/deploy/clusters", "/v1/deploy/gitops", "/v1/deploy/health", "/v1/deploy/login", "/v1/deploy/logout", "/v1/deploy/projects", "/v1/deploy/reconcile", "/v1/deploy/session/userinfo", "/v1/deploy/settings", "/v1/deploy/stream/applications", "/v1/deploy/version"}},
	{Name: "functions", Prefixes: []string{"/v1/functions"}},
	{Name: "todo", Prefixes: []string{"/v1/todo"}},
	{Name: "template", Prefixes: []string{"/v1/template"}},
	{Name: "blueprint", Prefixes: []string{"/v1/blueprint"}},
	{Name: "framework", Prefixes: []string{"/v1/framework"}},

	{Name: "knowledge", Prefixes: []string{"/v1/knowledge"}},
	// graph is the assertion plane: entities, the relations between them, and
	// who asserted each one when. It owns /v1/graph outright. GA: it carries
	// retention (HIP-1198), which was the one thing holding it back.
	{Name: "graph", Prefixes: []string{"/v1/graph"}},
	{Name: "help", Prefixes: []string{"/v1/help"}},
	{Name: "content", Prefixes: []string{"/v1/content"}},
	{Name: "webhook", Prefixes: []string{"/v1/webhook"}},
	{Name: "ml", Prefixes: []string{"/v1/ml/health", "/v1/ml/models"}},
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
	// and when they could first have said it.
	//
	// It answers at its OWN name. It used to answer under /v1/risk, on the reading
	// that the address was the product — openapi.Product took an operation's product
	// off the first /v1 segment — so ground truth had to sit inside the risk address
	// to be counted part of the risk product. HIP-0139 §4.1 retires that reading:
	// the tag is the app that SERVES the operation, so grouping no longer rides the
	// address and the address is free to say who owns the store. Three capabilities
	// left /v1/risk in that move; risk keeps its own scoring surface.
	//
	// It is its own subsystem rather than a leaf of the decision plane because its
	// WRITERS are mostly not that plane — commerce adjudicates the dispute, the
	// compliance face closes the case, an analyst files the review — and its record
	// is a compliance record with a retention clock of its own. A separate row also
	// means a separate per-tenant file, so no second process ever opens the
	// decision plane's single-writer store.
	{Name: "label", Prefixes: []string{"/v1/label"}},
	// reference is the LOOKUP DATA a decision consults and cannot derive:
	// disposable-email domains, datacentre and Tor ranges, issuer prefixes, and how
	// current the designation lists the screening engine holds are. It owns two
	// stores — the tenant's overrides file and the Hanzo-maintained baseline tables
	// nothing else writes — so it is its own capability, and it answers at its own
	// name for the reason the label row above states.
	{Name: "reference", Prefixes: []string{"/v1/reference"}},
	// /v1/risk/health is this app's own REAL probe (OwnsHealth), which the generic
	// always-ok liveness route would otherwise shadow.
	{Name: "risk", Prefixes: []string{"/v1/risk"}},
	// dataset is the RECORD of what a model was fitted on: a versioned, immutable
	// snapshot of one tenant's own event surface. It owns hanzo.risk_dataset and
	// hanzo.risk_row outright — those table names are store facts and stayed put
	// when the address came home, for the reason the label row above states. The
	// rows here feed the risk model, which learns in-process from the org's own
	// events, and never KServe.
	//
	// It is its own subsystem rather than a leaf of the decision plane because it
	// shares no state with a scorer — no in-memory model, no ring, no single-writer
	// file — and its tenant boundary is a different value: risk holds an in-process
	// per-org model, this holds a qualified `<brand>/<org>` KEY into a columnar
	// store. One package holding two tenancy models is the shape a privilege bug
	// grows in, so they are two rows claiming two disjoint roots, and zip refuses
	// two owners for one prefix at compose time.
	{Name: "dataset", Prefixes: []string{"/v1/dataset"}},
	{Name: "usage", Prefixes: []string{"/v1/usage"}},
	// It sat inside usage's prefix and answered under usage's name. The two are
	// two capabilities — leaderboard keeps the opt-in store, usage keeps none —
	// so it took its own name rather than folding into that one. The backfill is
	// the SuperAdmin view of this capability and lives where those live.
	{Name: "leaderboard", Prefixes: []string{"/v1/admin/leaderboard", "/v1/leaderboard"}},
	{Name: "crm", Prefixes: []string{"/v1/crm"}},
	{Name: "marketing", Prefixes: []string{"/v1/marketing"}},
	{Name: "ad", Prefixes: []string{"/v1/ad"}},
	{Name: "campaign", Prefixes: []string{"/v1/campaign"}},
	{Name: "validator", Prefixes: []string{"/v1/validator"}},
	{Name: "social", Prefixes: []string{"/v1/social"}},
	{Name: "standing", Prefixes: []string{"/v1/standing"}},
	// The INGESTION endpoint is load-bearing, not decorative: apps/event/event.go's
	// `doors` table serves /v1/event, and every beacon the products emit lands on it.
	// Listing only the read endpoints (as this row did) sent every write to commerce's
	// bare "/v1" catch-all, which does not serve them — 405, silently, for every event
	// in the fleet. The row was harmless while each app called its own routes(); it
	// became the router when the mega-build died, so a missing prefix is now an outage.
	//
	// "/v1/event" is the ONE canonical ingest endpoint — the product, team, PostHog
	// and Sentry-envelope wires ALL arrive on it, dispatched by SHAPE — and it is
	// the address this app is now NAMED for. It answered to analytics and served six
	// stems; the ingest endpoint is the one every client hard-codes (@hanzo/event's
	// EVENT_PATH, the hosted tag, HIP-0132's one telemetry ingest), so HIP-0139 §7.3
	// gives the app that word and §3.1 folds the rest under it, byte-identically for
	// the endpoint itself:
	//
	//	/v1/analytics/{overview,timeseries,top,health} -> /v1/event/…
	//	/v1/errors                                     -> /v1/event/errors
	//	/v1/insights/{events,health}                   -> /v1/event/insights/…
	//	/v1/replay                                     -> /v1/event/replay
	//	/v1/event.js                                   -> /v1/event/tag.js
	//
	// The tag move is the one that costs: ".js" is part of a single segment rather
	// than a child of it, so "/v1/event.js" could never be a child of the ingest
	// endpoint's prefix and had to be its own row — and unclaimed it fell to ai's bare
	// "/v1", a 404 that reads to a browser as a broken script tag. As a real child
	// it needs no row, and the price is every page that embedded the old src and is
	// never re-embedded. No alias: §7 has no fourth way.
	//
	// The PostHog wire's own path stays gone: insights.hanzo.ai's /e, /batch and
	// /capture rewrite onto /v1/event, so no caller moved, and "/v1/insights/e" is
	// not claimed here — a prefix is a claim that this app ANSWERS the path, and the
	// endpoint is retired (retiredDoors, apps/event/doors_test.go). /v1/todo is NOT here
	// and never was: the todo product owns that name.
	{Name: "event", Prefixes: []string{"/v1/event"}},
	{Name: "git", Prefixes: []string{"/v1/git"}},
	{Name: "sync", Prefixes: []string{"/v1/sync"}},
	{Name: "visor", Prefixes: []string{"/v1/visor"}},
	{Name: "captable", Prefixes: []string{"/v1/captable"}},
	{Name: "code", Prefixes: []string{"/v1/code"}},
	// lsp answers at its OWN root. code and lsp are two reads of one repository
	// — code is the static index, lsp the live language server that resolves
	// through dependencies — and that makes them SIBLINGS, each at the address
	// its own name spells (HIP-0139 §3.1). lsp is a §2.5 word, so it needs no
	// argument to be one. It used to sit under /v1/code, which read as one home
	// for code intelligence and was an address answered by an app not named in
	// it; the two are cross-referenced instead, which is what a reader actually
	// follows. Adjacency in this list is documentation.
	{Name: "lsp", Prefixes: []string{"/v1/lsp"}},
	// This row held "/v1/edge/nodes" — a top-level name for something that was never a
	// product. Four unrelated things wore "edge": the on-device inference runtime
	// (hanzoai/edge, a binary a customer runs on their own machine, so it has no cloud
	// prefix and never should), the public catalogue cache, the gateway's policy role,
	// and THESE — ZT fabric edge-routers, which are the nodes of an overlay network and
	// are now addressed as such at "/v1/network/routers". A prefix belongs to a product
	// a customer calls, so "edge" gets none: /v1/edge 404s at every depth, and that is
	// the right answer rather than a missing product.
	//
	// The app was called zt and answered on two stems, /v1/network and /v1/mesh.
	// "zt" abbreviates the UPSTREAM controller and is not a word anyone says for the
	// thing, so HIP-0139 §7.3 renames the app to the address's word — singular by
	// §2.2, because an org has one overlay — and §3.1 folds the second stem under
	// it: a mesh row IS an edge service of this network, exactly as an edge-router
	// is one of its nodes. ONE prefix now, and the /v1/<name> convention covers it,
	// so plugin/network states no Prefixes at all.
	{Name: "network", Prefixes: []string{"/v1/network"}},
	{Name: "share", Prefixes: []string{"/v1/share"}},
	// Two subtrees. The trust centre a data room backs is a PRODUCT any org runs,
	// so its platform roster — the one cross-tenant read, refused to anyone who is
	// not a SuperAdmin — answers at the operator's depth beside the tenant surface.
	{Name: "dataroom", Prefixes: []string{"/v1/dataroom", "/v1/admin/dataroom"}},
	{Name: "explorer", Prefixes: []string{"/v1/explorer"}},
	{Name: "security", Prefixes: []string{"/v1/security"}},
	{Name: "integrations", Prefixes: []string{"/v1/integrations"}},
	// The browser tag config is served by the projects app, which holds both the
	// handler and the project store it reads (apps/projects/tagdoor.go); it is under
	// that app's prefix, so this row does not name it. A prefix must be claimed
	// exactly once — two apps claiming one panics the host build.
	{Name: "destination", Prefixes: []string{"/v1/destination"}},
	{Name: "cloudflare", Prefixes: []string{"/v1/cloudflare"}},
	{Name: "sbom", Prefixes: []string{"/v1/sbom"}},
	// The collaborator lanes — the Y.js WebSocket and the markup snapshot RPC — are
	// branches of /v1/team now. They answered at a bare /collaborator, app-level
	// because the Team front derives both from COLLABORATOR_URL rather than from the
	// /v1/team base; unnamed here they fell past every prefix to the console the host
	// serves at "/", so the collaborative editor got the HTML shell and the typed RPC
	// reached no app at all. Under one prefix that cannot happen again, and the front
	// names the new address in the one config value it already reads.
	{Name: "team", Prefixes: []string{"/v1/team"}},
	// One prefix, because the call client is not here any more: it is its own
	// image on its own host (ghcr.io/hanzoai/meet at meet.hanzo.ai) and this row
	// used to claim /meet for the //go:embed copy. What is left is the whole
	// /v1/meet subtree rather than its leaves — naming each leaf was a list that
	// had to be edited every time a route was added, and an unnamed leaf falls to
	// whichever row holds the bare remainder.
	{Name: "meet", Prefixes: []string{"/v1/meet"}},
	{Name: "settings", Prefixes: []string{"/v1/settings"}},
	{Name: "pref", Prefixes: []string{"/v1/pref"}},
	{Name: "notify", Prefixes: []string{"/v1/notify"}},
	{Name: "channels", Prefixes: []string{"/v1/channels"}},
	{Name: "gateway", Prefixes: []string{"/v1/gateway"}},
	{Name: "entitlement", Prefixes: []string{"/v1/entitlement"}},
	// The three file addresses used to be roots of their own — /v1/upload,
	// /v1/download, /v1/files — because that is the shape the LibreChat code
	// interpreter's clients compose. They compose them off a CONFIGURABLE base,
	// so the fold costs a base-URL change and no wire change: a session's files
	// are the session's, and the session is exec's.
	{Name: "exec", Prefixes: []string{"/v1/exec"}},
	{Name: "sandbox", Prefixes: []string{"/v1/sandbox"}},
	{Name: "websearch", Prefixes: []string{"/v1/websearch"}},
	{Name: "crawl", Prefixes: []string{"/v1/crawl"}},
	// Beside the two surfaces that read the web, because it measures the same web
	// one layer up: websearch asks what a query returns, crawl reads one page, and
	// seo asks what a phrase is worth and where a domain places for it.
	{Name: "seo", Prefixes: []string{"/v1/seo"}},
	{Name: "index", Prefixes: []string{"/v1/index"}},
	{Name: "catalog", Prefixes: []string{"/v1/catalog"}},
	// The product TAXONOMY — categories, tags and display order — beside catalog
	// rather than inside it, and beside commerce rather than inside it. catalog is
	// the deployed-sites corpus and commerce's `product` is a priced SKU; this is
	// navigation copy, most of which is not purchasable and has no price.
	{Name: "taxonomy", Prefixes: []string{"/v1/taxonomy"}},
	{Name: "world", Prefixes: []string{"/v1/world"}},
	// web3 is named for the domain and serves none of it under /v1/web3, so the
	// /v1/<name> default would cover nothing it registers — the apps/plan defect.
	// The three prefixes are its whole surface (chains, rpc, tokens).
	{Name: "web3", Prefixes: []string{"/v1/web3"}},
	// A bot RUN and the relay to the executor that drives it — two route families
	// under one prefix:
	//
	//	/v1/bot/runs[/{runId}/stop]  the run control plane
	//	/v1/bot/runtime/*            @hanzo/bot's own ops paths, relayed
	//
	// The relay's greedy wildcard sits under its own segment. It was
	// app.All("/v1/bot/*") once, one specificity rule away from swallowing every
	// sibling above it.
	{Name: "bot", Prefixes: []string{"/v1/bot"}},
	// The MACHINES, which are not a kind of bot. A node dials in over a socket and
	// is asked to run commands it declared it can run; a bot run is a task the
	// executor drives on a surface it rents. Two stores, two addresses, two names
	// — HIP-0139 §7.2, which permits a split along a store boundary and only
	// there: the node plane's is the presence registry over Hanzo KV, the run
	// plane's is the executor's own, and neither reads the other's.
	{Name: "node", Prefixes: []string{"/v1/node"}},
	{Name: "author", Prefixes: []string{"/v1/admin/author", "/v1/author"}},
	{Name: "audit", Prefixes: []string{"/v1/audit"}},
	{Name: "affiliate", Prefixes: []string{"/v1/admin/affiliate", "/v1/affiliate"}},
	{Name: "esign", Prefixes: []string{"/v1/esign"}},
	// search is the QUERY surface — hybrid keyword+semantic over the org's own
	// corpora at POST /v1/search — and, since product dissolved into the two
	// capabilities that owned its roots, the Meilisearch inventory at
	// /v1/search/{indexes,stats}. Allocating a search index is a different act and
	// lives at /v1/provisioning/search.
	{Name: "search", Prefixes: []string{"/v1/search", "/v1/admin/search"}},
	{Name: "eval", Prefixes: []string{"/v1/eval"}},
	{Name: "benchmark", Prefixes: []string{"/v1/benchmark"}},
	{Name: "research", Prefixes: []string{"/v1/research"}, Stage: Alpha},
	{Name: "experiment", Prefixes: []string{"/v1/experiment"}},
	{Name: "books", Prefixes: []string{"/v1/books/accounts", "/v1/books/ask", "/v1/books/bank/exchange", "/v1/books/bank/import", "/v1/books/bank/token", "/v1/books/bank/sync", "/v1/books/bank/transactions", "/v1/books/bank/unreconciled", "/v1/books/export", "/v1/books/gl", "/v1/books/inbox", "/v1/books/metrics", "/v1/books/pnl", "/v1/books/questions", "/v1/books/rules", "/v1/books/scan", "/v1/books/sync", "/v1/books/transactions", "/v1/books/position", "/v1/books/trial", "/v1/books/vendors"}},
	{Name: "treasury", Prefixes: []string{"/v1/admin/treasury", "/v1/treasury"}},
	{Name: "admin", Prefixes: []string{"/v1/admin"}},
	{Name: "admission", Prefixes: []string{"/v1/admission"}, Stage: Alpha},
	// One prefix, because the studio is not here any more: it is its own image on
	// its own host (ghcr.io/hanzoai/admin-tasks at tasks.hanzo.ai) and this row used to
	// claim /tasks for the //go:embed copy. The studio still reads this surface
	// same-origin — the edge serves the bundle at that host's root and hands
	// /v1/tasks here, so one origin survives the split.
	{Name: "tasks", Prefixes: []string{"/v1/tasks"}},
	{Name: "tel", Prefixes: []string{"/v1/tel"}},
	// The address was already the word: the product is Hanzo Auto, the app has
	// always served one group at /v1/auto, and HIP-1063's front matter reads
	// capability: auto. Only the package name was out of step, so HIP-0139 §7.3
	// closes "/v1/auto automations" by renaming the app and moving no route. What
	// does NOT move with it: the store's sub name and the ledger product, both
	// still "automations", because both key rows already written (apps/auto/store.go,
	// apps/auto/auto.go Mount).
	{Name: "auto", Prefixes: []string{"/v1/auto"}},
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
	// Its three views — skills, plugins and the org's external MCP servers — used
	// to be roots of their own. Each is the SAME registry narrowed, over rows this
	// app opens and no other does, so each folded under the plane rather than
	// splitting off an app that would share a store. The last fold vacates the
	// /v1/mcp root entirely: that address is the host's agent MCP server.
	{Name: "tools", Prefixes: []string{"/v1/tools"}},
	{Name: "marketplace", Prefixes: []string{"/v1/marketplace"}},
	{Name: "referral", Prefixes: []string{"/v1/admin/referral/bonuses", "/v1/admin/referral/sweep", "/v1/referral"}},
	{Name: "guide", Prefixes: []string{"/v1/guide"}},
	{Name: "company", Prefixes: []string{"/v1/company"}},
	{Name: "compliance", Prefixes: []string{"/v1/compliance"}},
	// A DIFFERENT NOUN from the row above, and the two must never be merged.
	// compliance is the CUSTOMER's identity verification — KYC/KYB onboarding,
	// accreditation records and the evidence trail behind them. trust is THIS
	// organization's own control posture, published for a reviewer to read. One
	// is about who your customer is; the other is about how you run. They share
	// a vocabulary and nothing else, so they get two names and two prefixes.
	{Name: "trust", Prefixes: []string{"/v1/trust"}},
	{Name: "legal", Prefixes: []string{"/v1/legal"}},
	{Name: "ask", Prefixes: []string{"/v1/ask"}},
	{Name: "translate", Prefixes: []string{"/v1/translate"}},
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
