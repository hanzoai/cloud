package openapi_test

import (
	"strings"
	"testing"

	graphqlgo "github.com/graph-gophers/graphql-go"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
)

// body is a requestBody as the document holds one: open-typed, keyed by media.
func body(schema map[string]any) map[string]any {
	return map[string]any{"content": map[string]any{
		"application/json": map[string]any{"schema": schema},
	}}
}

// ok is a 200 response carrying one schema.
func ok(schema map[string]any) map[string]any {
	return map[string]any{"200": map[string]any{"content": map[string]any{
		"application/json": map[string]any{"schema": schema},
	}}}
}

func ref(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

// doc is a small composed document: one component, and the operations given.
func doc(comps map[string]any, paths map[string]openapi.PathItem) *openapi.Document {
	return &openapi.Document{
		OpenAPI:    "3.1.0",
		Paths:      paths,
		Components: &openapi.Components{Schemas: comps},
	}
}

// TestOnlyDispatchableOperationsBecomeFields is the rule that keeps this schema
// honest. A document carries every ROUTE; only the typed subset is answerable, and
// publishing a name the fleet replies `unknown` to is worse than publishing less.
func TestOnlyDispatchableOperationsBecomeFields(t *testing.T) {
	sdl := openapi.GraphQL(doc(nil, map[string]openapi.PathItem{
		"/v1/graph": {"get": {OperationID: "graphRead", Tool: true, Responses: ok(map[string]any{"type": "object"})}},
		"/v1/thing": {"get": {OperationID: "thingRead", Responses: ok(map[string]any{"type": "object"})}},
	}))

	if !strings.Contains(sdl, "graphRead") {
		t.Errorf("a dispatchable operation is missing from the schema:\n%s", sdl)
	}
	if strings.Contains(sdl, "thingRead") {
		t.Errorf("a described-but-undispatchable route was published as a field:\n%s", sdl)
	}
}

// TestAGetIsAQueryAndEverythingElseIsAMutation pins the one split GraphQL makes,
// on the same signal every other projection here reads.
func TestAGetIsAQueryAndEverythingElseIsAMutation(t *testing.T) {
	sdl := openapi.GraphQL(doc(nil, map[string]openapi.PathItem{
		"/v1/graph": {
			"get":  {OperationID: "graphRead", Tool: true, Responses: ok(map[string]any{"type": "object"})},
			"post": {OperationID: "graphAssert", Tool: true, Responses: ok(map[string]any{"type": "object"})},
		},
	}))

	q := section(sdl, "type Query {")
	m := section(sdl, "type Mutation {")
	if !strings.Contains(q, "graphRead") || strings.Contains(q, "graphAssert") {
		t.Errorf("Query holds the wrong fields:\n%s", q)
	}
	if !strings.Contains(m, "graphAssert") || strings.Contains(m, "graphRead") {
		t.Errorf("Mutation holds the wrong fields:\n%s", m)
	}
}

// TestParametersBecomeTypedArguments proves a caller can see what an operation
// takes without reading the REST document beside this one.
func TestParametersBecomeTypedArguments(t *testing.T) {
	sdl := openapi.GraphQL(doc(nil, map[string]openapi.PathItem{
		"/v1/graph/search": {"get": {
			OperationID: "graphSearch", Tool: true,
			Parameters: []openapi.Parameter{
				{Name: "q", Required: true, Schema: map[string]any{"type": "string"}},
				{Name: "limit", Schema: map[string]any{"type": "integer"}},
			},
			Responses: ok(map[string]any{"type": "object"}),
		}},
	}))

	for _, want := range []string{"q: String!", "limit: Int"} {
		if !strings.Contains(sdl, want) {
			t.Errorf("argument %q missing:\n%s", want, sdl)
		}
	}
}

// TestABodyArrivesAsOneArgument pins the choice not to flatten. A body property
// and a query parameter sharing a namespace is a collision the caller cannot see.
func TestABodyArrivesAsOneArgument(t *testing.T) {
	sdl := openapi.GraphQL(doc(
		map[string]any{"Claim": map[string]any{
			"type":       "object",
			"properties": map[string]any{"entity": map[string]any{"type": "string"}},
		}},
		map[string]openapi.PathItem{
			"/v1/graph": {"post": {
				OperationID: "graphAssert", Tool: true,
				RequestBody: body(ref("Claim")),
				Responses:   ok(ref("Claim")),
			}},
		}))

	if !strings.Contains(sdl, "body: ClaimInput") {
		t.Errorf("a request body must arrive as one typed argument:\n%s", sdl)
	}
	// GraphQL will not accept an object type as an argument, so the input twin is
	// minted — and only for what a body can reach.
	if !strings.Contains(sdl, "input ClaimInput {") {
		t.Errorf("the input twin was not minted:\n%s", sdl)
	}
	if !strings.Contains(sdl, "type Claim {") {
		t.Errorf("the output type is missing:\n%s", sdl)
	}
}

// TestAnInputTwinIsMintedOnlyWhereABodyReaches guards against doubling the whole
// component set: a type nothing sends gets no input twin.
func TestAnInputTwinIsMintedOnlyWhereABodyReaches(t *testing.T) {
	sdl := openapi.GraphQL(doc(
		map[string]any{"Reply": map[string]any{
			"type":       "object",
			"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
		}},
		map[string]openapi.PathItem{
			"/v1/thing": {"get": {OperationID: "thingRead", Tool: true, Responses: ok(ref("Reply"))}},
		}))

	if strings.Contains(sdl, "input ReplyInput") {
		t.Errorf("an input twin was minted for a type no body reaches:\n%s", sdl)
	}
}

// TestAShapeGraphQLCannotNameBecomesJSON is the escape hatch. JSON Schema is an
// open vocabulary and GraphQL's types are closed; a oneOf or a property-less map
// must still be published, because dropping it narrows the surface silently.
func TestAShapeGraphQLCannotNameBecomesJSON(t *testing.T) {
	sdl := openapi.GraphQL(doc(
		map[string]any{
			"Either": map[string]any{"oneOf": []any{
				map[string]any{"type": "string"}, map[string]any{"type": "integer"},
			}},
			"Bag": map[string]any{"type": "object"},
		},
		map[string]openapi.PathItem{
			"/v1/a": {"get": {OperationID: "readEither", Tool: true, Responses: ok(ref("Either"))}},
			"/v1/b": {"get": {OperationID: "readBag", Tool: true, Responses: ok(ref("Bag"))}},
		}))

	if !strings.Contains(sdl, "scalar JSON") {
		t.Error("the escape hatch must be declared")
	}
	for _, dead := range []string{"type Either {", "type Bag {"} {
		if strings.Contains(sdl, dead) {
			t.Errorf("%q is not nameable in GraphQL and must not be emitted as a type:\n%s", dead, sdl)
		}
	}
	if !strings.Contains(sdl, "readEither: JSON") || !strings.Contains(sdl, "readBag: JSON") {
		t.Errorf("an unnameable result must still answer:\n%s", sdl)
	}
}

// TestTheProjectionIsDeterministic is what makes this diffable. Every map here is
// walked sorted, so two runs over one document are one schema.
func TestTheProjectionIsDeterministic(t *testing.T) {
	d := doc(
		map[string]any{
			"A": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
			"B": map[string]any{"type": "object", "properties": map[string]any{"y": map[string]any{"type": "string"}}},
		},
		map[string]openapi.PathItem{
			"/v1/a": {"get": {OperationID: "readA", Tool: true, Responses: ok(ref("A"))}},
			"/v1/b": {"get": {OperationID: "readB", Tool: true, Responses: ok(ref("B"))}},
			"/v1/c": {"post": {OperationID: "writeC", Tool: true, RequestBody: body(ref("A")), Responses: ok(ref("B"))}},
		})

	first := openapi.GraphQL(d)
	for i := 0; i < 8; i++ {
		if got := openapi.GraphQL(d); got != first {
			t.Fatalf("run %d differs — the projection is not deterministic", i)
		}
	}
}

// TestTheFleetSchemaParses is the one that matters. It projects THIS repository's
// composed document — every app's own subset, the same bytes the host embeds — and
// hands the result to a real GraphQL parser. A schema that does not parse is not a
// schema, and no unit test over a toy document would have caught the shapes two
// thousand real operations carry.
func TestTheFleetSchemaParses(t *testing.T) {
	subsets, err := openapi.Subsets(manifest.Names(), fromTree, manifest.StageOf)
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}
	composed, err := openapi.Fleet(subsets)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}

	sdl := openapi.GraphQL(composed)
	if _, err := graphqlgo.ParseSchema(sdl, nil); err != nil {
		t.Fatalf("the fleet schema does not parse: %v\n\nfirst 2000 bytes:\n%s", err, head(sdl, 2000))
	}

	t.Logf("fleet schema: %d bytes, %d query fields, %d mutation fields, %d types",
		len(sdl),
		fieldCount(section(sdl, "type Query {")),
		fieldCount(section(sdl, "type Mutation {")),
		strings.Count(sdl, "\ntype ")+strings.Count(sdl, "\ninput "))
}

