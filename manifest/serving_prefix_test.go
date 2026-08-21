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

import (
	"strings"
	"testing"
)

// serving is the model-SERVING product: the prefix apps/ml answers, KServe
// InferenceServices under it.
//
// It is named here because it is the one the fleet keeps mis-addressing INTO, and
// for one reason: `ml` is the vague word. A plane that touches models at all reads
// /v1/ml as the obvious home, and openapi.Product then publishes it as part of a
// product it has nothing to do with.
//
// /v1/train was the second entry here and is deliberately not: it was deleted
// (`ml: delete the /v1/train facade — the CRDs behind it are not served`) because
// the two Kubeflow CRDs behind it are not served by the cluster and its health
// door answered 503 in production. A gate must fence prefixes that EXIST — naming
// a deleted one would trip the non-vacuity check below and say nothing true.
var serving = []string{"/v1/ml"}

// servingOwner is the ONE app those two prefixes belong to.
const servingOwner = "ml"

// TestOnlyOneAppAnswersTheServingPrefixes is the gate for a mistake this
// repository has recorded making THREE times.
//
// # Why the address decides the product
//
// openapi.Product takes an operation's product from the FIRST /v1 segment of its
// path and nothing else, and a per-op tag cannot override it (openapi.Fold
// assigns op.Tags from the router projection). So an address is a published
// PRODUCT MEMBERSHIP: the fleet's tag list, the floor ratchet, the doc site's
// headings, every generated SDK's namespace and the CLI's command tree are all
// projections of that one segment. Choosing a path chooses a product, whether or
// not the author meant to.
//
// # The three times
//
// apps/label addressed /v1/ml/labels, apps/reference addressed /v1/ml/reference,
// and apps/datasets addressed /v1/ml/datasets. All three are the RISK product —
// the labels a decision is adjudicated with, the lookup data it consults, and the
// snapshot its model was fitted on — and all three were corrected to /v1/risk/*.
// Each correction records the same finding in its own words: /v1/ml is a LIVE
// product with customers on it, and a second product filed under it makes
// /v1/ml/models mean two things at once. apps/datasets's own address_test.go says
// the third time "stops being a recollection and becomes this gate", and it built
// exactly that gate — for apps/datasets.
//
// A per-app gate cannot catch the fourth time, because the fourth time happens in
// a fifth app that does not have one. This is the invariant stated once, on the
// side that every app has to pass through: the ROUTING GRANT. A plane cannot
// publish under /v1/ml without a manifest row that says so, so a row is the one
// place the mistake is always visible.
//
// # What this refuses, and what it deliberately does not
//
// It refuses a SECOND OWNER, not a second app. Fourteen products in this fleet
// are answered by more than one app (billing, catalog, finance, plans, platform,
// search, usage, vector among them) and 27 apps publish into more than one
// product, so neither "one app per product" nor "one product per app" is a fleet
// invariant and asserting either here would be inventing a rule the fleet does
// not keep. What holds — and what all three mistakes broke — is narrower and
// true: the live serving prefixes have ONE owner, and a plane that is not the
// serving plane belongs somewhere else.
//
// It also does not judge the NAME. `ml` being the vague word is why this keeps
// happening, and renaming a prefix that customers call is a wire change with its
// own cost; until that is worth paying, the ambiguity is fenced rather than
// resolved.
func TestOnlyOneAppAnswersTheServingPrefixes(t *testing.T) {
	granted := 0
	for _, a := range Apps {
		for _, pre := range a.Prefixes {
			for _, s := range serving {
				if !under(pre, s) {
					continue
				}
				granted++
				if a.Name == servingOwner {
					continue
				}
				t.Errorf("ROUTING GRANT: app %q claims %q, which is under the serving prefix %s.\n"+
					"openapi.Product reads a product off the FIRST /v1 segment, so every operation this "+
					"app publishes there joins the %s product — its tag list, its floor, its SDK "+
					"namespace and its CLI command tree — and %s is LIVE with customers on it. That is "+
					"one prefix answering two products, which apps/label, apps/reference and "+
					"apps/datasets each did once and each corrected. Address the plane under the product "+
					"it belongs to; if it genuinely is model serving, it belongs in app %q rather than "+
					"beside it.",
					a.Name, pre, s, strings.TrimPrefix(s, "/v1/"), s, servingOwner)
			}
		}
	}

	// A gate that examined nothing passes. Every way this could see zero grants —
	// a renamed row, a narrowed prefix, a manifest that no longer carries the
	// serving plane at all — is a defect that would otherwise arrive here as a
	// green tick. It is not hypothetical for this fleet: narrowing ai's row to
	// "/v1/ai" took the whole inference surface off the wire with every probe green
	// (see TestInferenceSurfaceIsRoutable).
	if granted == 0 {
		t.Fatalf("no app claims anything under %v — either the serving plane lost its routing "+
			"grant (nothing answers /v1/ml/models) or the prefix was renamed and this gate now "+
			"proves nothing", serving)
	}
	t.Logf("%d grants under %v, all held by %q", granted, serving, servingOwner)
}

// TestTheServingOwnerStillClaimsItsOwnLeaves is the other half, and it is the
// half the ai incident argues for: a gate that only refuses INTRUDERS stays green
// when the owner itself disappears.
//
// The leaves are named individually rather than asserted as a count, because the
// failure that matters is one address going unserved — GET /v1/ml/models
// answering 404 with the pod Ready — and a count cannot say which one.
func TestTheServingOwnerStillClaimsItsOwnLeaves(t *testing.T) {
	for _, p := range []string{
		"/v1/ml/models",
		"/v1/ml/models/x",
		"/v1/ml/models/x/predict",
		"/v1/ml/health",
	} {
		if !claims(servingOwner, p) {
			t.Errorf("app %q does not claim %s — the address is published in the fleet document and "+
				"nothing routes it, which is a 404 on a live surface with every probe green",
				servingOwner, p)
		}
	}
}

// under reports whether prefix falls under the product prefix root: the root
// itself, or a path segment beneath it. It is segment-aware on purpose —
// "/v1/mlops" is a different product from "/v1/ml" and a string-prefix test
// would charge it with squatting one it never touched.
func under(prefix, root string) bool {
	if prefix == root {
		return true
	}
	return strings.HasPrefix(prefix, root+"/")
}
