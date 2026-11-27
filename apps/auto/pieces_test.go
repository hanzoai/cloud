package auto

import (
	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/piece/catalog"
	"github.com/hanzoai/cloud/script"
)

// The test binary wires the connector catalog exactly as plugin/auto does at
// its composition root, so the catalogue under test is the one production
// serves.
func init() {
	c, err := catalog.Load()
	if err != nil {
		panic(err)
	}
	script.Install(catalog.Program, catalog.Programs())
	provider.UsePieces(provider.Pieces{Catalog: c.Pieces})
}
