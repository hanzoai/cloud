package graph

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// ask runs one GraphQL query through the mounted endpoint as a validated principal
// and returns the decoded envelope. It goes through the ROUTE rather than
// calling the resolvers, because the thing worth testing is that the endpoint
// carries the tenant into them.
func ask(t *testing.T, app *zip.App, q string) map[string]any {
	t.Helper()
	_, b := call(t, app, http.MethodPost, "/v1/graph/graphql", "", map[string]any{"query": q})
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("the endpoint answered something that is not JSON: %v: %s", err, b)
	}
	if errs, ok := env["errors"]; ok {
		t.Fatalf("query failed: %v", errs)
	}
	return env
}

// TestTheEndpointTraversesInOneRequest is the whole reason this endpoint exists. Over
// REST, "the things this one points at, and what each of THOSE points at" is a
// request per hop with the intermediate keys held by the caller. Here it is one
// query, and the nesting is the answer's shape.
func TestTheEndpointTraversesInOneRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		assertFact(t, app, "", "acme/order/1", "placedBy", "acme/person/ada", true)
		assertFact(t, app, "", "acme/person/ada", "worksFor", "acme/org/hanzo", true)

		env := ask(t, app, `{
		entity(key: "acme/order/1") {
			key
			edges { key edges { key } }
		}
	}`)

		root, _ := env["data"].(map[string]any)["entity"].(map[string]any)
		if root["key"] != "acme/order/1" {
			t.Fatalf("root key = %v", root["key"])
		}
		hop1, _ := root["edges"].([]any)
		if len(hop1) == 0 {
			t.Fatal("no edges from the seed — the walk answered nothing, so the endpoint is not traversing")
		}
		// The second hop is what a REST caller could not have had without a second
		// request: it is resolved from a key this query never named.
		var reached []string
		for _, e := range hop1 {
			for _, h := range e.(map[string]any)["edges"].([]any) {
				reached = append(reached, h.(map[string]any)["key"].(string))
			}
		}
		if len(reached) == 0 {
			t.Error("the second hop is empty: one request bought one hop, which is what REST already did")
		}
	})
}

// TestTheEndpointReadsTheSameGraphTheOpsDo pins that this is a second ENDPOINT and not
// a second store: an assertion filed through REST is visible here, with the
// server-minted fields the write path stamps.
func TestTheEndpointReadsTheSameGraphTheOpsDo(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		assertFact(t, app, "", "acme/thing/1", "colour", "blue", false)

		env := ask(t, app, `{
		assertions(entity: "acme/thing/1") { entity relation value names by knowable }
	}`)
		rows, _ := env["data"].(map[string]any)["assertions"].([]any)
		if len(rows) != 1 {
			t.Fatalf("assertions = %d, want the one just filed", len(rows))
		}
		row := rows[0].(map[string]any)
		if row["relation"] != "colour" || row["value"] != "blue" {
			t.Errorf("read back %v", row)
		}
		if row["names"] != false {
			t.Error("a scalar assertion is not an edge")
		}
		// `by` and `knowable` are stamped server-side. Their presence here is what
		// says the resolver went through the op rather than echoing the input.
		if row["by"] == "" || row["knowable"] == "" {
			t.Errorf("server-minted fields are empty: by=%v knowable=%v", row["by"], row["knowable"])
		}
	})
}

// TestTheEndpointIsScopedToTheCallersOrg is the tenancy check, and it is the reason
// every resolver calls an op rather than the store: the org is read from the
// validated principal on the request context, so a second endpoint cannot widen it.
func TestTheEndpointIsScopedToTheCallersOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		assertFact(t, app, "", "acme/secret/1", "value", "acme-only", false)

		// The one request here that does NOT go through call: what call supplies is
		// exactly what this test varies, so routing it through the helper would hide
		// the thing being proved.
		body, _ := json.Marshal(map[string]any{
			"query": `{ assertions(entity: "acme/secret/1") { value } }`,
		})
		req, _ := http.NewRequest("POST", "http://cloud/v1/graph/graphql", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(zip.HeaderOrg, "other-corp")
		req.Header.Set(zip.HeaderUser, "other-corp/ceo@other.test")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var env map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&env)

		if data, ok := env["data"].(map[string]any); ok {
			if rows, ok := data["assertions"].([]any); ok && len(rows) > 0 {
				t.Fatalf("another org read %d assertion(s) of acme's: %v", len(rows), rows)
			}
		}
	})
}

