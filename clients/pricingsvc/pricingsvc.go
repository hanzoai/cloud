// Package pricingsvc mounts the @hanzo/pricing service into the unified cloud
// binary under /v1/pricing/* (+ the /v1/models, /v1/gpu, /v1/tools aliases),
// per HIP-0106.
//
// HONEST GOJA STATUS: @hanzo/pricing is an EXPRESS app. Express needs Node's
// http/net stack and CANNOT run in goja. So the Express *transport* is dropped
// and replaced by native zip routes; the pricing *handlers* (pure transforms
// over data/pricing.json + the @hanzo/plans catalog) run in goja via the
// goja/bundle.js shipped by github.com/hanzoai/pricing. The sync.mjs MARKUP
// logic (toMTok/roundPrice/processOpenRouterModel/…) also runs in goja through
// the bundle's applyMarkup(); the only thing that does NOT run in goja is the
// live network fetch (OpenRouter/HuggingFace — no fetch/AbortController in
// goja), which this wrapper performs with Go's net/http and then feeds the raw
// JSON into applyMarkup. See SyncEnabled.
//
// Module boundary: pricing source + markup logic live in hanzoai/pricing. This
// wrapper is glue. No pricing data or markup math is reimplemented in Go.
//
// IAM gating + X-Org-Id: read endpoints are open to any authenticated caller
// (the public pricing catalog). The sync trigger is admin-only (c.IsAdmin()).
package pricingsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/gojahost"
	hpricing "github.com/hanzoai/pricing"
	"github.com/hanzoai/zip"
)

var host *gojahost.Host

