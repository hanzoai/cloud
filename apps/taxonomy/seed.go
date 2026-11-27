package taxonomy

// The FIRST-BOOT contents.
//
// seed.json is the console's hardcoded product registry, lifted out of it: 184
// products across 14 categories, in the order and under the categories the
// registry already used, with each category's one-line summary and per-brand scope
// taken from the same place. Nothing here is invented — every field is the value
// the TypeScript held, which is what makes the move a MOVE and not a rewrite.
//
// Every row is stamped `"owner": "hanzo"`. These are Hanzo's OWN products, so
// they are the PLATFORM catalogue, and the seed says whose they are on each row
// rather than leaving it to a default somewhere else — the check below refuses
// any row that claims otherwise. A customer's rows are written by that customer
// and are never seeded.
//
// TAGS SEED EMPTY, on purpose. The registry has no tags to carry; tags are the new
// editable axis, and filling them from some other field (a product's Google Cloud
// equivalent, its repo) would be inventing a taxonomy while claiming to preserve
// one. They start empty and a person fills them.
//
// It is a SEED, not a reconciliation. Once the store holds anything it is never
// consulted again — a seed that re-asserted itself on every restart would undo an
// editor's deletion each time the pod moved, which is how a "self-healing" default
// quietly becomes the only thing anyone can configure.

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed seed.json
var seedJSON []byte

// catalogue is the seed document's shape — the two lists, exactly as the store
// takes them.
type catalogue struct {
	Categories []Category `json:"categories"`
	Taxa       []Taxon    `json:"taxa"`
}

// seed fills an empty store and reports how many taxa it wrote. It returns 0
// without touching anything when the store already holds rows.
func seed(ctx context.Context, store *Store) (int, error) {
	var doc catalogue
	if err := json.Unmarshal(seedJSON, &doc); err != nil {
		return 0, fmt.Errorf("decode seed: %w", err)
	}
	// The seed is embedded in the binary, so a broken one is a build that must not
	// start rather than a store that half-fills. Checking it here — before the
	// transaction — means the failure names the row.
	known := make(map[string]bool, len(doc.Categories))
	for _, c := range doc.Categories {
		if c.ID == "" || c.Label == "" {
			return 0, fmt.Errorf("seed category %q: id and label are required", c.ID)
		}
		if c.Owner != hanzo {
			return 0, fmt.Errorf("seed category %q is owned by %q; the seed is the PLATFORM catalogue and every row belongs to %q", c.ID, c.Owner, hanzo)
		}
		known[c.ID] = true
	}
	for _, e := range doc.Taxa {
		if e.ID == "" || e.Name == "" {
			return 0, fmt.Errorf("seed taxon %q: id and name are required", e.ID)
		}
		if e.Owner != hanzo {
			return 0, fmt.Errorf("seed taxon %q is owned by %q; the seed is the PLATFORM catalogue and every row belongs to %q", e.ID, e.Owner, hanzo)
		}
		if !known[e.Category] {
			return 0, fmt.Errorf("seed taxon %q names category %q, which the seed does not define", e.ID, e.Category)
		}
		// The same two shapes the write endpoint enforces. The seed is lifted from
		// TypeScript by a parser, and a parser that fails to resolve a reference
		// yields the expression text — a tile whose href reads `ext.api` and opens
		// nothing. Refuse it here, where the row is named.
		if (e.Route == "") == (e.Href == "") {
			return 0, fmt.Errorf("seed taxon %q: give exactly one of route or href", e.ID)
		}
		if e.Route != "" && !strings.HasPrefix(e.Route, "/") {
			return 0, fmt.Errorf("seed taxon %q: route %q does not start with /", e.ID, e.Route)
		}
		if e.Href != "" && !strings.HasPrefix(e.Href, "https://") && !strings.HasPrefix(e.Href, "http://") {
			return 0, fmt.Errorf("seed taxon %q: href %q is not a URL", e.ID, e.Href)
		}
	}
	wrote, err := store.Seed(ctx, doc.Categories, doc.Taxa)
	if err != nil {
		return 0, err
	}
	if !wrote {
		return 0, nil
	}
	return len(doc.Taxa), nil
}
