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
		if a.Name == name {
			// Copy: a caller must not be able to mutate the fleet's routing table.
			out := make([]string, len(a.Prefixes))
			copy(out, a.Prefixes)
			return out
		}
	}
	return nil
}
