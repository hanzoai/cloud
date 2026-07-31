// Package plan mounts the @hanzo/plans catalog into the unified cloud
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
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	hplans "github.com/hanzoai/plans"
	"github.com/zap-proto/zip"
)

// host is the process-global goja host for the plans bundle. nil before Mount.
var host *goja.Host

// noArgs is the input of an op that takes nothing.
type noArgs struct{}

// healthView is the GET /v1/plans/health response.
type healthView struct {
	// Status is "ok" whenever the route answers — the probe is native, so it needs
	// neither the bundle nor a catalog to reply.
	Status string `json:"status"`
	// Service names which subsystem answered, so a shadowing route is visible.
	Service string `json:"service"`
}

// health reports that the plans subsystem is mounted and answering. It runs no JS
// and reads no catalog, so it stays true when the bundle cannot resolve a plan.
//
// Response: {"status": "ok", "service": "plans"}
func health(context.Context, *noArgs) (*healthView, error) {
	return &healthView{Status: "ok", Service: "plans"}, nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, the ONLY way prose reaches the published document, the MCP tool
// list and the CLI help — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Mount registers the /v1/plans/* surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("plan.Mount: nil app")
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

	g := app.Group("/v1/plans")

	// Native health endpoint — always answers, no JS, no auth. It is the ONE route
	// here with a shape this package knows, so it is the one typed op; a router with
	// no registry loses only that declaration, not the surface.
	if reg := cloud.ZipApp(app); reg != nil {
		zip.Get(reg, "/v1/plans/health", health)
	} else {
		g.Get("/health", func(c *zip.Ctx) error {
			return c.JSON(http.StatusOK, healthView{Status: "ok", Service: "plans"})
		})
	}

	// Every route below dispatches into the goja bundle, which answers with its OWN
	// bytes and its OWN status (200, or 404 for an unknown plan). Neither is a shape
	// this package holds — the catalog's schema lives in @hanzo/plans — so none of
	// them can be a typed op: an Out would have to be invented, and the status
	// passthrough would be lost.
	//
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
	g.Get("/resolve/:id", func(c *zip.Ctx) error {
		return dispatch(c, "resolve", map[string]string{"id": c.Param("id")})
	})
	g.Get("/entitlements/:id", func(c *zip.Ctx) error {
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
	if org, ok := principal.Org(c); ok {
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

// Entitlements resolves the canonical entitlement block for a plan id from the
// @hanzo/plans catalog (the single source of truth). It runs the bundle's
// "entitlements" route on the shared goja host and returns the parsed, namespaced
// `entitlements` map (e.g. "world.api_rate_limit", "ai.tokens_per_min"). This is
// the one Go seam other subsystems use to read plan entitlements without importing
// the catalog data or reimplementing the fromLegacy derivation. Tenant is the
// public "hanzo" catalog (plan entitlements are tenant-independent metadata).
//
// It errors if the plans subsystem is not mounted or the plan id is unknown, so
// callers can fail closed rather than silently granting a default tier.
func Entitlements(ctx context.Context, id string) (map[string]any, error) {
	if host == nil {
		return nil, fmt.Errorf("plan.Entitlements: plans not mounted")
	}
	resp, err := host.Dispatch(ctx, goja.Request{
		Route:  "entitlements",
		Params: map[string]string{"id": id},
		Tenant: "hanzo",
	})
	if err != nil {
		return nil, fmt.Errorf("plan.Entitlements(%q): dispatch: %w", id, err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("plan.Entitlements(%q): status %d", id, resp.Status)
	}
	var out struct {
		Entitlements map[string]any `json:"entitlements"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("plan.Entitlements(%q): decode: %w", id, err)
	}
	return out.Entitlements, nil
}

// LicenseEntitlement resolves BOTH the canonical entitlement block AND the flat
// license-feature list for a plan id from the @hanzo/plans catalog (the single
// source of truth). It runs the bundle's "entitlements" route on the shared goja
// host — the SAME route /v1/plans/entitlements/:id serves — and returns the parsed
// `entitlements` map plus the `license_features` list the bundle's toLicenseFeatures
// transform produces. It is the seam the commerce entitlement resolver
// (commerce.CheckEntitlement) uses to map a subscription's plan tier to the flat
// features a signed license carries, WITHOUT reimplementing the vocabulary in Go
// (the entitlement transforms stay in one place — the JS bundle).
//
// found reports whether the plan id exists in the catalog. A 404 from the bundle is
// (nil, nil, false, nil): a real "unknown plan", NOT a machinery error — so a caller
// scanning several subscriptions can skip an unknown tier and keep going. ANY other
// failure (plans subsystem not mounted, dispatch error, non-200/404 status, decode
// error) returns a non-nil error so the money-path caller FAILS CLOSED rather than
// treating an unresolved plan as "grants nothing".
func LicenseEntitlement(ctx context.Context, id string) (entitlements map[string]any, features []string, found bool, err error) {
	if host == nil {
		return nil, nil, false, fmt.Errorf("plan.LicenseEntitlement: plans not mounted")
	}
	resp, err := host.Dispatch(ctx, goja.Request{
		Route:  "entitlements",
		Params: map[string]string{"id": id},
		Tenant: "hanzo",
	})
	if err != nil {
		return nil, nil, false, fmt.Errorf("plan.LicenseEntitlement(%q): dispatch: %w", id, err)
	}
	if resp.Status == http.StatusNotFound {
		return nil, nil, false, nil
	}
	if resp.Status != http.StatusOK {
		return nil, nil, false, fmt.Errorf("plan.LicenseEntitlement(%q): status %d", id, resp.Status)
	}
	var out struct {
		Entitlements    map[string]any `json:"entitlements"`
		LicenseFeatures []string       `json:"license_features"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, nil, false, fmt.Errorf("plan.LicenseEntitlement(%q): decode: %w", id, err)
	}
	return out.Entitlements, out.LicenseFeatures, true, nil
}

// withContentType sets application/json and returns the bytes unchanged.
func withContentType(c *zip.Ctx, b []byte) []byte {
	c.SetHeader("Content-Type", "application/json")
	return b
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
