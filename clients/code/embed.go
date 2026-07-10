package code

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Embedder turns text into vectors for the semantic tier. It is an interface so
// the subsystem wires the real gateway client while tests inject a deterministic
// offline embedder — the semantic tier is fully testable without a live model.
type Embedder interface {
	// Embed returns one vector per input, aligned by index. A disabled embedder
	// returns (nil, nil) so indexing proceeds lexically + symbolically.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Enabled() bool
}

// gatewayEmbedder calls the Hanzo AI gateway's OpenAI-compatible /embeddings,
// the SAME endpoint + env (CLOUD_AI_BASE_URL / CLOUD_AI_API_KEY) clients/knowledge
// uses, so index and query share one model and dimensions always match. An empty
// key disables the tier (honest degrade, never a fabricated vector).
type gatewayEmbedder struct {
	base  string
	key   string
	model string
	http  *http.Client
}

func newEmbedder() *gatewayEmbedder {
	base := strings.TrimRight(getenv("CLOUD_AI_BASE_URL", "https://api.hanzo.ai/v1"), "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return &gatewayEmbedder{
		base:  base,
		key:   os.Getenv("CLOUD_AI_API_KEY"),
		model: getenv("CODE_EMBED_MODEL", "text-embedding-3-small"),
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *gatewayEmbedder) Enabled() bool { return e.key != "" }

// embedBatch bounds one request so a large index call fans out over several
// round-trips rather than one oversized body.
const embedBatch = 64

func (e *gatewayEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if !e.Enabled() || len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatch {
		end := start + embedBatch
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.embedOne(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *gatewayEmbedder) embedOne(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody, _ := json.Marshal(map[string]any{"model": e.model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/embeddings", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.key)

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("embeddings status %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	var decoded struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	if len(decoded.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(decoded.Data), len(texts))
	}
	sort.Slice(decoded.Data, func(i, j int) bool { return decoded.Data[i].Index < decoded.Data[j].Index })
	vecs := make([][]float32, len(decoded.Data))
	for i, d := range decoded.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

func getenv(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
