package main

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/piece/catalog"
	"github.com/hanzoai/cloud/script"
)

// The connector catalog, wired in THIS composition root because this is the
// process that connects and runs connectors: it holds every org's credential,
// so it is the one place a piece's action may run and a piece's own credential
// check may run against a pasted key. The data (piece/catalog) is linked here
// and by the automation engine, and by nothing else in the fleet.
func init() {
	c, err := catalog.Load()
	if err != nil {
		panic(err)
	}
	script.Install(catalog.Program, catalog.Programs())
	provider.UsePieces(provider.Pieces{
		Catalog: c.Pieces,
		Logo:    catalog.Logo,
		Run:     script.Run,
		Check: func(ctx context.Context, id, kind string, auth any) (bool, error) {
			err := script.Validate(ctx, id, kind, auth)
			if errors.Is(err, script.ErrNoCheck) {
				return false, nil
			}
			return true, err
		},
	})
}
