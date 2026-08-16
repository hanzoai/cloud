// Copyright © 2026 Hanzo AI. MIT License.

// Package commerce is selling: checkout, subscriptions, invoices, spend alerts,
// payment webhooks and the storefront catalog.
//
// It is the merchant half, embedded from hanzoai/commerce and mounted on cloud's
// own router. It is not the wallet — EmbedConfig.Ledger injects apps/finance, so a
// credit minted here lands in the one ledger of record.
//
// This file mounts that MODULE into a cloud binary (HIP-0106) via the NATIVE
// co-residence contract: commerce registers its routes
// directly on the HOST's zip app (EmbedConfig.App) — one router, one specificity
// space, zero handler adaptation. This adapter narrows cloud.Deps, boots the
// embed, and wires the in-process seams. Direction is one-way: cloud → commerce.
//
// PCI SCOPE. Commerce is a LIGHT ROUTER, NOT in PCI-DSS scope: tokens + intent IDs
// only, NEVER a PAN. PAN-touching paths call the out-of-process Payments / Vault
// (ZAP-RPC); when those clients are absent the payment handlers fail closed while
// tenant config + admin stay served — Mount warns loudly at startup.
//
// FAIL-SOFT. A broken Embed does NOT crash the binary: commerce degrades to a 503
// on its own prefixes while every co-resident subsystem stays up.
package commerce

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	accountclient "github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	financeclient "github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/principal"
	commercemod "github.com/hanzoai/commerce"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	catalogapi "github.com/hanzoai/commerce/api/catalog"
	planapi "github.com/hanzoai/commerce/api/plan"
	commerceresources "github.com/hanzoai/commerce/api/resources"
	commercestore "github.com/hanzoai/commerce/api/store"
	"github.com/hanzoai/commerce/billing/paywall"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	commercemid "github.com/hanzoai/commerce/middleware"
	"github.com/hanzoai/commerce/middleware/iammiddleware"
	commercensctx "github.com/hanzoai/commerce/util/nscontext"
	"github.com/hanzoai/commerce/util/permission"
	sqlitedrv "github.com/hanzoai/sqlite"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func init() {
	// In-process CommerceClient factory — pickCommerceClient calls it when the
	// commerce subsystem is enabled. Registered HERE (not called directly from
	// package cloud) because the commerce client's entitlement client imports
	// clients/plan, which imports cloud: the hook keeps the package graph acyclic.
	cloud.RegisterCommerceClientFactory(func(cfg *cloud.Config, _ luxlog.Logger) cloud.CommerceClient {
		return InProcessClient(cfg.Brand)
	})
}

// Prefixes is every root path the commerce surface owns on the shared
// app. Under the native SharedApp contract most of these are registered by
// commerce's own setupRoutes; the list is the fail-closed 503 set AND the wire
// contract prefix_test pins — the route families a session gate or the
// AI /v1/* catch-all must never swallow.
//
// It is exported because the composition root DECLARES it (apps.Wire's commerce
// entry, Prefixes: commerce.Prefixes), which is what puts commerce on the light
// host's manifest. Derived instead, the walk would read the `app.Group("/v1")`
// this file opens for the store/catalog/plan bundle as a claim on ALL of /v1 and
// hand commerce every request in the fleet. Same list, one owner, stated once.
var Prefixes = []string{
	"/v1/commerce", // public checkout + tenant + catalog + deposits
	"/_/commerce",  // tenant-admin surface
	// The BARE store surface: GET /v1/store/current (the org-scoped default
	// store the admin dashboard AND the content storefront edge resolve), the
	// per-listing upsert /v1/store/:id/listing/:slug the publish edge writes,
	// and the public storefront reads karma.style serves at runtime. Without
	// this owner, /v1/store/* fell through to the bare /v1/* AI catch-all —
	// whose prepaid BALANCE gate 402'd every store read (a store-metadata read
	// must never require an LLM balance).
	"/v1/store",
	// Platform-admin catalog CMS: GET/POST/PUT/DELETE /v1/catalog/entries +
	// POST /v1/catalog/seed — the SuperAdmin CRUD admin.hanzo.ai's editor drives.
	// commerce's setupRoutes wires only the PUBLIC read (/v1/commerce/catalog);
	// the CRUD lives on the standalone /v1 bundle (api.Route → catalogApi.AdminRoute),
	// which the co-resident embed skips, so Mount mounts it below on the
	// same /v1 gate chain. Own the prefix here so it reaches commerce (each handler
	// is requireSuperAdmin-gated) instead of the AI /v1/* balance catch-all.
	"/v1/catalog",
	// Platform-admin subscription/DNS plan authority CMS: GET/POST/PUT/DELETE
	// /v1/plans/entries + POST /v1/plans/seed (increment 3a) — the SuperAdmin CRUD
	// the console plan editor drives. The PUBLIC read stays GET /v1/billing/plans;
	// this CRUD rides the /v1 bundle (api.Route → planApi.AdminRoute), which the
	// embed skips, so Mount mounts it below. Own the prefix so it reaches
	// commerce (each handler requireSuperAdmin-gated), not the AI /v1/* 402 gate.
	"/v1/plans",
	// Payment-provider webhook receiver (POST /v1/billing/webhooks/:provider —
	// Square et al). The provider's HMAC over the registered notification URL +
	// body IS the auth; a bearer gate is impossible for provider callbacks.
	"/v1/billing/webhooks",
	// The platform auto-recharge sweep (PlatformOnly, POST .../run-all). The
	// durable cron's poke carries the COMMERCE_SERVICE_TOKEN bearer; without
	// this owner it landed on the account-bridge /v1/billing/* catch-all, whose
	// session gate 403s a service token. That catch-all is gone, so the miss is
	// now a 404 from the /v1 remainder instead of a 403 — still an outage, still
	// this list's job to prevent. (Landed 5x before the unfork — #274 — and the
	// pin test lives beside THIS list so it can't silently regress.)
	"/v1/billing/recharge",
	// The typed payment ops (POST /v1/payments, GET /v1/payments/:id — payments.go).
	// Own the prefix here or the bare /v1/* AI catch-all swallows them: that gate
	// refuses on a prepaid BALANCE, which would make "take a payment" require the
	// balance the payment exists to create.
	paymentsPrefix,
	// The typed cart ops (cart.go) — the first step of a sale, which this binary
	// served the LAST three steps of and not the first. Own the prefix for the same
	// reason payments owns its own: unclaimed, /v1/cart falls to the bare /v1
	// remainder, whose prepaid balance gate would make filling a basket require the
	// balance the basket exists to create.
	cartPrefix,
}

// commerceMasterKey answers ONE question — can this build actually use the key we
// have? — and it is the only place cloud decides commerce's at-rest posture.
//
// ONE KEY for one process. commerce encrypts its per-tenant money stores under a
// 32-byte KEK, and cloud already resolves one (CLOUD_KMS_MASTER_KEY_REF, the same
// master durableCipher and cek derive from), so it hands that over rather than a
// SECOND key being provisioned for the same process through commerce's own env var.
//
// That is not a convenience. Without a key commerce refuses to boot on a
// libsqlcipher-linked build — correctly, it will not open money data unencrypted —
// and the refusal is silent from out here: Mount never reaches transport.SetApp, so
// every S2S billing read falls through to the network and DNS-resolves the
// in-process placeholder. On 2026-07-27 that read as "Insufficient balance" on
// funded accounts, fleet-wide.
//
// But a key this build CANNOT USE is its own failure, in the opposite direction.
// commerce's stores need the LIVE libsqlcipher codec: its dual pool opens a
// concurrent read pool and a serialized write pool on the same file, which the
// pure-Go codec envelope (a single writer) cannot serve. So on a pure-Go build an
// injected key is not a stricter posture, it is a hard refusal — commerce cannot
// boot at all, and the whole money plane is 503 in every local dev build and every
// `go test`. That is the state this function exists to end.
//
// CodecLinked is the SAME predicate commerce's own resolveMasterKey gates on, so
// asking it here keeps ONE posture decision across the process rather than two that
// can disagree:
//
//   - codec linked (the production image: CGO_ENABLED=1 -tags libsqlite3) → inject.
//     commerce encrypts, and its resolveMasterKey still fails closed if the key is
//     absent, so a production build can never quietly write plaintext money data.
//   - codec not linked (a pure-Go dev/CI build) → nil, which hands the decision back
//     to commerce's own env, whose documented answer is a zero-config unencrypted
//     dev store. Such a build has no encrypted path to fall back to; the choice is
//     between an unencrypted dev store and no money plane at all.
func commerceMasterKey(master []byte, lg luxlog.Logger) []byte {
	if sqlitedrv.CodecLinked() {
		return master
	}
	if len(master) > 0 {
		lg.Warn("commerce: pure-Go build cannot open its dual pool encrypted — using commerce's unencrypted dev store; the production image links libsqlcipher and encrypts",
			"codec_linked", false)
	}
	return nil
}

