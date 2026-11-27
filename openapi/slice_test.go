package openapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

const whole = `{
 "openapi":"3.1.0","info":{"title":"t","version":"v1"},
 "security":[{"bearer":[]}],
 "tags":[{"name":"kms"},{"name":"billing"}],
 "paths":{
  "/v1/kms/secrets":{
    "get":{"operationId":"kmsList","tags":["kms"],"responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Secret"}}}}}},
    "post":{"operationId":"kmsCreate","tags":["kms"],"requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Loop"}}}}}},
  "/v1/billing/invoice":{
    "get":{"operationId":"billingInvoice","tags":["billing"],"responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Invoice"}}}}}}}
 },
 "components":{
  "schemas":{
   "Secret":{"type":"object","properties":{"meta":{"$ref":"#/components/schemas/Meta"}}},
   "Meta":{"type":"object"},
   "Loop":{"type":"object","properties":{"again":{"$ref":"#/components/schemas/Again"}}},
   "Again":{"type":"object","properties":{"loop":{"$ref":"#/components/schemas/Loop"}}},
   "Invoice":{"type":"object"},
   "Orphan":{"type":"object"}
  },
  "securitySchemes":{"bearer":{"type":"http","scheme":"bearer"}}
 }
}`

type slicedDoc struct {
	Info  map[string]any `json:"info"`
	Tags  []struct{ Name string }
	Paths map[string]map[string]struct {
		ID string `json:"operationId"`
	} `json:"paths"`
	Components struct {
		Schemas         map[string]any `json:"schemas"`
		SecuritySchemes map[string]any `json:"securitySchemes"`
	} `json:"components"`
}

func cut(t *testing.T, capability, operation string) (slicedDoc, bool) {
	t.Helper()
	return cutUnder(t, capability, operation, "")
}

func cutUnder(t *testing.T, capability, operation, under string) (slicedDoc, bool) {
	t.Helper()
	s, err := newSlicer([]byte(whole))
	if err != nil {
		t.Fatalf("newSlicer: %v", err)
	}
	body, ok, err := s.slice(capability, operation, under)
	if err != nil {
		t.Fatalf("slice: %v", err)
	}
	if !ok {
		return slicedDoc{}, false
	}
	var d slicedDoc
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("a slice must be a valid document: %v\n%s", err, body)
	}
	return d, true
}

// A capability slice keeps its own paths and tag, and exactly the schemas those
// paths reach — transitively — and nothing of the other capability or the orphan.
func TestSliceByCapabilityKeepsOnlyWhatItReaches(t *testing.T) {
	d, ok := cut(t, "kms", "")
	if !ok {
		t.Fatal("kms matches two operations")
	}
	if len(d.Paths) != 1 || len(d.Paths["/v1/kms/secrets"]) != 2 {
		t.Errorf("paths = %v, want only /v1/kms/secrets with get and post", d.Paths)
	}
	if len(d.Tags) != 1 || d.Tags[0].Name != "kms" {
		t.Errorf("tags = %v, want only kms", d.Tags)
	}
	for _, n := range []string{"Secret", "Meta", "Loop", "Again"} {
		if _, in := d.Components.Schemas[n]; !in {
			t.Errorf("schema %s missing: a $ref the slice kept would dangle", n)
		}
	}
	for _, n := range []string{"Invoice", "Orphan"} {
		if _, in := d.Components.Schemas[n]; in {
			t.Errorf("schema %s copied though no kept path reaches it", n)
		}
	}
	if d.Components.SecuritySchemes["bearer"] == nil || d.Info["title"] != "t" {
		t.Errorf("a slice is a document on its own: info and securitySchemes ride along; got %+v", d)
	}
}

// One operation narrows further, and a $ref cycle ends.
func TestSliceByOperation(t *testing.T) {
	d, ok := cut(t, "", "kmsList")
	if !ok {
		t.Fatal("kmsList exists")
	}
	if len(d.Paths["/v1/kms/secrets"]) != 1 || d.Paths["/v1/kms/secrets"]["get"].ID != "kmsList" {
		t.Errorf("paths = %v, want only kmsList", d.Paths)
	}
	if _, in := d.Components.Schemas["Loop"]; in {
		t.Errorf("Loop belongs to kmsCreate, not kmsList")
	}
	if _, in := d.Components.Schemas["Meta"]; !in {
		t.Errorf("Meta is reached through Secret and must come along")
	}
	if _, ok := cut(t, "kms", "billingInvoice"); ok {
		t.Errorf("an operation outside the named capability must not match")
	}
}

// A path prefix keeps the operations under it, combines with the other filters,
// and matches nothing it is not a prefix of.
func TestSliceByPathPrefix(t *testing.T) {
	d, ok := cutUnder(t, "", "", "/v1/kms")
	if !ok {
		t.Fatal("/v1/kms is a prefix of two operations")
	}
	if len(d.Paths) != 1 || len(d.Paths["/v1/kms/secrets"]) != 2 {
		t.Errorf("paths = %v, want only /v1/kms/secrets with get and post", d.Paths)
	}
	if _, in := d.Components.Schemas["Invoice"]; in {
		t.Errorf("Invoice belongs to billing, outside the prefix")
	}
	if d, ok := cutUnder(t, "", "kmsCreate", "/v1/kms"); !ok || len(d.Paths["/v1/kms/secrets"]) != 1 {
		t.Errorf("prefix and operation combine: got %v", d.Paths)
	}
	if _, ok := cutUnder(t, "billing", "", "/v1/kms"); ok {
		t.Errorf("a capability outside the prefix must not match")
	}
	if _, ok := cutUnder(t, "", "", "/v2"); ok {
		t.Errorf("a prefix of nothing must not answer an empty document")
	}
}

