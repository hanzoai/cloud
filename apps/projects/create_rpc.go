package projects

// create_rpc.go — a project made for the caller by another app.
//
// A coding run asked for with no repository starts a project of its own, and the
// run is the agent app's while the project store is this one's. So the create is
// an op on the plane, and it is the SAME create: [draft] validates the row and
// [persist] writes it and gives it its source ([born]), exactly as POST
// /v1/project does.

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeCreate publishes the create on the internal plane. Called from Mount.
func exposeCreate() {
	cloud.Plane().Post("/projects/create", planeCreate,
		zip.WithOperationID(client.ProjectsCreate),
		zip.WithSummary("Create a project in the caller's org under an exact slug"))
}

// planeCreate makes one project in the CALLER's org, under exactly the slug asked
// for.
//
// The org is the caller's plane identity and never an argument, as for every op
// here, and it passes the same path check org() applies to a request, because it
// becomes the same S3 key segment.
//
// The project is PUBLIC. Private is paid for by the request that asks for it
// ([resolve]), and a plane call has no request to charge, so this op does not
// offer the choice rather than make it for somebody.
//
// The slug is never renumbered. One the org already holds is the 409 POST
// /v1/project answers, and the caller picks the next: it is the caller that knows
// what else the name has to be free of.
func planeCreate(ctx context.Context, in *client.ProjectIn) (*client.ProjectOut, error) {
	org := cloud.Who(ctx).Org
	if org == "" || !orgPathSafe(org) {
		return nil, zip.ErrForbidden("projects create: org required")
	}
	s := mounted
	if s == nil || s.State.store == nil {
		return nil, zip.ErrInternal("projects: this process does not own the project store")
	}
	body := projectsCreate{Name: in.Name, Slug: in.Slug}
	body.Repo.URL = in.Repo
	p, err := draft(s, org, body)
	if err != nil {
		return nil, err
	}
	p.Visibility = Public
	out, err := persist(s, ctx, p, nil)
	if err != nil {
		return nil, err
	}
	return &client.ProjectOut{ID: out.ID, Slug: out.Slug}, nil
}
