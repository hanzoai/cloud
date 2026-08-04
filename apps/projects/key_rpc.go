package projects

import (
	"context"

	"github.com/zap-proto/zip"

	cloud "github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/analytics"
	"github.com/hanzoai/cloud/plane"
)

// The ingest door asks projects which project minted a beacon's key.
//
// Same seam and same reason as sites_rpc.go: the reader (the /v1/event door) and
// the owner of the fact (this store) are different processes in production, so
// the package-level registry analytics.SetKeyResolver writes is nil where the
// door reads it. In-process when they are co-resident, over the plane when they
// are not.
func exposeKeys() {
	zip.Post[plane.KeyIn, plane.Attribution](cloud.Plane(), "/projects/resolve-key", planeResolveKey,
		zip.WithOperationID(plane.ProjectsResolveKey),
		zip.WithSummary("Resolve a publishable ingest key to the project that minted it"))
}

// planeResolveKey answers which (org, project) a key names. Not-found is
// `Found:false`, never an error: the door turns that into an honest refusal, and
// an error into a 5xx. Collapsing them would refuse every live site's beacons
// during a transient failure of this app.
func planeResolveKey(ctx context.Context, in *plane.KeyIn) (*plane.Attribution, error) {
	r, err := currentKeyResolver()
	if err != nil {
		return nil, err
	}
	sc, ok, err := r.Resolve(ctx, in.Key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &plane.Attribution{Found: false}, nil
	}
	return &plane.Attribution{Found: true, Org: sc.Org, Project: sc.Project}, nil
}

// The resolver this process serves plane answers from. Set at Mount beside
// analytics.SetKeyResolver, so the two can never name different stores.
var planeKeyResolver keyResolver

func setKeyResolverForPlane(r keyResolver) { planeKeyResolver = r }

// currentKeyResolver refuses rather than answering not-found when the store is
// absent. A process that has not mounted projects cannot know whether a key
// exists, and saying "no" would take every site's analytics off the air.
func currentKeyResolver() (keyResolver, error) {
	if planeKeyResolver.store == nil {
		return keyResolver{}, zip.ErrInternal("projects: this process does not own the project store")
	}
	return planeKeyResolver, nil
}

// analytics.KeyResolver is what the door consults; keyResolver is what answers it,
// in-process and over the plane. Pinned so a signature drift fails the build here
// rather than at a nil dispatch on the ingest path.
var _ analytics.KeyResolver = keyResolver{}
