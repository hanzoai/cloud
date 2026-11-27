package websearch

// rank.go — the merged page is ordered by what the engines AGREE on, not by
// which engine was named first.
//
// The merge used to preserve engine order: every hit from the first engine, then
// every new hit from the second. That is a decision about configuration order
// masquerading as a decision about relevance, and it is measurably wrong.
// Measured against the real engines:
//
//	query: "post quantum cryptography lattice"
//	bing  post.ca.gov/Training · post.ca.gov/post-profile · usps.com
//	ddg   blog.cloudflare.com/lattice-crypto-primer · ssh.com · redhat.com
//
// Bing matched the word "post" and returned the California Peace Officer
// Standards and Training board. DDG answered the actual question. With engine
// order preserved and bing named first, the user's page opened with three
// irrelevant results — the better engine's answer pushed below the fold by a
// comma in an env var.
//
// TWO SIGNALS, AND NEITHER IS THE ENGINE'S NAME:
//
//   - AGREEMENT. A URL more than one engine returned is more likely to be the
//     answer than one only a single engine found. This is the whole reason to run
//     several engines rather than the best one, and the merge was throwing it
//     away by deduping agreement into a single first-seen hit.
//   - RANK. Within one engine, position carries that engine's own judgement.
//     Averaging the positions a URL held preserves it without letting one engine's
//     ordering dominate the page.
//
// Ties break on the best single rank any engine gave the URL, then on the URL
// itself so the order is TOTAL and the same inputs always produce the same page.
// A non-deterministic search result is a search result nobody can debug.

import "sort"

// scored is one URL's evidence across every engine that returned it.
type scored struct {
	result webResult
	// engines is how many distinct engines returned this URL.
	engines int
	// sumRank is the sum of its zero-based positions, best is the smallest.
	sumRank int
	best    int
	// first is the merge order it was discovered in — the last tiebreak, so the
	// result is stable rather than map-ordered.
	first int
}

// rankMerged orders the per-engine result lists into one page.
//
// perEngine is indexed the same way enabledEngines() is, and a nil entry (an
// engine that failed or was challenged) simply contributes nothing — the same
// rule the rest of this package follows.
func rankMerged(perEngine [][]webResult, limit int) []webResult {
	byURL := map[string]*scored{}
	order := 0
	for _, rs := range perEngine {
		for pos, r := range rs {
			key := normalizeURL(r.URL)
			if key == "" {
				continue
			}
			s, ok := byURL[key]
			if !ok {
				s = &scored{result: r, best: pos, first: order}
				order++
				byURL[key] = s
			}
			s.engines++
			s.sumRank += pos
			if pos < s.best {
				s.best = pos
			}
			// Keep the richest copy: an engine that returned a snippet says more
			// than one that returned a bare link, whichever found it first.
			if len(r.Content) > len(s.result.Content) {
				s.result = r
			}
		}
	}

	all := make([]*scored, 0, len(byURL))
	for _, s := range byURL {
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		// More engines agreeing wins outright.
		if a.engines != b.engines {
			return a.engines > b.engines
		}
		// Then the better average position across the engines that had it.
		ai, bi := a.sumRank*b.engines, b.sumRank*a.engines // compare means without floats
		if ai != bi {
			return ai < bi
		}
		// Then the best single position any engine gave it.
		if a.best != b.best {
			return a.best < b.best
		}
		// Then discovery order, so the sort is total and deterministic.
		return a.first < b.first
	})

	out := make([]webResult, 0, limit)
	for _, s := range all {
		if len(out) >= limit {
			break
		}
		out = append(out, s.result)
	}
	return out
}