func TestSliceOfNothingIsRefused(t *testing.T) {
	if _, ok := cut(t, "nosuch", ""); ok {
		t.Errorf("an unknown capability must not answer an empty document")
	}
	if _, ok := cut(t, "", "nosuch"); ok {
		t.Errorf("an unknown operation must not answer an empty document")
	}
}

// Through the real endpoint: the whole document is unchanged without a query, a
// query narrows it, an unknown name is 404, a malformed one is 400, and the
// well-known alias narrows the same way.
func TestEndpointNarrowsByQuery(t *testing.T) {
	app := newApp()
	app.Get("/v1/kms/orgs/:org/secrets", func(ctx context.Context, in *secretIn) (*secretOut, error) { return &secretOut{}, nil })
	app.Raw(http.MethodGet, "/v1/billing/usage", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	Use(app, spec.Info{Title: "Hanzo Cloud", Version: "v1"})

	get := func(path string) (int, []byte) {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	paths := func(b []byte) map[string]json.RawMessage {
		var d struct {
			Paths map[string]json.RawMessage `json:"paths"`
		}
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("not a document: %v", err)
		}
		return d.Paths
	}

	code, full := get(Path)
	if code != 200 || len(paths(full)) < 3 {
		t.Fatalf("whole document: %d with %d paths", code, len(paths(full)))
	}
	code, kms := get(Path + "?capability=kms")
	if code != 200 {
		t.Fatalf("capability=kms: %d %s", code, kms)
	}
	got := paths(kms)
	if _, in := got["/v1/kms/orgs/{org}/secrets"]; !in || len(got) != 1 {
		t.Errorf("capability=kms paths = %v, want only the kms operation", got)
	}
	if len(kms) >= len(full) {
		t.Errorf("a slice (%d bytes) must be smaller than the whole (%d)", len(kms), len(full))
	}
	if code, _ := get(WellKnown + "?capability=kms"); code != 200 {
		t.Errorf("the well-known alias must narrow too: %d", code)
	}
	if code, _ := get(Path + "?capability=nosuch"); code != 404 {
		t.Errorf("unknown capability = %d, want 404", code)
	}
	if code, _ := get(Path + "?capability=" + "a%2Fb"); code != 400 {
		t.Errorf("a capability that is not a name = %d, want 400", code)
	}

	// ?tag= is the same filter as ?capability= (hanzo-inc/cloud#431: it used to
	// be ignored, which answered the whole document).
	code, byTag := get(Path + "?tag=kms")
	if code != 200 || string(byTag) != string(kms) {
		t.Errorf("tag=kms = %d, want the capability=kms slice byte for byte", code)
	}
	if code, _ := get(Path + "?tag=kms&capability=kms"); code != 200 {
		t.Errorf("the same name twice = %d, want 200", code)
	}
	if code, _ := get(Path + "?tag=kms&capability=billing"); code != 400 {
		t.Errorf("tag and capability disagreeing = %d, want 400", code)
	}
	code, under := get(Path + "?path=/v1/billing")
	if code != 200 {
		t.Fatalf("path=/v1/billing: %d %s", code, under)
	}
	if got := paths(under); len(got) != 1 {
		t.Errorf("path=/v1/billing paths = %v, want only the billing operation", got)
	} else if _, in := got["/v1/billing/usage"]; !in {
		t.Errorf("path=/v1/billing paths = %v, want /v1/billing/usage", got)
	}
	if code, _ := get(Path + "?path=/v9"); code != 404 {
		t.Errorf("a prefix of nothing = %d, want 404", code)
	}
	if code, _ := get(Path + "?path=v1"); code != 400 {
		t.Errorf("a relative prefix = %d, want 400", code)
	}
	// The root spelling redirects to the canonical address, query and all.
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, RootSpec+"?tag=kms", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET %s: %v", RootSpec, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != Path+"?tag=kms" {
		t.Errorf("GET %s?tag=kms = %d Location %q, want 301 to %s?tag=kms", RootSpec, resp.StatusCode, resp.Header.Get("Location"), Path)
	}
	if app.Declares(http.MethodGet, RootSpec) {
		t.Errorf("the redirect is not an operation and must stay out of the contract")
	}
}

// A capability's index names its own slice of the spec as the thing that describes
// it, so a reader walking down from /v1 never has to learn the query.
func TestACapabilityIndexIsDescribedByItsOwnSlice(t *testing.T) {
	_, per := Discover(fleet(t))
	if got, want := per["kms"].Links["describedby"].Href, Path+"?capability=kms"; got != want {
		t.Errorf("kms describedby = %q, want %q", got, want)
	}
}
