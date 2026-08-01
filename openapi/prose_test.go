package openapi

import (
	"strings"
	"testing"
)

// A document whose operations all say something passes, and the route printed in
// an operationId is not mistaken for prose.
func TestCompleteAcceptsADocumentThatSaysSomething(t *testing.T) {
	doc := &Document{Paths: map[string]PathItem{
		"/v1/widgets": {"get": {OperationID: "get_v1_widgets", Summary: "List your org's widgets"}},
		"/v1/widgets/{id}": {"delete": {
			OperationID: "delete_v1_widgets_by_id",
			Description: "Removes the addressed widget. Answers 204 once it is gone.",
		}},
	}}
	if err := Complete(doc); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

// The first half of the law: an operation with neither summary nor description is
// refused, named, and the message says where the sentence goes. An operationId is
// not a sentence — it is the same mechanical string a fallback would print.
func TestCompleteRefusesAnOperationThatSaysNothing(t *testing.T) {
	doc := &Document{Paths: map[string]PathItem{
		"/v1/widgets":      {"get": {OperationID: "get_v1_widgets", Summary: "List your org's widgets"}},
		"/v1/widgets/{id}": {"delete": {OperationID: "delete_v1_widgets_by_id"}},
	}}
	err := Complete(doc)
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
			OperationID: "post_v1_store_storefront-token",
			Summary:     "Mint your org's least-privilege storefront read key",
			Tags:        []string{"store"},
		}},
	}}
	err := Complete(doc)
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
		"/v1/widgets": {"get": {OperationID: "get_v1_widgets", Summary: "List your org's widgets"}},
	}}
	if err := Complete(doc); err != nil {
		t.Fatalf("another product's declaration was charged to this app: %v", err)
	}
}

// A wildcard route is described by the pattern the ROUTER carries (/v1/kms/secrets/+),
// while the document renders it as a template (/v1/kms/secrets/{wildcard1}). The
// orphan check compares the two through the same translation the projection uses,
// so a correctly-keyed description of a relay door is not read as an orphan.
func TestCompleteReadsAWildcardDeclarationThroughTheSameTranslation(t *testing.T) {
	Describe("/v1/kms/secrets/+", "GET", "Read one secret", "Answers the addressed secret's value.")
	t.Cleanup(func() { unregister("/v1/kms/secrets/+", "GET") })

	doc := &Document{Paths: map[string]PathItem{
		"/v1/kms/secrets/{wildcard1}": {"get": {
			OperationID: "get_v1_kms_secrets_by_wildcard1",
			Summary:     "Read one secret",
			Tags:        []string{"kms"},
		}},
	}}
	if err := Complete(doc); err != nil {
		t.Fatalf("a correctly-keyed wildcard declaration was read as an orphan: %v", err)
	}
}
