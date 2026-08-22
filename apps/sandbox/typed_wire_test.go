package sandbox

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of sandbox operations that are NOT typed
// ops, each with the wire fact that keeps it raw. The address is written the way
// the DOCUMENT writes it, which is the identity every projection keys on.
//
// SIX, in three families, and none of them is "not looked at yet":
//
//   - THE FILE PLANE answers and takes RAW BYTES. GET /fs is text/plain — a file
//     as its bytes, a directory as one entry per line — and a typed op's only
//     response path is c.JSON. POST /fs takes the file ITSELF as the request
//     body, and zip decodes every non-empty typed body as JSON, so a typed In
//     would turn today's write into a 400 on the first binary file. The agent's
//     door carries the same two operations in a JSON shape
//     (/v1/sandbox/{read,write}), which is what a caller wanting types should
//     reach for — this pair is the byte-exact one, kept because a shell pipeline
//     needs it.
//   - THE INTERACTIVE DOCUMENTS answer text/html: a page a browser loads, which
//     then opens its own socket. Same c.JSON refusal, and typing them would
//     publish a JSON schema for a page.
//   - THE WEBSOCKETS are protocol upgrades. A typed op is entered after the body
//     is read and answers one JSON value; there is no Out that means "I hijacked
//     the connection".
//
// The TICKETS that authorize those doors ARE typed, which is the useful half: an
// agent can mint a grant and hand it to a human, and the page and the socket stay
// the browser's business.
var untypedByDesign = map[string]string{
	"GET /v1/sandbox/{id}/fs": "answers text/plain — a file as its bytes, a directory as one entry per " +
		"line — and a typed op's only response path is c.JSON. The typed twin is POST /v1/sandbox/read.",
	"POST /v1/sandbox/{id}/fs": "takes the file's RAW BYTES as the request body, and zip decodes every " +
		"non-empty typed body as JSON. The typed twin is POST /v1/sandbox/write.",
	"GET /v1/sandbox/{id}/terminal": "answers text/html: the page a browser loads, which opens its own " +
		"socket. A typed op always marshals JSON.",
	"GET /v1/sandbox/{id}/screen": "answers text/html for the same reason as the terminal page.",
	"GET /v1/sandbox/{id}/terminal/ws": "a WebSocket upgrade — there is no typed Out that means the " +
		"connection was hijacked.",
	"GET /v1/sandbox/{id}/screen/ws": "a WebSocket upgrade, same refusal as the terminal socket.",
}

// TestEveryRouteIsTypedOrNamed reads BOTH projections of the live router and
// requires the two ledgers to SUM to the served surface: a route added untyped
// goes red without anyone remembering this file, and a name that stops being
// served goes red too.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := mountHTTP(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "sandbox", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/sandbox") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/sandbox") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if typed[key] {
			continue
		}
		if _, named := untypedByDesign[key]; !named {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"An untyped route publishes no schema, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it, or name it in untypedByDesign with the WIRE FACT that keeps it "+
			"raw — re-read against the pinned zip, never inherited from an older pass.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this surface no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestEveryTypedOpIsDescribed: prose is the product surface. A typed op with no
// description reaches the document, every generated SDK and the MCP tool list as
// a name and a shape with nothing saying what it does.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountHTTP(t), openapi.Info{Title: "sandbox", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/sandbox") {
			continue
		}
		for method, op := range item {
			key := strings.ToUpper(method) + " " + path
			if _, named := untypedByDesign[key]; named {
				continue
			}
			if strings.TrimSpace(op.Description) == "" && strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/sandbox/...", key)
			}
		}
	}
}
