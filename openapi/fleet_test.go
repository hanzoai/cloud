package openapi_test

// The composition the LIGHT HOST serves, in isolation from the fleet's size.
//
// openapi/weave_test.go proves Fleet over the real 113 subsets equals the
// committed golden, and cmd/cloud/openapi_test.go proves the host answers with
// exactly that. What is left — and what those two cannot show, because both run
// on a document that is already correct — is what Fleet and MountFleet do when
// an input is WRONG. A composition that drops a bad input silently publishes an
// API with one product missing and stays green: that is how plugin/ingress lost
// eight paths from every generated SDK.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// subset is one app's committed document, as bytes — the shape plugin.Spec hands
// back from the host's embed.
func subset(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := json.Marshal(openapi.Document{
		OpenAPI: "3.1.0",
		Paths:   map[string]openapi.PathItem{path: {"get": {OperationID: "get" + strings.ReplaceAll(path, "/", "_")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestMountFleetServesTheCompositionIncludingItself: the host's answer is every
// app's surface PLUS the door it came through. The door is projected from a real
// mount rather than written down, so it cannot name an address the fleet does not
// answer on — and a document that omitted its own endpoint would be a spec no
// generated client could refresh itself from.
func TestMountFleetServesTheCompositionIncludingItself(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	openapi.MountFleet(app, func() ([]openapi.Part, error) {
		return openapi.Subsets([]string{"ads", "crm"}, func(a string) []byte { return subset(t, "/v1/"+a) })
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, openapi.Path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d, want 200", openapi.Path, resp.StatusCode)
	}
	var doc openapi.Document
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/v1/ads", "/v1/crm", openapi.Path} {
		if _, ok := doc.Paths[want]; !ok {
			t.Errorf("the served document omits %s — it publishes %d paths", want, len(doc.Paths))
		}
	}
	if doc.Info.Title != "Hanzo Cloud API" {
		t.Errorf("Info.Title = %q, want the one fleet identity every projection carries", doc.Info.Title)
	}
}

// TestSubsetsRefuseAnAppThatPublishedNothing: an app whose subset is missing is a
// LOUD failure, by name. Skipping it would serve a document with that product's
// whole surface absent — 200 OK, well-formed, and wrong in the one way no
// consumer can detect.
func TestSubsetsRefuseAnAppThatPublishedNothing(t *testing.T) {
	_, err := openapi.Subsets([]string{"ads", "ghost"}, func(a string) []byte {
		if a == "ghost" {
			return nil // never described
		}
		return subset(t, "/v1/"+a)
	})
	if err == nil {
		t.Fatal("Subsets accepted an app with no subset — its whole surface would be silently absent from the published spec")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("refusal = %q, want it to name the app so the fix is obvious", err)
	}
}

// TestSubsetsRefusesAnAppWhoseOwnIDsCollide: A SUBSET IS GENERATED OR IT IS
// WRONG, and the refusal says which app.
//
// [openapi.From] will not emit a document whose operationIds collide, so a
// GENERATED subset cannot arrive broken — which made "the parts are injective" an
// assumption rather than a check. It was wrong about a committed file, because a
// file can be edited: /v1/billing/methods was written into commerce's subset by
// copying the /v1/billing/portal/methods block, operationId and prose together,
// and two paths then claimed get_billing_portal_methods. Nothing could be
// regenerated until it was fixed — the weave is the sole writer of openapi.yaml,
// so the CLI, the SDKs, MCP and the docs were all frozen behind it.
//
// Weave DID refuse it. But a collision inside ONE part reaches Weave with no app
// attached, so the report could name the two addresses and nothing else, and
// which of 123 subsets shipped them was a search. Subsets is holding the name
// when it decodes the bytes, so this is where the question gets answered.
func TestSubsetsRefusesAnAppWhoseOwnIDsCollide(t *testing.T) {
	// The real shape of the defect: a second path carrying the first one's id.
	collide, err := json.Marshal(openapi.Document{OpenAPI: "3.1.0", Paths: map[string]openapi.PathItem{
		"/v1/billing/methods":        {"get": {OperationID: "get_billing_portal_methods"}},
		"/v1/billing/portal/methods": {"get": {OperationID: "get_billing_portal_methods"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = openapi.Subsets([]string{"commerce"}, func(string) []byte { return collide })
	if err == nil {
		t.Fatal("Subsets accepted one operationId at two addresses — every generator downstream " +
			"mis-consumes that document, and the weave that catches it later cannot say whose it is")
	}
	if !strings.Contains(err.Error(), "commerce") {
		t.Errorf("refusal = %q, want it to name the app — naming only the paths is what cost a "+
			"search of every subset in plugin/", err)
	}

	// An injective subset still decodes. The check must cost a green run nothing.
	if _, err := openapi.Subsets([]string{"ads"}, func(a string) []byte { return subset(t, "/v1/"+a) }); err != nil {
		t.Errorf("injective subset: %v", err)
	}
}

// TestMountFleetReportsAFailedCompositionRatherThanAnEmptyDocument: the endpoint
// must never answer 200 with less API than the fleet has. That is the production
// defect this whole path exists to close, so the failure mode is a 5xx a probe
// can see — not a smaller document that looks fine.
func TestMountFleetReportsAFailedCompositionRatherThanAnEmptyDocument(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	openapi.MountFleet(app, func() ([]openapi.Part, error) {
		return openapi.Subsets([]string{"ghost"}, func(string) []byte { return nil })
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, openapi.Path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatalf("GET %s = 200 on a composition that could not be made — a spec that is quietly "+
			"smaller than the API is exactly the failure this endpoint had in production", openapi.Path)
	}
}
