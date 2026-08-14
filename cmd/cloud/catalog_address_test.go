package main

// The product catalogue's addresses, read back against the document this host
// publishes.
//
// This file is about one production defect, and it is the same SHAPE as the one
// openapi_test.go pins next door. There, a spec door answered with a document
// that was not the fleet's; here, a catalogue answers with routes that are not
// the fleet's. Both were wrong for months while every gate stayed green, because
// in both cases the claim and the thing claimed about lived in different repos
// and nothing ever put them side by side.
//
// GET /v1/commerce/catalog?brand=hanzo advertised 84 products. THIRTY-ONE named
// an apiPath with nothing under it anywhere in the 1,800 paths this host
// publishes — /v1/providers, /v1/containers, /v1/nodes, /v1/vpc, /v1/cdn,
// /v1/wallet, /v1/indexer and 24 more, most of them literally "/v1/" + the slug.
// Every one was published "enabled", so a menu built from the catalogue rendered
// dead links and reported them as working, and cloud.hanzo.ai's own derived
// count read 53 of 84 without anything being able to say which 31 were wrong.
//
// # Why only this list could drift
//
// Nothing else in the estate claims a route in a second place. The published
// document is DERIVED from the router and refused unless every operation carries
// prose (openapi.Complete), so a served route cannot be missing from it and a
// described route that nothing serves cannot appear in it. The SDKs, the CLI and
// the MCP tool list are all generated from that document. The catalogue was the
// last hand-copied copy — and a copy nobody diffs is a copy that is wrong.
//
// So the fix is not "correct the 31". It is this test, which is why it reads the
// SNAPSHOT rather than production: a value corrected in a database is corrected
// once, and a value corrected in a snapshot a build refuses to disagree with is
// corrected for good. commerce's catalogentry.Correct carries it the last hop to
// the store on every boot.

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/commerce/models/catalogentry"
	"sigs.k8s.io/yaml"
)

// TestCatalogAddressesAreServed is the law: EVERY PRODUCT THIS FLEET SELLS AS
// CALLABLE IS CALLABLE, AND EVERY PRODUCT THAT IS NOT SAYS SO.
//
// Both halves are refused here, because both hand a reader the same broken
// promise. A service whose apiPath resolves nowhere is a dead link sold as
// working; a service that names no path at all is the same silence with better
// manners, and would let the next 31 arrive one row at a time.
//
// A path resolves EXACTLY (/v1/wallets is a published path) or as a PREFIX
// (/v1/iam publishes 166 paths beneath it and none at the bare address). Both are
// real answers to "where do I call this product", and the surfaces that count
// products already count both — so counting them differently here would grade the
// catalogue against a rule nothing else uses.
func TestCatalogAddressesAreServed(t *testing.T) {
	published := publishedPaths(t)
	rows, err := catalogentry.HanzoSeedRows()
	if err != nil {
		t.Fatalf("read the product snapshot: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("the product snapshot is empty — a catalogue that claims nothing cannot be checked")
	}

	var dead, silent []string
	callable := 0
	for _, r := range rows {
		switch catalogentry.KindOf(&catalogentry.CatalogEntry{Role: r.Kind}) {
		case catalogentry.KindClient, catalogentry.KindPending:
			// Says plainly that it has no API. Judging it by one is the category
			// error that made seven working clients read as broken; seed_test.go
			// in commerce holds each to a named table so the claim costs an edit.
			if r.ApiPath != "" {
				dead = append(dead, r.Slug+" is "+r.Kind+" yet claims "+r.ApiPath)
			}
			continue
		}
		if r.ApiPath == "" {
			silent = append(silent, r.Slug)
			continue
		}
		if !resolves(published, r.ApiPath) {
			dead = append(dead, r.Slug+" claims "+r.ApiPath)
			continue
		}
		callable++
	}

	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("%d product(s) advertise an address this fleet does not serve:\n  %s\n\n"+
			"The path is not a name to be guessed from the slug — it is where the router "+
			"actually delivers, and manifest/apps.go is the one place that decides. Look for "+
			"where the product WENT before concluding it is gone: /v1/vpc became /v1/vpcs, "+
			"score-configs became /v1/evals/rubrics, annotation-queues became /v1/o11y/reviews, "+
			"and /v1/edge was taken away on purpose. When a product genuinely has no API here, "+
			"say so with kind %q or %q instead of pointing at something adjacent — a catalogue "+
			"that dresses one product as another is worse than one that admits a gap. Fix it in "+
			"hanzoai/commerce models/catalogentry/seed/hanzo-catalog.json",
			len(dead), strings.Join(dead, "\n  "), catalogentry.KindClient, catalogentry.KindPending)
	}
	sort.Strings(silent)
	if len(silent) > 0 {
		t.Errorf("%d product(s) are sold as services and name no address at all:\n  %s\n\n"+
			"An empty apiPath on a service is not neutral — the console renders no way to call "+
			"the product and nothing reports why. Either give it the route the fleet serves, or "+
			"mark it %q (a real product this host does not front) or %q (it consumes the API and "+
			"serves none).",
			len(silent), strings.Join(silent, "\n  "), catalogentry.KindPending, catalogentry.KindClient)
	}
	t.Logf("%d of %d catalogue products are callable, and every one resolves in %s",
		callable, len(rows), golden)
}

// resolves reports whether the fleet publishes anything at or beneath addr.
//
// An address must name a product, so it has to reach PAST /v1. Without that,
// "/v1" and "/" both resolve — every published path lies beneath them — and a row
// addressed either one passes while telling a customer to call api.hanzo.ai/v1
// and hope. A prefix is a real answer only when it is a prefix of something.
func resolves(published map[string]json.RawMessage, addr string) bool {
	if !strings.HasPrefix(addr, "/v1/") || len(addr) <= len("/v1/") {
		return false
	}
	if _, ok := published[addr]; ok {
		return true
	}
	under := strings.TrimSuffix(addr, "/") + "/"
	for p := range published {
		if strings.HasPrefix(p, under) {
			return true
		}
	}
	return false
}

// publishedPaths is the path set of the document this host serves at
// GET /v1/openapi.json. openapi_test.go pins that the door answers with this
// committed artifact byte for byte, and that the artifact is the router's own
// projection — so reading the file here is reading what a customer reads.
func publishedPaths(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	doc, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v — run `make describe`", golden, err)
	}
	var d struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := yaml.Unmarshal(doc, &d); err != nil {
		t.Fatalf("%s is not an OpenAPI document: %v", golden, err)
	}
	if len(d.Paths) == 0 {
		t.Fatalf("%s publishes no paths — every catalogue address would fail against an empty document", golden)
	}
	return d.Paths
}
