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
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/goja"
	"github.com/hanzoai/cloud/clients/principal"
	hpricing "github.com/hanzoai/pricing"
	"github.com/zap-proto/zip"
)

var (
	host *goja.Host
	// cat is the catalog enablement overlay (SQLite/Base). It lays mutable
	// {enabled,betaOrgs,overrides} state over the static bundle and gates the
	// catalog read path. nil only before Mount; the read handlers fall back to
	// the raw bundle output when it is nil.
	cat *catalog
)

// Mount registers the pricing surface on app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("pricing.Mount: nil zip.App")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("pricing.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "pricing")

	bundle, err := hpricing.Bundle()
	if err != nil {
		return fmt.Errorf("pricing.Mount: load bundle: %w", err)
	}
	pricingData, err := hpricing.Pricing()
	if err != nil {
		return fmt.Errorf("pricing.Mount: load pricing.json: %w", err)
	}
	plansExtra, err := hpricing.PlansExtra()
	if err != nil {
		return fmt.Errorf("pricing.Mount: load plans-extra: %w", err)
	}
	// The pricing bundle also reads the @hanzo/plans catalog for the
	// subscription/blockchain/policy/tools/gpu endpoints. We pull that from the
	// plans embed module so both subsystems share ONE source of truth.
	plansData, err := loadPlansCatalog()
	if err != nil {
		return fmt.Errorf("pricing.Mount: load plans catalog: %w", err)
	}

	h, err := goja.New(goja.Config{
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
		return fmt.Errorf("pricing.Mount: goja host: %w", err)
	}
	host = h

	// Catalog enablement overlay. It is a SECURITY CONTROL: an admin-hidden model
	// must stay hidden across restarts. A non-persistent (in-memory) overlay would
	// silently re-expose hidden models on the next pod start — a fail-OPEN
	// degradation of a security control. So an empty DataDir is a hard boot error
	// (prod sets CLOUD_DATA_DIR), never a silent downgrade. provisioningsvc already
	// requires DataDir, so the unified binary always provides one.
	if deps.DataDir == "" {
		return fmt.Errorf("pricing.Mount: empty DataDir — the catalog enablement overlay requires a persistent data dir (set CLOUD_DATA_DIR); refusing to boot with a non-persistent overlay that would re-expose admin-hidden models on restart")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("pricing.Mount: data dir: %w", err)
	}
	dbPath := filepath.Join(deps.DataDir, "catalog.db")
	cstore, err := openCatalog(dbPath)
	if err != nil {
		return fmt.Errorf("pricing.Mount: open catalog overlay: %w", err)
	}
	cat = cstore

	app.Get("/v1/pricing/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "service": "pricing"})
	})

	// /v1/pricing/* read surface (mirrors server.mjs handler-for-handler). The
	// catalog routes (the root blob + models/free/featured/providers/summary +
	// the single model lookup) are gated below by the enablement overlay;
	// everything else is a straight pass-through of the bundle's output. The
	// `fixed` list is audited to carry NO model/provider identities (plans/infra/
	// tools/gpu/policy only) — confirmed against the plans catalog.
	type binding struct{ path, route string }
	fixed := []binding{
		{"/v1/pricing/compute", "compute"},
		{"/v1/pricing/compute/presets", "compute/presets"},
		{"/v1/pricing/cloud", "cloud"},
		{"/v1/pricing/cloud/plans", "cloud/plans"},
		{"/v1/pricing/cloud/regions", "cloud/regions"},
		{"/v1/pricing/cloud/storage", "cloud/storage"},
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

	// Gated catalog read path: the overlay filters disabled/beta entries and
	// merges overrides for the calling org (admins see everything, flagged).
	// The root /v1/pricing returns the WHOLE blob (hanzoModels, thirdPartyModels,
	// providers, freeModels, families) so it MUST be gated too — else it is an
	// un-gated second source for everything the leaves hide.
	app.Get("/v1/pricing", gatedRoot)
	app.Get("/v1/pricing/models", gatedModelList("models"))
	app.Get("/v1/pricing/free", gatedModelList("free"))
	app.Get("/v1/pricing/featured", gatedModelList("featured"))
	app.Get("/v1/pricing/providers", gatedProviders)
	app.Get("/v1/pricing/summary", gatedSummary)
	app.Get("/v1/pricing/model/:name", gatedModel)

	// Admin write surface for the overlay (global-admin only; see admin.go).
	app.Get("/v1/admin/catalog", adminCatalog)
	app.Patch("/v1/admin/catalog/models/*", adminPatchModel)
	app.Patch("/v1/admin/catalog/providers/:name", adminPatchProvider)

	// Enablement registry (#30/#31) over the SAME overlay store (see enablement.go):
	// global off|beta|ga (admin) + per-user/org beta self-opt-in (any authed user).
	app.Get("/v1/admin/enablement", adminEnablementList)
	app.Put("/v1/admin/enablement", adminEnablementSet)
	app.Get("/v1/enablement", enablementView)
	app.Post("/v1/enablement/optin", enablementOptIn)
	app.Post("/v1/enablement/optout", enablementOptOut)

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
		"fixed_routes", len(fixed),
		"gated_routes", 6, // models, free, featured, providers, summary, model/:name
		"admin_routes", 3, // GET /v1/admin/catalog + PATCH models/* + PATCH providers/:name
		"overlay_db", dbPath,
		"express", false,
		"goja", true,
		"brand", deps.Brand,
	)
	return nil
}

