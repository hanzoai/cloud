package plan

// The typed-op seam for the /v1/plan catalog surface.
//
// A typed op (zip.Get[In, Out]) is ONE registry entry with N projections — the
// REST route, the OpenAPI operation's schema AND prose, the MCP tool, the CLI
// command and the generated SDK method all follow from it. An untyped route gets
// a route and a bare operation (method, path, product) and nothing else: no
// schema, no prose, no MCP tool, no CLI command, no SDK method. All fifteen
// operations on this surface were untyped, so /v1/plan published fifteen
// addresses and said nothing whatsoever about any of them.
//
// They were typeable all along. The premise that held them back — "a typed op
// cannot proxy the bundle's bytes verbatim" — is false here for the same reason
// it was false for apps/pricing: apps/goja already re-marshals the bundle's
// answer with Go's encoding/json before any handler sees it
// (Host.DispatchWith, `json.Marshal(m["body"])`), so what the raw pass-through
// wrote was never the JS engine's bytes, it was Go's, with map keys sorted. Each
// Out below therefore either declares its keys IN THAT SORTED ORDER or carries
// the value as json.RawMessage, which encoding/json re-emits verbatim — and
// wire_test.go proves byte-equality route by route against the live router
// rather than asserting it here.
//
// WHY json.RawMessage AND NOT map[string]any. @hanzo/plans owns the shape of a
// plan, a tier, a region and a JSON-Schema document; a Go struct claiming to know
// one would be a second, staler source that silently drops every field the
// catalog adds. The opaque carrier has to be honest about that AND about what it
// is: zip v1.18.11's schemaOf asks whether a type marshals itself before it asks
// what it is made of, so json.RawMessage publishes `{}` — "any JSON", the only
// true thing to say — while map[string]any falls through to
// `additionalProperties: {"type": "object"}`, which asserts every value is an
// object and is refuted by the first `"priceMonthly": 20` in the catalog. A
// false schema is worse than a thin one.
//
// TWO RESIDUAL DELTAS, recorded rather than hidden, both pinned by wire_test.go
// so neither can move again unnoticed:
//
//   - Content-Type. The raw pass-through set a bare "application/json"; the typed
//     writer sets "application/json; charset=utf-8" — which is what every other
//     answer on this surface, the health probe and every zip error alike, already
//     sent. JSON is UTF-8 by definition (RFC 8259), and `charset` is not a
//     registered parameter of application/json, so no parser reads it.
//   - A NON-200 answer's body gains zip's `status` field. The bundle answers
//     `{"error": "plan not found: x"}`; a typed op can only refuse by RETURNING an
//     error, which zip's one errorHandler renders as `{"status": 404, "error":
//     "plan not found: x"}`. The status is the bundle's and the message is the
//     bundle's, byte for byte, under the same key — dispatchErr is what carries
//     both — and the shape is the one every other typed route in the fleet
//     already answers. This is the same trade main already took for
//     GET /v1/pricing/model/{name}, whose 404 is equally first-class.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ops binds the typed ops to a receiver. A zip.TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the subsystem — so
// what an op needs beyond its input arrives as a receiver, and every op is a
// method value (o.listPlans). That is also the only bound form cmd/zipdoc can
// lift prose from: a closure returned by a helper is a call expression with
// nothing to read, which is why the fifteen one-line methods below are methods
// over one generic helper rather than one closure factory called fifteen times.
//
// log is the subsystem logger Mount built. The raw handlers used the REQUEST
// logger (c.Log()); a typed op has no request, and the failure logged here — the
// goja host — is the subsystem's, not the caller's.
type ops struct{ log luxlog.Logger }

// planNoInput is the In of an op that takes nothing off the wire: no path
// segment, no query, no body. Its tenant comes from the context, never from a
// field — see catalogTenant.
type planNoInput struct{}

// planRef addresses one plan in the catalog.
type planRef struct {
	// ID is the plan's catalog id or slug — "pro", "team", "world-enterprise",
	// "rpc-growth". Both are matched, so a slug resolves the plan it names.
	ID string `json:"id"`
}

