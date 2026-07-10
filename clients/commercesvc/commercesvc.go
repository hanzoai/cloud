// Package commercesvc folds hanzoai/commerce into the unified hanzoai/cloud
// binary as an in-process subsystem (HIP-0106) — task #89 phase 1. Cloud serves
// the commerce surface (/v1/commerce/*, /_/commerce/*) ITSELF instead of proxying
// to a remote commerce pod.
//
// WRAP, DON'T REWRITE. Commerce is a gin app whose full route surface is built by
// commerce.Embed → App.Bootstrap → setupRoutes (the public /v1/commerce checkout +
// tenant surface) plus the imperative api.Route(/v1) bundle. commerce.Embed runs
// that ENTIRE runtime (DB, per-org SQLite, KMS, hooks, cron) in-process, binds NO
// listener, and returns an http.Handler; this subsystem mounts THAT handler verbatim
// on cloud's shared zip.App at every prefix commerce owns. No handler is
// reimplemented — the same gin engine answers, so commerce's behaviour is preserved
// byte-for-byte. This is exactly how clients/iam wraps IAM's Beego handler and
// clients/kms re-exposes the embedded KMS store.
//
// The package lives at clients/commercesvc (NOT clients/commerce) so its name does
// not collide with the upstream github.com/hanzoai/cloud/clients/commerce library it embeds —
// mirroring the clients/kmssvc-vs-clients/kms convention.
//
// PCI SCOPE. Commerce is a LIGHT ROUTER, NOT in PCI-DSS scope: it handles tokens +
// intent IDs only and MUST NEVER touch a PAN. Any PAN-touching path is refactored to
// call deps.Payments / deps.Vault (ZAP-RPC to the out-of-process PCI-CDE services);
// when those clients are nil the payment-path handlers fail closed while tenant
// config + admin stay served. This subsystem warns loudly at mount when they are
// absent so the operator sees the gap before a customer does.
//
// FAIL-CLOSED, NOT FAIL-LOUD. A broken/misconfigured commerce does NOT crash the
// consolidated binary: an Embed failure degrades THIS subsystem to a 503 fail-closed
// on every commerce prefix (mountFailClosed) while every co-resident subsystem stays
// up — the blast-radius isolation the whole consolidation exists for, mirroring the
// KMS "no master key → health-only" pattern.
//
// ACTIVATION is the standard enable-list gate: the operator adds "commerce" to the
// deployment's --enable only when the cloud pod should host commerce; until then
// commerce is served by the standalone commerce pod via ingress and cloud reaches it
// over the CLOUD_COMMERCE_HTTP_URL / CLOUD_COMMERCE_ZAP_ADDR seam (unchanged).
package commercesvc

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/hanzoai/cloud"
	commerce "github.com/hanzoai/cloud/clients/commerce"
	api "github.com/hanzoai/cloud/clients/commerce/api/api"
	"github.com/zap-proto/zip"
)

// commercePrefixes is every root path prefix the commerce gin handler owns. The
// whole handler is mounted at each via zip.App.Mount (which registers prefix+"/*"
// and preserves the full request path). The handler answers only paths commerce
// registered; an unknown path under a prefix 404s from gin exactly as standalone
// commerce does, so a broad mount cannot leak another subsystem's surface.
var commercePrefixes = []string{
	"/v1/commerce", // public checkout + tenant + catalog + deposits + the api.Route bundle
	"/_/commerce",  // tenant-admin surface
}

