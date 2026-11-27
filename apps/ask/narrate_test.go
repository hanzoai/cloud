package ask

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/infer"
)

// stuckAI is a model that never answers until its caller gives up, and records that
// the caller did.
type stuckAI struct {
	webAI
	gaveUp chan struct{}
}

func (s *stuckAI) ChatCompletion(ctx context.Context, _ *infer.ChatRequest) (*infer.ChatResponse, error) {
	<-ctx.Done()
	close(s.gaveUp)
	return nil, ctx.Err()
}

// A narration that does not come is given up on, and the figures are still answered
// by the template: the request's own context never ends, so without the bound the
// advisor waits on the model for as long as it takes.
func TestANarrationThatDoesNotComeIsGivenUpOn(t *testing.T) {
	was := narrateWait
	narrateWait = 50 * time.Millisecond
	t.Cleanup(func() { narrateWait = was })

	ai := &stuckAI{gaveUp: make(chan struct{})}
	app := newAskApp(t, ai, byOrg{"acme": money("$4,200")}, nil, nil)
	code, r := ask(t, app, "acme", "what is my MRR?")
	if code != 200 || !strings.Contains(r.Answer, "$4,200") {
		t.Fatalf("the template must answer when the model does not: code=%d answer=%q", code, r.Answer)
	}
	select {
	case <-ai.gaveUp:
	case <-time.After(time.Second):
		t.Fatal("the model call was never given up on")
	}
}
