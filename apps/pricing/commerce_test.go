package pricing

import (
	"context"
	"testing"

	"github.com/hanzoai/commerce/models/catalogentry"
	"github.com/hanzoai/commerce/util/test/ae"
)

// seed writes one first-party model into the commerce catalog.
func seed(t *testing.T, slug, name string, published bool, in, out string) {
	t.Helper()
	db := catalogentry.SystemDB(context.Background())
	e := catalogentry.New(db)
	e.Slug = slug
	e.Name = name
	e.Category = catalogentry.CategoryEnso
	e.Published = published
	e.Rates = []catalogentry.Rate{
		{Key: catalogentry.RateIn, Unit: catalogentry.UnitMTok, Price: in},
		{Key: catalogentry.RateOut, Unit: catalogentry.UnitMTok, Price: out},
	}
	if err := e.Create(); err != nil {
		t.Fatalf("seed %s: %v", slug, err)
	}
}

// snapshot is the shape the embedded pricing.json carries.
func snapshot(models ...map[string]any) map[string]any {
	section := make([]any, 0, len(models))
	for _, m := range models {
		section = append(section, m)
	}
	return map[string]any{"hanzoModels": section}
}

func modelsByName(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, raw := range doc["hanzoModels"].([]any) {
		m, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("non-object in hanzoModels: %#v", raw)
		}
		out[m["name"].(string)] = m
	}
	return out
}

// The bug this whole change exists to stop: the snapshot said one number, the
// service charged another, and the snapshot won.
func TestCommercePriceBeatsTheSnapshot(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	seed(t, "zen4", "Zen4", true, "1.50", "4.50")

	doc := snapshot(map[string]any{
		"name":     "zen4",
		"fullName": "Zen4 — Flagship",
		"pricing":  map[string]any{"input": 99.0, "output": 999.0},
	})
	priced, added, legacy, err := applyCommerceRates(context.Background(), doc)
	if err != nil {
		t.Fatalf("applyCommerceRates: %v", err)
	}
	if priced != 1 || added != 0 || legacy != 0 {
		t.Fatalf("priced=%d added=%d legacy=%d, want 1/0/0", priced, added, legacy)
	}
	got := modelsByName(t, doc)["zen4"]
	p := got["pricing"].(map[string]any)
	if p["input"] != 1.5 || p["output"] != 4.5 {
		t.Errorf("published %v/%v, want commerce's 1.5/4.5 — the snapshot must not win", p["input"], p["output"])
	}
	// Editorial copy is the snapshot's, not commerce's, and must survive.
	if got["fullName"] != "Zen4 — Flagship" {
		t.Errorf("fullName = %v, want the snapshot's copy preserved", got["fullName"])
	}
}

// Enso is priced in commerce and absent from the snapshot entirely. This is the
// case that puts it on the public price list for the first time.
func TestCommerceAddsAModelTheSnapshotNeverHad(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	seed(t, "enso", "Enso", true, "4.00", "20.00")

	doc := snapshot(map[string]any{"name": "zen4", "pricing": map[string]any{"input": 1.5, "output": 4.5}})
	priced, added, legacy, err := applyCommerceRates(context.Background(), doc)
	if err != nil {
		t.Fatalf("applyCommerceRates: %v", err)
	}
	if priced != 0 || added != 1 || legacy != 1 {
		t.Fatalf("priced=%d added=%d legacy=%d, want 0/1/1", priced, added, legacy)
	}
	enso, ok := modelsByName(t, doc)["enso"]
	if !ok {
		t.Fatal("enso is priced in commerce but did not reach the published catalog")
	}
	p := enso["pricing"].(map[string]any)
	if p["input"] != 4.0 || p["output"] != 20.0 {
		t.Errorf("enso published at %v/%v, want the billed 4/20", p["input"], p["output"])
	}
}

// The vision engines carry a price so billing history can reference them, but
// they are reached only through vision_fallback and must never be listed.
func TestUnpublishedModelsNeverReachTheCatalog(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	seed(t, "enso-vl", "Enso VL", false, "2.28", "9.60")

	doc := snapshot(map[string]any{"name": "zen4", "pricing": map[string]any{"input": 1.5, "output": 4.5}})
	_, added, _, err := applyCommerceRates(context.Background(), doc)
	if err != nil {
		t.Fatalf("applyCommerceRates: %v", err)
	}
	if added != 0 {
		t.Errorf("added=%d, want 0 — an unpublished row reached the public catalog", added)
	}
	if _, listed := modelsByName(t, doc)["enso-vl"]; listed {
		t.Error("enso-vl is an internal vision engine and must never appear on the price list")
	}
}

// End to end over the two halves that ship separately: commerce's seed writes
// the Enso family at the price enso bills, and this overlay publishes it. The
// number a customer is quoted has to survive BOTH, and each half passing its own
// tests would not prove that.
func TestTheSeededEnsoFamilyReachesThePriceList(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	if _, err := catalogentry.SeedEnsoModels(catalogentry.SystemDB(context.Background())); err != nil {
		t.Fatalf("SeedEnsoModels: %v", err)
	}

	doc := snapshot(map[string]any{"name": "zen4", "pricing": map[string]any{"input": 1.5, "output": 4.5}})
	if _, _, _, err := applyCommerceRates(context.Background(), doc); err != nil {
		t.Fatalf("applyCommerceRates: %v", err)
	}
	got := modelsByName(t, doc)

	// What production bills, per MTok.
	for slug, want := range map[string][2]float64{
		"enso":       {4, 20},
		"enso-flash": {2, 4},
		"enso-ultra": {5, 25},
	} {
		m, listed := got[slug]
		if !listed {
			t.Errorf("%s never reached the price list", slug)
			continue
		}
		p := m["pricing"].(map[string]any)
		if p["input"] != want[0] || p["output"] != want[1] {
			t.Errorf("%s published at %v/%v, want the billed %v/%v",
				slug, p["input"], p["output"], want[0], want[1])
		}
	}
	// The internal vision engines are priced but must not be listed.
	for _, slug := range []string{"enso-vl", "enso-vl-pro"} {
		if _, listed := got[slug]; listed {
			t.Errorf("%s is an internal vision engine and must never be published", slug)
		}
	}
}

// An empty commerce catalog must leave the document exactly as it was, so the
// overlay can never blank a public price list.
func TestEmptyCommerceLeavesTheSnapshotIntact(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	doc := snapshot(map[string]any{"name": "zen4", "pricing": map[string]any{"input": 1.5, "output": 4.5}})
	priced, added, legacy, err := applyCommerceRates(context.Background(), doc)
	if err != nil {
		t.Fatalf("applyCommerceRates: %v", err)
	}
	if priced != 0 || added != 0 || legacy != 0 {
		t.Fatalf("priced=%d added=%d legacy=%d, want all zero", priced, added, legacy)
	}
	if got := len(doc["hanzoModels"].([]any)); got != 1 {
		t.Errorf("hanzoModels has %d entries, want the snapshot's 1 left untouched", got)
	}
}
