package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/infer"
)

// fakePlane is a deterministic tool plane: it offers exactly what it is given and
// records every dispatch, so a test can assert WHAT ran and WHO it ran as.
type fakePlane struct {
	offer []infer.ToolDef
	// calls records (name, args, org, actor) in order.
	calls []planeCall
	err   error
	out   string
}

type planeCall struct{ name, args, org, actor string }

func (f *fakePlane) catalog(_ context.Context, org, _ string, want []string) []infer.ToolDef {
	if org == "" || len(want) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, n := range want {
		wanted[n] = true
	}
	var out []infer.ToolDef
	for _, d := range f.offer {
		if wanted[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

func (f *fakePlane) call(_ context.Context, org, actor, name, args string) (string, error) {
	f.calls = append(f.calls, planeCall{name: name, args: args, org: org, actor: actor})
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

// withPlane swaps the process tool plane for the duration of one test.
func withPlane(t *testing.T, p toolPlane) {
	t.Helper()
	prev := runTools
	runTools = p
	t.Cleanup(func() { runTools = prev })
}

// scriptAI answers from a script, one entry per completion, and records every
// request it was given — which is how a test proves the tools were OFFERED and
// the results were fed back.
type scriptAI struct {
	replies []infer.ChatResponse
	seen    []infer.ChatRequest
	n       int
}

func (s *scriptAI) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	cp := *req
	cp.Messages = append([]infer.ChatMessage(nil), req.Messages...)
	cp.Tools = append([]infer.ToolDef(nil), req.Tools...)
	s.seen = append(s.seen, cp)
	if s.n >= len(s.replies) {
		return nil, errors.New("scriptAI: no reply scripted")
	}
	r := s.replies[s.n]
	s.n++
	return &r, nil
}

func (s *scriptAI) Embed(_ context.Context, _ *infer.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

func (s *scriptAI) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

func toolDef(name string) infer.ToolDef {
	return infer.ToolDef{Name: name, Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}
}

// A run whose agent declares a tool the plane offers must OFFER it to the model,
// EXECUTE what the model asks for, feed the result back, and answer from the
// second completion. This is the whole point of the change: before it, Agent.Tools
// was never read by the run.
func TestRunCallsTools(t *testing.T) {
	plane := &fakePlane{offer: []infer.ToolDef{toolDef("weather")}, out: `{"temp":21}`}
	withPlane(t, plane)
	ai := &scriptAI{replies: []infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "c1", Name: "weather", Arguments: `{"city":"Tokyo"}`}}, FinishReason: "tool_calls"},
		{Content: "It is 21 degrees in Tokyo."},
	}}

	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "weather in Tokyo?", nil, "", "run_test")

	if r.Status != "ok" {
		t.Fatalf("want ok, got %q err=%q", r.Status, r.Error)
	}
	if r.Output != "It is 21 degrees in Tokyo." {
		t.Fatalf("output must be the model's answer AFTER the tool ran, got %q", r.Output)
	}
	if len(plane.calls) != 1 {
		t.Fatalf("want exactly one dispatch, got %d (%+v)", len(plane.calls), plane.calls)
	}
	got := plane.calls[0]
	if got.name != "weather" || got.args != `{"city":"Tokyo"}` {
		t.Fatalf("dispatch must carry the model's own call, got %+v", got)
	}
	if got.org != "maxpower" || got.actor != "maxpower/u1" {
		t.Fatalf("dispatch must be attributable to the run's org+actor, got %+v", got)
	}
	if len(ai.seen) != 2 {
		t.Fatalf("want two completions (ask, then answer), got %d", len(ai.seen))
	}
	if len(ai.seen[0].Tools) != 1 || ai.seen[0].Tools[0].Name != "weather" {
		t.Fatalf("the first completion must OFFER the declared tool, got %+v", ai.seen[0].Tools)
	}
	// The second completion must carry the whole transcript: who the agent is (system),
	// the user turn, the assistant's tool call, and the tool result linked back by id.
	msgs := ai.seen[1].Messages
	want := []string{infer.RoleSystem, infer.RoleUser, infer.RoleAssistant, infer.RoleTool}
	if len(msgs) != len(want) {
		t.Fatalf("want %v in the second turn, got %d: %+v", want, len(msgs), msgs)
	}
	for i, role := range want {
		if msgs[i].Role != role {
			t.Fatalf("turn %d must be %s, got %+v", i, role, msgs[i])
		}
	}
	if len(msgs[2].ToolCalls) != 1 {
		t.Fatalf("assistant turn must carry its tool calls, got %+v", msgs[2])
	}
	if msgs[3].ToolCallID != "c1" || msgs[3].Content != `{"temp":21}` {
		t.Fatalf("tool result must be linked to the call by id, got %+v", msgs[3])
	}
}

