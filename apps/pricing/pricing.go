// Package pricing is the price list: what every model, provider, GPU tier, tool and
// hosting plan costs.
//
// It serves /v1/pricing/* — plus the enablement registry that
// decides which catalog entries a caller may even see
// (/v1/pricing/enablement{,/optin,/optout} and the SuperAdmin
// /v1/admin/pricing/{catalog,enablement}). Enablement is served here, over the
// same overlay store the price gate reads, so it is not a second capability: a
// second app on one store is the split HIP-0139 §7.2 refuses.
//
// It shares the @hanzo/plans catalog with apps/plan, so eight of its sections
// (cloud, subscriptions, blockchain, gpu, tools, policy, and the cloud/{regions,storage}
// pair) answer the same data /v1/plan/* answers at a second address.
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
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/goja"
	hpricing "github.com/hanzoai/pricing"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

var (
	host *goja.Host
	// cat is the catalog enablement overlay (SQLite/Base). It lays mutable
	// {enabled,betaOrgs,overrides} state over the static bundle and gates the
	// catalog read path. nil only before Mount; the read handlers fall back to
	// the raw bundle output when it is nil.
	cat *catalog
	// plog is the subsystem logger, kept so the document assembly in RunSync can
	// report where it sourced first-party prices. Set by Mount.
	plog luxlog.Logger
)