// TestAQueryThatCannotRunIsAGraphQLError pins the wire, not the manners. A
// GraphQL client parses an error LIST out of a 200; answering a transport error
// instead reads as the server being down rather than the query being wrong.
func TestAQueryThatCannotRunIsAGraphQLError(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)

		code, b := call(t, app, http.MethodPost, "/v1/graph/graphql", "", map[string]any{"query": `{ noSuchField }`})
		if code != http.StatusOK {
			t.Errorf("status = %d, want 200 carrying an error list", code)
		}
		var env map[string]any
		_ = json.Unmarshal(b, &env)
		if _, ok := env["errors"]; !ok {
			t.Error("a query naming a field the schema does not have answered no errors")
		}
	})
}

// TestTheSchemaAndTheTablesAgree is the check graph-gophers made at mount, made
// here instead: every field the SDL publishes is answered — a row in its type's
// table, or a JSON field of its value type — and every row and field is one the
// SDL publishes. A field in one and not the other is a schema that advertises
// what nothing answers, or an answer nothing can ask for.
func TestTheSchemaAndTheTablesAgree(t *testing.T) {
	answered := map[string][]string{
		"Query":       keysOf(query),
		"Entity":      keysOf(entities),
		"Assertion":   jsonOf(assertion{}),
		"Resolution":  jsonOf(resolution{}),
		"Vocabulary":  jsonOf(vocabulary{}),
		"Declaration": jsonOf(declaration{}),
	}
	if err := zip.CheckSchema(sdl); err != nil {
		t.Fatalf("the schema is not one a client can build against: %v", err)
	}
	published := map[string][]string{}
	var typ string
	for _, line := range strings.Split(sdl, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "type ") && strings.HasSuffix(line, "{"):
			typ = strings.Fields(line)[1]
		case line == "}":
			typ = ""
		case typ != "" && line != "" && !strings.HasPrefix(line, "#"):
			name := line[:strings.IndexAny(line, "(:")]
			published[typ] = append(published[typ], strings.TrimSpace(name))
		}
	}
	if len(published) != len(answered) {
		t.Fatalf("the SDL publishes types %v; the code answers %v", sortedKeys(published), sortedKeys(answered))
	}
	for typ, fields := range published {
		sort.Strings(fields)
		if got := answered[typ]; strings.Join(got, ",") != strings.Join(fields, ",") {
			t.Errorf("%s: the SDL publishes %v, the code answers %v", typ, fields, got)
		}
	}
}

func jsonOf(v any) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		out = append(out, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(out)
	return out
}

func keysOf[T any](t table[T]) []string {
	out := make([]string, 0, len(t))
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestAFieldIsAskedOnlyWhenSelected pins the economy the tables buy: two aliases
// of one field with different arguments are two answers, and the type name is the
// schema's.
func TestAFieldIsAskedOnlyWhenSelected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		assertFact(t, app, "", "acme/order/1", "placedBy", "acme/person/ada", true)
		assertFact(t, app, "", "acme/order/1", "colour", "red", false)

		env := ask(t, app, `{
		__typename
		entity(key: "acme/order/1") {
			__typename
			edges: assertions(relation: "placedBy") { value }
			colour: assertions(relation: "colour") { value __typename }
		}
	}`)
		data := env["data"].(map[string]any)
		if data["__typename"] != "Query" {
			t.Errorf("root __typename = %v", data["__typename"])
		}
		e := data["entity"].(map[string]any)
		if e["__typename"] != "Entity" {
			t.Errorf("entity __typename = %v", e["__typename"])
		}
		edges, colour := e["edges"].([]any), e["colour"].([]any)
		if len(edges) != 1 || edges[0].(map[string]any)["value"] != "acme/person/ada" {
			t.Errorf("the placedBy alias answered %v", edges)
		}
		if len(colour) != 1 || colour[0].(map[string]any)["value"] != "red" ||
			colour[0].(map[string]any)["__typename"] != "Assertion" {
			t.Errorf("the colour alias answered %v", colour)
		}
	})
}

