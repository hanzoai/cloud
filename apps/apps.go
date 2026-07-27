// Package apps is the composition root: the single, explicit list of which
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
// deployments for blast-radius isolation.
//
// Every subsystem below mounts under the default (empty CLOUD_ENABLE) EXCEPT the
// staged ones — see config.go's stagedSubsystems, which is the single source of
// that set. A subsystem is staged when its Mount can abort startup, so it must be
// named in CLOUD_ENABLE deliberately; one whose Mount fails closed instead (as
// clients/iam does) is not staged.
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
//
//go:generate go run ../cmd/gen-app-cmds
package apps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	// External subsystem modules. As of the atomic wave-2 bump they NO LONGER
	// self-register (no cloud.Register in their init) — the composition root wires
	// each one explicitly below, so removing an entry here is the ONLY way to drop it.
	"github.com/hanzoai/authz"
	"github.com/hanzoai/licensing"
	"github.com/hanzoai/metrics"

	// In-repo subsystem packages (clients/*). Each exports a Mount (and, where it
	// owns process-lifetime resources, a Shutdown); Wire references them directly.
	"github.com/hanzoai/cloud/clients/account"
	"github.com/hanzoai/cloud/clients/admin"
	"github.com/hanzoai/cloud/clients/admission"
	"github.com/hanzoai/cloud/clients/ads"
	"github.com/hanzoai/cloud/clients/affiliates"
	"github.com/hanzoai/cloud/clients/agent"
	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/agentskills"
	"github.com/hanzoai/cloud/clients/analytics"
	"github.com/hanzoai/cloud/clients/ask"
	"github.com/hanzoai/cloud/clients/auditlog"
	"github.com/hanzoai/cloud/clients/authors"
	"github.com/hanzoai/cloud/clients/automations"
	"github.com/hanzoai/cloud/clients/base"
	"github.com/hanzoai/cloud/clients/benchmark"
	"github.com/hanzoai/cloud/clients/billing"
	"github.com/hanzoai/cloud/clients/blueprint"
	"github.com/hanzoai/cloud/clients/books"
	"github.com/hanzoai/cloud/clients/bots"
	"github.com/hanzoai/cloud/clients/campaign"
	"github.com/hanzoai/cloud/clients/captable"
	"github.com/hanzoai/cloud/clients/catalogsync"
	"github.com/hanzoai/cloud/clients/channels"
	"github.com/hanzoai/cloud/clients/cloudflare"
	"github.com/hanzoai/cloud/clients/code"
	"github.com/hanzoai/cloud/clients/company"
	"github.com/hanzoai/cloud/clients/compliance"
	"github.com/hanzoai/cloud/clients/content"
	"github.com/hanzoai/cloud/clients/crm"
	"github.com/hanzoai/cloud/clients/dataroom"
	// NOTE: clients/deploy is deliberately NOT imported — it is loaded at run
	// time as a plugin (see the deploy entry in Wire). Deleting this import is
	// the whole reason that works: an import here keeps its 253 exclusive
	// packages — k8s.io/kubernetes/pkg, k8s.io/kubectl/pkg — linked into cloud
	// whether or not any Wire entry referenced it.
	"github.com/hanzoai/cloud/clients/destinations"
	"github.com/hanzoai/cloud/clients/dns"
	"github.com/hanzoai/cloud/clients/do"
	"github.com/hanzoai/cloud/clients/domain"
	"github.com/hanzoai/cloud/clients/entitlements"
	"github.com/hanzoai/cloud/clients/esign"
	"github.com/hanzoai/cloud/clients/eval"
	"github.com/hanzoai/cloud/clients/exec"
	"github.com/hanzoai/cloud/clients/experiments"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/hanzoai/cloud/clients/framework"
	"github.com/hanzoai/cloud/clients/functions"
	"github.com/hanzoai/cloud/clients/gateway"
	"github.com/hanzoai/cloud/clients/git"
	"github.com/hanzoai/cloud/clients/graph"
	"github.com/hanzoai/cloud/clients/guide"
	"github.com/hanzoai/cloud/clients/help"
	"github.com/hanzoai/cloud/clients/iam"
	"github.com/hanzoai/cloud/clients/index"
	"github.com/hanzoai/cloud/clients/ingress"
	"github.com/hanzoai/cloud/clients/integrations"
	"github.com/hanzoai/cloud/clients/kafka"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/knowledge"
	"github.com/hanzoai/cloud/clients/leaderboard"
	"github.com/hanzoai/cloud/clients/legal"
	"github.com/hanzoai/cloud/clients/link"
	"github.com/hanzoai/cloud/clients/marketing"
	"github.com/hanzoai/cloud/clients/marketplace"
	"github.com/hanzoai/cloud/clients/ml"
	"github.com/hanzoai/cloud/clients/notify"
	// NOTE: clients/o11y is deliberately NOT imported. It is loaded at run time
	// as a plugin (see the o11y entry in Wire), and this line is the whole reason
	// that works: an import here would keep its 2.7k-package graph — the
	// otel-collector, prometheus, gonum — linked into cloud whether or not any
	// Wire entry referenced it. Unlinking a subsystem means deleting its import,
	// not just its mount.
	"github.com/hanzoai/cloud/clients/paas"
	"github.com/hanzoai/cloud/clients/plan"
	"github.com/hanzoai/cloud/clients/platform"
	"github.com/hanzoai/cloud/clients/plugin"
	"github.com/hanzoai/cloud/clients/prefs"
	"github.com/hanzoai/cloud/clients/pricing"
	"github.com/hanzoai/cloud/clients/product"
	"github.com/hanzoai/cloud/clients/projects"
	"github.com/hanzoai/cloud/clients/prompts"
	"github.com/hanzoai/cloud/clients/provisioning"
	"github.com/hanzoai/cloud/clients/pubsub"
	"github.com/hanzoai/cloud/clients/referrals"
	"github.com/hanzoai/cloud/clients/research"
	"github.com/hanzoai/cloud/clients/rollingcap"
	"github.com/hanzoai/cloud/clients/runtime"
	"github.com/hanzoai/cloud/clients/sbom"
	"github.com/hanzoai/cloud/clients/security"
	"github.com/hanzoai/cloud/clients/settings"
	"github.com/hanzoai/cloud/clients/share"
	"github.com/hanzoai/cloud/clients/social"
	"github.com/hanzoai/cloud/clients/storage"
	"github.com/hanzoai/cloud/clients/sync"
	"github.com/hanzoai/cloud/clients/tasks"
	"github.com/hanzoai/cloud/clients/team"
	"github.com/hanzoai/cloud/clients/templates"
	"github.com/hanzoai/cloud/clients/tools"
	"github.com/hanzoai/cloud/clients/tracker"
	"github.com/hanzoai/cloud/clients/translate"
	"github.com/hanzoai/cloud/clients/treasury"
	"github.com/hanzoai/cloud/clients/usage"
	"github.com/hanzoai/cloud/clients/validators"
	"github.com/hanzoai/cloud/clients/venue"
	"github.com/hanzoai/cloud/clients/visor"
	"github.com/hanzoai/cloud/clients/wallets"
	"github.com/hanzoai/cloud/clients/webhooks"
	"github.com/hanzoai/cloud/clients/websearch"
	"github.com/hanzoai/cloud/clients/world"
	"github.com/hanzoai/cloud/clients/x402"
	"github.com/hanzoai/cloud/clients/zt"

	// Framework CONTENT modules — pure fixture lanes that carry no HTTP surface and
	// are absent from Wire(). Each registers its DocType fixtures and, for erp, its
	// ledger-posting lifecycle hooks into the clients/framework DocType engine from a
	// package init() (framework.RegisterModule) — the idiomatic register-into-a-
	// registry pattern (cf. database/sql drivers). The framework engine is mounted
	// (always-on, /v1/framework/*) but its module registry is populated ONLY by these
	// blank imports. Dropping one silently strips that lane's DocTypes and hooks — for
	// erp, the immutable ledger postings — from the binary with NO mount change and NO
	// failing mount test. #248 dropped them; TestFrameworkContentModulesLinked now
	// guards against a recurrence. Keep. (help is a framework lane too but ALSO mounts
	// a thin /v1/help public plane, so it is a real import + a Wire() spec below,
	// alongside knowledge — the other lane with a companion subsystem.)
	_ "github.com/hanzoai/cloud/clients/cms"
	_ "github.com/hanzoai/cloud/clients/erp"
)

