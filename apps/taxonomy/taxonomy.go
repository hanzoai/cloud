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
// EVERY ROW BELONGS TO AN ORG. Hanzo's own products belong to the hanzo org and
// are the PLATFORM catalogue — the part that is true for everyone; a customer org
// owns the rows it adds. One table, one record, projected per audience: there is
// no second store for "customer taxonomy", because two stores answering one
// question drift apart and then disagree.
//
// READ: the platform's rows PLUS your own org's, and never another customer's.
// That is the tenancy boundary and it is the whole point. A signed-out visitor
// gets the platform catalogue alone, which is what the marketing landing renders
// from — so the read is public by the mechanism this binary already has rather
// than a new one: the identity middleware never rejects, it only strips and
// re-mints, so a route is public by not calling a gate (the rule /v1/summary and
// /v1/health are served under). Who is calling decides only WHOSE rows join the
// platform's, and whether unpublished ones are shown.
//
// WRITE: an org admin edits their OWN org's rows; a SuperAdmin edits the
// platform's. Those are the platform's two existing scopes and this package adds
// no third — org-scope is the org on the validated principal, platform sudo is
// cloud.Super. Keeping them apart is not pedantry: an org admin who could reach a
// platform row would rename a category for every other tenant, which is exactly
// what cloud.Scope.Super was drawn for ("an act against SHARED platform state,
// which no customer-org admin may take however much authority they hold inside
// their own org"). Conflating the two is a privilege escalation, so an admin of
// the hanzo org is refused the platform rows unless they are also platform sudo.
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

// platformOrg owns the PLATFORM catalogue — Hanzo's own products, the rows every
// tenant sees. It is the hanzo org and not a reserved marker, because these are
// genuinely one org's products rather than a special kind of thing; a customer's
// rows sit in the same table under their own org.
//
// It is NOT the SuperAdmin org. Platform sudo is membership of the reserved
// `admin` org, which is an identity fact; this is whose products these are. The
// two are deliberately different values: an admin OF the hanzo org administers
// hanzo, and only a SuperAdmin edits what every tenant sees.
//
// Brand does not enter into it. A lux or zoo deployment serves this same platform
// catalogue narrowed by the per-row Brands scope — one catalogue, projected — so
// there is no per-brand owner to resolve.
const platformOrg = "hanzo"

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
