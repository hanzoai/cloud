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
// host's manifest. Derived instead, the walk would read the group this file
// opens for the store/catalog/plan bundle as a claim on whatever prefix that
// group happens to carry. Same list, one owner, stated once.
var Prefixes = []string{
	// ONE ROOT for every merchant noun (HIP-1220 §1). It carries the public
	// checkout and org reads, the merchant resources, the storefront, the
	// SuperAdmin catalog and plan CRUD, the typed cart and the typed payment
	// door — each a leaf under /v1/commerce rather than a top-level address of
	// its own. The reason the leaves were ever named here is the bare /v1/*
	// AI catch-all: it refuses on a prepaid BALANCE, so an unclaimed store read
	// or cart open 402'd for want of an LLM balance it has nothing to do with
	// (the karma /v1/store/current outage). One root claims them all.
	"/v1/commerce",
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
	exposeSubs()
	exposeStoreCosts()
	exposeTxns()
	exposeSpend()
	exposeScopeRules()
	exposeInvoices()
	exposeStatement()
	exposeGrants()
	exposeAlerts()
	exposeRails()

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
	// POST /v1/billing/topup/token below, and the agent's typed POST /v1/commerce/payments in
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
	// shared core is the whole argument: POST /v1/commerce/payments reaches the same authorized
	// deposit the top-up does, and it is published as an MCP tool besides — so the
	// screen is composed onto its HANDLER, where every projection of the op runs it,
	// rather than onto the router only REST is served through. exposePayments puts the
	// screen on the WRITE only; the receipt read mints nothing.
	exposePayments(zapp, screen)
	// The typed cart surface (cart.go). It reads and writes the embedded module's
	// own cart store, so unlike the payment ops it is only useful when the embed
	// below succeeds — and it is registered HERE anyway, ahead of it, so a cart
	// call against a failed embed answers the 503 payingOrg raises ("commerce is
	// not co-resident in this process") instead of the bare 404 an unregistered
	// route gives. A missing basket and a missing feature are different answers.
	exposeCart(zapp)

	// The liveness probe, registered FIRST so it answers even when the embed
	// fails below. Typed, so the probe is a published op like every other.
	//
	// It was /_/commerce/healthz, which HIP-0139 §3.3 does not allow, and the
	// address it folds to is the one serve.go would otherwise have invented:
	// GET /v1/<name>/health. So this is not a rename onto a free address — the
	// plugin declares OwnsHealth to take it, and commerce answers its own probe
	// instead of the host's always-ok stub. Two health spellings under one root
	// would be the second address nobody needs.
	zip.Get(zapp, "/v1/commerce/health", health)

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

	// THE ONE GROUP the module's own route tables bind onto, at commerce's own
	// root. Chain mirrors the standalone /v1 bundle: gated request context, host,
	// IAM resolution; each Route's own tokenRequired arg gates its CRUD. The
	// bundles bind the module's route table, so no route in them is typed yet —
	// module work, per the module-handler note.
	//
	// The prefix is what folds the addresses. Each of these tables registers
	// RELATIVE to the group it is handed — store.Route at /store, catalogapi at
	// /catalog, planapi at /plans — so opening the group at /v1/commerce puts
	// every one of them under commerce's root and the top-level /v1/store,
	// /v1/catalog and /v1/plans spellings cease to exist (HIP-1220 §1). It was
	// /v1, which is also why this file needed a Global middleware grant and a
	// commerceErrorScope guard to keep commerce's JSON error envelope off every
	// sibling subsystem mounted after it.
	//
	// It is the SAME group the merchant resources bind onto further down: two
	// groups at one prefix with one chain would run that chain twice per request.
	commerceV1 := app.Group("/v1/commerce")
	commerceV1.Use(commercemid.AddHost(), commercemid.RequestContext(), commerceErrorScope())
	// Unconditional, exactly like the standalone bundle: IAMTokenRequired
	// no-ops gracefully when IAM is not initialized.
	commerceV1.Use(iammiddleware.IAMTokenRequired())
	commercestore.Route(commerceV1, commercemid.TokenRequired())

	// Platform-admin catalog CMS on the SAME group: GET/POST/PUT/DELETE
	// /v1/commerce/catalog/entries + POST /v1/commerce/catalog/seed. setupRoutes wires only the
	// public read (/v1/commerce/catalog); the CRUD rides the standalone /v1 bundle
	// (api.Route → catalogApi.AdminRoute), which the co-resident embed skips — so
	// register it here, exactly as the standalone does. The group's IAMTokenRequired
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
	catalogapi.AdminRoute(commerceV1, commercemid.TokenRequired())

	// Platform-admin subscription/DNS plan authority CRUD on the SAME group:
	// GET/POST/PUT/DELETE /v1/commerce/plans/entries + POST /v1/commerce/plans/seed (increment 3a).
	// Mirrors the catalog mount: the standalone wires it on the /v1 bundle
	// (api.Route → planApi.AdminRoute), which the co-resident embed skips. The
	// embed seed SOURCE is injected here (the composition root) — commercebilling.
	// SeedRows, the SAME @hanzo/plans embed the boot seed + resolveSubscriptionPlan
	// read — so api/plan never imports api/billing. Each handler is
	// requireSuperAdmin-gated (anon → 403); prices are admin-editable but the mint
	// gates score the IMMUTABLE embed, so an edit never moves a charge gate.
	// The bundle binds the module's own route table, so no route in it is
	// typed yet — module work, per the module-handler note.
	planapi.AdminRoute(commerceV1, commercebilling.SeedRows)

	// Reconcile the plan authority to the catalog this binary ships, the way the
	// standalone does at its own boot. GET /v1/billing/plans reads the authority
	// and falls back to the embed only when the authority is empty, so without
	// this a repriced catalog ships in the binary and never reaches a customer —
	// the rows keep serving whatever a past seed wrote, and the only way to move
	// them is a hand-driven POST /v1/commerce/plans/seed.
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
	//
	// It binds onto the group opened above rather than a second one at the same
	// prefix: same address, same chain, so two groups would run AddHost,
	// RequestContext and IAMTokenRequired twice on every merchant request.
	commerceresources.Route(
		commerceV1,
		commercemid.TokenRequired(),
		commercemid.TokenRequired(permission.Admin),
		paywall.Require,
		nil,
	)

	// Provider webhook intake, under commerce's own root. Chain mirrors the
	// commerce-standalone posture: gated request context, then the sessionless
	// HMAC-verified handler. It stays raw because it speaks the provider's
	// protocol: the signature verifies the raw payload bytes, so there is no
	// typed input to declare.
	//
	// IT STAYS IN THIS PROCESS while every other /v1/billing door moved to the
	// billing capability, and the reason is the signature. The processor signs
	// the RAW BODY — Square signs the notification URL concatenated with it — so
	// the bytes have to be verified before anything can be typed out of them,
	// and a relay that re-encoded them would be verifying a payload the
	// processor never signed. It also SETTLES: the handler credits the wallet
	// out of commerce's own store, which is the store this process owns.
	//
	// So the address follows the process rather than the product prefix
	// (HIP-0139 §3.1), and the processor dashboards are repointed to it.
	app.Post("/v1/commerce/webhooks/:provider", commercemid.RequestContext(), commercebilling.HandleProviderWebhook)

	// EVERY /v1/billing DOOR IS GONE FROM THIS FILE, and what replaced them is
	// beside them: the plane ops in this package's *_rpc.go files.
	//
	// There used to be thirty-seven registrations here, each one a commerce
	// module handler mounted at a /v1/billing address with its own gate chain
	// re-derived by hand — the console reads, the mint writes, the service-token
	// twins, the spend-cap CRUD. They were right to exist: commerce's own route
	// bundle is never compiled into this binary, so an address nothing named here
	// reached nobody, and the history each of them carried was a real outage.
	//
	// They were also the wrong app's addresses. /v1/billing is the billing
	// capability's root (HIP-0018 carries `capability: billing`; HIP-1220 §2 says
	// commerce must not serve it), and every one of those thirty-seven was a line
	// in openapi/misfiled.txt. So the doors moved to apps/billing and the ANSWERS
	// stayed here, where the store is: each question is a plane op on a
	// value-taking core exported by the module, and billing asks it by name.
	//
	// What that bought beyond conformance is the gate chains. A relayed op takes
	// its tenant from the caller — there is no org field on any input — and its
	// billing subject from the door that resolved it, so the pin that eight
	// separate PinBillingSubject links used to apply per route is now a property
	// of the types. A route cannot be added here that forgets it, because there is
	// no route here to add.

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
// is healthy. Unauthenticated: a probe that needs a credential is a probe that
// reports the credential.
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
