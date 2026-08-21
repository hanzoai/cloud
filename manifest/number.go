// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package manifest

import "strings"

// GRAMMATICAL NUMBER, decided once, for every capability, forever.
//
// A capability has ONE name — the one in Apps — and that name IS its address,
// its tag, its tool, its CLI word and its docs page (HIP-0139 §3). English gives
// every count noun two spellings, so a caller holding one of them has a
// one-in-two chance of the one we chose. The answer for years was to RENAME the
// capability whenever somebody guessed wrong, and the churn is measurable: five
// capabilities were renamed for number inside one week (65317fd75, 35a75797),
// each rename touching nine projections and every generated client.
//
// The rename was never necessary. A word's number is a fact about ENGLISH, and
// English is derivable, so the ROUTER derives it: every capability answers at
// both spellings of its name and PUBLISHES exactly one. The document, the SDKs,
// the tool list and the docs carry the canonical spelling alone — two published
// names for one thing is precisely the duplication this exists to end — and the
// other spelling is a courtesy the router extends on the way in, never a second
// address the document claims.
//
// That asymmetry is the whole design. Publishing both would make the fleet's
// surface ambiguous and double every route table it passes through; accepting
// both costs one string comparison at the door and makes a whole class of
// customer error impossible.
//
// THE CONSEQUENCE IS THE POINT: a capability is never renamed for number again.
// Whichever spelling Apps carries, the other one already answers, so the reason
// to reach for a rename has been removed rather than resolved. HIP-0139 §2.2.

// Plural is the ONE pluralisation rule in this repository, exported because it
// has a second caller: fleet/verbs.go turns an operation id into the phrase an
// SDK method and a CLI verb read as, and a collection read at a singular address
// needs the plural of that address's noun to say "list". Two copies of English
// in one repo is two answers to one question, so there is one.
//
// It is naive English, deliberately and completely:
//
//	-y after a consonant   ->  -ies      (entity   -> entities)
//	-s -x -z -ch -sh       ->  -es       (sandbox  -> sandboxes)
//	everything else        ->  -s        (agent    -> agents)
//
// No irregulars, no dictionary, no inflection library. Naive is sufficient
// because the input is not English at large: it is the 122 words in Apps, which
// we choose, and a capability whose plural this gets wrong is a capability we
// should have named something else — HIP-0139 §2.5 already bans the compound and
// the coinage. If one ever arrives it goes in noNumber, and the alias listing
// shows exactly what was lost.
func Plural(w string) string {
	switch {
	case strings.HasSuffix(w, "y") && len(w) > 1 && !isVowel(w[len(w)-2]):
		return w[:len(w)-1] + "ies"
	case strings.HasSuffix(w, "s"), strings.HasSuffix(w, "x"), strings.HasSuffix(w, "z"),
		strings.HasSuffix(w, "ch"), strings.HasSuffix(w, "sh"):
		return w + "es"
	}
	return w + "s"
}

// singular is Plural READ BACKWARDS, and it is defined that way rather than
// written as a second table of suffixes: it proposes the stem and accepts it
// only if pluralising it yields the word back. So the two are inverse by
// CONSTRUCTION, and there is exactly one rule of English in this file.
//
// That is not fussiness — a second table gets the ambiguous cases wrong, and
// both of the fleet's live ambiguities are in that class. "-es" is a real
// inflection in `sandboxes` and is not one in `bases`, whose stem is `base`; a
// table stripping "-es" answers `bas`. And a word may END in an inflection
// without carrying one: `ingress` is singular, and a table stripping "-s"
// answers `ingres`. Asking "does this pluralise back?" settles both without
// knowing anything about them — sandbox+es is sandboxes, base+s is bases,
// ingres+es is ingreses, so only the true stems survive.
//
// A word that is not a plural of anything returns "", which is the honest
// answer for every singular name in Apps.
func singular(w string) string {
	if one, named := irregular[w]; named {
		return one
	}
	if strings.HasSuffix(w, "ies") && len(w) > 4 {
		if stem := w[:len(w)-3] + "y"; Plural(stem) == w {
			return stem
		}
	}
	// Longest inflection first: "-es" is two letters where "-s" is one, and a
	// word carrying the longer one also ends in the shorter.
	for _, cut := range []int{2, 1} {
		if len(w) > cut {
			if stem := w[:len(w)-cut]; Plural(stem) == w {
				return stem
			}
		}
	}
	return ""
}

// irregular is the short, explicit list of plurals the one rule above reads
// wrongly, and it exists because English genuinely is ambiguous here — not
// because the rule is lazy.
//
// THE CLASS, so the next line has a reason rather than a precedent: a stem
// ending in a sibilant (s, x, z, ch, sh) followed by a silent -e spells the same
// plural as the stem WITHOUT that -e. `base` + s and `bas` + es are both
// `bases`, and nothing in the spelling says which one was meant. Every word of
// that shape is ambiguous; no word of another shape is, which is why this list
// has one entry rather than a hundred.
//
// A guess here is worse than an exception: read as `bas`, the alias would open
// /v1/bas and leave /v1/base — the address the fleet actually serves — reachable
// only by luck. State it and move on.
var irregular = map[string]string{
	"bases": "base",
}

