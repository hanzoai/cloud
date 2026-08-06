// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/zap-proto/zip"
)

// An org's projects, published on the internal plane.
//
// A project belongs to IAM — it is the org-scoped container every PaaS app is
// scoped to — and platform is the peer that has to read it. When platform and
// IAM share a binary that read is a Go call into the store. When they do not,
// platform reached for the ONE thing a process boundary left it: an HTTP GET to
// /v1/iam/projects, authenticated as a machine identity platform minted per org
// so IAM would admit the read.
//
// That credential is what this deletes. Nothing about "which projects does this
// org own" needs an identity minted for it: the plane call already carries the
// caller's tenant, asserted by the gateway and forwarded, so the org is not
// something the caller can name and therefore not something a grant has to fence.
// The narrowest grant is the one that was never issued — and a per-org client
// secret sealed into KMS to answer a list query was a lot of machinery standing
// in for a tenant the call was already carrying.

// exposeProjects publishes the project read. Mount calls it.
//
// Named handler, not a closure, for the reason exposeRoster's is: zipdoc lifts an
// op's prose off its handler's doc comment and can lift nothing from an anonymous
// one.
func exposeProjects() {
	zip.Post[struct{}, plane.Projects](cloud.Plane(), "/iam/projects",
		projects,
		zip.WithOperationID(plane.IAMProjects),
		zip.WithSummary("Projects this org owns"))
}

// projects answers with the projects owned by the CALLER'S OWN org.
//
// The org comes from the authenticated call and can never be an argument. Owner
// is the tenancy key of the whole project table, so a caller able to pass it
// could list another tenant's work — which is exactly the hole the HTTP client
// this replaces had to mint a per-org credential to close.
//
// The projection is narrow on purpose: five fields are what it takes to key,
// name and date a project. Returning the record itself would put IAM's metadata
// and workspace columns on a wire whose layout is positional, so every field
// here is one the contract can never reorder.
//
// It fails closed on a store that is not open. This process owns the store, so a
// nil handle is a boot-order fault, and an empty list would read as "this org has
// no projects" — a lie that a caller would act on by offering to create one that
// already exists.
func projects(ctx context.Context, _ *struct{}) (*plane.Projects, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("projects: no org on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("projects: identity store not open in the process that owns it")
	}
	rows, err := iamstore.GetOrganizationProjects(db, org)
	if err != nil {
		return nil, fmt.Errorf("projects: %w", err)
	}
	out := make([]plane.Project, 0, len(rows))
	for _, p := range rows {
		if p == nil {
			continue
		}
		out = append(out, plane.Project{
			Owner:       p.Owner,
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Description: p.Description,
			CreatedTime: p.CreatedTime,
		})
	}
	return &plane.Projects{Projects: out}, nil
}
