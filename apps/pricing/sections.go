package pricing

// The catalog's FIXED SECTIONS — plan, infrastructure, tool, GPU and policy
// pricing — as typed ops.
//
// These fifteen addresses are the half of the pricing surface that carries no
// model or provider identity, so the enablement overlay has nothing to gate:
// they hand back a section of the @hanzo/pricing / @hanzo/plans catalog as the
// source wrote it. That is why they live in their own file, apart from the model
// catalog plane in pricing.go, which is gated on every read.
//
// They used to be untyped pass-throughs on the grounds that a typed op cannot
// proxy the bundle's bytes verbatim. It can: apps/goja already re-marshals the
// bundle's answer with Go's encoding/json (see Host.DispatchWith,
// `json.Marshal(m["body"])`), so what the raw route wrote was never the JS
// engine's own bytes — it was Go's, keys sorted. Decoding those bytes into an
// Out and re-marshalling them is therefore byte-for-byte identical, which
// sections_wire_test.go proves against the live router for every one of the
// fifteen rather than asserting it here in prose.
//
// The bundle's STATUS is still the bundle's: a section the catalog does not hold
// answers 503 (see goja/bundle.js), and sectionOf returns that status as the
// error it is, carrying the bundle's own message — the same way the gated
// catalog ops already do.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/zap-proto/zip"
)

// ---- the shapes ----
//
// Every value below is opaque on purpose: @hanzo/pricing and @hanzo/plans own
// the shape of a plan, a tier, a preset and a region, and a Go struct claiming
// to know it would be a second, staler source that silently drops any field the
// catalog adds. So each section is either one pricingBlob or a single-key
// envelope over a list of them — which is exactly what the source emits, and
// carries every field it emits.

// pricingPlanList is a plan list: the shape six of the section routes answer
// with (cloud, subscription, blockchain, IAM, Base and PaaS plans).
type pricingPlanList struct {
	// Plans are the plans in this section, each an opaque object exactly as the
	// pricing source emits it — typically id, name, description, price and a
	// feature list.
	Plans []pricingBlob `json:"plans"`
}

// pricingPresetList is the named-size list of a compute section.
type pricingPresetList struct {
	// Presets are the named compute sizes, each an opaque object exactly as the
	// pricing source emits it — typically id, name, provider slug, vCPU, memory,
	// disk and price.
	Presets []pricingBlob `json:"presets"`
}

// pricingRegionList is the region directory of the cloud section.
type pricingRegionList struct {
	// Regions are the regions cloud instances can be placed in, each an opaque
	// object exactly as the pricing source emits it — typically id, name and
	// location.
	Regions []pricingBlob `json:"regions"`
}

// pricingToolList is the per-use tool price list.
type pricingToolList struct {
	// Tools are the metered tools, each an opaque object exactly as the pricing
	// source emits it — typically name, billing unit and price.
	Tools []pricingBlob `json:"tools"`
}

// pricingTierList is the GPU tier list.
type pricingTierList struct {
	// Tiers are the rentable GPU configurations, each an opaque object exactly
	// as the pricing source emits it — typically id, name, accelerator count and
	// model, VRAM, vCPU, memory and hourly price.
	Tiers []pricingBlob `json:"tiers"`
}

// sectionOf runs one section route on the pricing bundle and decodes its answer
// into the op's Out. It is the ONE read all fifteen ops below share.
//
// It is a package-level function rather than a method because a Go method cannot
// take a type parameter, and each section answers a different shape. Every op is
// still a bound METHOD over it — the only form cmd/zipdoc can lift prose from,
// and the reason there are fifteen one-line methods here instead of one helper
// called fifteen times.
//
// A non-200 from the bundle is returned as the error it is, carrying the
// BUNDLE's status and the BUNDLE's message (dispatchErr), so a section the
// catalog does not hold still answers the 503 it always answered.
func sectionOf[T any](ctx context.Context, o ops, route string) (*T, error) {
	status, body, err := rawDispatch(ctx, route, nil)
	if err != nil {
		o.log.Error("pricing dispatch failed", "route", route, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing dispatch failed")
	}
	if status != http.StatusOK {
		return nil, dispatchErr(status, body) // the bundle's own status and message.
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "pricing catalog shape not recognised")
	}
	return &out, nil
}

// mountSections declares the fixed-section ops. Called from Mount with the
// subsystem's zip app, so each op's absolute address is its identity in every
// projection — the REST route, the OpenAPI operation, the MCP tool, the CLI
// command and the generated SDK method.
func mountSections(zapp *zip.App, o ops) {
	zip.Get(zapp, "/v1/pricing/compute", o.compute)
	zip.Get(zapp, "/v1/pricing/compute/presets", o.computePresets)
	zip.Get(zapp, "/v1/pricing/cloud", o.cloud)
	zip.Get(zapp, "/v1/pricing/cloud/plans", o.cloudPlans)
	zip.Get(zapp, "/v1/pricing/cloud/regions", o.cloudRegions)
	zip.Get(zapp, "/v1/pricing/cloud/storage", o.cloudStorage)
	zip.Get(zapp, "/v1/pricing/subscriptions", o.subscriptions)
	zip.Get(zapp, "/v1/pricing/blockchain", o.blockchain)
	zip.Get(zapp, "/v1/pricing/iam", o.iam)
	zip.Get(zapp, "/v1/pricing/base", o.base)
	zip.Get(zapp, "/v1/pricing/paas", o.paas)
	zip.Get(zapp, "/v1/pricing/policy", o.policy)
	zip.Get(zapp, "/v1/pricing/tools", o.tools)
	zip.Get(zapp, "/v1/pricing/gpu", o.gpu)
}

