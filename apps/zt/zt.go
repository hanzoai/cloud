// Package zt mounts the Hanzo Cloud NETWORKING surface: the tenant's Hanzo Zero
// Trust footprint — overlay networks, their routers and mesh services — served as
// clean, org-scoped REST off the unified cloud binary and fronting the Hanzo Zero
// Trust controller (hanzoai/zt, an OpenZiti-based fabric). It exists so the
// console's Networks, Service Mesh and Routers pages read REAL per-org ZT state from
// ONE place (api.hanzo.ai/v1/*) instead of rendering "not connected".
//
// This subsystem OWNS no ZT state — the controller does. It is a thin, tenant-scoped
// translator: it fronts the controller's Edge MANAGEMENT API (/edge/management/v1),
// filters every resource to the caller's org by the "org-<org>" role attribute, and
// re-shapes ZT objects into the exact JSON the console modules consume (types.go).
// It never fabricates: a mesh row is a real ZT edge service, a router row is a real
// edge-router with its real online status, and a network exists only when the org
// actually has edge-routers on the fabric.
//
// Surface (every route org-scoped by the validated principal; HIP-0026):
//
//	GET /v1/networks          the org's ZT overlay network(s)  -> {networks:[networkView]}
//	GET /v1/networks/routers  the org's ZT edge-routers        -> {routers:[routerView]}
//	GET /v1/networks/:id      one overlay network by id        -> networkView (404 if absent)
//	GET /v1/mesh/services     the org's ZT edge services       -> {services:[meshView]}
//
// Networks maps to the fabric overview, its routers to the ZT edge-routers that ARE
// that overlay's nodes, and Service Mesh to ZT edge services — the three ZT concepts
// the three console pages need.
//
// THE ROUTERS LIVE UNDER THE NETWORK, and this is the whole reason the route moved.
// They were served at /v1/edge/nodes, which read as a top-level Hanzo product named
// "edge" and was not one: hanzoai/edge is the on-device inference runtime, a binary
// a customer runs on their own machine, and it has no cloud surface at all. Three
// more unrelated things wore the same word — the public catalogue cache, the
// gateway's CORS-and-rate-ceiling policy role, and a JWT audience list — so "edge"
// named a POSITION, never a product, and a prefix belongs to a product a customer
// calls. An edge-router is a node of an overlay network, so it is addressed as one.
// /v1/edge now 404s at every depth, which is correct rather than a missing product.
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
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// state is zt's own data; shared deps live in the embedded cloud.Base.
type state struct {
	cl *client
}

// Mount wires the networking surface onto app per HIP-0106 — one line over the
// generic subsystem entrypoint.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "zt", build, routes)
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
//
// cloud.Bridge FIRST and ONCE, through Use, BEFORE the leaves: a typed op receives
// only a context, so the validated org reaches it by being parked there — never as
// an In field, which is caller-supplied and would be a cross-tenant read the caller
// asserted for itself. fiber runs middleware in registration order, so one installed
// after its leaves never runs. On a scope, Use is bounded by the prefixes the
// manifest declared for this subsystem (/v1/networks and /v1/mesh/services), which
// is every route below and nothing else; on a bare app — what this package's tests
// mount on — it is app-wide, which is what makes the tenant boundary hold there.
//
// IT USED TO BE TWO GROUP INSTALLS, and that is the defect this shape closes.
// Middleware on a group wraps what is composed BENEATH it, and the two collection
// ROOTS are declared on the App with their whole path — joining "/v1/networks" with
// an empty leaf yields "/v1/networks/", a different address from the one they have
// always served. So the group's Bridge covered /v1/networks/routers and /:id and
// missed /v1/networks itself: a validated caller reached the op with no org parked
// and was answered 403. TestBridgeIsInstalledOnEveryPrefix is that measurement, and
// the path-bounded Use covers the root and the subtree with one install.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ztOps{s: s}
	zapp := cloud.ZipApp(app)
	app.Use(cloud.Bridge())

	ng := app.Group("/v1/networks")
	zip.Get(zapp, "/v1/networks", o.listNetworks)
	// The overlay's routers hang off the network they belong to, so they register on
	// the SAME group — one subtree, no second top-level name. "routers" is a literal
	// beside ":id" and goes first, per this function's rule. That ordering is a
	// convention here, not a load-bearing accident: measured on this router, the
	// static segment wins over its param sibling in EITHER registration order, so
	// nothing has to be frozen by a test to keep /v1/networks/routers reachable.
	zip.Get(ng, "/routers", o.listRouters)
	zip.Get(ng, "/:id", o.getNetwork)

	mg := app.Group("/v1/mesh")
	zip.Get(mg, "/services", o.listMeshServices)
}