// ---- the shapes ----
//
// Every catalog value below is opaque on purpose. @hanzo/plans owns the shape of
// a plan, a tier, a region and an entitlement block, and it is the single source
// of truth for them; a Go struct restating one would drop any field the catalog
// adds, silently, on a read surface whose whole job is to relay the catalog.

// planList is a plan list — the shape five of the catalog routes answer with
// (the cloud plans, the subscription ladder, the blockchain/RPC plans and the DNS
// plans all publish the same envelope).
type planList struct {
	// Plans are the plans in this section, each an opaque object exactly as the
	// @hanzo/plans catalog emits it — typically id, name, description,
	// priceMonthly, category, a feature list, a limits block and a price_ref.
	Plans []json.RawMessage `json:"plans"`
}

// planTierList is the GPU tier list.
type planTierList struct {
	// Tiers are the rentable GPU configurations, each an opaque object exactly as
	// the catalog emits it — typically id, name, GPU count and model, VRAM, vCPUs,
	// host memory and hourly price.
	Tiers []json.RawMessage `json:"tiers"`
}

// planRegionList is the region directory cloud plans are placed in.
type planRegionList struct {
	// Regions are the regions cloud capacity is offered in, each an opaque object
	// exactly as the catalog emits it — typically id, name, location and flag.
	Regions []json.RawMessage `json:"regions"`
}

// planToolList is the per-use tool price list.
type planToolList struct {
	// Tools are the metered tools, each an opaque object exactly as the catalog
	// emits it — typically name, billing unit and price.
	Tools []json.RawMessage `json:"tools"`
}

// planDoc is a whole catalog document whose keys are @hanzo/plans' own — the
// storage price block, the pricing policy. Its values are relayed verbatim, so
// the document says what is true of them and no more: an object of arbitrary
// JSON.
type planDoc map[string]json.RawMessage

// planSchemas carries the two JSON Schema documents that define the vocabulary
// this surface speaks.
type planSchemas struct {
	// Entitlements is entitlements.schema.json — the JSON Schema every
	// entitlement key is declared in, including its type, unit and enum.
	Entitlements json.RawMessage `json:"entitlements"`
	// Plan is plan.schema.json — the JSON Schema a catalog plan record conforms
	// to.
	Plan json.RawMessage `json:"plan"`
}

// planVocab is the entitlement key vocabulary, derived from
// entitlements.schema.json rather than restated beside it.
type planVocab struct {
	// EngineFeatures are the inference-engine capabilities a license can grant:
	// inference, embeddings, rerank, training, vision, audio, tools.
	EngineFeatures []string `json:"engine_features"`
	// Keys maps every entitlement key to its descriptor — key, namespace, JSON
	// type(s), nullability, unit, enum and title, as the schema declares them.
	Keys map[string]json.RawMessage `json:"keys"`
	// Namespaces are the entitlement key namespaces: the prefix before the dot in
	// "ai.tokens_per_min".
	Namespaces []string `json:"namespaces"`
}

// planResolution is a plan resolved to the values every consumer of the catalog
// needs at once.
type planResolution struct {
	// Entitlements is the canonical namespaced entitlement block derived from the
	// plan's limits and addons — keys like "ai.tokens_per_min" and
	// "world.api_rate_limit", where -1 means unlimited.
	Entitlements json.RawMessage `json:"entitlements"`
	// ID is the plan's catalog id.
	ID string `json:"id"`
	// LicenseFeatures is the flat, sorted feature list a signed license carries,
	// derived from the entitlements — "ai.premium", "licensing.product:team".
	LicenseFeatures []string `json:"license_features"`
	// PriceRef is the plan's billing reference — currency, the recurring monthly
	// and annual amounts, whether it prices per seat, its Stripe lookup key and
	// its metered components.
	PriceRef json.RawMessage `json:"price_ref"`
	// TenantID is the catalog the record came from: "hanzo" for the canonical
	// catalog, a reseller org for that reseller's override.
	TenantID string `json:"tenant_id"`
}

// planEntitlements is the entitlement half of a resolution, for a caller that
// needs what a tier grants and not what it costs.
type planEntitlements struct {
	// Entitlements is the canonical namespaced entitlement block derived from the
	// plan's limits and addons, where -1 means unlimited.
	Entitlements json.RawMessage `json:"entitlements"`
	// ID is the plan id or slug that was resolved, as it was requested.
	ID string `json:"id"`
	// LicenseFeatures is the flat, sorted feature list a signed license carries,
	// derived from the entitlements.
	LicenseFeatures []string `json:"license_features"`
}

