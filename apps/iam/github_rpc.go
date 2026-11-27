// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	iamschema "github.com/hanzoai/iam/pkg/schema"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

// maxGitHubPeople bounds the ids one call names.
const maxGitHubPeople = 500

// exposeGitHub publishes the GitHub-account read. Mount calls it.
func exposeGitHub() {
	cloud.Plane().Post("/iam/github_people",
		githubPeople,
		zip.WithOperationID(client.IAMGitHubPeople),
		zip.WithSummary("The IAM person who linked each GitHub account named"))
}

// githubPeople resolves GitHub user ids to the people who linked them. IAM's
// federation writes the numeric GitHub user id on the user row at sign-in; that is
// the id a webhook carries, so a pull request's author is matched by a value GitHub
// signed and the person cannot type. An id nobody linked is left out, and earns
// nothing downstream.
//
// The filter is "github=", the column's JSON name: "GitHub=" matches no row
// (TestGitHubPeopleNamesTheOldestLivePersonWhoLinkedAnAccount seeds the rows the way
// IAM writes them and finds none under it).
//
// One GitHub account can be linked by more than one user row (a person signed in to
// two brands). The oldest live person row stands for it, so a repeat sign-in never
// moves credit to a newer account. A deleted, barred or non-person row (a service
// account, an application) never stands for one.
//
// It reads across tenants, so only the platform asks.
func githubPeople(ctx context.Context, in *client.GitHubPeopleIn) (*client.GitHubPeople, error) {
	if cloud.Who(ctx).Org != authz.AdminOrg {
		return nil, zip.ErrForbidden("github people: the platform asks")
	}
	if in == nil || len(in.IDs) > maxGitHubPeople {
		return nil, zip.ErrBadRequest(fmt.Sprintf("github people: at most %d ids", maxGitHubPeople))
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("github people: identity store not open in the process that owns it")
	}
	out := &client.GitHubPeople{People: []client.GitHubPerson{}}
	seen := map[string]bool{}
	for _, id := range in.IDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		rows, err := orm.TypedQuery[iamschema.User](db).Filter("github=", id).Limit(20).GetAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("github people: %w", err)
		}
		rows = slices.DeleteFunc(rows, func(u *iamschema.User) bool {
			return u == nil || u.IsDeleted || u.IsForbidden || (u.Type != "" && u.Type != "normal-user")
		})
		if len(rows) == 0 {
			continue
		}
		slices.SortFunc(rows, func(a, b *iamschema.User) int {
			return cmp.Or(cmp.Compare(a.CreatedTime, b.CreatedTime), cmp.Compare(a.Id, b.Id))
		})
		out.People = append(out.People, client.GitHubPerson{ID: id, Subject: rows[0].Id, Org: rows[0].Owner})
	}
	return out, nil
}
