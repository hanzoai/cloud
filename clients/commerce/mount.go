// Copyright © 2026 Hanzo AI. MIT License.

// mount.go folds the embedded commerce app into the unified Hanzo Cloud binary
// (HIP-0106): cloud serves the /v1/commerce/* + /_/commerce/* checkout / tenant /
// billing surface ITSELF instead of proxying a remote commerce pod. It absorbs the
// former thin -svc mount wrapper — ONE package now owns the commerce library,
// its in-process cloud.CommerceClient (client.go), AND this cloud-subsystem
// registration, exactly as clients/kms owns the KMS library + its /v1/kms subsystem
// (the "drop the -svc wrapper" pattern, cf. #241 gatewaysvc → gateway). cloud never
// imports this package back: init() registers the mount + the client factory via
// cloud's inversion hooks (cloud.Register / cloud.RegisterCommerceClientFactory), so
// there is no cloud⇄commerce import cycle.
//
// WRAP, DON'T REWRITE. commerce.Embed runs the ENTIRE gin runtime (DB, per-org
// SQLite, KMS, hooks, cron) in-process, binds NO listener, and returns an
// http.Handler; Mount attaches THAT handler verbatim at every prefix commerce owns,
// so commerce's behaviour is preserved byte-for-byte.
//
// PCI SCOPE. Commerce is a LIGHT ROUTER, NOT in PCI-DSS scope: tokens + intent IDs
// only, NEVER a PAN. PAN-touching paths call the out-of-process Payments / Vault
// (ZAP-RPC); when those clients are absent the payment handlers fail closed while
// tenant config + admin stay served — mountFromDeps warns loudly at startup.
//
// FAIL-SOFT. A broken Embed does NOT crash the binary: commerce degrades to a 503
// on its own prefixes (mountFailClosed) while every co-resident subsystem stays up
// — the blast-radius isolation the consolidation exists for.
//
// ACTIVATION is the enable-list gate: the operator adds "commerce" to --enable only
// when the cloud pod should host commerce; until then commerce is served by the
// standalone pod and cloud reaches it over the CLOUD_COMMERCE_HTTP_URL /
// CLOUD_COMMERCE_ZAP_ADDR seam (pickCommerceClient — the network path is preserved;
// this fold does NOT force the cutover).
package commerce

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/hanzoai/cloud"
	api "github.com/hanzoai/cloud/clients/commerce/api/api"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// MountConfig is the decomplected commerce-relevant slice of cloud.Deps — the
// VALUES Mount uses (brand/env/data-dir/domain), grouped, instead of the whole Deps
// bag. Accept a value, return the concrete *Embedded.
type MountConfig struct {
	Brand   string
	Env     string
	DataDir string
	Domain  string
}

// commercePrefixes is every root path the commerce gin handler owns. The whole
// handler is mounted at each via zip.App.Mount (which registers prefix+"/*" and
// preserves the full request path — gin routes on it). An unknown path under a
// prefix 404s from gin exactly as standalone commerce does, so a broad mount cannot
// leak another subsystem's surface.
var commercePrefixes = []string{
	"/v1/commerce", // public checkout + tenant + catalog + deposits + the api.Route bundle
	"/_/commerce",  // tenant-admin surface
}

