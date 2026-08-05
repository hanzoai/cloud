package tracker

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// upsert_plane.go carries the external-issue mirror across a PROCESS boundary.
//
// The STORE is this app's. The FEEDER is integrations — it holds the GitHub App
// and verifies the webhook — so cloud.RegisterIssueSink, which only registers in-process, left
// issueSink nil on exactly the path that has work to file. Every mirrored issue
// and every backfill row was refused with "tracker issue sink not registered"
// while this tracker was serving its own surface in the next process.
//
// The item travels instead, through the same upsert the sink calls, so ExtRef
// idempotency holds across the boundary too: a webhook redelivery arriving over
// the plane updates the row a co-resident call created rather than duplicating
// it. The tenant is the caller's plane identity rather than the argument, so a
// feeder acting for one org cannot file into another's board.

// exposeUpsert publishes the mirror upsert on the internal plane. Mount calls it,
// beside registerIssueSink — two doors, one upsert.
func exposeUpsert() {
	zip.Post[plane.IssueIn, plane.IssueUpserted](cloud.Plane(), "/tracker/upsert", planeUpsert,
		zip.WithOperationID(plane.TrackerUpsert),
		zip.WithSummary("Mirror one external work item into the native tracker"))
}

// planeUpsert mirrors one external work item into the CALLER's org — creating the
// row, or updating the one already carrying that ExtRef — and reports which it did
// plus the tracker identity the item is now known by.
//
// The org is the caller's plane identity and never the argument — plane.IssueIn has
// no org field, deliberately, because a feeder able to state the org could file
// into another tenant's tracker. Anonymous is refused rather than defaulted: an
// item arriving with no principal must fail, not land on somebody's board.
//
// It calls upsertIssue, never cloud.UpsertIssue. cloud.UpsertIssue now falls
// through to THIS op when the local sink is nil, so a process serving it that went
// back through it would dial its own socket and ask itself, forever.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeUpsert(ctx context.Context, in *plane.IssueIn) (*plane.IssueUpserted, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("tracker upsert: org required")
	}
	if mounted == nil {
		return nil, zip.Errorf(503, "tracker not mounted")
	}
	res, err := upsertIssue(ctx, cloud.IssueUpsert{
		Org: who.Org, Project: in.Project, ProjectKey: in.Key, ProjectName: in.TeamName,
		Repo: in.Repo, ExtRef: in.ExtRef, Kind: in.Kind, Source: in.Source,
		Title: in.Title, Description: in.Description, State: in.State,
		Assignee: in.Assignee, Labels: in.Labels,
	})
	if err != nil {
		return nil, err
	}
	return &plane.IssueUpserted{Created: res.Created, Number: res.Number, Identifier: res.Identifier}, nil
}
