package link

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// fakeAI is a types.AIClient that records the credential it saw on the context — to
// prove the adapter delivers the routed credential to the egress via the carrier,
// never via a serialized field.
type fakeAI struct {
	sawToken   string
	sawAccount string
	resp       *types.ChatResponse
	err        error
}

func (f *fakeAI) ChatCompletion(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	if c, ok := CredentialFrom(ctx); ok {
		f.sawToken = c.Token
	}
	if a, ok := AccountFrom(ctx); ok {
		f.sawAccount = a.String()
	}
	return f.resp, f.err
}
func (f *fakeAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }

func TestCarrierRoundTrip(t *testing.T) {
	ctx := WithAccount(WithCredential(context.Background(), Credential{Token: "sec"}), Account{"openai", "work"})
	if c, ok := CredentialFrom(ctx); !ok || c.Token != "sec" {
		t.Fatalf("credential did not round-trip: %+v ok=%v", c, ok)
	}
	if a, ok := AccountFrom(ctx); !ok || a.String() != "openai:work" {
		t.Fatalf("account did not round-trip: %+v", a)
	}
	// A bare context carries neither — the egress then uses its normal path.
	if _, ok := CredentialFrom(context.Background()); ok {
		t.Fatal("a bare context must carry no credential")
	}
}

func TestAIClientUpstreamThreadsCredential(t *testing.T) {
	ai := &fakeAI{resp: &types.ChatResponse{PromptTokens: 3, CompletionTokens: 7, TotalTokens: 10}}
	up := AIClientUpstream{AI: ai}
	res, err := up.Call(context.Background(), Credential{Token: "tok-abc"}, Account{"openai", "work"},
		Request{Model: "gpt-4o", Payload: &types.ChatRequest{Model: "gpt-4o"}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if ai.sawToken != "tok-abc" {
		t.Fatalf("egress did not receive the routed credential via context: %q", ai.sawToken)
	}
	if ai.sawAccount != "openai:work" {
		t.Fatalf("egress did not receive the account: %q", ai.sawAccount)
	}
	if res.TotalTokens != 10 || res.Model != "gpt-4o" {
		t.Fatalf("result mapping wrong: %+v", res)
	}
}

func TestAIClientUpstreamRejectsWrongPayload(t *testing.T) {
	up := AIClientUpstream{AI: &fakeAI{}}
	if _, err := up.Call(context.Background(), Credential{}, Account{"openai", "work"}, Request{Model: "x"}); err == nil {
		t.Fatal("a non-ChatRequest payload must be rejected")
	}
}

func TestAIClientUpstreamPropagatesQuota(t *testing.T) {
	ai := &fakeAI{err: errors.New("429 Too Many Requests: rate limit")}
	up := AIClientUpstream{AI: ai}
	_, err := up.Call(context.Background(), Credential{Token: "t"}, Account{"openai", "work"},
		Request{Payload: &types.ChatRequest{}})
	if !IsQuota(err) {
		t.Fatalf("a provider 429 must classify as quota so the router cycles, got: %v", err)
	}
}

func TestFeeFromEnvDefaultsZero(t *testing.T) {
	t.Setenv("LINK_BYO_FEE_UUSD_PER_1K", "")
	if got := feeFromEnv()(Result{TotalTokens: 1_000_000}); got != 0 {
		t.Fatalf("no-fee default must charge 0, got %d", got)
	}
	t.Setenv("LINK_BYO_FEE_UUSD_PER_1K", "2000") // $2 / 1M tokens
	if got := feeFromEnv()(Result{TotalTokens: 1_000_000}); got != 200 {
		t.Fatalf("2000 uUSD/1k over 1M tokens = 200c, got %d", got)
	}
}
