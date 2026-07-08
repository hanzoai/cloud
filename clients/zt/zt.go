// Package zt mounts the Hanzo Cloud NETWORKING surface: the tenant's Hanzo Zero
// Trust footprint — overlay networks, mesh services and edge nodes — served as
// clean, org-scoped REST off the unified cloud binary and fronting the Hanzo Zero
// Trust controller (hanzoai/zt, an OpenZiti-based fabric). It exists so the
// console's Networks, Service Mesh and Edge pages read REAL per-org ZT state from
// ONE place (api.hanzo.ai/v1/*) instead of rendering "not connected".
//
// This subsystem OWNS no ZT state — the controller does. It is a thin, tenant-scoped
// translator: it fronts the controller's Edge MANAGEMENT API (/edge/management/v1),
// filters every resource to the caller's org by the "org-<org>" role attribute, and
// re-shapes ZT objects into the exact JSON the console modules consume (types.go).
// It never fabricates: a mesh row is a real ZT edge service, an edge node is a real
// edge-router with its real online status, and a network exists only when the org
// actually has edge-routers on the fabric.
//
// Surface (every route org-scoped by the validated principal; HIP-0026):
//
//	GET /v1/networks         the org's ZT overlay network(s)   -> {networks:[networkView]}
//	GET /v1/networks/:id     one overlay network by id         -> networkView (404 if absent)
//	GET /v1/mesh/services    the org's ZT edge services        -> {services:[meshView]}
//	GET /v1/edge/nodes       the org's ZT edge-routers          -> {nodes:[edgeNodeView]}
//
// Networks maps to the fabric overview (edge-routers are the overlay's nodes),
// Service Mesh to ZT edge services, and Edge to ZT edge-routers — the three ZT
// concepts the three console pages need.
//
// TENANT ISOLATION. The org (principal.Tenant, the validated IAM owner) selects the
// "org-<org>" role attribute; the client lists the controller's resources and this
// subsystem filters to that role, so a caller can only ever read their OWN tenant's
// ZT footprint. The org is taken from the validated identity, never a client field.
//
// FAIL-CLOSED. Absent the ZT service credential (ZT_CLIENT_ID / ZT_CLIENT_SECRET,
// KMS-injected) the subsystem mounts its full route space but every op returns an
// honest 503; it NEVER fabricates a network, service or node.
package zt

import (
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type svc struct {
	cl  *client
	log luxlog.Logger
}

// Mount wires the networking surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("zt.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("zt.Mount: nil deps.Logger")
	}
	s := &svc{cl: newClient(), log: deps.Logger.New("subsystem", "zero-trust")}
	s.routes(app)

	if !s.cl.configured() {
		s.log.Warn("zt networking surface mounted fail-closed: ZT_CLIENT_ID/ZT_CLIENT_SECRET not set (all ops 503 until configured)",
			"controller", s.cl.target)
		return nil
	}
	s.log.Info("zt networking surface mounted", "controller", s.cl.target, "brand", deps.Brand, "env", deps.Env)
	return nil
}

// routes is the ONE place the surface is wired — shared by Mount (real controller)
// and the test (an httptest fake). Static paths register before their :param
// siblings so an id can never shadow a literal.
func (s *svc) routes(app *zip.App) {
	app.Get("/v1/networks", s.listNetworks)
	app.Get("/v1/networks/:id", s.getNetwork)
	app.Get("/v1/mesh/services", s.listMeshServices)
	app.Get("/v1/edge/nodes", s.listEdgeNodes)
}

func init() {
	cloud.Register("zero-trust", 134, cloud.Typed(Mount))
}

// tenant resolves the org — the tenant-isolation KEY, taken verbatim from the
// validated IAM owner claim (principal.Tenant). It selects the "org-<org>" role
// attribute this client filters ZT resources by, so a caller can never read another
// tenant's networking. gate() also enforces the fail-closed 503 when ZT is
// unconfigured, in ONE place, before any handler touches the controller.
func (s *svc) gate(c *zip.Ctx) (string, error) {
	if !s.cl.configured() {
		// Customer-facing body names NO internal env/config (the Warn log at Mount
		// records ZT_CLIENT_ID/ZT_CLIENT_SECRET for ops). Fail-closed 503 = "mounted
		// but not configured on this deployment", which the console renders as a clean
		// "not available yet" state.
		return "", zip.Errorf(http.StatusServiceUnavailable, "networking is not configured on this deployment")
	}
	org, ok := principal.Tenant(c)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// ---- networks (the org's ZT overlay, projected from its edge-routers) ----

func (s *svc) listNetworks(c *zip.Ctx) error {
	org, err := s.gate(c)
	if err != nil {
		return err
	}
	routers, err := s.orgRouters(c, org)
	if err != nil {
		return err
	}
	out := make([]networkView, 0, 1)
	if nv := networkFromRouters(org, routers); nv != nil {
		out = append(out, *nv)
	}
	return c.JSON(http.StatusOK, map[string]any{"networks": out})
}

func (s *svc) getNetwork(c *zip.Ctx) error {
	org, err := s.gate(c)
	if err != nil {
		return err
	}
	// The org has exactly one overlay network, keyed by networkID(org). Any other
	// id is another tenant's (or a non-existent) network → 404, never a peek.
	if c.Param("id") != networkID(org) {
		return zip.ErrNotFound("network not found")
	}
	routers, err := s.orgRouters(c, org)
	if err != nil {
		return err
	}
	nv := networkFromRouters(org, routers)
	if nv == nil {
		return zip.ErrNotFound("network not found")
	}
	return c.JSON(http.StatusOK, *nv)
}

// ---- mesh services (ZT edge services) ----

func (s *svc) listMeshServices(c *zip.Ctx) error {
	org, err := s.gate(c)
	if err != nil {
		return err
	}
	all, err := listAll[ztService](s.cl, c, "/services")
	if err != nil {
		return err
	}
	scoped := filterServices(all, org)
	out := make([]meshView, 0, len(scoped))
	for _, sv := range scoped {
		out = append(out, toMeshView(sv))
	}
	return c.JSON(http.StatusOK, map[string]any{"services": out})
}

// ---- edge nodes (ZT edge-routers) ----

func (s *svc) listEdgeNodes(c *zip.Ctx) error {
	org, err := s.gate(c)
	if err != nil {
		return err
	}
	routers, err := s.orgRouters(c, org)
	if err != nil {
		return err
	}
	out := make([]edgeNodeView, 0, len(routers))
	for _, r := range routers {
		out = append(out, toEdgeNodeView(r))
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": out})
}

// orgRouters lists the controller's edge-routers and filters to the caller's org —
// the ONE place routers are fetched+scoped, shared by /v1/networks and
// /v1/edge/nodes so both derive from the identical tenant-filtered set.
func (s *svc) orgRouters(c *zip.Ctx, org string) ([]ztEdgeRouter, error) {
	all, err := listAll[ztEdgeRouter](s.cl, c, "/edge-routers")
	if err != nil {
		return nil, err
	}
	return filterRouters(all, org), nil
}
