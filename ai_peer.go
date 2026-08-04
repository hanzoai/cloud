package cloud

// Inference asked of the `ai` app BY NAME.
//
// `ai` is a plugin of this same binary running as its own process, so the honest
// way to reach it is the plane — the same socket the meter debits over and the
// gate reads balances over. Addressing it that way removes the three settings
// that existed only to describe where our own code is: its base URL, a key to
// authenticate to ourselves with, and the token endpoint to mint that key from.
//
// It is chosen by what the binary IS, not by configuration: peerAI is used when
// this deployment carries the `ai` app (manifest.Apps), exactly as the host
// decides what to mount. A deployment that genuinely calls a REMOTE inference
// service still does — that is a different fact, and it keeps the HTTP client.

import (
	"context"

	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
)

// peerAI implements types.AIClient over the internal plane. It holds no address:
// Ask resolves `ai` from its name, and starts it if it is not yet listening.
type peerAI struct{}

var _ types.AIClient = peerAI{}

// aiApp is the app name the plane resolves to a socket. One spelling.
const aiApp = "ai"

func (peerAI) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	// The tenant travels as a VALUE on the call, not as an ambient fact: the
	// servant reads its data scope (BYO keys, RAG) from it, and a peer call has no
	// request behind it to infer one from.
	out, err := Ask[plane.ChatIn, plane.ChatOut](For(ctx, req.Org), aiApp, plane.AIChat, &plane.ChatIn{
		Model:     req.Model,
		Prompt:    req.Prompt,
		Org:       req.Org,
		Project:   req.Project,
		MaxTokens: req.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return &types.ChatResponse{}, nil
	}
	return &types.ChatResponse{
		Content:          out.Content,
		PromptTokens:     out.PromptTokens,
		CompletionTokens: out.CompletionTokens,
		TotalTokens:      out.TotalTokens,
	}, nil
}

func (peerAI) Embed(ctx context.Context, req *types.EmbedRequest) ([][]float32, error) {
	if req == nil || len(req.Inputs) == 0 {
		return nil, nil
	}
	out, err := Ask[plane.EmbedIn, plane.EmbedOut](For(ctx, req.Org), aiApp, plane.AIEmbed, &plane.EmbedIn{
		Model:   req.Model,
		Inputs:  req.Inputs,
		Org:     req.Org,
		Project: req.Project,
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return out.Vectors, nil
}
