// Copyright © 2026 Hanzo AI. MIT License.
//
// commerced is the Hanzo Commerce daemon: one Go binary, gateway-trust
// identity (no in-binary JWKS), embedded admin SPA at /_/commerce/.
// Mirrors the cmd/tasksd / cmd/iamd shape — thin entrypoint, all
// surface area in pkg/commerce.

package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	commerceApp "github.com/hanzoai/cloud/clients/commerce"
	api "github.com/hanzoai/cloud/clients/commerce/api/api"
	commerce "github.com/hanzoai/cloud/clients/commerce"
)

func main() {
	var (
		dataDir         = flag.String("data", envStr("COMMERCE_DIR", "./commerce_data"), "data directory")
		httpAddr        = flag.String("http", envStr("COMMERCE_HTTP", "127.0.0.1:8090"), "HTTP listen address")
		dev             = flag.Bool("dev", envBool("COMMERCE_DEV", false), "enable development mode")
		requireIdentity = flag.Bool("require-identity", envBool("COMMERCED_REQUIRE_IDENTITY", false), "refuse requests without X-Org-Id/X-User-Id (gateway trust)")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown := initTelemetry(ctx, "hanzo-commerce")
	defer shutdown(context.Background())

	srv, err := commerce.Embed(ctx, commerce.EmbedConfig{
		DataDir:         *dataDir,
		HTTPAddr:        *httpAddr,
		Dev:             *dev,
		RequireIdentity: *requireIdentity,
		Logger:          logger,
	})
	if err != nil {
		logger.Error("commerce.Embed", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Stop(shutdownCtx)
	}()

	// Register the full Commerce API routes (billing, checkout, store, …)
	// directly on the live router.
	//
	// Previously this attempted to bind OnRouteSetup AFTER Bootstrap had
	// already fired — which silently no-ops, so /v1/billing/* returned 404
	// for the entire life of commerced. The fix is to mount routes
	// imperatively on the live *gin.Engine the moment Embed returns.
	//
	// api.Route() expects a router.Router (gin.IRouter). The App.Router
	// is *gin.Engine which satisfies that. Group at "/v1/" so handlers
	// land under /v1/billing/*, /v1/checkout/*, etc. matching the global
	// "/v1/ only" rule and the Prefixes["api"]="/v1/" config.
	apiGroup := srv.App().Router.Group("/v1")
	api.Route(apiGroup)

	httpSrv := &http.Server{
		Addr:              srv.HTTPAddr(),
		Handler:           srv.HTTPHandler(),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("http listener", "addr", httpSrv.Addr, "version", commerceApp.Version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

