package cloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// /v1/graph and /v1/graphql are different things and the difference is easy to
// lose: one is the knowledge graph — assertions, nodes, neighbours — and the
// other is the query language over every typed op. serve.go mounts the second at
// /v1/graphql for exactly that reason, and this is the test that says so, because
// the next person to read "graph" in a path will assume there is only one.
func TestTheGraphProjectionDoesNotShadowTheKnowledgeGraph(t *testing.T) {
	type assertion struct {
		Subject string `json:"subject"`
	}
	type node struct {
		ID string `json:"id"`
	}

	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	// What apps/graph registers: GET and POST on exactly /v1/graph.
	zip.Get(app, "/v1/graph", func(ctx context.Context, _ *assertion) (*node, error) {
		return &node{ID: "the knowledge graph answered"}, nil
	}, zip.WithOperationID("graphRead"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	app.MountGraph("/v1/graphql")

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// The knowledge graph still owns its address.
	code, body := get("/v1/graph")
	if code != http.StatusOK || !strings.Contains(body, "the knowledge graph answered") {
		t.Fatalf("the projection shadowed /v1/graph (%d): %s", code, body)
	}
	if strings.Contains(body, "type Query {") {
		t.Fatalf("/v1/graph served a GraphQL schema — the two addresses were conflated:\n%s", body)
	}

	// And the projection answers at its own.
	code, body = get("/v1/graphql")
	if code != http.StatusOK || !strings.Contains(body, "type Query {") {
		t.Fatalf("/v1/graphql did not serve the schema (%d): %s", code, body)
	}
}
