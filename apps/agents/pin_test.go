package agents

// pin_test.go pins one model per generation turn (#802).
//
// A turn is many completions — the tool loop asks, runs what the model called, and
// asks again — and every round used to start from the agent's own model. So a round
// its model could not answer went to the fallback, and the next round went back: one
// tool sequence begun by one model and continued by another.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/infer"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// errRefused is a provider refusal: failover moves to the next model at once, with
// no backoff, so a test scripts exactly which round a model cannot answer.
var errRefused = errors.New("provider returned error: model is overloaded")

// turnAI answers each model from its own script, one entry per call to that model,
// and records every request in the order it was made. A nil entry is a refusal.
type turnAI struct {
	script map[string][]*infer.ChatResponse
	seen   []infer.ChatRequest
}

func (a *turnAI) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	cp := *req
	cp.Messages = slices.Clone(req.Messages)
	a.seen = append(a.seen, cp)
	q := a.script[req.Model]
	if len(q) == 0 {
		return nil, errors.New("turnAI: no reply scripted for " + req.Model)
	}
	a.script[req.Model] = q[1:]
	if q[0] == nil {
		return nil, errRefused
	}
	r := *q[0]
	return &r, nil
}

func (a *turnAI) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, nil }
func (a *turnAI) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

// models is which model each request was put to, in order.
func (a *turnAI) models() []string {
	out := make([]string, len(a.seen))
	for i, r := range a.seen {
		out[i] = r.Model
	}
	return out
}

// lookup is one tool call to the one tool these tests offer.
func lookup(id string) []infer.ToolCall {
	return []infer.ToolCall{{ID: id, Name: "lookup", Arguments: "{}"}}
}

// turnOnce runs one turn of the tool loop under its own agent.step span, as
// executeRun does, and returns that span.
func turnOnce(t *testing.T, ai infer.Client) (*infer.ChatResponse, string, sdktrace.ReadOnlySpan) {
	t.Helper()
	sink := traced(t)
	defs := []infer.ToolDef{toolDef("lookup")}
	withPlane(t, &fakePlane{offer: defs, out: "result"})

	ctx, span := agentTracer().Start(context.Background(), "agent.step")
	resp, used, err, _ := completeWithTools(ctx, ai, "acme", "acme/u1",
		[]infer.ChatMessage{{Role: infer.RoleUser, Content: "look it up"}}, "own", "steady", defs, "run-802")
	span.End()
	if err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	sp, ok := sink.find("agent.step")
	if !ok {
		t.Fatal("the turn produced no agent.step span")
	}
	return resp, used, sp
}

// switches is every "model switch" event on a span.
func switches(sp sdktrace.ReadOnlySpan) []sdktrace.Event {
	var out []sdktrace.Event
	for _, e := range sp.Events() {
		if e.Name == "model switch" {
			out = append(out, e)
		}
	}
	return out
}

