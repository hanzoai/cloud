// Package subsystems is the composition root: the single, explicit list of which
// Hanzo cloud subsystems are linked into the binary AND the order they mount in.
//
// Wire() returns []cloud.MountSpec in mount order (slice position == order). There
// is no init()-registry and no order-int: adding, removing, or reordering a
// subsystem is a one-line edit to Wire(), read top-to-bottom. cmd/cloud and
// cmd/hanzo both call Wire() and thread the slice into cloud.Serve — the set is
// defined ONCE, here.
//
// (This package must NOT live in package cloud: the subsystems import cloud for
// Deps + Typed, so a root-package bundle would form an import cycle. As a sibling
// subpackage it composes them without one.)
//
// HIP-0106: the unified cloud binary is the APPLICATION layer plus the embedded KMS
// secrets plane and the embedded IAM identity plane ("one Go binary embeds IAM +
// KMS + o11y"). The edge/infra tier (mcp, gateway, ingress-edge) runs as its own
// deployments for blast-radius isolation; several application folds (iam, base,
// commerce, captable, dataroom, sign, ingress) are STAGED — linked here but mounted
// only when the operator names them in CLOUD_ENABLE.
//
// Ordering provenance: order-int ascending; ties in the exact order the
// pre-refactor init()-registry mounted them, captured empirically from origin/main
// @c504d2b (68 self-registering specs) and frozen by TestWireOrderMatchesFrozen
// (wire_test.go). ai (@150) and the hanzoai/o11y module wildcard (@70) are NOT in
// that dump: on their wave-2 tags (ai v1.805.2, o11y v1.5.12) they no longer
// self-register, so origin/main currently DROPS them (a latent regression this
// composition root fixes). They are wired back at their order-int slots — o11y-ext
// kept adjacent to the in-repo o11y read-plane (@69); ai as the last /v1/* catch-all
// before plugins (@900). Do not re-sort; edit positions deliberately.
package subsystems

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	// External subsystem modules. As of the atomic wave-2 bump they NO LONGER
	// self-register (no cloud.Register in their init) — the composition root wires
	// each one explicitly below, so removing an entry here is the ONLY way to drop it.
	"github.com/hanzoai/ai"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/licensing"
	"github.com/hanzoai/metrics"
	o11ymod "github.com/hanzoai/o11y"

	// In-repo subsystem packages (clients/*). Each exports a Mount (and, where it
	// owns process-lifetime resources, a Shutdown); Wire references them directly.
	"github.com/hanzoai/cloud/clients/account"
	"github.com/hanzoai/cloud/clients/admin"
	"github.com/hanzoai/cloud/clients/ads"
	"github.com/hanzoai/cloud/clients/affiliates"
	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/agentskills"
	"github.com/hanzoai/cloud/clients/analytics"
	"github.com/hanzoai/cloud/clients/auditlog"
	"github.com/hanzoai/cloud/clients/authors"
	"github.com/hanzoai/cloud/clients/automations"
	"github.com/hanzoai/cloud/clients/base"
	"github.com/hanzoai/cloud/clients/billing"
	"github.com/hanzoai/cloud/clients/bot"
	"github.com/hanzoai/cloud/clients/bots"
	"github.com/hanzoai/cloud/clients/captable"
	"github.com/hanzoai/cloud/clients/catalogsync"
	"github.com/hanzoai/cloud/clients/code"
	"github.com/hanzoai/cloud/clients/commerce"
	"github.com/hanzoai/cloud/clients/content"
	"github.com/hanzoai/cloud/clients/crm"
	"github.com/hanzoai/cloud/clients/cron"
	"github.com/hanzoai/cloud/clients/dataroom"
	"github.com/hanzoai/cloud/clients/do"
	"github.com/hanzoai/cloud/clients/entitlements"
	"github.com/hanzoai/cloud/clients/eval"
	"github.com/hanzoai/cloud/clients/exec"
	"github.com/hanzoai/cloud/clients/featureflags"
	"github.com/hanzoai/cloud/clients/framework"
	"github.com/hanzoai/cloud/clients/functions"
	"github.com/hanzoai/cloud/clients/gateway"
	"github.com/hanzoai/cloud/clients/git"
	"github.com/hanzoai/cloud/clients/graph"
	"github.com/hanzoai/cloud/clients/iam"
	"github.com/hanzoai/cloud/clients/ingress"
	"github.com/hanzoai/cloud/clients/integrations"
	"github.com/hanzoai/cloud/clients/kafka"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/knowledge"
	"github.com/hanzoai/cloud/clients/marketing"
	"github.com/hanzoai/cloud/clients/marketplace"
	"github.com/hanzoai/cloud/clients/ml"
	"github.com/hanzoai/cloud/clients/notify"
	"github.com/hanzoai/cloud/clients/o11y"
	"github.com/hanzoai/cloud/clients/paas"
	"github.com/hanzoai/cloud/clients/plan"
	"github.com/hanzoai/cloud/clients/platform"
	"github.com/hanzoai/cloud/clients/plugin"
	"github.com/hanzoai/cloud/clients/pricing"
	"github.com/hanzoai/cloud/clients/product"
	"github.com/hanzoai/cloud/clients/projects"
	"github.com/hanzoai/cloud/clients/prompts"
	"github.com/hanzoai/cloud/clients/provisioning"
	"github.com/hanzoai/cloud/clients/pubsub"
	"github.com/hanzoai/cloud/clients/referrals"
	"github.com/hanzoai/cloud/clients/sbom"
	"github.com/hanzoai/cloud/clients/security"
	"github.com/hanzoai/cloud/clients/settings"
	"github.com/hanzoai/cloud/clients/sign"
	"github.com/hanzoai/cloud/clients/social"
	"github.com/hanzoai/cloud/clients/storage"
	"github.com/hanzoai/cloud/clients/tasks"
	"github.com/hanzoai/cloud/clients/team"
	"github.com/hanzoai/cloud/clients/templates"
	"github.com/hanzoai/cloud/clients/tools"
	"github.com/hanzoai/cloud/clients/tracker"
	"github.com/hanzoai/cloud/clients/treasury"
	"github.com/hanzoai/cloud/clients/usage"
	"github.com/hanzoai/cloud/clients/visor"
	"github.com/hanzoai/cloud/clients/wallets"
	"github.com/hanzoai/cloud/clients/websearch"
	"github.com/hanzoai/cloud/clients/world"
	"github.com/hanzoai/cloud/clients/zt"

	// Framework CONTENT modules — NOT mount subsystems (they carry no HTTP surface
	// and are absent from Wire()). Each registers its DocType fixtures and, for erp,
	// its ledger-posting lifecycle hooks into the clients/framework DocType engine
	// from a package init() (framework.RegisterModule) — the idiomatic
	// register-into-a-registry pattern (cf. database/sql drivers). The framework
	// engine is mounted (always-on, /v1/framework/*) but its module registry is
	// populated ONLY by these blank imports. Dropping one silently strips that
	// lane's DocTypes and hooks — for erp, the immutable ledger postings — from the
	// binary with NO mount change and NO failing mount test. #248 dropped them;
	// TestFrameworkContentModulesLinked now guards against a recurrence. Keep.
	_ "github.com/hanzoai/cloud/clients/cms"
	_ "github.com/hanzoai/cloud/clients/erp"
	_ "github.com/hanzoai/cloud/clients/help"
)

