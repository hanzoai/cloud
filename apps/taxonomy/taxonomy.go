// Package taxonomy is the product catalogue's shape: which categories exist, what
// each product is called, which category it sits in, what it is tagged with, and
// the order the two are shown in.
//
// It is the editable home of a thing that was source code. The console rendered
// its whole navigation from a hardcoded TypeScript array — 184 products across 14
// categories — so renaming a category, retagging a product or moving one up the
// list was a commit, a review and a deploy. Nothing about that list is a program;
// it is data that a person maintains, and this is where it lives now.
//
// A TAXON is one product's place in the catalogue — its name, its category, its
// tags, its position and how it opens. The word is the precise one for a unit of
// a taxonomy, and it is used here because the obvious alternative is not free in
// either direction: apps/catalog already publishes a schema called Entry, and
// Product is the thing commerce CHARGES for. A name that collided with either
// would make an SDK bind the wrong shape or a reader reach for the wrong store.
//
// Surface (/v1 only):
//
//	GET    /v1/taxonomy                  the whole catalogue, ordered    -> Taxonomy
//	PUT    /v1/taxonomy/categories/:id   create or replace one category  -> Category
//	DELETE /v1/taxonomy/categories/:id   remove one category             -> Deleted
//	PUT    /v1/taxonomy/taxa/:id         create or replace one taxon     -> Taxon
//	DELETE /v1/taxonomy/taxa/:id         remove one taxon                -> Deleted
//
// ONE read, four writes. There is no POST beside the PUT: an id here is a stable
// slug the editor chooses ("vector", "observe"), not a number the server invents,
// so the URL addresses the row whether or not it exists yet and create and replace
// are the same act. There is no PATCH beside the PUT either, for the same reason a
// list editor sends what the row should BE — a partial edit of a row this small
// buys nothing and costs the reader a second write vocabulary.
//
// READ IS PUBLIC, WRITE IS PLATFORM SUDO. The read carries no tenant data — it is
// the same catalogue for every caller, and the marketing landing renders from it
// while signed out — so it asks who is calling only to decide whether to include
// UNPUBLISHED rows. It uses the mechanism the binary already has rather than
// inventing one: the identity middleware never rejects, it only strips and
// re-mints, so a route is public by not calling a gate (the rule /v1/summary and
// /v1/health are served under).
//
// The writes are cloud.Super — platform sudo — and NOT cloud.Admin. This is one
// catalogue for the whole platform, not a per-org one, so it is exactly what
// cloud.Scope.Super was drawn for: "an act against SHARED platform state, which no
// customer-org admin may take however much authority they hold inside their own
// org". An org admin who could rename a category would rename it for every other
// tenant.
//
// NOT THE BILLING CATALOGUE. apps/commerce owns `product` — a Stripe-shaped SKU
// with prices, the thing a customer is CHARGED for. This is navigation and
// marketing: a category, an icon, a route, a per-brand scope. Most rows here are
// not purchasable and have no price, so filing them as billing products would put
// priceless rows in the ledger and make "what do we sell" unanswerable. Two
// questions, two stores, neither lying about the other.
package taxonomy

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

type service struct {
	store *Store
	log   luxlog.Logger
}

var mounted *service

// Mount opens the taxonomy store, seeds it on a first-ever boot, and registers the
// surface per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("taxonomy.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("taxonomy.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("taxonomy.Mount: open taxonomy store: %w", err)
	}
	log := luxlog.Default().New("subsystem", "taxonomy")
	seeded, err := seed(context.Background(), store)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("taxonomy.Mount: seed: %w", err)
	}
	s := &service{store: store, log: log}
	mounted = s

	routes(app, s)

	log.Info("taxonomy surface mounted", "prefix", "/v1/taxonomy", "seeded", seeded, "brand", deps.Brand)
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
