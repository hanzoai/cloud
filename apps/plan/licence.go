package plan

// licence.go writes a tier's licensing facts down the way the engine reads them.
//
// The facts themselves — which products, app builds and engine capabilities a tier
// grants — live on the commerce plan ROW. They used to be read from the @hanzo/plans
// catalog, and that is the defect: the catalog lists what is ON SALE TODAY, so a tier
// that has been retired resolves to nothing, definitively, and a subscriber who is
// still being charged is refused the products they bought. The row keeps its answer
// after retirement, which is the same reason Paid classifies on the row's category
// and price rather than looking the slug up in the price list.
//
// Only the SPELLING of the tokens is here, and a spelling is not a policy: the
// plan→product decision belongs to the row, and this states it in the flat vocabulary
// the engine verifies (@hanzo/plans entitlements.mjs#toLicenseFeatures).

import "slices"

// Licence is the licensing facts of one tier — the subset of a commerce plan row
// this package needs to state the rule without importing the commerce models, for
// the same reason Tier exists.
type Licence struct {
	// Products are the commerce SKUs the tier entitles ("engine", "team", …).
	Products []string
	// Apps are the engine app builds it licenses ("hanzo", "lux", "zoo").
	Apps []string
	// Features are engine capability tokens granted verbatim ("inference", …).
	Features []string
}

// Tokens returns l as the flat license-feature list: engine features verbatim, plus
// one namespaced token per licensed app and product. Sorted and deduplicated so the
// same licence always produces the same list, and so a token issued from it is
// byte-stable across processes.
//
// A tier that licenses nothing yields an empty list — never a token, never a grant.
func Tokens(l Licence) []string {
	out := make([]string, 0, len(l.Features)+len(l.Apps)+len(l.Products))
	out = append(out, l.Features...)
	for _, a := range l.Apps {
		out = append(out, "licensing.app:"+a)
	}
	for _, p := range l.Products {
		out = append(out, "licensing.product:"+p)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
