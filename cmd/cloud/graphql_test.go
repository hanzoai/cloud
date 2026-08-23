package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// TestTheGraphQLDoorIsTheHostsNotACatchAlls is the defect, as a test.
//
// /v1/graphql was claimed by nobody, so it fell to the app holding the /v1
// remainder — which mounted a projection of its OWN registry and answered with a
// schema one field wide. The document endpoint had exactly this bug and this is the
// same test for the same shape, because the reasoning is identical: an answer
// about the whole fleet is the host's, since no plugin can see past itself.
func TestTheGraphQLDoorIsTheHostsNotACatchAlls(t *testing.T) {
	app := host(t)

	code, ctype, body := do(t, app, "/v1/graphql")
	if code != 200 {
		t.Fatalf("GET /v1/graphql = %d, want 200", code)
	}
	// The oracle answers with an app's NAME, so a bare name here IS the misroute.
	if to := strings.TrimSpace(body); !strings.HasPrefix(to, "#") {
		t.Fatalf("GET /v1/graphql was answered by the %q plugin, not by the host — "+
			"which is how this address came to publish one field about the wrong registry", to)
	}
	if !strings.Contains(ctype, "text/plain") {
		t.Errorf("GET /v1/graphql Content-Type = %q, want the SDL as text", ctype)
	}
}

// TestTheSchemaIsTheFleetsAndNotOneApps pins the size. One field was the bug; a
// schema that shrank back to a handful would be the same bug returning quietly.
func TestTheSchemaIsTheFleetsAndNotOneApps(t *testing.T) {
	app := host(t)
	_, _, sdl := do(t, app, "/v1/graphql")

	fields := 0
	for _, block := range []string{"type Query {", "type Mutation {"} {
		i := strings.Index(sdl, block)
		if i < 0 {
			t.Fatalf("the schema has no %s at all", block)
		}
		rest := sdl[i+len(block):]
		if j := strings.Index(rest, "\n}"); j >= 0 {
			rest = rest[:j]
		}
		for _, line := range strings.Split(rest, "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				fields++
			}
		}
	}
	if fields < 500 {
		t.Errorf("the fleet schema publishes %d fields; this deployment serves far more than that, "+
			"so the projection is reading one process's registry again", fields)
	}
	t.Logf("fleet schema: %d bytes, %d fields", len(sdl), fields)
}

// TestClaimingTheGraphQLDoorTookNothingWithIt is the other half: one address, not
// the family. The knowledge graph keeps /v1/graph and ai keeps its own product.
func TestClaimingTheGraphQLDoorTookNothingWithIt(t *testing.T) {
	app := host(t)

	for path, want := range map[string]string{
		"/v1/graph":            "graph",
		"/v1/graph/vocabulary": "graph",
		"/v1/chat/completions": "ai",
		"/v1/models":           "ai",
	} {
		if _, _, to := do(t, app, path); strings.TrimSpace(to) != want {
			t.Errorf("GET %s -> %q, want %q", path, strings.TrimSpace(to), want)
		}
	}
}

// TestTheIndexAdvertisesTheQueryLanguage keeps the endpoint findable. A caller reads
// the root once and learns every way into this API; a projection missing from
// that list is one nobody is told about, which is how this address came to be
// unclaimed in the first place.
func TestTheIndexAdvertisesTheQueryLanguage(t *testing.T) {
	app := host(t)

	code, _, body := do(t, app, "/v1")
	if code != 200 {
		t.Fatalf("GET /v1 = %d, want 200", code)
	}
	var root struct {
		Links map[string]struct {
			Href string `json:"href"`
		} `json:"_links"`
	}
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatalf("the index is not JSON: %v: %.120s", err, body)
	}
	link, ok := root.Links["graphql"]
	if !ok {
		t.Fatalf("the index links %v and not the query language", keysOf(root.Links))
	}
	if link.Href != openapi.GraphPath {
		t.Errorf("the index points at %q, want %q", link.Href, openapi.GraphPath)
	}
	// And it is a real address, not a link to nothing.
	if c, _, b := do(t, app, link.Href); c != 200 || !strings.HasPrefix(strings.TrimSpace(b), "#") {
		t.Errorf("the advertised address answered %d / %.60s", c, b)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
