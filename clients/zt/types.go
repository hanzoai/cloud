// types.go holds the ZT wire structs (what the OpenZiti Edge Management API
// returns) and the console view structs (what this subsystem emits), plus the PURE
// mapping between them. The view JSON keys mirror the console modules EXACTLY so the
// Networks, Service Mesh and Edge pages render with no front-end change:
//
//   - networkView  -> console NetworksModule.tsx   BootnodeNetwork {id,name,chain,status,nodes,rpc}
//   - meshView     -> console ServiceMeshModule.tsx MeshService     {id,service,namespace,mtls,requests,status}
//   - edgeNodeView -> console EdgeModule.tsx        EdgeNode        {id,name,region,status,requests,latency}
//
// Every field is a REAL ZT value or an honest omission. Telemetry ZT's management
// API does not carry (per-service request counts, per-router latency) is left off
// the view so the UI renders "—", never a fabricated 0.
//
// TENANT ISOLATION. ZT (OpenZiti) has no native org tenancy; services and
// edge-routers are scoped by their `roleAttributes` — the SAME first-class strings
// Ziti uses to drive service and edge-router policies. The org boundary is therefore
// the role attribute "org-<org>": a resource belongs to a tenant iff its
// roleAttributes contains that exact string (the org is the validated IAM owner,
// used verbatim). List/get filter to the caller's role, so one tenant can never see
// another's services or nodes. An untagged resource belongs to NO org and is
// invisible to every tenant — honest-empty over a cross-tenant leak.
package zt

import "strings"

// orgRolePrefix + regionRolePrefix are the ONE role-attribute conventions this
// subsystem reads. "org-<org>" is the tenant key; "region-<slug>" optionally
// surfaces an edge-router's region (honest "—" when a router carries none).
const (
	orgRolePrefix    = "org-"
	regionRolePrefix = "region-"
)

// orgRole is the role attribute that marks a ZT resource as belonging to org.
func orgRole(org string) string { return orgRolePrefix + org }

// ---- ZT wire structs (OpenZiti rest_model subset, JSON as the API emits) ----

