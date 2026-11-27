package coding

// workspace.go — which namespace on the forge a run works in, and as whom.
//
// Two kinds, told apart by forge.Workspace.Made and by nothing else:
//
//	THE ESTATE'S   hanzo, lux, zoo, adnexus — forge's closed table. Every read and
//	               write is made AS THE PERSON (Sudo), so the forge's own ACL
//	               decides what they may reach, exactly as it always has.
//	MADE           every other org — a private namespace the platform made for it
//	               on first need (forge.Provision), recorded in the project store.
//	               A run acts there as the machine, and the check is that the
//	               run's org — the caller's validated org, never a field — is the
//	               org the record names.
//
// The org decides which workspace; nothing a caller sends can.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk/plane"
)

// workspace is org's workspace on the forge, made when create is set and the
// org has none. An org with none, asked without create, is answered with an
// empty Owner.
//
// The estate's orgs answer from forge's closed table, here, with no call to
// anything. Every other org's is the project store's record, asked AS org: the
// call states org on a context of its own, because the store answers for the
// caller's org and a request context would answer for whoever made the request
// (a SuperAdmin reading another org's run). org is always a validated org —
// the principal's, or the one a session the caller may read belongs to.
//
// The answer crosses a process boundary, so it is checked again on this side:
// a made namespace must be a plain name, and must not be one the estate holds or
// keeps (forge.Reserved).
func workspace(ctx context.Context, org string, create bool) (forge.Workspace, error) {
	if owner, err := forge.Owner(org); err == nil {
		return forge.Workspace{Owner: owner}, nil
	}
	call, cancel := context.WithTimeout(plane.For(context.Background(), org), clientTimeout)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	out, err := projectpeer.ProjectsWorkspace(call, &client.WorkspaceIn{Make: create})
	if err != nil {
		return forge.Workspace{}, err
	}
	if out == nil || out.Owner == "" {
		if create {
			return forge.Workspace{}, fmt.Errorf("coding: the project store made no workspace")
		}
		return forge.Workspace{}, nil
	}
	ws, err := forge.Answered(out.Owner, out.Made)
	if err != nil {
		return forge.Workspace{}, fmt.Errorf("coding: %w", err)
	}
	return ws, nil
}

// acting is the forge as a run in ws acts on it: the person in the estate's
// namespaces, the machine in a made one.
func acting(c *forge.Client, ws forge.Workspace, actor string) *forge.Client {
	if ws.Made {
		return c.Machine()
	}
	return c.As(actor)
}

// party names whoever a run in ws acts for, for a refusal a person reads.
func party(ws forge.Workspace, actor string) string {
	if ws.Made || strings.TrimSpace(actor) == "" {
		return "this organization"
	}
	return actor
}
