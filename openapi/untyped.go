package openapi

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
)

// THE TWIN RATCHET: the undispatchable remainder may shrink and may not grow.
//
// [Floor] holds a line under what the document PUBLISHES. Nothing held one under
// what a client can actually CALL, and those are different facts about the same
// operation: an untyped route publishes an address and a tag and is reachable by
// no MCP tool, no CLI command and no typed SDK method. A document can therefore
// grow — clearing the floor — while a larger share of it becomes unreachable, and
// every gate stays green because every gate is measuring presence.
//
// The remedy the fleet already had is a per-app ledger: `untypedByDesign` plus a
// test summing it against that app's served surface. It works, and it is why the
// packages that carry one cannot regress. But it is opt-in, so it is silent
// exactly where nobody has looked — measured when this landed, FOURTEEN apps had
// no ledger of any shape and forty-eight raw operations between them, and finding
// that took three passes because the first two greps keyed on a variable NAME and
// then on a PHRASE and got the wrong answer both times. A property that can only
// be measured by pattern-matching test source is not a property the tree holds.
//
// So this is the fleet-wide half, in the shape floor.go already established: one
// file, one number per product, checked at the ONE place the woven document is
// produced. It needs no per-app cooperation, it covers an app written tomorrow,
// and it cannot be defeated by forgetting to add a test.
//
// # It does NOT replace the per-app ledgers, and must not
//
// A count says an app grew a raw route. A ledger says WHICH address and WHY it is
// raw — the wire fact, re-readable against the pinned zip. Only the ledger can go
// red when a reason stops being true, which is how the five agents session writes
// were found: their refusal named its own expiry condition, zip shipped it, and
// nothing noticed for months. The two answer different questions and the cheap one
// does not excuse the precise one.
//
// # Why per product, and why the direction
//
// Per product for floor.go's reason: totals net out, so an app that goes wholly
// raw is paid for by a neighbour that types four routes. The direction is the one
// a count can enforce — the RAW remainder falling — because the typed count rising
// is satisfied by adding typed routes while raw ones are added beside them, which
// is precisely the case worth catching.
//
// # Lowering is automatic; RAISING is an act
//
// A conversion lowers the number and the new file is committed with it. A
// deliberate new raw route — a webhook signed over raw bytes, a byte stream, an
// upgrade — raises it by a hand edit in the same commit, where a reviewer sees the
// number go UP next to the reason, and where the app's own ledger should gain the
// entry that says which address and why.
type Untyped struct {
	Operations int            `json:"operations"`
	Products   map[string]int `json:"products"`
}

// Count counts what the document publishes that no client can dispatch.
//
// The signal is [Operation.Tool], written by [Fold] where the typed registry is
// read and nowhere else — so this asks the same question the MCP catalogue asks
// and cannot answer differently. Reading the document rather than the registry is
// what makes it a fleet measurement: the woven document is every app's subset, and
// an app that publishes nothing dispatchable is visible here without that app
// running, cooperating, or existing when this was written.
func Count(d *Document) Untyped {
	u := Untyped{Products: map[string]int{}}
	for _, item := range d.Paths {
		for _, op := range item {
			if op.Tool {
				continue
			}
			u.Operations++
			for _, t := range Products(op.Tags) {
				u.Products[t]++
			}
		}
	}
	return u
}

// Shrink returns the remainder for now, or refuses because now is larger.
//
// The returned value is the pointwise MINIMUM, mirroring [Floor.Raise]: a product
// that legitimately grew has already been let through by a hand edit, and taking
// the min means that edit is not silently undone by the next regeneration.
func (u Untyped) Shrink(now Untyped) (Untyped, error) {
	var grew []string
	if now.Operations > u.Operations {
		grew = append(grew, fmt.Sprintf("  operations  %d → %d  (+%d)",
			u.Operations, now.Operations, now.Operations-u.Operations))
	}
	var worse []string
	for _, p := range sortedKeys(now.Products) {
		was, is := u.Products[p], now.Products[p]
		if is > was {
			note := ""
			if was == 0 {
				note = "  ← this product had NO raw operations"
			}
			worse = append(worse, fmt.Sprintf("  %-28s %4d → %-4d (+%d)%s", p, was, is, is-was, note))
		}
	}
	sort.Strings(worse)
	grew = append(grew, worse...)

	// The minimum is taken over the UNION, and a product ABSENT from now counts as
	// ZERO rather than keeping its old number — absence here means the product has
	// no raw operation left, which is the improvement this ratchet exists to lock
	// in. Copying the old map and merging only what `now` carries looks equivalent
	// and is the opposite: it would hold a cleared product at its old number
	// forever, so the app that finished its conversion would be the one app the
	// gate stopped protecting.
	out := Untyped{Operations: min(u.Operations, now.Operations), Products: map[string]int{}}
	for p := range maps.Keys(unionOf(u.Products, now.Products)) {
		// A product that no longer publishes any raw operation is DROPPED rather
		// than pinned at zero: pinning would forbid it ever gaining a legitimate
		// byte stream or signed webhook again, which is stricter than this ratchet
		// means and leaves no way to say yes.
		if n := min(u.Products[p], now.Products[p]); n > 0 {
			out.Products[p] = n
		}
	}

	if len(grew) > 0 {
		return Untyped{}, fmt.Errorf("THE UNDISPATCHABLE REMAINDER GREW:\n\n%s\n\n"+
			"A route that is not a typed op publishes an address and a tag and NOTHING a client can call:\n"+
			"no schema, no MCP tool, no CLI command, no typed SDK method. The published surface can grow\n"+
			"while this grows with it, so every gate that measures presence stays green — which is why this\n"+
			"one measures reach.\n\n"+
			"If the route CAN be a typed op, make it one (LLM.md, 'The typed migration: THE PLAYBOOK').\n"+
			"If a wire fact forbids it — a signature over the raw bytes, a byte stream, a protocol upgrade,\n"+
			"a body cap decided before the parse — then raise the number in openapi/untyped.json in the same\n"+
			"commit, and name the address and that fact in the app's own untypedByDesign ledger, which is\n"+
			"the half that can go red when the reason stops being true", strings.Join(grew, "\n"))
	}
	return out, nil
}

// ReadUntyped reads a committed remainder. A MISSING file is refused for
// [ReadFloor]'s reason: an absent ratchet makes every regression legal, so losing
// the file would lose the guarantee silently.
func ReadUntyped(path string) (Untyped, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Untyped{}, fmt.Errorf("read %s: %w — this is what makes a surface that is growing "+
			"LESS reachable visible; without it every regeneration may publish more that no client can call", path, err)
	}
	var u Untyped
	if err := json.Unmarshal(raw, &u); err != nil {
		return Untyped{}, fmt.Errorf("%s: %w", path, err)
	}
	if u.Products == nil {
		u.Products = map[string]int{}
	}
	return u, nil
}

// Write renders it as the same indented, newline-terminated JSON every other
// artifact here is written as, so it reviews as a diff.
func (u Untyped) Write(path string) error {
	raw, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// unionOf is the key set of both maps, so a product present in either is judged.
func unionOf(a, b map[string]int) map[string]struct{} {
	keys := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	return keys
}
