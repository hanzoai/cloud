package contributor

import "sort"

// ComponentWeightsFromSBOM derives a per-component weight used to distribute
// revenue that is NOT explicitly tagged to a component. This is the
// "deployed images → SBOM components → contributors" mapping: each component
// the platform ships (an SBOM entry) earns a share of untagged revenue.
//
// Preference order for the weight signal:
//  1. UsageCount  — real billing-period usage of the component (best signal)
//  2. TotalLines  — code-size proxy when usage hasn't been measured yet
//  3. equal (1)   — every shipped component shares equally as a last resort
//
// The chosen signal is consistent across all entries: if ANY entry reports
// usage, usage is used for all; otherwise lines; otherwise equal.
func ComponentWeightsFromSBOM(entries []SBOMEntry) map[string]int64 {
	var usageTotal, linesTotal int64
	for _, e := range entries {
		usageTotal += e.UsageCount
		linesTotal += e.TotalLines
	}

	weights := make(map[string]int64, len(entries))
	for _, e := range entries {
		if e.Component == "" {
			continue
		}
		switch {
		case usageTotal > 0:
			weights[e.Component] += e.UsageCount
		case linesTotal > 0:
			weights[e.Component] += e.TotalLines
		default:
			weights[e.Component]++
		}
	}
	return weights
}

// DistributeRevenue splits amount across components proportional to weights.
// Integer rounding remainder is assigned to the highest-weight component
// (ties broken by component name for determinism) so the distributed total
// exactly equals amount — no cents are created or lost.
func DistributeRevenue(amount int64, weights map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(weights))
	if amount <= 0 || len(weights) == 0 {
		return out
	}

	var total int64
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		return out
	}

	// Deterministic iteration order.
	comps := make([]string, 0, len(weights))
	for c, w := range weights {
		if w > 0 {
			comps = append(comps, c)
		}
	}
	sort.Strings(comps)

	var distributed int64
	var topComp string
	var topW int64
	for _, c := range comps {
		w := weights[c]
		share := amount * w / total
		out[c] = share
		distributed += share
		if w > topW {
			topW = w
			topComp = c
		}
	}

	if rem := amount - distributed; rem != 0 && topComp != "" {
		out[topComp] += rem
	}
	return out
}
