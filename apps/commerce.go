// Copyright © 2026 Hanzo AI. MIT License.

// commerce.go mounts the hanzoai/commerce MODULE into the unified cloud binary
// (HIP-0106) via the NATIVE co-residence contract: commerce registers its routes
// directly on the HOST's zip app (EmbedConfig.App) — one router, one specificity
// space, zero handler adaptation. This adapter narrows cloud.Deps, boots the
// embed, and wires the in-process seams. Direction is one-way: cloud → commerce.
//
// PCI SCOPE. Commerce is a LIGHT ROUTER, NOT in PCI-DSS scope: tokens + intent IDs
// only, NEVER a PAN. PAN-touching paths call the out-of-process Payments / Vault
// (ZAP-RPC); when those clients are absent the payment handlers fail closed while
// tenant config + admin stay served — mountCommerce warns loudly at startup.
//
// FAIL-SOFT. A broken Embed does NOT crash the binary: commerce degrades to a 503
// on its own prefixes while every co-resident subsystem stays up.
package apps

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	accountclient "github.com/hanzoai/cloud/clients/account"
	"github.com/hanzoai/cloud/clients/commerceclient"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	financeclient "github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/commerce"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercestore "github.com/hanzoai/commerce/api/store"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	commercemid "github.com/hanzoai/commerce/middleware"
	"github.com/hanzoai/commerce/middleware/iammiddleware"
	commercensctx "github.com/hanzoai/commerce/util/nscontext"
	log "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func init() {
	// In-process CommerceClient factory — pickCommerceClient calls it when the
	// commerce subsystem is enabled. Registered HERE (not called directly from
	// package cloud) because commerceclient's entitlement client imports
	// clients/plan, which imports cloud: the hook keeps the package graph acyclic.
	cloud.RegisterCommerceClientFactory(func(cfg *cloud.Config, _ log.Logger) cloud.CommerceClient {
		return commerceclient.InProcessClient(cfg.Brand)
	})
}

// commercePrefixes is every root path the commerce surface owns on the shared
// app. Under the native SharedApp contract most of these are registered by
// commerce's own setupRoutes; the list is the fail-closed 503 set AND the wire
// contract commerce_prefix_test pins — the route families a session gate or the
// AI /v1/* catch-all must never swallow:
var commercePrefixes = []string{
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
	// Payment-provider webhook receiver (POST /v1/billing/webhooks/:provider —
	// Square et al). The provider's HMAC over the registered notification URL +
	// body IS the auth; a bearer gate is impossible for provider callbacks.
	"/v1/billing/webhooks",
	// The platform auto-recharge sweep (PlatformOnly, POST .../run-all). The
	// durable cron's poke carries the COMMERCE_SERVICE_TOKEN bearer; without
	// this owner it lands on the account-bridge /v1/billing/* catch-all, whose
	// session gate 403s a service token. (Landed 5x before the unfork — #274 —
	// and the pin test lives beside THIS list so it can't silently regress.)
	"/v1/billing/auto-recharge",
}