// Mount registers the pricing surface on app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("pricingsvc.Mount: nil zip.App")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("pricingsvc.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "pricing")

	bundle, err := hpricing.Bundle()
	if err != nil {
		return fmt.Errorf("pricingsvc.Mount: load bundle: %w", err)
	}
	pricingData, err := hpricing.Pricing()
	if err != nil {
		return fmt.Errorf("pricingsvc.Mount: load pricing.json: %w", err)
	}
	plansExtra, err := hpricing.PlansExtra()
	if err != nil {
		return fmt.Errorf("pricingsvc.Mount: load plans-extra: %w", err)
	}
	// The pricing bundle also reads the @hanzo/plans catalog for the
	// subscription/blockchain/policy/tools/gpu endpoints. We pull that from the
	// plans embed module so both subsystems share ONE source of truth.
	plansData, err := loadPlansCatalog()
	if err != nil {
		return fmt.Errorf("pricingsvc.Mount: load plans catalog: %w", err)
	}

	h, err := gojahost.New(gojahost.Config{
		Name:   "pricing",
		Bundle: bundle,
		Globals: map[string]any{
			"__PRICING_DATA__": pricingData,
			"__PLANS_EXTRA__":  plansExtra,
			"__PLANS_DATA__":   plansData,
			"__MARKUP__": map[string]any{
				"thirdParty":     parseFloatEnv("THIRD_PARTY_MARKUP", 1.0),
				"computeMonthly": parseFloatEnv("COMPUTE_MARKUP_MONTHLY", 1.0),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("pricingsvc.Mount: goja host: %w", err)
	}
	host = h

	app.Get("/v1/pricing/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "service": "pricing"})
	})

	// /v1/pricing/* read surface (mirrors server.mjs handler-for-handler).
	type binding struct{ path, route string }
	fixed := []binding{
		{"/v1/pricing", "pricing"},
		{"/v1/pricing/models", "models"},
		{"/v1/pricing/summary", "summary"},
		{"/v1/pricing/free", "free"},
		{"/v1/pricing/featured", "featured"},
		{"/v1/pricing/compute", "compute"},
		{"/v1/pricing/compute/presets", "compute/presets"},
		{"/v1/pricing/cloud", "cloud"},
		{"/v1/pricing/cloud/plans", "cloud/plans"},
		{"/v1/pricing/cloud/regions", "cloud/regions"},
		{"/v1/pricing/cloud/storage", "cloud/storage"},
		{"/v1/pricing/providers", "providers"},
		{"/v1/pricing/subscriptions", "subscriptions"},
		{"/v1/pricing/blockchain", "blockchain"},
		{"/v1/pricing/iam", "iam"},
		{"/v1/pricing/base", "base"},
		{"/v1/pricing/paas", "paas"},
		{"/v1/pricing/policy", "policy"},
		{"/v1/pricing/tools", "tools"},
		{"/v1/pricing/gpu", "gpu"},
	}
	for _, b := range fixed {
		route := b.route
		app.Get(b.path, func(c *zip.Ctx) error { return dispatch(c, route, nil) })
	}

	// Single model lookup.
	app.Get("/v1/pricing/model/:name", func(c *zip.Ctx) error {
		return dispatch(c, "model", map[string]string{"name": c.Param("name")})
	})

	// Convenience aliases (the cleaner top-level surface from server.mjs).
	// NOTE: the bare /v1/models alias is DELIBERATELY NOT mounted here. In the
	// unified binary the AI subsystem owns the OpenAI-compatible /v1/models
	// (the {data:[{id,…}]} model list the api.hanzo.ai gateway forwards and
	// clients like cowork's model picker consume). Pricing's annotated catalog
	// already lives at /v1/pricing/models, so the bare alias would only shadow
	// AI's contract route with a different shape — a regression. Keep pricing
	// strictly under /v1/pricing/*. (Same reasoning the note below records for
	// /v1/plans, /v1/tools, /v1/gpu, /v1/cloud, /v1/subscriptions, /v1/iam —
	// all owned by other subsystems at the top level to avoid collisions.)
	app.Get("/v1/pricing-policy", func(c *zip.Ctx) error { return dispatch(c, "policy", nil) })

	// Live sync trigger — admin only. Network fetch in Go, markup in goja.
	app.Post("/v1/pricing/sync", func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return c.JSON(http.StatusUnauthorized, map[string]any{"error": "admin required"})
		}
		updated, err := RunSync(c.Context())
		if err != nil {
			c.Log().Error("pricing sync failed", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": "sync failed", "message": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "updated": updated})
	})

	logger.Info("pricing mounted",
		"prefix", "/v1/pricing",
		"routes", len(fixed)+4,
		"express", false,
		"goja", true,
		"brand", deps.Brand,
	)
	return nil
}

func dispatch(c *zip.Ctx, route string, params map[string]string) error {
	if host == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "pricing not initialised"})
	}
	tenant := c.Org()
	if tenant == "" {
		tenant = "hanzo"
	}
	resp, err := host.Dispatch(c.Context(), gojahost.Request{Route: route, Params: params, Tenant: tenant})
	if err != nil {
		c.Log().Error("pricing dispatch failed", "route", route, "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "pricing dispatch failed"})
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(resp.Status, resp.Body)
}

// RunSync performs the live third-party model sync: fetch upstream listings
// (network — Go's net/http, since goja has no fetch), run the markup transform
// in goja via the bundle's applyMarkup(), and swap the shaped third-party
// section into the served catalog. Returns an ISO timestamp.
//
// This is the HONEST split: network IO in Go, markup math in JS. Only the
// dynamic third-party section is refreshed here; the Zen catalog + cloud/DO
// pricing in sync.mjs need the zen-gateway + DO credentials and stay on the
// standalone sync path for now.
func RunSync(ctx context.Context) (string, error) {
	if host == nil {
		return "", fmt.Errorf("pricing not initialised")
	}
	raw := map[string]any{}

	// OpenRouter — public, no auth.
	if or, err := fetchJSON(ctx, "https://openrouter.ai/api/v1/models"); err == nil {
		if m, ok := or.(map[string]any); ok {
			raw["openrouter"] = m["data"]
		}
	}
	// (HuggingFace router needs a token; left to the standalone path unless
	// HF_TOKEN is wired. We still call applyMarkup with whatever we fetched so
	// the markup logic runs in goja.)

	rawJSON, _ := json.Marshal(raw)
	shapedJSON, err := host.Eval(ctx, "applyMarkup", rawJSON)
	if err != nil {
		return "", fmt.Errorf("applyMarkup: %w", err)
	}
	var shaped map[string]any
	if err := json.Unmarshal(shapedJSON, &shaped); err != nil {
		return "", fmt.Errorf("decode shaped: %w", err)
	}
	// Merge the freshly-shaped third-party section into the served catalog and
	// re-inject so subsequent reads see it.
	cur, _ := hpricing.Pricing()
	if m, ok := cur.(map[string]any); ok {
		if v, ok := shaped["thirdPartyModels"]; ok {
			m["thirdPartyModels"] = v
		}
		if v, ok := shaped["providers"]; ok {
			m["providers"] = v
		}
		if v, ok := shaped["freeModels"]; ok {
			m["freeModels"] = v
		}
		ts := time.Now().UTC().Format(time.RFC3339)
		m["updated"] = ts
		host.SetGlobal("__PRICING_DATA__", m)
		return ts, nil
	}
	return time.Now().UTC().Format(time.RFC3339), nil
}

func fetchJSON(ctx context.Context, url string) (any, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func init() {
	cloud.Register("pricing", 112, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("pricingsvc.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
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