// alt is the OTHER spelling of a word's number — the singular of a plural, the
// plural of a singular. It is an involution over our vocabulary, which is what
// lets ONE function serve both directions of the alias: nothing has to know
// whether the row it is holding is the singular or the plural, so a capability
// that is renamed from one to the other needs no edit here at all.
//
// A word with no grammatical number returns "" — see noNumber.
func alt(w string) string {
	if noNumber[w] {
		return ""
	}
	if one := singular(w); one != "" {
		return one
	}
	return Plural(w)
}

func isVowel(b byte) bool { return strings.IndexByte("aeiou", b) >= 0 }

// noNumber is the vocabulary that has no grammatical number, so no alias is
// derived for it and none is accepted.
//
// Every entry is here for ONE reason: the word ends in a letter the naive rule
// above reads as an inflection when it is not one, or it is not an English count
// noun at all. `dns` is not the plural of `dn`; `s3` and `x402` and `o11y` are
// not words; `ai`, `iam`, `sbom` and `seo` are initialisms that happen to carry
// vowels. Left to the rule, each would open a nonsense address — /v1/dn, /v1/km —
// that resolves to a real capability, which is worse than opening nothing.
//
// It is a VOCABULARY, not per-capability configuration: it says what these WORDS
// are, and it is read by the one derivation above rather than consulted per app
// at any call site. TestNoNumberNamesRealCapabilities keeps it from rotting.
var noNumber = map[string]bool{
	"ai":    true, // initialism
	"amqp":  true, // initialism
	"authz": true, // abbreviation of authorization
	"dns":   true, // initialism ending in s
	"iam":   true, // initialism
	"kms":   true, // initialism ending in s
	"kv":    true, // initialism
	"lsp":   true, // initialism
	"ml":    true, // initialism
	"mq":    true, // initialism
	"o11y":  true, // numeronym
	"s3":    true, // carries a digit
	"sbom":  true, // initialism
	"seo":   true, // initialism
	"tel":   true, // abbreviation of telemetry
	"x402":  true, // names a protocol and a status code
}

// alias maps a non-canonical PREFIX to the canonical one it means.
//
// It is keyed on the whole prefix and not on the bare word, and that is the
// safety property: only the segment that NAMES a capability is ever rewritten.
// A capability's own surface is untouched, so git's /v1/git/projects keeps every
// letter it has even while /v1/projects is an alias of /v1/project — the alias
// is anchored at the root, so a sibling's word deep inside somebody else's
// subtree can never be caught by it.
//
// A derived spelling that is ALREADY some app's canonical prefix is dropped. The
// canonical always wins: an alias may only open an address that nothing serves,
// never redirect one that something does. TestNoAliasShadowsACanonicalPrefix
// states it as a gate rather than leaving it as a property of this loop.
var alias = func() map[string]string {
	canonical := map[string]bool{}
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			canonical[p] = true
		}
	}
	out := map[string]string{}
	for _, a := range Apps {
		other := alt(a.Name)
		if other == "" {
			continue
		}
		for _, p := range a.Prefixes {
			spelt := respell(p, a.Name, other)
			if spelt == p || canonical[spelt] {
				continue
			}
			out[spelt] = p
		}
	}
	return out
}()

// respell rewrites the segments of p that are exactly from to to. A prefix names
// its capability as a whole segment (/v1/agent, /v1/admin/pricing), so a segment
// compare is the whole of it — and it is why an app whose name appears as a
// substring of a segment (s3 in /v1/s3/buckets) cannot be caught by accident.
func respell(p, from, to string) string {
	segs := segments(p)
	for i, s := range segs {
		if s == from {
			segs[i] = to
		}
	}
	return "/" + strings.Join(segs, "/")
}

// Normalize is the canonical spelling of an address: the same path with any
// aliased capability name rewritten to the name Apps carries. A path that is
// already canonical is returned unchanged, which is every path the fleet
// publishes.
//
// It is THE one place the alias is applied. The host calls it at the door, ahead
// of routing, so every child sees canonical paths and no app carries a second
// route; OwnerOf calls it so that "whose surface is this?" answers the same for
// both spellings; and nothing else needs to know the alias exists.
//
// The match is the LONGEST alias prefix, for the reason OwnerOf matches the
// longest declared one: prefixes nest, and a shorter alias from a different app
// must not swallow a path a deeper one spells exactly.
func Normalize(path string) string {
	segs := segments(path)
	for n := len(segs); n > 0; n-- {
		head := "/" + strings.Join(segs[:n], "/")
		to, ok := alias[head]
		if !ok {
			continue
		}
		if n == len(segs) {
			return to
		}
		return to + "/" + strings.Join(segs[n:], "/")
	}
	return path
}
