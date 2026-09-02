// types.go holds the ZT wire structs (what the ZT Edge Management API
// returns) and the console view structs (what this subsystem emits), plus the PURE
// mapping between them. The view JSON keys mirror the console modules EXACTLY so the
// Networks, Service Mesh and Edge pages render with no front-end change:
//
//   - networkView  -> console NetworksModule.tsx   BootnodeNetwork {id,name,chain,status,nodes,rpc}
//   - meshView     -> console ServiceMeshModule.tsx MeshService     {id,service,namespace,mtls,requests,status}
//   - routerView   -> console RoutersModule.tsx     Router          {id,name,region,status,requests,latency}
//
// Every field is a REAL ZT value or an honest omission. Telemetry ZT's management
// API does not carry (per-service request counts, per-router latency) is left off
// the view so the UI renders "—", never a fabricated 0.
//
// TENANT ISOLATION. ZT (ZT) has no native org tenancy; services and
// edge-routers are scoped by their `roleAttributes` — the SAME first-class strings
// ZT uses to drive service and edge-router policies. The org boundary is therefore
// the role attribute "org-<org>": a resource belongs to a tenant iff its
// roleAttributes contains that exact string (the org is the validated IAM owner,
// used verbatim). List/get filter to the caller's role, so one tenant can never see
// another's services or nodes. An untagged resource belongs to NO org and is
// invisible to every tenant — honest-empty over a cross-tenant leak.

package network

import (
	"fmt"
	"slices"
	"strings"
)

// orgRolePrefix + regionRolePrefix are the ONE role-attribute conventions this
// subsystem reads. "org-<org>" is the tenant key; "region-<slug>" optionally
// surfaces an edge-router's region (honest "—" when a router carries none).
const (
	orgRolePrefix    = "org-"
	regionRolePrefix = "region-"
)

// orgRole is the role attribute that marks a ZT resource as belonging to org.
func orgRole(org string) string { return orgRolePrefix + org }

// ---- ZT wire structs (ZT rest_model subset, JSON as the API emits) ----

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

// ztIdentity mirrors the Edge Management API IdentityDetail (JSON subset). Its
// enrollment carries the one-time token the controller minted at create, gone
// once the device spends it.
type ztIdentity struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	RoleAttributes []string `json:"roleAttributes"`
	Enrollment     struct {
		Ott *struct {
			JWT       string `json:"jwt"`
			ExpiresAt string `json:"expiresAt"`
		} `json:"ott"`
	} `json:"enrollment"`
}