// rawDispatch runs a goja route and returns its status + JSON body — the single
// point that touches the goja host on the read path. The gated handlers
// post-filter this body through the overlay; dispatch writes it verbatim.
func rawDispatch(c *zip.Ctx, route string, params map[string]string) (int, json.RawMessage, error) {
	if host == nil {
		return 0, nil, fmt.Errorf("pricing not initialised")
	}
	// Public catalog: only a VALIDATED principal selects its org overlay; an
	// anonymous or client-forged X-Org-Id falls back to the public "hanzo" default.
	tenant := "hanzo"
	if org, ok := principal.Org(c); ok {
		tenant = org
	}
	resp, err := host.Dispatch(c.Context(), goja.Request{Route: route, Params: params, Tenant: tenant})
	if err != nil {
		return 0, nil, err
	}
	return resp.Status, resp.Body, nil
}

// trustedOrg is the caller's org for catalog VISIBILITY gating, honored only for
// a VALIDATED principal; an anonymous or client-forged X-Org-Id yields "" (the
// public, non-org view) so it cannot peek another org's enablement overlay.
func trustedOrg(c *zip.Ctx) string {
	if org, ok := principal.Org(c); ok {
		return org
	}
	return ""
}

// dispatch writes a goja route's output verbatim — the ungated pass-through for
// the non-catalog read routes.
func dispatch(c *zip.Ctx, route string, params map[string]string) error {
	status, body, err := rawDispatch(c, route, params)
	if err != nil {
		c.Log().Error("pricing dispatch failed", "route", route, "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "pricing dispatch failed"})
	}
	return passthrough(c, status, body)
}

func passthrough(c *zip.Ctx, status int, body json.RawMessage) error {
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(status, body)
}

// gatedModelList gates a {updated,total,models[]} route (models/free/featured)
// through the overlay for the calling org, re-counting `total` over the gated set.
func gatedModelList(route string) zip.Handler {
	return func(c *zip.Ctx) error {
		status, body, err := rawDispatch(c, route, nil)
		if err != nil {
			c.Log().Error("pricing dispatch failed", "route", route, "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": "pricing dispatch failed"})
		}
		if status != http.StatusOK || cat == nil {
			return passthrough(c, status, body) // upstream error or no overlay: verbatim.
		}
		var payload struct {
			Updated any     `json:"updated"`
			Models  []Model `json:"models"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return passthrough(c, status, body) // unrecognised shape: never corrupt it.
		}
		gated, err := cat.Models(c.Context(), payload.Models, trustedOrg(c), c.IsAdmin())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": "catalog gate failed"})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"updated": payload.Updated,
			"total":   len(gated),
			"models":  gated,
		})
	}
}

// gatedModel gates the single-model lookup: a model hidden for the caller's org
// 404s — indistinguishable from absent, so disabled models get no existence
// oracle on the direct path.
func gatedModel(c *zip.Ctx) error {
	name := c.Param("name")
	status, body, err := rawDispatch(c, "model", map[string]string{"name": name})
	if err != nil {
		c.Log().Error("pricing dispatch failed", "route", "model", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "pricing dispatch failed"})
	}
	if status != http.StatusOK || cat == nil {
		return passthrough(c, status, body)
	}
	var m Model
	if err := json.Unmarshal(body, &m); err != nil {
		return passthrough(c, status, body)
	}
	gated, err := cat.Models(c.Context(), []Model{m}, trustedOrg(c), c.IsAdmin())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "catalog gate failed"})
	}
	if len(gated) == 0 {
		return c.JSON(http.StatusNotFound, map[string]any{"error": "Model not found: " + name})
	}
	return c.JSON(http.StatusOK, gated[0])
}

// gatedProviders gates the {updated,providers{}} dict route.
func gatedProviders(c *zip.Ctx) error {
	status, body, err := rawDispatch(c, "providers", nil)
	if err != nil {
		c.Log().Error("pricing dispatch failed", "route", "providers", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "pricing dispatch failed"})
	}
	if status != http.StatusOK || cat == nil {
		return passthrough(c, status, body)
	}
	var payload struct {
		Updated   any            `json:"updated"`
		Providers map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return passthrough(c, status, body)
	}
	snap, err := cat.Snapshot(c.Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "catalog gate failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"updated":   payload.Updated,
		"providers": VisibleProviders(payload.Providers, snap, trustedOrg(c), c.IsAdmin()),
	})
}

// gatedSummary passes the stats summary through but filters its providers
// sub-dict so a disabled provider's name never leaks. Aggregate counts are the
// bundle's catalog-wide stats, intentionally left untouched.
func gatedSummary(c *zip.Ctx) error {
	status, body, err := rawDispatch(c, "summary", nil)
	if err != nil {
		c.Log().Error("pricing dispatch failed", "route", "summary", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "pricing dispatch failed"})
	}
	if status != http.StatusOK || cat == nil {
		return passthrough(c, status, body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return passthrough(c, status, body)
	}
	if provs, ok := payload["providers"].(map[string]any); ok {
		snap, err := cat.Snapshot(c.Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": "catalog gate failed"})
		}
		payload["providers"] = VisibleProviders(provs, snap, trustedOrg(c), c.IsAdmin())
	}
	return c.JSON(http.StatusOK, payload)
}

// gatedRoot gates the kitchen-sink root payload (GET /v1/pricing returns the
// whole blob) so it never leaks a model/provider that the leaf routes hide.
func gatedRoot(c *zip.Ctx) error {
	status, body, err := rawDispatch(c, "pricing", nil)
	if err != nil {
		c.Log().Error("pricing dispatch failed", "route", "pricing", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "pricing dispatch failed"})
	}
	if status != http.StatusOK || cat == nil {
		return passthrough(c, status, body)
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return passthrough(c, status, body)
	}
	snap, err := cat.Snapshot(c.Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "catalog gate failed"})
	}
	GateRootData(data, snap, trustedOrg(c), c.IsAdmin())
	return c.JSON(http.StatusOK, data)
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
	// cloud.HealthOwner: pricing serves its own /v1/pricing/health (Mount), so
	// Serve skips the generic always-ok route rather than shadowing it.
	cloud.Register("pricing", 112, cloud.Typed(Mount), cloud.HealthOwner)
}

// Shutdown drops the goja host and closes the catalog overlay store. Idempotent.
func Shutdown(context.Context) error {
	var herr error
	if host != nil {
		herr = host.Close()
		host = nil
	}
	if cat != nil {
		_ = cat.Close()
		cat = nil
	}
	return herr
}
