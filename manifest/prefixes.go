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

// PrefixesFor returns the paths app answers, as declared in Apps.
//
// WHY THIS EXISTS. "Which paths does this app answer" is ONE fact, and it was
// being written down twice: here, where the light host reads it to route, and
// again as a literal in plugin/<app>/main.go, where the app states its own
// surface. Nothing made the two agree. They happened to agree — I checked all
// four that restated them — but "happened to" is the whole problem: the copies
// are in different files, edited by different changes, and a disagreement is
// invisible until a customer's request 404s.
//
// That is exactly how inference went down on v1.801.318/.319: ai's manifest row
// said "/v1/ai" while its router served /v1/chat/completions and /v1/models at
// top level. Both halves were locally sensible; only the pair was wrong, and
// nothing was looking at the pair.
//
// So the host's list is THE list, and an app reads it rather than restating it.
// One fact, one place, and the drift is not merely detected but unrepresentable.
//
// An unknown name returns nil, which zip treats as "no prefix claimed" — a plugin
// whose name does not appear in Apps was never routable anyway, and
// TestEveryPluginNameIsInTheManifest keeps that from happening silently.
func PrefixesFor(name string) []string {
	for _, a := range Apps {
		if a.Name == name || (name == "projects" && a.Name == "project") || (name == "project" && a.Name == "projects") {
			// Copy: a caller must not be able to mutate the fleet's routing table.
			out := make([]string, len(a.Prefixes))
			copy(out, a.Prefixes)
			return out
		}
	}
	return nil
}

// Coresident reports whether the named app routes no prefix of its own — it
// mounts as middleware on a sibling's router and decides per request whether to
// serve or Next (App.Coresident).
//
// It is the ONE predicate for that question. Three callers already fold a set on
// it — the host that declines to spawn a child (cmd/cloud mount), the derivation
// that skips it when listing what is elsewhere (Elsewhere), and the program that
// names itself (cloud.procName) — and each rebuilding the answer from Apps is how
// "who routes" comes to mean different things in the process that spawns and the
// process that runs.
//
// An unknown name routes for itself: a plugin absent from Apps was never a
// passenger on anybody, and TestEveryPluginNameIsInTheManifest keeps the case
// from arising silently.
func Coresident(name string) bool {
	for _, a := range Apps {
		if a.Name == name {
			return a.Coresident
		}
	}
	return false
}

// StageOf is the stage the named app's row declares (App.Stage, HIP-0139 §8).
//
// It is a function rather than a field read at each call site because the two
// callers are in packages that cannot see this type: the compose takes it as
// `func(string) string` (openapi cannot import manifest — manifest's own tests
// read that package), and the refusal at Serve takes the name it was handed.
//
// An unknown name is ga, which is the same answer the zero value gives. A name
// that is not in Apps was never routed here, so there is no prefix for a refusal
// to sit on and nothing for a stage to decide.
func StageOf(name string) string {
	for _, a := range Apps {
		if a.Name == name {
			return a.Stage
		}
	}
	return ""
}

// GrantFor is the subtrees whose MIDDLEWARE an app may install — what
// cloud.Plugin.Prefixes bounds, and what a plugin/<app>/main.go hands it.
//
// IT IS A DIFFERENT QUESTION FROM PrefixesFor, and for one app a different
// answer. PrefixesFor asks what the host ROUTES to an app; GrantFor asks what an
// app may WRAP. For every routed app they coincide — you gate what you serve —
// and TestGrantMatchesPrefixesForRoutedApps keeps that true, so this is not a
// second list to maintain.
//
// zen is the app that separates them, and it separated them the expensive way.
// It is Coresident: it answers no path and mounts as a Claim on ai's "/v1". Its
// row therefore states no prefix — correct, and what stopped it duplicating ai's
// routing claim. But plugin/zen/main.go fed that same nil into the GRANT, so the
// scope zen received owned only the conventional "/v1/zen", installing the Claim
// on "/v1" escaped it, and UseAll refused the mount outright. One field had
// been answering two questions, and dropping the routing answer silently revoked
// the gate.
//
// Gates is the second answer, stated once, where a co-resident app can say what
// it wraps without claiming to serve it.
func GrantFor(name string) []string {
	for _, a := range Apps {
		if a.Name == name {
			src := a.Gates
			if len(src) == 0 {
				src = a.Prefixes
			}
			out := make([]string, len(src))
			copy(out, src)
			return out
		}
	}
	return nil
}

// OwnerOf reports which app the host routes a path to: the app whose declared
// prefix is the LONGEST match. That is the same rule the router itself applies,
// and stating it here lets anything downstream ask "whose surface is this?"
// without restating the routing table — the mistake PrefixesFor exists to avoid.
//
// It matters because prefixes nest. `code` is routed /v1/code while `lsp` is
// routed the more specific /v1/code/lsp — so a shorter prefix from a different
// app can swallow a path it does not actually serve. Anything deciding policy
// from a bare HasPrefix scan will attribute those paths to the wrong app.
//
// An unrouted path returns "" — the caller decides what that means.
//
// The match is SEGMENT-WISE, because one address has three spellings: the
// manifest writes a parameter as `:skill`, a document writes it `{skill}`, and a
// real request carries the value itself. A byte-prefix compare answers only the
// literal spelling, which is how a declared owner of a parameterised prefix —
// skills at /.well-known/agent-skills/:skill/SKILL.md — reads as unowned and its
// live operations get pruned as a sibling's. A `:name` prefix segment matches any
// single path segment; a literal one matches itself.
// The path is NORMALIZED first, so both spellings of a capability's number answer
// here exactly as they do at the router (number.go). "Whose surface is /v1/agents?"
// and "whose surface is /v1/agent?" must give ONE answer, because the router
// gives one answer; a static rule disagreeing with the live one is the pairwise
// defect this file exists to prevent.
func OwnerOf(path string) string {
	ps := segments(Normalize(path))
	best, bestDepth := "", -1
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			pre := segments(p)
			if len(pre) == 0 || len(pre) > len(ps) {
				continue
			}
			matched := true
			for i, s := range pre {
				if strings.HasPrefix(s, ":") {
					continue // a parameter owns whatever stands in its place
				}
				if ps[i] != s {
					matched = false
					break
				}
			}
			if matched && len(pre) > bestDepth {
				best, bestDepth = a.Name, len(pre)
			}
		}
	}
	return best
}

// segments is a path as the router walks it: leading and trailing slashes
// dropped, one entry per segment.
func segments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}
