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
// org, a space inside it, or a project inside that; the ORG is always the
// caller's and is never an argument.
//
// IAM spells the middle level `Workspace` — it is that schema's column and that
// service's route — and the estate spells it Space. The two meet HERE and only
// here: every field named Workspace below is hanzoai/iam's, every field named
// Space is ours, and the assignments between them are the whole translation. It
// collapses to one word when iam releases the rename; until then a second
// spelling inside cloud would be the drift, not the fix.

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
	zip.Post[struct{}, plane.Seats](cloud.Plane(), "/iam/seats",
		seats,
		zip.WithOperationID(plane.IAMSeats),
		zip.WithSummary("The caller's org's billable people"))
	zip.Post[plane.FederatedIn, plane.Federated](cloud.Plane(), "/iam/federated",
		federated,
		zip.WithOperationID(plane.IAMFederated),
		zip.WithSummary("The caller's org member behind an external identity"))
}

// federated resolves a member of the caller's org from an identity another
// provider issued. GitHub is the one provider today: IAM's federation writes the
// numeric GitHub user id onto the user row at sign-in, and that is the id a
// GitHub webhook carries — so the same value, and nothing a person typed,
// decides who a comment runs as. The org is the caller's, and the query is
// bounded to it, so an id that belongs to a person in another org resolves to
// nobody here.
func federated(ctx context.Context, in *plane.FederatedIn) (*plane.Federated, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("federated: no org on the call")
	}
	if in == nil || in.Subject == "" {
		return nil, zip.ErrBadRequest("federated: provider and subject are required")
	}
	if in.Provider != "github" {
		return nil, zip.ErrBadRequest("federated: only github is resolved here")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("federated: identity store not open in the process that owns it")
	}
	rows, err := orm.TypedQuery[iamschema.User](db).Filter("Owner=", org).Filter("GitHub=", in.Subject).Limit(2).GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("federated: %w", err)
	}
	if len(rows) != 1 || rows[0].IsDeleted || rows[0].IsForbidden {
		return &plane.Federated{}, nil
	}
	return &plane.Federated{User: rows[0].Id}, nil
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
		if in.User != "" {
			q = q.Filter("User=", in.User)
		}
		// Any is what separates "the org's own grants" from "every scope": an empty
		// Space is a real scope, so it cannot also mean unfiltered.
		if !in.Any {
			q = q.Filter("Workspace=", in.Space).Filter("Project=", in.Project)
		}
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
		out = append(out, plane.Membership{
			User: m.User, Role: m.Role, Name: displayName(db, m.User),
			Space: m.Workspace, Project: m.Project,
		})
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
func grant(ctx context.Context, in *plane.GrantIn) (*cloud.Unit, error) {
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
	if _, err := iamstore.EnsureMembershipIn(ctx, db, in.User, org, in.Space, in.Project, role); err != nil {
		return nil, fmt.Errorf("grant: %w", err)
	}
	return &struct{}{}, nil
}

// seats counts the caller's org's billable people.
//
// The error is PROPAGATED: a wallet reading zero seats under-bills silently,
// where a failure retries. This is the one read here that must not degrade.
func seats(ctx context.Context, _ *cloud.Unit) (*plane.Seats, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("seats: no org on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("seats: identity store not open in the process that owns it")
	}
	n, guests, err := iamstore.Seats(ctx, db, org)
	if err != nil {
		return nil, fmt.Errorf("seats: %w", err)
	}
	return &plane.Seats{Seats: n, Guests: guests}, nil
}
