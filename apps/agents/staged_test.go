package agents

// The opening hop of a large surface carries one small tool, not the catalog.
// These pin that conversation never pays for the catalog, that asking for it
// hands it over without spending a round, and that a small surface is simply
// offered.

import (
	"context"
	"fmt"
	"testing"

	"github.com/hanzoai/cloud/sdk/infer"
)

// offers records the tool names each completion was offered and replies from a
// script: a reply with Calls asks for tools, one without is the answer.
type offers struct {
	fakeAI
	seen   [][]string
	script []infer.ChatResponse
}

func (o *offers) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	names := make([]string, len(req.Tools))
	for i, t := range req.Tools {
		names[i] = t.Name
	}
	o.seen = append(o.seen, names)
	r := o.script[min(len(o.seen)-1, len(o.script)-1)]
	return &r, nil
}

func surface(n int) []infer.ToolDef {
	defs := make([]infer.ToolDef, n)
	for i := range defs {
		defs[i] = infer.ToolDef{Name: fmt.Sprintf("svc%d", i), Description: "a service"}
	}
	return defs
}

func TestConversationNeverPaysForTheCatalog(t *testing.T) {
	ai := &offers{script: []infer.ChatResponse{{Content: "hello"}}}
	resp, _, err, calls := completeWithTools(context.Background(), ai, "acme", "acme/u", nil, "m", "", surface(40), "run_x")
	if err != nil || resp == nil || resp.Content != "hello" || calls != 0 {
		t.Fatalf("a greeting is answered in one hop, got %+v calls=%d err=%v", resp, calls, err)
	}
	if len(ai.seen) != 1 || len(ai.seen[0]) != 1 || ai.seen[0][0] != loadTools.Name {
		t.Fatalf("the opening hop must carry only %q, saw %v", loadTools.Name, ai.seen)
	}
}

func TestAskingForToolsHandsOverTheCatalog(t *testing.T) {
	ai := &offers{script: []infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "c1", Name: loadTools.Name, Arguments: `{"why":"look up deploys"}`}}},
		{Content: "found it"},
	}}
	resp, _, err, calls := completeWithTools(context.Background(), ai, "acme", "acme/u", nil, "m", "", surface(40), "run_y")
	if err != nil || resp == nil || resp.Content != "found it" {
		t.Fatalf("got %+v err=%v", resp, err)
	}
	if calls != 0 {
		t.Fatalf("being handed the catalog is not a tool call, counted %d", calls)
	}
	if len(ai.seen) != 2 || len(ai.seen[1]) != 40 {
		t.Fatalf("the hop after the ask must carry the catalog, saw %d then %d tools", len(ai.seen[0]), len(ai.seen[len(ai.seen)-1]))
	}
}

func TestASmallSurfaceIsSimplyOffered(t *testing.T) {
	ai := &offers{script: []infer.ChatResponse{{Content: "ok"}}}
	if _, _, err, _ := completeWithTools(context.Background(), ai, "acme", "acme/u", nil, "m", "", surface(3), "run_z"); err != nil {
		t.Fatal(err)
	}
	if len(ai.seen[0]) != 3 {
		t.Fatalf("three tools are offered as they are, saw %v", ai.seen[0])
	}
}
