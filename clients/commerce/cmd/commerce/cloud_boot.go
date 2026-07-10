// Copyright © 2026 Hanzo AI. MIT License.

//go:build cloud
// +build cloud

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	// The package import doubles as the cloud.Registry side-effect
	// trigger — commerce.Mount registers itself via init() at the same
	// import path. One named binding, not two.
	commerce "github.com/hanzoai/cloud/clients/commerce"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
	luxlog "github.com/luxfi/log"
)

// bootCloud serves commerce as a subsystem inside a zip.App per the
// HIP-0106 unified Hanzo Cloud binary contract. The gin engine is
// adapted via zip.AdaptNetHTTP and mounted under /v1/commerce + /_/commerce
// by commerce.Mount(); zip owns the listener.
//
// Same artifact runs every white-label deployment — only the gateway and
// brand differ. This is the path Phase 2+ migrates handler-by-handler to
// native zip routes; Phase 1 just gets the cutover live.
func bootCloud(dataDir, httpAddr string, dev, requireIdentity bool) error {
	logger := luxlog.New("commerce")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Build the zip.App with the canonical middleware pipeline. Match
	// cmd/cloud/main.go ordering so the cloud-mounted commerce and the
	// in-cloud-binary commerce behave identically at the boundary:
	//   Recover → RequestID → Logger. Auth/Telemetry are still gateway-
	//   owned in Phase 1 (per HIP-0026 trust boundary).
	app := zip.New(zip.Config{Logger: logger})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(logger))

	// Cloud Deps. In Phase 1 the inter-subsystem clients (Payments,
	// Vault, IAM, KMS) stay nil — Mount() warns at startup and the
	// payment-path handlers fail closed. Same as cmd/cloud today.
	deps := cloud.Deps{
		Logger:  logger,
		Brand:   envStr("CLOUD_BRAND", "hanzo"),
		Domain:  envStr("CLOUD_DOMAIN", "commerce.hanzo.ai"),
		DataDir: dataDir,
	}

	// commerce.Mount also boots the embedded gin engine via commerce.Embed,
	// which honors COMMERCE_DIR/HTTPAddr/etc. via DefaultConfig if dataDir
	// is empty. Set the env so the engine binds the same shape as the
	// legacy boot — this is intentional in Phase 1, even though the
	// listener inside Embed is unused (the zip.App owns the listener and
	// the embedded http handler is adapted, not served).
	_ = os.Setenv("COMMERCE_DIR", dataDir)
	_ = os.Setenv("COMMERCED_REQUIRE_IDENTITY", boolStr(requireIdentity))
	if dev {
		_ = os.Setenv("COMMERCE_DEV", "true")
	}

	if err := commerce.Mount(app, deps); err != nil {
		return err
	}

	listenErr := make(chan error, 1)
	go func() {
		logger.Info("http listener",
			"addr", httpAddr,
			"version", commerce.Version,
			"mode", "cloud",
			"brand", deps.Brand,
			"domain", deps.Domain,
		)
		if err := app.Listen(httpAddr); err != nil {
			listenErr <- err
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		return err
	}
	select {
	case err := <-listenErr:
		return err
	default:
		return nil
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
