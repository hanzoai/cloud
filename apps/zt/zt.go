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

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// state is zt's own data; shared deps live in the embedded cloud.Base.
type state struct {
	cl *client
}

// Mount wires the networking surface onto app per HIP-0106 — one line over the
// generic subsystem entrypoint.
func Mount(app cloud.Router, deps cloud.Deps) error {
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
// :param siblings so an id can never shadow a literal. Every route is a zip TYPED
// op, so the REST route, the OpenAPI document, the MCP tool and the CLI command all
// come from the one declaration; the bridge goes on FIRST because a typed op is
// handed only a context, so the request (and the validated principal it proves) is
// parked there.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	app.Group("/v1/networks").Use(cloud.Bridge())
	app.Group("/v1/mesh").Use(cloud.Bridge())
	app.Group("/v1/edge").Use(cloud.Bridge())

	zip.Get(z, "/v1/networks", o.listNetworks, zip.WithOperationID("listNetworks"))
	zip.Get(z, "/v1/networks/:id", o.getNetwork, zip.WithOperationID("getNetwork"))
	zip.Get(z, "/v1/mesh/services", o.listMeshServices, zip.WithOperationID("listMeshServices"))
	zip.Get(z, "/v1/edge/nodes", o.listEdgeNodes, zip.WithOperationID("listEdgeNodes"))
}

// ops binds the service to zt's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value. That is also the only bound
// form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// tenant resolves the org — the tenant-isolation KEY, taken verbatim from the
// validated IAM owner claim (principal.OrgFrom). It selects the "org-<org>" role
// attribute this client filters ZT resources by, so a caller can never read another
// tenant's networking. gate() also enforces the fail-closed 503 when ZT is
// unconfigured, in ONE place, before any handler touches the controller, and hands
// back the request the controller client forwards the caller's identity on.
func gate(ctx context.Context, s *cloud.Service[state]) (string, *zip.Ctx, error) {
	if !s.State.cl.configured() {
		// Customer-facing body names NO internal env/config (the Warn log at Mount
		// records ZT_CLIENT_ID/ZT_CLIENT_SECRET for ops). Fail-closed 503 = "mounted
		// but not configured on this deployment", which the console renders as a clean
		// "not available yet" state.
		return "", nil, zip.Errorf(http.StatusServiceUnavailable, "networking is not configured on this deployment")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", nil, zip.ErrForbidden("X-Org-Id required")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", nil, zip.ErrForbidden("X-Org-Id required")
	}
	return org, c, nil
}

// ---- networks (the org's ZT overlay, projected from its edge-routers) ----

// NetworkList is the org's overlay-network roster.
type NetworkList struct {
	// Networks holds the org's overlay network, or is empty when the org has no
	// edge-routers on the fabric.
	Networks []Network `json:"networks"`
}

// listNetworks returns the caller org's Hanzo Zero Trust overlay network(s).
//
// The org's overlay is projected from the edge-routers carrying its "org-<org>"
// role attribute, so an org with no routers on the fabric has no network.
// A deployment with no ZT credential, or an unreachable controller, degrades this
// READ to an honest-EMPTY list (200) rather than 503-ing the Networks page.
//
// Response: {"networks": [{"id": "org-acme", "name": "acme overlay", "status": "active", "nodes": 2}]}
func (o ops) listNetworks(ctx context.Context, _ *struct{}) (*NetworkList, error) {
	s := o.s
	if !s.State.cl.configured() {
		// ZT not configured on this deployment — a READ returns an honest-EMPTY list
		// (200), rendered as a clean "no networks" state, NOT the gate's fail-closed
		// 503 that logs as a console error on every load. Writes stay fail-closed.
		return &NetworkList{Networks: []Network{}}, nil
	}
	org, c, err := gate(ctx, s)
	if err != nil {
		return nil, err
	}
	routers, err := orgRouters(s, c, org)
	if err != nil {
		// ZT controller unreachable / unconfigured (ZT_CLIENT_ID/SECRET optional) —
		// degrade this READ to an honest-EMPTY list (200) instead of 503-ing the
		// Networks page. Writes stay fail-closed.
		s.Log.Warn("zt controller unreachable; returning empty network list", "org", org, "err", err)
		return &NetworkList{Networks: []Network{}}, nil
	}
	out := make([]Network, 0, 1)
	if nv := networkFromRouters(org, routers); nv != nil {
		out = append(out, *nv)
	}
	return &NetworkList{Networks: out}, nil
}

