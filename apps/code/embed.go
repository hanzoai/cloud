package code

import (
	"context"

	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/account"
)

// Embedder turns text into vectors for the semantic tier. It is an interface so
// the subsystem wires the real AI client while tests inject a deterministic
// offline embedder — the semantic tier is fully testable without a live model.
type Embedder interface {
	// Embed returns one vector per input, aligned by index, metered against the
	// (org, project) billing scope. A disabled embedder returns (nil, nil) so
	// indexing proceeds lexically + symbolically.
	Embed(ctx context.Context, org string, payer account.Account, project string, texts []string) ([][]float32, error)
	Enabled() bool
	// Space is the space its vectors are in before any is embedded (vec.Policy
	// of its model): the model revision, normalization and task prefixes the
	// org's index records with its first vector, and admits alone after that.
	// Embed is raw; a caller embeds through Space().Documents or Space().Query,
	// which put the prefixes on and shape the vectors.
	Space() vec.Space
}

// aiEmbedder routes embeddings through the shared cloud.AIClient (deps.AI) — the
// SAME org/project-aligned, metered, observed path chat runs through. There is
// deliberately NO embed-specific key or endpoint: one AI identity, one way. The
// gateway owns model routing; model is CLOUD_EMBED_MODEL (default bge-m3, the
// 1024-dim BAAI embedder the gateway serves).
type aiEmbedder struct {
	ai    infer.Client
	model string
	space vec.Space
}

func newEmbedder(ai infer.Client, model string) *aiEmbedder {
	if model == "" {
		// The gateway serves the SKU (zen-embedding), not the raw upstream name:
		// a request for "bge-m3" 400s ("model not available"), which silently left
		// the semantic tier empty (vectors:0). zen-embedding routes to bge-m3.
		model = env.Or("CLOUD_EMBED_MODEL", "zen-embedding")
	}
	return &aiEmbedder{ai: ai, model: model, space: vec.Policy(model)}
}

// Space is the deployment's embedding policy for this model (vec.Policy).
func (e *aiEmbedder) Space() vec.Space { return e.space }

// Enabled mirrors the synthesizer (a non-nil AI client). The disabled stub errors
// at call time, so indexing degrades to lexical + symbolic rather than
// fabricating vectors.
func (e *aiEmbedder) Enabled() bool { return e.ai != nil }

// Embed sends texts vec.BatchSize at a time, so a large index call fans out over
// several round trips rather than one oversized body, and refuses an answer that
// does not carry one vector per text.
func (e *aiEmbedder) Embed(ctx context.Context, org string, payer account.Account, project string, texts []string) ([][]float32, error) {
	if e.ai == nil || len(texts) == 0 {
		return nil, nil
	}
	return vec.Batch(texts, func(batch []string) ([][]float32, error) {
		return e.ai.Embed(ctx, &infer.EmbedRequest{
			Model:   e.model,
			Inputs:  batch,
			Org:     org,
			Payer:   payer,
			Project: project,
		})
	})
}