// planHealth is the subsystem's liveness answer.
type planHealth struct {
	// Service names the subsystem that answered.
	Service string `json:"service"`
	// Status is "ok" whenever this subsystem is mounted.
	Status string `json:"status"`
}

// ---- the one read every op shares ----

// catalogTenant is the catalog a typed op reads: the VALIDATED org the identity
// boundary asserted and cloud.Bridge parked on the context, so a reseller org
// sees its own overrides. It is NEVER an In field — an In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself, and here it would hand any caller any reseller's
// catalog by typing its name in the URL.
//
// The public "hanzo" catalog is the answer for an anonymous caller AND for a
// forged X-Org-Id with no validated principal behind it (principal.OrgFrom parks
// nothing for those), which is exactly what the raw handlers did through
// principal.Org. The plan catalog is a public read surface; falling back to the
// canonical catalog is the fail-closed answer, never another reseller's.
func catalogTenant(ctx context.Context) string {
	if org, ok := principal.OrgFrom(ctx); ok {
		return org
	}
	return "hanzo"
}

// routeOf runs one bundle route on the shared goja host and decodes its answer
// into the op's Out. It is the ONE read all fifteen ops below share.
//
// It is a package-level function rather than a method because a Go method cannot
// take a type parameter, and the routes answer six different shapes.
//
// Every status the raw pass-through wrote, this writes: 503 when the subsystem is
// not mounted, 500 when goja itself fails, and otherwise the BUNDLE's own status
// carrying the BUNDLE's own message (dispatchErr).
func routeOf[T any](ctx context.Context, o ops, route string, params map[string]string) (*T, error) {
	if host == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "plans not initialised")
	}
	resp, err := host.Dispatch(ctx, goja.Request{
		Route:  route,
		Params: params,
		Tenant: catalogTenant(ctx),
	})
	if err != nil {
		o.log.Error("plans dispatch failed", "route", route, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "plans dispatch failed")
	}
	if resp.Status != http.StatusOK {
		return nil, dispatchErr(resp.Status, resp.Body) // the bundle's own status and message.
	}
	var out T
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "plans catalog shape not recognised")
	}
	return &out, nil
}

// dispatchErr turns a non-200 answer from the @hanzo/plans bundle into the error
// a typed op returns: the bundle's OWN status, carrying the bundle's own message
// under the bundle's own key. The raw handlers wrote that body through verbatim;
// an op states it as an error, which is the one path a typed op's refusal can
// take.
func dispatchErr(status int, body json.RawMessage) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return zip.Errorf(status, "%s", e.Error)
	}
	return zip.Errorf(status, "plans catalog unavailable")
}

// ---- the catalog sections ----

// ListCloudPlans returns the Hanzo cloud plan catalog: every cloud tier with its
// price, included capacity, limits and feature list, scoped to the caller's
// catalog. A reseller org sees its own overrides in place of the canonical
// records it has replaced, and the canonical record for every tier it has not.
func (o ops) listPlans(ctx context.Context, _ *planNoInput) (*planList, error) {
	return routeOf[planList](ctx, o, "plans", nil)
}

// ListSubscriptionPlans returns the subscription ladder — the personal and team
// tiers a customer buys to use the cloud, each with its monthly and annual price,
// seat rules, limits and billing reference. Scoped to the caller's catalog.
func (o ops) listSubscriptions(ctx context.Context, _ *planNoInput) (*planList, error) {
	return routeOf[planList](ctx, o, "subscriptions", nil)
}

// ListBlockchainPlans returns the blockchain RPC plan catalog: the tiers metered
// in monthly compute units, with their prices, limits and overage terms. It is
// the canonical catalog for every caller — these plans carry no reseller
// overrides.
func (o ops) listBlockchain(ctx context.Context, _ *planNoInput) (*planList, error) {
	return routeOf[planList](ctx, o, "blockchain", nil)
}