// answer is one GraphQL answer as a client reads it.
type gqlAnswer struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message string `json:"message"`
		Path    []any  `json:"path"`
	} `json:"errors"`
}

func askQL(t *testing.T, app *zip.App, q string) (int, gqlAnswer) {
	t.Helper()
	code, b := call(t, app, http.MethodPost, "/v1/graph/graphql", "", map[string]any{"query": q})
	var env gqlAnswer
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("not JSON: %s", b)
	}
	return code, env
}

// hubs files red's graph: one seed and twelve hubs, every hub an edge to every
// hub, so each walk from a hub answers all thirteen and a nesting of `edges` k
// deep is 13^k entities from a query of a few dozen bytes.
func hubs(t *testing.T, app *zip.App) {
	t.Helper()
	var rows []map[string]any
	edge := func(from, to string) {
		rows = append(rows, map[string]any{"entity": from, "relation": "links", "value": to, "names": true,
			"at": "2026-01-01T00:00:00Z", "seen": "2026-01-01T00:00:00Z", "source": "test", "evidence": "test://" + from})
	}
	for i := 0; i < 12; i++ {
		edge("acme/seed", fmt.Sprintf("acme/hub/%d", i))
		for j := 0; j < 12; j++ {
			edge(fmt.Sprintf("acme/hub/%d", i), fmt.Sprintf("acme/hub/%d", j))
		}
	}
	for start := 0; start < len(rows); start += 50 {
		end := min(start+50, len(rows))
		code, b := call(t, app, http.MethodPost, "/v1/graph", "", map[string]any{"assertions": rows[start:end]})
		if code/100 != 2 {
			t.Fatalf("filing the hubs answered %d: %s", code, b)
		}
	}
}

// reached is how many entities an answer holds, at every depth.
func reached(v any) int {
	n := 0
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["key"]; ok {
			n++
		}
		for _, c := range t {
			n += reached(c)
		}
	case []any:
		for _, c := range t {
			n += reached(c)
		}
	}
	return n
}

// TestATraversalCannotFanOutWithoutEnd is red's fan-out, as a regression. Before
// the bounds, `edges` nested four deep over this graph materialised 24,678
// entities from ~2,056 walks in 3.6 s — one small authenticated request, charged
// once at the door. Now four deep is refused before any op runs, and three deep —
// 2,380 entities here — stops at the first edges that would pass entityMost.
func TestATraversalCannotFanOutWithoutEnd(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		hubs(t, app)

		nest := func(k int) string {
			return `{ entity(key: "acme/seed") { key ` + strings.Repeat("edges { key ", k) + strings.Repeat("} ", k) + `} }`
		}

		start := time.Now()
		code, four := askQL(t, app, nest(4))
		if code != http.StatusOK || four.Data["entity"] != nil || len(four.Errors) != 1 ||
			!strings.Contains(four.Errors[0].Message, "nests edges 4 deep") {
			t.Fatalf("four deep answered %d: %+v", code, four)
		}

		_, three := askQL(t, app, nest(3))
		if n := reached(three.Data); n > entityMost+1 {
			t.Fatalf("three deep answered %d entities, past the %d bound", n, entityMost)
		}
		refused := false
		for _, e := range three.Errors {
			refused = refused || strings.Contains(e.Message, fmt.Sprintf("more than %d entities", entityMost))
		}
		if !refused {
			t.Fatalf("three deep over 13 hubs (2,380 entities) was not stopped: %d errors", len(three.Errors))
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("both requests took %v; the bounds should make them cheap", took)
		}

		t.Logf("four deep: refused before any op; three deep: %d entities answered, stopped at the bound; %v for both",
			reached(three.Data), time.Since(start))

		// Two deep is 1 + 13 + 169 entities, well inside the bound, and answers whole.
		_, two := askQL(t, app, nest(2))
		if len(two.Errors) != 0 || reached(two.Data) < 100 {
			t.Fatalf("two deep: %d entities, errors %+v", reached(two.Data), two.Errors)
		}
	})
}

