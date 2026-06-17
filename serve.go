package cloud

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanzoai/zip"
	"github.com/hanzoai/zip/middleware"
)

// Serve boots the canonical compose root and mounts the selected subsystems.
//
// This is the ONE place the cloud-server body lives. cmd/cloud (the full fused
// surface) and every `hanzo <svc>` subcommand share it; no boot logic is
// duplicated per entrypoint.
//
// enable==nil ⇒ honor cfg.Enable from flags/env (cloud mode; empty = all).
// enable!=nil ⇒ force exactly that set (single-service mode), overriding
// --enable so `hanzo kms` is unambiguous.
//
// Serve registers the HIP-0106 liveness contract (GET /v1/<name>/health for
// every enabled subsystem) before MountAll, runs the canonical middleware
// pipeline (Recover → RequestID → Logger), and shuts down gracefully on
// SIGINT/SIGTERM.
func Serve(enable []string) error {
	cfg := LoadConfig()
	if enable != nil {
		cfg.Enable = enable
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	deps := BuildDeps(cfg)

	app := zip.New(zip.Config{Logger: deps.Logger})

	// Canonical middleware pipeline. Order matters:
	//  1. Recover   — panic → JSON 500
	//  2. RequestID — generate / propagate X-Request-Id
	//  3. Logger    — request-line log
	// Telemetry/Auth stay gateway-owned in Phase 1 (enable once mounted).
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(deps.Logger))

	// HIP-0106 liveness contract: every enabled subsystem answers
	// GET /v1/<name>/health uniformly, registered at the compose root before
	// MountAll so it precedes subsystem /v1/<n>/* wildcards.
	for _, spec := range Registry {
		if !cfg.Enabled(spec.Name) {
			continue
		}
		name := spec.Name
		app.Get("/v1/"+name+"/health", func(c *zip.Ctx) error {
			return c.JSON(200, map[string]string{"service": name, "status": "ok"})
		})
	}

	if err := MountAll(app, cfg, deps); err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listenErr := make(chan error, 1)
	go func() {
		deps.Logger.Info("listening",
			"http", cfg.ListenAddr,
			"zap", cfg.ZAPListenAddr,
			"enabled", cfg.Enable,
			"brand", cfg.Brand,
			"domain", cfg.Domain,
		)
		listenErr <- app.Listen(cfg.ListenAddr)
	}()

	select {
	case <-ctx.Done():
		deps.Logger.Info("shutdown requested")
	case err := <-listenErr:
		return fmt.Errorf("listen: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return app.ShutdownWithContext(shutdownCtx)
}
