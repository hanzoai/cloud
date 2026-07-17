// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// Package base embeds the Hanzo Base app engine in-process in the unified cloud
// binary (the HIP-0106 base fold) — the in-binary replacement for the standalone
// `ghcr.io/hanzoai/superbase` pod, whose whole job was `base.New()` + serve.
// cloud already links github.com/hanzoai/base, so it runs the SAME engine
// in-process across TWO orthogonal lanes:
//
//	LANE 1 — the viral waitlist (GTM launch surface). ONE platform Base app
//	carries the waitlist plugin; its /v1/waitlist/* routes are PUBLIC (a signup
//	surface has no principal to scope by) and platform-owned (one waitlist per
//	brand, not customer data). console reads it via WAITLIST_URL=…/v1/waitlist.
//
//	LANE 2 — managed Base hosting (what superbase/PocketHost provided). ONE Base
//	app PER ORG, opened lazily and pooled, each on its OWN SQLite under
//	{DataDir}/base/{orgSegment}/ — the same "prod = SQLite per tenant" model
//	(HIP-0302) the NewBase leaves (captable/sign/dataroom) use, so an org's
//	collections/records are PHYSICALLY isolated. Served AUTHENTICATED under
//	/v1/base/*, the org resolved from the VALIDATED cloud principal (never a
//	client header). This is the console Bases manager's backend.
//
// The two lanes are deliberately NOT one app: the waitlist is a public, single,
// brand-level instance; hosted Bases are private, per-org, and many.
//
// MOUNT PREFIX. Base's REST router honours BASE_API_PREFIX (default /v1); this
// package pins it to /v1/base so the per-org engine serves its collections API
// natively at /v1/base/collections/… (self-generated URLs included) and never
// collides with cloud's other /v1 routes. The waitlist plugin binds a FIXED
// /v1/waitlist regardless of the prefix, so the two lanes never overlap.
//
// IAM-NATIVE, ONE AUTH SOURCE. Each per-org app validates bearer tokens against
// Hanzo IAM's JWKS ({IAMIssuer}/v1/iam/.well-known/jwks) as its EXCLUSIVE auth
// source (apis.StoreKey{JWKSURL,ExternalAuthOnly}) — the same IAM the cloud edge
// validates for org routing. The edge selects the org; Base authorises the
// record; both consume ONE IAM. No second auth path is introduced.
//
// FAIL-CLOSED + STAGED. The embed activates only when CLOUD_BASE_EMBED is
// truthy. Absent it, Mount is a no-op except the always-on GET /v1/base/health
// liveness route, so linking this subsystem into every cloud variant changes
// nothing until a single-writer deployment opts in. Activation is one CR env.
//
// ONE WRITER, DURABLE. Every embedded store is single-open + single-writer: the
// base deployment is single-replica, strategy Recreate, on the RWO cloud-api-data
// PVC, so each per-org SQLite is durable across restarts — the property the
// standalone base pod had with its own PVC.
package base

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	baseapp "github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/plugins/waitlist"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// embedEnv gates activation. Truthy (1/true/yes) embeds the base app; anything
// else (incl. unset) leaves the subsystem a health-only no-op.
const embedEnv = "CLOUD_BASE_EMBED"

// apiPrefix is the ONE mount prefix for the per-org Base engine. Base reads it
// from BASE_API_PREFIX at router build, so setting it here makes every per-org
// app serve its collections/records API natively under /v1/base/*.
const apiPrefix = "/v1/base"

// platformSeg is the platform waitlist app's data subdir. The leading '_' is
// OUTSIDE the base32 [a-z2-7] TenantSegment alphabet, so it can never collide
// with any org's per-org directory.
const platformSeg = "_platform"

func embedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(embedEnv))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// subsystem is the mounted state, retained package-globally so Shutdown can
// release the platform app + every pooled per-org app.
type subsystem struct {
	pool     *pool
	platform *baseapp.Base
}

// mounted is the active subsystem (nil until Mount succeeds with the embed on).
var mounted *subsystem

