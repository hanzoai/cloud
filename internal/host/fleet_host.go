package host

import (
	"github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/manifest"
)

// The host knows the fleet: every process that links this package answers the
// framework's questions from the manifest.
func init() {
	cloud.SetFleet(cloud.Fleet{
		OwnerOf:     manifest.OwnerOf,
		Normalize:   manifest.Normalize,
		PrefixesFor: manifest.PrefixesFor,
		StageOf:     manifest.StageOf,
		OpenOf:      manifest.OpenOf,
	})
}