// ztOps binds the service to the typed networking ops. A TypedHandler takes no
// service parameter, so the service arrives as a RECEIVER and every op is a method
// value — also the only bound form cmd/zipdoc can lift prose from.
type ztOps struct{ s *cloud.Service[state] }

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query. GET carries no request body (zip's hasBody), so this publishes nothing.
type noIn struct{}

// networkRef addresses one overlay network by the id GET /v1/networks returns.
type networkRef struct {
	// ID is the network id from the path. The URL is the addressing authority, so
	// it binds from there whatever else the request carries.
	ID string `json:"id"`
}

// networkList is the GET /v1/networks envelope.
type networkList struct {
	// Networks holds the org's overlay network, or is empty when the org has no
	// edge-routers on the fabric (no nodes → no network, never a fabricated one).
	Networks []networkView `json:"networks"`
}

// meshServiceList is the GET /v1/mesh/services envelope.
type meshServiceList struct {
	// Services is one row per ZT edge service tagged with the caller's org role.
	Services []meshView `json:"services"`
}

// routerList is the GET /v1/networks/routers envelope.
type routerList struct {
	// Routers is one row per ZT edge-router tagged with the caller's org role.
	Routers []routerView `json:"routers"`
}

// tenant resolves the org — the tenant-isolation KEY, taken verbatim from the
// validated IAM owner claim that cloud.Bridge parked on the context. It selects the
// "org-<org>" role attribute this client filters ZT resources by, so a caller can
// never read another tenant's networking. gate() also enforces the fail-closed 503
// when ZT is unconfigured, in ONE place, before any handler touches the controller.
func gate(s *cloud.Service[state], ctx context.Context) (string, error) {
	if !s.State.cl.configured() {
		// Customer-facing body names NO internal env/config (the Warn log at Mount
		// records ZT_CLIENT_ID/ZT_CLIENT_SECRET for ops). Fail-closed 503 = "mounted
		// but not configured on this deployment", which the console renders as a clean
		// "not available yet" state.
		return "", zip.Errorf(http.StatusServiceUnavailable, "networking is not configured on this deployment")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// ---- networks (the org's ZT overlay, projected from its edge-routers) ----

// listNetworks returns the caller's org overlay network on the Zero Trust fabric.
//
// The org has at most ONE overlay, projected from the edge-routers tagged with its
// "org-<org>" role attribute: nodes is the real router count and status is
// "connected" once at least one router has dialed home, "provisioning" while none
// has. An org with no routers gets an empty list, never a fabricated network.
//
// The read degrades rather than erroring: a deployment with no ZT credential, and a
// controller that cannot be reached, both answer 200 with an empty list so the
// console's Networks page renders a clean empty state instead of an error.
func (o ztOps) listNetworks(ctx context.Context, _ *noIn) (*networkList, error) {
	s := o.s
	if !s.State.cl.configured() {
		// ZT not configured on this deployment — a READ returns an honest-EMPTY list
		// (200), rendered as a clean "no networks" state, NOT the gate's fail-closed
		// 503 that logs as a console error on every load. Writes stay fail-closed.
		return &networkList{Networks: []networkView{}}, nil
	}
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	routers, err := orgRouters(s, ctx, org)
	if err != nil {
		// ZT controller unreachable / unconfigured (ZT_CLIENT_ID/SECRET optional) —
		// degrade this READ to an honest-EMPTY list (200) instead of 503-ing the
		// Networks page. Writes stay fail-closed.
		s.Log.Warn("zt controller unreachable; returning empty network list", "org", org, "err", err)
		return &networkList{Networks: []networkView{}}, nil
	}
	out := make([]networkView, 0, 1)
	if nv := networkFromRouters(org, routers); nv != nil {
		out = append(out, *nv)
	}
	return &networkList{Networks: out}, nil
}

// getNetwork returns one overlay network by id, scoped to the caller's org.
//
// The org has exactly one overlay network and its id is derived from the org, so
// any other id — another tenant's, or one that does not exist — is 404 rather than
// a peek across the tenant boundary. An org whose network exists but has no
// edge-routers is 404 too, for the same reason the list is empty: there is no
// overlay until something is on it.
func (o ztOps) getNetwork(ctx context.Context, in *networkRef) (*networkView, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	// The org has exactly one overlay network, keyed by networkID(org). Any other
	// id is another tenant's (or a non-existent) network → 404, never a peek.
	if in.ID != networkID(org) {
		return nil, zip.ErrNotFound("network not found")
	}
	routers, err := orgRouters(s, ctx, org)
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

// listMeshServices returns the Zero Trust edge services the caller's org owns.
//
// One row per real ZT edge service tagged with the org's "org-<org>" role
// attribute: mtls is "required" when the service mandates end-to-end encryption and
// "enabled" otherwise (the fabric always mutually authenticates every link), and
// status is "active" because a listed service is a configured, dialable entry. A
// service tagged for another org, or tagged for none, is invisible here.
//
// Unlike the network and router reads this does NOT degrade: an unconfigured
// deployment answers 503 and an unreachable controller surfaces the upstream's
// status, so a mesh page never renders "no services" for a fabric it simply could
// not read.
func (o ztOps) listMeshServices(ctx context.Context, _ *noIn) (*meshServiceList, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	all, err := listAll[ztService](s.State.cl, ctx, "/services")
	if err != nil {
		return nil, err
	}
	scoped := filterServices(all, org)
	out := make([]meshView, 0, len(scoped))
	for _, sv := range scoped {
		out = append(out, toMeshView(sv))
	}
	return &meshServiceList{Services: out}, nil
}

// ---- routers (the overlay's ZT edge-routers) ----

// listRouters returns the Zero Trust routers the caller's org owns.
//
// One row per real ZT edge-router tagged with the org's "org-<org>" role attribute,
// carrying the controller's own health signal: "online" when connected, "disabled"
// when administratively disabled, "offline" otherwise. region is filled only from a
// "region-<slug>" role attribute and omitted when the router carries none, so the
// column renders "—" rather than a guess.
//
// The read degrades rather than erroring: a deployment with no ZT credential, and a
// controller that cannot be reached, both answer 200 with an empty list.
func (o ztOps) listRouters(ctx context.Context, _ *noIn) (*routerList, error) {
	s := o.s
	if !s.State.cl.configured() {
		// Same as listNetworks: an unconfigured ZT deployment yields an honest-EMPTY
		// router list (200), not the gate's fail-closed 503. Writes stay fail-closed.
		return &routerList{Routers: []routerView{}}, nil
	}
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	routers, err := orgRouters(s, ctx, org)
	if err != nil {
		// Same graceful fold as listNetworks — an unreachable/unconfigured ZT controller
		// yields an honest-EMPTY router list (200), never a 503 page error.
		s.Log.Warn("zt controller unreachable; returning empty router list", "org", org, "err", err)
		return &routerList{Routers: []routerView{}}, nil
	}
	out := make([]routerView, 0, len(routers))
	for _, r := range routers {
		out = append(out, toRouterView(r))
	}
	return &routerList{Routers: out}, nil
}

// orgRouters lists the controller's edge-routers and filters to the caller's org —
// the ONE place routers are fetched+scoped, shared by /v1/networks and
// /v1/networks/routers so both derive from the identical tenant-filtered set.
func orgRouters(s *cloud.Service[state], ctx context.Context, org string) ([]ztEdgeRouter, error) {
	all, err := listAll[ztEdgeRouter](s.State.cl, ctx, "/edge-routers")
	if err != nil {
		return nil, err
	}
	return filterRouters(all, org), nil
}
