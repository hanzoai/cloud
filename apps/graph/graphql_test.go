package graph

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

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

// TestTheDoorTraversesInOneRequest is the whole reason this endpoint exists. Over
// REST, "the things this one points at, and what each of THOSE points at" is a
// request per hop with the intermediate keys held by the caller. Here it is one
// query, and the nesting is the answer's shape.
func TestTheDoorTraversesInOneRequest(t *testing.T) {
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
}

// TestTheDoorReadsTheSameGraphTheOpsDo pins that this is a second ENDPOINT and not
// a second store: an assertion filed through REST is visible here, with the
// server-minted fields the write path stamps.
func TestTheDoorReadsTheSameGraphTheOpsDo(t *testing.T) {
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
}

// TestTheDoorIsScopedToTheCallersOrg is the tenancy check, and it is the reason
// every resolver calls an op rather than the store: the org is read from the
// validated principal on the request context, so a second endpoint cannot widen it.
func TestTheDoorIsScopedToTheCallersOrg(t *testing.T) {
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
}

// TestAQueryThatCannotRunIsAGraphQLError pins the wire, not the manners. A
// GraphQL client parses an error LIST out of a 200; answering a transport error
// instead reads as the server being down rather than the query being wrong.
func TestAQueryThatCannotRunIsAGraphQLError(t *testing.T) {
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
}
