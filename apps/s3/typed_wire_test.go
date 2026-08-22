package s3_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of object-storage operations that are NOT
// typed ops, each with the wire fact that keeps it raw. The address is written
// the way the DOCUMENT writes it, which is the identity every projection keys on.
//
// This package recorded THREE blockers and two of them have expired, which is
// why the gate is a test rather than a comment: a refusal in prose cannot notice
// that its reason stopped being true.
//
//   - THE MONEY WIRE said a balance denial must be written IN BAND, because a
//     typed op refuses only by RETURNING an error and zip renders that flat.
//     cloud.Denied carries the fleet's nested {"error":{"code","message"}} off a
//     returned error and serve.go installs DenyEnvelope app-wide. It never even
//     applied here: the gate is in guard, a MIDDLEWARE, so a denial is written
//     before an op is entered.
//   - TWO STATUSES, ONE OBJECT said /health cannot declare both. zip v1.31.0 made
//     WithStatus variadic and added StatusCoder, so the op declares the set and
//     the ANSWER says which one it is.
//
// What is left is the wildcard, and it is the same refusal apps/pricing and
// apps/kms hold: fiber binds `*` as a greedy capture, the typed registry
// publishes op.Path VERBATIM while the router reading renders {wildcardN}, and
// openapi.Fold then refuses the whole document because the two spellings of one
// route do not match. It is not a description that would be wrong — the app
// publishes NOTHING until it is resolved.
var untypedByDesign = map[string]string{
	"GET /v1/s3/buckets/{bucket}/objects/{wildcard1}": "the object key is a fiber greedy wildcard: the typed " +
		"registry publishes the path verbatim (`*`) while the router reading renders {wildcard1}, so " +
		"openapi.Fold refuses with \"typed op has no live route\" and the app publishes nothing at all.",
	"DELETE /v1/s3/buckets/{bucket}/objects/{wildcard1}": "the object key is a fiber greedy wildcard; same " +
		"two spellings of one route, same refusal from openapi.Fold.",
}

// TestEveryRouteIsTypedOrNamed reads BOTH projections of the live router at their
// one shared address form — what the document says is served, and which of those
// carry a typed registry entry — and requires the two ledgers to SUM to the
// served surface. A route added untyped goes red without anyone remembering this
// file, and a name that stops being served goes red too.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := newApp(t, true)
	doc, err := openapi.Spec(app, openapi.Info{Title: "s3", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/s3") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	// reg.Ops is already keyed "METHOD /path" in the document's own spelling —
	// Fold refuses outright when the two readings disagree, which is what makes
	// this comparison sound rather than approximate.
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/s3") {
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
			"method. Convert it (zip.Get/Post/... on the group in Mount), or name it in "+
			"untypedByDesign with the WIRE FACT that keeps it raw — re-read against the pinned "+
			"zip, never inherited from an older pass.", strings.Join(untyped, ", "))
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
	app := newApp(t, true)
	doc, err := openapi.Spec(app, openapi.Info{Title: "s3", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/s3") {
			continue
		}
		for method, op := range item {
			key := strings.ToUpper(method) + " " + path
			if _, named := untypedByDesign[key]; named {
				continue
			}
			if strings.TrimSpace(op.Description) == "" && strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/s3/...", key)
			}
		}
	}
}
