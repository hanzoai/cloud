// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	iamschema "github.com/hanzoai/iam/pkg/schema"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

// The caller's roles in their own org, published on the internal plane.
//
// Every subsystem that enforces per-role permissions asks this instead of keeping
// a role table: identity, membership and grants are IAM's, and a second copy is a
// second answer to who may do what.

// exposeRoles publishes the role read. Mount calls it.
func exposeRoles() {
	cloud.Plane().Post("/iam/roles",
		roles,
		zip.WithOperationID(client.IAMRoles),
		zip.WithSummary("The caller's role names in their own org"))
}

// roles resolves the caller's effective roles: the org grant they hold, plus every
// role naming them directly or through a team.
//
// The caller arrives named by the token's subject, a UUID, while every grant read
// here names a person by the "<owner>/<name>" key: the membership relation, role
// rosters and team rosters. So the subject is resolved to its user row once,
// through the store's one subject decoder, and every read below keys on that row.
// A subject that resolves to no user (an application acting as itself) keeps its
// own "<owner>/<name>" subject as the key. A key presented at home carries its
// holder's bare username, which names their row in that org and nowhere else.
//
// Member says whether the caller may act in the org at all, by the membership
// set the gateway admits an org by; the roles below are narrower.
//
// Standing comes first. The caller holds nothing in an org that is neither their
// home nor one an org-wide membership admits them to, whatever a roster names.
// Within it, an org owner or admin is a System Manager, which is what makes an org
// administrable the moment it exists: the grant IAM already records, rather than a
// first-caller-wins seed in whichever subsystem was reached first.
//
// A SuperAdmin supporting a tenant is the third shape of the one check
// (HIP-0527 §2): superadmin(a) ∧ assumed(s) = o passes every need, so they act
// as the org's System Manager. The assume is the token's, not a row: IAM's
// POST /v1/iam/assume re-mints the SuperAdmin's own token with the org in its
// signed `orgs` and writes no membership, and records the entry on the
// tenant's privileged trail. The identity boundary reads that token into the
// caller — Admin from the SuperAdmin rule, OrgAdmin from the role `orgs` names
// for the acting org — so both bits together are a SuperAdmin whose token
// names this org, and a SuperAdmin who merely named the org in a header holds
// OrgAdmin false and is answered like anyone else with no standing. The account
// row must be a live SuperAdmin's: a disabled or dismissed one holds nothing
// here, whatever token it still carries.
//
// It fails closed on a store that is not open: this process owns the store, so a
// nil handle is a boot-order fault, and an empty set would read as a member with
// no grants, a refusal the caller would blame on their own permissions.
func roles(ctx context.Context, _ *cloud.Unit) (*client.Roles, error) {
	who := cloud.Who(ctx)
	if who.Org == "" || who.User == "" {
		return nil, zip.ErrUnauthorized("roles: no caller on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("roles: identity store not open in the process that owns it")
	}

	id, home, admin, member := who.User, false, false, false
	u, err := iamstore.GetUserBySubject(ctx, db, who.User)
	if err == nil && u == nil && who.Owner == who.Org && !strings.Contains(who.User, "/") {
		// A key presented at home names its holder by username alone, and only in
		// their own org does a bare username name them.
		u, err = iamstore.GetUserByName(ctx, db, who.Org, who.User)
	}
	if err != nil {
		return nil, fmt.Errorf("roles: resolve the caller: %w", err)
	}
	if u != nil {
		if u.IsDeleted || u.IsForbidden {
			return &client.Roles{Roles: []string{}}, nil
		}
		id, home, admin = u.Owner+"/"+u.Name, u.Owner == who.Org, u.IsAdmin
		// The membership set, as IAM mints it into the `orgs` claim — the home org
		// and every org a row names them in (store.MemberOrgRefs). It is read here
		// rather than through MemberOrgRefs, which answers a failed read with the
		// home org alone: that would say an invited member is no longer one, when
		// the truth is that nobody could tell.
		rows, err := iamstore.MembershipsByUser(ctx, db, id)
		if err != nil {
			return nil, fmt.Errorf("roles: read memberships: %w", err)
		}
		member = home || slices.ContainsFunc(rows, func(m *iamschema.Membership) bool { return m != nil && m.Org == who.Org })
		// The account row decides SuperAdmin (schema.User.SuperAdmin), and a
		// disabled one was refused above, whatever token it still carries.
		if u.SuperAdmin() && who.Admin && who.OrgAdmin && who.ActedBy == "" {
			return &client.Roles{Roles: []string{"System Manager"}, Admin: true, Member: true}, nil
		}
	}
	m, err := iamstore.MembershipIn(ctx, db, id, who.Org, "", "")
	if err != nil {
		return nil, fmt.Errorf("roles: read membership: %w", err)
	}
	if u == nil {
		// An application acting as itself holds what an org granted it.
		member = m != nil
	}
	if m == nil && !home {
		return &client.Roles{Roles: []string{}, Member: member}, nil
	}

	out := []string{}
	// An operator-seeded admin holds the grant on the USER row (`isAdmin`), not on
	// a membership: provisioning writes no membership rows, and its contract says
	// org authority is that bit in the home org.
	orgAdmin := (m != nil && (m.Role == iamstore.RoleOwner || m.Role == iamstore.RoleAdmin)) || (home && admin)
	if orgAdmin {
		out = append(out, "System Manager")
	}

	teams, err := orm.TypedQuery[iamschema.Team](db).Filter("owner", who.Org).GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("roles: read teams: %w", err)
	}
	mine := map[string]bool{}
	for _, t := range teams {
		if t != nil && slices.Contains(t.Users, id) {
			mine[t.Name] = true
		}
	}

	named, err := orm.TypedQuery[iamschema.Role](db).Filter("owner", who.Org).GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("roles: read roles: %w", err)
	}
	for _, r := range named {
		if r == nil || !r.IsEnabled {
			continue
		}
		if slices.Contains(r.Users, id) || slices.ContainsFunc(r.Teams, func(t string) bool { return mine[t] }) {
			out = append(out, r.Name)
		}
	}
	slices.Sort(out)
	// Admin is the grant itself, stated apart from the names: a tenant may name a
	// role "System Manager", and a caller that must know whether someone
	// administers the org — a mint on its GitHub installation — asks this.
	return &client.Roles{Roles: slices.Compact(out), Admin: orgAdmin, Owner: m != nil && m.Role == iamstore.RoleOwner, Member: member}, nil
}
