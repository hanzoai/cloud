package main

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/tracker"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The agent-PR door, published on the internal plane.
//
// A finished coding run files its PR work item with tracker.CreateAgentPR, which
// begins `if mounted == nil { return "tracker: not mounted" }`. The run happens
// in the integrations process, so that is exactly what it returned — every
// completed run logged "tracker PR not created" and the branch it had just
// pushed and verified reached no board.
//
// Declared at this app's composition root for the reason plugin/git/seams.go
// gives: the capability is tracker's and already exported; what is being added
// is the door.
func init() {
	zip.Post[plane.AgentPRIn, plane.AgentPROut](cloud.Plane(), "/tracker/agent-pr", planeAgentPR,
		zip.WithOperationID(plane.TrackerAgentPR),
		zip.WithSummary("Open the native PR work item for a coding run's pushed branch"))
}

// planeAgentPR files the row and returns its stable KEY-N handle. A failure is
// an ERROR and never an empty handle: the caller records a PR-less run as a
// recorded problem, and an empty identifier that arrived as success would show a
// Slack card claiming a PR nobody can open.
func planeAgentPR(ctx context.Context, in *plane.AgentPRIn) (*plane.AgentPROut, error) {
	pr, err := tracker.CreateAgentPR(ctx, tracker.AgentPRInput{
		Org: in.Org, Project: in.Project, Repo: in.Repo, Base: in.Base,
		Head: in.Head, Title: in.Title, Body: in.Body, Assignee: in.Assignee,
	})
	if err != nil {
		return nil, err
	}
	return &plane.AgentPROut{Identifier: pr.Identifier, ProjectKey: pr.ProjectKey, Number: pr.Number}, nil
}