// Mount boots the in-process commerce app and attaches its http.Handler to cloud's
// shared zip.App. Called once by cloud.MountAll when "commerce" is enabled.
//
// commerce keeps process-global gin/DB singletons via commerce.Embed, so the
// bootstrap is inherently once-per-process; MountAll calls each subsystem's Mount
// exactly once, which satisfies that.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("commercesvc.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("commercesvc.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "commerce")

	// Native zip health endpoint — independent of the gin handler so
	// liveness/readiness probes survive a router-wide outage. Registered FIRST so
	// it answers even when the embed fails below and the prefixes serve 503.
	app.Get("/_/commerce/healthz", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "service": "commerce"})
	})

	// PCI scope guard. Commerce is mounted because the deployment wants the checkout
	// router; if the out-of-process Payments / Vault clients aren't wired the
	// deployment can still serve tenant config + admin, but every payment-path
	// handler fails closed when it reaches for deps.Payments. Warn loudly at startup.
	if deps.Payments == nil {
		log.Warn("deps.Payments is nil — payment intent paths will fail; tenant config + admin still served")
	}
	if deps.Vault == nil {
		log.Warn("deps.Vault is nil — vault charge paths unavailable; tenant config + admin still served")
	}

	// commerce persists its per-org SQLite + `base` tree under <DataDir>; every
	// per-tenant DB lands under one tree per HIP-0302. The cloud binary owns the
	// listener, so HTTPAddr is intentionally unset — commerce only contributes its
	// http.Handler.
	//
	// ISOLATION: nest commerce under {deps.DataDir}/commerce, NEVER at deps.DataDir
	// directly. cloud already owns {deps.DataDir}/orgs (its own per-org subsystem
	// SQLite, HIP-0302) and {deps.DataDir}/base (its Base/IAM store) — commerce also
	// writes orgs/ and base/, so sharing the root would collide two apps on the same
	// SQLite files (e.g. base/data.db) and corrupt them. The dedicated subdir keeps
	// commerce's ledgers physically separate on the same cloud-api-data PVC.
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
		log.Error("commerce embed failed — serving fail-closed 503 (cloud stays up; standalone commerce pod unaffected)", "err", err)
		mountFailClosed(app)
		return nil
	}

	handler := embedded.HTTPHandler()
	if handler == nil {
		log.Error("commerce produced a nil http handler — serving fail-closed 503")
		mountFailClosed(app)
		return nil
	}

	// Wire the full commerce API surface (/v1/billing, /v1/checkout, /v1/subscription,
	// /v1/store, /v1/account, …) on the live gin engine. Embed ran Bootstrap →
	// setupRoutes, which registers only the per-tenant checkout group + admin SPA —
	// not the legacy api.Route bundle. The same imperative call the standalone
	// commerce/commerced binaries make is mirrored here so the cloud-mounted surface
	// and the standalone surface expose identical routes. See commerce cmd/commerce.
	apiGroup := embedded.App().Router.Group("/v1")
	api.Route(apiGroup)

	mountHandler(app, handler)

	log.Info("commerce embedded in-process (gin handler mounted)",
		"prefixes", commercePrefixes,
		"data_dir", dataDir,
		"version", commerce.Version,
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// mountHandler attaches the commerce http.Handler at every prefix commerce owns.
// Split from Mount so the routing plumbing — zip.App.Mount dispatching prefix/* to
// the handler with the ORIGINAL request path preserved (gin routes on the full
// path) — is unit-testable without booting the full commerce runtime.
func mountHandler(app *zip.App, handler http.Handler) {
	for _, p := range commercePrefixes {
		app.Mount(p, handler)
	}
}

// mountFailClosed serves an honest JSON 503 on every commerce prefix when the embed
// cannot boot, so /v1/commerce/* answers "commerce unavailable" instead of falling
// through to another subsystem's catch-all. cloud and every other subsystem stay up
// — the fold's blast-radius isolation. During staged rollout commerce is still
// served by the standalone commerce pod via ingress, so clients never see this path
// until cutover.
func mountFailClosed(app *zip.App) {
	failed := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"commerce unavailable","code":503}`))
	}))
	for _, p := range commercePrefixes {
		app.All(p+"/*", failed)
	}
}

// init registers commerce with cloud's subsystem registry at order 100 — after its
// dependencies (kms=10, iam=50, base=60) and the billing/licensing tier — matching
// the slot the upstream commerce.Mount used. cloud never imports this package; the
// blank import in subsystems/subsystems.go pulls it into the build graph so this
// init runs, exactly as every other subsystem is wired (no cloud⇄commerce cycle:
// the upstream commerce.Mount that imports cloud stays behind //go:build cloud and
// is never compiled into cloud's plain build).
func init() {
	cloud.Register("commerce", 100, cloud.Typed(Mount))
}
