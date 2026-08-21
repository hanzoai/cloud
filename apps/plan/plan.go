// Package plan is the plan catalog: every tier you can buy, what it costs, and
// what it grants.
//
// It serves /v1/plan/* — cloud, subscription, blockchain, DNS, GPU and storage
// tiers, the entitlement vocabulary each grants and its JSON Schema, and a resolver
// from a plan id to both. It is the catalog of RECORD; apps/pricing reads the same
// @hanzo/plans source and answers eight of these sections again under /v1/pricing/*.
//
// STRATEGY: wrap, don't rewrite. @hanzo/plans is a Node data package (JSON
// catalog + entitlements.mjs transforms). We do NOT reimplement the entitlement
// vocabulary in Go and we do NOT copy the catalog into cloud. Instead:
//
//   - github.com/hanzoai/plans (the service repo's Go embed module) ships
//     goja/bundle.js — the ESM-free port of entitlements.mjs + the /v1/plan
//     route table — plus the embedded *.json catalog (plans.Data()).
//   - This wrapper loads that bundle into a goja runtime (apps/goja),
//     injects the catalog as globalThis.__PLANS_DATA__, and declares one TYPED op
//     per address (ops.go) that calls globalThis.handle({route, params, tenant}).
//     The entitlement transforms (fromLegacy/toLicenseFeatures/resolvePlan) run in
//     goja — real JS, not a Go reimplementation.
//
// The plans data is read-only public-catalog content; there are no secrets
// here. The licensing SIGNER/fingerprint that consumes toLicenseFeatures stays
// in hanzoai/licensing. This wrapper is pure glue.
//
// IAM gating + X-Org-Id tenant scope: every /v1/plan route threads the
// VALIDATED org into the bundle as the tenant, so a reseller org
// (tenant_id != "hanzo") sees its own catalog overrides. A typed op receives only
// a context, so that org arrives on the context (cloud.Bridge parks it,
// ops.go/catalogTenant reads it) and is never an In field — an In field is
// caller-supplied, and a tenant read from one would hand any caller any
// reseller's catalog. The plan catalog is readable by any authenticated caller;
// no admin scope is required for reads.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	hplans "github.com/hanzoai/plans"
	"github.com/zap-proto/zip"
)

// host is the process-global goja host for the plans bundle. nil before Mount.
var host *goja.Host

// Mount registers the /v1/plan/* surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("plan.Mount: nil app")
	}
	logger := luxlog.Default()
	if logger == nil {
		return fmt.Errorf("plan.Mount: nil luxlog.Default()")
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

	// The typed ops. zip.Get[In, Out] is ONE registry entry with N projections —
	// the REST route, the OpenAPI schema and prose, the MCP tool, the CLI command
	// and the generated SDK method — so each op is declared exactly once here and
	// every consumer follows from it. See ops.go.
	//
	// They are declared on the APP with absolute paths rather than on a group,
	// because one of them IS the prefix: GET /v1/plan has no leaf, and a group
	// cannot express it — zip.Get(g, "") composes to "/v1/plan/", a different
	// route. A group for fourteen plus an app-level exception for one is two
	// idioms; one absolute address per op is one, and it is the form apps/pricing
	// already uses for the same reason.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("plan.Mount: router is not backed by a zip app — typed ops have no registry to declare into")
	}
	// The composer owns cloud.Bridge: the fused host installs it once at its root
	// and the plugin constructor does the same for a plugin program, so no
	// subsystem installs it.

	o := ops{log: logger}

	// Native health probe — no JS, no auth, answers while the bundle is degraded.
	zip.Get(zapp, "/v1/plan/health", o.health)

	// The catalog sections. Each relays one @hanzo/plans bundle route; the tenant
	// is the validated org, so a reseller org reads its own catalog (ops.go,
	// catalogTenant).
	zip.Get(zapp, "/v1/plan", o.listPlans)
	zip.Get(zapp, "/v1/plan/subscriptions", o.listSubscriptions)
	zip.Get(zapp, "/v1/plan/blockchain", o.listBlockchain)
	zip.Get(zapp, "/v1/plan/dns", o.listDNS)
	zip.Get(zapp, "/v1/plan/gpu", o.listGPU)
	zip.Get(zapp, "/v1/plan/regions", o.listRegions)
	zip.Get(zapp, "/v1/plan/storage", o.getStorage)
	zip.Get(zapp, "/v1/plan/tools", o.listTools)
	zip.Get(zapp, "/v1/plan/policy", o.getPolicy)
	zip.Get(zapp, "/v1/plan/schema", o.getSchemas)
	zip.Get(zapp, "/v1/plan/vocab", o.getVocab)

	// Entitlement resolution — the data contract, end to end, in goja.
	zip.Get(zapp, "/v1/plan/resolve/:id", o.resolve)
	zip.Get(zapp, "/v1/plan/entitlements/:id", o.getEntitlements)

	logger.Info("plans mounted",
		"prefix", "/v1/plan",
		"routes", 15,
		"typed", 15,
		"brand", deps.Brand,
	)
	return nil
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
// host — the SAME route /v1/plan/entitlements/:id serves — and returns the parsed
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

// Shutdown drops the goja host. Idempotent.
func Shutdown(context.Context) error {
	if host == nil {
		return nil
	}
	err := host.Close()
	host = nil
	return err
}
