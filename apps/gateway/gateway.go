// Package gateway is live control of your API edge: CORS, rate limits, cache
// TTL and allowed methods, changed without a redeploy.
//
// It is the runtime config plane for the cloud edge ("gateway role"), served at
// /v1/gateway. It serves GET/PUT over the SAME
// edge.Store the EdgeCORS/EdgeRateLimit middleware and ScopeRateLimit
// read live, so an operator retunes the CORS allowlist, the pre-auth per-IP flood
// cap, or a tenant's authenticated rate ceiling with NO redeploy — replacing the
// gateway's baked-into-an-image KrakenD config.
//
// TWO IAM-gated scopes (mirrors apps/pricing/enablement.go: global state is
// SuperAdmin-only, self-service is scoped to the validated tenant):
//
//   - PLATFORM policy (CORS origins, per-IP cap + window) — pre-auth edge knobs
//     with no tenant at evaluation time. Writable ONLY by a SuperAdmin
//     (c.IsAdmin() ⟺ owner == admin org). A PUT carrying any platform field is
//     routed to the platform row explicitly (PutPlatform), so it lands correctly
//     even when the SuperAdmin is org-switched to another tenant.
//   - PER-ORG policy (OrgRPM, the authenticated ceiling; CacheTTLSec + CachePaths,
//     the edge-cache TTL; Methods, the accepted-method allowlist) — a tenant's own
//     row of self-service edge config. An org admin writes its own (org from
//     principal.Org, never a raw header); a SuperAdmin may target any tenant with
//     ?org=<slug>.
//
// The store is owned by BuildDeps (deps.GatewayPolicy) and shared; this subsystem
// does not open or close it (serve.go closes it once at shutdown), so there is one
// store, one source of truth. Per-PROJECT rate scoping is NOT duplicated here — it
// remains ScopeRateLimit's commerce-configured domain (per (org,project,service)).
package gateway

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/gateway openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// state is gateway's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store *edge.Store
}

// ops binds the mounted Service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// Mount wires /v1/gateway/config onto app over the shared policy store. The store
// is owned by deps (not a Base dep), so this constructs the Service value directly
// via cloud.NewBase rather than cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("gateway.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("gateway.Mount: nil deps.Logger")
	}
	if deps.GatewayPolicy == nil {
		return fmt.Errorf("gateway.Mount: nil deps.GatewayPolicy")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "gateway"), State: state{store: deps.GatewayPolicy}}

	routes(app, s)

	s.Log.Info("gateway config plane mounted", "prefix", "/v1/gateway")
	return nil
}

// routes registers the gateway config plane as TYPED ops — one registry entry per
// route, from which the REST route, the OpenAPI operation, the MCP tool, the CLI
// command and every generated SDK method follow.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// Bridge FIRST: a typed op receives only a context, so the validated org and the
	// request reach it by being parked there. fiber runs middleware in registration
	// order, so one installed after its leaves never runs. Installed through the
	// SUBSYSTEM's own router, which scopes it to the prefixes this app declares
	// (/v1/gateway) rather than the whole binary. Serve installs the same bridge
	// app-wide; nesting is harmless — the inner one is what the handler sees — and
	// this keeps the ops working wherever the subsystem is mounted, including a test
	// app that never calls Serve.
	app.Use(cloud.Bridge())

	// Declared on the GROUP: the op's path is the prefix composed with the leaf,
	// which is the identity every projection keys on, and cmd/zipdoc resolves the
	// prefix the same way, so the prose below reaches the document and the tool list.
	g := app.Group("/v1/gateway")
	o := ops{s: s}
	zip.Get(g, "/config", o.read)
	zip.Put(g, "/config", o.write)
}

// noArgs is the input of an op that takes none: no body, no query, no path param.
type noArgs struct{}

// caller is the ONE identity seam both config ops ask. It returns the caller's
// VALIDATED org (principal.OrgFrom — never an input field, which is caller-supplied)
// together with the request, because this surface gates on two facts the org alone
// does not carry: SuperAdmin-ness (X-User-IsAdmin) and the ?org=<slug> a SuperAdmin
// targets another tenant with. It fails closed off the HTTP path, where there is no
// principal and no attested admin.
func caller(ctx context.Context) (*zip.Ctx, string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("a validated principal is required")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("a validated principal is required")
	}
	return c, org, nil
}