// ztService mirrors the Edge Management API ServiceDetail (JSON subset). Its
// EdgeService model lives at controller/model/edge_service_model.go.
type ztService struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	RoleAttributes     []string `json:"roleAttributes"`
	Configs            []string `json:"configs"`
	EncryptionRequired bool     `json:"encryptionRequired"`
	TerminatorStrategy string   `json:"terminatorStrategy"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

// ztEdgeRouter mirrors the Edge Management API EdgeRouterDetail (JSON subset). Its
// EdgeRouter model lives at controller/model/edge_router_model.go. isOnline and
// disabled are the REAL health signals the controller reports.
type ztEdgeRouter struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	RoleAttributes    []string `json:"roleAttributes"`
	IsOnline          bool     `json:"isOnline"`
	IsVerified        bool     `json:"isVerified"`
	IsTunnelerEnabled bool     `json:"isTunnelerEnabled"`
	Hostname          string   `json:"hostname"`
	Disabled          bool     `json:"disabled"`
	NoTraversal       bool     `json:"noTraversal"`
	Cost              int      `json:"cost"`
	CreatedAt         string   `json:"createdAt"`
}

// ---- console view structs ----

// networkView is the shape console NetworksModule (BootnodeNetwork) consumes. It
// represents the org's slice of the ZT overlay — one network whose nodes are the
// org's edge-routers. chain/rpc are honestly omitted (a ZT overlay is not a
// blockchain with an RPC), so those columns render blank rather than fabricated.
type networkView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Nodes  int    `json:"nodes"`
}

// meshView is the shape console ServiceMeshModule (MeshService) consumes — one row
// per ZT edge service. mtls reflects the service's E2E encryption requirement;
// requests is omitted (the management API carries no per-service metrics).
type meshView struct {
	ID      string `json:"id"`
	Service string `json:"service"`
	Mtls    string `json:"mtls"`
	Status  string `json:"status"`
}

// edgeNodeView is the shape console EdgeModule (EdgeNode) consumes — one row per ZT
// edge-router. status is the REAL online/disabled/offline signal; region is filled
// only from a "region-<slug>" role attribute (honest "—" otherwise); requests and
// latency are omitted (no per-router telemetry in the management API).
type edgeNodeView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Region string `json:"region,omitempty"`
	Status string `json:"status"`
}

// ---- tenant filtering (PURE) ----

// hasRole reports whether attrs contains the exact role attribute want.
func hasRole(attrs []string, want string) bool {
	for _, a := range attrs {
		if a == want {
			return true
		}
	}
	return false
}

// filterServices keeps only the org's services (roleAttributes ∋ "org-<org>").
func filterServices(all []ztService, org string) []ztService {
	role := orgRole(org)
	out := make([]ztService, 0, len(all))
	for _, s := range all {
		if hasRole(s.RoleAttributes, role) {
			out = append(out, s)
		}
	}
	return out
}

// filterRouters keeps only the org's edge-routers (roleAttributes ∋ "org-<org>").
func filterRouters(all []ztEdgeRouter, org string) []ztEdgeRouter {
	role := orgRole(org)
	out := make([]ztEdgeRouter, 0, len(all))
	for _, r := range all {
		if hasRole(r.RoleAttributes, role) {
			out = append(out, r)
		}
	}
	return out
}

// ---- mapping (PURE) ----

// toMeshView maps a ZT edge service to the console mesh row. mtls is "required"
// when the service mandates end-to-end encryption, else "enabled" (the Ziti fabric
// always mutually authenticates every link — it is never truly off). status is
// "active": a listed service is a configured, dialable mesh entry.
func toMeshView(s ztService) meshView {
	mtls := "enabled"
	if s.EncryptionRequired {
		mtls = "required"
	}
	return meshView{
		ID:      s.ID,
		Service: s.Name,
		Mtls:    mtls,
		Status:  "active",
	}
}

// routerStatus is the REAL edge-router health: online > disabled > offline. A
// disabled router is reported as such (not "offline"), an enabled-but-not-connected
// router is "offline", and a connected router is "online".
func routerStatus(r ztEdgeRouter) string {
	switch {
	case r.IsOnline:
		return "online"
	case r.Disabled:
		return "disabled"
	default:
		return "offline"
	}
}

// regionOf returns the router's region from a "region-<slug>" role attribute, or ""
// when it carries none (the view then omits region and the UI shows "—").
func regionOf(r ztEdgeRouter) string {
	for _, a := range r.RoleAttributes {
		if strings.HasPrefix(a, regionRolePrefix) {
			if slug := strings.TrimPrefix(a, regionRolePrefix); slug != "" {
				return slug
			}
		}
	}
	return ""
}

// toEdgeNodeView maps a ZT edge-router to the console edge row.
func toEdgeNodeView(r ztEdgeRouter) edgeNodeView {
	name := r.Name
	if name == "" {
		name = r.ID
	}
	return edgeNodeView{
		ID:     r.ID,
		Name:   name,
		Region: regionOf(r),
		Status: routerStatus(r),
	}
}

// networkFromRouters projects the org's edge-routers into its overlay network view,
// or nil when the org has no routers (no nodes → no network → an honest empty list,
// never a fabricated overlay). nodes is the real router count; status is
// "connected" when at least one router is online, else "provisioning" (routers
// exist but none has dialed home yet). id/name are derived deterministically from
// the org so /v1/networks/:id round-trips.
func networkFromRouters(org string, routers []ztEdgeRouter) *networkView {
	if len(routers) == 0 {
		return nil
	}
	status := "provisioning"
	for _, r := range routers {
		if r.IsOnline {
			status = "connected"
			break
		}
	}
	return &networkView{
		ID:     networkID(org),
		Name:   org,
		Status: status,
		Nodes:  len(routers),
	}
}

// networkID is the stable, org-derived id for the org's overlay network — the key
// the /v1/networks/:id route addresses.
func networkID(org string) string { return orgRolePrefix + org }
