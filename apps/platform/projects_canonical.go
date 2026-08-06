// projects_canonical.go — the ProjectStore over the CANONICAL IAM.
//
// Split-horizon deployments run IAM as its own process — the store that mints
// every token and serves /v1/iam/projects at the edge. Platform used to read
// projects from cloud's EMBEDDED copy of the iam store instead: a second
// database for the same noun, so a project created at /v1/iam was invisible to
// the PaaS and vice versa.
//
// So platform reads the canonical store, and the ONLY question left is how it
// reaches a process it does not share. It is a peer, so it is reached over the
// peer plane: iamplane.IAMProjects, a ZAP call by NAME on iam's own socket.
//
// # What went away with the URL
//
// The HTTP client this replaces authenticated AS THE ORG: each read presented
// client_secret_basic for that org's own "<org>-platform-kms" identity, minted
// on first need, sealed into KMS, cached for a minute, and admitted by IAM's
// authorize as read-only/own-org-only. Every part of that existed to tell IAM
// which tenant was asking — over a transport that had no way to say so.
//
// The plane says so. The org rides the CALL (cloud.For), forwarded from the
// gateway's assertion, and it is not an argument the caller can set — so the
// callee reads its tenant from the same place it reads a request's, and there is
// nothing left for a per-org credential to prove. A minted secret, a KMS seal, a
// cache and a grant, all standing in for a field the transport now carries.
//
// It also took the workaround with it. IAM frames its single-project read as a
// POST, which the machine grant rightly refused, so Get was derived from List and
// the reaper logged "projects: get hanzo/index: status 403" every cycle. One op,
// one verb, no wall to route around.
//
// When this binary IS the IAM the store is in-process and iamProjects is used
// unchanged — a Go call beats any transport. The selector is newProjectStore.

package platform

import (
	"context"

	"github.com/hanzoai/cloud"
	iamplane "github.com/hanzoai/cloud/plane/iam"
	model "github.com/hanzoai/iam/pkg/model"
)

// newProjectStore selects the canonical project source: the in-process store
// when this binary IS the IAM, the iam peer when it is not.
func newProjectStore() ProjectStore {
	if !cloud.IAMExternal() {
		// No external IAM named: the embedded subsystem is the canonical store,
		// and reading it is a function call.
		return iamProjects{}
	}
	return canonicalProjects{}
}

// canonicalProjects reads projects from the process that owns them.
//
// It holds nothing — no address, no credential, no cache. That is the shape of a
// peer call: the name is the address and the tenant rides the context, so there
// is no state for a constructor to carry and none to go stale.
type canonicalProjects struct{}

// List asks iam for the org's projects and rebuilds IAM's own model from the
// contract, exactly as marketing's roster read does. The plane carries the
// PROJECTION (package plane), never IAM's record — a caller depends on the
// contract or it depends on the callee's implementation, and only one of those
// survives the two being deployed separately.
func (canonicalProjects) List(ctx context.Context, org string) ([]*model.Project, error) {
	out, err := iamplane.IAMProjects(cloud.For(ctx, org))
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	rows := make([]*model.Project, 0, len(out.Projects))
	for _, p := range out.Projects {
		rows = append(rows, &model.Project{
			Owner:       p.Owner,
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Description: p.Description,
			CreatedTime: p.CreatedTime,
		})
	}
	return rows, nil
}

// Get returns nil (no error) when the project does not exist — the convention
// requireProject's 404 mapping depends on.
//
// Still derived from List, now because it is cheap rather than because a grant
// forbade the direct read: an org's project count is small and the whole list is
// one frame. A second op to select one row from a list the caller can already
// hold would be a second way to ask one question.
func (c canonicalProjects) Get(ctx context.Context, org, name string) (*model.Project, error) {
	rows, err := c.List(ctx, org)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		if p != nil && p.Name == name {
			return p, nil
		}
	}
	return nil, nil
}

func (c canonicalProjects) Exists(ctx context.Context, org, name string) (bool, error) {
	p, err := c.Get(ctx, org, name)
	if err != nil {
		return false, err
	}
	return p != nil, nil
}