// init wires the cross-subsystem func seams — the composition root is the one place
// that may import two leaf subsystems at once, so a connection neither can express
// alone lives here. git's push→index reactor calls the code index without git
// importing code: the adapter converts git's IndexedFile to code's File and drops the
// result (the reactor only needs success/failure). Stored once at load; invoked on
// push, long after mount, so there is no ordering dependency. Same
// register-into-a-registry idiom the framework content modules use above.
func init() {
	git.SetIndexer(func(ctx context.Context, org, billingOrg, project, repo string, files []git.IndexedFile) error {
		in := make([]code.File, len(files))
		for i, f := range files {
			in[i] = code.File{Path: f.Path, Content: f.Content}
		}
		_, err := code.IndexFiles(ctx, org, billingOrg, project, repo, in)
		return err
	})
}

// Wire returns every linked subsystem as a cloud.MountSpec, in mount order. The
// slice position IS the order: cloud.MountAll iterates it as-given, registering each
// subsystem's teardown as a zip shutdown hook so teardown runs in reverse (LIFO).
// Enablement is a separate axis: cloud.Serve mounts only the specs cfg.Enabled(name)
// admits, so a STAGED subsystem is linked but inert until named.
func Wire() []cloud.MountSpec {
	return []cloud.MountSpec{
		// embedded NATS :4222 + JetStream.
		{Name: "pubsub", Mount: pubsub.Mount, Shutdown: pubsub.Shutdown},
		// embedded Kafka adaptor :9092.
		{Name: "kafka", Mount: kafka.Mount, Shutdown: kafka.Shutdown},
		// /.well-known/agent-skills/* — before IAM's /.well-known/* wildcard (50).
		{Name: "agentskills", Mount: agentskills.Mount},
		// Insights feature-flag evaluation seam (no routes; a hot value plane).
		{Name: "flags", Mount: flags.Mount, Shutdown: flags.Shutdown, OwnsHealth: true},
		// Embedded KMS secrets plane /v1/kms/*. OwnsHealth: serves its own fail-closed
		// /v1/kms/health (the generic always-ok route must not shadow it). Fails closed
		// until the operator injects CLOUD_KMS_MASTER_KEY_REF. (Its in-process client
		// factory is registered separately via cloud.RegisterKMSClientFactory.)
		{Name: "kms", Mount: kms.Mount, OwnsHealth: true},
		// hanzoai/metrics — native o11y. It declares its OWN narrow metrics.Deps (no
		// hanzoai/cloud import), so Typed cannot adapt it; mountMetrics builds that Deps
		// from cloud.Deps and calls metrics.Mount explicitly.
		{Name: "metrics", Mount: cloud.Global(mountMetrics), Global: true},
		// Embedded runtime edge (/v1/ingress/*). STAGED — edge listeners stay off unless
		// the operator names "ingress" in CLOUD_ENABLE.
		{Name: "ingress", Mount: ingress.Mount, Shutdown: ingress.Shutdown},
		// SPECIFIC self-service routes (/v1/iam/{keys,onboard}, /v1/csrf, /v1/embed-status,
		// /v1/commerce/topup/wallet). MUST mount before the IAM /v1/iam/* wildcard (50) so
		// they win Fiber's first-match scan (framework-guaranteed since zip v1.3.0).
		{Name: "account", Mount: account.MountAccount},
		// Embedded IAM identity plane (/v1/iam/*, /login/oauth/*) — the identity authority,
		// mounts before its dependents. The ONE implementation: the clean-room iam-v2
		// (zip-native + hanzoai/orm, beego-free); the retired Casdoor iam-v1 embed is GONE.
		// STAGED: the operator adds "iam" to --enable only after IAM config + the fold are
		// verified (login/authorize/token/jwks + the operator SSO chain).
		{Name: "iam", Mount: iam.Mount, Prefixes: iam.Prefixes},
		// Embedded Base app engine + viral waitlist (/v1/waitlist/*). STAGED behind
		// CLOUD_BASE_EMBED. OwnsHealth: native /v1/base/health.
		{Name: "base", Mount: base.Mount, Shutdown: base.Shutdown, OwnsHealth: true},
		// The ONE observability subsystem — and the first one that is NOT linked in.
		// It runs as its own binary (cmd/o11y) and mounts at /v1/o11y over a private
		// unix socket; the whole read plane, the runtime handler, the OTLP collector
		// and the trace sink moved into that process untouched (it calls the same
		// o11y.MountO11y). Nothing about the ROUTES changed: the plugin's own in-order
		// registration still puts every specific /v1/o11y/* route ahead of the
		// hanzoai/o11y module wildcard, and NOT OwnsHealth still leaves /v1/o11y/health
		// the generic always-ok route Serve registers before MountAll — which therefore
		// still wins over this mount's /v1/o11y/* and answers without waking the child.
		//
		// No Shutdown: teardown moved with the resources. zip.Load registers its own
		// OnShutdown that stops the child, and the child flushes its collector/sink in
		// its own app.OnShutdown. The host has nothing left of o11y's to close.
		//
		// Why this one first: o11y is the heaviest app in the graph — the
		// otel-collector, prometheus and gonum are here and nowhere else — and it is
		// imported by NOTHING but this line, so unlinking it is a pure subtraction.
		//
		// It owns TWO prefixes: the /v1/o11y read plane and the /v1/sentry/* wildcard
		// mountSentry registers. Both are named here because zip.Load is variadic —
		// a prefix left out is not an error, it is a silent 404 on that subtree.
		cloud.PluginSpec("o11y", pluginAt("o11y"), "/v1/o11y", "/v1/sentry"),
		{Name: "authz", Mount: cloud.Global(authz.Mount), Global: true},
		// Embedded commerce plane /v1/commerce/*, /_/commerce/* — the hanzoai/commerce
		// MODULE via the adapter in commerce.go (un-forked; the in-process
		// CommerceClient is wired directly in pickCommerceClient).
		{Name: "commerce", Mount: cloud.Global(mountCommerce), Global: true},
		{Name: "licensing", Mount: cloud.Global(licensing.Mount), Global: true},
		// clients/plan.Mount. Enable id normalized "plans" -> "plan" to match the
		// package + generated cmd/plan (one subsystem, one name). Its product routes
		// stay /v1/plans/* (incl. the OwnsHealth /v1/plans/health probe) — unchanged.
		{Name: "plan", Mount: plan.Mount, OwnsHealth: true},
		{Name: "pricing", Mount: pricing.Mount, OwnsHealth: true},
		// /v1/s3/buckets/* + /v1/s3/health. Mounts BEFORE provisioning (120) so its static
		// routes win over provisioning's /v1/s3/:name. OwnsHealth (real fail-closed probe).
		{Name: "storage", Mount: storage.Mount, OwnsHealth: true},
		// Provisioning control plane: /v1/sql,/v1/vector,/v1/datastore,/v1/kv,/v1/search,/v1/s3,/v1/docdb.
		{Name: "provisioning", Mount: provisioning.Mount},
		{Name: "billing", Mount: billing.Mount},
		// Rolling AI-spend cap: installs the ai gate's per-tier trailing-window cap
		// reader (registers no routes; its admin-editable knobs are platform switches
		// surfaced in the /v1/admin/flags cockpit). After commerce/plan so the tier +
		// finance globals it composes are wired.
		{Name: "rollingcap", Mount: rollingcap.Mount},
		// CATCH-ALL /v1/billing/* + /v1/commerce/* data bridges — AFTER clients/billing
		// (121) + the commerce embed (100). Same clients/account package as "account" (48).
		{Name: "account-bridge", Mount: account.MountBridge},
		{Name: "do", Mount: do.Mount},
		{Name: "platform", Mount: platform.Mount, Shutdown: ctxShutdown(platform.Shutdown), OwnsHealth: true},
		{Name: "projects", Mount: projects.Mount, Shutdown: ctxShutdown(projects.Shutdown)},
		// The /v1/dns forward head: relays the console DNS dashboard to the DNS
		// control plane under the caller's own validated bearer (clients/dns).
		{Name: "dns", Mount: dns.Mount},
		// The registrar: search/price/register domains (name.com) per org.
		{Name: "domain", Mount: domain.Mount},
		{Name: "prompts", Mount: prompts.Mount},
		{Name: "agents", Mount: agents.Mount, Shutdown: agents.Shutdown},
		// The unified AI login manager registry (/v1/links). Mounts AFTER agents so
		// a link revoke can stop the affected agent sessions in-process.
		{Name: "link", Mount: link.Mount, Shutdown: link.Shutdown},
		{Name: "wallets", Mount: wallets.Mount, Shutdown: ctxShutdown(wallets.Shutdown)},
		// x402 pay-per-use: settles a signed ERC-3009 authorization to a recipient
		// wallet through the metering spine. Mounts AFTER wallets (it resolves the
		// recipient via wallets.ResolvePaymentTarget) and provides the Enforce
		// middleware a marketplace applies to its priced routes.
		{Name: "x402", Mount: x402.Mount, Shutdown: ctxShutdown(x402.Shutdown)},
		{Name: "paas", Mount: paas.Mount, OwnsHealth: true},
		// GitOps deploy dashboard /v1/deploy/* (the ArgoCD-grade fleet view over the
		// operator App CRs) — the second app that is NOT linked in. It runs as its own
		// binary (cmd/deploy) and mounts at /v1/deploy over a private unix socket.
		//
		// Why this one next: after o11y it is the largest EXCLUSIVE contributor to the
		// graph — 253 packages nothing else pulls, because it is the ONLY importer of
		// k8s.io/kubernetes/pkg and k8s.io/kubectl/pkg. Every other candidate shares
		// its weight with hanzoai/ai or hanzoai/commerce, so unlinking one frees single
		// digits. clients/deploy was imported by this file and nothing else.
		//
		// It stays AFTER paas: the position is now cosmetic (the plugin carries its own
		// paas mount, because the release seam is a func pointer and does not cross a
		// process boundary — see cmd/deploy), but the two are still read as a pair.
		//
		// Not OwnsHealth: the plugin serves /v1/deploy/health itself, and the generic
		// always-ok route Serve registers before MountAll still wins — the same
		// behaviour o11y has. No Shutdown: zip.Load registers its own OnShutdown that
		// stops the child.
		cloud.PluginSpec("deploy", pluginAt("deploy"), "/v1/deploy"),
		{Name: "functions", Mount: functions.Mount},
		{Name: "tracker", Mount: tracker.Mount},
		{Name: "templates", Mount: templates.Mount},
		// OSS-template compute-cost basis /v1/blueprint/* — parses a blueprint's
		// docker-compose into its SBOM (bill of container images) and prices the
		// stack's CPU/memory footprint through a documented rate card. The per-hour
		// rate the deploy path meters an org on, and the "~$X/mo to run" the console
		// shows; it is the compute cost the 20% author royalty (clients/authors) is
		// taken from. OwnsHealth: serves its own /v1/blueprint/health. Reference
		// content (embedded blueprints), no store → no Shutdown. After templates, its
		// sibling catalog concern; before the AI /v1/* catch-all.
		{Name: "blueprint", Mount: blueprint.Mount, OwnsHealth: true},
		{Name: "framework", Mount: framework.Mount, Shutdown: ctxShutdown(framework.Shutdown)},
		{Name: "knowledge", Mount: knowledge.Mount},
		// Hanzo Support PUBLIC plane /v1/help/* (help center KB read + customer ticket
		// intake) — the anonymous face the secure-by-default framework surface can't
		// serve. A framework lane like knowledge: its DocType fixtures register via
		// init() (help.Module), and this mounts the thin public subsystem. Owns no store
		// (delegates to the framework in-process API), so no Shutdown. After framework
		// (whose in-process API it calls at request time); before the AI /v1/* catch-all.
		{Name: "help", Mount: help.Mount},
		// Marketing content loop /v1/content/* (generate → CMS → transition → publish).
		// After framework (its DocType store the ops read/write) + knowledge (the sibling
		// framework lane); before the AI /v1/* catch-all so /v1/content/* resolves here.
		// CRUD/tenancy/install are framework's; this adds the board, lifecycle transition,
		// and the generate/publish orchestration over the zen5 + studio + social edges.
		{Name: "content", Mount: content.Mount, Shutdown: ctxShutdown(content.Shutdown)},
		// Reverse storefront loop: consume the commerce COMMERCE stream (product.created)
		// → content.EnsureCatalogAsset (render the new product's ecom asset, design==slug).
		// After content (whose EnsureCatalogAsset it drives). Inert until CLOUD_COMMERCE_NATS_URL
		// names the NATS carrying commerce catalog events — the reverse of the forward edge.
		{Name: "catalogsync", Mount: catalogsync.Mount, Shutdown: catalogsync.Shutdown},
		// The platform-global webhook layer: /v1/webhooks registry (per-org SQLite) +
		// ONE durable JetStream consumer that delivers ANY bus event to org-registered
		// HTTP subscribers. A bus consumer like catalogsync — placed adjacent to it and
		// after the commerce embed whose COMMERCE stream it reads. Fail-soft: the
		// registry serves even with the bus down; the consumer retries in the background.
		// Owns per-org store handles + a worker pool → Shutdown drains both.
		{Name: "webhooks", Mount: webhooks.Mount, Shutdown: webhooks.Shutdown},
		{Name: "ml", Mount: ml.Mount, OwnsHealth: true},
		{Name: "usage", Mount: usage.Mount},
		// Gamified usage analytics: /v1/usage/leaderboard + /v1/usage/activity + the
		// per-day contribution graph, over the datastore rollup (#43). Co-owns /v1/usage/*
		// with usage (a distinct concern — who leads + your activity graph) at its own
		// exact paths; owns the opt-in SQLite store. Before the ai /v1/* catch-all.
		{Name: "leaderboard", Mount: leaderboard.Mount, Shutdown: leaderboard.Shutdown},
		{Name: "crm", Mount: crm.Mount},
		// Native /v1/marketing/* — the in-process fold of github.com/hanzoai/marketing
		// (per-org campaign store on Base/SQLite), twin of crm. Owns a DB handle, so
		// its Shutdown closes it cleanly on SIGTERM (ctxShutdown adapts func() error).
		{Name: "marketing", Mount: marketing.Mount, Shutdown: ctxShutdown(marketing.Shutdown)},
		// Native /v1/ads/* — the net-new per-org ad-campaign store on Base/SQLite,
		// twin of crm/marketing. Owns a DB handle, so its Shutdown closes it cleanly
		// on SIGTERM (ctxShutdown adapts func() error).
		{Name: "ads", Mount: ads.Mount, Shutdown: ctxShutdown(ads.Shutdown)},
		// Top-level GTM orchestration /v1/campaign/* — the capability layer that fans a
		// campaign VALUE out to its channels (paid→ads, organic→publish, email→marketing),
		// each CONSUMING the connector plane via integrations.TokenFor. Metrics read from
		// the ONE analytics plane (never a second store); creative A/B composes the
		// experiment seam. The paid channel executor is wired in wire_seams.go. Owns a DB
		// handle, so its Shutdown closes it cleanly on SIGTERM.
		{Name: "campaign", Mount: campaign.Mount, Shutdown: ctxShutdown(campaign.Shutdown)},
		// GDA/SDM validator onboarding /v1/validators/* — wallet-sig + ETH-mainnet
		// GenesisNFT ownerOf → seal luxd staking identity into KMS → write a NEW-node
		// LuxNetwork CR (node.lux.cloud, never the live luxd) → enqueue an owner-gated
		// registration (never auto-submitted to any P-Chain). Owns a DB handle.
		{Name: "validators", Mount: validators.Mount, Shutdown: ctxShutdown(validators.Shutdown)},
		// Native /v1/social/* — the in-process fold of the live social stack
		// (github.com/hanzoai/social: social-backend/frontend/orchestrator, a Postiz-style
		// scheduler), a per-org accounts+posts store on Base/SQLite, twin of crm. Owns a DB
		// handle, so its Shutdown closes it cleanly on SIGTERM (ctxShutdown adapts func() error).
		{Name: "social", Mount: social.Mount, Shutdown: ctxShutdown(social.Shutdown)},
		{Name: "analytics", Mount: analytics.Mount, OwnsHealth: true},
		{Name: "git", Mount: git.Mount},
		// Universal sync (/v1/sync/links + engine). Registers the cloud.SyncEngine the
		// GitHub/Hanzo Git webhooks enqueue to; git is its first provider. Owns per-org
		// DB handles, so its Shutdown closes them on SIGTERM.
		{Name: "sync", Mount: sync.Mount, Shutdown: ctxShutdown(sync.Shutdown)},
		{Name: "visor", Mount: visor.Mount},
		// Connect-a-cloud-account plane /v1/cloud/*: an org links its DigitalOcean /
		// AWS / GCP / Azure accounts (labeled, KMS-sealed, keyless where possible),
		// Hanzo DISCOVERS each account's native Kubernetes clusters and FOLDS them into
		// the ONE fleet (clients/fleet.Register) — so they surface in visor's
		// /v1/clusters and run work like any BYO/managed cluster. No second registry.
		{Name: "venue", Mount: venue.Mount},
		// Cap table on Base via goja. STAGED behind CLOUD_ENABLE.
		{Name: "captable", Mount: captable.Mount, Shutdown: captable.Shutdown},
		{Name: "code", Mount: code.Mount, Shutdown: code.Shutdown},
		{Name: "zero-trust", Mount: zt.Mount},
		// ngrok-native public sharing: /v1/share/* provisions a per-org zrok
		// account so `hanzo share <port>` publishes a local service to a public
		// https://<token>.share.hanzo.ai URL. Fail-closed until ZROK_ADMIN_TOKEN.
		{Name: "share", Mount: share.Mount},
		// Data rooms via goja + per-tenant Base. STAGED behind CLOUD_ENABLE. OwnsHealth.
		{Name: "dataroom", Mount: dataroom.Mount, Shutdown: dataroom.Shutdown, OwnsHealth: true},
		{Name: "graph", Mount: graph.Mount},
		{Name: "security", Mount: security.Mount, Shutdown: ctxShutdown(security.Shutdown), OwnsHealth: true},
		{Name: "integrations", Mount: integrations.Mount, Shutdown: integrations.Shutdown},
		// Marketing destinations /v1/destinations/* — the native fan-out that
		// TRANSLATES the canonical /v1/event stream to each connected ad/analytics
		// platform (GA4 Measurement Protocol, Meta Conversions API, X/LinkedIn/TikTok/
		// Reddit) and forwards it server-side. Mounts AFTER analytics (whose fan-out
		// sink it installs) and integrations (a destination may reuse an OAuth
		// connection's token via integrations.TokenFor). Owns a DB handle → Shutdown.
		{Name: "destinations", Mount: destinations.Mount, Shutdown: ctxShutdown(destinations.Shutdown)},
		// First-class per-org Cloudflare asset plane /v1/cloudflare/{zones,pages,workers,
		// ai,r2,kv,d1}/* (sibling of /v1/dns, /v1/domain). Mounts AFTER integrations
		// because it reads the org's Cloudflare token through the integrations custody
		// seam (integrations.TokenFor) — one token, one custody boundary. Connecting the
		// provider stays on the integrations plane; this plane only MANAGES resources.
		{Name: "cloudflare", Mount: cloudflare.Mount},
		{Name: "sbom", Mount: sbom.Mount, OwnsHealth: true},
		{Name: "team", Mount: team.Mount, Shutdown: ctxShutdown(team.Shutdown)},
		{Name: "settings", Mount: settings.Mount, Shutdown: settings.Shutdown},
		{Name: "prefs", Mount: prefs.Mount, Shutdown: prefs.Shutdown},
		{Name: "notify", Mount: notify.Mount, OwnsHealth: true},
		{Name: "channels", Mount: channels.Mount, Shutdown: channels.Shutdown},
		{Name: "gateway", Mount: gateway.Mount},
		{Name: "entitlements", Mount: entitlements.Mount, Shutdown: entitlements.Shutdown},
		{Name: "exec", Mount: exec.Mount},
		{Name: "websearch", Mount: websearch.Mount},
		// The in-binary full-text index (/v1/index): a per-org inverted index on
		// Base/SQLite speaking the Meilisearch dialect, so a Meilisearch client
		// repoints at it unchanged. websearch above queries the OUTSIDE world;
		// this one indexes ours. NOT /v1/search — that path belongs to the
		// hanzoai/ai RAG plane, and the collision silently ate two routes.
		{Name: "index", Mount: index.Mount, Shutdown: ctxShutdown(index.Shutdown), OwnsHealth: true},
		{Name: "world", Mount: world.Mount, Shutdown: ctxShutdown(world.Shutdown)},
		// The bot runtime's ops face (/v1/bot/*). The transport itself is domain-free;
		// the run control plane is "bots" below.
		{Name: "runtime", Mount: runtime.Mount},
		{Name: "authors", Mount: authors.Mount, Shutdown: ctxShutdown(authors.Shutdown)},
		{Name: "bots", Mount: bots.Mount},
		{Name: "audit", Mount: auditlog.Mount},
		{Name: "affiliates", Mount: affiliates.Mount},
		// Hanzo e-signature via goja + per-tenant Base. Mounts under the default. OwnsHealth.
		{Name: "esign", Mount: esign.Mount, Shutdown: esign.Shutdown, OwnsHealth: true},
		{Name: "product", Mount: product.Mount},
		{Name: "evals", Mount: eval.Mount},
		{Name: "benchmark", Mount: benchmark.Mount},
		// The R&D EVIDENCE plane (HIP-0512) + its R&D Ops Board UI at /research —
		// the arena's sibling: benchmark measures, research is the versioned diary
		// every product's runs accrue into. Non-staged: mounts under the default.
		{Name: "research", Mount: research.Mount, Shutdown: ctxShutdown(research.Shutdown)},
		// The unified EXPERIMENT primitive (/v1/experiments): A/B testing as ONE value
		// whatever the variant kind (feature | ad creative | email | model). It is a
		// COMPOSITION — assignment via flags, measurement via analytics, evidence via
		// research — so it mounts AFTER all three. It owns only the experiment registry
		// store → Shutdown. clients/campaign composes it (experiments.Assign/Analyze).
		{Name: "experiments", Mount: experiments.Mount, Shutdown: ctxShutdown(experiments.Shutdown)},
		// The revenue BOOKS spine (/v1/books): a native double-entry general ledger that
		// reads commerce transactions (the sole posting source) and books the accounting twin —
		// plus bank import (PDF/OFX/CSV/Plaid/Teller), reconciliation, and the AI Ask brain.
		{Name: "books", Mount: books.Mount, Shutdown: ctxShutdown(books.Shutdown)},
		{Name: "treasury", Mount: treasury.Mount, Shutdown: ctxShutdown(treasury.Shutdown)},
		{Name: "admin", Mount: admin.Mount},
		// Launch-control gate (per-service waitlist): the COMPLETE feature — host→service
		// registry + brand seed + the waitlist.<svc> switch registration + the
		// /v1/flags/waitlist (and /v1/admission/mode compat) mode read + the Enforce
		// middleware — COMPOSING the flags engine one-way (flags.Bool/Register/
		// SetPlatformSwitch; flags never imports admission). Mounts AFTER flags so the
		// engine's platform-switch plane is installed first; the admin board is the
		// /v1/admin/services lens over it. Owns the registry store handle → Shutdown.
		{Name: "admission", Mount: admission.Mount, Shutdown: ctxShutdown(admission.Shutdown)},
		// Tasks: the durable workflow/UI surface AND platform cron (durable schedules
		// on the same shared engine, replacing every k8s CronJob). cron was a separate
		// Wire entry; it mounts no routes and only registers schedules, so it is folded
		// in as a sub-mount of tasks.Mount — ONE tasks subsystem.
		{Name: "tasks", Mount: tasks.Mount},
		// Automations: the connector catalogue + flow engine AND native single-connector
		// execution (POST /v1/automations/connectors/:id/run, HIP-0126). The connector
		// runner mounts no other routes, so it is folded in as a sub-mount of
		// automations.Mount (was a separate "connectorruntime" entry) — ONE subsystem.
		{Name: "automations", Mount: automations.Mount, Shutdown: automations.Shutdown},
		// Unified tool plane: /v1/tools/* — the ONE registry (connectors, functions,
		// agents, skills, external MCP servers, full-cloud-control /v1 routes), per-org
		// activation, and the unified MCP endpoint. Sources register into it from their
		// own Mounts, so mount position is not load-bearing (List/Dispatch run at request
		// time); placed after automations, before the zen/ai catch-all so /v1/tools wins.
		{Name: "tools", Mount: tools.Mount, Shutdown: tools.Shutdown},
		// Marketplace: /v1/marketplace/* — listing/discovery/install over the tool plane,
		// with x402-priced monetized listings. Mounts after tools (it fills the price seam).
		{Name: "marketplace", Mount: marketplace.Mount, Shutdown: marketplace.Shutdown},
		{Name: "referrals", Mount: referrals.Mount},
		// Business AI Guide /v1/guide/* — the interactive launch checklist engine +
		// the agent that executes a step through the per-principal MCP plane. After
		// automations (whose InvokeTool it drives) and referrals; before the ai
		// catch-all. Owns per-org SQLite, so its Shutdown closes the stores.
		{Name: "guide", Mount: guide.Mount, Shutdown: ctxShutdown(guide.Shutdown)},
		// Hanzo Company — the incorporation + fundraising state machine
		// (/v1/company/*). Mounts after the seams it composes (integrations for the
		// google token custody; captable/dataroom facades) and before the /v1/* AI
		// catch-all so its routes resolve here.
		{Name: "company", Mount: company.Mount, Shutdown: company.Shutdown},
		// The corporate back-office surfaces — orthogonal to company/captable/billing:
		// Hanzo Compliance (/v1/compliance) orchestrates KYC/KYB verification providers
		// + tracks accreditation state + surfaces the SOC 2 audit posture; Hanzo Legal
		// (/v1/legal) is the versioned template + generation engine + e-sign/filing seams.
		// Both are TOOLING with providers/professionals in the loop, never advice or
		// certification. They mount before the /v1/* AI catch-all so their routes resolve.
		{Name: "compliance", Mount: compliance.Mount, Shutdown: ctxShutdown(compliance.Shutdown), OwnsHealth: true},
		{Name: "legal", Mount: legal.Mount, Shutdown: ctxShutdown(legal.Shutdown), OwnsHealth: true},
		// Chat orchestrator — POST /v1/chat: ONE LLM tool-calling round over the tool
		// plane. It COMPOSES the ai completion path (in-process, so per-org billing
		// runs) + the unified tool registry, and splits the model's tool calls into
		// server-executed actions and client-applied ops. Mounts BEFORE the zen/ai
		// catch-all so /v1/chat resolves here (Fiber first-match); the ai module's
		// beego /v1/chat alias behind its /v1/* glob is thereby shadowed, while ai
		// keeps /v1/chat/completions + /v1/completions.
		{Name: "agent", Mount: cloud.Global(agent.Mount), Global: true},
		// The UNIFIED GROUNDED ADVISOR — POST /v1/ask. DISTINCT from /v1/chat/completions
		// (ai's RAW model) and /v1/agent (tool-calling): it routes a plain-language question
		// to the domain(s) that can GROUND it, reads the REAL figures from each domain's own
		// endpoint IN-PROCESS under the caller's own creds (agent.go's replay pattern, so
		// per-tenant isolation is inherited), hands the model the EXACT figures, and returns
		// the grounded answer + figures + the domain reads that backed them. The model NEVER
		// invents a figure. Contributors plug in via a registry (books today; o11y/billing
		// next) WITHOUT a router edit. Mounts BEFORE the ai /v1/* catch-all so /v1/ask wins
		// Fiber's first-match; after books/agent so the domains it composes are wired.
		{Name: "ask", Mount: ask.Mount},
		// POST /v1/translate (HIP-0516) — the ONE translation surface: a quality tier
		// on the model plane and a bulk tier on MADLAD-400, behind one endpoint, one
		// auth path, one meter. It COMPOSES the model plane (deps.AI, which already
		// gates + debits its own tokens) rather than standing up a second inference
		// stack, and owns only its per-org translation memory → Shutdown. Mounts
		// BEFORE the zen/ai catch-all so /v1/translate resolves here.
		{Name: "translate", Mount: translate.Mount, Shutdown: ctxShutdown(translate.Shutdown)},
		// The bare /v1/* AI catch-all — the LAST route position. Every owning subsystem above
		// wins its own namespace (Fiber first-match); AI is the fallback for the rest of /v1/*.
		// zen mounts as a /v1-scoped Claim middleware BEFORE ai: it routes zen* models
		// to zen's serving layer in-process (identity, tools, 1M ladder, codec) and
		// c.Next()s everything else to ai. zen owns the zen family; ai owns every
		// other model and the /v1/models list. Order is load-bearing — Claim must
		// run before ai's catch-all. (See hip-00NN.)
		{Name: "zen", Mount: mountZen, Prefixes: []string{"/v1"}},
		{Name: "ai", Mount: cloud.Global(mountAI), Global: true},
		// Runtime wasm/proxy plugins — mounts dead last.
		{Name: "plugins", Mount: plugin.Mount},
	}
}

