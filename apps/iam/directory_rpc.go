// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/iam/pkg/model"
	iamschema "github.com/hanzoai/iam/pkg/schema"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

// exposeDirectory publishes the directory read. Mount calls it.
func exposeDirectory() {
	cloud.Plane().Post("/iam/directory",
		directory,
		zip.WithOperationID(client.IAMDirectory),
		zip.WithSummary("Every org and person, as the operator console lists them"))
}

// directory answers with every organization and every live account in the
// identity store, each projected to what a directory row shows. An account
// carries its type and tag, so the console keeps the people and drops the rest.
//
// It is the source the operator console's read model is built from. The console
// used to ask IAM one question per org on every page it drew, which cost a round
// trip per row and a scan of the store per round trip; this is one read of each
// kind, made in the process that owns the store, a few times a minute.
//
// The platform alone may ask. It is every tenant at once, so an org-scoped
// caller, whose tenancy the whole identity store is keyed on, is refused.
//
// The projection carries no credential column: a password hash, a key or an MFA
// secret has no place on a wire whose purpose is a list.
func directory(ctx context.Context, _ *cloud.Unit) (*client.Directory, error) {
	if cloud.Who(ctx).Org != authz.AdminOrg {
		return nil, zip.ErrForbidden("directory: the platform reads it")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("directory: identity store not open in the process that owns it")
	}
	orgs, err := orm.TypedQuery[model.Organization](db).Filter("Owner=", authz.AdminOrg).GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("directory: orgs: %w", err)
	}
	users, err := orm.TypedQuery[model.User](db).GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("directory: users: %w", err)
	}
	grants, err := orm.TypedQuery[iamschema.Membership](db).GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("directory: members: %w", err)
	}
	out := &client.Directory{
		Orgs:    make([]client.DirectoryOrg, 0, len(orgs)),
		Users:   make([]client.DirectoryUser, 0, len(users)),
		Members: make([]client.DirectoryMember, 0, len(grants)),
	}
	for _, o := range orgs {
		if o == nil || o.Name == "" {
			continue
		}
		out.Orgs = append(out.Orgs, client.DirectoryOrg{Name: o.Name, Display: o.DisplayName, Created: o.CreatedTime})
	}
	for _, u := range users {
		if u == nil || u.IsDeleted || u.Name == "" {
			continue
		}
		out.Users = append(out.Users, client.DirectoryUser{
			ID: u.Id, Owner: u.Owner, Name: u.Name, Email: u.Email, Verified: u.EmailVerified, Display: u.DisplayName, Type: u.Type, Tag: u.Tag,
			Created: u.CreatedTime, LastSignin: u.LastSigninTime, Admin: u.IsAdmin, Forbidden: u.IsForbidden,
		})
	}
	// A person's grant anywhere in an org makes them its member; a team's grant names
	// no person here.
	seen := map[client.DirectoryMember]bool{}
	for _, g := range grants {
		if g == nil || g.User == "" || g.Org == "" {
			continue
		}
		m := client.DirectoryMember{User: g.User, Org: g.Org, Role: g.Role}
		if !seen[m] {
			seen[m] = true
			out.Members = append(out.Members, m)
		}
	}
	return out, nil
}