// ListDNSPlans returns the DNS plan catalog: the tiers priced on zones, records
// per zone and queries per day. It is the canonical catalog for every caller —
// these plans carry no reseller overrides.
func (o ops) listDNS(ctx context.Context, _ *planNoInput) (*planList, error) {
	return routeOf[planList](ctx, o, "dns", nil)
}

// ListGPUTiers returns the rentable GPU configurations, each with its accelerator
// count and model, VRAM, vCPUs, host memory and hourly price.
func (o ops) listGPU(ctx context.Context, _ *planNoInput) (*planTierList, error) {
	return routeOf[planTierList](ctx, o, "gpu", nil)
}

// ListRegions returns the regions cloud capacity is offered in, each with its
// display name and physical location.
func (o ops) listRegions(ctx context.Context, _ *planNoInput) (*planRegionList, error) {
	return routeOf[planRegionList](ctx, o, "regions", nil)
}

// GetStoragePricing returns the block-storage price block: the price per GB per
// month and the volume size bounds a cloud plan may attach.
func (o ops) getStorage(ctx context.Context, _ *planNoInput) (*planDoc, error) {
	return routeOf[planDoc](ctx, o, "storage", nil)
}

// ListToolPrices returns the per-use price of every metered tool — web search,
// code interpreter, image generation, speech — each with the unit it is billed
// in.
func (o ops) listTools(ctx context.Context, _ *planNoInput) (*planToolList, error) {
	return routeOf[planToolList](ctx, o, "tools", nil)
}

// GetPricingPolicy returns the published pricing policy: whether pricing is
// transparent, the revenue-sharing terms (idle compute resale and the open-source
// share) and the principles the catalog is priced by.
func (o ops) getPolicy(ctx context.Context, _ *planNoInput) (*planDoc, error) {
	return routeOf[planDoc](ctx, o, "policy", nil)
}

// GetPlanSchemas returns the two JSON Schema documents this surface speaks:
// entitlements.schema.json, which declares every entitlement key with its type,
// unit and enum, and plan.schema.json, which a catalog plan record conforms to.
func (o ops) getSchemas(ctx context.Context, _ *planNoInput) (*planSchemas, error) {
	return routeOf[planSchemas](ctx, o, "schema", nil)
}

// GetEntitlementVocabulary returns the entitlement key vocabulary: every key with
// its namespace, JSON type, nullability, unit, enum and title, the list of
// namespaces, and the engine features a license can grant. It is derived from
// entitlements.schema.json on every call, so it cannot fall behind the schema.
func (o ops) getVocab(ctx context.Context, _ *planNoInput) (*planVocab, error) {
	return routeOf[planVocab](ctx, o, "vocab", nil)
}

// ---- resolution ----

// ResolvePlan resolves one plan to everything a consumer of the catalog needs at
// once: its canonical entitlement block, the flat license-feature list a signed
// license carries, its billing reference, and the catalog it came from. The id
// may be the plan's id or its slug, and it is resolved against the caller's
// catalog, so a reseller's override wins over the canonical record. An id no
// catalog holds answers 404.
//
// Example: {"id": "pro"}
func (o ops) resolve(ctx context.Context, in *planRef) (*planResolution, error) {
	return routeOf[planResolution](ctx, o, "resolve", map[string]string{"id": in.ID})
}

// GetPlanEntitlements returns what one plan GRANTS and not what it costs: the
// canonical namespaced entitlement block and the flat license-feature list
// derived from it. It is the entitlement half of ResolvePlan, over the same
// catalog and the same 404 for an id no catalog holds — the read a licensing or
// quota gate makes.
//
// Example: {"id": "team"}
func (o ops) getEntitlements(ctx context.Context, in *planRef) (*planEntitlements, error) {
	return routeOf[planEntitlements](ctx, o, "entitlements", map[string]string{"id": in.ID})
}

// Health reports that the plans subsystem is mounted and serving. It answers from
// the process itself and consults neither the catalog bundle nor the goja host,
// so it stays "ok" while either is degraded.
//
// Response: {"service":"plans","status":"ok"}
func (o ops) health(context.Context, *planNoInput) (*planHealth, error) {
	return &planHealth{Service: "plans", Status: "ok"}, nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/plan openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
