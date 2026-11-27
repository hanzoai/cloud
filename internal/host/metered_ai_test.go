package host

import (
	"context"
	"github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/sdk/infer"
)

// recordingAI is a transport stub that records the request it received (so a test
// can assert the billing scope was forwarded), returns a canned response, and tells
// the call's price as the plane does.
type recordingAI struct {
	chatReq   *infer.ChatRequest
	embedReq  *infer.EmbedRequest
	rerankReq *infer.RerankRequest
	resp      *infer.ChatResponse
}

func (r *recordingAI) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	r.chatReq = req
	tellTokens(ctx, tokensOf(r.resp))
	return r.resp, nil
}

func (r *recordingAI) Embed(ctx context.Context, req *infer.EmbedRequest) ([][]float32, error) {
	r.embedReq = req
	tellTokens(ctx, cloud.EstTokens(req.Inputs...))
	return [][]float32{{1, 2, 3}}, nil
}

func (r *recordingAI) Rerank(ctx context.Context, req *infer.RerankRequest) ([]float64, error) {
	r.rerankReq = req
	tellTokens(ctx, cloud.EstTokens(append([]string{req.Query}, req.Documents...)...))
	return []float64{0.5, 0.9}, nil
}

// tellTokens prices tokens on ctx's Bill as the plane does for the model API.
func tellTokens(ctx context.Context, tokens int) {
	infer.BillOf(ctx).Tell(int64(tokens) * nanosPerToken)
}

// tokensOf is every token a response reports.
func tokensOf(r *infer.ChatResponse) int {
	if r == nil {
		return 0
	}
	return max(r.TotalTokens, r.PromptTokens+r.CompletionTokens)
}

// nanosPerToken is what the stub transports below price a token at, prompt and
// completion alike: $2 a million, as the model API tells it on the call's Bill.
const nanosPerToken = 2_000
