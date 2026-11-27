package main

import (
	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/piece/catalog"
	"github.com/hanzoai/cloud/script"
)

// The connector catalog, wired in THIS composition root because flows compose
// connectors: the catalogue a builder browses and the actions a step names come
// from it. A piece action a flow runs is still RUN by the provider app, which
// holds the org's credential — the registry's action asks it over the plane —
// so no credential and no runner is installed here. The programs are, for the
// one route that runs an action with a credential its caller supplies
// (/v1/auto/provider/:id/run).
func init() {
	c, err := catalog.Load()
	if err != nil {
		panic(err)
	}
	script.Install(catalog.Program, catalog.Programs())
	provider.UsePieces(provider.Pieces{Catalog: c.Pieces})
}