// An agent with no tools — or one whose declared names the plane does not offer —
// takes one completion with no tools field, and is shown what it is (system) and
// what it was asked (user) as two separate turns so prior turns sit between them.
func TestRunWithoutToolsIsUnchanged(t *testing.T) {
	plane := &fakePlane{} // offers nothing
	withPlane(t, plane)
	ai := &scriptAI{replies: []infer.ChatResponse{{Content: "hi there"}}}

	a := mk("maxpower", "greeter")
	a.Instructions = "You are a greeter."
	a.Tools = []string{"weather"} // declared, but the plane offers nothing
	r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "say hi", nil, "", "run_test")

	if r.Status != "ok" || r.Output != "hi there" {
		t.Fatalf("want the plain completion, got %+v", r)
	}
	if len(ai.seen) != 1 {
		t.Fatalf("want exactly one completion, got %d", len(ai.seen))
	}
	if len(ai.seen[0].Tools) != 0 {
		t.Fatalf("no-tools path must offer no tools, got %+v", ai.seen[0].Tools)
	}
	msgs := ai.seen[0].Messages
	if len(msgs) != 2 ||
		msgs[0].Role != infer.RoleSystem || msgs[0].Content != "You are a greeter." ||
		msgs[1].Role != infer.RoleUser || msgs[1].Content != "say hi" {
		t.Fatalf("want the instructions (system) and the ask (user) as two turns, got %+v", msgs)
	}
	if len(plane.calls) != 0 {
		t.Fatalf("nothing may be dispatched, got %+v", plane.calls)
	}
}

// A tool that fails must NOT kill the turn: the error goes back to the model as
// the tool's result, and the model still answers.
func TestToolFailureReachesTheModel(t *testing.T) {
	plane := &fakePlane{offer: []infer.ToolDef{toolDef("weather")}, err: errors.New("connector offline")}
	withPlane(t, plane)
	ai := &scriptAI{replies: []infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "c1", Name: "weather", Arguments: `{}`}}},
		{Content: "I could not reach the weather service."},
	}}

	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "weather?", nil, "", "run_test")

	if r.Status != "ok" {
		t.Fatalf("a failed tool must not fail the run, got %q err=%q", r.Status, r.Error)
	}
	if r.Output != "I could not reach the weather service." {
		t.Fatalf("the model must get to answer, got %q", r.Output)
	}
	msgs := ai.seen[1].Messages
	result := msgs[len(msgs)-1]
	if result.Role != infer.RoleTool || !strings.Contains(result.Content, "connector offline") {
		t.Fatalf("the failure must be handed back as the tool result, got %+v", result)
	}
}

// The loop is BOUNDED. A model that only ever asks for tools gets maxToolRounds
// tool-bearing turns and then one final turn with NO tools, which is what forces
// an answer instead of an unbounded spend.
func TestToolLoopIsBounded(t *testing.T) {
	plane := &fakePlane{offer: []infer.ToolDef{toolDef("weather")}, out: "ok"}
	withPlane(t, plane)
	replies := make([]infer.ChatResponse, maxToolRounds)
	for i := range replies {
		replies[i] = infer.ChatResponse{ToolCalls: []infer.ToolCall{{ID: "c", Name: "weather", Arguments: fmt.Sprintf(`{"step":%d}`, i)}}}
	}
	// The final, tool-less turn answers in words.
	replies = append(replies, infer.ChatResponse{Content: "done"})
	ai := &scriptAI{replies: replies}

	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "go", nil, "", "run_test")

	if r.Status != "ok" || r.Output != "done" {
		t.Fatalf("bounded loop must still answer, got %+v", r)
	}
	if len(ai.seen) != maxToolRounds+1 {
		t.Fatalf("want %d completions, got %d", maxToolRounds+1, len(ai.seen))
	}
	if len(plane.calls) != maxToolRounds {
		t.Fatalf("want %d dispatches, got %d", maxToolRounds, len(plane.calls))
	}
	if len(ai.seen[maxToolRounds].Tools) != 0 {
		t.Fatalf("the last turn must be offered NO tools so it has to answer in words")
	}
}

