// SPDX-License-Identifier: Apache-2.0 OR MIT

package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/types"
)

type aiBearerSlot struct{}

// WithAIBearer attaches the caller's validated IAM JWT bearer to ctx so the
// HTTP AI client forwards the caller's own identity to Hanzo AI / Engine.
func WithAIBearer(ctx context.Context, bearer string) context.Context {
	if strings.TrimSpace(bearer) == "" {
		return ctx
	}
	return context.WithValue(ctx, aiBearerSlot{}, strings.TrimSpace(bearer))
}

func aiBearerFrom(ctx context.Context) string {
	v, _ := ctx.Value(aiBearerSlot{}).(string)
	return v
}

// httpAI implements types.AIClient over Hanzo's canonical OpenAI-compatible
// /v1/chat/completions and /v1/embeddings endpoints (e.g. https://api.hanzo.ai
// or a local Hanzo Engine at http://127.0.0.1:<port>).
//
// Egress posture: only loopback or Hanzo-owned hosts are admitted unless the
// operator explicitly opts into CLOUD_AI_ALLOW_CUSTOM_HOST=true. Never falls
// back to external providers or synthetic answers on error.
type httpAI struct {
	base   string // normalized base ending in /v1
	apiKey string
	client *http.Client
	err    error
}

// AIHTTPAt constructs a fail-closed HTTP AI client targeting baseURL.
// baseURL may omit or include /v1 (both "https://api.hanzo.ai" and
// "https://api.hanzo.ai/v1" normalize to "https://api.hanzo.ai/v1").
func AIHTTPAt(baseURL, apiKey string) types.AIClient {
	return newHTTPClient(baseURL, apiKey, nil)
}

func newHTTPClient(baseURL, apiKey string, hc *http.Client) *httpAI {
	norm, err := normalizeAIBaseURL(baseURL)
	if hc == nil {
		hc = &http.Client{
			Timeout: 30 * time.Second,
			// Never follow redirects automatically so a response cannot
			// redirect a bearer-carrying request onto an external host.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &httpAI{
		base:   norm,
		apiKey: strings.TrimSpace(apiKey),
		client: hc,
		err:    err,
	}
}

func normalizeAIBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("ai http: empty base URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("ai http: invalid base URL %q", raw)
	}
	if !allowedAIHost(u.Hostname()) && !allowCustomAIHost() {
		return "", fmt.Errorf("ai http: host %q is not loopback or Hanzo-owned (set CLOUD_AI_ALLOW_CUSTOM_HOST=true for private custom endpoints)", u.Hostname())
	}
	p := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(p, "/v1") {
		p += "/v1"
	}
	u.Path = p
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func allowedAIHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	for _, suffix := range []string{".hanzo.ai", ".hanzo.network", ".hanzo.local", ".svc.cluster.local"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return h == "api.hanzo.ai" || h == "hanzo.ai"
}

func allowCustomAIHost() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLOUD_AI_ALLOW_CUSTOM_HOST"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

type chatCompletionReq struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResp struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error any `json:"error,omitempty"`
}

func (c *httpAI) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	if req == nil || strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("ai http: prompt is required")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, errors.New("ai http: model is required")
	}
	temp := 0.2
	body, err := json.Marshal(chatCompletionReq{
		Model:       model,
		Messages:    []chatMessage{{Role: "user", Content: req.Prompt}},
		Stream:      false,
		MaxTokens:   256,
		Temperature: &temp,
	})
	if err != nil {
		return nil, fmt.Errorf("ai http: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai http: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if tok := aiBearerFrom(ctx); tok != "" {
		httpReq.Header.Set("Authorization", "Bearer "+tok)
	} else if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if req.Org != "" {
		httpReq.Header.Set("X-Org-Id", req.Org)
	}
	if req.Project != "" {
		httpReq.Header.Set("X-Project-Id", req.Project)
	}

	res, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: ai http: %v", types.ErrUpstreamBusy, err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: ai http read: %v", types.ErrUpstreamBusy, err)
	}
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: ai upstream HTTP %d", types.ErrUpstreamBusy, res.StatusCode)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("ai upstream HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 240)])))
	}

	var parsed chatCompletionResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ai http: invalid JSON response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return nil, fmt.Errorf("%w: ai upstream returned empty completion", types.ErrUpstreamBusy)
	}
	return &types.ChatResponse{
		Content:          strings.TrimSpace(parsed.Choices[0].Message.Content),
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}, nil
}

type embedReqBody struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedRespBody struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (c *httpAI) Embed(ctx context.Context, req *types.EmbedRequest) ([][]float32, error) {
	if c.err != nil {
		return nil, c.err
	}
	if req == nil || len(req.Inputs) == 0 {
		return nil, errors.New("ai http: inputs are required")
	}
	body, err := json.Marshal(embedReqBody{Model: req.Model, Input: req.Inputs})
	if err != nil {
		return nil, fmt.Errorf("ai http: marshal embed: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai http: embed request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if tok := aiBearerFrom(ctx); tok != "" {
		httpReq.Header.Set("Authorization", "Bearer "+tok)
	} else if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if req.Org != "" {
		httpReq.Header.Set("X-Org-Id", req.Org)
	}

	res, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: ai http embed: %v", types.ErrUpstreamBusy, err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: ai http embed read: %v", types.ErrUpstreamBusy, err)
	}
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: ai embed upstream HTTP %d", types.ErrUpstreamBusy, res.StatusCode)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("ai embed upstream HTTP %d", res.StatusCode)
	}
	var parsed embedRespBody
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ai http: invalid embed JSON: %w", err)
	}
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
