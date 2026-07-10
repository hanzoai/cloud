// Copyright © 2026 Hanzo AI. MIT License.

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	commerce "github.com/hanzoai/cloud/clients/commerce"
	api "github.com/hanzoai/cloud/clients/commerce/api/api"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
)

// bootLegacy is the historical direct-Gin boot path — the shape the
// production deployment has been running. Embed() boots the gin engine,
// api.Route() wires the full /v1 API surface, net/http serves it.
//
// Kept default through Phase 1 of the cloud-mount migration so a flag
// flip is required to switch over; the same binary serves both shapes.
func bootLegacy(dataDir, httpAddr string, dev, requireIdentity bool) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := commerce.Embed(ctx, commerce.EmbedConfig{
		DataDir:         dataDir,
		HTTPAddr:        httpAddr,
		Dev:             dev,
		RequireIdentity: requireIdentity,
		Logger:          logger,
	})
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Stop(shutdownCtx)
	}()

	// Wire the full Commerce API routes (billing, checkout, store, …)
	// directly on the live router.
	//
	// Previously this attempted to bind OnRouteSetup AFTER Bootstrap had
	// already fired — which silently no-ops, so /v1/billing/* returned 404
	// for the entire life of commerce. The fix is to mount routes
	// imperatively on the live *gin.Engine the moment Embed returns.
	apiGroup := srv.App().Router.Group("/v1")
	// EdgeAuth (the standalone-edge trust boundary that strips/mints identity)
	// is installed by Bootstrap (server.go installIdentityBoundary) BEFORE
	// pkg/auth.Gin and ahead of every route group, so it covers /v1 here too
	// (this group is registered after Bootstrap returns). See middleware/edgeauth.go.
	//
	// IAM gateway-trust shim: every /v1/* handler that calls
	// middleware.GetOrganization(c) needs the "organization" gin key
	// populated. The store-backed setupRoutes path only wires
	// IAMTokenRequired() on /v1/commerce/* (commerce.go:823); when the
	// gateway hits /v1/billing/me/welcome (or any other /v1/billing/*
	// path) with X-Org-Id headers, accesstoken middleware skips
	// (gateway-trusted, no DB token), the organization key never gets
	// set, and middleware.GetOrganization panics with "key organization
	// does not exist". Pin IAMTokenRequired on the /v1 root so the
	// header → org resolution runs for everything api.Route() registers.
	apiGroup.Use(iammiddleware.IAMTokenRequired())
	api.Route(apiGroup)

	httpSrv := &http.Server{
		Addr:              srv.HTTPAddr(),
		Handler:           srv.HTTPHandler(),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http listener", "addr", httpSrv.Addr, "version", commerce.Version, "mode", "legacy")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
