package openapi

// The whitelist, tested as the property it exists for: SILENCE MEANS ABSENT.
//
// Every other gate in this package asks "does the document match the routes".
// These ask the opposite question — "what does the document REFUSE to publish"
// — and the answers have to hold for operations nobody has written yet, because
// the leak this prevents is the one that happens when somebody forgets.

import (
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// declaring runs fn against a CLEAN whitelist and puts the process's own back
// afterwards. The registry is package-global (it is written from apps' inits, at
// program start, exactly like Register and Describe), so a test that added to it
// permanently would change what every later test in this package publishes.
func declaring(t *testing.T, fn func()) {
	t.Helper()
	publicMu.Lock()
	saved := publicOps
	publicOps = map[opKey]bool{}
	publicMu.Unlock()
	t.Cleanup(func() {
		publicMu.Lock()
		publicOps = saved
		publicMu.Unlock()
	})
	fn()
}

// THE PROPERTY. An operation added to a live router with nothing said about it
// reaches the internal document and does NOT reach the public one.
//
// This is the whole reason the split is a whitelist. The same test written
// against a denylist passes only while somebody keeps the list current, and the
// day it stops being current is the day a new product ships publicly by
// accident — which nobody notices, because the failure is a document containing
// something rather than a document missing something.
func TestANewOperationLandsInternalAndNotPublic(t *testing.T) {
	declaring(t, func() {
		Public("/v1/whitelist/said", "GET")

		app := newApp()
		app.Get("/v1/whitelist/said", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
		app.Get("/v1/whitelist/silent", func(c *zip.Ctx) error { return c.JSON(200, "ok") })

		doc, err := Spec(app, Info{Title: "t", Version: "v1"})
		if err != nil {
			t.Fatal(err)
		}
		if doc.Paths["/v1/whitelist/silent"]["get"] == nil {
			t.Fatal("the internal document dropped an operation the router serves")
		}
		if doc.Paths["/v1/whitelist/silent"]["get"].Public {
			t.Error("an operation that declared nothing came back marked public")
		}
		if !doc.Paths["/v1/whitelist/said"]["get"].Public {
			t.Fatal("a declared operation came back unmarked")
		}

		pub, err := Publish(doc)
		if err != nil {
			t.Fatal(err)
		}
		if _, leaked := pub.Paths["/v1/whitelist/silent"]; leaked {
			t.Error("the public document carries an operation nothing declared — default-deny is not holding")
		}
		if pub.Paths["/v1/whitelist/said"]["get"] == nil {
			t.Error("the public document dropped the operation that WAS declared")
		}
	})
}

// A declaration is normalized through the SAME translate the document's own
// addresses are, so the two spellings of one address are one key. The door's
// operations arrive templated (`/v1/videos/{id}`) and a router's arrive as fiber
// patterns (`/v1/videos/:id`); a whitelist that distinguished them would silently
// fail to publish whichever one the author did not guess.
func TestADeclarationIsTheSameKeyInEitherSpelling(t *testing.T) {
	for _, declared := range []string{"/v1/whitelist/thing/:id", "/v1/whitelist/thing/{id}"} {
		declaring(t, func() {
			Public(declared, "GET")
			app := newApp()
			app.Get("/v1/whitelist/thing/:id", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
			doc, err := Spec(app, Info{Title: "t", Version: "v1"})
			if err != nil {
				t.Fatal(err)
			}
			if !doc.Paths["/v1/whitelist/thing/{id}"]["get"].Public {
				t.Errorf("Public(%q) did not mark the operation the document publishes at "+
					"/v1/whitelist/thing/{id}", declared)
			}
		})
	}
}

// A declaration renders only on an operation that exists — the same law Register
// and Describe obey, and the reason it cannot be an error: the registry is
// process-wide and a describe run mounts ONE app, so every other app's
// declarations are legitimately unmatched in it.
func TestADeclarationWithNoOperationRendersNothing(t *testing.T) {
	declaring(t, func() {
		Public("/v1/whitelist/absent", "GET")
		app := newApp()
		app.Get("/v1/whitelist/present", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
		doc, err := Spec(app, Info{Title: "t", Version: "v1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, invented := doc.Paths["/v1/whitelist/absent"]; invented {
			t.Fatal("a declaration added an address to the document — the registry must never be able to")
		}
		if _, err := Publish(doc); err == nil {
			t.Error("a document whose only declaration matched nothing published something")
		}
	})
}

// The refusals at the declaration site. Each one is a way of writing a PREFIX
// LIST by accident, which is the shape this whole file exists to avoid.
func TestPublicRefusesWhatIsNotOneOperation(t *testing.T) {
	for _, tc := range []struct{ name, path, method string }{
		{"a wildcard", "/v1/ai/*", "GET"},
		{"a plus", "/v1/ai/+", "GET"},
		{"a method this generator never publishes", "/v1/ai/x", "HEAD"},
		{"a method that is not a method", "/v1/ai/x", "SUBSCRIBE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declaring(t, func() {
				defer func() {
					if recover() == nil {
						t.Errorf("Public(%q, %q) was accepted", tc.path, tc.method)
					}
				}()
				Public(tc.path, tc.method)
			})
		})
	}
}

func TestPublicRefusesADuplicateDeclaration(t *testing.T) {
	declaring(t, func() {
		Public("/v1/whitelist/twice", "GET")
		defer func() {
			if recover() == nil {
				t.Error("two declarations for one operation were accepted")
			}
		}()
		Public("/v1/whitelist/twice", "get") // same operation, other case
	})
}

// An EMPTY projection is refused, because it is indistinguishable from a
// projection run over a document composed without the declarations — and the
// artifact of that mistake is a well-formed OpenAPI file that generates an SDK
// with no calls in it.
func TestPublishRefusesAnEmptyProjection(t *testing.T) {
	d := &Document{OpenAPI: "3.1.0", Paths: map[string]PathItem{
		"/v1/x": {"get": &Operation{OperationID: "get_v1_x"}},
	}}
	if _, err := Publish(d); err == nil {
		t.Fatal("a document with nothing declared public produced a public document")
	}
}

// Components are pruned to the CLOSURE of what public operations reference: the
// types a public call binds, plus the types those name, and nothing else. A
// public document carrying an internal product's schemas would leak the shape of
// operations it does not publish into every generated client.
func TestPublishPrunesComponentsToWhatPublicOperationsReach(t *testing.T) {
	d := &Document{
		OpenAPI: "3.1.0",
		Paths: map[string]PathItem{
			"/v1/open": {"post": &Operation{
				OperationID: "post_v1_open", Public: true, Tags: []string{"open"},
				RequestBody: map[string]any{"content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"$ref": refPrefix + "Ask"}}}},
			}},
			"/v1/shut": {"post": &Operation{
				OperationID: "post_v1_shut", Tags: []string{"shut"},
				RequestBody: map[string]any{"content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"$ref": refPrefix + "Secret"}}}},
			}},
		},
		Components: &Components{Schemas: map[string]any{
			// Ask names Nested, so both survive; Orphan and Secret do not.
			"Ask":    map[string]any{"properties": map[string]any{"n": map[string]any{"$ref": refPrefix + "Nested"}}},
			"Nested": map[string]any{"type": "string"},
			"Secret": map[string]any{"type": "string"},
			"Orphan": map[string]any{"type": "string"},
		}},
	}
	pub, err := Publish(d)
	if err != nil {
		t.Fatal(err)
	}
	got := pub.Components.Schemas
	for _, name := range []string{"Ask", "Nested"} {
		if _, kept := got[name]; !kept {
			t.Errorf("component %q is reachable from a public operation and was pruned", name)
		}
	}
	for _, name := range []string{"Secret", "Orphan"} {
		if _, kept := got[name]; kept {
			t.Errorf("component %q is not reachable from any public operation and was published", name)
		}
	}
	if len(pub.Tags) != 1 || pub.Tags[0].Name != "open" {
		t.Errorf("tags = %v, want just the products the surviving operations belong to", pub.Tags)
	}
}

// A $ref the document does not define is refused rather than dropped: a public
// document with a dangling reference breaks in whichever generator meets it
// first, which is a worse place to find out than here.
func TestPublishRefusesADanglingReference(t *testing.T) {
	d := &Document{OpenAPI: "3.1.0", Paths: map[string]PathItem{
		"/v1/open": {"post": &Operation{OperationID: "post_v1_open", Public: true,
			RequestBody: map[string]any{"schema": map[string]any{"$ref": refPrefix + "Nowhere"}}}},
	}}
	if _, err := Publish(d); err == nil || !strings.Contains(err.Error(), "Nowhere") {
		t.Fatalf("err = %v, want a refusal naming the undefined component", err)
	}
}

// PUBLIC and COMPAT are contradictory claims about one address, and they can meet:
// [Compat] is declared by the code that serves a LEGACY SPELLING, this file's
// declarations by the code that publishes the contract, and the two live in
// different repositories. Publishing both would put two SDK methods, two CLI verbs
// and two MCP tools on one call — the surface Compat exists to keep out — so the
// contradiction is named rather than resolved.
func TestPublishRefusesAnOperationThatIsBothPublicAndCompat(t *testing.T) {
	d := &Document{OpenAPI: "3.1.0", Paths: map[string]PathItem{
		"/v1/chat": {"post": &Operation{
			OperationID: "post_v1_chat", Public: true, Tags: []string{"chat", Compat}}},
	}}
	_, err := Publish(d)
	if err == nil {
		t.Fatal("an operation declared public AND tagged compat was published")
	}
	for _, want := range []string{"/v1/chat", Compat} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}
