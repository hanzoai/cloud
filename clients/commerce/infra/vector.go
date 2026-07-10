// Package infra provides infrastructure clients.
//
// This file implements the Qdrant vector database client using the REST API.
// No gRPC dependency -- plain HTTP + JSON over port 6333.
package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// VectorConfig holds Qdrant configuration
type VectorConfig struct {
	// Enabled enables the vector service
	Enabled bool

	// Host is the Qdrant server host
	Host string

	// Port is the Qdrant HTTP port (default: 6333)
	Port int

	// APIKey for authentication (optional)
	APIKey string

	// UseTLS enables TLS connection
	UseTLS bool

	// DefaultCollection is the default collection name
	DefaultCollection string

	// DefaultDimensions is the default vector dimensions (1536 for OpenAI)
	DefaultDimensions uint64
}

// VectorClient wraps the Qdrant REST client
type VectorClient struct {
	config  *VectorConfig
	baseURL string
	client  *http.Client
}

// NewVectorClient creates a new Qdrant vector client over HTTP
func NewVectorClient(_ context.Context, cfg *VectorConfig) (*VectorClient, error) {
	if cfg.Port == 0 {
		cfg.Port = 6333
	}
	if cfg.DefaultDimensions == 0 {
		cfg.DefaultDimensions = 1536
	}
	if cfg.DefaultCollection == "" {
		cfg.DefaultCollection = "products"
	}

	scheme := "http"
	if cfg.UseTLS {
		scheme = "https"
	}

	return &VectorClient{
		config:  cfg,
		baseURL: fmt.Sprintf("%s://%s:%d", scheme, cfg.Host, cfg.Port),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// do executes an HTTP request against Qdrant REST API.
func (c *VectorClient) do(ctx context.Context, method, path string, body interface{}) (json.RawMessage, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.config.APIKey != "" {
		req.Header.Set("api-key", c.config.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("qdrant %s %s: %d: %s", method, path, resp.StatusCode, respBody)
	}

	// Qdrant REST responses wrap result in {"result": ..., "status": "ok", "time": ...}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Status interface{}     `json:"status"`
	}
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &envelope); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return envelope.Result, nil
}

// EnsureCollection creates a collection if it doesn't exist
func (c *VectorClient) EnsureCollection(ctx context.Context, name string, dimensions uint64) error {
	if dimensions == 0 {
		dimensions = c.config.DefaultDimensions
	}

	// Check if collection exists (GET returns 200 if exists, 404 if not)
	_, err := c.do(ctx, http.MethodGet, "/collections/"+name, nil)
	if err == nil {
		return nil // Collection exists
	}

	// Create collection
	body := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     dimensions,
			"distance": "Cosine",
		},
	}
	_, err = c.do(ctx, http.MethodPut, "/collections/"+name, body)
	if err != nil {
		return fmt.Errorf("failed to create collection: %w", err)
	}

	return nil
}

// Upsert inserts or updates vectors
func (c *VectorClient) Upsert(ctx context.Context, collection string, points []*VectorPoint) error {
	if collection == "" {
		collection = c.config.DefaultCollection
	}

	qdrantPoints := make([]map[string]interface{}, len(points))
	for i, p := range points {
		qdrantPoints[i] = map[string]interface{}{
			"id":      p.ID,
			"vector":  p.Vector,
			"payload": p.Payload,
		}
	}

	body := map[string]interface{}{
		"points": qdrantPoints,
	}

	_, err := c.do(ctx, http.MethodPut, "/collections/"+collection+"/points?wait=true", body)
	if err != nil {
		return fmt.Errorf("failed to upsert points: %w", err)
	}

	return nil
}

// Search performs vector similarity search
func (c *VectorClient) Search(ctx context.Context, opts *VectorSearchOpts) ([]VectorSearchResult, error) {
	if opts.Collection == "" {
		opts.Collection = c.config.DefaultCollection
	}
	if opts.Limit == 0 {
		opts.Limit = 10
	}

	body := map[string]interface{}{
		"vector":          opts.Vector,
		"limit":           opts.Limit,
		"with_payload":    true,
		"score_threshold": opts.MinScore,
	}

	if opts.Filter != nil {
		body["filter"] = buildRESTFilter(opts.Filter)
	}

	raw, err := c.do(ctx, http.MethodPost, "/collections/"+opts.Collection+"/points/search", body)
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}

	var hits []struct {
		ID      interface{}            `json:"id"`
		Score   float32                `json:"score"`
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &hits); err != nil {
		return nil, fmt.Errorf("unmarshal search results: %w", err)
	}

	results := make([]VectorSearchResult, len(hits))
	for i, h := range hits {
		results[i] = VectorSearchResult{
			ID:      fmt.Sprintf("%v", h.ID),
			Score:   h.Score,
			Payload: h.Payload,
		}
	}

	return results, nil
}

// Delete removes vectors by ID
func (c *VectorClient) Delete(ctx context.Context, collection string, ids []string) error {
	if collection == "" {
		collection = c.config.DefaultCollection
	}

	body := map[string]interface{}{
		"points": ids,
	}

	_, err := c.do(ctx, http.MethodPost, "/collections/"+collection+"/points/delete?wait=true", body)
	if err != nil {
		return fmt.Errorf("failed to delete points: %w", err)
	}

	return nil
}

// Health checks the Qdrant connection
func (c *VectorClient) Health(ctx context.Context) HealthStatus {
	start := time.Now()

	_, err := c.do(ctx, http.MethodGet, "/collections", nil)
	if err != nil {
		return HealthStatus{
			Healthy: false,
			Latency: time.Since(start),
			Error:   err.Error(),
		}
	}

	return HealthStatus{
		Healthy: true,
		Latency: time.Since(start),
	}
}

// Close is a no-op for the HTTP client (satisfies the interface).
func (c *VectorClient) Close() error {
	return nil
}

// VectorPoint represents a point to insert
type VectorPoint struct {
	ID      string
	Vector  []float32
	Payload map[string]interface{}
}

// VectorSearchOpts configures vector search
type VectorSearchOpts struct {
	Collection string
	Vector     []float32
	Limit      int
	MinScore   float32
	Filter     map[string]interface{}
}

// VectorSearchResult represents a search result
type VectorSearchResult struct {
	ID      string
	Score   float32
	Payload map[string]interface{}
}

// buildRESTFilter converts a flat key=value map to a Qdrant REST filter.
func buildRESTFilter(filter map[string]interface{}) map[string]interface{} {
	if filter == nil {
		return nil
	}

	conditions := make([]map[string]interface{}, 0, len(filter))
	for key, value := range filter {
		conditions = append(conditions, map[string]interface{}{
			"key": key,
			"match": map[string]interface{}{
				"value": fmt.Sprintf("%v", value),
			},
		})
	}

	return map[string]interface{}{
		"must": conditions,
	}
}
