// identity.go — fabric identities: who is on the org's overlay.
//
// A person or machine is on the fabric AS ITS IAM SUBJECT. The controller
// trusts Hanzo IAM as its only signer, so a caller logs in to it with its own
// IAM access token (POST /edge/client/v1/authenticate?method=ext-jwt) and is
// the identity whose externalId is that token's `sub`. There is nothing to
// enroll and no credential of the fabric's own: POST /v1/network/identities
// makes sure that identity exists, and from then on the caller's token is the
// way in — to dial the org's published services, and with a
// "<service>-host" role, which only a steward takes (network.go), to HOST one
// (publish.go writes the bind policy that selects exactly that attribute).
//
// TENANCY is the same one rule the read half filters by. Every identity
// ensured here carries the "org-<org>" role attribute of the VALIDATED caller,
// and every extra role a caller supplies is scoped to that org on the way in —
// so no request can grant an identity an attribute another tenant's policy
// selects, and the list and delete below see only the org's own.

package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"

	"github.com/zap-proto/zip"
)

// iamPolicy is the auth policy the controller bootstraps to admit only its
// Hanzo IAM signer — the one way in an identity ensured here may use.
const iamPolicy = "iam"

// identityIn is what POST /v1/network/identities takes. Every field is
// optional: the caller's own IAM subject is the identity.
type identityIn struct {
	// Name is the identity's name within the org — a DNS label, stored on the
	// fabric as "<name>.<org>". Absent, the identity is named by the caller's
	// IAM subject. It is set when the identity is first made; an identity that
	// already exists keeps its name.
	Name string `json:"name,omitempty"`
	// Roles are extra role attributes for the identity, each scoped to the
	// caller's org on the way in ("k3s-host" is written as "k3s-host.<org>") so
	// no caller can claim an attribute another tenant's policy selects. A role
	// of the form "<service>-host" makes this identity a HOST of that published
	// service — the bind policy from POST /v1/network/services selects exactly
	// that attribute — and is refused when the org has no such service. Only a
	// steward (an admin of the org or its own machine client) takes roles; a
	// plain member asking for one is 403.
	Roles []string `json:"roles,omitempty"`
}

// identityView is one fabric identity of the org's.
type identityView struct {
	// ID is the identity's fabric id — the key DELETE addresses.
	ID string `json:"id"`
	// Name is the identity's name within the org.
	Name string `json:"name"`
	// ExternalID is the IAM subject the identity logs in as: the controller
	// admits a bearer whose `sub` equals it.
	ExternalID string `json:"externalId"`
	// Roles are the identity's role attributes as the fabric holds them: the
	// "org-<org>" of every org it was ensured in, plus any org-scoped roles.
	Roles []string `json:"roles,omitempty"`
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

// ensureIdentity puts the caller on the org's overlay as its own IAM subject.
//
// The identity is the one whose externalId is the caller's `sub`, admitted by
// the controller's "iam" auth policy: the caller logs in to the fabric with its
// own IAM access token, and nothing is enrolled. It is named by the subject
// unless a name is given, and carries the org's "org-<org>" role attribute plus
// any supplied roles — each scoped to the org, and a "<service>-host" role
// refused unless the org has published that service. Roles are a steward's to
// take: a plain member joins with the org role alone, and asking for more is
// 403 before the controller is written.
//
// An API key is refused 403: the fabric admits an IAM access token, a person's
// or an application's, and a key has none to log in with.
//
// IDEMPOTENT: a caller who already has an identity gets the same one back, with
// any of these roles it lacked added — which is how one person's identity comes
// to serve every org they act in.
//
// A write, so it does not degrade: an unconfigured deployment answers 503.
func (o ops) ensureIdentity(ctx context.Context, in *identityIn) (*identityView, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.Roles) > 0 && !steward(ctx) {
		return nil, errSteward("taking a role on the network")
	}
	name := sub
	if strings.TrimSpace(in.Name) != "" {
		n, err := label(in.Name)
		if err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
		name = scoped(n, org)
	}
	roles := []string{orgRole(org)}
	for _, r := range in.Roles {
		// A role is a label and never a tenancy role, so the one it scopes to
		// ("<role>.<org>") can be told apart from every other org's.
		r, err := label(r)
		if err != nil || strings.HasPrefix(r, orgRolePrefix) {
			return nil, zip.ErrBadRequest("each role must be a DNS label not beginning with " + orgRolePrefix)
		}
		if svc, isHost := strings.CutSuffix(r, "-host"); isHost {
			if err := o.orgService(ctx, org, svc); err != nil {
				return nil, err
			}
		}
		roles = append(roles, scoped(r, org))
	}
	id, err := s.State.cl.ensure(ctx, sub, name, roles)
	if err != nil {
		return nil, err
	}
	return toIdentityView(id, org), nil
}

