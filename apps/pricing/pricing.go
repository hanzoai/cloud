// Package pricing is the price list: what every model, provider, GPU tier, tool and
// hosting plan costs.
//
// It serves /v1/pricing/* and /v1/pricing-policy — plus the enablement registry that
// decides which catalog entries a caller may even see
// (/v1/enablement{,/optin,/optout} and the SuperAdmin /v1/admin/{catalog,enablement}).
//
// It shares the @hanzo/plans catalog with apps/plan, so eight of its sections
// (cloud, subscriptions, blockchain, gpu, tools, policy, and the cloud/{regions,storage}
// pair) answer the same data /v1/plans/* answers at a second address.
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
	"github.com/hanzoai/cloud/apps/goja"
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("pricing.Mount: nil app")
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
	// (prod sets CLOUD_DATA_DIR), never a silent downgrade. provisioning already
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

	// The typed ops. zip.Get[In, Out] is ONE registry entry with N projections —
	// the REST route, the OpenAPI schema and prose, the MCP tool, the CLI command
	// and the generated SDK method — so each op below is declared exactly once
	// here and every consumer follows from it.
	//
	// They are declared on the APP with absolute paths rather than on a group per
	// prefix. This surface answers five top-level prefixes (Prefixes), and one of
	// its ops IS a prefix: GET /v1/pricing has no leaf, and a group cannot express
	// it — zip.Get(g, "") composes to "/v1/pricing/", a different route. Five
	// groups plus an app-level exception is two idioms; one absolute address per
	// op is one, and it is the form apps/marketing already uses.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("pricing.Mount: router is not backed by a zip app — typed ops have no registry to declare into")
	}
	// Bridge FIRST: a typed op receives only a context, so the validated org (and
	// the request the admin gate reads X-User-IsAdmin off) reach it by being
	// parked there. fiber runs middleware in registration order, so this must
	// precede every leaf below. On the scoped router it installs once per declared
	// prefix — which is why Prefixes has to name all five. Serve installs one
	// app-wide too; nesting is harmless (the inner one is what the handler sees)
	// and this is what makes the subsystem's own tests, which mount it on a bare
	// zip app, exercise the same identity path production does.
	app.Use(cloud.Bridge())

	o := ops{log: logger}
	zip.Get(zapp, "/v1/pricing/health", o.health)

	// /v1/pricing/* read surface (mirrors server.mjs handler-for-handler). The
	// catalog routes (the root blob + models/free/featured/providers/summary +
	// the single model lookup) are gated below by the enablement overlay;
	// the fixed sections carry NO model/provider identity (plans/infra/tools/
	// gpu/policy only — audited against the plans catalog) so there is nothing
	// for the overlay to gate and they answer the section verbatim. Both halves
	// are typed ops; the sections live in sections.go.
	mountSections(zapp, o)

	// Gated catalog read path: the overlay filters disabled/beta entries and
	// merges overrides for the calling org (admins see everything, flagged).
	// The root /v1/pricing returns the WHOLE blob (hanzoModels, thirdPartyModels,
	// providers, freeModels, families) so it MUST be gated too — else it is an
	// un-gated second source for everything the leaves hide.
	zip.Get(zapp, "/v1/pricing", o.catalog)
	zip.Get(zapp, "/v1/pricing/models", o.listModels)
	zip.Get(zapp, "/v1/pricing/free", o.listFree)
	zip.Get(zapp, "/v1/pricing/featured", o.listFeatured)
	zip.Get(zapp, "/v1/pricing/providers", o.listProviders)
	zip.Get(zapp, "/v1/pricing/summary", o.summary)
	zip.Get(zapp, "/v1/pricing/model/:name", o.getModel)

	// Admin write surface for the overlay (SuperAdmin only; see admin.go). These
	// two PATCHes are the ONLY untyped routes left on this surface: the model id
	// routes through a greedy wildcard, and typing that turns the WHOLE document
	// red (zip keys the op at `/…/models/*`, the document at `/…/models/{wildcard1}`,
	// and openapi.Fold refuses a registry entry whose route it cannot find); the
	// shared `overrides` field is an RFC 7386 merge patch stored and echoed
	// VERBATIM, which pins it to json.RawMessage — published as an array of
	// integers (it is []byte) — while map[string]any would reorder it. ops.go
	// carries the full reasoning and typed_wire_test.go PROVES both, so a fix
	// upstream shows up as a red test rather than as stale prose.
	zip.Get(zapp, "/v1/admin/catalog", o.adminCatalog)
	app.Patch("/v1/admin/catalog/models/*", adminPatchModel)
	zip.Patch(zapp, "/v1/admin/catalog/providers/:name", adminPatchProvider)

	// Enablement registry (#30/#31) over the SAME overlay store (see enablement.go):
	// global off|beta|ga (admin) + per-user/org beta self-opt-in (any authed user).
	zip.Get(zapp, "/v1/admin/enablement", o.adminEnablementList)
	zip.Put(zapp, "/v1/admin/enablement", o.adminEnablementSet)
	zip.Get(zapp, "/v1/enablement", o.enablementView)
	zip.Post(zapp, "/v1/enablement/optin", o.enablementOptIn)
	zip.Post(zapp, "/v1/enablement/optout", o.enablementOptOut)

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
	// The ONE top-level alias this surface does serve, /v1/pricing-policy, is
	// declared with the other sections in sections.go.

	// Live sync trigger — admin only. Network fetch in Go, markup in goja.
	zip.Post(zapp, "/v1/pricing/sync", o.sync)

	logger.Info("pricing mounted",
		"prefix", "/v1/pricing",
		"section_routes", 15, // the fixed plans/infra/tools/gpu/policy sections + the policy alias
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
// point that touches the goja host on the read path. The gated ops post-filter
// this body through the overlay; dispatch writes it verbatim.
//
// It takes a context, not a request: a typed op is handed only the former, and
// the tenant it needs rides there (callerOrg, parked by cloud.Bridge) — the same
// value principal.Org gave the raw handlers, so the two paths cannot disagree
// about whose overlay a catalog read selects.
func rawDispatch(ctx context.Context, route string, params map[string]string) (int, json.RawMessage, error) {
	if host == nil {
		return 0, nil, fmt.Errorf("pricing not initialised")
	}
	// Public catalog: only a VALIDATED principal selects its org overlay; an
	// anonymous or client-forged X-Org-Id falls back to the public "hanzo" default.
	tenant := "hanzo"
	if org := callerOrg(ctx); org != "" {
		tenant = org
	}
	resp, err := host.Dispatch(ctx, goja.Request{Route: route, Params: params, Tenant: tenant})
	if err != nil {
		return 0, nil, err
	}
	return resp.Status, resp.Body, nil
}

// ---- the typed read plane ----
//
// Every Out here replaces a map[string]any the raw handler built. Go emits a
// map's keys SORTED, so each struct declares its fields in that same order and
// the bytes on the wire are unchanged.

// pricingHealth is the subsystem's liveness answer.
type pricingHealth struct {
	// Service names the subsystem that answered.
	Service string `json:"service"`
	// Status is "ok" whenever this subsystem is mounted.
	Status string `json:"status"`
}

// Health reports that the pricing subsystem is mounted and serving. It answers
// from the process itself and consults neither the catalog bundle nor the
// enablement store, so it stays "ok" while either is degraded.
//
// Response: {"service":"pricing","status":"ok"}
func (o ops) health(context.Context, *pricingNoInput) (*pricingHealth, error) {
	return &pricingHealth{Service: "pricing", Status: "ok"}, nil
}

// pricingModelList is a catalog listing: the models visible to the caller, and
// how many that is.
type pricingModelList struct {
	// Models are the catalog entries visible to the caller, each an opaque
	// object exactly as the pricing source emits it, with any admin override
	// merged on top. An admin additionally sees hidden entries, each annotated
	// under "_overlay".
	Models []Model `json:"models"`
	// Total is how many models this answer carries — recounted over the visible
	// set, not the catalog's own total.
	Total int `json:"total"`
	// Updated is when the catalog was last refreshed, as the pricing source
	// recorded it.
	Updated any `json:"updated"`
}

// pricingProviderList is the provider directory visible to the caller.
type pricingProviderList struct {
	// Providers maps a provider name to its opaque info object. A provider
	// hidden for the caller's org is absent entirely.
	Providers map[string]any `json:"providers"`
	// Updated is when the catalog was last refreshed, as the pricing source
	// recorded it.
	Updated any `json:"updated"`
}

// pricingBlob is an object whose keys are the pricing source's own — a whole
// document (the root payload, the stats summary, a catalog section) or one entry
// inside a section's list. It is deliberately not enumerated: @hanzo/pricing and
// @hanzo/plans own the catalog's shape, and a Go struct claiming to know it would
// be a second, staler source that silently drops every field they add.
type pricingBlob map[string]any

// pricingModelRef addresses one catalog model.
type pricingModelRef struct {
	// Name is the model's name or its slugged id ("zen4",
	// "anthropic/claude-opus-4.6"), matched case-insensitively. It comes from
	// the path: the URL is the addressing authority.
	Name string `json:"name"`
}

// pricingSyncOut acknowledges a completed catalog sync.
type pricingSyncOut struct {
	// Status is "ok" when the sync completed.
	Status string `json:"status"`
	// Updated is the RFC 3339 time the refreshed catalog was stamped with.
	Updated string `json:"updated"`
}

// modelList is the shared body of the three catalog listings. It is a helper
// rather than a closure factory because cmd/zipdoc lifts prose from a bound
// METHOD, and a closure a helper returns is a call expression with nothing to
// read — so each listing is its own method with its own true description.
func (o ops) modelList(ctx context.Context, route string) (*pricingModelList, error) {
	status, body, err := rawDispatch(ctx, route, nil)
	if err != nil {
		o.log.Error("pricing dispatch failed", "route", route, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing dispatch failed")
	}
	if status != http.StatusOK {
		return nil, dispatchErr(status, body) // the bundle's own status and message.
	}
	var payload struct {
		Updated any     `json:"updated"`
		Models  []Model `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing catalog shape not recognised")
	}
	if cat == nil {
		// No overlay (only after Shutdown): the catalog is un-gated, exactly as
		// the bundle emitted it.
		return &pricingModelList{Models: payload.Models, Total: len(payload.Models), Updated: payload.Updated}, nil
	}
	gated, err := cat.Models(ctx, payload.Models, callerOrg(ctx), callerIsAdmin(ctx))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog gate failed")
	}
	return &pricingModelList{Models: gated, Total: len(gated), Updated: payload.Updated}, nil
}

// ListModels returns the whole model catalog — Hanzo's own Zen models and every
// third-party model — filtered to what the caller's org may see. A model an
// admin has disabled is absent; one in beta appears only for an org granted it.
// A SuperAdmin sees every model, each annotated with its enablement state.
func (o ops) listModels(ctx context.Context, _ *pricingNoInput) (*pricingModelList, error) {
	return o.modelList(ctx, "models")
}

// ListFreeModels returns the models that cost nothing to call, filtered to what
// the caller's org may see. It is the same catalog as ListModels narrowed to
// entries the pricing source marks free.
func (o ops) listFree(ctx context.Context, _ *pricingNoInput) (*pricingModelList, error) {
	return o.modelList(ctx, "free")
}

// ListFeaturedModels returns the models the catalog highlights, filtered to what
// the caller's org may see. It is the same catalog as ListModels narrowed to
// entries the pricing source marks featured.
func (o ops) listFeatured(ctx context.Context, _ *pricingNoInput) (*pricingModelList, error) {
	return o.modelList(ctx, "featured")
}

// GetModel returns one model's catalog entry — its pricing, context window and
// capabilities as the pricing source records them. A model hidden for the
// caller's org answers the same 404 an unknown name does, so a disabled model
// gets no existence oracle.
//
// Example: {"name": "zen4"}
func (o ops) getModel(ctx context.Context, in *pricingModelRef) (*Model, error) {
	status, body, err := rawDispatch(ctx, "model", map[string]string{"name": in.Name})
	if err != nil {
		o.log.Error("pricing dispatch failed", "route", "model", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing dispatch failed")
	}
	if status != http.StatusOK {
		return nil, dispatchErr(status, body) // the bundle's own 404/503 and message.
	}
	var m Model
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing catalog shape not recognised")
	}
	if cat == nil {
		return &m, nil
	}
	gated, err := cat.Models(ctx, []Model{m}, callerOrg(ctx), callerIsAdmin(ctx))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog gate failed")
	}
	if len(gated) == 0 {
		return nil, zip.ErrNotFound("Model not found: " + in.Name)
	}
	return &gated[0], nil
}

// ListProviders returns the model providers the catalog knows, each with its
// info object, filtered to what the caller's org may see. A provider an admin
// has disabled is absent — and so are its models everywhere else on this
// surface, because a provider's state cascades to what it serves.
func (o ops) listProviders(ctx context.Context, _ *pricingNoInput) (*pricingProviderList, error) {
	status, body, err := rawDispatch(ctx, "providers", nil)
	if err != nil {
		o.log.Error("pricing dispatch failed", "route", "providers", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing dispatch failed")
	}
	if status != http.StatusOK {
		return nil, dispatchErr(status, body)
	}
	var payload struct {
		Updated   any            `json:"updated"`
		Providers map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing catalog shape not recognised")
	}
	if cat == nil {
		return &pricingProviderList{Providers: payload.Providers, Updated: payload.Updated}, nil
	}
	snap, err := cat.Snapshot(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog gate failed")
	}
	return &pricingProviderList{
		Providers: VisibleProviders(payload.Providers, snap, callerOrg(ctx), callerIsAdmin(ctx)),
		Updated:   payload.Updated,
	}, nil
}

// GetPricingSummary returns the catalog's headline statistics — model counts by
// family and the provider directory. The provider sub-object is filtered to what
// the caller's org may see, so a disabled provider's name never leaks; the
// aggregate counts are the catalog's own, over everything it holds.
func (o ops) summary(ctx context.Context, _ *pricingNoInput) (*pricingBlob, error) {
	status, body, err := rawDispatch(ctx, "summary", nil)
	if err != nil {
		o.log.Error("pricing dispatch failed", "route", "summary", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing dispatch failed")
	}
	if status != http.StatusOK {
		return nil, dispatchErr(status, body)
	}
	var payload pricingBlob
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing catalog shape not recognised")
	}
	if cat == nil {
		return &payload, nil
	}
	if provs, ok := payload["providers"].(map[string]any); ok {
		snap, err := cat.Snapshot(ctx)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "catalog gate failed")
		}
		payload["providers"] = VisibleProviders(provs, snap, callerOrg(ctx), callerIsAdmin(ctx))
	}
	return &payload, nil
}

// GetPricing returns the whole pricing catalog in one document: Zen and
// third-party models, providers, model families, the free-model list, plan and
// infrastructure pricing. Every model and provider it names is filtered to what
// the caller's org may see — the same gate the leaf routes apply, so this can
// never be an un-gated second source for what they hide.
func (o ops) catalog(ctx context.Context, _ *pricingNoInput) (*pricingBlob, error) {
	status, body, err := rawDispatch(ctx, "pricing", nil)
	if err != nil {
		o.log.Error("pricing dispatch failed", "route", "pricing", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing dispatch failed")
	}
	if status != http.StatusOK {
		return nil, dispatchErr(status, body)
	}
	var data pricingBlob
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing catalog shape not recognised")
	}
	if cat == nil {
		return &data, nil
	}
	snap, err := cat.Snapshot(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog gate failed")
	}
	GateRootData(data, snap, callerOrg(ctx), callerIsAdmin(ctx))
	return &data, nil
}

// SyncPricing refreshes the third-party section of the catalog from its upstream
// listings and returns the time the refreshed catalog was stamped with. The
// fetch runs in Go and the markup transform in the pricing bundle. SuperAdmin
// only; every other caller is refused.
func (o ops) sync(ctx context.Context, _ *pricingNoInput) (*pricingSyncOut, error) {
	if !callerIsAdmin(ctx) {
		return nil, zip.ErrUnauthorized("admin required")
	}
	updated, err := RunSync(ctx)
	if err != nil {
		// The cause is logged, not returned: zip's error carries one message and
		// the raw route's second field ("message") has nowhere to go, so the one
		// field both shapes have keeps exactly the text it always had.
		o.log.Error("pricing sync failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "sync failed")
	}
	return &pricingSyncOut{Status: "ok", Updated: updated}, nil
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
