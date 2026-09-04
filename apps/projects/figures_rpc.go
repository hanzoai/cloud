package projects

// figures_rpc.go — what the org has built and what of it is serving.
//
// It is the answer to "what have we shipped", asked by a caller that does not
// know a slug — which is every caller that is a question in words rather than a
// console with a project already open.
//
// It is NOT sites_live. That op is the public directory: it spans every org by
// design (catalog.go filters on visibility, not on tenant), and answering a
// tenant's question from it would put other people's projects in this org's
// answer. Same app, same store, opposite tenancy — so this is its own op, and
// the two are not variants of each other.

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// exposeFigures publishes the org's project rollup on the internal plane.
// Called from Mount.
func exposeFigures() {
	zip.Post[client.FiguresIn, client.FiguresOut](cloud.Plane(), "/projects/figures", planeFigures,
		zip.WithOperationID(client.ProjectsFigures),
		zip.WithSummary("The caller's headline project figures"))
}

// planeFigures answers the caller's own project rollup: how many projects
// exist, how many are serving, and which went live most recently.
//
// The org is the CALLER's plane identity, exactly as projects_ownership takes
// it, and [client.FiguresIn] carries no field that could name another. It does
// NOT reproduce the HTTP surface's "admin" bucket (typed.go): an operator with
// no org of their own has no projects of their own, and inventing a tenant for
// them here would be this op answering a question nobody asked.
//
// A store this process does not own is an ERROR, never an empty rollup — the
// same refusal currentScopeResolver makes, for the same reason: "zero projects"
// and "I am not the process that would know" must not arrive as one answer.
func planeFigures(ctx context.Context, _ *client.FiguresIn) (*client.FiguresOut, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("projects figures: org required")
	}
	r, err := currentScopeResolver()
	if err != nil {
		return nil, err
	}
	rows, err := r.store.ListProjects(ctx, who.Org)
	if err != nil {
		return nil, zip.ErrInternal("projects figures: list failed")
	}

	live := 0
	var newest int64
	var latest string
	for _, p := range rows {
		if p.Status != "live" {
			continue
		}
		live++
		if p.UpdatedAt > newest {
			newest, latest = p.UpdatedAt, p.Slug
		}
	}

	figs := []client.Figure{
		{Label: "Projects", Value: fmt.Sprint(len(rows))},
		{Label: "Deployed and serving", Value: fmt.Sprint(live)},
	}
	if latest != "" {
		figs = append(figs, client.Figure{
			Label:  "Most recently deployed",
			Value:  latest,
			Period: time.Unix(newest, 0).UTC().Format("2006-01-02"),
		})
	}
	return &client.FiguresOut{Figures: figs}, nil
}
