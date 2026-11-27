// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/commerce/transport"
	financeclient "github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/readiness"
	commercemod "github.com/hanzoai/commerce"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	catalogapi "github.com/hanzoai/commerce/api/catalog"
	planapi "github.com/hanzoai/commerce/api/plan"
	rateapi "github.com/hanzoai/commerce/api/rate"
	commerceresources "github.com/hanzoai/commerce/api/resources"
	commercestore "github.com/hanzoai/commerce/api/store"
	"github.com/hanzoai/commerce/billing/paywall"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	commercedb "github.com/hanzoai/commerce/db"
	commercelog "github.com/hanzoai/commerce/log"
	commercemid "github.com/hanzoai/commerce/middleware"
	"github.com/hanzoai/commerce/middleware/iammiddleware"
	commercensctx "github.com/hanzoai/commerce/util/nscontext"
	"github.com/hanzoai/commerce/util/permission"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func init() {
	// In-process CommerceClient factory — pickCommerceClient calls it when the
	// commerce subsystem is enabled. Registered HERE (not called directly from
	// package cloud) because the commerce client's entitlement client imports
	// clients/plan, which imports cloud: the hook keeps the package graph acyclic.
	cloud.RegisterCommerceClientFactory(func(cfg *cloud.Config, _ luxlog.Logger) cloud.CommerceClient {
		return InProcessClient()
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
	// endpoint — each a leaf under /v1/commerce rather than a top-level address of
	// its own. The reason the leaves were ever named here is the bare /v1/*
	// AI catch-all: it refuses on a prepaid BALANCE, so an unclaimed store read
	// or cart open 402'd for want of an LLM balance it has nothing to do with
	// (the karma /v1/store/current outage). One root claims them all.
	"/v1/commerce",
}

// seam is where each of an org's commerce stores lives. commerce asks the seam for the
// store of a namespace and builds its own over the handle it is given; the seam opens it
// as every store of this deployment is opened (locus.Use): a schema on hanzo-sql when
// CLOUD_SQL_DSN is set, its file under dir otherwise, and a file moved to the server on
// first open when CLOUD_SQL_MOVE says so. The files are where commerce always kept them
// (dir is its data directory), under the key the data plane derives for each, so a
// deployment's existing stores are found and read as they are.
//
// The handle's keying is the data plane's and not commerce's: commerce is given no master
// key, and no longer needs the live codec its own dual pool required.
func seam(dir string, lg luxlog.Logger) commercedb.Seam {
	return func(ns namespace.Namespace, open func(*sql.DB, bool) (io.Closer, error)) (io.Closer, error) {
		return locus.Use(context.Background(), lg, ns, tenantSubsystem, dir, func(h *sql.DB) (io.Closer, error) {
			return open(h, dialect.Of(h).Postgres())
		})
	}
}

// tenantSubsystem names what commerce's per-tenant store holds: the basename of its file
// (commerce.db) and the second half of its key. It is commerce's own constant
// (db.tenantSubsystem) and a name on live volumes, so it is stated here and not shared.
const tenantSubsystem = "commerce"

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
// THE NOTE IS NOW A TEST, which is the only form of it that can go red.
// typed_wire_test.go holds two ledgers — `untypedByDesign` for the three wires
// this stack cannot describe, `typingOwed` for the 173 the module owes — and
// requires them to SUM with the typed ops to the surface the live router serves.
// This paragraph enumerated nothing, so it could not notice that the bundles
// below bind 175 addresses; the ledger names every one, and a route added
// upstream arrives unclassified rather than unnoticed.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Mount boots commerce ON the shared zip app (native co-residence).
// commerce's own setupRoutes registers /v1/commerce/* and /_/commerce/*
// directly; the standalone-only surfaces (bare /healthz, legacy /admin SPA,
// checkout SPA root catch-all, Listen) are skipped by the SharedApp contract.
// This adapter registers the remaining wire-contract families with commerce's
// own gate chains (see Prefixes).
func Use(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return errors.New("commerce: nil app")
	}
	// The ledger lives here, so the methods that read and move it are published
	// here: balance, the prepaid gate, the debit and the credit.
	exposeBalance()
	exposeMeter(deps.Metering)
	exposeCredit()
	exposeSource()
	exposeLine(deps.Audit)
	exposeCoupon(deps.Audit)
	exposePromo(deps.Audit)
	exposeUsage()
	exposeSubs()
	exposeStoreCosts()
	exposeTxns()
	exposeCatalogRefresh()
	exposeSpend()
	exposeScopeRules()
	exposeInvoices()
	exposeStatement()
	exposeGrants()
	exposeAlerts()
	exposeRails()
	// The posture, the plan and the card endpoints: the four families the money
	// endpoint serves that this app used to answer at /v1/billing itself. They are ops
	// here for the same reason the rest are — the store has one owner, and the
	// address belongs to somebody else.
	exposePosture()
	exposeRouting()
	exposeAgent()
	exposePlan()
	exposeLink()
	exposeOverrides()
	exposeAuthority()
	exposeEntitlement()
	exposeRate()
	exposeLimits()

	// The embedded hanzoai/commerce module registers its own addresses on the
	// concrete App. commerce's app-wide reach (it wraps ALL of /v1, below) is
	// declared as Plugin.Root at its composition root.

	// THE BRIDGE, on commerce's OWN subtree and ahead of every leaf it serves.
	//
	// A typed op receives a context and no request, so the validated org has to be
	// parked on that context before the op runs — which is what cloud.Bridge does,
	// and what orgOf (payments.go) reads back. Serve installs one app-wide, so
	// nothing here was live-broken; what was missing is that NO PACKAGE TEST RUNS
	// Serve. Three tests in this package installed it by hand
	// (risk_payments_test.go, orgof_s2s_test.go, ledger_peer_test.go), which
	// is three copies of one fact and covers only the ops those three drive: a
	// typed op added here without remembering would resolve no org and 403 a valid
	// request, in this package's tests only, with the production path fine.
	//
	// It goes on the app, which is the grant commerce declares: the embedded
	// module registers its own addresses here, and a group's Use never reaches a
	// leaf declared on the parent.
	//
	// Installing it twice is harmless and is what every other converted app does:
	// each Bridge derives the same facts from the same request, and the status
	// carry-back reads the innermost binding, so the answer is unchanged.
	app.Use(zip.H(cloud.Bridge()))

	lg := luxlog.Default().New("subsystem", "commerce")
	// The ledger reads the debits the request path posted to the usage stream.
	if deps.Metering != nil {
		go deps.Metering.Drain(context.Background(), lg.Warn)
	}
	// The other direction of the same idea as the ops above: those publish what
	// this process OWNS, and this reaches for the one thing it does not. The credit
	// endpoints below screen against a model that can only live in one binary, so the
	// scorer is installed as a plane client — cloud.SetRiskScorer's first producer
	// (risk.go).
	installRiskScorer(lg)
	// THE CREDIT SCREEN, resolved ONCE and handed to every endpoint that mints.
	//
	// There is ONE endpoint onto the mint: the browser's POST /v1/billing/topup/token,
	// whose typed plane ops carry the screen (sale_rpc.go). A SECOND address onto the
	// same money move used to sit at POST /v1/commerce/payments — one act with two
	// public names, two operation ids and two MCP tools — and it was retired rather
	// than screened twice. It had been opened because the billing route was raw and so
	// reached no agent; that route is a typed op now, which is what made the second
	// address redundant instead of necessary.
	//
	// IT GOES ON THE HANDLER, NOT ON THE ROUTER, and that is what makes it a control on
	// the MINT rather than on a URL. A typed op is recorded once and projected four ways
	// — REST, MCP tool, by-name call, CLI — and all four dispatch to the op's handler,
	// while router middleware wraps only the fiber handler REST is served through. The
	// screen was once mounted on a router while `takePayment` sat in tools/list, so an
	// agent's tools/call reached the same authorized deposit unscreened.
	//
	// It is one VALUE, not one call per endpoint, so there is no arrangement of these
	// registrations in which two of them hold different screens.
	screen := riskGate(lg)
	exposeSale(screen)
	exposeOrders(screen)
	// The typed cart surface (cart.go). It reads and writes the embedded module's
	// own cart store, so unlike the payment ops it is only useful when the embed
	// below succeeds — and it is registered HERE anyway, ahead of it, so a cart
	// call against a failed embed answers the 503 orgOf raises ("commerce is
	// not co-resident in this process") instead of the bare 404 an unregistered
	// route gives. A missing basket and a missing feature are different answers.
	exposeCart(app)
	exposeOrder(app)
	exposeCustomer(app, deps.Audit)

	// The liveness probe, registered FIRST so it answers even when the embed
	// fails below. Typed, so the probe is a published op like every other.
	//
	// It was /_/commerce/healthz, which HIP-0139 §3.3 does not allow, and the
	// address it folds to is the one serve.go would otherwise have invented:
	// GET /v1/<name>/health. So this is not a rename onto a free address — the
	// plugin declares OwnsHealth to take it, and commerce answers its own probe
	// instead of the host's always-ok stub. Two health spellings under one root
	// would be the second address nobody needs.
	app.Group("/v1/commerce").Get("/health", health)

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
	if cloud.DataDir() != "" {
		dataDir = filepath.Join(cloud.DataDir(), "commerce")
	}

	baseHost, err := placeBase(context.Background(), lg, dataDir)
	var embedded *commercemod.Embedded
	if err == nil {
		embedded, err = commercemod.Embed(context.Background(), commercemod.EmbedConfig{
			DataDir: dataDir,
			Seam:    seam(dataDir, lg),
			// The module's Base (its cache and settings): two schemas of hanzo-sql and a
			// bucket when the deployment keeps its state there, else the files (base.go).
			Base: baseHost,
			// RequireIdentity stays gateway-owned: the gateway in front of the cloud
			// binary is the trust boundary per HIP-0026.
			RequireIdentity: false,
			// THE native co-residence contract: commerce registers its routes on
			// cloud's own app — no second engine, no net/http adaptation.
			App: app,
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
			Secrets: hostSecrets(deps.KMS),
			// The kind a new org's record is born in: live on mainnet, test on every
			// other network (see born).
			Born: born(),
			// The most any org transacts in: no cap on mainnet, test on every other
			// network (see ceiling).
			Ceiling: ceiling(),
			// Commerce's outbound mail — the operator's notice of every paid plan sale —
			// goes out on the notify rail, the same delivery IAM's codes use (see mail.go).
			Mail: notifyMail{},
		})
	}
	if err != nil {
		lg.Error("commerce embed failed — serving fail-closed 503 (cloud stays up)", "err", err)
		// State, not just a log line: /v1/health reports this and the release smoke
		// refuses an image whose planes are dead. Without it a fail-closed commerce
		// is indistinguishable from a commerce that was never enabled.
		readiness.Degraded("commerce", err)
		mountCommerceFailClosed(app)
		return nil
	}

	// The plan links the tier and plan reads consult for an org on no plan of its own.
	// Opened beside the embed for the same reason: a process that serves the tier op
	// without it could not tell an org with no link from one it cannot read.
	linked, err := openLinks(lg, dataDir)
	if err != nil {
		lg.Error("plan links not opened — serving fail-closed 503 (cloud stays up)", "err", err)
		readiness.Degraded("commerce", err)
		mountCommerceFailClosed(app)
		return nil
	}
	if old := planLinks.Swap(linked); old != nil {
		_ = old.Close()
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
	// (api.Route hands AdminRoute its adminRequired). The scheduled runs that
	// reach these routes — the model sync, the auto-recharge poke below — are the
	// platform's own application, an IAM identity under the reserved admin org,
	// and IsPlatform admits it; a shared service token used to be their only
	// door, and when it was not wired the model sync 403'd on every attempt and
	// the catalog sat empty in production.
	// The bundle binds the module's own route table, so no route in it is
	// typed yet — module work, per the module-handler note.
	catalogapi.AdminRoute(commerceV1, commercemid.TokenRequired())

	// The plan authority's raw rows on the SAME group: GET /v1/commerce/plans/entries,
	// requireSuperAdmin-gated by the handler (anon → 403). It is the one route of
	// api/plan this binary mounts. A plan is added or restated through the admin
	// app's typed ops (POST /v1/admin/plans, PUT /v1/admin/plans/:slug), which gate,
	// audit with the row before and after, and ask exposeAuthority's plane ops; the
	// boot seed below reconciles the rest to the shipped catalog.
	commerceV1.Group("/plans").Raw(http.MethodGet, "/entries", planapi.ListEntries)

	// The RATE authority's CRUD — what one unit of anything costs, edited at
	// admin.hanzo.ai. Same shape and gate as the plan CRUD beside it: a rate is
	// cross-tenant money, so every handler asks IsSuperAdmin itself rather than
	// trusting the bundle's token gate.
	//
	// It takes NO seed source, deliberately. Plans carry an embed the boot seed
	// reconciles against; rates do not, because the document that fills them is
	// 506 published prices and compiling it in is what made a price change wait
	// for a build. POST /v1/rates/import loads it instead, reconciling rather
	// than replacing, so an operator's edit is never overwritten by an import.
	rateapi.AdminRoute(commerceV1)

	// Reconcile the plan authority to the catalog this binary ships, the way the
	// standalone does at its own boot. GET /v1/billing/plans reads the authority
	// and falls back to the embed only when the authority is empty, so without
	// this a repriced catalog ships in the binary and never reaches a customer —
	// the rows keep serving whatever a past seed wrote.
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
	// discounts, sales channels, stock locations, webhooks.
	//
	// Commerce is a PLUGIN of this binary, so its merchant surface is reachable
	// through this binary or it is not reachable at all — there is no separate
	// commerce backend to fall back to. Every admin data view is served by the
	// resource bundle mounted here.
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

	// The provider webhook route, POST /v1/commerce/webhooks/:provider, is
	// commerce's own declaration (its setupRoutes, on the shared app), so this
	// binary and a standalone commerce serve it from one place. It stays in this
	// process because the signature is over the RAW body — Square signs the
	// notification URL concatenated with it — and because the handler settles out
	// of the store this process owns.

	// EVERY /v1/billing ENDPOINT IS GONE FROM THIS FILE, and what replaced them is
	// beside them: the plane ops in this package's *_rpc.go files.
	//
	// There used to be thirty-seven registrations here, each one a commerce
	// module handler mounted at a /v1/billing address with its own gate chain
	// re-derived by hand — the console reads, the mint writes, the spend-cap
	// CRUD. They were right to exist: commerce's own route
	// bundle is never compiled into this binary, so an address nothing named here
	// reached nobody, and the history each of them carried was a real outage.
	//
	// They were also the wrong app's addresses. /v1/billing is the billing
	// capability's root (HIP-0018 carries `capability: billing`; HIP-1220 §2 says
	// commerce must not serve it), and every one of those thirty-seven was a line
	// in openapi/misfiled.txt. So the endpoints moved to apps/billing and the ANSWERS
	// stayed here, where the store is: each question is a plane op on a
	// value-taking core exported by the module, and billing asks it by name.
	//
	// What that bought beyond conformance is the gate chains. A relayed op takes
	// its tenant from the caller — there is no org field on any input — and its
	// billing subject from the endpoint that resolved it, so the pin that eight
	// separate PinBillingSubject links used to apply per route is now a property
	// of the types. A route cannot be added here that forgets it, because there is
	// no route here to add.

	// In-process clients:
	//   - the commerce transport routes the S2S billing byte-stream into the co-resident
	//     app (the metering debit path) instead of a socket to a standalone pod.
	//   - the commerce client reads the Embedded's datastore DIRECTLY (entitlements +
	//     BalanceCents) — no HTTP shape at all.
	transport.SetApp(app.Fiber())
	PublishEmbedded(embedded)

	// The agent checkout protocols (agentic.go): each mounts only when its operator
	// configured it, and only here, over the checkout the embed above just booted.
	mountAgentic(app, lg, hostSecrets(deps.KMS))

	// Usage-cap enforcement on the FINANCE path. The unified binary records usage in
	// the finance ledger (fin.RecordUsage), NOT commerce's transaction store — which
	// it leaves empty — so the cap must read spend from, and fire alerts on, the
	// finance ledger. Two clients, both scoped by the (project, service) each finance
	// entry records:
	//   - SetPeriodSpendReader: AuthorizeSpendCap's scopeSpentCents reads the cap's
	//     scope's finance period spend instead of the empty commerce transaction
	//     ledger, so a real LLM request increments the cap's `spent` and trips the 402.
	//   - SetUsageHook: after each finance debit, fire the org's alerts on the
	//     SAME crossing (the alert half), reading the same finance spend + debouncing.
	commercebilling.SetPeriodSpendReader(financePeriodSpend)
	// An org on account (account.go): finance gives its own account a line with no
	// limit, and after each debit the SuperAdmins hear when it owes past the alert.
	onAccount.trail = deps.Audit
	financeclient.SetAccount(onAccount.on)
	financeclient.SetUsageHook(func(org string, test bool, project, service string) {
		fireCapAlert(org, test, project, service)
		onAccount.owing(org, test)
		if !test {
			mirrorOrgBook(org)
		}
	})
	// Credits added tell the deployment once, where the wallet's balance rises (funds.go).
	financeclient.SetDepositHook(func(org string) {
		told(org, funds.Credit)
		mirrorOrgBook(org)
	})

	// Heal any stale exchange shared-address collisions on boot so RPC deposit watchers
	// un-wedge immediately, and start periodic cursor-lag / SLA stall monitoring.
	healAllWatchedCollisions(context.Background())
	startWatcherHealthMonitor(context.Background(), lg, deps.Audit)

	// Silence commerce's verbose per-key-encode [DEBUG] log spam outside dev — in prod
	// commerce emitted 115+ [DEBUG] lines/sec (Encode ...), burning CPU and log ingest.
	if e := strings.ToLower(strings.TrimSpace(cloud.Env())); e != "dev" && e != "development" {
		commercelog.SetVerbose(false)
	}

	lg.Info("commerce embedded natively (hanzoai/commerce module on the shared zip app)",
		"data_dir", dataDir,
		"brand", cloud.Brand(),
		"env", cloud.Env(),
	)
	return nil
}

// liveness is the probe's answer. Service precedes Status because the raw
// handler this op replaced marshalled a map, whose keys render sorted — keeping
// that order keeps the body byte-identical for every probe already parsing it.
// The name is fleet-unique on purpose: the compose refuses one schema name with
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
func health(_ context.Context, _ *cloud.Unit) (*liveness, error) {
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
func mountCommerceFailClosed(app *zip.App) {
	failed := func(c *zip.Ctx) error {
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(http.StatusServiceUnavailable, []byte(`{"error":"commerce unavailable","code":503}`))
	}
	// Raw, and All on purpose: the degraded surface must shadow every method
	// under every commerce prefix, and a typed op names one method at one path —
	// projecting this would publish operations that do not exist.
	for _, p := range Prefixes {
		app.Group(p).Raw(zip.MethodAll, "/*", failed)
	}
}

// financePeriodSpend is the usage-cap's period-spend source (injected into commerce
// via SetPeriodSpendReader). It returns the org's finance-ledger usage in cents since
// the start of the CURRENT UTC month — the window the cap resets on (mirrors
// commerce periodStartUTC), in the cap's own (project, service) scope: a cap on one
// service sums that service's debits and an org-wide cap ("", "") sums them all. A
// finance impl without the sum capability, or a process with no ledger, is an ERROR:
// an enforced cap then reaches no verdict and refuses, where a zero would read
// "nothing spent" and never trip.
func financePeriodSpend(ctx context.Context, org string, test bool, project, service string) (int64, error) {
	// A spend nobody can read is an error and never zero: a zero here is a cap that
	// reads "nothing spent" and never trips.
	fin := financeclient.Current()
	if fin == nil {
		return 0, errors.New("spend cap: no finance ledger in the process that owns the caps")
	}
	summer, ok := fin.(interface {
		SumUsageSince(context.Context, string, bool, string, string, int64) (int64, error)
	})
	if !ok {
		return 0, fmt.Errorf("spend cap: finance ledger %T sums no usage", fin)
	}
	n := time.Now().UTC()
	since := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	return summer.SumUsageSince(ctx, org, test, project, service, since)
}

// fireCapAlert fires the org's alerts after a finance usage debit — the alert
// half of the cap on the finance path (wired via finance.SetUsageHook). It resolves
// the org's commerce datastore (where the spend-alert rows live) and calls the
// exported commerce trigger, which reads each covering cap's period spend via
// financePeriodSpend and stamps/debounces. Detached + best-effort; never blocks the money path. Runs in
// its own goroutine (the hook is invoked with `go`), so a background context is right.
func fireCapAlert(org string, test bool, project, service string) {
	if strings.TrimSpace(org) == "" {
		return
	}
	ctx := commercensctx.WithNamespace(context.Background(), org)
	db := commercedatastore.New(ctx)
	commercebilling.FireSpendAlerts(ctx, db, org, test, project, service, nil)
}
