package agents

// route_test.go pins what a multi-hop model route is FOR.
//
// The route already had two hops in production and the second was unreachable.
// A hop that overran returned `context deadline exceeded`, failover treated
// anything but ErrUpstreamBusy as permanent, and the run failed — while the
// next hop in the same route answered in 9.9 seconds against the first one's
// 151. Two separate mistakes made that: the hops shared one deadline, so there
// was no time left to fall through WITH, and a timeout was not a reason to.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/infer"
)

// routeAI answers per model, and records what each hop was asked and how long
// it was given.
type routeAI struct {
	slow map[string]bool
	hard map[string]error

	seen  []string
	given []time.Duration
}

func (r *routeAI) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	r.seen = append(r.seen, req.Model)
	if dl, ok := ctx.Deadline(); ok {
		r.given = append(r.given, time.Until(dl))
	} else {
		r.given = append(r.given, 0)
	}
	if err := r.hard[req.Model]; err != nil {
		return nil, err
	}
	if r.slow[req.Model] {
		return nil, context.DeadlineExceeded
	}
	return &infer.ChatResponse{Content: "answered by " + req.Model}, nil
}

func (r *routeAI) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, nil }
func (r *routeAI) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

// A hop that runs out of its own time falls through to the next. This is the
// production failure, spelled as a test.
func TestSlowHopFallsThrough(t *testing.T) {
	ai := &routeAI{slow: map[string]bool{"slow": true}}
	resp, used, err := completeWithFailover(context.Background(), ai,
		&infer.ChatRequest{Model: "slow"}, "quick", nil)
	if err != nil {
		t.Fatalf("the route failed while a hop in it could answer: %v", err)
	}
	if used != "quick" {
		t.Fatalf("answered by %q, want the second hop", used)
	}
	if resp.Content != "answered by quick" {
		t.Fatalf("got %q", resp.Content)
	}
	if len(ai.seen) != 2 || ai.seen[0] != "slow" || ai.seen[1] != "quick" {
		t.Fatalf("hops tried: %v, want [slow quick] in order", ai.seen)
	}
}

// And each hop gets a FULL budget. Sharing one is what left no time to fall
// through with, so a route whose first hop is slow would still fail.
func TestEachHopGetsItsOwnBudget(t *testing.T) {
	ai := &routeAI{slow: map[string]bool{"slow": true}}
	if _, _, err := completeWithFailover(context.Background(), ai,
		&infer.ChatRequest{Model: "slow"}, "quick", nil); err != nil {
		t.Fatal(err)
	}
	if len(ai.given) != 2 {
		t.Fatalf("want two hops, got %d", len(ai.given))
	}
	for i, d := range ai.given {
		if d > modelBudget || d < modelBudget-5*time.Second {
			t.Errorf("hop %d was given %v, want a fresh modelBudget %v", i+1, d, modelBudget)
		}
	}
}

// A hard error still fails fast. No other hop would answer a malformed request
// or a bad credential differently, and trying is latency spent on a certainty.
func TestHardErrorDoesNotFailOver(t *testing.T) {
	boom := errors.New("invalid request: unknown field")
	ai := &routeAI{hard: map[string]error{"first": boom}}
	if _, _, err := completeWithFailover(context.Background(), ai,
		&infer.ChatRequest{Model: "first"}, "second", nil); !errors.Is(err, boom) {
		t.Fatalf("got %v, want the hard error", err)
	}
	if len(ai.seen) != 1 {
		t.Fatalf("tried %v; a hard error must not walk the route", ai.seen)
	}
}

// A cancelled CALLER ends the route. Falling through after the request went
// away would spend a second model on an answer nobody is waiting for.
func TestCancelledCallerStopsTheRoute(t *testing.T) {
	ai := &routeAI{slow: map[string]bool{"slow": true}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := completeWithFailover(ctx, ai, &infer.ChatRequest{Model: "slow"}, "quick", nil); err == nil {
		t.Fatal("a cancelled caller was answered")
	}
	if len(ai.seen) > 1 {
		t.Fatalf("tried %v after cancellation; the route should have stopped", ai.seen)
	}
}