// Wire returns every linked subsystem as a cloud.MountSpec, in mount order. The
// slice position IS the order: cloud.MountAll iterates it as-given, registering each
// subsystem's teardown as a zip shutdown hook so teardown runs in reverse (LIFO).
// Enablement is a separate axis: cloud.Serve mounts only the specs cfg.Enabled(name)
// admits, so a STAGED subsystem is linked but inert until named.
func Wire() []cloud.MountSpec {
	return []cloud.MountSpec{
		// embedded NATS :4222 + JetStream.
		{Name: "pubsub", Mount: cloud.Typed(pubsub.Mount), Shutdown: pubsub.Shutdown},
		// embedded Kafka adaptor :9092.
		{Name: "kafka", Mount: cloud.Typed(kafka.Mount), Shutdown: kafka.Shutdown},
		// /.well-known/agent-skills/* — before IAM's /.well-known/* wildcard (50).
		{Name: "agentskills", Mount: cloud.Typed(agentskills.Mount)},
		// Insights feature-flag evaluation seam (no routes; a hot value plane).
		{Name: "featureflags", Mount: cloud.Typed(featureflags.Mount)},
		// Embedded KMS secrets plane /v1/kms/*. OwnsHealth: serves its own fail-closed
		// /v1/kms/health (the generic always-ok route must not shadow it). Fails closed
		// until the operator injects CLOUD_KMS_MASTER_KEY_REF. (Its in-process client
		// factory is registered separately via cloud.RegisterKMSClientFactory.)
		{Name: "kms", Mount: cloud.Typed(kms.Mount), OwnsHealth: true},
		// hanzoai/metrics — native o11y. It declares its OWN narrow metrics.Deps (no
		// hanzoai/cloud import), so Typed cannot adapt it; mountMetrics builds that Deps
		// from cloud.Deps and calls metrics.Mount explicitly.
		{Name: "metrics", Mount: mountMetrics},
		// Embedded runtime edge (/v1/ingress/*). STAGED — edge listeners stay off unless
		// the operator names "ingress" in CLOUD_ENABLE.
		{Name: "ingress", Mount: cloud.Typed(ingress.Mount), Shutdown: ingress.Shutdown},
		// SPECIFIC self-service routes (/v1/iam/{keys,onboard}, /v1/csrf, /v1/embed-status,
		// /v1/commerce/topup/wallet). MUST mount before the IAM /v1/iam/* wildcard (50) so
		// they win Fiber's first-match scan (framework-guaranteed since zip v1.3.0).
		{Name: "account", Mount: cloud.Typed(account.MountAccount)},
		// Embedded IAM identity plane (/v1/iam/*, /.well-known/*, /login/oauth/*, /_/iam/*,
		// /cas/*, /scim/*) — the identity authority, mounts before its dependents. STAGED:
		// the operator adds "iam" to --enable only after IAM config + the fold are verified.
		{Name: "iam", Mount: cloud.Typed(iam.Mount)},
		// Embedded Base app engine + viral waitlist (/v1/waitlist/*). STAGED behind
		// CLOUD_BASE_EMBED. OwnsHealth: native /v1/base/health.
		{Name: "base", Mount: cloud.Typed(base.Mount), OwnsHealth: true},
		// In-repo o11y READ plane + the runtime-handler install (o11y.SetHandler). Every
		// specific /v1/o11y/* route registers INSIDE this one mount, hence BEFORE the
		// hanzoai/o11y module wildcard (70) — Fiber's in-order match gives them precedence.
		// OwnsHealth: the module's order-70 co-entry below owns the single /v1/o11y/health.
		{Name: "o11y", Mount: o11y.MountO11y, Shutdown: o11y.ShutdownO11y, OwnsHealth: true},
		// hanzoai/o11y module wildcard /v1/o11y/* — co-owner of the ONE o11y concept with
		// the in-repo entry above (same name), delegated to via o11y.SetHandler.
		{Name: "o11y", Mount: cloud.Typed(o11ymod.Mount)},
		{Name: "authz", Mount: cloud.Typed(authz.Mount)},
		// Embedded commerce plane /v1/commerce/*, /_/commerce/*. (Its in-process
		// CommerceClient factory is registered separately via RegisterCommerceClientFactory.)
		{Name: "commerce", Mount: cloud.Typed(commerce.MountFromDeps)},
		// hanzoai/licensing. Its Mount is func(any, cloud.Deps) error — a MountFunc
		// already — so Wire references it DIRECTLY, not through Typed.
		{Name: "licensing", Mount: licensing.Mount},
		{Name: "plans", Mount: cloud.Typed(plan.Mount), OwnsHealth: true},
		{Name: "pricing", Mount: cloud.Typed(pricing.Mount), OwnsHealth: true},
		// /v1/s3/buckets/* + /v1/s3/health. Mounts BEFORE provisioning (120) so its static
		// routes win over provisioning's /v1/s3/:name. OwnsHealth (real fail-closed probe).
		{Name: "storage", Mount: cloud.Typed(storage.Mount), OwnsHealth: true},
		// Provisioning control plane: /v1/sql,/v1/vector,/v1/datastore,/v1/kv,/v1/search,/v1/s3,/v1/docdb.
		{Name: "provisioning", Mount: cloud.Typed(provisioning.Mount)},
		{Name: "billing", Mount: cloud.Typed(billing.Mount)},
		// CATCH-ALL /v1/billing/* + /v1/commerce/* data bridges — AFTER clients/billing
		// (121) + the commerce embed (100). Same clients/account package as "account" (48).
		{Name: "account-bridge", Mount: cloud.Typed(account.MountBridge)},
		{Name: "do", Mount: cloud.Typed(do.Mount)},
		{Name: "platform", Mount: cloud.Typed(platform.Mount), OwnsHealth: true},
		{Name: "projects", Mount: cloud.Typed(projects.Mount)},
		{Name: "prompts", Mount: cloud.Typed(prompts.Mount)},
		{Name: "agents", Mount: cloud.Typed(agents.Mount), Shutdown: agents.Shutdown},
		{Name: "wallets", Mount: cloud.Typed(wallets.Mount), Shutdown: ctxShutdown(wallets.Shutdown)},
		{Name: "paas", Mount: cloud.Typed(paas.Mount), OwnsHealth: true},
		{Name: "functions", Mount: cloud.Typed(functions.Mount)},
		{Name: "tracker", Mount: cloud.Typed(tracker.Mount)},
		{Name: "templates", Mount: cloud.Typed(templates.Mount)},
		{Name: "framework", Mount: cloud.Typed(framework.Mount), Shutdown: ctxShutdown(framework.Shutdown)},
		{Name: "knowledge", Mount: cloud.Typed(knowledge.Mount)},
		// Marketing content loop /v1/content/* (generate → CMS → transition → publish).
		// After framework (its DocType store the ops read/write) + knowledge (the sibling
		// framework lane); before the AI /v1/* catch-all so /v1/content/* resolves here.
		// CRUD/tenancy/install are framework's; this adds the board, lifecycle transition,
		// and the generate/publish orchestration over the zen5 + studio + social edges.
		{Name: "content", Mount: cloud.Typed(content.Mount), Shutdown: ctxShutdown(content.Shutdown)},
		// Reverse storefront loop: consume the commerce COMMERCE stream (product.created)
		// → content.EnsureCatalogAsset (render the new product's ecom asset, design==slug).
		// After content (whose EnsureCatalogAsset it drives). Inert until CLOUD_COMMERCE_NATS_URL
		// names the NATS carrying commerce catalog events — the reverse of the forward edge.
		{Name: "catalogsync", Mount: cloud.Typed(catalogsync.Mount), Shutdown: catalogsync.Shutdown},
		{Name: "ml", Mount: cloud.Typed(ml.Mount), OwnsHealth: true},
		{Name: "usage", Mount: cloud.Typed(usage.Mount)},
		{Name: "crm", Mount: cloud.Typed(crm.Mount)},
		// Native /v1/marketing/* — the in-process fold of github.com/hanzoai/marketing
		// (per-org campaign store on Base/SQLite), twin of crm. Owns a DB handle, so
		// its Shutdown closes it cleanly on SIGTERM (ctxShutdown adapts func() error).
		{Name: "marketing", Mount: cloud.Typed(marketing.Mount), Shutdown: ctxShutdown(marketing.Shutdown)},
		// Native /v1/ads/* — the net-new per-org ad-campaign store on Base/SQLite,
		// twin of crm/marketing. Owns a DB handle, so its Shutdown closes it cleanly
		// on SIGTERM (ctxShutdown adapts func() error).
		{Name: "ads", Mount: cloud.Typed(ads.Mount), Shutdown: ctxShutdown(ads.Shutdown)},
		// Native /v1/social/* — the in-process fold of the live social stack
		// (github.com/hanzoai/social: social-backend/frontend/orchestrator, a Postiz-style
		// scheduler), a per-org accounts+posts store on Base/SQLite, twin of crm. Owns a DB
		// handle, so its Shutdown closes it cleanly on SIGTERM (ctxShutdown adapts func() error).
		{Name: "social", Mount: cloud.Typed(social.Mount), Shutdown: ctxShutdown(social.Shutdown)},
		{Name: "analytics", Mount: cloud.Typed(analytics.Mount), OwnsHealth: true},
		{Name: "git", Mount: cloud.Typed(git.Mount)},
		{Name: "visor", Mount: cloud.Typed(visor.Mount)},
		// Cap table on Base via goja. STAGED behind CLOUD_ENABLE.
		{Name: "captable", Mount: cloud.Typed(captable.Mount), Shutdown: captable.Shutdown},
		{Name: "code", Mount: cloud.Typed(code.Mount), Shutdown: code.Shutdown},
		{Name: "zero-trust", Mount: cloud.Typed(zt.Mount)},
		// Data rooms via goja + per-tenant Base. STAGED behind CLOUD_ENABLE. OwnsHealth.
		{Name: "dataroom", Mount: cloud.Typed(dataroom.Mount), Shutdown: dataroom.Shutdown, OwnsHealth: true},
		{Name: "graph", Mount: cloud.Typed(graph.Mount)},
		{Name: "security", Mount: cloud.Typed(security.Mount), Shutdown: ctxShutdown(security.Shutdown), OwnsHealth: true},
		{Name: "integrations", Mount: cloud.Typed(integrations.Mount), Shutdown: integrations.Shutdown},
		{Name: "sbom", Mount: cloud.Typed(sbom.Mount), OwnsHealth: true},
		{Name: "team", Mount: cloud.Typed(team.Mount), Shutdown: ctxShutdown(team.Shutdown)},
		{Name: "settings", Mount: cloud.Typed(settings.Mount), Shutdown: settings.Shutdown},
		{Name: "notify", Mount: cloud.Typed(notify.Mount), OwnsHealth: true},
		{Name: "gateway", Mount: cloud.Typed(gateway.Mount)},
		{Name: "entitlements", Mount: cloud.Typed(entitlements.Mount), Shutdown: entitlements.Shutdown},
		{Name: "exec", Mount: cloud.Typed(exec.Mount)},
		{Name: "websearch", Mount: cloud.Typed(websearch.Mount)},
		{Name: "world", Mount: cloud.Typed(world.Mount), Shutdown: ctxShutdown(world.Shutdown)},
		{Name: "bot", Mount: cloud.Typed(bot.Mount)},
		{Name: "authors", Mount: cloud.Typed(authors.Mount), Shutdown: ctxShutdown(authors.Shutdown)},
		{Name: "bots", Mount: cloud.Typed(bots.Mount)},
		{Name: "audit", Mount: cloud.Typed(auditlog.Mount)},
		{Name: "affiliates", Mount: cloud.Typed(affiliates.Mount)},
		// Hanzo Sign (e-signature) via goja + per-tenant Base. STAGED behind CLOUD_ENABLE. OwnsHealth.
		{Name: "sign", Mount: cloud.Typed(sign.Mount), Shutdown: sign.Shutdown, OwnsHealth: true},
		{Name: "product", Mount: cloud.Typed(product.Mount)},
		{Name: "evals", Mount: cloud.Typed(eval.Mount)},
		{Name: "treasury", Mount: cloud.Typed(treasury.Mount), Shutdown: ctxShutdown(treasury.Shutdown)},
		{Name: "admin", Mount: cloud.Typed(admin.Mount)},
		{Name: "tasks", Mount: cloud.Typed(tasks.Mount)},
		// Platform cron: durable schedules on the shared tasks engine replacing
		// every k8s CronJob — entries are cron.hanzo.ai ConfigMaps (universe git),
		// runs visible in the Tasks console. Mounts no routes; starts after the
		// engine is wired.
		{Name: "cron", Mount: cloud.Typed(cron.Mount)},
		{Name: "automations", Mount: cloud.Typed(automations.Mount), Shutdown: automations.Shutdown},
		// Unified tool plane: /v1/tools/* — the ONE registry (connectors, functions,
		// agents, skills, external MCP servers, full-cloud-control /v1 routes), per-org
		// activation, and the unified MCP endpoint. Sources register into it from their
		// own Mounts, so mount position is not load-bearing (List/Dispatch run at request
		// time); placed after automations, before the zen/ai catch-all so /v1/tools wins.
		{Name: "tools", Mount: cloud.Typed(tools.Mount), Shutdown: tools.Shutdown},
		// Marketplace: /v1/marketplace/* — listing/discovery/install over the tool plane,
		// with x402-priced monetized listings. Mounts after tools (it fills the price seam).
		{Name: "marketplace", Mount: cloud.Typed(marketplace.Mount), Shutdown: marketplace.Shutdown},
		{Name: "referrals", Mount: cloud.Typed(referrals.Mount)},
		// The bare /v1/* AI catch-all — the LAST route position. Every owning subsystem above
		// wins its own namespace (Fiber first-match); AI is the fallback for the rest of /v1/*.
		// zen mounts as a /v1-scoped Claim middleware BEFORE ai: it routes zen* models
		// to zen's serving layer in-process (identity, tools, 1M ladder, codec) and
		// c.Next()s everything else to ai. zen owns the zen family; ai owns every
		// other model and the /v1/models list. Order is load-bearing — Claim must
		// run before ai's catch-all. (See hip-00NN.)
		{Name: "zen", Mount: mountZen},
		{Name: "ai", Mount: cloud.Typed(ai.Mount)},
		// Runtime wasm/proxy plugins — mounts dead last.
		{Name: "plugins", Mount: cloud.Typed(plugin.Mount)},
	}
}

// mountMetrics adapts hanzoai/metrics into a cloud.MountFunc. Unlike the other
// externals, metrics declares its OWN narrow Deps (Logger, DataDir, Brand) and does
// not import hanzoai/cloud, so cloud.Typed cannot bridge it: the composition root
// builds metrics.Deps from cloud.Deps and calls metrics.Mount explicitly here.
func mountMetrics(app any, deps cloud.Deps) error {
	a, ok := app.(*zip.App)
	if !ok {
		return fmt.Errorf("metrics.Mount: app is %T, want *zip.App", app)
	}
	return metrics.Mount(a, metrics.Deps{Logger: deps.Logger, DataDir: deps.DataDir, Brand: deps.Brand})
}

// ctxShutdown adapts a subsystem's zero-arg Shutdown() error to the
// cloud.ShutdownFunc(ctx) signature. Several subsystems expose the simpler form
// (their teardown ignores the deadline); this bridges the impedance mismatch in ONE
// place so the Wire entries stay declarative — no inline closures.
func ctxShutdown(f func() error) cloud.ShutdownFunc {
	return func(context.Context) error { return f() }
}
