package openapi

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
)

// The gate is exercised with the FLEET'S OWN routing table, not a stand-in.
//
// A hand-written table here would let these tests agree with themselves while
// disagreeing with the host — which is the entire defect class this file pins.
// manifest.OwnerOf is what describe.go passes in production, so the rule under
// test is the rule that ships. (openapi is imported BY manifest's tests, never the
// other way round; reading manifest from openapi's own test binary is fine, and
// the one-way edge is why Complete takes an [Owner] instead of importing it.)
var routed = manifest.OwnerOf

// A document whose operations all say something passes, and the route printed in
// an operationId is not mistaken for prose.
func TestCompleteAcceptsADocumentThatSaysSomething(t *testing.T) {
	doc := &Document{Paths: map[string]PathItem{
		"/v1/widgets": {"get": {OperationID: "get_widgets", Summary: "List your org's widgets"}},
		"/v1/widgets/{id}": {"delete": {
			OperationID: "delete_widgets_by_id",
			Description: "Removes the addressed widget. Answers 204 once it is gone.",
		}},
	}}
	if err := Complete(doc, routed); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

// The first half of the law: an operation with neither summary nor description is
// refused, named, and the message says where the sentence goes. An operationId is
// not a sentence — it is the same mechanical string a fallback would print.
func TestCompleteRefusesAnOperationThatSaysNothing(t *testing.T) {
	doc := &Document{Paths: map[string]PathItem{
		"/v1/widgets":      {"get": {OperationID: "get_widgets", Summary: "List your org's widgets"}},
		"/v1/widgets/{id}": {"delete": {OperationID: "delete_widgets_by_id"}},
	}}
	err := Complete(doc, routed)
	if err == nil {
		t.Fatal("an operation with no prose was accepted")
	}
	if !strings.Contains(err.Error(), "DELETE /v1/widgets/{id}") {
		t.Errorf("the failure must name the operation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "doc comment") {
		t.Errorf("the failure must name the remedy, got: %v", err)
	}
}

// The second half: prose keyed to a route this app does not serve renders
// NOWHERE while reading in the source as though it had landed. That is how
// POST /v1/store/storefront-token published an operationId and nothing else while
// its description sat under the store's old /v1/store/token address.
func TestCompleteRefusesADescriptionThatNamesNoOperation(t *testing.T) {
	Describe("/v1/store/token", "POST", "Mint a storefront key", "The prose, keyed to yesterday's address.")
	t.Cleanup(func() { unregister("/v1/store/token", "POST") })

	doc := &Document{Paths: map[string]PathItem{
		"/v1/store/storefront-token": {"post": {
			OperationID: "post_store_storefront-token",
			Summary:     "Mint your org's least-privilege storefront read key",
			Tags:        []string{"store"},
		}},
	}}
	err := Complete(doc, routed)
	if err == nil {
		t.Fatal("a description keyed to no route was accepted")
	}
	if !strings.Contains(err.Error(), "POST /v1/store/token") {
		t.Errorf("the failure must name the orphaned declaration, got: %v", err)
	}
}

// An app is judged only inside the products it publishes. Every app binary links
// cloud's core, so a subset is built with declarations belonging to subsystems it
// does not mount — those are not its business, and refusing them would make every
// app answer for every other.
func TestCompleteIgnoresProseForAProductThisAppDoesNotPublish(t *testing.T) {
	Describe("/v1/metrics/query", "GET", "Query one signal", "Prose for a product this app does not serve.")
	t.Cleanup(func() { unregister("/v1/metrics/query", "GET") })

	doc := &Document{Paths: map[string]PathItem{
		"/v1/widgets": {"get": {OperationID: "get_widgets", Summary: "List your org's widgets"}},
	}}
	if err := Complete(doc, routed); err != nil {
		t.Fatalf("another product's declaration was charged to this app: %v", err)
	}
}

// A wildcard route is described by the pattern the ROUTER carries (/v1/kms/secrets/+),
// while the document renders it as a template (/v1/kms/secrets/{wildcard1}). The
// orphan check compares the two through the same translation the projection uses,
// so a correctly-keyed description of a relay is not read as an orphan.
func TestCompleteReadsAWildcardDeclarationThroughTheSameTranslation(t *testing.T) {
	Describe("/v1/kms/secrets/+", "GET", "Read one secret", "Answers the addressed secret's value.")
	t.Cleanup(func() { unregister("/v1/kms/secrets/+", "GET") })

	doc := &Document{Paths: map[string]PathItem{
		"/v1/kms/secrets/{wildcard1}": {"get": {
			OperationID: "get_kms_secrets_by_wildcard1",
			Summary:     "Read one secret",
			Tags:        []string{"kms"},
		}},
	}}
	if err := Complete(doc, routed); err != nil {
		t.Fatalf("a correctly-keyed wildcard declaration was read as an orphan: %v", err)
	}
}

// THE FALSE POSITIVE, at the pair that produced it.
//
// provisioning both SERVES and DESCRIBES POST /v1/s3 — the route at
// apps/provisioning/provisioning.go:257, the prose at :341 — while the s3 app
// serves the deeper /v1/s3/buckets and /v1/s3/health. By product segment both are
// "s3", so the data plane was charged with a declaration that is provisioning's
// and correct, and `make check` died on an app with nothing wrong with it:
//
//	s3: 1 description(s) name no operation:
//	  POST /v1/s3
//
// By owner they are two apps, and s3 is never asked about provisioning's prose.
func TestCompleteDoesNotChargeAnAppForASiblingSharingItsProduct(t *testing.T) {
	Describe("/v1/s3", "POST", "Provision an s3 resource",
		"provisioning's own declaration, on a route provisioning itself serves.")
	t.Cleanup(func() { unregister("/v1/s3", "POST") })

	dataplane := &Document{Paths: map[string]PathItem{
		"/v1/s3/buckets": {"get": {OperationID: "get_s3_buckets", Summary: "List your org's buckets"}},
		"/v1/s3/health":  {"get": {OperationID: "get_s3_health", Summary: "Report the object store's reachability"}},
	}}
	if err := Complete(dataplane, routed); err != nil {
		t.Fatalf("s3 was charged with provisioning's declaration: %v", err)
	}
}

// ...and the gate still goes RED on the defect it exists for, in the shape it
// actually happened.
//
// This is the mutation that proves the fix is a fix and not a deletion. The
// declaration is mis-keyed INSIDE s3's own prefix — "object" for the "objects"
// the router carries — so the fleet routes the address to s3, the prose renders
// nowhere, and reading the source it looks landed. Exactly
// POST /v1/store/storefront-token, one app over. Attributing by owner must not
// soften this by one inch.
func TestCompleteStillRefusesADeclarationMisfiledInsideTheAppsOwnPrefix(t *testing.T) {
	const misfiled = "/v1/s3/buckets/:bucket/object" // the router carries .../objects
	if got := routed(misfiled); got != "s3" {
		t.Fatalf("premise broken: the fleet routes %s to %q, so this no longer tests "+
			"a misfile inside s3's OWN surface", misfiled, got)
	}
	Describe(misfiled, "GET", "List a bucket's objects", "Prose one character off the address.")
	t.Cleanup(func() { unregister(misfiled, "GET") })

	dataplane := &Document{Paths: map[string]PathItem{
		"/v1/s3/buckets": {"get": {OperationID: "get_s3_buckets", Summary: "List your org's buckets"}},
		"/v1/s3/buckets/{bucket}/objects": {"get": {
			OperationID: "get_s3_buckets_by_bucket_objects",
			Summary:     "List the objects in one bucket",
		}},
	}}
	err := Complete(dataplane, routed)
	if err == nil {
		t.Fatal("a declaration misfiled inside the app's own prefix was ACCEPTED — the gate " +
			"is gone, which is worse than the false positive it replaced")
	}
	if !strings.Contains(err.Error(), "GET "+misfiled) {
		t.Errorf("the failure must name the orphaned declaration, got: %v", err)
	}
}

// The rule is a RULE, not a patch for s3.
//
// Fourteen products are answered by more than one app, and every one of them is
// a pair the product segment collapses and longest prefix separates. For each,
// prose on the shallower app's address must not be charged to the deeper app.
// DERIVED from the manifest rather than listed, so the fifteenth pair is covered
// the day someone adds it — and so this test states the general fact rather than
// re-asserting the one instance that happened to break.
func TestNoAppIsChargedForASiblingSharingItsProduct(t *testing.T) {
	type claim struct{ app, prefix string }
	byProduct := map[string][]claim{}
	for _, a := range manifest.Apps {
		for _, p := range a.Prefixes {
			if pr := Product(p); pr != "" {
				byProduct[pr] = append(byProduct[pr], claim{a.Name, p})
			}
		}
	}

	pairs := 0
	for product, claims := range byProduct {
		for _, mine := range claims {
			for _, sibling := range claims {
				if mine.app == sibling.app || mine.prefix == sibling.prefix {
					continue
				}
				pairs++
				t.Run(product+":"+sibling.app+"->"+mine.app, func(t *testing.T) {
					// The premise: one product, two apps, and the manifest routes
					// each prefix to its own. This is what Product() collapsed.
					if got := routed(sibling.prefix); got != sibling.app {
						t.Fatalf("%s routes to %q, not %q", sibling.prefix, got, sibling.app)
					}
					if got := routed(mine.prefix); got != mine.app {
						t.Fatalf("%s routes to %q, not %q", mine.prefix, got, mine.app)
					}

					Describe(sibling.prefix, "POST", "The sibling's own operation",
						"Declared by the app that serves it, on the address it serves.")
					t.Cleanup(func() { unregister(sibling.prefix, "POST") })

					doc := &Document{Paths: map[string]PathItem{
						mine.prefix: {"get": {OperationID: "probe", Summary: "This app's own operation"}},
					}}
					if err := Complete(doc, routed); err != nil {
						t.Fatalf("%s was charged with %s's declaration on %s (both are product %q): %v",
							mine.app, sibling.app, sibling.prefix, product, err)
					}
				})
			}
		}
	}
	if pairs == 0 {
		t.Fatal("no product is shared by two apps — this test has stopped testing anything; " +
			"if that is real, the collision class is gone and so is the reason for owner attribution")
	}
	t.Logf("%d shared-product app pairs, none charged for a sibling's prose", pairs)
}

// No routing table, no judgement — and no document either.
//
// The dangerous default is the quiet one: judged against nothing, every
// declaration looks like somebody else's and the gate reports success on the whole
// class of defect it exists to catch. It fails closed instead, so a caller that
// forgets cannot ship an unchecked artifact.
func TestCompleteRefusesToJudgeWithoutTheRoutingTable(t *testing.T) {
	doc := &Document{Paths: map[string]PathItem{
		"/v1/widgets": {"get": {OperationID: "get_widgets", Summary: "List your org's widgets"}},
	}}
	err := Complete(doc, nil)
	if err == nil {
		t.Fatal("Complete judged a document with no routing table")
	}
	if !strings.Contains(err.Error(), "OwnerOf") {
		t.Errorf("the failure must name the remedy, got: %v", err)
	}
}

// A tag is the app that serves the operation (HIP-0139 §4), so its sentence is
// the app's own — never a sibling's that happens to hold a deeper route.
func TestProseIsTheAppsOwnSentence(t *testing.T) {
	parts := []Part{
		{App: "commerce", Doc: &Document{
			Info:  Info{Description: "Package commerce is selling: checkout, subscriptions, invoices."},
			Paths: map[string]PathItem{"/v1/plans/entries": {"get": {Tags: []string{"commerce"}}}},
		}},
		{App: "plan", Doc: &Document{
			Info:  Info{Description: "Package plan is the plan catalog: every tier you can buy."},
			Paths: map[string]PathItem{"/v1/plan": {"get": {Tags: []string{"plan"}}}},
		}},
	}
	said := prose(parts)
	if !strings.HasPrefix(said["plan"], "Package plan is the plan catalog") {
		t.Fatalf("plan took the wrong sentence: %q", said["plan"])
	}
	if !strings.HasPrefix(said["commerce"], "Package commerce is selling") {
		t.Fatalf("commerce took the wrong sentence: %q", said["commerce"])
	}
}

// An app with no package doc leaves its tag blank rather than taking the fleet's
// own sentence — an undescribed capability must stay distinguishable from a
// described one.
func TestProseIsSilentWhenTheAppSaysNothingAboutItself(t *testing.T) {
	parts := []Part{{App: "metrics", Doc: &Document{
		Info:  fleetInfo,
		Paths: map[string]PathItem{"/v1/metrics/query": {"get": {Tags: []string{"metrics"}}}},
	}}}
	if got, said := prose(parts)["metrics"]; said {
		t.Fatalf("the fleet's own sentence was published as a capability's description: %q", got)
	}
}