// ---- infrastructure ----

// GetComputePricing returns the compute section of the catalog: the cloud
// provider and region the prices are quoted for, the monthly markup applied to
// them, the full instance-size tier list and the named presets. It is the
// whole section as the pricing source records it, un-gated — no model or
// provider identity appears in it.
func (o ops) compute(ctx context.Context, _ *pricingNoInput) (*pricingBlob, error) {
	return sectionOf[pricingBlob](ctx, o, "compute")
}

// GetComputePresets returns just the named compute sizes — the short,
// human-labelled list ("Starter", "Pro") a size picker renders, each carrying
// its provider slug, vCPU, memory, disk and price. It is the presets of the
// compute section on their own, for a caller that does not need the full tier
// table.
func (o ops) computePresets(ctx context.Context, _ *pricingNoInput) (*pricingPresetList, error) {
	return sectionOf[pricingPresetList](ctx, o, "compute/presets")
}

// ---- cloud ----

// GetCloudPricing returns the public cloud section of the catalog in one
// document: its instance plans, its regions and its block-storage prices. The
// section's internal half — the provider costs Hanzo pays and the plan-to-
// provider routing table — is stripped before it is served, so this is what a
// customer may see and nothing more.
func (o ops) cloud(ctx context.Context, _ *pricingNoInput) (*pricingBlob, error) {
	return sectionOf[pricingBlob](ctx, o, "cloud")
}

// GetCloudPlans returns just the cloud instance plans — each with its vCPU,
// memory, disk, CPU type, VM allowance, feature list and monthly and hourly
// price. It is the plans of the cloud section on their own.
func (o ops) cloudPlans(ctx context.Context, _ *pricingNoInput) (*pricingPlanList, error) {
	return sectionOf[pricingPlanList](ctx, o, "cloud/plans")
}

// GetCloudRegions returns the regions a cloud instance can be placed in, each
// with its id, display name and physical location. It is the regions of the
// cloud section on their own.
func (o ops) cloudRegions(ctx context.Context, _ *pricingNoInput) (*pricingRegionList, error) {
	return sectionOf[pricingRegionList](ctx, o, "cloud/regions")
}

// GetCloudStoragePricing returns the block-storage prices of the cloud
// section: the per-GB monthly rate and the volume size bounds a caller may ask
// for.
func (o ops) cloudStorage(ctx context.Context, _ *pricingNoInput) (*pricingBlob, error) {
	return sectionOf[pricingBlob](ctx, o, "cloud/storage")
}

// ---- plans ----

// ListSubscriptionPlans returns the API subscription plans — the account-level
// tiers a customer subscribes to, each with its monthly and annual price,
// included credit, rate limits and feature list.
func (o ops) subscriptions(ctx context.Context, _ *pricingNoInput) (*pricingPlanList, error) {
	return sectionOf[pricingPlanList](ctx, o, "subscriptions")
}

// ListBlockchainPlans returns the blockchain access plans — the RPC and node
// tiers, each with its monthly price, compute-unit allowance and feature list.
func (o ops) blockchain(ctx context.Context, _ *pricingNoInput) (*pricingPlanList, error) {
	return sectionOf[pricingPlanList](ctx, o, "blockchain")
}

// ListIAMPlans returns the identity plans — the Hanzo IAM tiers, each with its
// monthly and annual price, monthly-active-user allowance and feature list.
func (o ops) iam(ctx context.Context, _ *pricingNoInput) (*pricingPlanList, error) {
	return sectionOf[pricingPlanList](ctx, o, "iam")
}

// ListBasePlans returns the Hanzo Base plans — the managed-instance tiers,
// each with its monthly and annual price, storage and request allowances and
// feature list.
func (o ops) base(ctx context.Context, _ *pricingNoInput) (*pricingPlanList, error) {
	return sectionOf[pricingPlanList](ctx, o, "base")
}

// ListPaaSPlans returns the application-hosting plans — the deploy-and-host
// tiers, each with its monthly and annual price, app and memory allowances and
// feature list.
func (o ops) paas(ctx context.Context, _ *pricingNoInput) (*pricingPlanList, error) {
	return sectionOf[pricingPlanList](ctx, o, "paas")
}

// ---- metered surfaces ----

// ListToolPrices returns the per-use tool prices — web search, code
// interpreter, file storage, image generation, speech-to-text and
// text-to-speech — each with the unit it is billed by and its price in that
// unit.
func (o ops) tools(ctx context.Context, _ *pricingNoInput) (*pricingToolList, error) {
	return sectionOf[pricingToolList](ctx, o, "tools")
}

// ListGPUTiers returns the rentable GPU configurations, each with its
// accelerator count and model, VRAM, vCPU, host memory and hourly price.
func (o ops) gpu(ctx context.Context, _ *pricingNoInput) (*pricingTierList, error) {
	return sectionOf[pricingTierList](ctx, o, "gpu")
}

// ---- policy ----

// GetPricingPolicy returns the pricing policy document: the revenue-sharing
// terms (the idle-resale share and the open-source share, each with its
// percentage and who is eligible) and the commitments Hanzo makes about how it
// bills — no hidden fees, no egress charges, no surprise bills.
func (o ops) policy(ctx context.Context, _ *pricingNoInput) (*pricingBlob, error) {
	return sectionOf[pricingBlob](ctx, o, "policy")
}
