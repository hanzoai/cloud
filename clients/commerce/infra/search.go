// Package infra provides infrastructure clients.
//
// This file implements the Meilisearch client for full-text product search.
package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/search-go"
)

// SearchConfig holds Meilisearch configuration
type SearchConfig struct {
	// Enabled enables the search service
	Enabled bool

	// Host is the Meilisearch server URL
	Host string

	// APIKey for authentication
	APIKey string

	// DefaultIndex is the default search index
	DefaultIndex string

	// Timeout for requests
	Timeout time.Duration
}

// SearchClient wraps the Meilisearch client
type SearchClient struct {
	config *SearchConfig
	client meilisearch.ServiceManager
}

// NewSearchClient creates a new Meilisearch client
func NewSearchClient(ctx context.Context, cfg *SearchConfig) (*SearchClient, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.DefaultIndex == "" {
		cfg.DefaultIndex = "products"
	}

	client := meilisearch.New(cfg.Host, meilisearch.WithAPIKey(cfg.APIKey))

	// Verify connection
	if _, err := client.Health(); err != nil {
		return nil, fmt.Errorf("failed to connect to meilisearch: %w", err)
	}

	return &SearchClient{
		config: cfg,
		client: client,
	}, nil
}

// EnsureIndex creates an index if it doesn't exist
func (c *SearchClient) EnsureIndex(ctx context.Context, uid string, primaryKey string) error {
	_, err := c.client.GetIndex(uid)
	if err != nil {
		// Index doesn't exist, create it
		task, err := c.client.CreateIndex(&meilisearch.IndexConfig{
			Uid:        uid,
			PrimaryKey: primaryKey,
		})
		if err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}

		// Wait for task completion
		_, err = c.client.WaitForTask(task.TaskUID, c.config.Timeout)
		if err != nil {
			return fmt.Errorf("failed waiting for index creation: %w", err)
		}
	}

	return nil
}

// ConfigureIndex updates index settings
func (c *SearchClient) ConfigureIndex(ctx context.Context, uid string, settings *IndexSettings) error {
	index := c.client.Index(uid)

	meiliSettings := &meilisearch.Settings{
		SearchableAttributes: settings.SearchableAttributes,
		FilterableAttributes: settings.FilterableAttributes,
		SortableAttributes:   settings.SortableAttributes,
		RankingRules:         settings.RankingRules,
		StopWords:            settings.StopWords,
		Synonyms:             settings.Synonyms,
	}

	if settings.DistinctAttribute != "" {
		meiliSettings.DistinctAttribute = &settings.DistinctAttribute
	}

	task, err := index.UpdateSettings(meiliSettings)
	if err != nil {
		return fmt.Errorf("failed to update settings: %w", err)
	}

	_, err = c.client.WaitForTask(task.TaskUID, c.config.Timeout)
	if err != nil {
		return fmt.Errorf("failed waiting for settings update: %w", err)
	}

	return nil
}

// Index adds or updates documents in an index
func (c *SearchClient) Index(ctx context.Context, indexUID string, documents interface{}, primaryKey ...string) error {
	if indexUID == "" {
		indexUID = c.config.DefaultIndex
	}

	index := c.client.Index(indexUID)

	var opts *meilisearch.DocumentOptions
	if len(primaryKey) > 0 {
		pk := primaryKey[0]
		opts = &meilisearch.DocumentOptions{PrimaryKey: &pk}
	}

	task, err := index.AddDocuments(documents, opts)

	if err != nil {
		return fmt.Errorf("failed to index documents: %w", err)
	}

	// Wait for indexing to complete
	_, err = c.client.WaitForTask(task.TaskUID, c.config.Timeout)
	if err != nil {
		return fmt.Errorf("failed waiting for indexing: %w", err)
	}

	return nil
}

// Search performs a search query
func (c *SearchClient) Search(ctx context.Context, opts *SearchOptions) (*SearchResult, error) {
	if opts.Index == "" {
		opts.Index = c.config.DefaultIndex
	}

	index := c.client.Index(opts.Index)

	req := &meilisearch.SearchRequest{
		Limit:                 int64(opts.Limit),
		Offset:                int64(opts.Offset),
		Filter:                opts.Filter,
		Sort:                  opts.Sort,
		AttributesToRetrieve:  opts.AttributesToRetrieve,
		AttributesToCrop:      opts.AttributesToCrop,
		CropLength:            int64(opts.CropLength),
		AttributesToHighlight: opts.AttributesToHighlight,
		HighlightPreTag:       opts.HighlightPreTag,
		HighlightPostTag:      opts.HighlightPostTag,
		ShowMatchesPosition:   opts.ShowMatchesPosition,
		Facets:                opts.Facets,
	}

	resp, err := index.Search(opts.Query, req)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Convert hits to []interface{}
	hits := make([]interface{}, len(resp.Hits))
	for i, h := range resp.Hits {
		// Convert meilisearch.Hit (map[string]json.RawMessage) to map[string]interface{}
		m := make(map[string]interface{})
		if err := h.DecodeInto(&m); err == nil {
			hits[i] = m
		} else {
			hits[i] = h
		}
	}

	// Convert facet distribution from json.RawMessage
	var facetDist map[string]interface{}
	if resp.FacetDistribution != nil && len(resp.FacetDistribution) > 0 {
		// Parse the raw JSON into map
		_ = json.Unmarshal(resp.FacetDistribution, &facetDist)
	}

	result := &SearchResult{
		Hits:              hits,
		NbHits:            resp.EstimatedTotalHits,
		Offset:            int(resp.Offset),
		Limit:             int(resp.Limit),
		ProcessingTimeMs:  resp.ProcessingTimeMs,
		Query:             resp.Query,
		FacetDistribution: facetDist,
	}

	return result, nil
}

