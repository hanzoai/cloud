// Copyright © 2026 Hanzo AI. MIT License.

// commerce.go mounts the hanzoai/commerce MODULE into the unified cloud binary
// (HIP-0106) — the un-fork of the old inlined clients/commerce tree. The module is
// a pure library (zero cloud imports); THIS adapter is the one place its embedded
// gin app is narrowed from cloud.Deps and wired to cloud's in-process seams
// (commerceinproc.SetHandler for S2S dispatch, PublishEmbedded for the
// entitlement client). Direction is one-way: cloud → commerce, no cycle.
//
// WRAP, DON'T REWRITE. commerce.Embed runs the ENTIRE gin runtime (DB, per-org
// SQLite, KMS, hooks, cron) in-process, binds NO listener, and returns an
// http.Handler; the handler is attached verbatim at every prefix commerce owns.
//
// PCI SCOPE. Commerce is a LIGHT ROUTER, NOT in PCI-DSS scope: tokens + intent IDs
// only, NEVER a PAN. PAN-touching paths call the out-of-process Payments / Vault
// (ZAP-RPC); when those clients are absent the payment handlers fail closed while
// tenant config + admin stay served — mountCommerce warns loudly at startup.
//
// FAIL-SOFT. A broken Embed does NOT crash the binary: commerce degrades to a 503
// on its own prefixes while every co-resident subsystem stays up.
package subsystems

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerceclient"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/commerce"
	"github.com/hanzoai/commerce/api"
	log "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func init() {
	// In-process CommerceClient factory — pickCommerceClient calls it when the
	// commerce subsystem is enabled. Registered HERE (not called directly from
	// package cloud) because commerceinproc's entitlement client imports
	// clients/plan, which imports cloud: the hook keeps the package graph acyclic.
	cloud.RegisterCommerceClientFactory(func(cfg *cloud.Config, _ log.Logger) cloud.CommerceClient {
		return commerceclient.InProcessClient(cfg.Brand)
	})
}

// commercePrefixes is every root path the commerce gin handler owns. The whole
// handler is mounted at each via app.All(prefix+"/*", zip.AdaptNetHTTP(h)), which
// preserves the full request path — gin routes on it. An unknown path under a
// prefix 404s from gin exactly as standalone commerce does, so a broad mount cannot
// leak another subsystem's surface.
var commercePrefixes = []string{
	"/v1/commerce", // public checkout + tenant + catalog + deposits + the api.Route bundle
	"/_/commerce",  // tenant-admin surface
	// Payment-provider webhook receiver (POST /v1/billing/webhooks/:provider —
	// Square et al). The provider's HMAC over the registered notification URL +
	// body IS the auth; a bearer gate is impossible for provider callbacks. Only
	// this route family lives under the prefix, and commerce mounts BEFORE the
	// account-bridge /v1/billing/* catch-all (Wire order 100 < bridge), so the
	// webhook path reaches gin while every other /v1/billing/* route keeps its
	// existing owner.
	"/v1/billing/webhooks",
}

// mountCommerce adapts cloud.Deps to the commerce module: boots the embedded gin
// app, attaches it at the commerce prefixes, and publishes the two in-process
// seams. It carries the PCI scope-guard warnings (Payments / Vault presence) that
// belong with Deps, keeping the module itself off the wide dependency surface.
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

	// Native zip health endpoint — independent of the gin handler so liveness/
	// readiness probes survive a router-wide outage. Registered FIRST so it answers
	// even when the embed fails below and the prefixes serve 503.
	app.Get("/_/commerce/healthz", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "service": "commerce"})
	})

	// commerce persists its per-org SQLite + `base` tree under <DataDir>/commerce,
	// NEVER at DataDir directly: cloud already owns DataDir/orgs (its own per-org
	// subsystem SQLite) and DataDir/base (its Base/IAM store), and commerce also
	// writes orgs/ + base/, so sharing the root would collide two apps on the same
	// SQLite files and corrupt them.
	dataDir := "/var/lib/cloud/commerce"
	if deps.DataDir != "" {
		dataDir = filepath.Join(deps.DataDir, "commerce")
	}

	embedded, err := commerce.Embed(context.Background(), commerce.EmbedConfig{
		DataDir: dataDir,
		// RequireIdentity stays gateway-owned: the gateway in front of the cloud
		// binary is the trust boundary per HIP-0026.
		RequireIdentity: false,
	})
	if err != nil {
		lg.Error("commerce embed failed — serving fail-closed 503 (cloud stays up)", "err", err)
		mountCommerceFailClosed(app)
		return nil
	}

	handler := embedded.HTTPHandler()
	if handler == nil {
		lg.Error("commerce produced a nil http handler — serving fail-closed 503")
		mountCommerceFailClosed(app)
		return nil
	}

	// Wire the full commerce API surface (/v1/billing, /v1/checkout, /v1/subscription,
	// /v1/store, /v1/account, …) on the live gin engine — mirror the standalone
	// commerced call so the cloud-mounted surface and the standalone expose
	// identical routes.
	api.Route(embedded.App().Router.Group("/v1"))

	// Publish the two in-process seams: the S2S http handler (every co-resident
	// billing proxy dispatches straight into this gin engine) and the Embedded
	// (the entitlement client reads its datastore directly).
	commerceinproc.SetHandler(handler)
	commerceclient.PublishEmbedded(embedded)

	for _, p := range commercePrefixes {
		app.All(p+"/*", zip.AdaptNetHTTP(handler))
	}

	lg.Info("commerce embedded in-process (hanzoai/commerce module)",
		"prefixes", commercePrefixes,
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
	failed := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"commerce unavailable","code":503}`))
	}))
	for _, p := range commercePrefixes {
		app.All(p+"/*", failed)
	}
}