// eventAttr reads one attribute off an event as text, "" when it is absent.
func eventAttr(e sdktrace.Event, key string) string {
	for _, kv := range e.Attributes {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

// reads reports whether any message of a request carries s.
func reads(req infer.ChatRequest, s string) bool {
	for _, m := range req.Messages {
		if strings.Contains(m.Content, s) {
			return true
		}
	}
	return false
}

// The production shape: the agent's model cannot answer the first round, the
// fallback does and asks for a tool, and the turn goes on. The rest of the turn is
// the fallback's — the agent's model, recovered or not, is not asked to pick up a
// tool sequence it never began.
func TestATurnKeepsTheModelThatAnsweredItsFirstRound(t *testing.T) {
	ai := &turnAI{script: map[string][]*infer.ChatResponse{
		"own":    {nil, {Content: "own picked up a turn it never began"}},
		"steady": {{ToolCalls: lookup("c1")}, {Content: "done"}},
	}}
	resp, used, sp := turnOnce(t, ai)

	if got, want := ai.models(), []string{"own", "steady", "steady"}; !slices.Equal(got, want) {
		t.Fatalf("models asked: %v, want %v — the second round went back to the agent's model mid-turn", got, want)
	}
	if resp.Content != "done" || used != "steady" {
		t.Fatalf("answer %q by %q, want \"done\" by steady", resp.Content, used)
	}
	// A first round the fallback answered is the fallback's turn from its start:
	// nothing of it was the agent model's, so there was no switch.
	if got := attr(sp, "hanzo.agent.turn.model"); got != "steady" {
		t.Fatalf("hanzo.agent.turn.model = %q, want steady, the model the turn was served by", got)
	}
	if got := attr(sp, "hanzo.agent.turn.switches"); got != "0" {
		t.Fatalf("hanzo.agent.turn.switches = %q, want 0", got)
	}
	if n := len(switches(sp)); n != 0 {
		t.Fatalf("%d model switch events on a turn one model served", n)
	}
}

// A turn whose model stops answering mid-turn moves to the next one, and that model
// starts a fresh reasoning context: the turn's words and tool calls, none of the
// reasoning the first model wrote. The turn then stays on it, still without that
// reasoning, and the switch is on the span.
func TestASwitchStartsAFreshReasoningContext(t *testing.T) {
	ai := &turnAI{script: map[string][]*infer.ChatResponse{
		"own": {
			{Content: "<think>the secret plan</think>Looking it up.", ToolCalls: lookup("c1")},
			nil,
		},
		"steady": {{ToolCalls: lookup("c2")}, {Content: "done"}},
	}}
	resp, used, sp := turnOnce(t, ai)

	if got, want := ai.models(), []string{"own", "own", "steady", "steady"}; !slices.Equal(got, want) {
		t.Fatalf("models asked: %v, want %v", got, want)
	}
	if resp.Content != "done" || used != "steady" {
		t.Fatalf("answer %q by %q, want \"done\" by steady", resp.Content, used)
	}
	for i, req := range ai.seen[2:] {
		if reads(req, "the secret plan") {
			t.Fatalf("steady's request %d carries own's reasoning: %+v", i+1, req.Messages)
		}
		// What was SAID and DONE crosses: the words, the call, its result.
		if !reads(req, "Looking it up.") || !reads(req, "result") {
			t.Fatalf("steady's request %d lost the turn's words or tool result: %+v", i+1, req.Messages)
		}
		var called bool
		for _, m := range req.Messages {
			for _, tc := range m.ToolCalls {
				called = called || tc.ID == "c1"
			}
		}
		if !called {
			t.Fatalf("steady's request %d lost own's tool call: %+v", i+1, req.Messages)
		}
	}

	if got := attr(sp, "hanzo.agent.turn.model"); got != "steady" {
		t.Fatalf("hanzo.agent.turn.model = %q, want steady, the model holding the turn", got)
	}
	if got := attr(sp, "hanzo.agent.turn.switches"); got != "1" {
		t.Fatalf("hanzo.agent.turn.switches = %q, want 1", got)
	}
	ev := switches(sp)
	if len(ev) != 1 {
		t.Fatalf("%d model switch events, want 1", len(ev))
	}
	for key, want := range map[string]string{
		"hanzo.agent.turn.from":          "own",
		"hanzo.agent.turn.to":            "steady",
		"hanzo.agent.turn.round":         "1",
		"hanzo.agent.turn.fresh_context": "true",
	} {
		if got := eventAttr(ev[0], key); got != want {
			t.Fatalf("model switch %s = %q, want %q", key, got, want)
		}
	}
}

// The model that holds a turn reads the turn as it wrote it. A fresh context is
// what a switch costs, not something every round pays.
func TestThePinnedModelReadsItsOwnTurn(t *testing.T) {
	ai := &turnAI{script: map[string][]*infer.ChatResponse{
		"own": {
			{Content: "<think>my plan</think>Looking it up.", ToolCalls: lookup("c1")},
			{Content: "done"},
		},
	}}
	_, used, sp := turnOnce(t, ai)

	if got, want := ai.models(), []string{"own", "own"}; !slices.Equal(got, want) {
		t.Fatalf("models asked: %v, want %v", got, want)
	}
	if used != "own" {
		t.Fatalf("answered by %q, want own", used)
	}
	if !reads(ai.seen[1], "<think>my plan</think>Looking it up.") {
		t.Fatalf("own's second round lost its own turn: %+v", ai.seen[1].Messages)
	}
	if got := attr(sp, "hanzo.agent.turn.model"); got != "own" {
		t.Fatalf("hanzo.agent.turn.model = %q, want own", got)
	}
	if n := len(switches(sp)); n != 0 {
		t.Fatalf("%d model switch events on a turn one model served", n)
	}
}

// What a fresh context keeps out is reasoning written at the head of an answer;
// the answer's own text, a tag in the middle of it included, stays.
func TestWithoutReasoning(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain answer", "plain answer"},
		{"<think>plan</think>answer", "answer"},
		{"  \n<think>plan</think>\n\nanswer", "answer"},
		{"<thinking>plan</thinking>answer", "answer"},
		{"<think>a</think><think>b</think>answer", "answer"},
		{"<think>a partial plan the model never closed", ""},
		{"use <think> to mark reasoning", "use <think> to mark reasoning"},
		{"", ""},
	} {
		if got := withoutReasoning(tc.in); got != tc.want {
			t.Errorf("withoutReasoning(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A fresh context is a new transcript: the one the turn was asked with, and the
// request already sent from it, are left as they were. An assistant turn that was
// nothing but reasoning says nothing to the next model and is left out; one that
// called a tool keeps the call its result answers.
func TestFreshContextIsACopy(t *testing.T) {
	msgs := []infer.ChatMessage{
		{Role: infer.RoleUser, Content: "<think>a user may type this</think>q"},
		{Role: infer.RoleAssistant, Content: "<think>only thinking</think>"},
		{Role: infer.RoleAssistant, Content: "<think>p</think>", ToolCalls: lookup("c1")},
		{Role: infer.RoleTool, ToolCallID: "c1", Content: "r"},
	}
	before := slices.Clone(msgs)
	got := freshContext(msgs)

	if !slices.EqualFunc(msgs, before, func(a, b infer.ChatMessage) bool { return a.Content == b.Content }) {
		t.Fatalf("freshContext changed the transcript it was given: %+v", msgs)
	}
	want := []infer.ChatMessage{
		{Role: infer.RoleUser, Content: "<think>a user may type this</think>q"},
		{Role: infer.RoleAssistant, Content: "", ToolCalls: lookup("c1")},
		{Role: infer.RoleTool, ToolCallID: "c1", Content: "r"},
	}
	if len(got) != len(want) {
		t.Fatalf("freshContext = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content || len(got[i].ToolCalls) != len(want[i].ToolCalls) {
			t.Fatalf("freshContext[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
