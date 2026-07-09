// Package gatewaysvc is the /v1/gateway subsystem: the RUNTIME config plane for
// the cloud edge ("gateway role"). It serves GET/PUT over the SAME
// gatewaypolicy.Store the EdgeCORS/EdgeRateLimit middleware and ScopeRateLimit
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
//   - PER-ORG policy (OrgRPM, the authenticated ceiling) — a tenant's own row. An
//     org admin writes its own (org from principal.Tenant, never a raw header); a
//     SuperAdmin may target any tenant with ?org=<slug>.
//
// The store is owned by BuildDeps (deps.GatewayPolicy) and shared; this subsystem
// does not open or close it (serve.go closes it once at shutdown), so there is one
// store, one source of truth. Per-PROJECT rate scoping is NOT duplicated here — it
// remains ScopeRateLimit's commerce-configured domain (per (org,project,service)).
package gatewaysvc

import (
	"encoding/json"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/gatewaypolicy"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type svc struct {
	store *gatewaypolicy.Store
	log   luxlog.Logger
}

var mounted *svc

// Mount wires /v1/gateway/config onto app over the shared policy store.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("gatewaysvc.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("gatewaysvc.Mount: nil deps.Logger")
	}
	if deps.GatewayPolicy == nil {
		return fmt.Errorf("gatewaysvc.Mount: nil deps.GatewayPolicy")
	}
	s := &svc{store: deps.GatewayPolicy, log: deps.Logger.New("subsystem", "gateway")}
	mounted = s

	app.Get("/v1/gateway/config", s.get)
	app.Put("/v1/gateway/config", s.put)

	s.log.Info("gateway config plane mounted", "prefix", "/v1/gateway")
	return nil
}

func init() {
	// Order 139: after settings (138), before the AI /v1/* catch-all (150), so the
	// explicit /v1/gateway/* routes register ahead of the wildcard.
	cloud.Register("gateway", 139, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("gatewaysvc.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// get returns the EFFECTIVE edge policy the caller is subject to: the platform
// CORS + per-IP cap in force, plus the caller's own OrgRPM ceiling. A SuperAdmin
// may inspect a specific tenant with ?org=<slug>.
func (s *svc) get(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if c.IsAdmin() {
		if q := c.Query("org"); q != "" {
			org = q
		}
	}
	return c.JSON(200, s.store.Effective(org))
}

// put writes a policy scope. A body carrying any PLATFORM field (cors_origins,
// per_ip_rpm, window_sec) is a platform write and requires SuperAdmin; otherwise
// it is a per-org OrgRPM write scoped to the caller's own org (or, for a
// SuperAdmin, ?org=<slug>). Metadata is server-stamped, never client-supplied.
func (s *svc) put(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var in gatewaypolicy.Policy
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid JSON body")
	}
	in.UpdatedBy = c.User() // server-stamped; a client-supplied value is ignored.

	platformWrite := len(in.CORSOrigins) > 0 || in.PerIPRPM > 0 || in.WindowSec > 0
	if platformWrite {
		if !c.IsAdmin() {
			return zip.ErrForbidden("platform policy (cors/per-IP) requires SuperAdmin")
		}
		saved, err := s.store.PutPlatform(c.Context(), in)
		if err != nil {
			s.log.Warn("gateway platform policy write failed", "err", err)
			return zip.Errorf(503, "policy store unavailable")
		}
		s.log.Info("gateway platform policy updated", "by", in.UpdatedBy,
			"cors", len(saved.CORSOrigins), "per_ip_rpm", saved.PerIPRPM, "window_sec", saved.WindowSec)
		return c.JSON(200, saved)
	}

	if in.OrgRPM <= 0 {
		return zip.ErrBadRequest("nothing to set: provide org_rpm, or cors_origins/per_ip_rpm/window_sec (SuperAdmin)")
	}
	target := org
	if c.IsAdmin() {
		if q := c.Query("org"); q != "" {
			target = q // SuperAdmin sets a specific tenant's ceiling.
		}
	}
	saved, err := s.store.Put(c.Context(), target, gatewaypolicy.Policy{OrgRPM: in.OrgRPM, UpdatedBy: in.UpdatedBy})
	if err != nil {
		s.log.Warn("gateway org policy write failed", "org", target, "err", err)
		return zip.Errorf(503, "policy store unavailable")
	}
	s.log.Info("gateway org policy updated", "org", target, "by", in.UpdatedBy, "org_rpm", saved.OrgRPM)
	return c.JSON(200, s.store.Effective(target))
}