// TestTheFleetSchemaPublishesTheDispatchableSurface pins the size against the
// document's own count, so a projection that silently drops half the fleet fails
// here rather than looking merely smaller.
func TestTheFleetSchemaPublishesTheDispatchableSurface(t *testing.T) {
	subsets, err := openapi.Subsets(manifest.Names(), fromTree, manifest.StageOf)
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}
	composed, err := openapi.Fleet(subsets)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}

	want := 0
	for _, item := range composed.Paths {
		for _, op := range item {
			if op != nil && op.Tool && op.OperationID != "" {
				want++
			}
		}
	}
	if want == 0 {
		t.Fatal("this repository composed no dispatchable operations at all")
	}

	sdl := openapi.GraphQL(composed)
	got := fieldCount(section(sdl, "type Query {")) + fieldCount(section(sdl, "type Mutation {"))
	if got != want {
		t.Errorf("schema publishes %d fields, document marks %d dispatchable", got, want)
	}
}

// section is one SDL block, from its header to the closing brace.
func section(sdl, header string) string {
	i := strings.Index(sdl, header)
	if i < 0 {
		return ""
	}
	rest := sdl[i+len(header):]
	if j := strings.Index(rest, "\n}"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// fieldCount counts field lines in a block, which are the ones that are not the
// comment above them.
func fieldCount(block string) int {
	n := 0
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
	}
	return n
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TestEveryPublishedFieldCanBeSent is the invariant the whole door rests on, and
// it was asserted only by construction: [GraphQL] and [Fields] read one document
// at one moment, so a field in the schema is a field in the table.
//
// "By construction" is a claim about code that both functions have to keep. They
// derive their names through the same call today; the day one of them sanitizes
// differently, the schema advertises a name the dispatch cannot resolve and the
// caller gets `no field named` for something it read in the schema. That is the
// exact failure this door was built to end, so it is worth a test rather than a
// sentence.
func TestEveryPublishedFieldCanBeSent(t *testing.T) {
	subsets, err := openapi.Subsets(manifest.Names(), fromTree, manifest.StageOf)
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}
	composed, err := openapi.Fleet(subsets)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}

	sdl := openapi.GraphQL(composed)
	table := openapi.Fields(composed)
	if len(table) == 0 {
		t.Fatal("the dispatch table is empty")
	}

	published := map[string]bool{}
	for _, root := range []string{"type Query {", "type Mutation {"} {
		for _, line := range strings.Split(section(sdl, root), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			name := line
			if i := strings.IndexAny(name, "(:"); i >= 0 {
				name = name[:i]
			}
			published[name] = true
		}
	}
	if len(published) == 0 {
		t.Fatal("the schema published no fields")
	}

	for name := range published {
		f, ok := table[name]
		if !ok {
			t.Errorf("the schema publishes %q and the dispatch table cannot resolve it", name)
			continue
		}
		if f.App == "" {
			t.Errorf("%q resolves to no app, so nothing can answer it", name)
		}
		if f.Method == "" || f.Path == "" {
			t.Errorf("%q resolves to %q %q — an address nothing can be sent to", name, f.Method, f.Path)
		}
	}
	for name := range table {
		if !published[name] {
			t.Errorf("the dispatch table holds %q and the schema never published it", name)
		}
	}
	t.Logf("%d fields, every one publishable and sendable", len(published))
}