// zipdoc lifts the doc comment off each typed op into zipdoc_gen.go, which is the
// ONLY way that prose reaches the plane's registry and the MCP tool list — Go
// drops comments at compile time. commerce's typed ops are its internal
// /finance/* plane ops (balance_rpc.go, credit_rpc.go, meter_rpc.go) and the
// HTTP ops this repo owns (payments.go, invoices.go, the health probe); the
// surface the embedded module serves stays raw and states its prose through
// openapi.Describe instead (describe.go).
//
// THE MODULE-HANDLER NOTE, stated once because most registrations below cite
// it: binding one of hanzoai/commerce's handlers is NOT a sanctioned reason to
// stay raw — these routes are JSON-shaped and typable. Each is not typed YET
// because the typing is module work: the handler is a func(*zip.Ctx) error
// whose behavior and response shape live in the module behind unexported
// internals, so the only honest typed op is the payments.go pattern — export a
// value-taking core from the module, then declare the op on it here. A typed
// twin declared against the module's private shape instead would be a SECOND
// implementation of the same money move: two sets of bounds to drift, two
// response shapes to disagree, on the one plane where a drifted field name is
// a customer incident. Every registration below that cites this note is a
// conversion the module still owes, not a route that can never be typed.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Mount boots commerce ON the shared zip app (native co-residence).
// commerce's own setupRoutes registers /v1/commerce/* and /_/commerce/*
// directly; the standalone-only surfaces (bare /healthz, legacy /admin SPA,
// checkout SPA root catch-all, Listen) are skipped by the SharedApp contract.
// This adapter registers the remaining wire-contract families with commerce's
// own gate chains (see Prefixes).
func Mount(app cloud.Router, deps cloud.Deps) error {
	// The ledger lives here, so the methods that read and move it are published
	// here: balance, the prepaid gate, the debit and the credit.
	exposeBalance()
	exposeMeter(deps.Metering)
	exposeCredit()
	exposeUsage()
	exposeTxns()
	exposeSpend()
	exposeScopeRules()

	if app == nil {
		return fmt.Errorf("commerce: nil app")
	}
	// The embedded hanzoai/commerce module and the two typed surfaces below
	// register on the concrete App — the named hole, cloud.ZipApp, not a widened
	// Mount signature. commerce's app-wide reach (it wraps ALL of /v1, below) is
	// DECLARED as Plugin.Global at its composition root instead of being implied
	// by a parameter type nobody was checking.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("commerce: router is not a zip app — the embedded module has nothing to register on")
	}
	lg := luxlog.Default().New("subsystem", "commerce")
	// The other direction of the same idea as the ops above: those publish what
	// this process OWNS, and this reaches for the one thing it does not. The credit
	// doors below screen against a model that can only live in one binary, so the
	// scorer is installed as a plane client — cloud.SetRiskScorer's first producer
	// (risk.go).
	installRiskScorer(lg)
	// THE CREDIT SCREEN, resolved ONCE and composed onto the HANDLER of every door
	// that mints.
	//
	// There are two, and they are registered a hundred lines apart: the browser's
	// POST /v1/billing/topup/token below, and the agent's typed POST /v1/payments in
	// exposePayments. Both end in commerce's ONE card money move (billing.TakePayment),
	// so both mint spendable balance from a settled charge — which is why the screen is
	// named here, at the composition root, and handed to each registration rather than
	// being reached for at either. A gate fetched independently at each door is a gate
	// that can be fetched at one of them.
	//
	// IT GOES ON THE HANDLER, NOT ON THE ROUTER, and that is what makes it a control on
	// the MINT rather than on a URL. A typed op is recorded once and projected four ways
	// — REST, MCP tool, by-name call, CLI — and all four dispatch to the op's handler,
	// while router middleware wraps only the fiber handler REST is served through. The
	// screen was mounted on a router; `takePayment` is in tools/list; so an agent's
	// tools/call reached the same authorized deposit unscreened and taught the model
	// nothing when it settled. Both doors now WRAP THEIR HANDLER with it (risk.go
	// screen.route, screen.op), which is the one composition point every projection has
	// to run through.
	//
	// It is one VALUE, not one call per door, so there is no arrangement of these two
	// registrations in which they hold different screens.
	screen := riskGate(lg)
	if deps.Payments == nil {
		lg.Warn("commerce: deps.Payments is nil — payment intent paths will fail; tenant config + admin still served")
	}
	if deps.Vault == nil {
		lg.Warn("commerce: deps.Vault is nil — vault charge paths unavailable; tenant config + admin still served")
	}

	// The typed payment surface. Registered EARLY, beside the health probe and
	// ahead of the embed, for the reason the probe is: these are the fleet's only
	// agent-callable money ops, and a failure to boot the legacy embed must not be
	// what decides whether an agent can take a payment. They share commerce's ONE
	// charge core with the browser's card top-up (payments.go), so registering
	// them here adds a door, never a second money path.
	//
	// AND A DOOR ONTO THE MINT IS SCREENED, which is why the screen is passed in. The
	// shared core is the whole argument: POST /v1/payments reaches the same authorized
	// deposit the top-up does, and it is published as an MCP tool besides — so the
	// screen is composed onto its HANDLER, where every projection of the op runs it,
	// rather than onto the router only REST is served through. exposePayments puts the
	// screen on the WRITE only; the receipt read mints nothing.
	exposePayments(zapp, screen)
	exposeInvoices(zapp)
	// The typed cart surface (cart.go). It reads and writes the embedded module's
	// own cart store, so unlike the payment ops it is only useful when the embed
	// below succeeds — and it is registered HERE anyway, ahead of it, so a cart
	// call against a failed embed answers the 503 payingOrg raises ("commerce is
	// not co-resident in this process") instead of the bare 404 an unregistered
	// route gives. A missing basket and a missing feature are different answers.
	exposeCart(zapp)

	// The liveness probe, registered FIRST so it answers even when the embed
	// fails below. Typed, so the probe is a published op like every other.
	zip.Get(zapp, "/_/commerce/healthz", health)

	// GET /v1/commerce/org — the public org projection the pay SPA reads on every
	// boot — is COMMERCE'S, and is deliberately not re-declared here.
	//
	// It used to be declared here, because commerce served that route only from
	// its standalone router and the embed mounted the merchant table alone, so
	// the one public route existed nowhere. That is no longer true: commerce
	// registers it on the host's app (`public.Get("/org")`, commerce.go), and two
	// declarations of one path is the one thing zip will not compose. It refuses
	// the whole projection and panics — and because this plugin is LAZY, the
	// panic lands on the first request rather than at boot, so the binary looks
	// healthy while `/v1/commerce/*` is dead and every balance read answers 502.
	// A funded account then reads $0.00, because the callers' documented
	// fall-through lands on a ledger production does not fund.
	//
	// Commerce's is also the better of the two. Both concerns this copy existed
	// to serve are handled there: it is on a group carrying only
	// `forwardedHostMiddleware` — public, no `IAMTokenRequired`, so no deadlock —
	// and its loader is cached 60s with a 2s deadline that fails to a MISS, which
	// is the fix for the pool exhaustion at commerce 1.42.44 that motivated the
	// `NewOrgResolver(nil)` here. That nil loader ALWAYS misses, so this copy
	// could only ever answer with the synthetic default, never a real org; and it
	// read the ingress-rewritten host, which `forwardedHostMiddleware` corrects.
	//
	// If it ever needs to come back, it comes back as one declaration — inject
	// the resolver into the embed, do not add a second route.

	// commerce persists its per-org SQLite + `base` tree under <DataDir>/commerce,
	// NEVER at DataDir directly: cloud already owns DataDir/orgs and DataDir/base,
	// and commerce also writes orgs/ + base/ — sharing the root would collide two
	// apps on the same SQLite files and corrupt them.
	dataDir := "/var/lib/cloud/commerce"
	if deps.DataDir != "" {
		dataDir = filepath.Join(deps.DataDir, "commerce")
	}

	embedded, err := commercemod.Embed(context.Background(), commercemod.EmbedConfig{
		DataDir:   dataDir,
		MasterKey: commerceMasterKey(deps.MasterKey, lg),
		// RequireIdentity stays gateway-owned: the gateway in front of the cloud
		// binary is the trust boundary per HIP-0026.
		RequireIdentity: false,
		// THE native co-residence contract: commerce registers its routes on
		// cloud's own app — no second engine, no net/http adaptation.
		App: zapp,
		// ONE LEDGER: commerce's POST /v1/billing/credit mints into cloud's native
		// finance ledger (the SAME per-org account the AI spend-gate reads), so a
		// granted credit is immediately spendable. commercemod.Embed calls
		// creditledger.Set(this) before routes register; nil would leave commerce on
		// its own datastore (standalone), but in this unified binary finance is
		// co-resident, so we inject the finance-backed ledger adapter.
		Ledger: ledger{},
		// THE HOST'S SECRET PLANE, in-process. deps.KMS is the embedded KMS this
		// binary already runs (the logs say so at boot: "deps.KMS -> the kms app
		// over the internal plane"), so commerce reads a deployment secret by
		// asking its host rather than through an env fan-out — KMS to a k8s
		// Secret to a pod variable, three places to go stale and a restart to
		// pick up a rotation. nil when this build has no KMS, which commerce
		// treats as "fall back", never as an error.
		Secrets: deps.KMS,
	})
	if err != nil {
		lg.Error("commerce embed failed — serving fail-closed 503 (cloud stays up)", "err", err)
		// State, not just a log line: /v1/health reports this and the release smoke
		// refuses an image whose planes are dead. Without it a fail-closed commerce
		// is indistinguishable from a commerce that was never enabled.
		cloud.Degraded("commerce", err)
		mountCommerceFailClosed(app)
		return nil
	}

	// The BARE /v1/store surface (see Prefixes). Group-scoped chain
	// mirrors the standalone /v1 bundle: gated request context, host, IAM
	// resolution; store.Route's own tokenRequired arg gates the CRUD.
	// The bundle binds the module's own route table, so no route in it is
	// typed yet — module work, per the module-handler note.
	storeV1 := app.Group("/v1")
	storeV1.Use(commercemid.AddHost(), commercemid.RequestContext(), commerceErrorScope())
	// Unconditional, exactly like the standalone bundle: IAMTokenRequired
	// no-ops gracefully when IAM is not initialized.
	storeV1.Use(iammiddleware.IAMTokenRequired())
	commercestore.Route(storeV1, commercemid.TokenRequired())

	// Platform-admin catalog CMS on the SAME /v1 bundle: GET/POST/PUT/DELETE
	// /v1/catalog/entries + POST /v1/catalog/seed. setupRoutes wires only the
	// public read (/v1/commerce/catalog); the CRUD rides the standalone /v1 bundle
	// (api.Route → catalogApi.AdminRoute), which the co-resident embed skips — so
	// register it here, exactly as the standalone does. storeV1's IAMTokenRequired
	// populates the claims each handler's requireSuperAdmin reads (anon → 403, a
	// platform admin edits); it is cross-tenant data, never org-scoped.
	//
	// TokenRequired is passed for the same reason the standalone passes it
	// (api.Route hands AdminRoute its adminRequired): IAMTokenRequired resolves a
	// USER, and only TokenRequired's service-token branch stamps the marker
	// IsServiceToken reads. Without it no scheduled run can ever authenticate
	// here — the model sync 403'd on every attempt and the model catalog sat
	// empty in production. Same reason the auto-recharge poke below is wired with
	// its own TokenRequired rather than relying on this group's chain.
	// The bundle binds the module's own route table, so no route in it is
	// typed yet — module work, per the module-handler note.
	catalogapi.AdminRoute(storeV1, commercemid.TokenRequired())

	// Platform-admin subscription/DNS plan authority CRUD on the SAME /v1 bundle:
	// GET/POST/PUT/DELETE /v1/plans/entries + POST /v1/plans/seed (increment 3a).
	// Mirrors the catalog mount: the standalone wires it on the /v1 bundle
	// (api.Route → planApi.AdminRoute), which the co-resident embed skips. The
	// embed seed SOURCE is injected here (the composition root) — commercebilling.
	// SeedRows, the SAME @hanzo/plans embed the boot seed + resolveSubscriptionPlan
	// read — so api/plan never imports api/billing. Each handler is
	// requireSuperAdmin-gated (anon → 403); prices are admin-editable but the mint
	// gates score the IMMUTABLE embed, so an edit never moves a charge gate.
	// The bundle binds the module's own route table, so no route in it is
	// typed yet — module work, per the module-handler note.
	planapi.AdminRoute(storeV1, commercebilling.SeedRows)

	// Reconcile the plan authority to the catalog this binary ships, the way the
	// standalone does at its own boot. GET /v1/billing/plans reads the authority
	// and falls back to the embed only when the authority is empty, so without
	// this a repriced catalog ships in the binary and never reaches a customer —
	// the rows keep serving whatever a past seed wrote, and the only way to move
	// them is a hand-driven POST /v1/plans/seed.
	//
	// Safe on every boot by construction: seeded values ARE the embed, so it
	// moves no charge; rows an admin edited stay authoritative; a slug the
	// catalog stopped publishing is archived, not deleted, so renewals and
	// invoices still price from it. Failure is logged and not fatal — a plan
	// authority that could not be reconciled is a stale price list, which is
	// worse to serve than to refuse booting the whole API for.
	if created, corrected, err := commercebilling.SeedPlans(context.Background()); err != nil {
		lg.Error("plan authority not reconciled to the shipped catalog", "err", err)
	} else if created > 0 || corrected > 0 {
		lg.Info("plan authority reconciled", "created", created, "corrected", corrected)
	}

	// THE MERCHANT RESOURCES, at the endpoint every Hanzo surface is told to
	// use: api.hanzo.ai/v1/commerce/<kind> — products, variants, collections,
	// discounts, sales channels, stock locations, subscribers, webhooks.
	//
	// Commerce is a PLUGIN of this binary, so its merchant surface is reachable
	// through this binary or it is not reachable at all. It was not: there is no
	// commerce backend pod, commerce-api.hanzo.ai routes here, and this embed
	// carried no resource bundle — so every admin data view 404'd in production
	// while sign-in, catalog and billing answered 200. Honest, and total.
	//
	// It calls the resources LEAF rather than commerce's api.Route. Route also
	// binds an index route, a permissive CORS policy and a wildcard OPTIONS onto
	// whatever router it is handed — right for a process that owns its tree,
	// wrong in this shared one. Importing that package would also drag checkout,
	// subscriptions and thirdparty/netlify, which this binary deliberately does
	// not carry.
	//
	// A DUPLICATE DECLARATION IS A BOOT PANIC, not a silent merge. This comment
	// used to say byte-identical patterns "merge silently, first wins"; measured
	// against the zip this tree pins, they do not — the composer refuses the
	// program outright and names every conflicting pair with file:line:
	//
	//	zip: this program does not compose, so it has no projection:
	//	zip: GET /v1/collection: declared by "/collection" at rest/rest.go:133
	//	  (via root → /v1 → /collection) and by "/collection" at rest/rest.go:133
	//
	// WHERE it lands differs by zip version, and both are before a request is
	// served: this tree resolves v1.25.1, which accepts the second Route() call
	// and refuses when the program is COMPOSED; commerce standalone pins v1.24.2,
	// which refuses inside Route(). So a check that only registers reads "fine"
	// here and is measuring nothing — ask for the composition.
	//
	// That matters for anything ADDED here later: a kind may be registered in
	// exactly ONE place. Putting a kind on this leaf while commerce's api.Route
	// still registers it takes the STANDALONE down at boot, and the failure is
	// in the other binary from the one that changed.
	//
	// productEvents is nil: the storefront publish loop belongs to the
	// standalone's event bus, and the CRUD does not depend on it.
	commerceV1 := app.Group("/v1/commerce")
	commerceV1.Use(commercemid.AddHost(), commercemid.RequestContext(), commerceErrorScope())
	commerceV1.Use(iammiddleware.IAMTokenRequired())
	commerceresources.Route(
		commerceV1,
		commercemid.TokenRequired(),
		commercemid.TokenRequired(permission.Admin),
		paywall.Require,
		nil,
	)

	// Provider webhook intake at the LIVE registered path. Chain mirrors the
	// commerce-standalone posture: gated request context, then the sessionless
	// HMAC-verified handler. It stays raw because it speaks the provider's
	// protocol: the signature verifies the raw payload bytes, so there is no
	// typed input to declare.
	app.Post("/v1/billing/webhooks/:provider", commercemid.RequestContext(), commercebilling.HandleProviderWebhook)

	// Durable-cron auto-recharge poke (COMMERCE_SERVICE_TOKEN bearer) at its
	// live path — the retired /v1/billing/* forwarder's session gate 403'd it. Same gate
	// chain the commerce route table uses: TokenRequired authenticates the
	// service token, PlatformOnly authorizes the mint. It is not typed yet —
	// module work, per the module-handler note.
	app.Post("/v1/billing/recharge/run-all",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		commercemid.PlatformOnly(),
		commercebilling.RunAutoRechargeAllOrgs,
	)

	// POST /v1/billing/mode — the org's live/sandbox switch, and the ONLY way to
	// move a tenant onto real card rails. organization.TestMode() is `!o.Live` and it is
	// the SINGLE authority for both the Square environment and the ledger bucket, so an
	// org that has never been flipped transacts in SANDBOX — fail-closed by design, and
	// the reason a production-credentialled deployment can still hand a buyer a sandbox
	// card form. Unrouted, that flip could not be performed at all in this binary: the
	// switch lives on commerce's mint group, which the co-resident embed never compiles.
	//
	// Chain is auto-recharge/run-all's, because this is the same class of route — a
	// money-MINT control, not a customer action. commerce gates it on `mint`
	// (middleware.Mint: internal service token OR platform global admin, NEVER the
	// org-level Admin bit), and TokenRequired + PlatformOnly is that gate here. An org
	// admin must not be able to move their own org between sandbox and production.
	// It is not typed yet — module work, per the module-handler note.
	app.Post("/v1/billing/mode",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		commercemid.PlatformOnly(),
		commercebilling.SetOrgTestMode,
	)

	// GET /v1/billing/plans — the public tier catalog the console renders. commerce's
	// legacy api.Route() billing bundle (ListPlans, invoices, subscriptions, …) is NOT
	// registered by the co-resident embed: setupRoutes wires only /v1/commerce/*, so
	// /v1/billing/plans has NO handler in this binary. The account bridge's
	// /v1/billing/* wildcard (order 122) then forwarded the read BACK to commerce at
	// COMMERCE_URL — which defaults to the public api.hanzo.ai edge — re-entering the
	// same bridge in an unbounded self-dispatch loop that surfaced as a 502
	// ("commerce unreachable: Get https://api.hanzo.ai/v1/billing/plans"). Registering
	// the static ListPlans handler HERE serves plans in-process — the same co-resident
	// move billing.go makes for usage/balance. RequestContext supplies the namespaced
	// context promo.Active reads.
	//
	// THE BRIDGE IS GONE (its manifest row with it), and this registration is why: the
	// wildcard's whole allowlist ended up served co-resident, here and on billing, so it
	// held two bare stems and answered nothing. Each address below is claimed on THIS
	// app's manifest row, deeper than the bare /v1/billing stem, which is what makes the
	// host route it here. The past-tense loops recorded below are the reason each of
	// these registrations exists — history, not a live hazard — and dropping one now
	// misses on the /v1 remainder instead of self-dispatching. It is not typed
	// yet — module work, per the module-handler note.
	app.Get("/v1/billing/plans", commercemid.RequestContext(), commercebilling.ListPlans)

	// The rest of the console's billing READS, served co-resident for the SAME reason
	// plans is: commerce's api.Route() billing bundle is never compiled here, so without
	// these registrations every one of them fell through to the account bridge's
	// /v1/billing/* wildcard, which forwarded to COMMERCE_URL — the public api.hanzo.ai
	// edge, which is THIS binary — and self-dispatched into a 502 loop. In prod there is
	// no separate commerce backend to point COMMERCE_URL at (the in-cluster `commerce`
	// Service selects the cloud pods), so co-residence is the only way to serve them.
	//
	// Chain: RequestContext (gated context) → IAMTokenRequired (resolves the org from the
	// gateway-validated X-Org-Id into Locals("organization"), the namespace commerce's
	// GetOrganization reads) → PinBillingSubject (pins the caller's OWN billing subject
	// into the query — the isolation the bridge applied, carried onto the co-resident
	// route so a read can never widen past the caller; fail-closed for an unvalidated
	// caller) → the commerce handler. payment-config is
	// org-scoped, not subject-scoped, but PinBillingSubject is still the auth gate that
	// keeps an unvalidated caller from reaching GetOrganization; its pinned (unused)
	// subject params are ignored by that handler.
	//
	// No row is typed yet — module work, per the module-handler note.
	billingRead := []struct {
		path string
		h    zip.Handler
	}{
		{"/v1/billing/invoices", commercebilling.ListInvoices},
		// The PDF download also serves bytes — an attachment, not a JSON shape —
		// which keeps it off the typed plane on its own.
		{"/v1/billing/invoices/:id/pdf", commercebilling.DownloadInvoicePDF},
		{"/v1/billing/subscriptions", commercebilling.ListBillingSubscriptions},
		{"/v1/billing/alerts", commercebilling.ListSpendAlerts},
		{"/v1/billing/payouts", commercebilling.ListPayouts},
		{"/v1/billing/settings", commercebilling.GetPaymentConfig},
		// The Credits tab's read. It sat in the same hole as its siblings: the
		// handler exists in the vendored module and no app in the fleet registered
		// it, so the address reached nobody and billing.hanzo.ai's client swallowed
		// the 404 into an empty list — a customer with grants saw none. Read-only
		// and subject-scoped like the rest; MINTING credit stays where it is, on
		// the mint-gated POST /v1/billing/credit.
		{"/v1/billing/credits", commercebilling.ListCreditGrants},
		// The per-key rate-limit tier the ai router reads on every request
		// (hanzoai/ai routers/ratelimit.go commerceTierLookup). Naming the prefix in
		// the manifest says WHO owns a path; it does not serve one, and commerce's
		// api.Route() bundle that would is never compiled here — so this 404'd, every
		// lookup failed, and the fallback is zen-free: 60 rpm against 500 for pro.
		// It gave nothing away; it silently served every PAYING customer the most
		// restrictive tier in the table.
		//
		// The wire top-up rail: the serving BRAND's receiving bank details
		// (host→brand, org row hydrated from KMS /tenants/<brand>/wire) with the
		// caller's billing key rendered into the payment reference — which is why
		// it belongs on THIS chain: the reference is payer attribution, and an
		// unpinned caller would be an unattributable wire. Nothing mints here;
		// settlement is the admin wire/credit verb on bank receipt.
		{"/v1/billing/wire", commercebilling.GetWireInstructions},
		// The custody rail's live capability list (chains + tokens straight from
		// the MPC processor) — the pay SPA renders its asset picker from this.
		{"/v1/billing/crypto/options", commercebilling.GetCryptoOptions},
		// A caller-scoped crypto deposit intent read (pending → confirming →
		// succeeded); a foreign intent id answers 404.
		{"/v1/billing/crypto/deposit/:id", commercebilling.GetCryptoDeposit},
	}
	for _, r := range billingRead {
		app.Get(r.path,
			commercemid.RequestContext(),
			iammiddleware.IAMTokenRequired(),
			accountclient.PinBillingSubject(),
			r.h,
		)
	}

	// GET /v1/billing/tier — the per-key rate-limit tier the ai router reads on
	// every request (hanzoai/ai routers/ratelimit.go commerceTierLookup).
	//
	// It gets its OWN chain because its caller is a SERVICE, not a person, and the
	// two resolve an org by different doors. IAMTokenRequired resolves one only
	// from a gateway-validated user identity — ownerID AND X-User-Id AND email —
	// and deliberately falls through on a bare X-Org-Id, because admitting on that
	// alone once let an off-gateway caller name any victim org. The router sends
	// exactly that: a service bearer plus X-Org-Id and no user. So it arrived with
	// a nil org, GetTier's type assertion panicked, and every lookup answered 500.
	//
	// TokenRequired is the door for a caller that IS a credential: it verifies the
	// service token FIRST and only then calls ensureIAMOrg, which resolves the
	// header. Credential before trust, so nothing is admitted on a header alone —
	// the same reason the catalog CRUD and the recharge poke above each carry
	// their own TokenRequired rather than riding an IAM chain.
	//
	// Fixing the panic alone was not enough and it is worth saying why: the router
	// maps ANY non-2xx to TierZenFree, so a refusal downgrades a paying customer
	// exactly like the crash did — 60 rpm against 500 for pro — silently, with no
	// error anywhere the customer or we would see. The tier has to RESOLVE, not
	// merely stop crashing. It is not typed yet — module work, per the
	// module-handler note.
	// PinBillingSubject IS REQUIRED HERE, and its absence was a live leak.
	//
	// TokenRequired authenticates and does not pin, which is the whole hazard the
	// comment on /v1/billing/methods already spells out: any authenticated browser
	// could name another subject. GetTier reads ?user= verbatim and answers with
	// that subject's wallet — prepaidAvailable, creditsRemaining, effectiveAvailable
	// — so one signed-in customer could read every other customer's balance.
	//
	// It is CROSS-CUSTOMER, not merely cross-subject, because every self-serve
	// signup lands in the SAME org (account.SignupOrg = "hanzo") with a per-person
	// subject. The org namespace is closed; the subject was not. Measured live
	// before this line: one caller, four wallets — hanzo/z 10966, hanzo 6375,
	// hanzo/admin 10000, hanzo/dev 0.
	//
	// The pin does not break the S2S reader this route exists for: PinBillingSubject
	// admits a verified service-token caller that names its own org and leaves its
	// query untouched (apps/account/billing_coresident.go), which is exactly the
	// shape ai's rate limiter and apps/metering send. TokenRequired stays after it
	// so that caller still resolves an org.
	app.Get("/v1/billing/tier",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.GetTier,
	)

	// POST /v1/billing/crypto/deposit — mint a per-payer MPC custody deposit
	// address (commerce thirdparty/mpc → the hanzo-mpc signer fleet's /keygen).
	// Same chain as the reads: the credited payer is the PINNED caller subject,
	// never a body value, and the handler reuses the payer's open intent so a
	// refresh cannot spray keygens. No balance moves here — the chain watcher
	// credits on real confirmations. It is not typed yet — module work, per the
	// module-handler note.
	app.Post("/v1/billing/crypto/deposit",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.CreateCryptoDeposit,
	)

	// The PORTAL payment-method pair — the address the BILLING app proxies to.
	//
	// It is the SERVICE-TOKEN face of the same rows the customer family above answers,
	// and it is a separate address because it admits a separate principal — not because
	// anything forwards to it. It was born when billing still held the customer address
	// and could not forward there (the forward was its own route and re-entered it — the
	// depth-8 502 top-up hit); billing asked for /v1/billing/portal/methods all along
	// while NOTHING in the fleet served it, so the proxy forwarded a 404 verbatim, the
	// saved-card list was empty no matter how many cards were vaulted, and the DELETE had
	// no owner at all. Both families live here now (manifest.Apps gives commerce both
	// prefixes) and neither hops; what remains is the gate, which differs, which is why
	// they remain two addresses rather than one.
	//
	// THE GATE IS THE POINT, and it is not the console chain above. Both handlers key
	// their finer scope on a value the CALLER supplies — PortalPaymentMethods filters
	// on ?customerId, DetachPaymentMethod resolves :id — so the chain has to pin the
	// tenant on BOTH kinds of principal that reach here:
	//
	//   - TokenRequired (NOT IAMTokenRequired) authenticates the caller and resolves
	//     the ORG from the gateway-pinned X-Org-Id into Locals("organization") — the
	//     namespace GetOrganization reads — for an IAM member AND for the raw
	//     COMMERCE_SERVICE_TOKEN the billing proxy presents. IAMTokenRequired admits
	//     only the former and would leave the org unset for the latter (a 500, the
	//     way GetTier found out). X-Org-Id is stripped from every client request by
	//     the gateway, so the org is never a caller-supplied value.
	//   - PinBillingSubject is the IDOR control. TokenRequired ALONE authenticates
	//     without pinning, and that is the whole hazard: any authenticated browser
	//     could then read another subject's cards by naming their customerId. The pin
	//     OVERWRITES every billing-subject key (user/userId/customerId) with the
	//     VALIDATED caller's own account.Payer subject and drops ?org, so a forged
	//     query cannot widen scope; it passes the query through ONLY for a caller
	//     whose bearer constant-time-matches COMMERCE_SERVICE_TOKEN (the billing
	//     proxy, which pins the subject server-side to readerOrg before it calls);
	//     and it fail-closes anyone who is neither, BEFORE the handler runs.
	//
	// Cross-ORG is the namespace and is closed for both principals — including the
	// privileged one, which bypasses commerce's intra-org owner guard but never the
	// namespace (commerce api/billing/payment_methods_tenant_test.go proves it).
	// Neither is typed yet — module work, per the module-handler note.
	app.Get("/v1/billing/portal/methods",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.PortalPaymentMethods,
	)
	app.Delete("/v1/billing/portal/methods/:id",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.DetachPaymentMethod,
	)
	// SAVING a card completes the family, and it had no owner at all: a customer
	// could list saved cards and delete one, but never add one. The billing app's
	// POST /v1/billing/methods forwards to commerce's /v1/billing/methods — an
	// address commerce does NOT serve co-resident and which the in-cluster
	// `commerce` Service resolves back to THESE pods, so the forward re-enters the
	// forwarder. It never got the chance to: CLOUD_COMMERCE_HTTP_URL is unset, so
	// the proxy is unconfigured and every saved-card call — GET and POST alike —
	// answers 501 "billing is not configured". The whole feature is dark in
	// production, which is why nothing can bill a monthly plan to a card on file.
	//
	// Co-resident on the portal face is the fix the GET and DELETE already model:
	// no HTTP hop, so nothing can self-dispatch, and the same gate applies.
	// commerce's CreatePaymentMethod vaults the Square nonce (providerRef) as a
	// reusable card-on-file and stores the billing address with it — that vaulted
	// card is what a subscription renewal charges later.
	//
	// PinBillingSubject is load-bearing on a WRITE, not decoration: this handler
	// reads its subject from the BODY (customerId), and the pin overwrites the
	// subject keys there while preserving card/type/sourceId — so a caller can
	// only ever attach a card to its OWN account, whatever the body claims.
	// THE CUSTOMER ADDRESS for saved cards. cloud's billing app used to forward
	// these to commerce over HTTP, and that proxy is unconfigured here — so a
	// signed-in customer got 401 listing their own cards and the checkout's
	// prefill failed on every load. Served in-process instead: no hop to
	// misconfigure, and the same pinned-subject gate as its portal twin, which
	// is what keeps a caller inside its own account whatever it sends. None of
	// the three is typed yet — module work, per the module-handler note.
	app.Get("/v1/billing/methods",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.ListPaymentMethods,
	)
	app.Post("/v1/billing/methods",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.CreatePaymentMethod,
	)
	app.Delete("/v1/billing/methods/:id",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.DetachPaymentMethod,
	)

	// The portal save — the service-token twin of the customer POST above. It
	// is not typed yet — module work, per the module-handler note.
	app.Post("/v1/billing/portal/methods",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.CreatePaymentMethod,
	)

	// GET /v1/billing/alerts/authorize — the per-request per-scope spend-CAP
	// VERDICT the request-edge metering gate consumes (clients/metering scopeAuthorize,
	// pathLimitsAuthorize). It is a SERVICE-token S2S read (COMMERCE_SERVICE_TOKEN +
	// X-Org-Id), NOT a browser/IAM read — so it needs its OWN registration, distinct from
	// the console billingRead block above: without a co-resident handler this authorize
	// fell through to the account bridge's /v1/billing/* wildcard (order 122), which — being
	// service-token-forwardable (its allowlist) — re-forwarded it to
	// COMMERCE_URL (the public api.hanzo.ai edge = THIS binary) over the commerce transport's
	// self-routing dispatch, re-entering the same wildcard until the depth-8 guard refused
	// → 502 → the gate fails OPEN (the cap is a policy overlay, so no traffic was blocked,
	// but ~135 502s/30m spammed the money path and each burned 8 full-app dispatches). The
	// plain GET /v1/billing/alerts (registered above) already broke this loop for the
	// CRUD read; this closes the /authorize sibling the metering gate hits on every call.
	//
	// Chain mirrors commerce's OWN gate on this route (api/billing/handlers.go: the `billing`
	// group's userRequired = TokenRequired) plus the RequestContext the standalone supplies
	// globally — NOT the IAM/PinBillingSubject console chain: a raw service token is not an
	// IAM JWT, so IAMTokenRequired would leave GetOrganization unset and AuthorizeSpendCap
	// would 500. TokenRequired authenticates the service token AND resolves the tenant from
	// the gateway-pinned X-Org-Id into Locals("organization"), which AuthorizeSpendCap reads.
	// No PlatformOnly:
	// authorize is a per-org cap read, not a cross-org mint (unlike auto-recharge/run-all).
	// It is not typed yet — module work, per the module-handler note.
	app.Get("/v1/billing/alerts/authorize",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		commercebilling.AuthorizeSpendCap,
	)

	// Self-service spend-cap CRUD WRITES — the customer half of the cap: a customer
	// (or the admin S2S) CREATES / EDITS / REMOVES their own usage caps. These are the
	// write siblings of the co-resident GET /v1/billing/alerts list; without their
	// own registration they too fell through the account bridge's /v1/billing/* wildcard
	// (its allowlist included POST alerts) into the SAME 502 self-dispatch loop
	// authorize hit — so a customer could not set a cap AT ALL in the unified binary
	// (POST/PATCH/DELETE all 502'd). Same chain commerce's own route table gates them with
	// (api/billing/handlers.go:322-325, the `user` group's userRequired = TokenRequired) +
	// the global RequestContext — an IAM JWT OR the COMMERCE_SERVICE_TOKEN, org resolved
	// from the gateway-pinned X-Org-Id into Locals("organization"). Org-scoped by that
	// namespace (a caller only ever writes their OWN org's caps; a foreign :id is a
	// not-found miss in the caller's namespace), so no PinBillingSubject — alerts are
	// org-level, not billing-subject-level.
	// A spend cap is a FINANCIAL SAFETY control, so its writes are gated to an ORG ADMIN
	// (or SuperAdmin, or the trusted S2S service token for the SuperAdmin cap-oversight
	// Forward) — never any authenticated member. commerce's own `user` group admits any
	// member, which would let a compromised member key DELETE the org's cap (→ unbounded
	// spend) or POST a 1¢ enforce cap (→ org-wide 402 DoS). requireSpendCapAdmin closes that.
	// None of the three is typed yet — module work, per the module-handler note.
	app.Post("/v1/billing/alerts",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		requireSpendCapAdmin(),
		commercebilling.CreateSpendAlert,
	)
	app.Patch("/v1/billing/alerts/:id",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		requireSpendCapAdmin(),
		commercebilling.UpdateSpendAlert,
	)
	app.Delete("/v1/billing/alerts/:id",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		requireSpendCapAdmin(),
		commercebilling.DeleteSpendAlert,
	)

	// THE CUSTOMER'S OWN LEDGER — the four reads billing.hanzo.ai's Transactions,
	// Credits, Team and Settings tabs call, and which NOTHING in this binary served.
	//
	// commerce declares them on its api.Route() `user` group (api/billing/handlers.go),
	// but that route table is never compiled here: the co-resident embed registers on
	// the HOST's router, so a commerce route reaches production only if this file names
	// it. The library had the handlers all along — so the symptom read as a stale image
	// and was not one. Every unnamed route falls through to the account bridge's
	// /v1/billing/* wildcard, whose allowlist does not include these four, so all four
	// answered a bare 404 and four tabs of the billing app rendered empty forever.
	//
	// THE CHAIN IS GetTier's, and each link earns its place on a MONEY READ:
	//   - IAMTokenRequired resolves the org from the gateway-validated X-User-Id +
	//     X-Org-Id into Locals("organization") — the namespace every handler reads.
	//     It FALLS THROUGH when there is no validated principal rather than refusing.
	//   - PinBillingSubject is therefore both the gate and the IDOR control. It
	//     fail-closes that fall-through with 401 "sign in to view billing" (401, not
	//     403 — a browser re-authenticates on 401 and merely reports 403), and it
	//     OVERWRITES every billing subject key {user,userId,customerId} with the
	//     caller's own account.Payer subject. That is load-bearing here and not
	//     decoration: ListTransactions filters on ?user and GetCreditBalance on
	//     ?userId, both unpinned client values — an unpinned read returns every
	//     subject's rows in the org namespace. Because the pin SETS the key rather
	//     than merely validating it, the handlers' "required parameter" 400 can never
	//     fire for a real caller, and the subject is exactly the one account.Payer
	//     debits, so a read can never disagree with the wallet it describes.
	//   - TokenRequired (no masks) runs AFTER the pin so the trusted S2S reader still
	//     resolves an org — IAMTokenRequired admits only IAM principals and would
	//     leave GetOrganization unset for a service token, which panics on a nil
	//     Locals assertion. With no masks it passes any authenticated principal, so
	//     the browser path is unchanged.
	//
	// accounts/:id/members carries no subject key and guards itself (:id must equal
	// the resolved org, else 403), but it takes the same chain: one chain for the
	// family, and the pin is what turns an anonymous call into 401 instead of a panic.
	// None is typed yet — module work, per the module-handler note.
	app.Get("/v1/billing/transactions",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.ListTransactions,
	)
	app.Get("/v1/billing/credit-balance",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.GetCreditBalance,
	)
	// The same balance grouped by tag, which is how a reader tells trial credit
	// from bought credit. Chat asks for it per session; an unregistered route
	// answers 404 and the caller reads that as "no credit".
	app.Get("/v1/billing/credit-balance/breakdown",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.GetCreditBalanceBreakdown,
	)
	app.Get("/v1/billing/accounts",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.ListBillingAccounts,
	)
	app.Get("/v1/billing/accounts/:id/members",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.ListAccountMembers,
	)

	// POST /v1/billing/topup/token — the INLINE Square card top-up (the console's
	// "Billing → Credits → add credits": the Square Web Payments SDK tokenizes the card
	// IN THE BROWSER → a single-use nonce → this endpoint charges it and credits the
	// caller's balance). commerce's api.Route() billing bundle is NOT compiled into the
	// co-resident embed, so — exactly like plans/invoices/alerts above — without
	// this registration the POST fell through to the account bridge's /v1/billing/*
	// wildcard (order 122). That wildcard was service-token-forwardable for topup/token,
	// so it re-forwarded to COMMERCE_URL (default the public api.hanzo.ai edge = THIS
	// binary) over the commerce transport's self-routing dispatch, re-entering the SAME
	// wildcard until the depth-8 guard refused → the "commerce transport: in-process
	// dispatch depth 8 exceeded" 502 that broke top-up outright. Registering commerce's
	// real TopupWithToken co-resident here serves the charge in-process at depth 1 — no
	// HTTP hop, no self-dispatch — and it is the ONLY route to it now: the wildcard and
	// its allowlist are gone, and the split-deploy case is the commerce transport's
	// plain-HTTP fallback, not a second copy of this endpoint.
	//
	// Chain — the browser money-WRITE posture, byte-for-byte what the bridge applied:
	//   RequireCSRF        — the ambient-cookie anti-CSRF gate the bridge's requireCSRF
	//                        wrapped POST /v1/billing/* with (a Bearer/gateway caller is
	//                        not CSRF-able; an ambient-cookie write needs the token).
	//   RequestContext     — the gated request context commerce's handler + ledger read.
	//   IAMTokenRequired   — resolves the org from the gateway-validated X-Org-Id into
	//                        Locals("organization"), which TopupWithToken.GetOrganization
	//                        + topupDestination read as the org billing key.
	//   PinBillingSubject  — pins ?user= to the caller's OWN account.Payer subject (the
	//                        SAME rule the ai spend-gate debits), so the credit lands on
	//                        the caller's subject (person=org/name) and can never be
	//                        widened; fail-closed for an unvalidated caller — the IDOR
	//                        boundary stays exactly where the bridge put it.
	// The card PAN never touches this binary: TopupWithToken charges the Square nonce only,
	// and the settled charge itself is the mint authority (mintauth.WithAuthorized).
	//   screen             — that mint authority is exactly why this route is screened.
	//                        A settled charge on a stolen card IS spendable balance, so
	//                        HANZO RISK judges the payer at the last moment before the
	//                        charge, as a PRIVILEGED grant: a scorer that is present and
	//                        cannot answer refuses rather than proceeds. It sits after
	//                        PinBillingSubject so the subject it judges is the subject the
	//                        charge credits, and before the handler so a refusal costs no
	//                        card authorization. See risk.go.
	// It is not typed yet — module work, per the module-handler note; the typed
	// door onto this same charge core is POST /v1/payments (payments.go), and it holds
	// the SAME `screen` value — that is what makes the control one control.
	//
	// THE SCREEN WRAPS THE HANDLER, at the same point in the same chain it always ran
	// at: `screen.route(TopupWithToken)` is the last entry, so it still runs after
	// PinBillingSubject and before any card is authorized. A raw route could carry the
	// screen as its own chain entry instead — it has one projection, so the two are
	// equivalent here — and it wraps the handler because the typed door MUST, and one
	// composition point is what makes "every door onto the mint is screened" a property
	// the structural test can read rather than a habit.
	app.Post("/v1/billing/topup/token",
		accountclient.RequireCSRF(),
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		screen.route(commercebilling.TopupWithToken),
	)

	// POST /v1/billing/topup — the SAVED-card top-up: charge a card the subject
	// already has on file (paymentMethodId) and credit the same canonical balance.
	// It is topup/token's card-on-file twin and the ONE saved-card top-up door
	// (the auto-recharge cron shares its chargeAndCredit core). Until now it had
	// NO co-resident route, so the four vaulted cards a customer could SEE were
	// cards nothing could CHARGE — saving a card worked, spending it did not.
	//
	// Chain is topup/token's byte-for-byte, the risk screen included: both
	// credit the SPENDABLE wallet, so both are screened before the charge.
	// PinBillingSubject pins the body's userId to the caller's own subject; the
	// handler itself refuses a paymentMethodId owned by any OTHER subject (404,
	// no oracle) — paymentMethodId is not a subject key, so the pin cannot
	// cover it.
	app.Post("/v1/billing/topup",
		accountclient.RequireCSRF(),
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		screen.route(commercebilling.Topup),
	)

	// POST /v1/billing/subscribe/card — the card-on-file MONTHLY subscription: vault a
	// Square nonce as a reusable card, charge the first period at the SERVER-AUTHORITATIVE
	// plan price, create the subscription. It is the paid path's front door, and it had NO
	// route in this binary at all: commerce publishes it on its api.Route() `user` group
	// (api/billing/handlers.go), which the co-resident embed never compiles, and mount
	// never registered it — so the one endpoint that turns a visitor into a subscriber
	// answered the account bridge's "sign in to view billing" no matter who called it.
	//
	// Chain is topup/token's byte-for-byte, and for the same reasons: both are browser
	// money-WRITES that charge a single-use Square nonce. commerce's own gate is the
	// `user` group (TokenRequired — any authenticated member may subscribe by paying);
	// IAMTokenRequired is that gate here, resolving the org GetOrganization reads.
	// PinBillingSubject pins the subject into query AND body, so subscribeSubject can
	// only ever resolve the caller's OWN org (its `userId` is honored only inside that
	// bound) — the IDOR boundary the bridge set. The PAN never touches this binary:
	// the nonce goes to Square and the settled charge is its own mint authority.
	// It is not typed yet — module work, per the module-handler note.
	app.Post("/v1/billing/subscribe/card",
		accountclient.RequireCSRF(),
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.SubscribeWithCard,
	)

	// The remaining console billing WRITES that share topup/token's self-dispatch loop
	// class — each is a POST the console makes, each had NO co-resident handler, so each
	// fell through to the account bridge's /v1/billing/* wildcard (order 122) and re-entered
	// it over the commerce transport until the depth-8 guard refused (the same "in-process
	// dispatch depth 8 exceeded" 502 that broke top-up). Each commerce handler exists in the
	// vendored module (v1.49.13); registering them co-resident serves the write in-process at
	// depth 1, and is now the only route to it. Chain matches the bridge's write posture
	// byte-for-byte:
	//
	//   - RequireCSRF        — the ambient-cookie anti-CSRF gate the bridge wrapped POST
	//                          /v1/billing/* with (Bearer/gateway callers are not CSRF-able).
	//   - RequestContext     — the gated request context commerce's handlers read.
	//   - IAMTokenRequired   — resolves the org from the gateway-validated X-Org-Id into
	//                          Locals("organization") — the namespace GetOrganization reads.
	//   - PinBillingSubject  — pins the caller's OWN account.Payer subject into BOTH query and
	//                          body AND fail-closes an unvalidated caller. It is the auth gate
	//                          on every one, and the IDOR control on the subject-scoped one.
	//
	// payment-methods is not in THIS block, and the reason is the chain, not the owner:
	// the three verbs are registered above on the pinned-subject chain their portal twins
	// use. THIS APP OWNS THAT ADDRESS — manifest.Apps names "/v1/billing/methods" on the
	// commerce row and withholds it from billing, and apps/billing/billing.go says so at
	// the spot the proxy used to sit ("served CO-RESIDENT by the commerce app, not proxied
	// from here"). It reads the other way round in the history because it WAS billing's
	// until the HTTP hop was deleted: that proxy pointed at a service compiled into the
	// same binary and was unconfigured here besides, so every saved-card call answered
	// 401/501 to a signed-in customer.
	//
	// One claim on one address is the whole requirement — openapi.Weave refuses a second
	// one rather than pick a winner. Moving the verbs did not satisfy it on its own: the
	// subset that PUBLISHES them was hand-edited to match, and copied the portal
	// operationIds along with the prose, so two paths claimed one id and no document could
	// be woven at all. A subset is generated (`make describe`) or it is wrong.
	//
	// subscriptions/:id/{cancel,reactivate} are org-NAMESPACE-scoped: commerce's handlers
	// resolve the subscription by `:id` WITHIN the caller's org namespace (a foreign org's id
	// is a 404 miss), so tenancy is the namespace IAMTokenRequired resolves and PinBillingSubject
	// is the fail-closed-anon auth gate — its pinned subject params are ignored by these
	// handlers (the SAME role it plays for the org-scoped payment-config read). The bridge's
	// subject-pin was likewise a no-op for these, so nothing was dropped. Neither
	// is typed yet — module work, per the module-handler note.
	app.Post("/v1/billing/subscriptions/:id/cancel",
		accountclient.RequireCSRF(),
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.CancelBillingSubscription,
	)
	app.Post("/v1/billing/subscriptions/:id/reactivate",
		accountclient.RequireCSRF(),
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.ReactivateBillingSubscription,
	)

	// In-process seams:
	//   - the commerce transport routes the S2S billing byte-stream into the co-resident
	//     app (the metering debit path) instead of a socket to a standalone pod.
	//   - the commerce client reads the Embedded's datastore DIRECTLY (entitlements +
	//     BalanceCents) — no HTTP shape at all.
	transport.SetApp(app.Fiber())
	PublishEmbedded(embedded)

	// Usage-cap enforcement on the FINANCE path. The unified binary records usage in
	// the finance ledger (fin.RecordUsage), NOT commerce's transaction store — which
	// it leaves empty — so the cap must read spend from, and fire alerts on, the
	// finance ledger. Two seams, both org-wide (the finance Entry carries no scope;
	// per-scope caps are a follow-up):
	//   - SetPeriodSpendReader: AuthorizeSpendCap's scopeSpentCents reads the org's
	//     finance period spend instead of the empty commerce transaction ledger, so
	//     a real LLM request increments the cap's `spent` and trips the 402.
	//   - SetUsageHook: after each finance debit, fire the org's alerts on the
	//     SAME crossing (the alert half), reading the same finance spend + debouncing.
	commercebilling.SetPeriodSpendReader(financePeriodSpend)
	financeclient.SetUsageHook(fireCapAlert)

	lg.Info("commerce embedded natively (hanzoai/commerce module on the shared zip app)",
		"data_dir", dataDir,
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// liveness is the probe's answer. Service precedes Status because the raw
// handler this op replaced marshalled a map, whose keys render sorted — keeping
// that order keeps the body byte-identical for every probe already parsing it.
// The name is fleet-unique on purpose: the weave refuses one schema name with
// two shapes, and the health names were already taken by sibling subsystems.
type liveness struct {
	// Service names the answering subsystem; it is always commerce.
	Service string `json:"service"`
	// Status is always ok: mounted is the only state that can answer.
	Status string `json:"status"`
}

// Answers ok whenever the commerce subsystem is mounted. It is registered
// before the module embed boots, so it keeps answering even when the embed
// failed and every business route serves the fail-closed 503 — which is the
// point: it reports that the process is reachable, never that the money plane
// is healthy. Unauthenticated, and under /_ so the ingress withholds it
// publicly.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func health(_ context.Context, _ *struct{}) (*liveness, error) {
	return &liveness{Service: "commerce", Status: "ok"}, nil
}

