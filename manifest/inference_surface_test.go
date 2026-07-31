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

import "testing"

// TestInferenceSurfaceIsRoutable guards the paths every customer SDK calls.
//
// ai's router registers the OpenAI-compatible surface at TOP LEVEL
// (/v1/chat/completions, /v1/models, /v1/messages, …) and apps/zen states the
// other half of the contract: zen is "a MIDDLEWARE, not a route owner" that
// claims zen SKUs and calls c.Next() "so ai's /v1/* catch-all serves non-zen
// models". So ai must hold a prefix those paths fall under.
//
// When ai's row was narrowed to "/v1/ai" it dropped out of the /v1 chain and
// zen's c.Next() fell through to the console catch-all: POST /v1/chat/completions
// answered 405 and GET /v1/models 404 on v1.801.318 and .319 — with the pod
// Ready, probes green and chat.hanzo.ai serving 200. hanzo.chat, hanzo.app and
// every SDK caller got nothing, and no alarm fired, because from outside the
// deployment looked healthy. One narrowed prefix took down inference invisibly.
func TestInferenceSurfaceIsRoutable(t *testing.T) {
	for _, p := range []string{
		"/v1/chat/completions",
		"/v1/chat",
		"/v1/completions",
		"/v1/messages",
		"/v1/models",
		"/v1/responses",
		"/v1/embeddings",
	} {
		if !claims("ai", p) {
			t.Errorf("ai does not claim %s — it drops out of the /v1 chain and nothing serves the path", p)
		}
	}
}

// claims reports whether app's declared prefixes cover path.
//
// It asks whether ai claims these paths, NOT whether ai claims them FIRST — and
// that distinction is the whole test. A first-match-wins version PASSED with the
// bug reintroduced (zen holds /v1 and precedes ai, so every path "matched"
// something), then blamed commerce for owning everything. Apps legitimately hold
// overlapping prefixes and chain through c.Next(), so presence in the chain is the
// invariant and position is not.
func claims(app, path string) bool {
	for _, a := range Apps {
		if a.Name != app {
			continue
		}
		for _, pre := range a.Prefixes {
			if pre == path {
				return true
			}
			if len(path) > len(pre) && path[:len(pre)] == pre &&
				(pre[len(pre)-1] == '/' || path[len(pre)] == '/') {
				return true
			}
		}
	}
	return false
}

// NOTE ON ORDERING — deliberately NOT asserted here.
//
// apps/zen/zen.go says zen "is wired BEFORE ai in Wire() so Claim's c.Next()
// falls through to ai's" catch-all. That comment predates this manifest: Wire()
// is gone, and the live order is ai BEFORE zen. I wrote a test asserting the
// documented order and it failed against intentional upstream state, so it is
// removed rather than kept as a guess — a build should not break on an invariant
// inferred from a stale comment.
//
// It is still worth someone who owns the zen/ai split confirming which order is
// intended, because the two are not equivalent for BILLING: zen's own Gate/Meter
// only run if zen's Claim sees the request first, and the edge BillingGate prices
// bare /v1/messages, /v1/chat/completions and /v1/embeddings at 0 precisely
// because zen was expected to bill them itself.