// mountCommerce boots commerce ON the shared zip app (native co-residence).
// commerce's own setupRoutes registers /v1/commerce/* and /_/commerce/*
// directly; the standalone-only surfaces (bare /healthz, legacy /admin SPA,
// checkout SPA root catch-all, Listen) are skipped by the SharedApp contract.
// This adapter registers the remaining wire-contract families with commerce's
// own gate chains (see commercePrefixes).
func mountCommerce(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("commerce: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("commerce: nil deps.Logger")
	}
	lg := deps.Logger.New("subsystem", "commerce")
	if deps.Payments == nil {
		lg.Warn("commerce: deps.Payments is nil — payment intent paths will fail; tenant config + admin still served")
	}
	if deps.Vault == nil {
		lg.Warn("commerce: deps.Vault is nil — vault charge paths unavailable; tenant config + admin still served")
	}

	// Native zip health endpoint — registered FIRST so probes answer even when
	// the embed fails below.
	app.Get("/_/commerce/healthz", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "service": "commerce"})
	})

	// commerce persists its per-org SQLite + `base` tree under <DataDir>/commerce,
	// NEVER at DataDir directly: cloud already owns DataDir/orgs and DataDir/base,
	// and commerce also writes orgs/ + base/ — sharing the root would collide two
	// apps on the same SQLite files and corrupt them.
	dataDir := "/var/lib/cloud/commerce"
	if deps.DataDir != "" {
		dataDir = filepath.Join(deps.DataDir, "commerce")
	}

	embedded, err := commerce.Embed(context.Background(), commerce.EmbedConfig{
		DataDir: dataDir,
		// RequireIdentity stays gateway-owned: the gateway in front of the cloud
		// binary is the trust boundary per HIP-0026.
		RequireIdentity: false,
		// THE native co-residence contract: commerce registers its routes on
		// cloud's own app — no second engine, no net/http adaptation.
		App: app,
		// ONE LEDGER: commerce's POST /v1/billing/credit mints into cloud's native
		// finance ledger (the SAME per-org account the AI spend-gate reads), so a
		// granted credit is immediately spendable. commerce.Embed calls
		// creditledger.Set(this) before routes register; nil would leave commerce on
		// its own datastore (standalone), but in this unified binary finance is
		// co-resident, so we inject the finance-backed ledger adapter.
		Ledger: ledger{},
	})
	if err != nil {
		lg.Error("commerce embed failed — serving fail-closed 503 (cloud stays up)", "err", err)
		mountCommerceFailClosed(app)
		return nil
	}

	// The BARE /v1/store surface (see commercePrefixes). Group-scoped chain
	// mirrors the standalone /v1 bundle: gated request context, host, IAM
	// resolution; store.Route's own tokenRequired arg gates the CRUD.
	storeV1 := app.Group("/v1")
	storeV1.Use(commercemid.AddHost(), commercemid.RequestContext(), commerceErrorScope())
	// Unconditional, exactly like the standalone bundle: IAMTokenRequired
	// no-ops gracefully when IAM is not initialized.
	storeV1.Use(iammiddleware.IAMTokenRequired())
	commercestore.Route(storeV1, commercemid.TokenRequired())

	// Provider webhook intake at the LIVE registered path. Chain mirrors the
	// commerce-standalone posture: gated request context, then the sessionless
	// HMAC-verified handler.
	app.Post("/v1/billing/webhooks/:provider", commercemid.RequestContext(), commercebilling.HandleProviderWebhook)

	// Durable-cron auto-recharge poke (COMMERCE_SERVICE_TOKEN bearer) at its
	// live path — the bridge's session gate would 403 the poke. Same gate
	// chain the commerce route table uses: TokenRequired authenticates the
	// service token, PlatformOnly authorizes the mint.
	app.Post("/v1/billing/auto-recharge/run-all",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		commercemid.PlatformOnly(),
		commercebilling.RunAutoRechargeAllOrgs,
	)

	// GET /v1/billing/plans — the public tier catalog the console renders. commerce's
	// legacy api.Route() billing bundle (ListPlans, invoices, subscriptions, …) is NOT
	// registered by the co-resident embed: setupRoutes wires only /v1/commerce/*, so
	// /v1/billing/plans has NO handler in this binary. The account bridge's
	// /v1/billing/* wildcard (order 122) then forwards the read BACK to commerce at
	// COMMERCE_URL — which defaults to the public api.hanzo.ai edge — re-entering the
	// same bridge in an unbounded self-dispatch loop that surfaces as a 502
	// ("commerce unreachable: Get https://api.hanzo.ai/v1/billing/plans"). Registering
	// the static ListPlans handler HERE (order 100, ahead of the bridge) shadows that
	// wildcard and serves plans in-process — the same co-resident move billing.go makes
	// for usage/balance. RequestContext supplies the namespaced context promo.Active reads.
	app.Get("/v1/billing/plans", commercemid.RequestContext(), commercebilling.ListPlans)

	// The rest of the console's billing READS, served co-resident for the SAME reason
	// plans is: commerce's api.Route() billing bundle is never compiled here, so without
	// these registrations every one of them falls through to the account bridge's
	// /v1/billing/* wildcard, which forwards to COMMERCE_URL — the public api.hanzo.ai
	// edge, which is THIS binary — and self-dispatches into a 502 loop. In prod there is
	// no separate commerce backend to point COMMERCE_URL at (the in-cluster `commerce`
	// Service selects the cloud pods), so co-residence is the only way to break the loop.
	//
	// Chain: RequestContext (gated context) → IAMTokenRequired (resolves the org from the
	// gateway-validated X-Org-Id into Locals("organization"), the namespace commerce's
	// GetOrganization reads) → PinBillingSubject (pins the caller's OWN billing subject
	// into the query — the SAME isolation the bridge applies, so a read can never widen
	// past the caller; fail-closed for an unvalidated caller) → the commerce handler. The
	// specific paths shadow the bridge wildcard (order 100 < 122). payment-config is
	// org-scoped, not subject-scoped, but PinBillingSubject is still the auth gate that
	// keeps an unvalidated caller from reaching GetOrganization; its pinned (unused)
	// subject params are ignored by that handler.
	billingRead := []struct {
		path string
		h    zip.Handler
	}{
		{"/v1/billing/invoices", commercebilling.ListInvoices},
		{"/v1/billing/invoices/:id/pdf", commercebilling.DownloadInvoicePDF},
		{"/v1/billing/subscriptions", commercebilling.ListBillingSubscriptions},
		{"/v1/billing/spend-alerts", commercebilling.ListSpendAlerts},
		{"/v1/billing/payouts", commercebilling.ListPayouts},
		{"/v1/billing/payment-config", commercebilling.GetPaymentConfig},
	}
	for _, r := range billingRead {
		app.Get(r.path,
			commercemid.RequestContext(),
			iammiddleware.IAMTokenRequired(),
			accountclient.PinBillingSubject(),
			r.h,
		)
	}

	// GET /v1/billing/spend-alerts/authorize — the per-request per-scope spend-CAP
	// VERDICT the request-edge metering gate consumes (clients/metering scopeAuthorize,
	// pathLimitsAuthorize). It is a SERVICE-token S2S read (COMMERCE_SERVICE_TOKEN +
	// X-Org-Id), NOT a browser/IAM read — so it needs its OWN registration, distinct from
	// the console billingRead block above: without a co-resident handler this authorize
	// fell through to the account bridge's /v1/billing/* wildcard (order 122), which — being
	// service-token-forwardable (billing.go billingForwardable) — re-forwarded it to
	// COMMERCE_URL (the public api.hanzo.ai edge = THIS binary) over commerceinproc's
	// self-routing transport, re-entering the same wildcard until the depth-8 guard refused
	// → 502 → the gate fails OPEN (the cap is a policy overlay, so no traffic was blocked,
	// but ~135 502s/30m spammed the money path and each burned 8 full-app dispatches). The
	// plain GET /v1/billing/spend-alerts (registered above) already broke this loop for the
	// CRUD read; this closes the /authorize sibling the metering gate hits on every call.
	//
	// Chain mirrors commerce's OWN gate on this route (api/billing/handlers.go: the `billing`
	// group's userRequired = TokenRequired) plus the RequestContext the standalone supplies
	// globally — NOT the IAM/PinBillingSubject console chain: a raw service token is not an
	// IAM JWT, so IAMTokenRequired would leave GetOrganization unset and AuthorizeSpendCap
	// would 500. TokenRequired authenticates the service token AND resolves the tenant from
	// the gateway-pinned X-Org-Id into Locals("organization"), which AuthorizeSpendCap reads.
	// The specific route shadows the bridge wildcard (order 100 < 122). No PlatformOnly:
	// authorize is a per-org cap read, not a cross-org mint (unlike auto-recharge/run-all).
	app.Get("/v1/billing/spend-alerts/authorize",
		commercemid.RequestContext(),
		commercemid.TokenRequired(),
		commercebilling.AuthorizeSpendCap,
	)

	// In-process seams:
	//   - commerceinproc routes the S2S billing byte-stream into the co-resident
	//     app (the metering debit path) instead of a socket to a standalone pod.
	//   - commerceclient reads the Embedded's datastore DIRECTLY (entitlements +
	//     BalanceCents) — no HTTP shape at all.
	commerceinproc.SetApp(app)
	commerceclient.PublishEmbedded(embedded)

	// Usage-cap enforcement on the FINANCE path. The unified binary records usage in
	// the finance ledger (fin.RecordUsage), NOT commerce's transaction store — which
	// it leaves empty — so the cap must read spend from, and fire alerts on, the
	// finance ledger. Two seams, both org-wide (the finance Entry carries no scope;
	// per-scope caps are a follow-up):
	//   - SetPeriodSpendReader: AuthorizeSpendCap's scopeSpentCents reads the org's
	//     finance period spend instead of the empty commerce transaction ledger, so
	//     a real LLM request increments the cap's `spent` and trips the 402.
	//   - SetUsageHook: after each finance debit, fire the org's spend-alerts on the
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

// mountCommerceFailClosed serves an honest JSON 503 on every commerce prefix when
// the embed cannot boot, so /v1/commerce/* answers "commerce unavailable" instead
// commerceErrorScope confines commerce's JSON error envelope to commerce's OWN
// routes. commercemid.ErrorHandlerJSON is a `/v1` GROUP middleware, and fiber
// matches group middleware by PREFIX, not by the handle a route registered on —
// so on the shared `/v1` it wraps every subsystem mounted AFTER commerce and
// flattens their typed zip.HTTPError (403/400/…) into a blanket 500 (the store
// envelope always renders 500). Guarded by commercePrefixes, the envelope stays on
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
// or a child of one), the SAME ownership commercePrefixes encodes for the
// fail-closed mount.
func hasCommercePrefix(path string) bool {
	for _, p := range commercePrefixes {
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
	for _, p := range commercePrefixes {
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

// fireCapAlert fires the org's spend-alerts after a finance usage debit — the alert
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
