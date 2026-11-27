package agents

// tools_budget_test.go pins the deadlines a tool loop actually imposes.
//
// There are three, and the bug they replaced was that there was one. Eight
// rounds shared a single four-minute budget, so a model that answered in a
// second left plenty and a model that took forty seconds to its first token
// exhausted the run in six — and the error, `chat completion: context deadline
// exceeded`, named the completion while the deadline belonged to the loop.
// Enso failed exactly this way in production: cold it took 39.5s to return
// sixteen tokens, warm it took 0.97s, so the same question failed or answered
// depending on nothing the asker did.
//
// The three ask different questions and must not collapse back into one:
// modelBudget asks whether the model is answering, toolCallTimeout whether a
// connector is, toolRunBudget whether the whole thing has gone on too long.

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/infer"
)

// deadlineAI records the deadline each completion is handed, which is the only
// way to see a budget from outside: it is applied to a context, and a context's
// deadline is not otherwise observable.
//
// rounds is how many times it asks for a tool before answering, which is what
// puts more than one completion on the clock.
type deadlineAI struct {
	rounds int
	seen   []time.Duration
}

func (d *deadlineAI) ChatCompletion(ctx context.Context, _ *infer.ChatRequest) (*infer.ChatResponse, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.seen = append(d.seen, time.Until(dl))
	} else {
		d.seen = append(d.seen, 0) // no deadline at all, which is its own failure
	}
	if len(d.seen) <= d.rounds {
		return &infer.ChatResponse{
			ToolCalls:    []infer.ToolCall{{ID: "c1", Name: "weather", Arguments: "{}"}},
			FinishReason: "tool_calls",
		}, nil
	}
	return &infer.ChatResponse{Content: "answered"}, nil
}

func (d *deadlineAI) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

func (d *deadlineAI) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) {
	return nil, nil
}

// toolingAgent is an agent whose declared tool the plane offers, which is what
// puts a run on the tool-loop path rather than the single-completion one.
func toolingAgent(t *testing.T, rounds int) *deadlineAI {
	t.Helper()
	withPlane(t, &fakePlane{offer: []infer.ToolDef{toolDef("weather")}, out: "{}"})
	return &deadlineAI{rounds: rounds}
}

func drive(t *testing.T, ctx context.Context, ai *deadlineAI) Run {
	t.Helper()
	a := mk("maxpower", "greeter")
	a.Tools = []string{"weather"}
	return executeRun(ctx, ai, "maxpower", "maxpower/u1", a, "weather?", nil, "", "run_test")
}

// near reports whether a deadline is the given budget, allowing for the clock
// moving between the two reads. Asserting equality would fail on a slow machine
// for no reason.
func near(got, want time.Duration) bool { return got <= want && got > want-5*time.Second }

// A completion is bounded by modelBudget, not by the loop's ceiling. Handing a
// single call the whole run's budget is what let one slow completion spend the
// budget the remaining rounds also needed.
func TestCompletionGetsTheModelBudget(t *testing.T) {
	ai := toolingAgent(t, 0)
	if r := drive(t, context.Background(), ai); r.Status != "ok" {
		t.Fatalf("want ok, got %q err=%q", r.Status, r.Error)
	}
	if len(ai.seen) != 1 {
		t.Fatalf("want one completion, got %d", len(ai.seen))
	}
	if got := ai.seen[0]; !near(got, modelBudget) {
		t.Fatalf("the model was given %v, want modelBudget %v", got, modelBudget)
	}
}

// THE fix: every round gets a FULL model budget, not what the round before it
// left over. Under the old single budget each completion saw the run's whole
// remaining ceiling, so this is also what tells the two designs apart.
func TestEveryRoundGetsAFreshBudget(t *testing.T) {
	ai := toolingAgent(t, 3)
	if r := drive(t, context.Background(), ai); r.Status != "ok" {
		t.Fatalf("want ok, got %q err=%q", r.Status, r.Error)
	}
	if len(ai.seen) != 4 {
		t.Fatalf("want 3 tool rounds and a final answer, got %d completions", len(ai.seen))
	}
	for i, got := range ai.seen {
		if !near(got, modelBudget) {
			t.Fatalf("round %d was given %v, want a fresh modelBudget %v", i+1, got, modelBudget)
		}
	}
}

// A CALLER's tighter deadline still wins. A per-call budget that raised a
// caller's deadline would let one run outlive the request that asked for it.
func TestTighterCallerDeadlineWins(t *testing.T) {
	ai := toolingAgent(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	drive(t, ctx, ai)

	if len(ai.seen) != 1 {
		t.Fatalf("want one completion, got %d", len(ai.seen))
	}
	if got := ai.seen[0]; got > 40*time.Millisecond {
		t.Fatalf("the model was given %v, want the caller's tighter 40ms", got)
	}
}

// The bounds have to keep their order and their distance from the work. There
// is no way to watch a fifteen-minute ceiling expire in a unit test, so what is
// asserted is the relationship — which is what an edit would break.
func TestBoundsSitClearOfTheWork(t *testing.T) {
	// A local model can take two minutes to its FIRST TOKEN. A per-call bound
	// anywhere near that kills runs that were about to succeed, which is the
	// production failure this replaced.
	if modelBudget < 3*time.Minute {
		t.Errorf("modelBudget is %v; local TTFT reaches ~2m, so this cuts answers that were coming", modelBudget)
	}
	// A per-call bound at or above the ceiling is not a bound: the run would
	// die first and the model's deadline would never be the thing that fired.
	if modelBudget >= toolRunBudget {
		t.Errorf("modelBudget %v >= toolRunBudget %v, so the per-call bound never applies", modelBudget, toolRunBudget)
	}
	// The ceiling must clear a full loop of slow rounds, or it is not a
	// safeguard against a wedged agent — maxToolRounds and toolCallTimeout are
	// what bound one of those — it is a random failure on healthy work.
	if toolRunBudget < 3*modelBudget {
		t.Errorf("toolRunBudget %v leaves room for fewer than three slow rounds of %v", toolRunBudget, modelBudget)
	}
	// A connector must not be able to spend a completion's budget.
	if toolCallTimeout >= modelBudget {
		t.Errorf("toolCallTimeout %v is not tighter than modelBudget %v", toolCallTimeout, modelBudget)
	}
}
