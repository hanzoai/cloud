// Copyright © 2026 Hanzo AI. MIT License.

//go:build cloud
// +build cloud

// Mount implements the HIP-0106 unified Hanzo Cloud binary contract.
//
// Commerce is a LIGHT ROUTER, NOT in PCI-DSS scope. It handles tokens
// and intent IDs only; it MUST NEVER touch a PAN. Anything that would
// touch a PAN MUST be refactored to call deps.Payments / deps.Vault
// (ZAP-RPC clients that resolve to the out-of-process Payments + Vault
// services).
//
// The legacy gin engine inside *commerceApp.App owns the full
// /v1/commerce, /_/commerce and /admin route surface — Mount adapts it
// onto zip via zip.AdaptNetHTTP so cmd/cloud can serve commerce inside
// the unified binary without reimplementing every handler.

package commerce

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	api "github.com/hanzoai/cloud/clients/commerce/api/api"
	"github.com/zap-proto/zip"
)

// Mount registers commerce on app per the HIP-0106 contract.
//
// Per HIP-0106 — commerce is a LIGHT ROUTER, NOT in PCI-DSS scope.
// Token handling only. ANY code path here that would touch a PAN MUST
// be refactored to call deps.Payments / deps.Vault instead.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("commerce.Mount: nil zip.App")
	}

	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("commerce.Mount: nil deps.Logger")
	}

	// PCI scope guard. Commerce is mounted because the deployment wants
	// the checkout router; if the out-of-process Payments / Vault
	// clients aren't wired the deployment can still serve tenant config
	// + admin endpoints, but every payment-path handler will fail closed
	// when it reaches for deps.Payments. Warn loudly at startup so the
	// operator sees the gap before a customer does.
	if deps.Payments == nil {
		logger.Warn("commerce.Mount: deps.Payments is nil — payment intent paths will fail; tenant config + admin still served")
	}
	if deps.Vault == nil {
		logger.Warn("commerce.Mount: deps.Vault is nil — vault charge paths unavailable; tenant config + admin still served")
	}

	// Bring up the legacy commerce app (gin engine + DB + hooks + KMS).
	// The DataDir flows from cloud.Deps so every per-tenant SQLite lands
	// under one tree per HIP-0302.
	dataDir := deps.DataDir
	if dataDir == "" {
		dataDir = "/var/lib/cloud/commerce"
	}

	embedded, err := Embed(context.Background(), EmbedConfig{
		DataDir: dataDir,
		// HTTPAddr is intentionally unset: the cloud binary owns the
		// listener; commerce only contributes its http.Handler.
		HTTPAddr: "",
		// RequireIdentity stays env-driven; the gateway in front of the
		// cloud binary is the trust boundary per HIP-0026.
		RequireIdentity: false,
	})
	if err != nil {
		return fmt.Errorf("commerce.Mount: embed: %w", err)
	}

	handler := embedded.HTTPHandler()
	if handler == nil {
		return fmt.Errorf("commerce.Mount: nil HTTPHandler")
	}

	// Wire the full Commerce API surface (/v1/billing, /v1/checkout,
	// /v1/subscription, /v1/store, /v1/account, …) directly on the live
	// gin engine. Embed() ran Bootstrap() which fired setupRoutes(), but
	// setupRoutes() registers only the per-tenant checkout group + admin
	// SPA — not the legacy api.Route() bundle. The same imperative call
	// the legacy commerce/commerced binaries make is mirrored here so the
	// cloud-mounted surface and the standalone surface expose identical
	// routes. See cmd/commerce/main.go for the standalone-binary call.
	apiGroup := embedded.App().Router.Group("/v1")
	api.Route(apiGroup)

	// Native zip health endpoint — independent of the gin handler so
	// liveness/readiness probes survive a router-wide outage. Use a
	// commerce-scoped path so probes can target this subsystem specifically.
	app.Get("/_/commerce/healthz", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{
			"status":  "ok",
			"service": "commerce",
		})
	})

	// Public + admin surface — adapted via zip.AdaptNetHTTP. The gin
	// engine retains its full middleware chain (RequestContext, IAM,
	// AccessControl, CachePrivate, OnRouteSetup hooks). Cost is ~5%
	// per request vs native fiber — acceptable for the migration.
	app.Mount("/v1/commerce", handler)
	app.Mount("/_/commerce", handler)

	// The embedded admin Next.js bundle still lives at /admin behind
	// the same gin engine; cloud may not be the right surface for /admin
	// (consoles run at console.hanzo.ai), so we adapt it only when
	// explicitly enabled via env. The legacy commerced binary still
	// serves /admin standalone.
	// Intentionally NOT mounting /admin here — that is console territory.

	logger.Info("commerce mounted",
		"prefix.public", "/v1/commerce",
		"prefix.admin", "/_/commerce",
		"data_dir", dataDir,
		"brand", deps.Brand,
	)
	return nil
}

func init() {
	cloud.Register("commerce", 100, func(app any, deps cloud.Deps) error {
		zapp, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("commerce.Mount: expected *zip.App, got %T", app)
		}
		return Mount(zapp, deps)
	})
}
