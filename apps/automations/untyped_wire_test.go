package automations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
//
// The obvious next move is to drop the struct: an In of map[string]any or any takes
// an open or arbitrary body without complaint. That is the ESCAPE HATCH these pins
// have to close, because it trades one break for a quieter one. bindURL
// (typed.go) returns early unless the In's kind is Struct, so a NON-struct In
// receives no path param at all — and hooks and resume are both addressed by the
// URL. An In can describe an open body, or it can be addressed by the URL. Not both.
// So the pins below assert ADDRESSING as well as acceptance: that the run resumed is
// the run the path named, and that the event reached the flow subscribed to that
// (source,event). Acceptance alone passes under a non-struct In — every payload
// 404s or matches nothing, uniformly, which a test comparing the answers to each
// OTHER cannot see.

// One retype the four pins below do NOT catch, which is why there is a fifth:
// a STRUCT In whose UnmarshalJSON swallows the whole body.
//
//	type resumeIn struct {
//		ID      string `json:"id"`
//		payload json.RawMessage
//	}
//	func (r *resumeIn) UnmarshalJSON(b []byte) error { r.payload = b; return nil }
//
// That In accepts every JSON value (nothing can fail) AND is a struct, so bindURL
// still binds :id from the path. The REST wire survives intact — measured: with
// resume retyped this way the whole package suite, all four pins included, stays
// GREEN. It is not a repair, though. It moves the break to the projections typing
// exists to serve, where nothing in this package was watching:
//
//   - Over MCP a tools/call carries every argument in ONE JSON object and zip binds
//     no path from it (mcp.go: `op.invoke(ctx, dec, params.Arguments, nil, nil)`),
//     and the ZAP call plane does the same (call.go). The In IS the whole message
//     there. So an In that discards its own JSON keys can never receive the address:
//     measured, that tools/call answers "run not found" for a run that exists.
//   - The published schema becomes a lie in the one direction nobody checks. The
//     MCP inputSchema and the OpenAPI requestBody for that op read
//     {"properties":{"id":{"type":"string"}}} — an object with an id — while the body
//     this route actually takes is an arbitrary JSON value in which id never appears.
//
// So the rule the exclusions really rest on is sharper than "an In cannot bind both":
// a typed op must be addressable through its In ALONE, because for two of its four
// transports the In is the only channel there is. TestOpsAddressThroughArgumentsAlone
// pins that, so this retype goes red where the REST pins cannot see it.

