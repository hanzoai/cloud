package taxonomy

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
)

// hanzo owns the PLATFORM catalogue — our own products, the rows every tenant
// sees. It is an ordinary IAM org id and not a reserved marker, because these are
// genuinely one org's products rather than a special kind of thing; a customer's
// rows sit in the same table under their own org.
//
// It is named for the value it is, which is the point: the deployment's own
// reserved store partition is spelled "platform" (orgns.Reserved) and our
// registry namespace is spelled "hanzoai" (apps/sandbox). Three different
// strings, three different systems — a name like "the platform org" would read
// as any of them, and whichever one a reader guessed they would be wrong twice.
//
// It is NOT the SuperAdmin org. Platform authority is owner == "admin" — a person
// whose own org is the reserved `admin` org — which is an identity fact; this is whose products these are. The
// two are deliberately different values: an admin OF this org administers it, and
// only a SuperAdmin edits what every tenant sees.
//
// Brand does not enter into it. A lux or zoo deployment serves this same platform
// catalogue narrowed by the per-row Brands scope — one catalogue, projected — so
// there is no per-brand owner to resolve.
const hanzo = "hanzo"

type service struct {
	store *Store
	log   luxlog.Logger
}

var mounted *service

// Mount opens the taxonomy store, seeds it on a first-ever boot, and registers the
// surface per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("taxonomy: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("taxonomy.Use:  empty DataDir")
	}
	store, err := openStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("taxonomy.Use:  open taxonomy store: %w", err)
	}
	log := luxlog.Default().New("subsystem", "taxonomy")
	seeded, err := seed(context.Background(), store)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("taxonomy.Use:  seed: %w", err)
	}
	s := &service{store: store, log: log}
	mounted = s

	routes(app, s)

	log.Info("taxonomy surface mounted", "prefix", "/v1/taxonomy", "seeded", seeded, "brand", cloud.Brand())
	return nil
}

// Shutdown releases the taxonomy store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.store != nil {
		err = mounted.store.Close()
	}
	mounted = nil
	return err
}