// ensure makes sure the identity whose externalId is sub exists and carries
// roles: made, named name, when there is none, and patched when it lacks any of
// them. It answers the identity as the controller then holds it.
func (cl *client) ensure(ctx context.Context, sub, name string, roles []string) (ztIdentity, error) {
	// The subject is JSON-quoted into the filter, which is the controller's
	// string-literal grammar.
	q, _ := json.Marshal(sub)
	raw, err := cl.get(ctx, "/identities", url.Values{"filter": {"externalId=" + string(q)}}.Encode())
	if err != nil {
		return ztIdentity{}, err
	}
	var found ztPage[ztIdentity]
	if err := json.Unmarshal(raw, &found); err != nil {
		return ztIdentity{}, zip.Errorf(http.StatusBadGateway, "zt: decode identities: %v", err)
	}

	if len(found.Data) == 0 {
		raw, err := cl.call(ctx, http.MethodPost, "/identities", "", map[string]any{
			"name":           name,
			"type":           "Default",
			"isAdmin":        false,
			"externalId":     sub,
			"authPolicyId":   iamPolicy,
			"roleAttributes": roles,
		})
		if err != nil {
			return ztIdentity{}, err
		}
		var created ztCreated
		if err := json.Unmarshal(raw, &created); err != nil || created.Data.ID == "" {
			return ztIdentity{}, zip.Errorf(http.StatusBadGateway, "zt: create identity returned no id")
		}
		return ztIdentity{ID: created.Data.ID, Name: name, ExternalID: sub, RoleAttributes: roles}, nil
	}

	id := found.Data[0]
	have := len(id.RoleAttributes)
	for _, r := range roles {
		if !slices.Contains(id.RoleAttributes, r) {
			id.RoleAttributes = append(id.RoleAttributes, r)
		}
	}
	if len(id.RoleAttributes) > have {
		if _, err := cl.call(ctx, http.MethodPatch, "/identities/"+url.PathEscape(id.ID), "",
			map[string]any{"roleAttributes": id.RoleAttributes}); err != nil {
			return ztIdentity{}, err
		}
	}
	return id, nil
}

// subject is the caller's IAM `sub` verbatim, as the identity boundary attested
// it — the claim the controller's ext-jwt login matches externalId against. A
// caller with no attested subject is refused: it has no token the fabric would
// admit, so there is nothing to make an identity for.
//
// Only the two credentials that log in to the fabric hold an identity on it: a
// person's IAM token, whose `sub` is a UUID, and an application's, whose `sub`
// IAM signs as `<owner>/<name>` — two shapes that never meet. An API KEY is
// refused by Kind: the controller never sees one, and its subject is its
// holder's row, `<owner>/<name>`, the very string IAM signs for an application
// named after that holder. A key that made, listed or removed an identity by it
// would make, list or remove that application's fabric login.
func subject(ctx context.Context) (string, error) {
	if c, ok := cloud.Request(ctx); ok {
		if p, ok := principal.Minted(c); ok && p.Subject != "" {
			switch p.Kind {
			case principal.Person, principal.App:
				return p.Subject, nil
			case principal.Key:
				return "", zip.ErrForbidden("an API key holds no identity on the network: the fabric admits an IAM access token")
			}
		}
	}
	return "", principal.RefusedFrom(ctx)
}

// listIdentities returns the fabric identities the caller's org owns.
//
// One row per identity tagged with the org's "org-<org>" role attribute, each
// naming the IAM subject it logs in as. A steward sees every one; a plain member
// sees only its own, since who else is on the org's network is not a member's
// to read, and a plain member's API key, which holds no identity, is refused.
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
	mine, err := visible(ctx, filterIdentities(all, org))
	if err != nil {
		return nil, err
	}
	out := make([]identityView, 0, len(mine))
	for _, id := range mine {
		out = append(out, *toIdentityView(id, org))
	}
	return &identityList{Identities: out}, nil
}

// visible narrows the org's identities to the ones the caller may see and act
// on: all of them for a steward, the caller's own for a plain member. A member
// with no attested subject owns none.
func visible(ctx context.Context, ids []ztIdentity) ([]ztIdentity, error) {
	if steward(ctx) {
		return ids, nil
	}
	sub, err := subject(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ids, func(z ztIdentity) bool { return z.ExternalID != sub }), nil
}

// deleteIdentity takes one of the org's fabric identities out of the org.
//
// The identity's "org-<org>" role and every role scoped to the org are
// removed and the rest is left alone, so an identity another org shares keeps
// working there. The identity itself is deleted when no org role is left on
// it, or when the caller IS its IAM subject — a person may always remove
// themselves.
//
// An id belonging to another org — or to nothing — is 404 before any write
// reaches the controller: whether an identity exists is itself a cross-tenant
// fact, and a delete may only ever act on what the caller could list, which for
// a plain member is its own identity. A fabric administrator is the
// controller's own and is refused.
func (o ops) deleteIdentity(ctx context.Context, in *identityRef) (*cloud.Unit, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	all, err := listAll[ztIdentity](s.State.cl, ctx, "/identities")
	if err != nil {
		return nil, err
	}
	mine, err := visible(ctx, filterIdentities(all, org))
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(mine, func(z ztIdentity) bool { return z.ID == strings.TrimSpace(in.ID) })
	if i < 0 {
		return nil, zip.ErrNotFound("identity not found")
	}
	id := mine[i]
	if id.IsAdmin {
		return nil, zip.ErrForbidden("a fabric administrator is kept by the controller")
	}
	path := "/identities/" + url.PathEscape(id.ID)
	kept := slices.DeleteFunc(slices.Clone(id.RoleAttributes), func(r string) bool { return ownedBy(r, org) })
	sub, _ := subject(ctx)
	if (sub != "" && sub == id.ExternalID) || !slices.ContainsFunc(kept, func(r string) bool { return strings.HasPrefix(r, orgRolePrefix) }) {
		_, err = s.State.cl.call(ctx, http.MethodDelete, path, "", nil)
	} else {
		_, err = s.State.cl.call(ctx, http.MethodPatch, path, "", map[string]any{"roleAttributes": kept})
	}
	return nil, err
}