// Mount registers the pricing surface on app per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("pricing.Use:  nil app")
	}
	logger := luxlog.Default()
	if logger == nil {
		return fmt.Errorf("pricing.Use:  nil luxlog.Default()")
	}
	logger = logger.New("subsystem", "pricing")

	bundle, err := hpricing.Bundle()
	if err != nil {
		return fmt.Errorf("pricing.Use:  load bundle: %w", err)
	}
	pricingData, err := hpricing.Pricing()
	if err != nil {
		return fmt.Errorf("pricing.Use:  load pricing.json: %w", err)
	}
	// The embedded snapshot ships the document's shape and the resold section;
	// commerce owns the retail number on the models we make. See commerce.go.
	plog = logger
	pricingData = overlay(context.Background(), pricingData, logger)
	plansExtra, err := hpricing.PlansExtra()
	if err != nil {
		return fmt.Errorf("pricing.Use:  load plans-extra: %w", err)
	}
	// The pricing bundle also reads the @hanzo/plans catalog for the
	// subscription/blockchain/policy/tools/gpu endpoints. We pull that from the
	// plans embed module so both subsystems share ONE source of truth.
	plansData, err := loadPlansCatalog()
	if err != nil {
		return fmt.Errorf("pricing.Use:  load plans catalog: %w", err)
	}
	// The Datastore rate card is authored in the pricing repo rather than produced
	// by the sync, so it is its own file and its own global.
	datastoreCard, err := hpricing.Datastore()
	if err != nil {
		return fmt.Errorf("pricing.Use:  load datastore card: %w", err)
	}

	h, err := goja.New(goja.Config{
		Name:   "pricing",
		Bundle: bundle,
		Globals: map[string]any{
			"__PRICING_DATA__": pricingData,
			"__PLANS_EXTRA__":  plansExtra,
			"__PLANS_DATA__":   plansData,
			"__DATASTORE__":    datastoreCard,
			"__MARKUP__": map[string]any{
				"thirdParty":     parseFloatEnv("THIRD_PARTY_MARKUP", 1.0),
				"computeMonthly": parseFloatEnv("COMPUTE_MARKUP_MONTHLY", 1.0),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("pricing.Use:  goja host: %w", err)
	}
	host = h

	// Catalog enablement overlay. It is a SECURITY CONTROL: an admin-hidden model
	// must stay hidden across restarts. A non-persistent (in-memory) overlay would
	// silently re-expose hidden models on the next pod start — a fail-OPEN
	// degradation of a security control. So an empty DataDir is a hard boot error
	// (prod sets CLOUD_DATA_DIR), never a silent downgrade. provisioning already
	// requires DataDir, so the unified binary always provides one.
	if cloud.DataDir() == "" {
		return fmt.Errorf("pricing.Use:  empty DataDir — the catalog enablement overlay requires a persistent data dir (set CLOUD_DATA_DIR); refusing to boot with a non-persistent overlay that would re-expose admin-hidden models on restart")
	}
	cstore, err := openCatalog(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("pricing.Use:  open catalog overlay: %w", err)
	}
	cat = cstore

	// The typed ops. zip.Get[In, Out] is ONE registry entry with N projections —
	// the REST route, the OpenAPI schema and prose, the MCP tool, the CLI command
	// and the generated SDK method — so each op below is declared exactly once
	// here and every consumer follows from it.
	//
	// They are declared on the APP with absolute paths rather than on a group per
	// prefix. This surface answers two top-level prefixes (Prefixes), and one of
	// its ops IS a prefix: GET /v1/pricing has no leaf, and a group cannot express
	// it — zip.Get(g, "") composes to "/v1/pricing/", a different route. Groups
	// plus an app-level exception is two idioms; one absolute address per op is
	// one, and it is the form apps/marketing already uses.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("pricing.Use:  router is not backed by a zip app — typed ops have no registry to declare into")
	}
	// A typed op receives only a context, so the validated org — and the request
	// the admin gate reads X-User-IsAdmin off — reach it by being parked there by
	// cloud.Bridge, installed HERE, on this subsystem's own router and ahead of
	// every leaf below (fiber runs middleware in registration order, so one
	// installed after its leaves never runs).
	//
	// The composer installs one too, app-wide, and this is deliberately not a
	// substitute for that: the composer's runs after the identity check that mints
	// the org, an order only the composer can hold, and it covers every app in the
	// binary. What a subsystem cannot borrow is a composer that is not there —
	// this package's own tests mount Mount on a bare app, and so does anything
	// else that composes pricing without cloud.Serve. Without a local install a
	// typed op resolves no org THERE and only there, which is a failure that
	// exists in tests and never in production: the worst shape a gap can take.
	// Bridge derives everything it parks from the request, so the second pass over
	// the same request parks the same values.
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

	// Admin write surface for the overlay (SuperAdmin only; see admin.go). The
	// models PATCH is the ONE untyped route left on this surface, and the reason
	// is the greedy wildcard alone: zip keys a typed op at the fiber pattern
	// `/…/models/*` (Template rewrites `:name` segments and leaves `*` verbatim,
	// zip@v1.36.3/address.go:61) while this document keys the route at
	// `/…/models/{wildcard1}` (openapi/openapi.go:811), so openapi.Fold finds no
	// live route for the op and refuses (openapi/openapi.go:705) — and Spec builds
	// ONE document, so every other pricing operation goes down with it.
	//
	// Its BODIES are declared even so (admin.go's init): the wildcard blocks
	// typing, not describing. `overrides` was a second reason and is not one any
	// more — zip publishes a json.RawMessage as the unconstrained value it is —
	// which is why PATCH providers/:name below is an op. typed_wire_test.go proves
	// both against the toolchain in go.mod, so a fix upstream shows up as a red
	// test rather than as stale prose.
	zip.Get(zapp, "/v1/admin/pricing/catalog", o.adminCatalog)
	zip.Patch(zapp, "/v1/admin/pricing/catalog/models/*", adminPatchModel)
	zip.Patch(zapp, "/v1/admin/pricing/catalog/providers/:name", adminPatchProvider)

	// Enablement registry (#30/#31) over the SAME overlay store (see enablement.go):
	// global off|beta|ga (admin) + per-user/org beta self-opt-in (any authed user).
	zip.Get(zapp, "/v1/admin/pricing/enablement", o.adminEnablementList)
	zip.Put(zapp, "/v1/admin/pricing/enablement", o.adminEnablementSet)
	zip.Get(zapp, "/v1/pricing/enablement", o.enablementView)
	zip.Post(zapp, "/v1/pricing/enablement/optin", o.enablementOptIn)
	zip.Post(zapp, "/v1/pricing/enablement/optout", o.enablementOptOut)

	// Convenience aliases (the cleaner top-level surface from server.mjs).
	// NOTE: the bare /v1/models alias is DELIBERATELY NOT mounted here. In the
	// unified binary the AI subsystem owns the standard /v1/models
	// (the {data:[{id,…}]} model list the api.hanzo.ai gateway forwards and
	// clients like cowork's model picker consume). Pricing's annotated catalog
	// already lives at /v1/pricing/models, so the bare alias would only shadow
	// AI's contract route with a different shape — a regression. Keep pricing
	// strictly under /v1/pricing/*. (Same reasoning the note below records for
	// /v1/plan, /v1/tools, /v1/gpu, /v1/cloud, /v1/subscriptions, /v1/iam —
	// all owned by other subsystems at the top level to avoid collisions.)
	// Live sync trigger — admin only. Network fetch in Go, markup in goja.
	zip.Post(zapp, "/v1/pricing/sync", o.sync)

	logger.Info("pricing mounted",
		"prefix", "/v1/pricing",
		"section_routes", 14, // the fixed plans/infra/tools/gpu/policy sections
		"gated_routes", 6, // models, free, featured, providers, summary, model/:name
		"admin_routes", 3, // GET /v1/admin/pricing/catalog + PATCH models/* + PATCH providers/:name
		"overlay_db", "catalog", // the subsystem; build.go already logs the data dir
		"express", false,
		"goja", true,
		"brand", cloud.Brand(),
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
	// "acme/some-model-1"), matched case-insensitively. It comes from
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

// ListModels returns the whole model catalog — every model the gateway serves,
// Zen and third-party alike — filtered to what the caller's org may see. A
// model an admin has disabled is absent; one in beta appears only for an org
// granted it. A SuperAdmin sees every model, each annotated with its
// enablement state.
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
		// AN ADMIN ASKS ABOUT THE CATALOG, NOT ABOUT THE BUNDLE. The embedded
		// @hanzo/pricing bundle is a stale snapshot of first-party prices; the
		// authority for what exists and whether it is enabled is the overlay,
		// which commerce backs. A model the admin has DISABLED is exactly the
		// case where the two disagree: the bundle stops serving it, so its 404
		// arrives before the gate below can decide, and the one caller who is
		// supposed to still see it — the admin who turned it off — is told it
		// does not exist. VisibleCatalog already keeps disabled entries for
		// admins; it simply never ran.
		//
		// So a 404 from the bundle is not the end of the question. If the overlay
		// knows this id, the admin gets the overlay's answer.
		if status == http.StatusNotFound && callerIsAdmin(ctx) && cat != nil {
			if ov, ok, gerr := cat.Get(ctx, kindModel, in.Name); gerr == nil && ok {
				m := mergeModel(Model{"id": in.Name}, ov.Overrides)
				m["_overlay"] = modelAdminState(ov, true, Overlay{}, false)
				return &m, nil
			}
		}
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
	GateRootData(data, snap, callerOrg(ctx), callerIsAdmin(ctx), callerGrant(ctx))
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
		// hpricing.Pricing() re-reads the EMBEDDED snapshot, so a sync would
		// otherwise discard the commerce overlay and quietly restore the
		// snapshot's first-party prices. The document is assembled the same way
		// in both places, which is the only way the two cannot disagree.
		ts := time.Now().UTC().Format(time.RFC3339)
		m["updated"] = ts
		host.SetGlobal("__PRICING_DATA__", overlay(ctx, m, plog))
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
