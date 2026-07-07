// Package subsystems is the single source of truth for which Hanzo cloud
// subsystems are linked into a binary.
//
// Blank-importing this package pulls every subsystem into the build graph;
// each one registers a cloud.MountSpec into cloud.Registry from its own
// init(). Registration is unconditional — a plain `go build ./cmd/cloud`
// (no build tags) links and mounts the full set. There is no //go:build
// gate on any subsystem.
//
// Both entrypoints — cmd/cloud (the full fused surface) and cmd/hanzo (the
// subcommand dispatcher) — blank-import THIS package and nothing else. The
// subsystem set is therefore defined ONCE, here; adding or removing a
// subsystem is a one-line change in one file, never duplicated per binary.
//
// (This package must NOT live in the root `cloud` package: the subsystems
// import `cloud` for Deps + Register, so a root-package bundle would form an
// import cycle. As a sibling subpackage it composes them without one.)
package subsystems

// The unified `cloud` binary is the APPLICATION layer plus the embedded KMS
// secrets plane and the embedded IAM identity plane (HIP-0106 "all Go embeds in
// cloud" — "one Go binary embeds IAM + KMS + o11y"). The remaining
// infrastructure/edge subsystems run as their own deployments, NOT fused in:
//   - mcp   → its own deployment             — tool surface
//   - gateway, ingress → the edge            — they route *to* this binary
//   - amqp  → removed (unused)
// Keeping those separate preserves blast-radius isolation and independent
// scaling for the security/edge tier.
//
// KMS is embedded in-process (clients/kmssvc mounts /v1/kms/* backed by
// clients/kms, replacing the legacy Infisical fork). Its master key is
// injected by the operator via a K8s Secret env; absent it the subsystem serves
// fail-closed health-only.
//
// IAM is embedded in-process (clients/iamsvc mounts /v1/iam/* + /.well-known/* +
// /login/oauth/* + /_/iam/* + /cas/* + /scim/* by wrapping IAM's own Beego
// handler via iamserver.Init — the LAST binary-consolidation piece). It is the
// identity authority (order 50, mounts before its dependents). ACTIVATION IS
// STAGED: the operator adds "iam" to the deployment's --enable only AFTER IAM's
// config (Beego app.conf + env + KMS signing keys) is present in the cloud
// runtime and the fold is verified; until then hanzo.id is served by the
// standalone iam pod via ingress. See clients/iamsvc.
import (
	_ "github.com/hanzoai/ai"        // order 150
	_ "github.com/hanzoai/authz"     // order 70
	_ "github.com/hanzoai/base"      // order 60
	_ "github.com/hanzoai/commerce"  // order 100
	_ "github.com/hanzoai/licensing" // order 110
	_ "github.com/hanzoai/metrics"   // order 40
	_ "github.com/hanzoai/o11y"      // order 70
	_ "github.com/hanzoai/vfs"       // order 20

	// Embedded KMS secrets plane (HIP-0106): mounts /v1/kms/* backed by the
	// in-process luxfi/kms SecretStore under CLOUD_DATA_DIR. Registered as
	// "kmssvc" (order 10) so the real /v1/kms/health probe is not shadowed by the
	// generic liveness route; secret ops fail closed until the operator injects
	// CLOUD_KMS_MASTER_KEY_REF.
	_ "github.com/hanzoai/cloud/clients/kmssvc" // order 10 — /v1/kms/*
	_ "github.com/hanzoai/cloud/clients/pubsub" // order 5 — embedded NATS :4222 + JetStream
	_ "github.com/hanzoai/cloud/clients/kafka"  // order 6 — embedded Kafka adaptor :9092

	// Embedded IAM identity plane (HIP-0106, the LAST binary-consolidation piece):
	// wraps IAM's own Beego handler (iamserver.Init) and mounts /v1/iam/* (API +
	// OAuth + OIDC + login), /.well-known/* (root OIDC/JWKS), /login/oauth/*,
	// /_/iam/*, /cas/*, /scim/*. Order 50 — the identity authority, mounts before
	// dependents. Auth semantics (authorize clientId org-resolution, JWT audiences,
	// SuperAdmin owner=="admin", argon2id hashing) are IAM's, unchanged. Activation
	// is the enable-list gate — do NOT add "iam" to the live --enable until IAM
	// config is present + the fold is verified (staged cutover from the standalone
	// iam pod). See clients/iamsvc.
	_ "github.com/hanzoai/cloud/clients/iamsvc" // order 50 — /v1/iam/*, /.well-known/*, /login/oauth/*, /_/iam/*, /cas/*, /scim/*

	// Node-service subsystems hosted in-process via base+goja (HIP-0106);
	// the JS + catalog data live in hanzoai/plans, hanzoai/pricing.
	_ "github.com/hanzoai/cloud/clients/bot"       // order 143 — /v1/bot/* (reverse proxy → bot-gateway)
	_ "github.com/hanzoai/cloud/clients/eval"      // order 145 — /v1/evals/*
	_ "github.com/hanzoai/cloud/clients/exec"      // order 140 — /v1/exec,/v1/upload,/v1/download,/v1/files (Code Interpreter → sandbox)
	_ "github.com/hanzoai/cloud/clients/observe"   // order 44 — /v1/o11y/{logs,metrics,status} (scoped) + /v1/settings/* (console product-detail data plane, #59)
	_ "github.com/hanzoai/cloud/clients/plan"      // order 111 — /v1/plans/*
	_ "github.com/hanzoai/cloud/clients/plugin"    // order 900 - runtime wasm/proxy plugins (goa wasm + ZAP proxy)
	_ "github.com/hanzoai/cloud/clients/pricing"   // order 112 — /v1/pricing/*
	_ "github.com/hanzoai/cloud/clients/websearch" // order 141 — /v1/websearch/* (SearXNG+Firecrawl-compat over Hanzo search+crawl)

	// S3 object-storage DATA plane: the org-scoped /v1/s3 file manager (buckets +
	// objects) over the shared SeaweedFS S3 gateway. Order 118 (< provisioning's
	// 120) so its static /v1/s3/buckets + /v1/s3/health register BEFORE
	// provisioning's /v1/s3/:name and win Fiber's first-match scan; registered as
	// "s3svc" so the generic health route does not shadow the real fail-closed
	// /v1/s3/health. It COMPLEMENTS provisioning (which owns the s3 RESOURCE
	// lifecycle at /v1/s3 + /v1/s3/:name) — both derive a tenant's physical bucket
	// name identically (provisioning.PhysicalName) so a provisioned bucket is
	// browsable here.
	_ "github.com/hanzoai/cloud/clients/s3" // order 118 — /v1/s3/buckets/*,/v1/s3/health

	// Provisioning control plane: creates logical resources (sql, vector,
	// datastore, kv, search, s3, docdb) inside the live shared backends.
	_ "github.com/hanzoai/cloud/clients/provisioning" // order 120 — /v1/sql,/v1/vector,/v1/datastore,/v1/kv,/v1/search,/v1/s3,/v1/docdb

	// DigitalOcean-native infra plane: the org-scoped /v1/vpcs + /v1/load-balancers
	// surface over the digitalocean/godo SDK (DO is Hanzo's EXCLUSIVE cloud venue).
	// DO is a single account, so tenant isolation is a name prefix — a resource's
	// physical DO name is "o"<orgHash>-<friendly> (provisioning.BucketName, the SAME
	// org-hash convention S3 uses); list/get/delete filter to the caller's prefix so
	// no tenant sees another's. Fails closed (503) without DO_API_TOKEN. Backs the
	// console's VPC + Load Balancers pages (moving them off the /paas proxy).
	_ "github.com/hanzoai/cloud/clients/do" // order 123 — /v1/vpcs/*,/v1/load-balancers/*

	// Console standalone surface: the native Go port of console's two NON-proxy
	// server routes (app/keys + app/onboard) — mint/revoke the user's `hk-` Cloud
	// API key and create the user's org — done as the confidential `hanzo-console`
	// IAM client on the VALIDATED caller's behalf. Porting these lets console drop
	// its last stateful Node handlers and be statically exported (task #41, "True
	// 1-binary FE"): the embedded SPA calls /v1/console/* on its own origin.
	_ "github.com/hanzoai/cloud/clients/console" // order 122 — /v1/console/keys,/v1/console/onboard

	// Customer-facing, org-scoped billing READS: /v1/billing/{usage,balance}. On the
	// console host the ingress sends /v1/* to cloud-api directly (the Next BFF is only
	// at "/"), so the console's billing calls land here; this proxies the caller's OWN
	// org ledger from commerce (org = validated owner claim; the all-orgs god view stays
	// admin-only in clients/admin). Without it every product overview + o11y usage panel
	// 403s ("Access required").
	_ "github.com/hanzoai/cloud/clients/billing" // order 121 — /v1/billing/{usage,balance} (customer, org-scoped)

	// Projects control plane: the ONE org-scoped store of buildable/deployable
	// sites, shared by hanzo.app (builder) and console.hanzo.ai (Projects), plus
	// the deploy pipeline (artifact/git → OUR S3 → live URL).
	_ "github.com/hanzoai/cloud/clients/projectsvc" // order 125 — /v1/projects/*

	// Tracker control plane: the native-Go, per-org issue tracker (projects +
	// issues) on SQLite — the durable replacement for the Huly/Svelte hanzo.team
	// tracker whose upstream reactive-batching render race left issue lists
	// rendering zero rows. Native @hanzo/gui over this one store sidesteps that
	// entire class of bug (rows come back as plain JSON and render deterministically).
	_ "github.com/hanzoai/cloud/clients/tracker" // order 129 — /v1/tracker/*

	// PaaS control plane: the native, in-process port of the standalone Dokploy
	// platform's deploy lifecycle. Reads the operator `Service` CR fleet as the
	// declared/running/drift board and deploys by merge-patching a CR's
	// `.spec.image` (the operator reconciles the rollout) — the ONE deploy path.
	// Global-admin only; the user-facing view lives in console.
	_ "github.com/hanzoai/cloud/clients/paassvc" // order 128 — /v1/paas/*

	// Platform (PaaS) control plane — PER-ORG, user-facing: the native Go port of
	// the standalone Dokploy (platform.hanzo.ai) tRPC backend. Users create
	// projects + applications, build them (arcd BuildKit) and deploy them
	// (operator hanzo.ai/v1 Service CR into their OWN tenant-<org> namespace).
	// Complements paassvc (admin fleet board) and projectsvc (static sites): this
	// is the container-app PaaS. Every route is org-scoped by the validated
	// X-Org-Id and the deploy namespace is DERIVED from it (tenant-<org>), never
	// taken from the request — the cross-tenant isolation boundary. Order 124
	// binds /v1/platform/* before projectsvc (125) and the AI catch-all (150).
	_ "github.com/hanzoai/cloud/clients/platform" // order 124 — /v1/platform/*

	// Product control planes: per-org, Base/SQLite-backed application surfaces
	// mounted natively in the cloud binary (the "all products in the cloud
	// binary" thesis). Each is org-scoped by the gateway-minted X-Org-Id.
	// clients/prompts is the red-approved, versioned prompt library and the ONE
	// owner of /v1/prompts/* (it supersedes the earlier clients/prompt facade).
	_ "github.com/hanzoai/cloud/clients/affiliates" // order 144 — /v1/affiliates/* + /v1/admin/affiliates* (partner-commission loop: ongoing commission via the commerce ledger)
	_ "github.com/hanzoai/cloud/clients/agents"     // order 127 — /v1/agents/*
	_ "github.com/hanzoai/cloud/clients/analytics"  // order 132 — /v1/analytics/* (native-Go analytics on datastore/ClickHouse: per-org LLM usage + web/commerce lenses)
	_ "github.com/hanzoai/cloud/clients/authors"    // order 143 — /v1/authors/* + /v1/admin/authors* (creator loop: OSS-author deploy royalty via the commerce ledger)
	_ "github.com/hanzoai/cloud/clients/crm"        // order 131 — /v1/crm/* (native-Go CRM on Base: companies/contacts/opportunities)
	_ "github.com/hanzoai/cloud/clients/referrals"  // order 149 — /v1/referrals/* + /v1/admin/referrals* (viral loop: promo credit via commerce ledger)
	// The native treasury: the platform's OWN double-entry reserve fund, one layer
	// ABOVE the per-org commerce credit ledger. A revenue-share policy accrues a %
	// of net platform revenue INTO the fund; the referral/affiliate/author payouts
	// DEBIT the fund (treasury.Reserve) so every payout is backed by funded capital,
	// never unbounded minting. The double-entry engine (clients/treasury/ledger) is
	// store-agnostic and cloud-decoupled — the seed of the native hanzoai/finance
	// central ledger (the Go replacement for the Formance stack). Order 146 (with the
	// admin surface); its routes are specific so they bind ahead of the AI catch-all.
	_ "github.com/hanzoai/cloud/clients/treasury" // order 146 — /v1/finance/* + /v1/admin/finance/* (scope-aware finance engine: reserve fund + backed payouts, native/Formance ledger of record, Hanzo L1 anchored)
	// The Hanzo Framework: a metadata-driven DocType engine (Frappe's DocType/
	// metadata core, rebuilt native in Go on Base). It is the FOUNDATION that
	// CMS content-types, ERPNext DocTypes, and Helpdesk become "just DocTypes"
	// on — ONE engine + ONE generic UI renders every business app. Per-org on
	// Base/SQLite, org derived ONCE via principal.Tenant (no Frappe/Python
	// runtime dep). Order 129 binds /v1/framework/* before the AI /v1/* catch-all.
	_ "github.com/hanzoai/cloud/clients/framework" // order 129 — /v1/framework/* (DocType engine)

	// The CMS app lane: DocType fixtures (Page/Post/Article/Media/Navigation/
	// Author, module "cms") registered with the framework at init. It mounts NO
	// HTTP surface of its own — CMS content IS documents on /v1/framework/*,
	// installed per-org via /v1/framework/modules/cms/install. First lane on the
	// engine; ERP/Helpdesk register the same way.
	_ "github.com/hanzoai/cloud/clients/cms" // (no order) — registers the "cms" framework module

	// The ERP app lane: ERPNext-core DocType fixtures (item/warehouse/sales-order/
	// sales-invoice/stock-entry/journal-entry/…, module "erp") + native-Go business
	// hooks (line/document totals, GL posting on invoice/journal/payment submit,
	// stock ledger on stock-entry submit, double-entry gates) registered with the
	// framework at init. No HTTP surface of its own — ERP IS documents on
	// /v1/framework/*, installed per-org via /v1/framework/modules/erp/install.
	_ "github.com/hanzoai/cloud/clients/erp" // (no order) — registers the "erp" framework module + hooks

	// The Help Center app lane: Frappe Helpdesk-core DocType fixtures (hd-ticket/
	// hd-agent/hd-team/hd-sla/hd-canned-response, module "help") registered with the
	// framework at init. Pure fixtures (no hooks); installed per-org via
	// /v1/framework/modules/help/install. Third lane on the engine.
	_ "github.com/hanzoai/cloud/clients/help" // (no order) — registers the "help" framework module

	// The Knowledge Base + unified AI-memory app lane: DocType fixtures (kb-page
	// wiki tree via a self-Link `parent`, kb-memory agent memory, kb-source ingested
	// docs, kb-connector connection metadata; module "kb") registered with the
	// framework at init, PLUS an after_save hook that upserts every knowledge write
	// to the org's vector namespace (index.go) — human wiki + AI memory are ONE
	// per-org knowledge store, indexed once. Unlike the pure-fixture lanes it also
	// mounts a thin control-plane subsystem (order 130): POST /v1/kb/search (the RAG
	// retrieval entry point) and /v1/kb/connectors/* (Slack/GitHub/Google OAuth that
	// ingest external docs INTO the same store + index; tokens in KMS, never plaintext).
	_ "github.com/hanzoai/cloud/clients/kb" // order 130 — "kb" framework module + hooks + /v1/kb/*

	// Team control plane (HIP-0106, task #45): the native-Go port of hanzo team-go
	// into the cloud binary — the SPA READ PLANE + bots-as-members. Mounts the
	// account API (/v1/team/account, JSON-RPC login + workspace selection over the
	// IAM OAuth bridge), the transactor data-plane WebSocket (/v1/team/transactor/
	// :token, ZAP-envelope frames over zip's wsx, serverVersion pinned 0.6.0), and
	// the bots read routes (/v1/team/bots, /v1/team/bots/sync). Bots-as-members are
	// sourced IN-PROCESS from the canonical agents registry (agents.ListForOrg) and
	// projected as workspace Employees — no IAM-SA HTTP hop. Every data path derives
	// its org from a VERIFIED token claim / principal.Tenant, never a client header.
	// Order 138 binds /v1/team/* before the AI /v1/* catch-all (150).
	_ "github.com/hanzoai/cloud/clients/team" // order 138 — /v1/team/*

	// order 140 — /v1/auto/* per-org reverse proxy to the standalone Hanzo Auto engine
	// (workflows + the connector catalog, in-platform, scoped to the validated tenant).
	_ "github.com/hanzoai/cloud/clients/auto"

	_ "github.com/hanzoai/cloud/clients/functions" // order 128 — /v1/functions/*
	_ "github.com/hanzoai/cloud/clients/git"       // order 132 — /v1/git/* (S3-backed native Git hosting; smart-HTTP clone/push)
	_ "github.com/hanzoai/cloud/clients/prompts"   // order 126 — /v1/prompts/*
	_ "github.com/hanzoai/cloud/clients/tasksvc"   // order 147 — /v1/tasks/*, /_/tasks/* (Hanzo Tasks HTTP+UI on the shared in-process durable engine)
	_ "github.com/hanzoai/cloud/clients/templates" // order 129 — /v1/templates/* (starter-kit gallery, read-only)
	_ "github.com/hanzoai/cloud/clients/visor"     // order 133 — /v1/machines/*,/v1/gpus/*,/v1/clusters/* (compute → Visor)

	// Networking control plane: tenant-scoped facade over Hanzo Zero Trust
	// (hanzoai/zt, an OpenZiti fabric). Fronts the controller's Edge Management API
	// and serves the console's Networks/ServiceMesh/Edge pages: /v1/networks (the
	// org's overlay, projected from its edge-routers), /v1/mesh/services (ZT edge
	// services) and /v1/edge/nodes (ZT edge-routers + real online status). Every
	// resource is org-scoped by the "org-<org>" role attribute (the ONE tenancy
	// convention ZT expresses natively), so a caller only ever sees their own
	// footprint. Fails closed (503) without ZT_CLIENT_ID/ZT_CLIENT_SECRET.
	_ "github.com/hanzoai/cloud/clients/zt" // order 134 — /v1/networks/*,/v1/mesh/services,/v1/edge/nodes (networking → Hanzo Zero Trust)

	// Chain-data control plane: principal-gated facade over the Lux chain-data plane
	// (luxfi/indexer explorer REST + luxfi/graph GraphQL). Serves the console's
	// Indexer and Oracles pages: /v1/indexers (the deployment's per-network indexing
	// status — chain/network/height/health from the indexer's /health + latest block)
	// and /v1/oracles (on-chain price feeds from the graph's O-Chain PriceFeed
	// registry). Chain data is a public ledger scoped per brand (each brand's cloud is
	// wired to its OWN indexer/graph), so the tenancy boundary is principal-gating (no
	// unauth read); honest 502 when an upstream is unreachable, never a fabricated row.
	_ "github.com/hanzoai/cloud/clients/graph" // order 135 — /v1/indexers,/v1/oracles (chain data → luxfi indexer + graph)

	// Security control plane: the native code-security surface — a dependency-free
	// in-process secrets scanner (pattern + Shannon-entropy) over caller-submitted
	// source, org-scoped findings persisted under DataDir, one metered unit per
	// scan, audit-emitting. The first Semgrep-class capability shipped natively;
	// findings store the redacted preview + SHA-256 fingerprint, NEVER the secret.
	_ "github.com/hanzoai/cloud/clients/security" // order 136 — /v1/security/*

	// Connectors control plane: the generic, provider-agnostic OAuth connector
	// framework. One registry, N providers (Slack live; GitHub scaffolded;
	// Google/Salesforce plug into the SAME registry later). Per-org customer tokens
	// go to KMS custody; the callback is state-authed (HMAC + single-use nonce), the
	// org derived ONLY from the signed state. Uses the GENERIC
	// /v1/integrations/{provider}/callback — it MUST NOT mount any /v1/slack/* route
	// (team-go owns /v1/slack/*). Order 137 binds after security (136), before the
	// AI /v1/* catch-all (150).
	_ "github.com/hanzoai/cloud/clients/integrations" // order 137 — /v1/integrations/*

	// Notify send surface (HIP-0106 fold of the standalone notifyd): mounts the
	// native /v1/notify/send OTP path in-process. Reuses notifyd's OWN public
	// provider packages (github.com/hanzoai/notify/service/*); the async Temporal
	// notify-send queue plane is intentionally NOT folded.
	_ "github.com/hanzoai/cloud/clients/notify" // order 139 — /v1/notify/send (+ /sms,/email,/health)

	// Connectors+Automations engine (HIP-0106, task #51): the /v1/automations/*
	// surface — org-scoped flows that run durably on the ONE shared in-process tasks
	// engine, invoking Tier-A connectors whose per-org credentials are custodied by
	// clients/integrations (KMS-sealed, reached ONLY via integrations.TokenFor). ONE
	// SQLite file, org column on every table; the durable activity's sole credential
	// scope is the VALIDATED FlowRunInput.Owner. Order 148 binds after integrations
	// (137) and BEFORE the AI /v1/* catch-all (150) so /v1/automations/* wins. Also
	// serves the HIP-0300 MCP tool surface (/v1/automations/mcp) that /v1/agents calls.
	_ "github.com/hanzoai/cloud/clients/automations" // order 148 — /v1/automations/*

	// ML/Train control plane: tenant-scoped k8s bridge fronting the kubeflow
	// forks (kserve InferenceService, trainer TrainJob, katib Experiment).
	_ "github.com/hanzoai/cloud/clients/ml" // order 130 — /v1/ml/*,/v1/train/*

	// Console Search/Vector product panels (browser-facing read surface).
	_ "github.com/hanzoai/cloud/clients/product" // order 145 — /v1/search-docs/*, /v1/vector/*

	// God-mode admin surface for the Hanzo Admin Console (admin.hanzo.ai). Fans
	// out to IAM (identity), commerce (billing) and o11y (health); global-admin
	// only, fail-closed.
	_ "github.com/hanzoai/cloud/clients/admin" // order 146 — /v1/admin/*

	// Installs the o11y runtime handler (reverse proxy to the dedicated o11y
	// Deployment) so hanzoai/o11y's /v1/o11y/* surface serves real telemetry
	// instead of the "runtime not initialized" 503.
	_ "github.com/hanzoai/cloud/clients/o11y" // order 71 — installs o11y.SetHandler
	// The console SPA is go:embed'd and served at "/" by webui.go's
	// mountConsole, called directly from Serve AFTER every /v1/* route mounts
	// (so real API routes always win; unmatched paths fall back to the SPA).
	// That is the "one binary" endgame — the unified cloud binary IS the
	// frontend too (Hanzo V8: Open Edition). It needs no import here; it is
	// wired in Serve, not registered as a subsystem.
)