// TestALongListUnderATraversalIsBounded is red's second fan-out, as a regression:
// the request makes a handful of walks and reads, and a plain list answers the
// size. Five hubs holding 2,500 assertions each, asked for under one edges, came
// back as 12,513 objects and 1.16 MB in 3.6 s from ~14 counted resolutions. zip
// now counts every list member answered, so it is refused whole; the same shape
// asking for 100 per hub answers.
func TestALongListUnderATraversalIsBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		var rows []map[string]any
		fact := func(entity, relation, value string, names bool) {
			rows = append(rows, map[string]any{"entity": entity, "relation": relation, "value": value, "names": names,
				"at": "2026-01-01T00:00:00Z", "seen": "2026-01-01T00:00:00Z", "source": "test", "evidence": "test://" + entity})
		}
		for h := 0; h < 5; h++ {
			hub := fmt.Sprintf("acme/hub/%d", h)
			fact("acme/seed", "links", hub, true)
			for i := 0; i < 2500; i++ {
				fact(hub, fmt.Sprintf("p%d", i), "v", false)
			}
		}
		for start := 0; start < len(rows); start += 500 {
			end := min(start+500, len(rows))
			if code, b := call(t, app, http.MethodPost, "/v1/graph", "", map[string]any{"assertions": rows[start:end]}); code/100 != 2 {
				t.Fatalf("filing answered %d: %.200s", code, b)
			}
		}

		start := time.Now()
		code, b := call(t, app, http.MethodPost, "/v1/graph/graphql", "", map[string]any{"query": `{ entity(key: "acme/seed") { edges { key assertions(limit: 100000) { id relation } } } }`})
		var env gqlAnswer
		_ = json.Unmarshal(b, &env)
		if code != http.StatusBadRequest || env.Data != nil || len(env.Errors) != 1 ||
			!strings.Contains(env.Errors[0].Message, "list members") {
			t.Fatalf("12,500 assertions under one edges answered %d, %d bytes: %.300s", code, len(b), b)
		}
		t.Logf("12,500 assertions under one edges: refused whole, %d bytes, %v", len(b), time.Since(start))

		_, small := askQL(t, app, `{ entity(key: "acme/seed") { edges { key assertions(limit: 100) { id } } } }`)
		if len(small.Errors) != 0 {
			t.Fatalf("100 per hub was refused: %+v", small.Errors)
		}
	})
}

// TestTheSchemaIsReadAtTheSameAddress is the replacement for introspection, which
// zip's engine does not serve: GET at the address returns the SDL.
func TestTheSchemaIsReadAtTheSameAddress(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		code, b := call(t, app, http.MethodGet, "/v1/graph/graphql", "", nil)
		if code != http.StatusOK || string(b) != sdl {
			t.Fatalf("GET answered %d:\n%s", code, b)
		}
		code, b = call(t, app, http.MethodPost, "/v1/graph/graphql", "", map[string]any{"query": `mutation { vocabulary { bound } }`})
		if code != http.StatusOK || !strings.Contains(string(b), "no mutations are offered") {
			t.Fatalf("a mutation answered %d: %s", code, b)
		}
	})
}
