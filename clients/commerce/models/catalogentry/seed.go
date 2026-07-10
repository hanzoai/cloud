package catalogentry

import (
	_ "embed"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

// hanzoCatalogSeed is the initial platform catalog, taken verbatim from the
// @hanzo/products snapshot (hanzoai/ui/pkgs/products/snapshot/catalog.json) —
// the schema owner. It is the STARTING state / offline fallback; once seeded,
// the CMS (admin FE editing this catalog) is authoritative. Re-baseline by
// re-copying the @hanzo/products snapshot.
//
//go:embed seed/hanzo-catalog.json
var hanzoCatalogSeed []byte

// SeedRow is the @hanzo/products snapshot shape (the exact CatalogEntry
// contract). `id` == `slug`; `pricingId` may be JSON null (→ "").
type SeedRow struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Category   string   `json:"category"`
	BrandColor string   `json:"brandColor"`
	IconKey    string   `json:"iconKey"`
	Slug       string   `json:"slug"`
	Route      string   `json:"route"`
	DocsUrl    string   `json:"docsUrl"`
	ApiPath    string   `json:"apiPath"`
	PricingId  string   `json:"pricingId"` // null → ""
	Brands     []string `json:"brands"`
	Repo       string   `json:"repo"`
	Admin      bool     `json:"admin"`
	Status     string   `json:"status"`
	Gcp        string   `json:"gcp"`
}

// HanzoSeedRows returns the parsed embedded snapshot.
func HanzoSeedRows() ([]SeedRow, error) {
	var rows []SeedRow
	if err := json.DecodeBytes(hanzoCatalogSeed, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// SeedIfEmpty seeds the catalog only when it is currently empty — a cheap single
// count query gates the full per-row create, so it is safe to call on every
// bootstrap. Once any entry exists (seeded or CMS-created) it never re-runs, so
// CMS state stays authoritative.
func SeedIfEmpty(db *datastore.Datastore) (created int, err error) {
	n, err := Query(db).Count()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	return Seed(db)
}

// Seed loads the embedded @hanzo/products snapshot into db (which MUST be
// namespaced to the catalog-owning "system" org). IDEMPOTENT + NON-DESTRUCTIVE:
// an entry whose slug already exists is left UNTOUCHED so CMS edits are never
// clobbered; only missing slugs are created. Order is the snapshot index (stable
// display order within a category). Returns the number created.
func Seed(db *datastore.Datastore) (created int, err error) {
	rows, err := HanzoSeedRows()
	if err != nil {
		return 0, err
	}

	for i, r := range rows {
		slug := r.Slug
		if slug == "" {
			slug = r.ID
		}

		existing := New(db)
		ok, qerr := existing.Query().Filter("Slug=", slug).Get()
		if qerr != nil {
			return created, qerr
		}
		if ok {
			continue
		}

		e := New(db)
		e.Slug = slug
		e.Name = r.Name
		e.Category = r.Category
		e.BrandColor = r.BrandColor
		e.IconKey = r.IconKey
		e.Route = r.Route
		e.DocsUrl = r.DocsUrl
		e.ApiPath = r.ApiPath
		e.PricingId = r.PricingId
		e.Brands = r.Brands
		e.Repo = r.Repo
		e.Admin = r.Admin
		e.Status = r.Status
		e.Gcp = r.Gcp
		e.Order = i
		e.Published = true
		if err := e.Create(); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
