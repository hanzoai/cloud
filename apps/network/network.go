package network

import (
	"context"
	"net/http"
	"sync"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// state is this subsystem's own data; shared deps live in the embedded cloud.Base.
type state struct {
	cl *client
	// trail is the audit trail the invite admin records an act on before it does it
	// (invites_admin.go); nil refuses the act.
	trail *audit.Recorder
	// seal is the KMS-held key invite codes and the intake's counts are made under, and
	// grown the orgs whose Base this process has grown by the intake's collections
	// (access.go).
	seal  *secret.Sealed
	grown *sync.Map
}

// Mount wires the networking surface onto app per HIP-0106 — one line over the
// generic subsystem entrypoint, which hands the audit trail to the state beside
// the controller client.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "network", func(b cloud.Base) (state, error) {
		s, err := build(b)
		s.trail = deps.Audit
		return s, err
	}, routes)
}

// build constructs the state: the ZT controller client, logging in with the
// cloud's own IAM identity. It records the mount posture — a fail-closed Warn when
// that identity is absent (every op 503 until configured), an Info with the
// controller target otherwise.
func build(b cloud.Base) (state, error) {
	cl := newClient(b.Log)
	if !cl.configured() {
		b.Log.Warn("network surface mounted fail-closed: IAM_CLIENT_ID/IAM_CLIENT_SECRET not set (all ops 503 until configured)",
			"controller", cl.target)
	} else {
		b.Log.Info("network surface mounted", "controller", cl.target, "brand", b.Brand, "env", b.Env)
	}
	return newState(cl, b.Coord), nil
}

// routes is the ONE place the surface is wired. Static paths register before their
// :param siblings so an id can never shadow a literal.
//
// cloud.Bridge is not installed here: the composer installs it once at the root,
// after the identity check that mints the validated org and before any subsystem
// registers a route — an order only the whole program can assert. The typed ops
// below read what it parks off the context.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	network := app.Group("/v1/network")
	network.Get("", o.listNetworks)
	g := app.Group("/v1/network")
	// Routers and services hang off the network they belong to, so they register on
	// the SAME group — one subtree, no second top-level name. Both are literals
	// beside ":id" and go first, per this function's rule. That ordering is a
	// convention here, not a load-bearing accident: measured on this router, a static
	// segment wins over its param sibling in EITHER registration order, so nothing
	// has to be frozen by a test to keep them reachable.
	g.Get("/routers", o.listRouters)
	g.Get("/services", o.listServices)
	g.Post("/services", o.publishService, zip.WithStatus(http.StatusCreated))
	g.Delete("/services/:id", o.unpublishService)
	g.Get("/identities", o.listIdentities)
	g.Post("/identities", o.ensureIdentity, zip.WithStatus(http.StatusCreated))
	g.Delete("/identities/:id", o.deleteIdentity)
	// The two anonymous operations (access.go says why they are open) and, beside
	// them, the invites a SuperAdmin issues. They are literals beside ":id" and go
	// before it, like every static path here.
	g.Post("/invite/verify", o.verifyInvite)
	g.Post("/waitlist", o.joinWaitlist, zip.WithStatus(http.StatusAccepted))
	g.Get("/:id", o.getNetwork)

	// The operator's own view of this capability (HIP-0139 §3.2): a SuperAdmin issues
	// invite codes, reads them and revokes them. The group refuses first on the
	// routed path and each op asks again, because MCP and the call plane invoke an
	// op with no route in front of it. The group carries its own Bridge, as
	// marketing's operator group does: the ops read the request's SuperAdmin bit.
	a := app.Group("/v1/admin/network", cloud.Gate(cloud.Super))
	a.Use(cloud.Bridge())
	a.Post("/invites", o.issueInvites, zip.WithStatus(http.StatusCreated))
	a.Get("/invites", o.listInvites)
	a.Delete("/invites/:id", o.revokeInvite)
}

// ops binds the service to the typed networking ops. A TypedHandler takes no
// service parameter, so the service arrives as a RECEIVER and every op is a method
// value — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query. GET carries no request body (zip's hasBody), so this publishes nothing.
type noIn struct{}

// networkRef addresses one overlay network by the id GET /v1/network returns.
type networkRef struct {
	// ID is the network id from the path. The URL is the addressing authority, so
	// it binds from there whatever else the request carries.
	ID string `json:"id"`
}

// networkList is the GET /v1/network envelope.
type networkList struct {
	// Networks holds the org's overlay network, or is empty when the org has no
	// edge-routers on the fabric (no nodes → no network, never a fabricated one).
	Networks []networkView `json:"networks"`
}

// meshServiceList is the GET /v1/network/services envelope.
type meshServiceList struct {
	// Services is one row per ZT edge service tagged with the caller's org role.
	Services []meshView `json:"services"`
}

// routerList is the GET /v1/network/routers envelope.
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
		// names the missing identity for ops). Fail-closed 503 = "mounted
		// but not configured on this deployment", which the console renders as a clean
		// "not available yet" state.
		return "", zip.Errorf(http.StatusServiceUnavailable, "networking is not configured on this deployment")
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	return org, nil
}

// steward reports whether the caller may change what the org's overlay serves
// or who else is on it: an admin of the org (IsOrgAdmin), the platform
// (Super), or the org's own machine client (IsApp), whose org is its
// owner and cannot be switched, so it can act for no other. It reads the
// authority the identity boundary minted from validated claims, and a call that
// arrived with no request (a tool call) is no steward: the gate fails closed.
func steward(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	return ok && (principal.Super(c) || principal.IsOrgAdmin(c) || principal.IsApp(c))
}

// errSteward refuses a member a steward's act.
func errSteward(what string) error {
	return zip.ErrForbidden(what + " is for an admin of the org or its own machine client")
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
func (o ops) listNetworks(ctx context.Context, _ *noIn) (*networkList, error) {
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
		// ZT controller unreachable, or it refused the cloud's IAM identity —
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
func (o ops) getNetwork(ctx context.Context, in *networkRef) (*networkView, error) {
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

// ---- services (ZT edge services) ----

// listServices returns the Zero Trust edge services the caller's org owns.
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
func (o ops) listServices(ctx context.Context, _ *noIn) (*meshServiceList, error) {
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
func (o ops) listRouters(ctx context.Context, _ *noIn) (*routerList, error) {
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
// the ONE place routers are fetched+scoped, shared by /v1/network and
// /v1/network/routers so both derive from the identical tenant-filtered set.
func orgRouters(s *cloud.Service[state], ctx context.Context, org string) ([]ztEdgeRouter, error) {
	all, err := listAll[ztEdgeRouter](s.State.cl, ctx, "/edge-routers")
	if err != nil {
		return nil, err
	}
	return filterRouters(all, org), nil
}
