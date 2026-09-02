// identity.go — fabric identities: how a DEVICE the org brings gets onto its
// overlay.
//
// A BYO machine — a k3s cluster in a microVM on a laptop, a GPU box under a
// desk — has no routable address and needs none. Its owner mints an identity
// here, enrolls the device with the one-time JWT the answer carries, and from
// then on the device holds a fabric credential of its own: it can dial the
// org's published services, and with a "<service>-host" role it can HOST one
// (publish.go writes the bind policy that selects exactly that attribute).
//
// TENANCY is the same one rule the read half filters by. Every identity minted
// here carries the "org-<org>" role attribute of the VALIDATED caller, and every
// extra role a caller supplies is scoped to that org on the way in — so no
// request can mint an identity another tenant's policy selects, and the list
// and delete below see only the org's own.
//
// THE ENROLLMENT JWT IS NOT A SECRET OF OURS. The controller mints it, it
// authenticates exactly one enrollment, and it lapses on its own; this surface
// carries it to the caller and stores nothing.
package network

import (
	"github.com/hanzoai/cloud"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/zap-proto/zip"
)

// identityIn is what POST /v1/network/identities takes.
type identityIn struct {
	// Name is the device's name within the org — a DNS label. The fabric knows
	// the identity as "<name>.<org>"; every answer here uses the caller's name.
	Name string `json:"name"`
	// Roles are extra role attributes for the identity, each scoped to the
	// caller's org on the way in ("k3s-host" is written as "k3s-host.<org>") so
	// no caller can claim an attribute another tenant's policy selects. A role
	// of the form "<service>-host" makes this identity a HOST of that published
	// service — the bind policy from POST /v1/network/services selects exactly
	// that attribute — and is refused when the org has no such service.
	Roles []string `json:"roles,omitempty"`
}

// enrollmentView is the identity's one-time enrollment, while it has one.
type enrollmentView struct {
	// JWT is the one-time enrollment token the device presents ONCE to join the
	// fabric (zt edge enroll / zt-edge-tunnel enroll). Spent or lapsed, it
	// authenticates nothing; this surface stores it nowhere.
	JWT string `json:"jwt"`
	// ExpiresAt is when the un-used token lapses, RFC 3339.
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// identityView is one fabric identity of the org's.
type identityView struct {
	// ID is the identity's fabric id — the key DELETE addresses.
	ID string `json:"id"`
	// Name is the identity's name within the org.
	Name string `json:"name"`
	// Roles are the identity's role attributes as the fabric holds them: the
	// org's own "org-<org>" plus any org-scoped roles it was minted with.
	Roles []string `json:"roles,omitempty"`
	// Enrollment is present only while the identity holds an un-used one-time
	// token — on create, and on a listed identity that has not yet enrolled, so
	// a mislaid JWT can be read again until it is spent or lapses.
	Enrollment *enrollmentView `json:"enrollment,omitempty"`
}

// identityList is the GET /v1/network/identities envelope.
type identityList struct {
	// Identities is one row per fabric identity tagged with the caller's org role.
	Identities []identityView `json:"identities"`
}

// identityRef addresses one identity by the id the list returns.
type identityRef struct {
	// ID is the identity id from the path. The URL is the addressing authority,
	// so it binds from there whatever else the request carries.
	ID string `json:"id"`
}

// createIdentity mints a fabric identity for a device the caller's org brings.
//
// The identity is created of type Device, tagged with the org's "org-<org>" role
// attribute plus any supplied roles — each scoped to the org, and a
// "<service>-host" role refused unless the org has published that service. The
// answer carries the controller's one-time enrollment JWT: the device presents
// it once to join the fabric, and until it does the same token can be read back
// off GET /v1/network/identities.
//
// A write, so it does not degrade: an unconfigured deployment answers 503.
func (o ops) createIdentity(ctx context.Context, in *identityIn) (*identityView, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	name, err := label(in.Name)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	roles := []string{orgRole(org)}
	for _, r := range in.Roles {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" || r == orgRole(org) {
			continue
		}
		if svc, isHost := strings.CutSuffix(r, "-host"); isHost {
			if err := o.orgService(ctx, org, svc); err != nil {
				return nil, err
			}
		}
		roles = append(roles, scoped(r, org))
	}
	body := map[string]any{
		"name":           scoped(name, org),
		"type":           "Device",
		"isAdmin":        false,
		"enrollment":     map[string]any{"ott": true},
		"roleAttributes": roles,
	}
	raw, err := s.State.cl.call(ctx, http.MethodPost, "/identities", "", body)
	if err != nil {
		return nil, err
	}
	var created ztCreated
	if err := json.Unmarshal(raw, &created); err != nil || created.Data.ID == "" {
		return nil, zip.Errorf(http.StatusBadGateway, "zt: create identity returned no id")
	}
	// The one-time token is minted BY the controller at create and lives on the
	// detail, so it is read back here — the moment this surface carries it.
	raw, err = s.State.cl.get(ctx, "/identities/"+url.PathEscape(created.Data.ID), "")
	if err != nil {
		return nil, err
	}
	var one ztOne[ztIdentity]
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "zt: decode identity: %v", err)
	}
	return toIdentityView(one.Data, org), nil
}

// listIdentities returns the fabric identities the caller's org owns.
//
// One row per identity tagged with the org's "org-<org>" role attribute — a
// device minted here, enrolled or not. An identity that has not yet enrolled
// still carries its one-time enrollment, so a mislaid JWT is read again here
// rather than re-minted.
//
// A tenancy read over the full inventory, so like the mesh list it does NOT
// degrade: an unconfigured deployment answers 503.
func (o ops) listIdentities(ctx context.Context, _ *noIn) (*identityList, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	all, err := listAll[ztIdentity](s.State.cl, ctx, "/identities")
	if err != nil {
		return nil, err
	}
	mine := filterIdentities(all, org)
	out := make([]identityView, 0, len(mine))
	for _, id := range mine {
		out = append(out, *toIdentityView(id, org))
	}
	return &identityList{Identities: out}, nil
}

// deleteIdentity removes one of the org's fabric identities. The device's
// credential stops authenticating and its enrollment, if unspent, stops
// enrolling.
//
// An id belonging to another org — or to nothing — is 404 before any write
// reaches the controller: whether an identity exists is itself a cross-tenant
// fact, and a delete may only ever act on what the caller could list.
func (o ops) deleteIdentity(ctx context.Context, in *identityRef) (*cloud.Unit, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	all, err := listAll[ztIdentity](s.State.cl, ctx, "/identities")
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(filterIdentities(all, org), func(z ztIdentity) bool { return z.ID == id }) {
		return nil, zip.ErrNotFound("identity not found")
	}
	if _, err := s.State.cl.call(ctx, http.MethodDelete, "/identities/"+url.PathEscape(id), "", nil); err != nil {
		return nil, err
	}
	return nil, nil
}
