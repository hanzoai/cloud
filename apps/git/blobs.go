package git

import (
	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/object"
)

// packBlobs is where the objects store keeps pack bytes: the deployment's object store
// (sdk/object), in the bucket named by GIT_BUCKET (hanzo-git by default). On a
// production process with no object store configured the store refuses at first use
// (objects.ErrNoBlobs) rather than keep packs on the replica's own disk.
// It is a variable so a test can put a store in memory.
var packBlobs = func() objects.Blobs {
	return objects.Bucket(object.Default(), env.Or("GIT_BUCKET", "hanzo-git"))
}

// UseBlobs puts this process's pack store at b and answers the function that puts
// back the one it replaced. A host that embeds the git service over a store of its
// own calls it before Use: a test that mounts git beside the app it is testing (the
// platform's push-to-build), where there is no deployment object store to reach.
func UseBlobs(b objects.Blobs) (restore func()) {
	prev := packBlobs
	packBlobs = func() objects.Blobs { return b }
	return func() { packBlobs = prev }
}
