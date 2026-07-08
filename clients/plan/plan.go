// Package plansvc mounts the @hanzo/plans catalog into the unified cloud
// binary under /v1/plans/*, per HIP-0106.
//
// STRATEGY: wrap, don't rewrite. @hanzo/plans is a Node data package (JSON
// catalog + entitlements.mjs transforms). We do NOT reimplement the entitlement
// vocabulary in Go and we do NOT copy the catalog into cloud. Instead:
//
//   - github.com/hanzoai/plans (the service repo's Go embed module) ships
//     goja/bundle.js — the ESM-free port of entitlements.mjs + the /v1/plans
//     route table — plus the embedded *.json catalog (plans.Data()).
//   - This wrapper loads that bundle into a goja runtime (clients/goja),
//     injects the catalog as globalThis.__PLANS_DATA__, and registers thin zip
//     handlers that call globalThis.handle({route, params, tenant}). The
//     entitlement transforms (fromLegacy/toLicenseFeatures/resolvePlan) run in
//     goja — real JS, not a Go reimplementation.
//
// The plans data is read-only public-catalog content; there are no secrets
// here. The licensing SIGNER/fingerprint that consumes toLicenseFeatures stays
// in hanzoai/licensing. This wrapper is pure glue.
//
// IAM gating + X-Org-Id tenant scope: every /v1/plans route reads the
// gateway-minted identity off the zip.Ctx (c.Org()) and threads it into the
// bundle as the tenant, so a reseller org (tenant_id != "hanzo") sees its own
// catalog overrides. The plan catalog is readable by any authenticated caller;
// no admin scope is required for reads.
package plan

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/goja"
	"github.com/hanzoai/cloud/clients/principal"
	hplans "github.com/hanzoai/plans"
	"github.com/zap-proto/zip"
)

// host is the process-global goja host for the plans bundle. nil before Mount.
var host *goja.Host

// Mount registers the /v1/plans/* surface on app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("plan.Mount: nil zip.App")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("plan.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "plans")

	bundle, err := hplans.Bundle()
	if err != nil {
		return fmt.Errorf("plan.Mount: load bundle: %w", err)
	}
	data, err := hplans.Data()
	if err != nil {
		return fmt.Errorf("plan.Mount: load catalog: %w", err)
	}

	h, err := goja.New(goja.Config{
		Name:    "plans",
		Bundle:  bundle,
		Globals: map[string]any{"__PLANS_DATA__": data},
	})
	if err != nil {
		return fmt.Errorf("plan.Mount: goja host: %w", err)
	}
	host = h

	// Native health endpoint — always answers, no JS, no auth.
	app.Get("/v1/plans/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "service": "plans"})
	})

	// Fixed-route handlers. Each maps a path to a bundle route name.
	// gateway-minted identity (c.Org()) becomes the tenant for catalog scoping.
	type binding struct{ path, route string }
	fixed := []binding{
		{"/v1/plans", "plans"},
		{"/v1/plans/subscriptions", "subscriptions"},
		{"/v1/plans/cloud", "cloud"},
		{"/v1/plans/blockchain", "blockchain"},
		{"/v1/plans/dns", "dns"},
		{"/v1/plans/gpu", "gpu"},
		{"/v1/plans/regions", "regions"},
		{"/v1/plans/storage", "storage"},
		{"/v1/plans/tools", "tools"},
		{"/v1/plans/policy", "policy"},
		{"/v1/plans/schema", "schema"},
		{"/v1/plans/vocab", "vocab"},
	}
	for _, b := range fixed {
		route := b.route
		app.Get(b.path, func(c *zip.Ctx) error {
			return dispatch(c, route, nil)
		})
	}

	// Parameterized: resolve + entitlements take a plan id.
	app.Get("/v1/plans/resolve/:id", func(c *zip.Ctx) error {
		return dispatch(c, "resolve", map[string]string{"id": c.Param("id")})
	})
	app.Get("/v1/plans/entitlements/:id", func(c *zip.Ctx) error {
		return dispatch(c, "entitlements", map[string]string{"id": c.Param("id")})
	})

	logger.Info("plans mounted",
		"prefix", "/v1/plans",
		"routes", len(fixed)+2,
		"brand", deps.Brand,
	)
	return nil
}

// dispatch runs one bundle route on the shared goja host and writes the
// {status, body} back as JSON. The tenant is the gateway-minted org (X-Org-Id
// per HIP-0026) so reseller catalogs resolve correctly.
func dispatch(c *zip.Ctx, route string, params map[string]string) error {
	if host == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{
			"error": "plans not initialised",
		})
	}
	// Public catalog: only a VALIDATED principal selects a reseller's overlay; an
	// anonymous or client-forged X-Org-Id falls back to the public "hanzo" default
	// (never another reseller's catalog).
	tenant := "hanzo"
	if org, ok := principal.Tenant(c); ok {
		tenant = org
	}
	resp, err := host.Dispatch(c.Context(), goja.Request{
		Route:  route,
		Params: params,
		Tenant: tenant,
	})
	if err != nil {
		c.Log().Error("plans dispatch failed", "route", route, "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"error": "plans dispatch failed",
		})
	}
	return c.Bytes(resp.Status, withContentType(c, resp.Body))
}

// withContentType sets application/json and returns the bytes unchanged.
func withContentType(c *zip.Ctx, b []byte) []byte {
	c.SetHeader("Content-Type", "application/json")
	return b
}

func init() {
	// cloud.HealthOwner: plan serves its own /v1/plans/health (Mount), so
	// Serve skips the generic always-ok route rather than shadowing it.
	cloud.Register("plans", 111, cloud.Typed(Mount), cloud.HealthOwner)
}

// Shutdown drops the goja host. Idempotent.
func Shutdown(context.Context) error {
	if host == nil {
		return nil
	}
	err := host.Close()
	host = nil
	return err
}
