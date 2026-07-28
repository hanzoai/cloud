// Package rank is the ONE rank-fusion implementation in the codebase.
//
// It is a leaf: it imports nothing and knows nothing about documents, orgs, or
// stores. That is deliberate. Fusion is needed at two DIFFERENT levels — across
// the tiers inside one corpus (clients/code fuses lexical + symbolic + semantic)
// and across corpora at the /v1/search surface — and a package that knew about
// either level could only serve that one, which is how a codebase ends up with
// two copies of the same algorithm drifting apart.
//
// Callers pass ranked KEYS and get back fused keys with provenance; mapping keys
// to payloads stays with the caller, who is the only one who knows what a key
// means.
package rank

import "sort"

// K damps the contribution of deep ranks in RRF. 60 is the value from the
// original paper and the one every mainstream implementation ships. It is a
// constant rather than a knob because making it tunable invites per-corpus
// fiddling, which is the thing rank fusion exists to avoid.
const K = 60.0

// List is ONE ranked input — a source name and its keys in rank order. Order is
// the entire signal: Keys[0] is that source's best hit.
type List struct {
	Source string
	Keys   []string
	// Scores optionally carries each key's native score, positionally aligned
	// with Keys. It is reported back as provenance and never used for ranking:
	// sources score on incomparable scales (a term-match count and a cosine
	// similarity), which is precisely why fusion uses ranks.
	Scores []float64
}

// Origin records that one source matched one key, at what rank and native score.
// Without it a fused ranking is unexplainable: you cannot distinguish a hit two
// sources agreed on from one only a single source saw, and you cannot tell a
// healthy source from one quietly returning nothing.
type Origin struct {
	Source string
	Rank   int
	Score  float64
}

// Fused is one output row: the key, its fused score, and every source that
// contributed to it.
type Fused struct {
	Key     string
	Score   float64
	Origins []Origin
}

// Fuse combines ranked lists into one ordered result. It is a variable, not a
// function, so a deployment or a test can substitute a different strategy without
// any caller changing; nil is not a valid value and callers should not set it.
var Fuse = RRF

// RRF is Reciprocal Rank Fusion: score(d) = Σ 1/(K + rank) over every list
// containing d, ranks being 1-based.
//
// WHY THIS AND NOT A WEIGHTED SUM. The inputs score on incomparable scales, so
// adding them requires a normalizer and a per-source weight — tuned magic numbers
// that are right for the corpus they were fitted on and silently wrong everywhere
// else. RRF discards the scores and keeps only ranks, which are comparable by
// construction. It needs no tuning, cannot be miscalibrated by a shifting score
// distribution, and degrades gracefully when a source drops out: the survivors'
// ranks are unchanged, so a partial answer is still correctly ordered.
//
// A document found by two sources outranks one found by either alone at the same
// depth — the whole reason to run both. Ties break on first appearance so paging
// is stable across identical queries.
func RRF(lists []List, limit int) []Fused {
	type acc struct {
		f     Fused
		order int
	}
	byKey := map[string]*acc{}
	seq := 0
	for _, l := range lists {
		for i, key := range l.Keys {
			a, ok := byKey[key]
			if !ok {
				a = &acc{f: Fused{Key: key}, order: seq}
				seq++
				byKey[key] = a
			}
			a.f.Score += 1.0 / (K + float64(i+1))
			var native float64
			if i < len(l.Scores) {
				native = l.Scores[i]
			}
			a.f.Origins = append(a.f.Origins, Origin{Source: l.Source, Rank: i + 1, Score: native})
		}
	}
	out := make([]*acc, 0, len(byKey))
	for _, a := range byKey {
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].f.Score != out[j].f.Score {
			return out[i].f.Score > out[j].f.Score
		}
		return out[i].order < out[j].order
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	fused := make([]Fused, 0, len(out))
	for _, a := range out {
		fused = append(fused, a.f)
	}
	return fused
}
