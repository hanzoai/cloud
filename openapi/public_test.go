package openapi

// The audience, tested as the rule it is: a customer operation is public the day
// it answers, and the operator's surface, the relays and the legacy
// spellings are not. Every case here is an address nobody has declared anything
// about, because that is the case the rule exists for.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest/mcp"
	"github.com/zap-proto/zip"
)

func TestACustomerOperationIsPublicByItsAddress(t *testing.T) {
	app := newApp()
	app.Get("/v1/widgets", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	app.Get("/v1/admin/widgets", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	app.Get("/.well-known/widgets", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	app.Get("/_/widgets", func(c *zip.Ctx) error { return c.JSON(200, "ok") })

	doc, err := Spec(app, Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		"/v1/widgets":          true,
		"/v1/admin/widgets":    false,
		"/.well-known/widgets": false,
		"/_/widgets":           false,
	} {
		op := doc.Paths[path]["get"]
		if op == nil {
			t.Fatalf("the internal document dropped %s, which the router serves", path)
		}
		if op.Public != want {
			t.Errorf("%s public = %v, want %v", path, op.Public, want)
		}
	}

	pub, err := Publish(doc)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Paths["/v1/widgets"]["get"] == nil {
		t.Error("the public document dropped the customer operation")
	}
	for _, p := range []string{"/v1/admin/widgets", "/.well-known/widgets", "/_/widgets"} {
		if _, leaked := pub.Paths[p]; leaked {
			t.Errorf("the public document carries %s", p)
		}
	}
}

// A relay publishes whatever grows behind it and names nothing a client can
// call; it stays internal whatever product it sits under.
func TestARelayEndpointIsNotPublic(t *testing.T) {
	op := &Operation{OperationID: "get_v1_tasks_wildcard1"}
	if audience("/v1/tasks/{wildcard1}", op) {
		t.Fatal("a {wildcardN} address came back public")
	}
}

// A legacy spelling is served so a pinned caller keeps working and is, by its
// own declaration, not the contract.
func TestALegacySpellingIsNotPublic(t *testing.T) {
	op := &Operation{OperationID: "post_v1_chat", Tags: []string{"chat", Compat}}
	if audience("/v1/chat", op) {
		t.Fatal("a compat-tagged operation came back public")
	}
}

// An EMPTY projection is refused: a document with no customer surface is a
// document composed wrong, and the artifact of that mistake would be a valid
// OpenAPI file that generates an SDK with no calls in it.
func TestPublishRefusesAnEmptyProjection(t *testing.T) {
	d := &Document{OpenAPI: "3.1.0", Paths: map[string]PathItem{
		"/v1/admin/x": {"get": &Operation{OperationID: "get_admin_x"}},
	}}
	if _, err := Publish(d); err == nil {
		t.Fatal("a document with no public operation produced a public document")
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
				OperationID: "post_open", Public: true, Tags: []string{"open"},
				RequestBody: map[string]any{"content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"$ref": refPrefix + "Ask"}}}},
			}},
			"/v1/admin/shut": {"post": &Operation{
				OperationID: "post_shut", Tags: []string{"admin"},
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
		"/v1/open": {"post": &Operation{OperationID: "post_open", Public: true,
			RequestBody: map[string]any{"schema": map[string]any{"$ref": refPrefix + "Nowhere"}}}},
	}}
	if _, err := Publish(d); err == nil || !strings.Contains(err.Error(), "Nowhere") {
		t.Fatalf("err = %v, want a refusal naming the undefined component", err)
	}
}

// The agent MCP endpoint is the fleet's: core projects it with its prose and
// bodies, MCP knows it, it is public, and an app describing itself never carries it.
func TestTheAgentEndpointIsTheFleetsAndNoApps(t *testing.T) {
	if !Host(mcp.Path) {
		t.Fatal("the agent MCP endpoint is not one of the host's own addresses")
	}
	app := newApp()
	app.Get("/v1/widgets", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	own, err := Spec(app, Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, carried := own.Paths[mcp.Path]; carried {
		t.Fatal("an app's own document carries the agent MCP endpoint")
	}
	c, err := core()
	if err != nil {
		t.Fatal(err)
	}
	op := c.Doc.Paths[mcp.Path]["post"]
	if op == nil {
		t.Fatal("core did not project the agent MCP endpoint")
	}
	if op.Summary == "" || op.RequestBody == nil || !op.Public {
		t.Fatalf("the MCP endpoint is projected bare: summary=%q body=%v public=%v", op.Summary, op.RequestBody != nil, op.Public)
	}
}
