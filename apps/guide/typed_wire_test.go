package guide

// typed_wire_test.go is the ledger, and it exists because the reason that kept the
// two GATED transitions raw had EXPIRED and nothing could notice.
//
// That refusal was precise: a blocked step answers a structured 409, and a zip
// error could carry no members beside its own envelope, so a typed op would have
// dropped the `blockedBy` array that names what is in the way. It was correct when
// it was written, HTTPError.Detail landed, and the comment went on saying it —
// because a comment cannot fail. The same shape, in the same week, as the five
// agents session writes whose refusal ended "they go typed when zip can express a
// response with a body per status."

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list, and the four left are two families.
//
// THE DOCUMENT WRITES (2) take a YAML-**or**-JSON body: `sigs.k8s.io/yaml` accepts
// either, and a typed op decodes with jsonenc before the handler is entered, so a
// curriculum written in YAML — which is how a human writes one — would answer 400.
// TestDocumentPutsAcceptYAML holds that wire.
//
// THE MERGE PATCH (1) takes an opaque RFC 7386 patch whose keys are the ITEM's own
// and whose explicit `null` DELETES a key. A pointer field cannot tell an explicit
// null from an absent one, so typing it changes the MERGE and not merely the
// schema — the one refusal here that is about semantics rather than transport.
//
// THE AGENT RUN (1) streams the agent's actions as Server-Sent Events when the
// caller asks (Accept: text/event-stream, or ?stream=1). A typed op answers exactly
// one marshalled value; there is no Out that means "I already streamed".
// TestDoStreamsSSE pins both triggers.
//
// Note what is NOT here any more: the 409. /do still answers it, and that is no
// longer a reason for anything, because blockedErr.refusal renders it through a
// returned error. When SSE becomes expressible this op converts with no further
// work on the refusal.
var untypedByDesign = map[string]string{
	"PUT /v1/guide/curriculum": "the body is YAML or JSON (sigs.k8s.io/yaml); a typed op json-decodes " +
		"before the handler, so the YAML a human writes would answer 400.",
	"PUT /v1/guide/blueprint": "the same YAML-or-JSON body as the curriculum.",
	"PATCH /v1/guide/blueprint/{collection}/{id}": "an opaque RFC 7386 merge patch whose keys are the " +
		"item's own and whose explicit null DELETES a key — a pointer field cannot tell that from absent, " +
		"so typing it changes the merge semantics, not just the schema.",
	"POST /v1/guide/steps/{id}/do": "streams the agent's actions as SSE when the caller asks; a typed op " +
		"answers one marshalled value and there is no Out meaning 'I already streamed'.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to the served
// surface, so a route added raw goes red without anyone remembering this file, and
// a reason that stops being true goes red the moment its op is written.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := newApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "guide", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/guide") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/guide") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if !typed[key] {
			if _, named := untypedByDesign[key]; !named {
				untyped = append(untyped, key)
			}
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK method. "+
			"Convert it, or name it in untypedByDesign with the wire fact that keeps it raw — re-read "+
			"against the PINNED zip, never inherited from an older pass. The 409 that kept two of these "+
			"raw is the cautionary case: it was true, it stopped being true, and the comment went on "+
			"saying it because a comment cannot fail.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this app no longer serves", key)
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