func TestDuplicateToolCallIsBlocked(t *testing.T) {
	plane := &fakePlane{offer: []infer.ToolDef{toolDef("weather")}, out: "ok"}
	withPlane(t, plane)
	always := infer.ChatResponse{ToolCalls: []infer.ToolCall{{ID: "c", Name: "weather", Arguments: `{}`}}}
	ai := &scriptAI{replies: []infer.ChatResponse{
		always,
		always,
		always,
		{Content: "stopped repeating"},
	}}
	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "go", nil, "", "run_test")
	if r.Status != "ok" || r.Output != "stopped repeating" {
		t.Fatalf("duplicate tool call guard must let the model recover, got %+v", r)
	}
	if len(plane.calls) != 2 {
		t.Fatalf("want 2 real dispatches before duplicate call is blocked, got %d", len(plane.calls))
	}
}

// A model that answers with nothing and calls nothing is told so once, and the
// second round's words are the answer; a model silent twice gets an honest
// line, never an empty reply.
func TestSilentAnswerIsNudgedOnce(t *testing.T) {
	for _, c := range []struct {
		replies []infer.ChatResponse
		want    string
	}{
		{[]infer.ChatResponse{{}, {Content: "Sunny, 64°F in San Francisco."}}, "Sunny, 64°F in San Francisco."},
		{[]infer.ChatResponse{{}, {}}, "couldn't put an answer into words"},
	} {
		ai := &scriptAI{replies: c.replies}
		a := mk("maxpower", "greeter")
		r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "weather in SF?", nil, "", "run_test")
		if r.Status != "ok" || !strings.Contains(r.Output, c.want) {
			t.Fatalf("want %q in the answer, got %+v", c.want, r)
		}
		if len(ai.seen) != 2 {
			t.Fatalf("a silent answer earns exactly one more round, got %d completions", len(ai.seen))
		}
		last := ai.seen[1].Messages
		if last[len(last)-1].Content != silentNote {
			t.Fatalf("the second round must be told the first was empty, got %+v", last[len(last)-1])
		}
	}
}

// A spent turn that the model still answers with NOTHING is not an empty
// reply: the last round is told its tools are gone, and silence after that is
// replaced by what was looked at and how to go on.
func TestSpentLoopNeverAnswersEmpty(t *testing.T) {
	plane := &fakePlane{offer: []infer.ToolDef{toolDef("weather")}, out: "ok"}
	withPlane(t, plane)
	always := infer.ChatResponse{ToolCalls: []infer.ToolCall{{ID: "c", Name: "weather", Arguments: `{}`}}}
	replies := make([]infer.ChatResponse, maxToolRounds)
	for i := range replies {
		replies[i] = always
	}
	replies = append(replies, infer.ChatResponse{}) // the model says nothing
	ai := &scriptAI{replies: replies}

	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "go", nil, "", "run_test")

	if r.Status != "ok" || !strings.Contains(r.Output, "ran out of steps") || !strings.Contains(r.Output, "weather") {
		t.Fatalf("a spent, silent turn must say so and how to go on, got %+v", r)
	}
	last := ai.seen[maxToolRounds].Messages
	if n := len(last); n == 0 || last[n-1].Role != infer.RoleSystem || last[n-1].Content != spentNote {
		t.Fatalf("the tool-less round must be told its tools are spent, got %+v", last[len(last)-1])
	}
}

// The dispatch principal is the run's own actor, and a run with no user subject
// (a scheduled run) lends none rather than inventing one.
func TestActorSub(t *testing.T) {
	for _, c := range []struct{ org, actor, want string }{
		{"acme", "acme/U123", "U123"},
		{"acme", "acme", ""},
		{"acme", "", ""},
		{"acme", "scheduler", "scheduler"},
	} {
		if got := actorSub(c.org, c.actor); got != c.want {
			t.Fatalf("actorSub(%q,%q) = %q, want %q", c.org, c.actor, got, c.want)
		}
	}
}