// target resolves the org a call acts on: the caller's own validated org, or — for
// a SuperAdmin only — the tenant named by ?org=<slug>. The override is read off the
// URL rather than modelled as an input field, because zip binds an input field from
// the BODY too and this route has never accepted an org there.
func target(c *zip.Ctx, org string) string {
	if c.IsAdmin() {
		if q := c.Query("org"); q != "" {
			return q
		}
	}
	return org
}

// Read returns the EFFECTIVE edge policy the caller is subject to: the platform CORS
// allowlist and pre-auth per-IP flood cap in force, plus the caller's own authenticated
// rate ceiling, edge-cache TTLs and accepted-method allowlist. A SuperAdmin may inspect
// a specific tenant's effective policy with ?org=<slug>.
func (o ops) read(ctx context.Context, _ *noArgs) (*edge.Policy, error) {
	c, org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	p := o.s.State.store.Effective(target(c, org))
	return &p, nil
}

// Write updates one policy scope and returns the policy in force after the write.
// A body carrying any PLATFORM field (cors_origins, per_ip_rpm, window_sec) is a
// platform write and requires SuperAdmin; otherwise it is a per-org write (org_rpm,
// cache_ttl_sec, cache_paths, methods) scoped to the caller's own org — or, for a
// SuperAdmin, the tenant named by ?org=<slug>. A body that sets nothing is a 400.
// updated_at and updated_by are server-stamped; a client-supplied value is ignored.
//
// Example: {"org_rpm": 120, "cache_ttl_sec": 30, "methods": ["GET", "POST"]}
func (o ops) write(ctx context.Context, in *edge.Policy) (*edge.Policy, error) {
	c, org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	in.UpdatedBy = c.User() // server-stamped; a client-supplied value is ignored.

	in.Normalize()
	platformWrite := len(in.CORSOrigins) > 0 || in.PerIPRPM > 0 || in.WindowSec > 0
	if platformWrite {
		if !c.IsAdmin() {
			return nil, zip.ErrForbidden("platform policy (cors/per-IP) requires SuperAdmin")
		}
		if err := in.Validate(); err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
		saved, err := o.s.State.store.PutPlatform(ctx, *in)
		if err != nil {
			o.s.Log.Warn("gateway platform policy write failed", "err", err)
			return nil, zip.Errorf(503, "policy store unavailable")
		}
		o.s.Log.Info("gateway platform policy updated", "by", in.UpdatedBy,
			"cors", len(saved.CORSOrigins), "per_ip_rpm", saved.PerIPRPM, "window_sec", saved.WindowSec)
		return &saved, nil
	}

	// Per-org scope: the tenant's OWN edge config — rate ceiling, cache policy,
	// method allowlist. Only the per-org fields are forwarded, so a tenant can never
	// smuggle a platform knob into its own row. Scoped to the caller's org (never a
	// body-supplied org); a SuperAdmin may target a specific tenant with ?org=<slug>.
	orgCfg := edge.Policy{
		OrgRPM:      in.OrgRPM,
		CacheTTLSec: in.CacheTTLSec,
		CachePaths:  in.CachePaths,
		Methods:     in.Methods,
		UpdatedBy:   in.UpdatedBy,
	}
	if orgCfg.OrgRPM <= 0 && orgCfg.CacheTTLSec <= 0 && len(orgCfg.CachePaths) == 0 && len(orgCfg.Methods) == 0 {
		return nil, zip.ErrBadRequest("nothing to set: provide org_rpm/cache_ttl_sec/cache_paths/methods, or cors_origins/per_ip_rpm/window_sec (SuperAdmin)")
	}
	if err := orgCfg.Validate(); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	tgt := target(c, org)
	saved, err := o.s.State.store.Put(ctx, tgt, orgCfg)
	if err != nil {
		o.s.Log.Warn("gateway org policy write failed", "org", tgt, "err", err)
		return nil, zip.Errorf(503, "policy store unavailable")
	}
	o.s.Log.Info("gateway org policy updated", "org", tgt, "by", orgCfg.UpdatedBy,
		"org_rpm", saved.OrgRPM, "cache_ttl_sec", saved.CacheTTLSec, "methods", len(saved.Methods))
	eff := o.s.State.store.Effective(tgt)
	return &eff, nil
}
