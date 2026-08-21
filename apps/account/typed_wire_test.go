package account

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file is the GATE on the typed/raw partition of the account surface. The
// package doc says every addressable route is typed but two; prose alone cannot keep
// that true, because the next route added here would be untyped and nothing would go
// red. So the refusals are a CLOSED list, each carrying the wire fact that keeps it
// raw, and an operation that is neither a typed op nor on that list fails the suite —
// the next account route is typed by default, and dropping one out of the registry
// takes a deliberate edit with a reason. Same shape as apps/team/typed_wire_test.go
// and apps/pricing/typed_wire_test.go.
//
// The list holds the profile-photo pair and nothing else. It also held seven —
// GET|POST /v1/billing/{wildcard1} and the five methods of /v1/commerce/{wildcard1},
// the wildcard forwarders of the retired account-bridge subsystem. They could not be
// typed and the reasons were real: the answer carried commerce's own status and bytes
// (a 402 spend cap, a PDF invoice) where a typed dispatch ends in c.JSON under one
// declared 2xx; the request body was never JSON-validated where op.invoke unmarshals
// before the handler runs; and the address was a wildcard remainder bounded by an
// allowlist rather than by a type. All three are properties of FORWARDING, so removing
// the forwarders removed them — and what is left raw is raw because of the WIRE, which
// is a reason a route cannot outgrow.

// untypedByDesign is the CLOSED list of account operations that are NOT typed ops,
// each with the reason it cannot be one. A typed op is a route PLUS a registry entry —
// the one value the OpenAPI operation, the MCP tool, the CLI command and every
// generated SDK method come from — so an operation missing from that registry is
// invisible to all four. Nothing is missing today; an addition here needs the wire
// fact that makes typing it impossible, not a preference.
var untypedByDesign = map[string]string{
	// The profile-photo pair (avatar.go). Both are raw by a property of the WIRE, not
	// by preference: the upload's request is a multipart form, where op.invoke
	// unmarshals JSON before the handler runs; and the read's response is the image's
	// BYTES under a Content-Type derived from those bytes, where a typed dispatch ends
	// in c.JSON under one declared 2xx. Neither is a shape an In/Out can carry.
	"POST /v1/account/avatar":                      "multipart upload: the request body is a form, not JSON",
	"GET /v1/account/avatar/{org}/{user}/{digest}": "raw image bytes under a byte-derived Content-Type, not a JSON envelope",
}

// accountOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. There is no prefix filter and there must not be one — the app
// holds this package's routes and nothing else, so a filter would be a second
// list to keep in sync with the mount and would hide exactly the route that
// escaped it. It reads whatever is mounted, which is how it still fires on a
// route registered outside /v1/account.
func accountOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mount(t, "hanzo")

	doc, err := openapi.Spec(app, openapi.Info{Title: "account", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when an account operation is neither a typed
// op nor named above — so the next route added here is typed by default, and
// dropping one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := accountOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which account no longer serves", key)
		}
	}
	// A typed op that the document does not serve is the third way the partition
	// can rot: the registry entry exists, so the gate above passes it, but no
	// route answers it and every projection publishes an address that 404s.
	for key := range typed {
		if !served[key] {
			t.Errorf("typed op %q is in the registry but not in the document — it publishes an address nothing serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed fails on a typed op with no lifted prose, because
// that prose IS the product surface: it becomes the OpenAPI description AND the
// MCP tool description a model reads to pick the tool. zipdoc_gen.go is what
// carries it into the binary, so an op added without regenerating shows up here
// as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := accountOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed account ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/account/...", key)
		}
	}
}