// Mount boots the in-process commerce app and attaches its http.Handler to app at
// every commerce prefix. It also publishes the handler for in-process S2S billing
// dispatch (commerceinproc) and the Embedded as the source for the in-process
// entitlement client (client.go). It returns the live *Embedded (nil on a fail-soft
// degrade). Called once when "commerce" is enabled.
func Mount(app *zip.App, cfg MountConfig, log luxlog.Logger) (*Embedded, error) {
	if app == nil {
		return nil, fmt.Errorf("commerce.Mount: nil zip.App")
	}
	if log == nil {
		return nil, fmt.Errorf("commerce.Mount: nil logger")
	}
	log = log.New("subsystem", "commerce")

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
	// SQLite files and corrupt them. The dedicated subdir keeps commerce's ledgers
	// physically separate on the same PVC.
	dataDir := "/var/lib/cloud/commerce"
	if cfg.DataDir != "" {
		dataDir = filepath.Join(cfg.DataDir, "commerce")
	}

	embedded, err := Embed(context.Background(), EmbedConfig{
		DataDir: dataDir,
		// RequireIdentity stays gateway-owned: the gateway in front of the cloud
		// binary is the trust boundary per HIP-0026.
		RequireIdentity: false,
	})
	if err != nil {
		log.Error("commerce embed failed — serving fail-closed 503 (cloud stays up; standalone commerce pod unaffected)", "err", err)
		mountFailClosed(app)
		return nil, nil
	}
	embedded.brand = cfg.Brand

	handler := embedded.HTTPHandler()
	if handler == nil {
		log.Error("commerce produced a nil http handler — serving fail-closed 503")
		mountFailClosed(app)
		return nil, nil
	}

	// Wire the full commerce API surface (/v1/billing, /v1/checkout, /v1/subscription,
	// /v1/store, /v1/account, …) on the live gin engine. Embed ran Bootstrap →
	// setupRoutes, which registers only the per-tenant checkout group + admin SPA —
	// not the imperative api.Route bundle; mirror the standalone commerced call so the
	// cloud-mounted surface and the standalone surface expose identical routes.
	apiGroup := embedded.App().Router.Group("/v1")
	api.Route(apiGroup)

	// Publish for the two in-process seams: the S2S http handler (every co-resident
	// billing proxy dispatches straight into this gin engine — no socket to
	// commerce.hanzo.svc:8001) and the Embedded (the entitlement client reads its
	// datastore directly). Behaviour-preserving: same request, headers, body/status.
	commerceinproc.SetHandler(handler)
	PublishEmbedded(embedded)

	for _, p := range commercePrefixes {
		app.Mount(p, handler)
	}

	log.Info("commerce embedded in-process (gin handler mounted)",
		"prefixes", commercePrefixes,
		"data_dir", dataDir,
		"version", Version,
		"brand", cfg.Brand,
		"env", cfg.Env,
	)
	return embedded, nil
}

// mountFailClosed serves an honest JSON 503 on every commerce prefix when the embed
// cannot boot, so /v1/commerce/* answers "commerce unavailable" instead of falling
// through to another subsystem's catch-all. cloud and every other subsystem stay up.
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

// mountFromDeps adapts the registry's cloud.Deps to Mount's decomplected MountConfig
// — the ONE place the whole-Deps bag is narrowed to the values commerce uses. It
// also carries the PCI scope-guard warnings (Payments / Vault presence) that belong
// with Deps, keeping Mount itself off the wide dependency surface.
func MountFromDeps(app *zip.App, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("commerce.Mount: nil deps.Logger")
	}
	if deps.Payments == nil {
		deps.Logger.Warn("commerce: deps.Payments is nil — payment intent paths will fail; tenant config + admin still served")
	}
	if deps.Vault == nil {
		deps.Logger.Warn("commerce: deps.Vault is nil — vault charge paths unavailable; tenant config + admin still served")
	}
	_, err := Mount(app, MountConfig{
		Brand:   deps.Brand,
		Env:     deps.Env,
		DataDir: deps.DataDir,
		Domain:  deps.Domain,
	}, deps.Logger)
	return err
}

func init() {
	// Register the mount at order 100 (after kms=10 / iam=50 / base=60 and the
	// billing/licensing tier) and the in-process client factory pickCommerceClient
	// calls. cloud never imports this package: both hooks are the same inversion
	// clients/kms uses, so the commerce library + its subsystem live in ONE package
	// with no cloud⇄commerce import cycle. Exactly one registration each.
	cloud.RegisterCommerceClientFactory(func(cfg *cloud.Config, _ luxlog.Logger) cloud.CommerceClient {
		return InProcessClient(cfg.Brand)
	})
}
