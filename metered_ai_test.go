package cloud

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// recordingAI is a transport stub that records the request it received (so a test
// can assert the billing scope was forwarded) and returns a canned response.
type recordingAI struct {
	chatReq  *types.ChatRequest
	embedReq *types.EmbedRequest
	resp     *types.ChatResponse
}

func (r *recordingAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	r.chatReq = req
	return r.resp, nil
}

func (r *recordingAI) Embed(_ context.Context, req *types.EmbedRequest) ([][]float32, error) {
	r.embedReq = req
	return [][]float32{{1, 2, 3}}, nil
}

// listerAI ALSO implements types.ModelLister — the capability that must survive the
// metering wrap so agents' model-catalog validation keeps working.
type listerAI struct{}

func (listerAI) ChatCompletion(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{}, nil
}
func (listerAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }
func (listerAI) Models(context.Context) ([]string, error)                        { return []string{"m"}, nil }

// passthroughMetered builds a meteredAI whose meter is not Enabled (no commerce
// URL), so the wrapper is a transparent pass-through — the dev/un-provisioned
// posture that must never block a call.
func passthroughMetered(inner types.AIClient) *meteredAI {
	return &meteredAI{inner: inner, meter: &ResourceMeter{}, rate: defaultAIPriceUUSDPer1kTokens}
}

func TestEstTokens(t *testing.T) {
	cases := map[string]int{"": 0, "abcd": 1, "a": 1, "aaaaaaaa": 2}
	for in, want := range cases {
		if got := estTokens(in); got != want {
			t.Fatalf("estTokens(%q) = %d, want %d", in, got, want)
		}
	}
	if got := estTokens(); got != 0 {
		t.Fatalf("estTokens() = %d, want 0", got)
	}
	if got := estTokens("aaaa", "aaaa"); got != 2 {
		t.Fatalf("estTokens(2x4chars) = %d, want 2", got)
	}
}

func TestMeteredAI_Pricing(t *testing.T) {
	m := &meteredAI{rate: 2000} // 2000 micro-USD per 1000 tokens ($2 / 1M tokens).
	if got := m.micros(1000); got != 2000 {
		t.Fatalf("micros(1000) = %d, want 2000", got)
	}
	// 2000 micro-USD is 0.2 cents → the conservative gate rounds UP to 1 cent.
	if got := m.cents(1000); got != 1 {
		t.Fatalf("cents(1000) = %d, want 1 (round up)", got)
	}
	// 10_000 tokens → 20_000 micro-USD = 2 cents exactly.
	if got := m.cents(10000); got != 2 {
		t.Fatalf("cents(10000) = %d, want 2", got)
	}
	// rate 0 = free/un-metered.
	free := &meteredAI{rate: 0}
	if free.micros(1000) != 0 || free.cents(1000) != 0 {
		t.Fatalf("rate 0 must price to zero (free)")
	}
}

// The wrapper forwards the call to the transport unchanged and returns its
// response, and the billing scope on the request reaches the transport (so the
// gen_ai span + any downstream carry it).
func TestMeteredAI_PassThroughForwardsScope(t *testing.T) {
	inner := &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 42}}
	m := passthroughMetered(inner)

	resp, err := m.ChatCompletion(context.Background(), &types.ChatRequest{Model: "x", Prompt: "yo", Org: "acme", Project: "p1"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "hi" {
		t.Fatalf("content = %q, want hi", resp.Content)
	}
	if inner.chatReq == nil || inner.chatReq.Org != "acme" || inner.chatReq.Project != "p1" {
		t.Fatalf("billing scope not forwarded to transport: %+v", inner.chatReq)
	}

	vecs, err := m.Embed(context.Background(), &types.EmbedRequest{Model: "bge-m3", Inputs: []string{"a"}, Org: "acme"})
	if err != nil || len(vecs) != 1 {
		t.Fatalf("embed: err=%v vecs=%d", err, len(vecs))
	}
	if inner.embedReq == nil || inner.embedReq.Org != "acme" {
		t.Fatalf("embed scope not forwarded: %+v", inner.embedReq)
	}
}

// A system call (no billing org) is executed but never gated or debited — there is
// no customer to bill — and must not error.
func TestMeteredAI_SystemCallNotMetered(t *testing.T) {
	inner := &recordingAI{resp: &types.ChatResponse{Content: "ok"}}
	m := passthroughMetered(inner)
	if _, err := m.ChatCompletion(context.Background(), &types.ChatRequest{Model: "x", Prompt: "sys", Org: ""}); err != nil {
		t.Fatalf("system call errored: %v", err)
	}
}

// The optional ModelLister capability must survive the metering wrap, or agents'
// model-catalog validation silently breaks.
func TestMeteredAI_ModelListerPreserved(t *testing.T) {
	wrapped := meteredAIClient(listerAI{}, Deps{})
	if _, ok := wrapped.(types.ModelLister); !ok {
		t.Fatalf("ModelLister capability lost through the metering wrap")
	}
	// A plain transport (no Models) must NOT gain a ModelLister.
	plain := meteredAIClient(&recordingAI{}, Deps{})
	if _, ok := plain.(types.ModelLister); ok {
		t.Fatalf("metering wrap fabricated a ModelLister on a non-lister transport")
	}
}
