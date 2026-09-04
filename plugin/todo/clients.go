package main

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/todo"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// The agent-PR endpoint, published on the internal plane.
//
// A finished coding run files its PR work item with todo.CreateAgentPR, which
// begins `if mounted == nil { return "todo: not mounted" }`. The run happens
// in the integrations process, so that is exactly what it returned — every
// completed run logged "todo PR not created" and the branch it had just
// pushed and verified reached no board.
//
// Declared at this app's composition root for the reason plugin/git/clients.go
// gives: the capability is todo's and already exported; what is being added
// is the endpoint.
func init() {
	zip.Post[client.AgentPRIn, client.AgentPROut](cloud.Plane(), "/todo/agent-pr", planeAgentPR,
		zip.WithOperationID(client.TodoAgentPR),
		zip.WithSummary("Open the native PR work item for a coding run's pushed branch"))
}

// planeAgentPR files the row and returns its stable KEY-N handle. A failure is
// an ERROR and never an empty handle: the caller records a PR-less run as a
// recorded problem, and an empty identifier that arrived as success would show a
// Slack card claiming a PR nobody can open.
//
// The org is the CALLER's plane identity and never the argument, exactly as
// planeUpsert resolves it on this same socket (apps/todo/upsert_plane.go).
// It used to be in.Org — read off the wire and passed straight into the
// per-tenant store selector — so a caller on the plane could file a work item
// onto ANOTHER tenant's board by naming it. Anonymous is refused rather than
// defaulted: a run arriving with no principal must fail, not land on somebody's
// board.
func planeAgentPR(ctx context.Context, in *client.AgentPRIn) (*client.AgentPROut, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("todo agent-pr: org required")
	}
	pr, err := todo.CreateAgentPR(ctx, todo.AgentPRInput{
		Org: who.Org, Project: in.Project, Repo: in.Repo, Base: in.Base,
		Head: in.Head, Title: in.Title, Body: in.Body, Assignee: in.Assignee,
	})
	if err != nil {
		return nil, err
	}
	return &client.AgentPROut{Identifier: pr.Identifier, ProjectKey: pr.ProjectKey, Number: pr.Number}, nil
}
