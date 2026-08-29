// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"
	"sort"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	iamschema "github.com/hanzoai/iam/pkg/schema"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

// Who may act in one scope of an org, and who may be added — published on the
// internal plane.
//
// A subsystem showing a roster asks this instead of keeping one. The scope is an
// org, a workspace inside it, or a project inside that; the ORG is always the
// caller's and is never an argument.

// exposeMembers publishes the roster read and the grant write. Mount calls it.
func exposeMembers() {
	zip.Post[plane.Scope, plane.Memberships](cloud.Plane(), "/iam/members",
		members,
		zip.WithOperationID(plane.IAMMembers),
		zip.WithSummary("Who may act in one scope of the caller's org"))
	zip.Post[plane.GrantIn, struct{}](cloud.Plane(), "/iam/grant",
		grant,
		zip.WithOperationID(plane.IAMGrant),
		zip.WithSummary("Record that a user may act in a scope of the caller's org"))
}

// members lists the grants in one scope, each with the display name IAM holds for
// the person — so a caller never keeps a copy of somebody's name to show it.
func members(ctx context.Context, in *plane.Scope) (*plane.Memberships, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("members: no org on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("members: identity store not open in the process that owns it")
	}
	q := orm.TypedQuery[iamschema.Membership](db).Filter("Org=", org)
	if in != nil {
		q = q.Filter("Workspace=", in.Workspace).Filter("Project=", in.Project)
	}
	rows, err := q.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	out := make([]plane.Membership, 0, len(rows))
	for _, m := range rows {
		if m == nil || m.User == "" {
			continue
		}
		out = append(out, plane.Membership{User: m.User, Role: m.Role, Name: displayName(db, m.User)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return &plane.Memberships{Memberships: out}, nil
}

// displayName is the name IAM holds, or the user id when it holds none. It never
// errors: a roster that cannot render a name still renders the person.
func displayName(db orm.DB, user string) string {
	u, err := iamstore.GetUserBySubject(context.Background(), db, user)
	if err != nil || u == nil || u.DisplayName == "" {
		return user
	}
	return u.DisplayName
}

// grant records one membership in the caller's org, idempotently.
func grant(ctx context.Context, in *plane.GrantIn) (*struct{}, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("grant: no org on the call")
	}
	if in == nil || in.User == "" {
		return nil, zip.ErrBadRequest("grant: a user is required")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("grant: identity store not open in the process that owns it")
	}
	role := in.Role
	if role == "" {
		role = "member"
	}
	if _, err := iamstore.EnsureMembershipIn(ctx, db, in.User, org, in.Workspace, in.Project, role); err != nil {
		return nil, fmt.Errorf("grant: %w", err)
	}
	return &struct{}{}, nil
}