// TestMCPAnswersUnparseableBody200 pins the JSON-RPC contract on POST
// /v1/automations/mcp: a body that is not JSON is a PROTOCOL result, not a transport
// failure, so it answers HTTP 200 carrying a -32700 (parse error) object. A typed op
// would answer 400 with no JSON-RPC envelope at all, which every conforming client
// reads as a transport failure instead of the parse error it is.
//
// This one is closed at a layer BELOW zip, which is why no In type reaches it: the
// decoder is encoding/json (zip's internal/jsonenc, stdlib only), and Unmarshal
// validates the WHOLE input before it dispatches to any UnmarshalJSON. So neither an
// In of json.RawMessage nor an In whose UnmarshalJSON never fails sees the bytes —
// both answer the same `invalid body: unexpected end of JSON input` 400. A syntax
// error is unreachable from Go, so it cannot be re-answered as -32700 from a handler.
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
//
// It also pins ADDRESSING, which acceptance alone does not. An In of `any` DOES take
// every payload below — and silently gives up the path param, because bindURL only
// walks a struct. The run id would be "", every resume would 404, and a test that
// only compares the payloads to each other would still pass because they would fail
// UNIFORMLY. So the seeded run and an unknown one must answer DIFFERENTLY.
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

	// The run the PATH names is the run resumed: the seeded run reaches the engine
	// (not-ready here) while an unknown id is not-found. An In that cannot receive the
	// path param collapses these two into one 404.
	if control.Code == http.StatusNotFound {
		t.Fatalf("the seeded run must be FOUND — the path param has to reach the handler, got 404: %s", control.Body)
	}
	if unknown := reqRaw(t, app, "/v1/automations/runs/run_absent/resume", "acme", `{"a":1}`); unknown.Code != http.StatusNotFound {
		t.Fatalf("an unknown run id must answer 404, got %d: %s", unknown.Code, unknown.Body)
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
//
// The test asserts DELIVERY, not just acceptance, because acceptance alone leaks past
// both retypings a reader would reach for:
//
//   - a STRUCT In makes the collision a loud 400 — but a payload key it has no field
//     for is not an error at all, it is DISCARDED. {"msg":"hello"} would answer the
//     same 200 with the same matched:1 and deliver {{trigger.msg}} EMPTY. Every
//     webhook keeps working and every payload arrives blank; nothing 400s, nothing
//     logs. That is the quietest break available here, so the payload the flow
//     receives is what gets pinned.
//   - a map[string]any In takes the open body — and gives up :source and :event,
//     because bindURL only walks a struct. The event would match no subscription and
//     answer matched:0, which a test that reads the body without asserting the count
//     cannot tell from a delivery.
func TestInboundHookAcceptsPayloadKeysCollidingWithPathParams(t *testing.T) {
	app := newApp(t)
	starts := captureStarter(t)
	seedWebhookFlow(t, mounted.State.store, "acme", "github", "push")

	// Each body carries a key that a typed In would have to own, holding a type that
	// cannot bind to the string field the path param needs. msg is the payload the
	// seeded flow threads as {{trigger.msg}} — an ordinary open key, and the one a
	// struct In would drop without a word.
	for _, raw := range []string{
		`{"msg":"a","source":42}`,
		`{"msg":"b","source":{"nested":true}}`,
		`{"msg":"c","event":[1,2]}`,
		`{"msg":"d","source":null,"event":99}`,
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
		// The (source,event) the PATH named has to reach the subscription index, or
		// nothing matches.
		if resp.Matched != 1 {
			t.Fatalf("event payload %s must match the subscribed flow (matched 1), got %d: %s",
				raw, resp.Matched, got.Body)
		}
	}

	// The open-keyed payload reaches the flow VERBATIM — every key, including the one
	// that collides with a path param. This is what a struct In would silently empty.
	if len(*starts) != 4 {
		t.Fatalf("want 4 starts (one per distinct body), got %d", len(*starts))
	}
	for i, want := range []string{"a", "b", "c", "d"} {
		trigger := (*starts)[i].Trigger
		if got, _ := trigger["msg"].(string); got != want {
			t.Fatalf("start %d must carry the payload verbatim: want msg=%q, got %#v", i, want, trigger)
		}
		if _, ok := trigger["source"]; !ok {
			if _, ok := trigger["event"]; !ok {
				t.Fatalf("start %d dropped the colliding key entirely: %#v", i, trigger)
			}
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

// TestOpsAddressThroughArgumentsAlone pins the rule the two URL-addressed exclusions
// rest on: a typed op must be addressable through its In alone. Over MCP and over the
// ZAP call plane there is no URL — the arguments object IS the whole input — so an op
// whose address reaches it only from the path is addressable over REST and nowhere
// else.
//
// It has two halves, and the first is what makes the second real:
//
//   - the CHANNEL, on an op that is typed today (GET /v1/automations/runs/:id): a
//     tools/call carrying the run id in its arguments must return that run, and the
//     same call without it must not. If zip ever stopped binding an op's address from
//     the arguments object, this half goes red for all fourteen ops at once.
//   - the EXCLUSIONS, dormant: resume and hooks register no op, so zip derives no tool
//     for them and the loop below finds nothing — that absence IS the cost routes()
//     names. The moment either becomes an op it appears here, and a body-swallowing In
//     (see the file note) fails it while every REST pin above stays green.
func TestOpsAddressThroughArgumentsAlone(t *testing.T) {
	app := newAppMCP(t)

	now := time.Now().UnixMilli()
	run := FlowRun{
		ID: "run_mcp", Org: "acme", FlowID: "flow_mcp", FlowVersionID: "ver_mcp",
		WorkflowID: "wf_mcp", Status: RunSucceeded, StartTime: now, Created: now, Updated: now,
	}
	if _, err := mounted.State.store.CreateRunIfAbsent(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	text, isErr := toolsCall(t, app, "acme", "get_v1_automations_runs_id", `{"id":"run_mcp"}`)
	if isErr {
		t.Fatalf("a typed op must be addressable by its arguments alone: %s", text)
	}
	if !strings.Contains(text, `"id":"run_mcp"`) {
		t.Fatalf("tools/call must answer the run its arguments named, got %s", text)
	}
	// Without the address the SAME tool cannot find it — so "found" below discriminates.
	if _, isErr := toolsCall(t, app, "acme", "get_v1_automations_runs_id", `{}`); !isErr {
		t.Fatal("a tools/call with no id must not resolve a run — the discriminator is dead")
	}

	for _, tool := range derivedTools(app) {
		switch {
		case strings.HasSuffix(tool, "_resume"):
			// The run the arguments name has to reach the handler. A body-swallowing
			// In discards it and every resume answers not-found.
			text, isErr := toolsCall(t, app, "acme", tool, `{"id":"run_mcp","a":1}`)
			if isErr && strings.Contains(text, "run not found") {
				t.Fatalf("%s cannot address a run over MCP: its In does not receive id from the arguments object, "+
					"so the REST wire survived and this projection did not (%s)", tool, text)
			}
		case strings.Contains(tool, "_hooks_"):
			// The (source,event) the arguments name has to reach the subscription
			// index, and the open payload has to reach the flow.
			starts := captureStarter(t)
			seedWebhookFlow(t, mounted.State.store, "acme", "github", "push")
			text, isErr := toolsCall(t, app, "acme", tool, `{"source":"github","event":"push","msg":"a"}`)
			if isErr {
				t.Fatalf("%s cannot address a subscription over MCP: %s", tool, text)
			}
			if !strings.Contains(text, `"matched":1`) {
				t.Fatalf("%s must match the subscribed flow over MCP, got %s", tool, text)
			}
			if len(*starts) != 1 {
				t.Fatalf("%s must deliver over MCP: want 1 start, got %d", tool, len(*starts))
			}
			if got, _ := (*starts)[0].Trigger["msg"].(string); got != "a" {
				t.Fatalf("%s must carry the payload verbatim over MCP, got %#v", tool, (*starts)[0].Trigger)
			}
		}
	}
}
