package account

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file is the GATE on the typed/raw partition of the account surface. The
// package doc names eleven typed ops and seven deliberate refusals; prose alone
// cannot keep that true, because the next route added here would be untyped and
// nothing would go red. So the refusals are a CLOSED list, each carrying the wire
// fact that keeps it raw, and an operation that is neither a typed op nor on that
// list fails the suite — the next account route is typed by default, and dropping
// one out of the registry takes a deliberate edit with a reason. Same shape as
// apps/team/typed_wire_test.go and apps/pricing/typed_wire_test.go.
//
// It reads the surface through mountBoth, which mounts BOTH of this package's
// subsystem registrations (account @48 and account-bridge @122) on one bare app —
// exactly what production registers. That matters here more than anywhere: all
// seven refusals live in the SECOND registration, so a gate that mounted only the
// self-service half would have declared the partition complete while covering none
// of it.

// verbatimForward is the one wire fact behind all seven refusals, and it is three
// independent facts about the same handler (billing.go, commerce.go). Each was
// re-verified against zip v1.18.6's own source, because "cannot be typed" is a
// claim about a DEPENDENCY and a dependency moves:
//
//   - THE ANSWER IS ANOTHER SERVICE'S BYTES AND STATUS. The handler ends in
//     `c.Bytes(status, raw)` — commerce's own status, commerce's own body,
//     including a PDF at billing's `invoices/{}/pdf`. A typed dispatch ends in
//     `c.JSON(out)` under the ONE status the op declared (typed.go:270-303), and
//     `WithStatus` panics on anything but a 2xx (typed.go:110), so it cannot even
//     name the upstream 4xx these routes pass through today.
//   - THE BODY IS FORWARDED AS RECEIVED, at any content type. `op.invoke`
//     json.Unmarshals the request body BEFORE the handler runs and returns
//     `ErrBadRequest("invalid body: …")` on failure (typed.go:225-231), so a form,
//     multipart or binary body a typed op would answer 400 is one this bridge
//     forwards.
//   - THE ADDRESS AND THE QUERY ARE OPEN SETS. The path is a wildcard remainder of
//     arbitrary depth (`Params("*")`) bounded by an ALLOWLIST rather than a type —
//     billingForwardable per method, commerceStoreHeads by head — and the query is
//     forwarded whole (currency, status, date ranges: whatever commerce accepts).
//     A typed op publishes a closed list of parameters, so any schema it published
//     would be a narrower claim than the wire.
//
// Opaque by construction, not by omission. Re-check when zip gains raw-body
// binding and multi-status/passthrough responses (#78's family), and convert.
const verbatimForward = "a verbatim forwarder: the path is a wildcard remainder bounded by an allowlist, " +
	"the body is forwarded as received at any content type, and the answer is commerce's own bytes AND " +
	"status — where a typed op json-decodes the body first and answers one declared status in JSON."

// untypedByDesign is the CLOSED list of account operations that are NOT typed
// ops, each with the reason it cannot be one. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and every generated SDK method come from — so an operation missing from
// that registry is invisible to all four. These seven are missing on purpose, and
// the published subset shows it: plugin/account-bridge/openapi.json carries them
// as route-only entries with no description and no schema. Addresses are written
// the way the DOCUMENT writes them, which is the identity every projection keys
// on — the wildcard renders as {wildcard1}.
var untypedByDesign = map[string]string{
	"GET /v1/billing/{wildcard1}":  verbatimForward,
	"POST /v1/billing/{wildcard1}": verbatimForward,

	"GET /v1/commerce/{wildcard1}":    verbatimForward,
	"POST /v1/commerce/{wildcard1}":   verbatimForward,
	"PUT /v1/commerce/{wildcard1}":    verbatimForward,
	"PATCH /v1/commerce/{wildcard1}":  verbatimForward,
	"DELETE /v1/commerce/{wildcard1}": verbatimForward,
}

// accountOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. There is no prefix filter and there must not be one — the app
// holds this package's routes and nothing else, and account's surface is spread
// across six top-level nouns (/v1/keys, /v1/iam, /v1/csrf, /v1/embed-status,
// /v1/billing, /v1/commerce), so any filter would be a second list to keep in sync
// with the mount and would hide exactly the route that escaped it.
func accountOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountBoth(t, "hanzo")

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
// op nor one of the seven above — so the next route added here is typed by
// default, and dropping one out of the registry takes a deliberate edit with a
// reason.
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
