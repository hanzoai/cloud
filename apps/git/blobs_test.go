package git

import "github.com/hanzoai/cloud/apps/git/objects"

// Every test mounts the service over one pack store in memory, which every service a test
// mounts shares, as replicas share the bucket: there is no object store to reach, and a
// service with none refuses to keep a repository. A pack is named by its checksum and
// the index that points at it is the test's own database, so tests do not see each
// other's repositories through it.
var testBlobs = objects.NewMemory()

func init() {
	packBlobs = func() objects.Blobs { return testBlobs }
}
