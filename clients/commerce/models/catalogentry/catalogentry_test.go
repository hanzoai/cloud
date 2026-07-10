package catalogentry

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

func sysDB(c context.Context) *datastore.Datastore {
	return datastore.New(nscontext.WithNamespace(c, "system"))
}

func TestSeed_IdempotentAndComplete(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := sysDB(c)

	created, err := Seed(db)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, _ := HanzoSeedRows()
	if len(rows) == 0 {
		t.Fatal("embedded snapshot is empty")
	}
	if created != len(rows) {
		t.Fatalf("first seed created %d, want %d (all snapshot rows)", created, len(rows))
	}

	created2, err := Seed(db)
	if err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if created2 != 0 {
		t.Fatalf("re-seed created %d, want 0 (idempotent)", created2)
	}
}

// TestProject_ConformsToContract checks the projection matches the
// @hanzo/products CatalogEntry contract for known seeded entries.
func TestProject_ConformsToContract(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := sysDB(c)
	if _, err := Seed(db); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cat, err := Project(db, "hanzo")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if cat.Brand != "hanzo" || len(cat.Categories) != 13 {
		t.Fatalf("hanzo: brand=%s cats=%d, want hanzo/13", cat.Brand, len(cat.Categories))
	}
	if cat.Categories[0].Label != "AI" || cat.Categories[0].ID != "ai" {
		t.Fatalf("first category = %+v, want {ai, AI, 0}", cat.Categories[0])
	}

	byID := map[string]Item{}
	for _, p := range cat.Products {
		byID[p.ID] = p
		if p.IconKey == "" {
			t.Fatalf("%s: empty iconKey", p.ID)
		}
		if p.BrandColor == "" {
			t.Fatalf("%s: empty brandColor (must be a swatch key)", p.ID)
		}
		if p.ID != p.Slug {
			t.Fatalf("%s: id != slug (%s)", p.ID, p.Slug)
		}
		if p.Route == "" {
			t.Fatalf("%s: empty route", p.ID)
		}
		if len(p.ApiPath) < 3 || p.ApiPath[:3] != "/v1" {
			t.Fatalf("%s: apiPath %q not /v1-prefixed", p.ID, p.ApiPath)
		}
	}

	m, ok := byID["models"]
	if !ok {
		t.Fatal("seeded entry 'models' missing from projection")
	}
	if m.Name != "Models" || m.Category != "AI" || m.IconKey != "Brain" {
		t.Fatalf("models = %+v, want Models/AI/Brain", m)
	}
	if m.BrandColor != "violet" { // swatch KEY, not hex
		t.Fatalf("models.brandColor = %q, want swatch key 'violet'", m.BrandColor)
	}
	if m.Route != "/models" || m.DocsUrl != "https://docs.hanzo.ai/docs/services/models" {
		t.Fatalf("models route/docs = %q / %q", m.Route, m.DocsUrl)
	}
	if m.PricingId != nil {
		t.Fatalf("models.pricingId = %v, want null", *m.PricingId)
	}

	if g, ok := byID["gateway"]; ok {
		if g.PricingId == nil || *g.PricingId != "gateway" {
			t.Fatalf("gateway.pricingId = %v, want \"gateway\"", g.PricingId)
		}
	}
}

// TestProject_CategoryScopedByBrand proves scoping is by CATEGORY (matching
// @hanzo/products catalogForBrand): a sovereign brand (lux) sees only entries in
// its 5 categories, never an AI/Compute/etc. entry.
func TestProject_CategoryScopedByBrand(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := sysDB(c)
	if _, err := Seed(db); err != nil {
		t.Fatalf("seed: %v", err)
	}

	lux, err := Project(db, "lux")
	if err != nil {
		t.Fatalf("project lux: %v", err)
	}
	if len(lux.Categories) != 5 {
		t.Fatalf("lux categories = %d, want 5", len(lux.Categories))
	}
	allowed := map[string]bool{"Web3": true, "Network": true, "Security": true, "Dev": true, "Settings": true}
	for _, p := range lux.Products {
		if !allowed[p.Category] {
			t.Fatalf("lux projection leaked %q in non-lux category %q", p.ID, p.Category)
		}
		if p.ID == "models" {
			t.Fatal("lux projection contains an AI-category entry (models)")
		}
	}
	hanzo, _ := Project(db, "hanzo")
	if len(hanzo.Products) <= len(lux.Products) {
		t.Fatalf("hanzo products (%d) should exceed lux (%d)", len(hanzo.Products), len(lux.Products))
	}
}

func TestProject_UnpublishedExcluded(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := sysDB(c)

	e := New(db)
	e.Slug = "hidden"
	e.Name = "Hidden"
	e.Category = "AI"
	e.IconKey = "Lock"
	e.BrandColor = "slate"
	e.Published = false
	if err := e.Create(); err != nil {
		t.Fatalf("create: %v", err)
	}

	cat, err := Project(db, "hanzo")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	for _, p := range cat.Products {
		if p.Slug == "hidden" {
			t.Fatal("unpublished entry appeared in the public projection")
		}
	}
}
