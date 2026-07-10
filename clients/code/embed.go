package code

import (
	"context"
	"os"

	"github.com/hanzoai/cloud"
)

// Embedder turns text into vectors for the semantic tier. It is an interface so
// the subsystem wires the real AI client while tests inject a deterministic
// offline embedder — the semantic tier is fully testable without a live model.
type Embedder interface {
	// Embed returns one vector per input, aligned by index, metered against the
	// (org, project) billing scope. A disabled embedder returns (nil, nil) so
	// indexing proceeds lexically + symbolically.
	Embed(ctx context.Context, org, project string, texts []string) ([][]float32, error)
	Enabled() bool
}

// aiEmbedder routes embeddings through the shared cloud.AIClient (deps.AI) — the
// SAME org/project-aligned, metered, observed path chat runs through. There is
// deliberately NO embed-specific key or endpoint: one AI identity, one way. The
// gateway owns model routing; model is CLOUD_EMBED_MODEL (default bge-m3, the
// 1024-dim BAAI embedder the gateway serves).
type aiEmbedder struct {
	ai    cloud.AIClient
	model string
}

func newEmbedder(ai cloud.AIClient, model string) *aiEmbedder {
	if model == "" {
		model = getenv("CLOUD_EMBED_MODEL", "bge-m3")
	}
	return &aiEmbedder{ai: ai, model: model}
}

// Enabled mirrors the synthesizer (a non-nil AI client). The disabled stub errors
// at call time, so indexing degrades to lexical + symbolic rather than
// fabricating vectors.
func (e *aiEmbedder) Enabled() bool { return e.ai != nil }

// embedBatch bounds one request so a large index call fans out over several
// round-trips rather than one oversized body.
const embedBatch = 64

func (e *aiEmbedder) Embed(ctx context.Context, org, project string, texts []string) ([][]float32, error) {
	if e.ai == nil || len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatch {
		end := start + embedBatch
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.ai.Embed(ctx, &cloud.EmbedRequest{
			Model:   e.model,
			Inputs:  texts[start:end],
			Org:     org,
			Project: project,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func getenv(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}
