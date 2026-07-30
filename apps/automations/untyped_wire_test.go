package automations

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// Four /v1/automations routes are deliberately NOT typed ops, and routes() names the
// wire fact behind each one. Prose is not a gate: a later reader can retype any of
// them, watch the suite stay green, and ship a silent wire change — three of the four
// break on inputs no existing test sends.
//
// These tests pin the FACTS, so the exclusion is enforced rather than asserted. Each
// one fails the moment its route becomes a typed op, and says which fact was lost.
//
// The shared mechanism for three of them is zip's typed-op invoke (typed.go): it
// unmarshals the request body into the op's In BEFORE the handler runs and answers
// `invalid body:` 400 when that fails. So an In can only describe a body whose shape
// is CLOSED and known. Reading raw bytes from cloud.Request(ctx) does NOT recover
// these routes — the 400 is returned before the handler is ever called.

// TestMCPAnswersUnparseableBody200 pins the JSON-RPC contract on POST
// /v1/automations/mcp: a body that is not JSON is a PROTOCOL result, not a transport
// failure, so it answers HTTP 200 carrying a -32700 (parse error) object. A typed op
// would answer 400 with no JSON-RPC envelope at all, which every conforming client
// reads as a transport failure instead of the parse error it is.
func TestMCPAnswersUnparseableBody200(t *testing.T) {
	app := newApp(t)

	r := reqRaw(t, app, "/v1/automations/mcp", "acme", `{"jsonrpc":"2.0","id":1,`)
	if r.Code != http.StatusOK {
		t.Fatalf("unparseable JSON-RPC body must answer 200 (the error rides in the envelope), got %d: %s", r.Code, r.Body)
	}
	var out struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("body must be a JSON-RPC envelope: %v (%s)", err, r.Body)
	}
	if out.Error.Code != -32700 {
		t.Fatalf("want JSON-RPC parse error -32700, got %d: %s", out.Error.Code, r.Body)
	}
}

