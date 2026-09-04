// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
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
	zip.Post[struct{}, client.Roles](cloud.Plane(), "/iam/roles",
		roles,
		zip.WithOperationID(client.IAMRoles),
		zip.WithSummary("The caller's role names in their own org"))
}

// roles resolves the caller's effective roles: the org grant they hold, plus every
// role naming them directly or through a team.
//
// An org owner or admin is a System Manager, which is what makes an org
// administrable the moment it exists — the grant IAM already records, rather than
// a first-caller-wins seed in whichever subsystem was reached first.
//
// It fails closed on a store that is not open: this process owns the store, so a
// nil handle is a boot-order fault, and an empty set would read as a member with
// no grants — a refusal the caller would blame on their own permissions.
func roles(ctx context.Context, _ *cloud.Unit) (*client.Roles, error) {
	who := cloud.Who(ctx)
	if who.Org == "" || who.User == "" {
		return nil, zip.ErrUnauthorized("roles: no caller on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("roles: identity store not open in the process that owns it")
	}

	out := []string{}
	if m, err := iamstore.GetMembership(ctx, db, who.User, who.Org); err == nil && m != nil {
		switch m.Role {
		case "owner", "admin":
			out = append(out, "System Manager")
		}
	}
	// An operator-seeded admin holds the grant on the USER row (`isAdmin`), not
	// on a membership — provisioning writes no membership rows, and its own
	// contract says org authority is that bit in the home org. Without this
	// read, a fresh org's seeded administrator could not install a module or
	// define a DocType anywhere: an org that exists but cannot be administered.
	if !slices.Contains(out, "System Manager") {
		name := strings.TrimPrefix(who.User, who.Org+"/")
		if rows, err := orm.TypedQuery[iamschema.User](db).Filter("Owner=", who.Org).Filter("Name=", name).Limit(1).GetAll(ctx); err == nil &&
			len(rows) == 1 && rows[0].IsAdmin && !rows[0].IsDeleted && !rows[0].IsForbidden {
			out = append(out, "System Manager")
		}
	}

	teams, err := orm.TypedQuery[iamschema.Team](db).Filter("owner", who.Org).GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("roles: read teams: %w", err)
	}
	mine := map[string]bool{}
	for _, t := range teams {
		if t != nil && slices.Contains(t.Users, who.User) {
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
		if slices.Contains(r.Users, who.User) || slices.ContainsFunc(r.Teams, func(t string) bool { return mine[t] }) {
			out = append(out, r.Name)
		}
	}
	slices.Sort(out)
	return &client.Roles{Roles: slices.Compact(out)}, nil
}
