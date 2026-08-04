package ai

// Inference, answered on the internal plane.
//
// `ai` is a plugin of this same binary running as its own process. A sibling
// that wants a completion had exactly one way to get it: the PUBLIC gateway —
// so the pod left through Cloudflare, minted an OAuth token to authenticate to
// its own deployment, and came back to reach code one socket away. Every part of
// that round trip was a consequence of addressing a peer by URL.
//
// These two ops are addressed by APP NAME (zip.SocketPath), which is the same
// way the meter reaches commerce and the gate reaches the ledger. There is no
// address to configure, no credential to mint, and no second answer to the
// question of where `ai` is.
//
// Billing is deliberately NOT here. The caller meters before it asks (metered_ai
// reserves, then settles on the reported usage), so pricing the call again on
// this side would bill it twice.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// servePlane publishes chat + embed on this app's socket. Mount calls it.
func servePlane(ai types.AIClient) {
	p := cloud.Plane()
	o := inference{ai: ai}

	zip.Post[plane.ChatIn, plane.ChatOut](p, "/ai/chat", o.chat,
		zip.WithOperationID(plane.AIChat),
		zip.WithSummary("Complete one prompt"))

	zip.Post[plane.EmbedIn, plane.EmbedOut](p, "/ai/embed", o.embed,
		zip.WithOperationID(plane.AIEmbed),
		zip.WithSummary("Embed one batch of inputs"))
}

type inference struct{ ai types.AIClient }

func (o inference) chat(ctx context.Context, in *plane.ChatIn) (*plane.ChatOut, error) {
	resp, err := o.ai.ChatCompletion(ctx, &types.ChatRequest{
		Model:     in.Model,
		Prompt:    in.Prompt,
		Org:       in.Org,
		Project:   in.Project,
		MaxTokens: in.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return &plane.ChatOut{}, nil
	}
	return &plane.ChatOut{
		Content:          resp.Content,
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
		TotalTokens:      resp.TotalTokens,
	}, nil
}

func (o inference) embed(ctx context.Context, in *plane.EmbedIn) (*plane.EmbedOut, error) {
	vecs, err := o.ai.Embed(ctx, &types.EmbedRequest{
		Model:   in.Model,
		Inputs:  in.Inputs,
		Org:     in.Org,
		Project: in.Project,
	})
	if err != nil {
		return nil, err
	}
	return &plane.EmbedOut{Vectors: vecs}, nil
}
