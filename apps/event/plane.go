package event

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	planeops "github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
)

// planeKeys resolves a beacon's publishable ingest key by asking the app that
// owns the project store, over the internal plane.
//
// This endpoint serves api.hanzo.ai; the key is a column on a project row. In
// production those are never the same process — the pod boots ~25 single-app
// processes — so the registry projects.Use writes is nil here. It is the package
// DEFAULT (attribution.go) and a co-resident store still answers with no hop,
// because currentKeyResolver prefers the in-process one.
//
// It lives in this package rather than at the compose root because the root
// cannot import it: analytics imports cloud, so cloud importing analytics is a
// cycle. The client belongs to the reader either way.
type planeKeys struct{}

// Resolve answers which project minted the key. Not-found is a clean refusal; a
// failure to ASK is an error and stays one, so a transient failure of the owning
// app is never mistaken for "this site does not exist".
func (planeKeys) Resolve(ctx context.Context, key string) (Attribution, bool, error) {
	// Org-less by construction: the KEY is the tenant key, and the answer names
	// the org. Passing one would let a caller file a beacon under someone else's.
	out, err := projectpeer.ProjectsResolveKey(cloud.For(ctx, ""), &planeops.KeyIn{Key: key})
	if err != nil {
		return Attribution{}, false, fmt.Errorf("analytics: ask projects: %w", err)
	}
	if out == nil {
		return Attribution{}, false, fmt.Errorf("analytics: projects answered nothing")
	}
	if !out.Found {
		return Attribution{}, false, nil
	}
	return Attribution{Org: out.Org, Project: out.Project}, true, nil
}
