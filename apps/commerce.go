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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerceclient"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/commerce"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercestore "github.com/hanzoai/commerce/api/store"
	commercemid "github.com/hanzoai/commerce/middleware"
	"github.com/hanzoai/commerce/middleware/iammiddleware"
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
		// co-resident, so we inject the ledgercore-backed adapter.
		Ledger: ledgercoreCredit{},
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
	storeV1.Use(commercemid.AddHost(), commercemid.RequestContext(), commercemid.ErrorHandlerJSON())
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

	// In-process seams:
	//   - commerceinproc routes the S2S billing byte-stream into the co-resident
	//     app (the metering debit path) instead of a socket to a standalone pod.
	//   - commerceclient reads the Embedded's datastore DIRECTLY (entitlements +
	//     BalanceCents) — no HTTP shape at all.
	commerceinproc.SetApp(app)
	commerceclient.PublishEmbedded(embedded)

	lg.Info("commerce embedded natively (hanzoai/commerce module on the shared zip app)",
		"data_dir", dataDir,
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// mountCommerceFailClosed serves an honest JSON 503 on every commerce prefix when
// the embed cannot boot, so /v1/commerce/* answers "commerce unavailable" instead
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