// ztCreated is the controller's create envelope: the new resource's id at data.id.
type ztCreated struct {
	Data struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ztOne is the controller's single-resource envelope.
type ztOne[T any] struct {
	Data T `json:"data"`
}

// ---- fabric naming (the write half's ONE convention) ----

// A fabric name is global and a caller's name is per-org, so everything the
// write half puts on the fabric carries the org as a dotted suffix: identity
// "laptop" of org acme is "laptop.acme", service "k3s" is "k3s.acme", and the
// service's DNS is its fabric name plus ".zt". The fleet's dialer
// (apps/fleet/zt.go) strips that one suffix to get the fabric name back, and
// nothing anywhere parses further.

// ztDNSSuffix is what turns a fabric service name into the name the fabric's
// DNS answers for it.
const ztDNSSuffix = ".zt"

// scoped is the fabric spelling of an org's name — for identities, services,
// and the role attributes a caller supplies (a role another tenant's policy
// selects must not be claimable, so it is scoped exactly like a name).
func scoped(name, org string) string { return name + "." + org }

// short is the caller's spelling of a fabric name: the org suffix stripped when
// present. A resource named outside this surface keeps its fabric name, which
// is the honest answer.
func short(name, org string) string { return strings.TrimSuffix(name, "."+org) }

// label admits the names the write half will put on the fabric and into DNS: a
// DNS label, lower-cased — so "<name>.<org>.zt" is always well-formed and a
// name can never forge or split the org suffix beside it.
func label(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 63 {
		return "", fmt.Errorf("name must be 1-63 characters")
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			r == '-' && i > 0 && i < len(s)-1
		if !ok {
			return "", fmt.Errorf("name must be a DNS label: lowercase letters, digits and inner hyphens")
		}
	}
	return s, nil
}

// ---- console view structs ----

// networkView is the shape console NetworksModule (BootnodeNetwork) consumes. It
// represents the org's slice of the ZT overlay — one network whose nodes are the
// org's edge-routers. chain/rpc are honestly omitted (a ZT overlay is not a
// blockchain with an RPC), so those columns render blank rather than fabricated.
type networkView struct {
	// ID is the org-derived id of the overlay network — the key
	// GET /v1/network/{id} addresses.
	ID string `json:"id"`
	// Name is the org the overlay belongs to.
	Name string `json:"name"`
	// Status is "connected" once at least one of the org's edge-routers is
	// online, else "provisioning" (routers exist but none has dialed home).
	Status string `json:"status"`
	// Nodes is how many edge-routers the org has on the fabric.
	Nodes int `json:"nodes"`
}

// meshView is the shape console ServiceMeshModule (MeshService) consumes — one row
// per ZT edge service. mtls reflects the service's E2E encryption requirement;
// requests is omitted (the management API carries no per-service metrics).
type meshView struct {
	// ID is the ZT edge service's id.
	ID string `json:"id"`
	// Service is the edge service's name.
	Service string `json:"service"`
	// Mtls is "required" when the service mandates end-to-end encryption, else
	// "enabled" — the fabric mutually authenticates every link, so it is never
	// truly off.
	Mtls string `json:"mtls"`
	// Status is "active": a listed service is a configured, dialable mesh entry.
	Status string `json:"status"`
}

// routerView is the shape console RoutersModule (Router) consumes — one row per ZT
// edge-router. status is the REAL online/disabled/offline signal; region is filled
// only from a "region-<slug>" role attribute (honest "—" otherwise); requests and
// latency are omitted (no per-router telemetry in the management API).
type routerView struct {
	// ID is the ZT edge-router's id.
	ID string `json:"id"`
	// Name is the edge-router's name, falling back to its id when it has none.
	Name string `json:"name"`
	// Region comes from a "region-<slug>" role attribute and is omitted when the
	// router carries none, so the column renders "—" rather than a guess.
	Region string `json:"region,omitempty"`
	// Status is the controller's own health signal: "online" when connected,
	// "disabled" when administratively disabled, "offline" otherwise.
	Status string `json:"status"`
}

// ---- tenant filtering (PURE) ----

// hasRole reports whether attrs contains the exact role attribute want.
func hasRole(attrs []string, want string) bool {
	return slices.Contains(attrs, want)
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

// filterIdentities keeps only the org's identities (roleAttributes ∋ "org-<org>").
func filterIdentities(all []ztIdentity, org string) []ztIdentity {
	role := orgRole(org)
	out := make([]ztIdentity, 0, len(all))
	for _, id := range all {
		if hasRole(id.RoleAttributes, role) {
			out = append(out, id)
		}
	}
	return out
}

// ---- mapping (PURE) ----

// toMeshView maps a ZT edge service to the console mesh row. mtls is "required"
// when the service mandates end-to-end encryption, else "enabled" (the ZT fabric
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
		if after, ok := strings.CutPrefix(a, regionRolePrefix); ok {
			if slug := after; slug != "" {
				return slug
			}
		}
	}
	return ""
}

// toRouterView maps a ZT edge-router to the console router row.
func toRouterView(r ztEdgeRouter) routerView {
	name := r.Name
	if name == "" {
		name = r.ID
	}
	return routerView{
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
// the org so /v1/network/:id round-trips.
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
// the /v1/network/:id route addresses.
func networkID(org string) string { return orgRolePrefix + org }

// toIdentityView maps a ZT identity to the org's view of it: the name in the
// caller's own spelling, the roles verbatim as the fabric holds them, and the
// one-time enrollment only while the identity still has one.
func toIdentityView(id ztIdentity, org string) *identityView {
	v := &identityView{ID: id.ID, Name: short(id.Name, org), Roles: id.RoleAttributes}
	if ott := id.Enrollment.Ott; ott != nil && ott.JWT != "" {
		v.Enrollment = &enrollmentView{JWT: ott.JWT, ExpiresAt: ott.ExpiresAt}
	}
	return v
}