// Delete removes documents from an index
func (c *SearchClient) Delete(ctx context.Context, indexUID string, documentIDs []string) error {
	if indexUID == "" {
		indexUID = c.config.DefaultIndex
	}

	index := c.client.Index(indexUID)

	task, err := index.DeleteDocuments(documentIDs, (*meilisearch.DocumentOptions)(nil))
	if err != nil {
		return fmt.Errorf("failed to delete documents: %w", err)
	}

	_, err = c.client.WaitForTask(task.TaskUID, c.config.Timeout)
	if err != nil {
		return fmt.Errorf("failed waiting for deletion: %w", err)
	}

	return nil
}

// DeleteAll removes all documents from an index
func (c *SearchClient) DeleteAll(ctx context.Context, indexUID string) error {
	if indexUID == "" {
		indexUID = c.config.DefaultIndex
	}

	index := c.client.Index(indexUID)

	task, err := index.DeleteAllDocuments((*meilisearch.DocumentOptions)(nil))
	if err != nil {
		return fmt.Errorf("failed to delete all documents: %w", err)
	}

	_, err = c.client.WaitForTask(task.TaskUID, c.config.Timeout)
	if err != nil {
		return fmt.Errorf("failed waiting for deletion: %w", err)
	}

	return nil
}

// GetDocument retrieves a document by ID
func (c *SearchClient) GetDocument(ctx context.Context, indexUID, documentID string, dst interface{}) error {
	if indexUID == "" {
		indexUID = c.config.DefaultIndex
	}

	index := c.client.Index(indexUID)

	err := index.GetDocument(documentID, nil, dst)
	if err != nil {
		return fmt.Errorf("failed to get document: %w", err)
	}

	return nil
}

// GetDocuments retrieves multiple documents
func (c *SearchClient) GetDocuments(ctx context.Context, indexUID string, opts *GetDocumentsOptions) (*DocumentsResult, error) {
	if indexUID == "" {
		indexUID = c.config.DefaultIndex
	}

	index := c.client.Index(indexUID)

	req := &meilisearch.DocumentsQuery{
		Limit:  int64(opts.Limit),
		Offset: int64(opts.Offset),
		Fields: opts.Fields,
	}

	var resp meilisearch.DocumentsResult
	err := index.GetDocuments(req, &resp)
	if err != nil {
		return nil, fmt.Errorf("failed to get documents: %w", err)
	}

	// Convert Results from meilisearch.Hits to []map[string]interface{}
	results := make([]map[string]interface{}, len(resp.Results))
	for i, hit := range resp.Results {
		m := make(map[string]interface{})
		if err := hit.DecodeInto(&m); err != nil {
			return nil, fmt.Errorf("failed to decode document %d: %w", i, err)
		}
		results[i] = m
	}

	return &DocumentsResult{
		Results: results,
		Offset:  int(resp.Offset),
		Limit:   int(resp.Limit),
		Total:   int(resp.Total),
	}, nil
}

// Stats returns index statistics
func (c *SearchClient) Stats(ctx context.Context, indexUID string) (*IndexStats, error) {
	if indexUID == "" {
		indexUID = c.config.DefaultIndex
	}

	index := c.client.Index(indexUID)

	stats, err := index.GetStats()
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}

	return &IndexStats{
		NumberOfDocuments: int(stats.NumberOfDocuments),
		IsIndexing:        stats.IsIndexing,
		FieldDistribution: stats.FieldDistribution,
	}, nil
}

// Health checks the Meilisearch connection
func (c *SearchClient) Health(ctx context.Context) HealthStatus {
	start := time.Now()

	_, err := c.client.Health()
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

// Client returns the underlying Meilisearch client for advanced operations
func (c *SearchClient) Client() meilisearch.ServiceManager {
	return c.client
}

// IndexSettings configures a search index
type IndexSettings struct {
	SearchableAttributes []string
	FilterableAttributes []string
	SortableAttributes   []string
	RankingRules         []string
	StopWords            []string
	Synonyms             map[string][]string
	DistinctAttribute    string
}

// SearchOptions configures a search query
type SearchOptions struct {
	Index                 string
	Query                 string
	Offset                int
	Limit                 int
	Filter                interface{}
	Sort                  []string
	Facets                []string
	AttributesToRetrieve  []string
	AttributesToCrop      []string
	CropLength            int
	AttributesToHighlight []string
	HighlightPreTag       string
	HighlightPostTag      string
	ShowMatchesPosition   bool
}

// SearchResult contains search results
type SearchResult struct {
	Hits              []interface{}
	NbHits            int64
	Offset            int
	Limit             int
	ProcessingTimeMs  int64
	Query             string
	FacetDistribution map[string]interface{}
}

// GetDocumentsOptions configures document retrieval
type GetDocumentsOptions struct {
	Offset int
	Limit  int
	Fields []string
}

// DocumentsResult contains document retrieval results
type DocumentsResult struct {
	Results []map[string]interface{}
	Offset  int
	Limit   int
	Total   int
}

// IndexStats contains index statistics
type IndexStats struct {
	NumberOfDocuments int
	IsIndexing        bool
	FieldDistribution map[string]int64
}
