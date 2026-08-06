package openapi

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// THE RATCHET: a published surface may grow and may not quietly shrink.
//
// Every gate this package already has compares the document to something that
// moved with it. The weave compares the golden to the subsets — both derived, so
// they can agree while both are wrong. check regenerates the subsets from
// source — which catches a stale artifact, and passes cleanly on an artifact that
// is freshly and correctly generated from code that stopped registering half its
// routes. Nothing held a line across time.
//
// So a surface can lose products with every gate green, and it has: the CLI's
// capture went from 151 products to 4 in one bad reading and shipped 46 products
// short, and plugin/ingress lost eight paths — /v1/ingress/routes, /services,
// /middlewares, /tls, /status and their :id forms — because a subset was never
// regenerated. Both were invisible for the same reason: NOTHING COMPARED THE
// SURFACE TO ITS OWN PAST. A smaller document is indistinguishable from a smaller
// API, and only the history says which one happened.
//
// floor.json is that history, in the only shape that is cheap to keep honest: the
// counts, per product. A regeneration that meets or beats every one of them raises
// the floor and the new file is committed alongside the document it describes. A
// regeneration that comes in UNDER any of them fails, names the products and the
// deltas, and writes nothing.
//
// # Why counts, and why per product
//
// The floor is not a second copy of the document — that would be one more derived
// artifact free to drift, which is the disease. It is the one fact a shrink cannot
// hide behind: a product with 161 operations that comes back with 4 is a hole
// whatever the four are called, and a product that comes back absent is the whole
// hole. Per product rather than in total, because totals net out — the run that
// lost all 143 of iam's paths added enough elsewhere to keep the total moving up.
//
// # Lowering it is allowed, and is meant to be an ACT
//
// A deliberate deletion lowers the floor by editing floor.json in the same commit
// that deletes the routes, where a reviewer sees the number go down next to the
// reason. That is the whole design: not "never shrink", but "never shrink by
// accident and never shrink silently".
type Floor struct {
	Paths      int            `json:"paths"`
	Operations int            `json:"operations"`
	Products   map[string]int `json:"products"`
}

// Measure counts what a document publishes.
//
// Products are read off the OPERATIONS' tags, not off the document's tag list: the
// tag list is itself derived, and a bug that emptied it would make every product
// vanish from the floor at once — the ratchet would then be measuring its own
// blind spot. An operation with no tag (every address outside /v1) is counted in
// Operations and in no product, which is exactly what it is.
func Measure(d *Document) Floor {
	f := Floor{Paths: len(d.Paths), Products: map[string]int{}}
	for _, item := range d.Paths {
		for _, op := range item {
			f.Operations++
			for _, t := range Products(op.Tags) {
				f.Products[t]++
			}
		}
	}
	return f
}

// Raise returns the floor for now, or refuses because now is smaller.
//
// The returned floor is the pointwise MAXIMUM, not now: a product that legitimately
// shrank has already been let through by a hand edit to floor.json, and taking the
// max means that one edit is not silently undone by the next unrelated regeneration.
func (f Floor) Raise(now Floor) (Floor, error) {
	var lost []string
	if now.Paths < f.Paths {
		lost = append(lost, fmt.Sprintf("  paths       %d → %d  (-%d)", f.Paths, now.Paths, f.Paths-now.Paths))
	}
	if now.Operations < f.Operations {
		lost = append(lost, fmt.Sprintf("  operations  %d → %d  (-%d)", f.Operations, now.Operations, f.Operations-now.Operations))
	}
	var shrank []string
	for _, p := range sortedKeys(f.Products) {
		was, is := f.Products[p], now.Products[p]
		if is < was {
			gone := ""
			if is == 0 {
				gone = "  ← the whole product"
			}
			shrank = append(shrank, fmt.Sprintf("  %-28s %4d → %-4d (-%d)%s", p, was, is, was-is, gone))
		}
	}
	sort.Strings(shrank)
	lost = append(lost, shrank...)

	out := Floor{
		Paths:      max(f.Paths, now.Paths),
		Operations: max(f.Operations, now.Operations),
		Products:   map[string]int{},
	}
	for p, n := range f.Products {
		out.Products[p] = n
	}
	for p, n := range now.Products {
		out.Products[p] = max(out.Products[p], n)
	}

	if len(lost) > 0 {
		return Floor{}, fmt.Errorf("THE PUBLISHED SURFACE SHRANK:\n\n%s\n\n"+
			"Regenerating the document produced FEWER operations than the committed floor. Every SDK, the\n"+
			"MCP tool list and the CLI are projections of this document, so an operation missing here is an\n"+
			"operation no generated client can reach — and a smaller document reads exactly like a smaller\n"+
			"API, which is how a surface loses products with every other gate green.\n\n"+
			"If routes were DELETED on purpose, lower the numbers in openapi/floor.json in the same commit\n"+
			"that deletes them, so the reduction is reviewed next to its reason. If they were not, something\n"+
			"stopped registering: a subsystem that failed to mount, a door whose registry did not answer, a\n"+
			"pin that moved. Find that before regenerating", strings.Join(lost, "\n"))
	}
	return out, nil
}

// ReadFloor reads a committed floor. A MISSING file is refused rather than
// treated as zero: an absent floor makes every shrink legal, so the failure mode
// of losing the file would be losing the guarantee — silently, which is the one
// thing this must never do.
func ReadFloor(path string) (Floor, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Floor{}, fmt.Errorf("read %s: %w — the floor is what makes a shrinking surface visible; "+
			"without it every regeneration is allowed to publish less than the last one", path, err)
	}
	var f Floor
	if err := json.Unmarshal(raw, &f); err != nil {
		return Floor{}, fmt.Errorf("%s: %w", path, err)
	}
	if f.Products == nil {
		f.Products = map[string]int{}
	}
	return f, nil
}

// Write renders the floor as the same indented, newline-terminated JSON every
// other artifact in this repo is written as, so it reviews as a diff.
func (f Floor) Write(path string) error {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
