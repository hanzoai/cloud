// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// Package base embeds the Hanzo Base app engine in-process in the unified
// cloud binary (the HIP-0106 base fold) and mounts the viral waitlist plugin
// on the shared zip.App. It is the in-binary replacement for the standalone
// `ghcr.io/hanzoai/superbase` pod, whose whole job was `base.New()` + serve:
// cloud already links hanzoai/base, so it runs the SAME base app in-process.
//
// ONE WRITER, DURABLE. The base app opens a SQLite store under
// {deps.DataDir}/base — the cloud deployment is single-replica, strategy
// Recreate, backed by the RWO cloud-api-data PVC, so the embedded store is
// single-writer + single-open + durable across restarts, exactly the property
// the standalone base pod had with its own PVC.
//
// LAUNCH SURFACE. The waitlist plugin binds /v1/waitlist/* (join, status,
// boost, neighborhood, list, activity, track-share, invite, award, export) on
// the base router; that router's mux is mounted here so cloud serves the
// waitlist directly. console reads it via WAITLIST_URL=http://cloud.hanzo.svc.
// All waitlist knobs resolve from the environment at boot (see the plugin's
// Config.resolve): TURNSTILE_SECRET_KEY, WAITLIST_ADMIN_SECRET,
// WAITLIST_AWARD_SECRET, WAITLIST_DEFAULT_SLUGS, WAITLIST_ACCESS_CAPACITY,
// WAITLIST_OPEN — secrets injected from KMS, never in code.
//
// FAIL-CLOSED + STAGED. Like the ingress edge fold, the embed only activates
// when CLOUD_BASE_EMBED is truthy. Absent it, Mount is a no-op (the generic
// GET /v1/base/health liveness route still answers) so linking this subsystem
// into every cloud variant changes nothing until a single-writer deployment
// opts in. Activation is one CR env, ONE place.
package base

import (
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
	"github.com/zap-proto/zip"
)

// embedEnv gates activation. Truthy (1/true/yes) embeds the base app; anything
// else (incl. unset) leaves the subsystem a health-only no-op.
const embedEnv = "CLOUD_BASE_EMBED"

func embedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(embedEnv))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// Mount embeds base + the waitlist plugin and binds /v1/waitlist/* onto app.
// It creates the base app, registers the waitlist plugin, bootstraps (opening
// the durable SQLite store and seeding collections), then builds base's HTTP
// mux the SAME way apis.Serve does — NewRouter -> fire OnServe (plugins bind
// their routes) -> BuildMux — minus binding any listener, and adapts that mux
// onto the shared zip app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("base.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("base.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "base")

	// Native /v1/base/health — always answers (HealthOwner), no auth, BEFORE the
	// embed gate, so the liveness probe never depends on CLOUD_BASE_EMBED (same
	// pattern as clients/plan + clients/pricing).
	app.Get("/v1/base/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"service": "base", "status": "ok"})
	})

	if !embedEnabled() {
		log.Info("base embed disabled; /v1/waitlist off (set CLOUD_BASE_EMBED=1 to enable)")
		return nil
	}

	dataDir := filepath.Join(deps.DataDir, "base")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("base.Mount: mkdir %s: %w", dataDir, err)
	}

	bapp := baseapp.NewWithConfig(baseapp.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})

	// Waitlist — the launch surface. Enabled explicitly; every other knob
	// (secrets, slugs, capacity, open) resolves from the environment at boot.
	waitlist.MustRegister(bapp, waitlist.Config{Enabled: true})

	// Bootstrap opens the DB + fires OnBootstrap (waitlist ensureSchema + seeds
	// WAITLIST_DEFAULT_SLUGS); RunAllMigrations applies base core migrations.
	if err := bapp.Bootstrap(); err != nil {
		return fmt.Errorf("base.Mount: bootstrap: %w", err)
	}
	if err := bapp.RunAllMigrations(); err != nil {
		return fmt.Errorf("base.Mount: migrations: %w", err)
	}

	handler, err := buildMux(bapp)
	if err != nil {
		return fmt.Errorf("base.Mount: build mux: %w", err)
	}

	h := zip.AdaptNetHTTP(handler)
	app.All("/v1/waitlist", h)
	app.All("/v1/waitlist/*", h)

	log.Info("base app embedded; waitlist live",
		"dataDir", dataDir, "surface", "/v1/waitlist/*", "brand", deps.Brand)
	return nil
}

// buildMux reproduces apis.Serve's handler construction without binding a
// listener: build the base router, fire the OnServe hook so plugins (waitlist)
// register their routes on it, then compile the mux. The one-off finalizer
// captures the built handler — the SAME code path apis.Serve runs inside its
// OnServe trigger, just without the net.Listen.
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

func init() {
	// Order 60 (matching the long-reserved base slot): the embedded base app +
	// waitlist bind /v1/waitlist/* well before hanzoai/ai's /v1/* catch-all (150).
	// cloud.HealthOwner: base serves its OWN /v1/base/health in Mount (above), so
	// the generic liveness route never shadows it and it answers even embed-off.
	cloud.Register("base", 60, cloud.Typed(Mount), cloud.HealthOwner)
}