// TestResumeAcceptsAnyJSONValue pins the resume payload on POST
// /v1/automations/runs/{id}/resume: it is an ARBITRARY JSON value — a number, a
// string, an array, a bool, null or an object — handed verbatim to the waitpoint as
// its output. A struct In accepts only an object (and null), so `42`, `"hi"`, `[1,2]`
// and `true` would all become 400s.
//
// No engine is wired in this harness, so every accepted payload lands on the same
// engine error. That is exactly the discriminator: the payload's SHAPE must not
// change the answer, and must never produce 400.
func TestResumeAcceptsAnyJSONValue(t *testing.T) {
	app := newApp(t)

	now := time.Now().UnixMilli()
	run := FlowRun{
		ID: "run_arb", Org: "acme", FlowID: "flow_arb", FlowVersionID: "ver_arb",
		WorkflowID: "wf_arb", Status: RunPaused, StartTime: now, Created: now, Updated: now,
	}
	if _, err := mounted.State.store.CreateRunIfAbsent(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// An object body is the shape a typed In could describe — it is the CONTROL.
	control := reqRaw(t, app, "/v1/automations/runs/run_arb/resume", "acme", `{"a":1}`)
	if control.Code == http.StatusBadRequest {
		t.Fatalf("object resume payload must not 400, got %d: %s", control.Code, control.Body)
	}

	// Every one of these is a legal resume payload today and a 400 under a struct In.
	for _, raw := range []string{`42`, `"hi"`, `[1,2]`, `true`, `null`} {
		got := reqRaw(t, app, "/v1/automations/runs/run_arb/resume", "acme", raw)
		if got.Code == http.StatusBadRequest {
			t.Fatalf("resume payload %s must be accepted verbatim, got 400: %s", raw, got.Body)
		}
		if got.Code != control.Code {
			t.Fatalf("resume payload %s changed the answer (%d) vs an object body (%d): the payload shape must not decide",
				raw, got.Code, control.Code)
		}
	}
}

// TestInboundHookAcceptsPayloadKeysCollidingWithPathParams pins the decisive fact
// about POST /v1/automations/hooks/{source}/{event}: the body is an OPEN-KEYED event
// payload threaded to flows as {{trigger.*}}, so a producer may legitimately send a
// key named "source" or "event" holding any JSON type.
//
// This is the fact that blocks typing, and it is sharper than the raw-byte dedupe
// hash routes() also cites — raw bytes ARE reachable from a typed op via
// cloud.Request(ctx), but this is not. zip binds :source and :event by NAME
// (bindURL matches a path param to the field whose json tag, else field name, equals
// it), so a typed In MUST carry fields named source and event. Unmarshalling
// {"source": 42} into that In fails, and zip answers 400 before the handler runs —
// where today the event is accepted and delivered.
func TestInboundHookAcceptsPayloadKeysCollidingWithPathParams(t *testing.T) {
	app := newApp(t)
	captureStarter(t)
	seedWebhookFlow(t, mounted.State.store, "acme", "github", "push")

	// Each body carries a key that a typed In would have to own, holding a type that
	// cannot bind to the string field the path param needs.
	for _, raw := range []string{
		`{"source":42}`,
		`{"source":{"nested":true}}`,
		`{"event":[1,2]}`,
		`{"source":null,"event":99}`,
	} {
		got := reqRaw(t, app, "/v1/automations/hooks/github/push", "acme", raw)
		if got.Code != http.StatusOK {
			t.Fatalf("event payload %s is legal today and must answer 200, got %d: %s", raw, got.Code, got.Body)
		}
		var resp struct {
			Matched int `json:"matched"`
		}
		if err := json.Unmarshal(got.Body, &resp); err != nil {
			t.Fatalf("hook body for %s: %v (%s)", raw, err, got.Body)
		}
	}
}

// TestOperationsAnswersTwoBodyShapes pins the reason POST
// /v1/automations/flows/{id}/operations cannot be a typed op: it answers TWO
// different bodies on one route and one status. CHANGE_STATUS is flow-scoped and
// answers the FLOW; every other operation edits the step tree and answers the
// VERSION. A typed op declares ONE Out, so typing this route would have to change
// the body one of the two branches sends.
//
// A union Out does not rescue it either: Flow's externalId/folderId/publishedVersionId
// are emitted unconditionally today, and the omitempty a union would need to keep the
// version branch clean would delete them from the flow branch.
func TestOperationsAnswersTwoBodyShapes(t *testing.T) {
	app := newApp(t)

	create := req(t, app, http.MethodPost, "/v1/automations/flows", "acme", map[string]any{
		"displayName": "Two Shapes",
		"trigger": map[string]any{
			"name": "trigger", "type": TriggerTypePiece, "displayName": "Start",
			"strategy": string(StrategyManual),
			"settings": map[string]any{"pieceName": "core", "triggerName": "manual"},
		},
	})
	var pf populatedFlow
	if err := json.Unmarshal(create.Body, &pf); err != nil {
		t.Fatalf("create flow: %v (%s)", err, create.Body)
	}
	path := "/v1/automations/flows/" + pf.ID + "/operations"

	// CHANGE_STATUS answers the FLOW: projectId is a Flow field, and no FlowVersion
	// carries it.
	st := req(t, app, http.MethodPost, path, "acme", map[string]any{
		"type": string(OpChangeStatus), "request": map[string]any{"status": string(FlowEnabled)},
	})
	if st.Code != http.StatusOK {
		t.Fatalf("CHANGE_STATUS want 200, got %d: %s", st.Code, st.Body)
	}
	var flowBody map[string]any
	if err := json.Unmarshal(st.Body, &flowBody); err != nil {
		t.Fatalf("CHANGE_STATUS body: %v (%s)", err, st.Body)
	}
	if _, ok := flowBody["projectId"]; !ok {
		t.Fatalf("CHANGE_STATUS must answer the Flow (projectId present), got %s", st.Body)
	}

	// Every other operation answers the VERSION: state/schemaVersion are FlowVersion
	// fields, and no Flow carries them.
	nm := req(t, app, http.MethodPost, path, "acme", map[string]any{
		"type": string(OpChangeName), "request": map[string]any{"displayName": "Renamed"},
	})
	if nm.Code != http.StatusOK {
		t.Fatalf("CHANGE_NAME want 200, got %d: %s", nm.Code, nm.Body)
	}
	var verBody map[string]any
	if err := json.Unmarshal(nm.Body, &verBody); err != nil {
		t.Fatalf("CHANGE_NAME body: %v (%s)", err, nm.Body)
	}
	if _, ok := verBody["schemaVersion"]; !ok {
		t.Fatalf("CHANGE_NAME must answer the FlowVersion (schemaVersion present), got %s", nm.Body)
	}

	// The two shapes are DISJOINT on their discriminators — one Out cannot be both.
	if _, ok := verBody["projectId"]; ok {
		t.Fatalf("the version branch must not carry projectId: %s", nm.Body)
	}
	if _, ok := flowBody["schemaVersion"]; ok {
		t.Fatalf("the flow branch must not carry schemaVersion: %s", st.Body)
	}
}