// ServeSingle is the ONE way to run a single app standalone: validate `name`
// against Wire(), then serve exactly it (cloud.Serve with a one-name enable
// list — MountAll mounts only it). It is the path `hanzo <name>` already uses;
// promoting it here lets each cmd/<app>/main.go stub reuse it instead of
// re-implementing the dispatch, so adding an app in Wire() is still the one edit
// and its standalone binary comes for free (generated). Returns an error for an
// unknown name rather than booting a no-op.
func ServeSingle(name string) error {
	if name == "" {
		return fmt.Errorf("ServeSingle: empty app name")
	}
	for _, spec := range Wire() {
		if spec.Name == name {
			return cloud.Serve(Wire(), []string{name})
		}
	}
	return fmt.Errorf("ServeSingle: unknown app %q — run `hanzo code ls`/`hanzo` for the list", name)
}

// pluginAt says where to find the binary for the plugin named name. Its two knobs
// are DERIVED from the name, so a plugin's configuration cannot disagree with its
// identity and adding one invents no new env var to document:
//
//	CLOUD_<NAME>_ADDR — already listening there; start nothing, just mount it.
//	CLOUD_<NAME>_BIN  — the binary's path on disk.
//
// They map 1:1 onto zip.Plugin's own fields, so there is no third notion of "where
// a plugin is" and nothing to translate.
//
// The default is a file named <name> beside the running cloud binary, which is the
// container layout: every binary in the image, still one artifact to ship.
// Resolving it from os.Executable rather than $PATH means a host always loads the
// plugin it was built and shipped with, not whichever one a PATH happens to find.
func pluginAt(name string) zip.Plugin {
	env := "CLOUD_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	if addr := strings.TrimSpace(os.Getenv(env + "_ADDR")); addr != "" {
		return zip.Plugin{Addr: addr}
	}
	path := strings.TrimSpace(os.Getenv(env + "_BIN"))
	if path == "" {
		path = name
		if self, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(self), name)
		}
	}
	return zip.Plugin{Path: path}
}

// mountMetrics adapts hanzoai/metrics into a cloud.MountFunc. Unlike the other
// externals, metrics declares its OWN narrow Deps (Logger, DataDir, Brand) and does
// not import hanzoai/cloud, so cloud.Typed cannot bridge it: the composition root
// builds metrics.Deps from cloud.Deps and calls metrics.Mount explicitly here.
func mountMetrics(a *zip.App, deps cloud.Deps) error {
	return metrics.Mount(a, metrics.Deps{Logger: deps.Logger, DataDir: deps.DataDir, Brand: deps.Brand})
}

// ctxShutdown adapts a subsystem's zero-arg Shutdown() error to the
// cloud.ShutdownFunc(ctx) signature. Several subsystems expose the simpler form
// (their teardown ignores the deadline); this bridges the impedance mismatch in ONE
// place so the Wire entries stay declarative — no inline closures.
func ctxShutdown(f func() error) cloud.ShutdownFunc {
	return func(context.Context) error { return f() }
}