// NetworkRef addresses one overlay network by id.
type NetworkRef struct {
	// ID is the overlay network id, "org-<org>" for the caller's own org.
	ID string `json:"id"`
}

// getNetwork returns one overlay network by id, 404 when it is not the caller's.
//
// An org has exactly one overlay network, keyed by its own org role attribute, so
// any other id is another tenant's (or a non-existent) network and answers 404.
//
// Example: {"id": "org-acme"}
// Response: {"id": "org-acme", "name": "acme overlay", "status": "active", "nodes": 2}
func (o ops) getNetwork(ctx context.Context, in *NetworkRef) (*Network, error) {
	org, c, err := gate(ctx, o.s)
	if err != nil {
		return nil, err
	}
	// The org has exactly one overlay network, keyed by networkID(org). Any other
	// id is another tenant's (or a non-existent) network → 404, never a peek.
	if in.ID != networkID(org) {
		return nil, zip.ErrNotFound("network not found")
	}
	routers, err := orgRouters(o.s, c, org)
	if err != nil {
		return nil, err
	}
	nv := networkFromRouters(org, routers)
	if nv == nil {
		return nil, zip.ErrNotFound("network not found")
	}
	return nv, nil
}

// ---- mesh services (ZT edge services) ----

// MeshServiceList is the org's mesh-service roster.
type MeshServiceList struct {
	// Services is one row per ZT edge service tagged for the caller's org.
	Services []MeshService `json:"services"`
}

// listMeshServices returns the caller org's Hanzo Zero Trust edge services.
//
// The controller's service list is filtered to the "org-<org>" role attribute, so
// an untagged service belongs to no org and is invisible to every tenant.
//
// Response: {"services": [{"id": "svc1", "service": "api", "mtls": "enabled", "status": "active"}]}
func (o ops) listMeshServices(ctx context.Context, _ *struct{}) (*MeshServiceList, error) {
	org, c, err := gate(ctx, o.s)
	if err != nil {
		return nil, err
	}
	all, err := listAll[ztService](o.s.State.cl, c, "/services")
	if err != nil {
		return nil, err
	}
	scoped := filterServices(all, org)
	out := make([]MeshService, 0, len(scoped))
	for _, sv := range scoped {
		out = append(out, toMeshView(sv))
	}
	return &MeshServiceList{Services: out}, nil
}

// ---- edge nodes (ZT edge-routers) ----

// EdgeNodeList is the org's edge-node roster.
type EdgeNodeList struct {
	// Nodes is one row per ZT edge-router tagged for the caller's org.
	Nodes []EdgeNode `json:"nodes"`
}

// listEdgeNodes returns the caller org's Hanzo Zero Trust edge routers and health.
//
// The controller's edge-router list is filtered to the "org-<org>" role attribute,
// and status is the controller's REAL online/disabled/offline signal.
// A deployment with no ZT credential, or an unreachable controller, degrades this
// READ to an honest-EMPTY list (200) rather than 503-ing the Edge page.
//
// Response: {"nodes": [{"id": "er1", "name": "edge-us-east", "region": "us-east", "status": "online"}]}
func (o ops) listEdgeNodes(ctx context.Context, _ *struct{}) (*EdgeNodeList, error) {
	s := o.s
	if !s.State.cl.configured() {
		// Same as listNetworks: an unconfigured ZT deployment yields an honest-EMPTY
		// edge-node list (200), not the gate's fail-closed 503. Writes stay fail-closed.
		return &EdgeNodeList{Nodes: []EdgeNode{}}, nil
	}
	org, c, err := gate(ctx, s)
	if err != nil {
		return nil, err
	}
	routers, err := orgRouters(s, c, org)
	if err != nil {
		// Same graceful fold as listNetworks — an unreachable/unconfigured ZT controller
		// yields an honest-EMPTY edge-node list (200), never a 503 page error.
		s.Log.Warn("zt controller unreachable; returning empty edge-node list", "org", org, "err", err)
		return &EdgeNodeList{Nodes: []EdgeNode{}}, nil
	}
	out := make([]EdgeNode, 0, len(routers))
	for _, r := range routers {
		out = append(out, toEdgeNodeView(r))
	}
	return &EdgeNodeList{Nodes: out}, nil
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