// An agent is itself a tool, so an agent that declares ITSELF would recurse with
// a fresh round cap at every level. It is never offered to itself.
func TestAgentIsNeverOfferedItself(t *testing.T) {
	a := mk("maxpower", "greeter")
	a.Tools = []string{"agent_greeter", "weather"}
	got := callableTools(a)
	if len(got) != 1 || got[0] != "weather" {
		t.Fatalf("an agent must not be offered itself, got %v", got)
	}
}

// A cycle of agents-as-tools (A → B → A) is bounded by DEPTH, which the round cap
// cannot bound: each nested run starts its own. At the limit an agent is offered
// no tools at all and has to answer for itself.
func TestNestedAgentsAreBoundedByDepth(t *testing.T) {
	plane := &fakePlane{offer: []infer.ToolDef{toolDef("weather")}, out: "ok"}
	withPlane(t, plane)
	ai := &scriptAI{replies: []infer.ChatResponse{{Content: "at the bottom"}}}

	ctx := context.Background()
	for range maxAgentDepth {
		ctx = deeper(ctx)
	}
	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	r := executeRun(ctx, ai, "maxpower", "maxpower/u1", a, "go", nil, "", "run_test")

	if r.Status != "ok" || r.Output != "at the bottom" {
		t.Fatalf("a run at the depth limit must still answer, got %+v", r)
	}
	if len(ai.seen) != 1 || len(ai.seen[0].Tools) != 0 {
		t.Fatalf("at the depth limit no tools may be offered, got %d completions %+v", len(ai.seen), ai.seen[0].Tools)
	}
	if len(plane.calls) != 0 {
		t.Fatalf("nothing may be dispatched at the depth limit, got %+v", plane.calls)
	}
}

// A dispatch carries the run one level deeper, which is what makes the depth
// bound reachable at all: the nested run reads it off the context it was handed.
func TestDispatchDeepensTheContext(t *testing.T) {
	var saw int
	withPlane(t, &depthProbe{seen: &saw, offer: []infer.ToolDef{toolDef("weather")}})
	ai := &scriptAI{replies: []infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "c1", Name: "weather", Arguments: `{}`}}},
		{Content: "done"},
	}}
	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	if r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "go", nil, "", "run_test"); r.Status != "ok" {
		t.Fatalf("run failed: %+v", r)
	}
	if saw != 1 {
		t.Fatalf("a dispatch from a top-level run must be at depth 1, got %d", saw)
	}
}

// depthProbe records the nesting depth the dispatch context carries.
type depthProbe struct {
	seen  *int
	offer []infer.ToolDef
}

func (d *depthProbe) catalog(_ context.Context, org, _ string, want []string) []infer.ToolDef {
	if org == "" || len(want) == 0 {
		return nil
	}
	return d.offer
}

func (d *depthProbe) call(ctx context.Context, _, _, _, _ string) (string, error) {
	*d.seen = agentDepth(ctx)
	return "ok", nil
}

// A tool result longer than the transcript budget is clipped AND SAID to be
// clipped — a silently truncated result is one the model believes it read whole.
func TestToolResultTruncationIsStated(t *testing.T) {
	long := strings.Repeat("x", maxToolResult+100)
	got := renderToolResult(long)
	if len(got) <= maxToolResult || !strings.Contains(got, "truncated") {
		t.Fatalf("a clipped result must say so, got %d bytes", len(got))
	}
	if s := renderToolResult(map[string]any{"a": 1}); s != `{"a":1}` {
		t.Fatalf("a structured result must reach the model as its JSON, got %q", s)
	}
}

func TestMalformedToolArgumentsStayValidJSONInTranscript(t *testing.T) {
	plane := &fakePlane{
		offer: []infer.ToolDef{toolDef("weather")},
		out:   "sunny",
	}
	withPlane(t, plane)
	ai := &scriptAI{replies: []infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "c1", Name: "weather", Arguments: `{"a":1}{"b":2}`}}},
		{Content: "fixed"},
	}}
	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "weather?", nil, "", "run_test")
	if r.Status != "ok" || r.Output != "fixed" {
		t.Fatalf("run failed: %+v", r)
	}
	if len(ai.seen) != 2 || ai.seen[1].Messages[2].ToolCalls[0].Arguments != "{}" {
		t.Fatalf("malformed tool arguments in the transcript must be replaced with valid JSON, got %+v", ai.seen)
	}
}
