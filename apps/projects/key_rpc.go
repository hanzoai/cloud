package projects

import (
	"context"

	"github.com/zap-proto/zip"

	cloud "github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/client"
)

// The ingest endpoint asks projects which project minted a beacon's key.
//
// Same client and same reason as sites_rpc.go: the reader (the /v1/event endpoint)
// and the owner of the fact (this store) are different processes in production, so
// the package-level registry event.SetKeyResolver writes is nil where the
// endpoint reads it. In-process when they are co-resident, over the plane when they
// are not.
func exposeKeys() {
	zip.Post[client.KeyIn, client.Attribution](cloud.Plane(), "/projects/resolve-key", planeResolveKey,
		zip.WithOperationID(client.ProjectsResolveKey),
		zip.WithSummary("Resolve a publishable ingest key to the project that minted it"))
}

// planeResolveKey answers which (org, project) a key names. Not-found is
// `Found:false`, never an error: the endpoint turns that into an honest refusal, and
// an error into a 5xx. Collapsing them would refuse every live site's beacons
// during a transient failure of this app.
func planeResolveKey(ctx context.Context, in *client.KeyIn) (*client.Attribution, error) {
	r, err := currentKeyResolver()
	if err != nil {
		return nil, err
	}
	sc, ok, err := r.Resolve(ctx, in.Key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &client.Attribution{Found: false}, nil
	}
	return &client.Attribution{Found: true, Org: sc.Org, Project: sc.Project}, nil
}

// The resolver this process serves plane answers from. Set at Mount beside
// event.SetKeyResolver, so the two can never name different stores.
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

// event.KeyResolver is what the endpoint consults; keyResolver is what answers it,
// in-process and over the plane. Pinned so a signature drift fails the build here
// rather than at a nil dispatch on the ingest path.
var _ event.KeyResolver = keyResolver{}