// mountCommerceFailClosed serves an honest JSON 503 on every commerce prefix when
// the embed cannot boot, so /v1/commerce/* answers "commerce unavailable" instead
// commerceErrorScope confines commerce's JSON error envelope to commerce's OWN
// routes. commercemid.ErrorHandlerJSON is a `/v1` GROUP middleware, and fiber
// matches group middleware by PREFIX, not by the handle a route registered on —
// so on the shared `/v1` it wraps every subsystem mounted AFTER commerce and
// flattens their typed zip.HTTPError (403/400/…) into a blanket 500 (the store
// envelope always renders 500). Guarded by Prefixes, the envelope stays on
// commerce and every other subsystem renders its own status via zip's default
// handler — the pre-commerce subsystems (kms, o11y, …) already do; this makes the
// post-commerce ones (projects, agents, wallets, …) match.
func commerceErrorScope() zip.Handler {
	envelope := commercemid.ErrorHandlerJSON()
	return func(c *zip.Ctx) error {
		if hasCommercePrefix(c.Path()) {
			return envelope(c)
		}
		return c.Next()
	}
}

// hasCommercePrefix reports whether path is a commerce-owned root (an exact prefix
// or a child of one), the SAME ownership Prefixes encodes for the
// fail-closed mount.
func hasCommercePrefix(path string) bool {
	for _, p := range Prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// of falling through to another subsystem's catch-all.
func mountCommerceFailClosed(app cloud.Router) {
	failed := func(c *zip.Ctx) error {
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(http.StatusServiceUnavailable, []byte(`{"error":"commerce unavailable","code":503}`))
	}
	// Raw, and All on purpose: the degraded surface must shadow every method
	// under every commerce prefix, and a typed op names one method at one path —
	// projecting this would publish operations that do not exist.
	for _, p := range Prefixes {
		app.All(p+"/*", failed)
	}
}

// financePeriodSpend is the usage-cap's period-spend source (injected into commerce
// via SetPeriodSpendReader). It returns the org's finance-ledger usage in cents since
// the start of the CURRENT UTC month — the window the cap resets on (mirrors
// commerce periodStartUTC). Org-wide: the finance Entry carries no project/service,
// so scope args are ignored and the org total is returned (what the covering
// org-wide spend-alert row binds on). A finance impl without the sum capability, or a
// split deploy (no co-resident finance), reports 0 — the cap can never over-count.
func financePeriodSpend(ctx context.Context, org string, test bool, _, _ string) (int64, error) {
	fin := financeclient.Current()
	if fin == nil {
		return 0, nil
	}
	summer, ok := fin.(interface {
		SumUsageSince(context.Context, string, bool, int64) (int64, error)
	})
	if !ok {
		return 0, nil
	}
	n := time.Now().UTC()
	since := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	return summer.SumUsageSince(ctx, org, test, since)
}

// fireCapAlert fires the org's alerts after a finance usage debit — the alert
// half of the cap on the finance path (wired via finance.SetUsageHook). It resolves
// the org's commerce datastore (where the spend-alert rows live) and calls the
// exported commerce trigger, which reads the org's period spend via financePeriodSpend
// and stamps/debounces. Detached + best-effort; never blocks the money path. Runs in
// its own goroutine (the hook is invoked with `go`), so a background context is right.
func fireCapAlert(org string, test bool, project, service string) {
	if strings.TrimSpace(org) == "" {
		return
	}
	ctx := commercensctx.WithNamespace(context.Background(), org)
	db := commercedatastore.New(ctx)
	commercebilling.FireSpendAlerts(ctx, db, org, test, project, service, nil)
}

// requireSpendCapAdmin gates a spend-alert WRITE to a validated ORG ADMIN or platform
// SuperAdmin (the unforgeable SanitizeIdentity-minted X-User-IsOrgAdmin / isAdmin bits),
// OR the trusted in-proc S2S service token (the SuperAdmin cap-oversight Forward + internal
// automation). A validated non-admin MEMBER is REFUSED (403): a spend cap is a financial
// safety boundary — a member must not be able to delete the org's cap (→ unbounded spend)
// or set a punitive 1¢ cap (→ org-wide 402 DoS). The read paths (list/authorize) stay
// member/S2S-open; only the mutations require admin.
func requireSpendCapAdmin() zip.Handler {
	return func(c *zip.Ctx) error {
		if principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c) || accountclient.IsServiceToken(c) {
			return c.Next()
		}
		return zip.ErrForbidden("org admin required to change spend caps")
	}
}