// Mount wires the base subsystem onto app per HIP-0106: the always-on health
// route, then — behind CLOUD_BASE_EMBED — the public waitlist lane and the
// authenticated per-org hosting lane.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("base.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("base.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "base")

	// Native /v1/base/health — always answers (OwnsHealth), no auth, BEFORE the
	// embed gate AND before the /v1/base/* wildcard, so the liveness probe never
	// depends on CLOUD_BASE_EMBED and the wildcard never shadows it (same pattern
	// as clients/plan + clients/pricing).
	app.Get("/v1/base/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"service": "base", "status": "ok"})
	})

	if !embedEnabled() {
		log.Info("base embed disabled; /v1/base + /v1/waitlist off (set CLOUD_BASE_EMBED=1 to enable)")
		return nil
	}
	if deps.DataDir == "" {
		return fmt.Errorf("base.Mount: empty DataDir")
	}

	// Pin Base's router prefix to /v1/base (its documented multi-app knob) unless
	// the operator already set one. Read once, applied to EVERY app built below.
	if strings.TrimRight(os.Getenv("BASE_API_PREFIX"), "/") == "" {
		if err := os.Setenv("BASE_API_PREFIX", apiPrefix); err != nil {
			return fmt.Errorf("base.Mount: set BASE_API_PREFIX: %w", err)
		}
	}

	// The cloud binary owns the ZAP transport; embedded Base apps must NOT each
	// start their own node (they'd all contend for the same :9999 bind). Disable
	// it for every app built below unless the operator explicitly set the knob.
	if _, set := os.LookupEnv("ZAP_DISABLED"); !set {
		if err := os.Setenv("ZAP_DISABLED", "true"); err != nil {
			return fmt.Errorf("base.Mount: set ZAP_DISABLED: %w", err)
		}
	}

	root := filepath.Join(deps.DataDir, "base")

	// LANE 1 — platform waitlist app (public /v1/waitlist/*).
	platformApp, waitlistMux, err := newPlatformApp(filepath.Join(root, platformSeg))
	if err != nil {
		return fmt.Errorf("base.Mount: platform waitlist app: %w", err)
	}
	wh := zip.AdaptNetHTTP(waitlistMux)
	app.All("/v1/waitlist", wh)
	app.All("/v1/waitlist/*", wh)

	// LANE 2 — per-org Base hosting (authenticated /v1/base/*).
	p := newPool(root, deps)
	app.All("/v1/base/*", func(c *zip.Ctx) error { return serveOrg(p, log, c) })

	mounted = &subsystem{pool: p, platform: platformApp}
	log.Info("base app embedded",
		"waitlist", "/v1/waitlist/*", "hosting", "/v1/base/*",
		"prefix", os.Getenv("BASE_API_PREFIX"), "brand", deps.Brand, "env", deps.Env)
	return nil
}

// serveOrg resolves the caller's org from the validated principal, acquires that
// org's pooled Base app (pinned for the request so eviction can't close it
// mid-flight), and serves the request through the org's own Base mux.
func serveOrg(p *pool, log interface{ Error(string, ...any) }, c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	h, release, err := p.acquire(org)
	if err != nil {
		log.Error("base: open org app failed", "err", err)
		return zip.Errorf(http.StatusInternalServerError, "base unavailable")
	}
	defer release()
	return zip.AdaptNetHTTP(h)(c)
}

// newPlatformApp builds the single platform Base app carrying the waitlist
// plugin and returns its handler (the FULL base mux; only /v1/waitlist/* is
// mounted from it). Every waitlist knob resolves from the environment at boot
// (see waitlist Config.resolve): TURNSTILE_SECRET_KEY, WAITLIST_ADMIN_SECRET,
// WAITLIST_AWARD_SECRET, WAITLIST_DEFAULT_SLUGS, WAITLIST_ACCESS_CAPACITY,
// WAITLIST_OPEN — secrets injected from KMS, never in code.
func newPlatformApp(dir string) (*baseapp.Base, http.Handler, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	bapp := baseapp.NewWithConfig(baseapp.Config{DefaultDataDir: dir, HideStartBanner: true})
	waitlist.MustRegister(bapp, waitlist.Config{Enabled: true})
	if err := bapp.Bootstrap(); err != nil {
		return nil, nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := bapp.RunAllMigrations(); err != nil {
		_ = bapp.ResetBootstrapState()
		return nil, nil, fmt.Errorf("migrations: %w", err)
	}
	h, err := buildMux(bapp)
	if err != nil {
		_ = bapp.ResetBootstrapState()
		return nil, nil, fmt.Errorf("build mux: %w", err)
	}
	return bapp, h, nil
}

// buildMux reproduces apis.Serve's handler construction without binding a
// listener: build the base router, fire the OnServe hook so plugins register
// their routes on it, then compile the mux. The SAME code path apis.Serve runs
// inside its OnServe trigger, just without the net.Listen.
func buildMux(app core.App) (http.Handler, error) {
	router, err := apis.NewRouter(app)
	if err != nil {
		return nil, err
	}

	var (
		once    sync.Once
		handler http.Handler
	)
	serveEvent := new(core.ServeEvent)
	serveEvent.App = app
	serveEvent.Router = router
	serveEvent.Server = &http.Server{}

	triggerErr := app.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error {
		mux, err := e.Router.BuildMux()
		if err != nil {
			return err
		}
		once.Do(func() { handler = mux })
		return nil
	})
	if triggerErr != nil {
		return nil, triggerErr
	}
	if handler == nil {
		return nil, fmt.Errorf("nil handler after OnServe")
	}
	return handler, nil
}

// Shutdown releases the platform app + every pooled per-org app. Idempotent.
func Shutdown(context.Context) error {
	s := mounted
	if s == nil {
		return nil
	}
	mounted = nil
	var firstErr error
	if s.pool != nil {
		if err := s.pool.closeAll(); err != nil {
			firstErr = err
		}
	}
	if s.platform != nil {
		if err := s.platform.ResetBootstrapState(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
