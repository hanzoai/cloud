// typed_wire_test.go — the projection gate, over the WHOLE capability. Three of
// this package's operations are TYPED ops; the rest are not, and each of those has
// a WIRE FACT that keeps it out. Both halves are MEASURED here rather than asserted
// in prose, because prose cannot go red: a route added untyped goes red without
// anyone remembering to name it, a reason naming a route this package no longer
// serves goes red too, and the two ledgers must sum to the surface the live router
// actually serves.
//
// ONE gate, because there is one capability. The node plane and the run plane each
// carried their own copy of this file while they were two apps, which meant each
// measured half a surface and neither could see a route that landed in the gap.
package bots

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of bot operations that are NOT typed ops, each
// with the wire fact that keeps it out. A typed op is a route PLUS a registry entry
// — the one value the OpenAPI operation, the MCP tool, the CLI command and the
// generated SDK method all come from — so an operation missing from that registry is
// invisible to all four. These are missing on purpose: every one would answer a
// DIFFERENT wire as a typed op, and typing is a description task. Addresses are
// written the way the DOCUMENT writes them, which is the identity every projection
// keys on.
var untypedByDesign = map[string]string{
	"GET /v1/nodes/connect": "a WebSocket UPGRADE. It answers 101 and the connection then carries " +
		"duplex frames for the life of the node (NodeWS, ws.go). A typed op answers one marshalled " +
		"value at one declared success status, and zip's WithStatus refuses a non-2xx — there is no " +
		"Out that can express a socket.",

	"POST /v1/nodes/{id}/invoke": "a refusal here is a 403 carrying a DOMAIN body — " +
		"{\"error\":\"denied\",\"code\":…,\"reason\":…} — that a client switches on, returned both for " +
		"the pre-flight system.run sanitize and for the node's own denial. A typed op's only refusal is " +
		"a RETURNED error, which zip renders as its flat {status,code,error}; writing the body from " +
		"inside the op does not escape it either, because a nil Out is stamped cmp.Or(op.Status, 204) " +
		"over the 403. Same class as apps/ml's in-band 402 and task #78's multi-status responses. It " +
		"also reads the caller's X-Device-Id (callerOf), which no In field may carry: a caller that " +
		"could name its own device could pre-approve its own system.run.",

	"POST /v1/nodes/peer/invoke": "a replica-to-replica machine hop served by a net/http handler " +
		"(Registry.PeerHandler, registry.go). Its refusals are text/plain — 503 \"peer forwarding " +
		"disabled\", 403 \"forbidden\", 405, 400 — while every zip error is JSON; it caps the forwarded " +
		"body with http.MaxBytesReader, a bound a typed op cannot see; and its ORG arrives in the body, " +
		"which is correct for a hop authenticated by a shared token and is exactly what an In field must " +
		"never be on a caller-facing route.",

	// The launch stub. Two facts, either one sufficient, and both re-read against
	// the PINNED zip (v1.18.12) rather than inherited as prose.
	//
	//  1. IT HAS NO SUCCESS. zip publishes a response schema for every typed op
	//     (typed.go registerTyped → responses keyed on cmp.Or(op.Status, 200)), so
	//     typing this would declare a 200 body it can never send AND mint an MCP
	//     tool plus a CLI command for an operation that cannot succeed — a model
	//     reading the tool list would call it. apps/books/bank_api.go declines its
	//     two 501 stubs on exactly this ground.
	//  2. IT IS BODY-TOLERANT. The handler never reads the body, so ANY bytes —
	//     malformed JSON included — answer 501 today; op.invoke decodes the body
	//     before the handler runs and returns ErrBadRequest on any failure, so
	//     typing it turns those 501s into 400s. TestRunToleratesAMalformedBody
	//     (run_wire_test.go) is that measurement.
	//
	// It gets typed in the same change that gives the bot runtime a launch
	// operation, and not before.
	"POST /v1/bots/runs": "answers 501 unconditionally — a typed op publishes a SUCCESS response it can " +
		"never send, and mints an MCP tool and CLI command for an operation that cannot succeed; it is also " +
		"body-tolerant, which op.invoke's unconditional 400 on an unparseable body cannot express.",

	// The relay face. All five ARE one registration —
	// app.All("/v1/bots/runtime/*", s.proxy) in relay.go — so they share one reason.
	//
	// FIVE, not seven: the document publishes what was DECLARED, and OPTIONS and
	// TRACE were never declared — they were methods the router happened to bind
	// under All(). They left the document in ceff43ac, which is the change that
	// drew that line, so they leave the ledger with it.
	"DELETE /v1/bots/runtime/{wildcard1}": reasonProxy,
	"GET /v1/bots/runtime/{wildcard1}":    reasonProxy,
	"PATCH /v1/bots/runtime/{wildcard1}":  reasonProxy,
	"POST /v1/bots/runtime/{wildcard1}":   reasonProxy,
	"PUT /v1/bots/runtime/{wildcard1}":    reasonProxy,
}

// botOps reads BOTH projections of the live router at their one shared address form:
// what the document says is served, and which of those carry a typed registry entry.
// Reading the router — the REAL routes(), not a reconstruction of it — is what makes
// this a gate rather than prose.
func botOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountAll(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "bot", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a bot operation is neither a typed op nor
// one named above — so the next route added here is typed by default, and dropping
// one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := botOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with "+
			"the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which bot no longer serves", key)
		}
	}
	// The two ledgers must SUM to the served surface: neither may quietly shrink.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema, because
// that prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries it
// into the binary, so an op added without regenerating shows up here as a nameless
// tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := botOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed bot ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/bot/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed covers the RESPONSE side the op-level gate
// cannot see. A typed op publishes its Out's whole schema, and a property that
// reaches openapi.yaml with no description reaches every generated SDK and every MCP
// inputSchema without one too — `caps` as a bare string list nowhere documented as
// the node's own self-report rather than as what it is permitted to do.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountAll(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "bot", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	if len(published.Components.Schemas) == 0 {
		t.Fatal("no published schemas at all — a typed op must publish its In/Out")
	}
	var bare []string
	for name, schema := range published.Components.Schemas {
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/bot/...",
			len(bare), strings.Join(bare, ", "))
	}
}
