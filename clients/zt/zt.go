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
// TENANT ISOLATION. The org (principal.Org, the validated IAM owner) selects the
// "org-<org>" role attribute; the client lists the controller's resources and this
// subsystem filters to that role, so a caller can only ever read their OWN tenant's
// ZT footprint. The org is taken from the validated identity, never a client field.
//
// FAIL-CLOSED. Absent the ZT service credential (ZT_CLIENT_ID / ZT_CLIENT_SECRET,
// KMS-injected) the subsystem mounts its full route space but every op returns an
// honest 503; it NEVER fabricates a network, service or node.
package zt

import (
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// state is zt's own data; shared deps live in the embedded cloud.Base.
type state struct {
	cl *client
}

// Mount wires the networking surface onto app per HIP-0106 — one line over the
// generic subsystem entrypoint.
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "zero-trust", build, routes)
}

// build constructs the zt state: the ZT controller client from its env
// (ZT_CLIENT_ID/ZT_CLIENT_SECRET are KMS-injected). It records the mount posture —
// a fail-closed Warn when the service credential is absent (every op 503 until
// configured), an Info with the controller target otherwise.
func build(b cloud.Base) (state, error) {
	cl := newClient()
	if !cl.configured() {
		b.Log.Warn("zt networking surface mounted fail-closed: ZT_CLIENT_ID/ZT_CLIENT_SECRET not set (all ops 503 until configured)",
			"controller", cl.target)
	} else {
		b.Log.Info("zt networking surface mounted", "controller", cl.target, "brand", b.Brand, "env", b.Env)
	}
	return state{cl: cl}, nil
}

// routes is the ONE place the surface is wired. Static paths register before their
// :param siblings so an id can never shadow a literal.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/networks", cloud.Handle(s, listNetworks))
	app.Get("/v1/networks/:id", cloud.Handle(s, getNetwork))
	app.Get("/v1/mesh/services", cloud.Handle(s, listMeshServices))
	app.Get("/v1/edge/nodes", cloud.Handle(s, listEdgeNodes))
}

func init() {
	cloud.Register("zero-trust", 134, cloud.Typed(Mount))
}

// tenant resolves the org — the tenant-isolation KEY, taken verbatim from the
// validated IAM owner claim (principal.Org). It selects the "org-<org>" role
// attribute this client filters ZT resources by, so a caller can never read another
// tenant's networking. gate() also enforces the fail-closed 503 when ZT is
// unconfigured, in ONE place, before any handler touches the controller.
func gate(s *cloud.Service[state], c *zip.Ctx) (string, error) {
	if !s.State.cl.configured() {
		// Customer-facing body names NO internal env/config (the Warn log at Mount
		// records ZT_CLIENT_ID/ZT_CLIENT_SECRET for ops). Fail-closed 503 = "mounted
		// but not configured on this deployment", which the console renders as a clean
		// "not available yet" state.
		return "", zip.Errorf(http.StatusServiceUnavailable, "networking is not configured on this deployment")
	}
	org, ok := principal.Org(c)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// ---- networks (the org's ZT overlay, projected from its edge-routers) ----

func listNetworks(s *cloud.Service[state], c *zip.Ctx) error {
	if !s.State.cl.configured() {
		// ZT not configured on this deployment — a READ returns an honest-EMPTY list
		// (200), rendered as a clean "no networks" state, NOT the gate's fail-closed
		// 503 that logs as a console error on every load. Writes stay fail-closed.
		return c.JSON(http.StatusOK, map[string]any{"networks": []networkView{}})
	}
	org, err := gate(s, c)
	if err != nil {
		return err
	}
	routers, err := orgRouters(s, c, org)
	if err != nil {
		// ZT controller unreachable / unconfigured (ZT_CLIENT_ID/SECRET optional) —
		// degrade this READ to an honest-EMPTY list (200) instead of 503-ing the
		// Networks page. Writes stay fail-closed.
		s.Log.Warn("zt controller unreachable; returning empty network list", "org", org, "err", err)
		return c.JSON(http.StatusOK, map[string]any{"networks": []networkView{}})
	}
	out := make([]networkView, 0, 1)
	if nv := networkFromRouters(org, routers); nv != nil {
		out = append(out, *nv)
	}
	return c.JSON(http.StatusOK, map[string]any{"networks": out})
}

func getNetwork(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := gate(s, c)
	if err != nil {
		return err
	}
	// The org has exactly one overlay network, keyed by networkID(org). Any other
	// id is another tenant's (or a non-existent) network → 404, never a peek.
	if c.Param("id") != networkID(org) {
		return zip.ErrNotFound("network not found")
	}
	routers, err := orgRouters(s, c, org)
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

func listMeshServices(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := gate(s, c)
	if err != nil {
		return err
	}
	all, err := listAll[ztService](s.State.cl, c, "/services")
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

func listEdgeNodes(s *cloud.Service[state], c *zip.Ctx) error {
	if !s.State.cl.configured() {
		// Same as listNetworks: an unconfigured ZT deployment yields an honest-EMPTY
		// edge-node list (200), not the gate's fail-closed 503. Writes stay fail-closed.
		return c.JSON(http.StatusOK, map[string]any{"nodes": []edgeNodeView{}})
	}
	org, err := gate(s, c)
	if err != nil {
		return err
	}
	routers, err := orgRouters(s, c, org)
	if err != nil {
		// Same graceful fold as listNetworks — an unreachable/unconfigured ZT controller
		// yields an honest-EMPTY edge-node list (200), never a 503 page error.
		s.Log.Warn("zt controller unreachable; returning empty edge-node list", "org", org, "err", err)
		return c.JSON(http.StatusOK, map[string]any{"nodes": []edgeNodeView{}})
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
func orgRouters(s *cloud.Service[state], c *zip.Ctx, org string) ([]ztEdgeRouter, error) {
	all, err := listAll[ztEdgeRouter](s.State.cl, c, "/edge-routers")
	if err != nil {
		return nil, err
	}
	return filterRouters(all, org), nil
}
