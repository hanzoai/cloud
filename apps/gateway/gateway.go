// Package gateway is the /v1/gateway subsystem: the RUNTIME config plane for
// the cloud edge ("gateway role"). It serves GET/PUT over the SAME
// edge.Store the EdgeCORS/EdgeRateLimit middleware and ScopeRateLimit
// read live, so an operator retunes the CORS allowlist, the pre-auth per-IP flood
// cap, or a tenant's authenticated rate ceiling with NO redeploy — replacing the
// gateway's baked-into-an-image KrakenD config.
//
// TWO IAM-gated scopes (mirrors clients/pricing/enablement.go: global state is
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

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// state is gateway's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store *edge.Store
}

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

// routes registers the gateway config plane. Both verbs are zip TYPED ops, so the
// one declaration the router reads is the one the OpenAPI document, the MCP tool
// list and the CLI read too.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: a typed op receives only its context and its decoded input,
	// so the validated org and the request itself cross here — and fiber runs
	// middleware in registration order, so one installed after these leaves would
	// never run.
	app.Group("/v1/gateway").Use(cloud.Bridge())
	zip.Get(z, "/v1/gateway/config", o.get)
	zip.Put(z, "/v1/gateway/config", o.put)
}

// ops binds the policy store to the typed handlers: a TypedHandler takes only a
// context and its input, so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

// scope names the tenant a read is answered for. The org is NOT the caller's own
// tenant key — that is read from the validated principal and can never be
// asserted by a caller — it is the SuperAdmin-only override that lets an operator
// inspect another tenant's row; it is ignored for everyone else.
type scope struct {
	// Org selects which tenant's effective policy to return, SuperAdmin only.
	// Empty — and, for every other caller, always — means the caller's own org.
	Org string `json:"org"`
}

// get returns the effective edge policy the caller is subject to.
//
// That is the platform CORS allowlist + per-IP flood cap in force, fused with
// the caller's own OrgRPM ceiling, cache TTL and method allowlist.
// A SuperAdmin may inspect a specific tenant with ?org=<slug>.
//
// Example: {"org": "acme"}
// Response: {"cors_origins": ["https://console.hanzo.ai"], "per_ip_rpm": 600, "window_sec": 60, "org_rpm": 1200}
func (o ops) get(ctx context.Context, in *scope) (*edge.Policy, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	if c.IsAdmin() && in.Org != "" {
		org = in.Org
	}
	p := o.s.State.store.Effective(org)
	return &p, nil
}

// put writes one edge policy scope and returns the policy now in force.
//
// A body carrying any PLATFORM field (cors_origins, per_ip_rpm, window_sec) is a
// platform write and requires SuperAdmin; otherwise it is a per-org write
// (org_rpm, cache_ttl_sec, cache_paths, methods) scoped to the caller's own org
// (or, for a SuperAdmin, ?org=<slug>).
// Metadata is server-stamped, never client-supplied, and every field is
// bounds-checked before it is persisted.
//
// Example: {"org_rpm": 1200, "cache_ttl_sec": 30, "methods": ["GET", "POST"]}
func (o ops) put(ctx context.Context, in *edge.Policy) (*edge.Policy, error) {
	s := o.s
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
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
		saved, err := s.State.store.PutPlatform(ctx, *in)
		if err != nil {
			s.Log.Warn("gateway platform policy write failed", "err", err)
			return nil, zip.Errorf(503, "policy store unavailable")
		}
		s.Log.Info("gateway platform policy updated", "by", in.UpdatedBy,
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
	target := org
	if c.IsAdmin() {
		// The SuperAdmin write target rides the URL, not the body — the same
		// ?org=<slug> the read takes. zip declares query parameters only on the
		// bodyless methods, so it is read off the request here rather than named
		// as an input field the document would then describe as part of the body.
		if q := c.Query("org"); q != "" {
			target = q // SuperAdmin sets a specific tenant's config.
		}
	}
	saved, err := s.State.store.Put(ctx, target, orgCfg)
	if err != nil {
		s.Log.Warn("gateway org policy write failed", "org", target, "err", err)
		return nil, zip.Errorf(503, "policy store unavailable")
	}
	s.Log.Info("gateway org policy updated", "org", target, "by", orgCfg.UpdatedBy,
		"org_rpm", saved.OrgRPM, "cache_ttl_sec", saved.CacheTTLSec, "methods", len(saved.Methods))
	eff := s.State.store.Effective(target)
	return &eff, nil
}
