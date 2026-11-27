// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/client"
	aictl "github.com/hanzoai/ai/controllers"
)

// A router id is billed at the tier that answers it, so the price list is never told
// it is free, even while the family's service still lists it among its free models.
func TestARouterIdIsNeverFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	const doc = `
version: 1
router:
  depth:
    enso-auto: &enso
      default: enso-flash
      high: enso-pro
      max: enso-ultra
    enso: *enso
models: {}
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := aictl.InitModelConfig(path); err != nil {
		t.Fatal(err)
	}
	if got := skus([]string{"enso", "enso-auto", "enso-free"}); !slices.Equal(got, []string{"enso-free"}) {
		t.Errorf("free = %v, want [enso-free]: a router id is no free model", got)
	}
}

// A router id the price list names is quoted at its default tier's price, as
// /v1/models lists it; a tier, the free model, an unknown id and a router whose tier
// is unserved are not quoted again.
func TestARouterIdIsQuotedAtItsDefaultTier(t *testing.T) {
	prev := depth
	depth = func(id string) (string, bool) {
		switch id {
		case "enso", "enso-auto":
			return "enso-flash", true
		case "hanzo/enso":
			return "", true
		}
		return "", false
	}
	t.Cleanup(func() { depth = prev })
	tiers := []client.Quote{
		{Model: "enso-flash", Price: client.Price{Input: "0.33", Output: "1.65", CacheRead: "0.033", CacheWrite: "0.4125"}},
		{Model: "enso-pro", Price: client.Price{Input: "4.4", Output: "22"}},
	}
	got := routed(tiers, []string{"enso", "ENSO", "enso-auto", "hanzo/enso", "enso-flash", "enso-free", "zen5", ""})
	if len(got) != 2 || got[0].Model != "enso" || got[1].Model != "enso-auto" {
		t.Fatalf("quotes = %+v, want enso and enso-auto alone", got)
	}
	for _, q := range got {
		if q.Price != tiers[0].Price || q.Tier != "enso-flash" {
			t.Errorf("%s = %+v, want enso-flash's price, tier enso-flash", q.Model, q)
		}
	}
}

// Any model a program in this process is about to call is quoted at the row the
// model API bills it from, so the meter holds what the call can cost rather than a
// rate of its own. A model no row prices is not quoted: the default the model API
// bills an unpriced model at is a guess, never a list price.
func TestAModelTheMeterNamesIsQuotedAtItsBilledRow(t *testing.T) {
	prevDepth, prevPriced := depth, priced
	depth = func(id string) (string, bool) { return "enso-flash", id == "enso" }
	priced = func(id string) (aictl.Rate, bool) {
		if id == "zen5" {
			return aictl.Rate{Input: 0.5, Output: 2, CacheRead: 0.05}, true
		}
		return aictl.Rate{}, false
	}
	t.Cleanup(func() { depth, priced = prevDepth, prevPriced })
	quoted := []client.Quote{{Model: "enso-flash", Price: client.Price{Input: "0.33", Output: "1.65"}}}
	got := listed(quoted, []string{"zen5", "ZEN5", "enso-flash", "enso", "nobody-prices-me", ""})
	if len(got) != 1 || got[0].Model != "zen5" {
		t.Fatalf("quotes = %+v, want zen5 alone", got)
	}
	if want := (client.Price{Input: "0.5", Output: "2", CacheRead: "0.05"}); got[0].Price != want {
		t.Fatalf("zen5 = %+v, want its billed row %+v", got[0].Price, want)
	}
}