// TestAPathParameterIsCarriedIntoTheTable pins the split the executor depends on:
// a parameter in the ADDRESS is not a query parameter, and confusing the two
// sends {braces} to a child that 404s on them.
func TestAPathParameterIsCarriedIntoTheTable(t *testing.T) {
	table := openapi.Fields(doc(nil, map[string]openapi.PathItem{
		"/v1/thing/{id}": {"get": {
			OperationID: "thingRead", Tool: true, App: "thing",
			Parameters: []openapi.Parameter{
				{Name: "id", In: "path", Required: true, Schema: map[string]any{"type": "string"}},
				{Name: "limit", In: "query", Schema: map[string]any{"type": "integer"}},
			},
			Responses: ok(map[string]any{"type": "object"}),
		}},
	}))

	f, ok := table["thingRead"]
	if !ok {
		t.Fatal("thingRead is missing from the table")
	}
	if len(f.Route) != 1 || f.Route[0] != "id" {
		t.Errorf("route params = %v, want [id]", f.Route)
	}
	if len(f.Query) != 1 || f.Query[0] != "limit" {
		t.Errorf("query params = %v, want [limit]", f.Query)
	}
	if f.Path != "/v1/thing/{id}" {
		t.Errorf("path = %q; the template must survive, because substituting it is the arguments' job", f.Path)
	}
}
