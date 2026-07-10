// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package ai provides AI-powered product recommendations and embeddings
// via Hanzo Cloud-Backend (Rust inference API) and Hanzo Cloud (Go API).
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Default endpoints for Hanzo Cloud services.
// All inference routes through the Go cloud-api which handles native
// provider routing (DO-AI, Fireworks, OpenAI Direct). LiteLLM is removed.
const (
	DefaultCloudEndpoint  = "https://cloud.hanzo.ai/api"
	DefaultModel          = "gpt-4o"
	DefaultEmbeddingModel = "text-embedding-3-small"
	DefaultTimeout        = 30 * time.Second
)

// AIConfig holds configuration for the Hanzo AI services
type AIConfig struct {
	// Endpoint is the base URL for the Hanzo Cloud API.
	// All inference routes through cloud-api natively (no LiteLLM middleman).
	// Default: https://cloud.hanzo.ai/api
	Endpoint string `json:"endpoint"`

	// CloudEndpoint is an alias for Endpoint (kept for backward compatibility).
	CloudEndpoint string `json:"cloudEndpoint"`

	// APIKey is the Bearer token for authentication
	APIKey string `json:"apiKey"`

	// Model is the default model for chat completions
	// Options: gpt-4o, gpt-5, claude-opus-4-6, zen4-pro, zen4-coder-pro
	Model string `json:"model"`

	// EmbeddingModel is the model used for generating embeddings
	// Options: text-embedding-3-small, text-embedding-3-large, text-embedding-ada-002
	EmbeddingModel string `json:"embeddingModel"`

	// Temperature controls randomness in responses (0.0-2.0)
	Temperature float32 `json:"temperature"`

	// MaxTokens limits the response length
	MaxTokens int `json:"maxTokens"`

	// Timeout for API requests
	Timeout time.Duration `json:"timeout"`

	// GRPOEnabled enables Group Relative Policy Optimization for better responses
	GRPOEnabled bool `json:"grpoEnabled"`
}

// NewAIConfig creates a new AIConfig with defaults from environment variables.
// All inference now routes through the Go cloud-api (no LiteLLM middleman).
func NewAIConfig() *AIConfig {
	// Unified endpoint — HANZO_CLOUD_ENDPOINT takes priority, then
	// HANZO_AI_ENDPOINT for backward compat, then default.
	endpoint := os.Getenv("HANZO_CLOUD_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("HANZO_AI_ENDPOINT")
	}
	if endpoint == "" {
		endpoint = DefaultCloudEndpoint
	}

	apiKey := os.Getenv("HANZO_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("HANZO_AI_API_KEY")
	}

	model := os.Getenv("HANZO_AI_MODEL")
	if model == "" {
		model = DefaultModel
	}

	embeddingModel := os.Getenv("HANZO_EMBEDDING_MODEL")
	if embeddingModel == "" {
		embeddingModel = DefaultEmbeddingModel
	}

	return &AIConfig{
		Endpoint:       endpoint,
		CloudEndpoint:  endpoint,
		APIKey:         apiKey,
		Model:          model,
		EmbeddingModel: embeddingModel,
		Temperature:    0.7,
		MaxTokens:      2048,
		Timeout:        DefaultTimeout,
		GRPOEnabled:    false,
	}
}

// Client is the AI client for Hanzo Cloud services
type Client struct {
	config     *AIConfig
	httpClient *http.Client
}

// NewClient creates a new AI client with the given configuration
func NewClient(config *AIConfig) *Client {
	if config == nil {
		config = NewAIConfig()
	}

	timeout := config.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	return &Client{
		config: config,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// ChatMessage represents a message in a chat completion request
type ChatMessage struct {
	Role    string `json:"role"`    // system, user, assistant
	Content string `json:"content"` // message content
}

// ChatCompletionRequest is the request body for chat completions
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float32      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	GRPOEnabled bool          `json:"grpo_enabled,omitempty"`
	Groundtruth *string       `json:"groundtruth,omitempty"`
}

// ChatCompletionResponse is the response from chat completions
type ChatCompletionResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`
	// GRPO metadata when enabled
	GRPOMetadata *GRPOMetadata `json:"grpo_metadata,omitempty"`
}

// ChatChoice represents a single completion choice
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// Usage tracks token usage for billing
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// GRPOMetadata contains GRPO-specific response metadata
type GRPOMetadata struct {
	ExperiencesUsed []string `json:"experiences_used"`
	GroupSize       int      `json:"group_size"`
	BestReward      float64  `json:"best_reward"`
	AvgReward       float64  `json:"avg_reward"`
}

// EmbeddingRequest is the request body for embeddings
type EmbeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// EmbeddingResponse is the response from embeddings
type EmbeddingResponse struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  EmbeddingUsage  `json:"usage"`
}

// EmbeddingData contains a single embedding vector
type EmbeddingData struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// EmbeddingUsage tracks token usage for embeddings
type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ProductInfo represents product data for recommendations
type ProductInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Price       float64  `json:"price"`
	Tags        []string `json:"tags"`
	SKU         string   `json:"sku,omitempty"`
}

// RecommendationRequest is the request for product recommendations
type RecommendationRequest struct {
	UserID           string        `json:"userId"`
	Products         []ProductInfo `json:"products"`
	PurchaseHistory  []string      `json:"purchaseHistory,omitempty"`
	BrowsingHistory  []string      `json:"browsingHistory,omitempty"`
	MaxResults       int           `json:"maxResults,omitempty"`
	IncludeReasoning bool          `json:"includeReasoning,omitempty"`
}

// Recommendation represents a single product recommendation
type Recommendation struct {
	ProductID  string  `json:"productId"`
	Score      float64 `json:"score"`
	Reasoning  string  `json:"reasoning,omitempty"`
	Category   string  `json:"category,omitempty"`
	Similarity float64 `json:"similarity,omitempty"`
}

// RecommendationResponse is the response for product recommendations
type RecommendationResponse struct {
	UserID          string           `json:"userId"`
	Recommendations []Recommendation `json:"recommendations"`
	GeneratedAt     time.Time        `json:"generatedAt"`
	Model           string           `json:"model"`
}

// CustomerSupportRequest is the request for customer support AI
type CustomerSupportRequest struct {
	UserID       string        `json:"userId,omitempty"`
	Message      string        `json:"message"`
	Context      string        `json:"context,omitempty"`
	History      []ChatMessage `json:"history,omitempty"`
	OrderContext *OrderContext `json:"orderContext,omitempty"`
}

// OrderContext provides order information for support queries
type OrderContext struct {
	OrderID     string    `json:"orderId"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	TotalAmount float64   `json:"totalAmount"`
	Items       []string  `json:"items"`
}

// CustomerSupportResponse is the response from customer support AI
type CustomerSupportResponse struct {
	Response      string   `json:"response"`
	SuggestedNext []string `json:"suggestedNext,omitempty"`
	Sentiment     string   `json:"sentiment,omitempty"`
	RequiresHuman bool     `json:"requiresHuman"`
	Confidence    float64  `json:"confidence"`
}

// APIError represents an error from the API
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("AI API error %d: %s - %s", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("AI API error %d: %s", e.Code, e.Message)
}

// doRequest performs an HTTP request to the API
func (c *Client) doRequest(ctx context.Context, method, url string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(respBody, &apiErr); err != nil {
			return nil, &APIError{
				Code:    resp.StatusCode,
				Message: string(respBody),
			}
		}
		apiErr.Code = resp.StatusCode
		return nil, &apiErr
	}

	return respBody, nil
}

// ChatCompletion sends a chat completion request to the API
func (c *Client) ChatCompletion(ctx context.Context, messages []ChatMessage) (*ChatCompletionResponse, error) {
	model := c.config.Model
	if model == "" {
		model = DefaultModel
	}

	req := ChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Temperature: &c.config.Temperature,
		MaxTokens:   &c.config.MaxTokens,
		GRPOEnabled: c.config.GRPOEnabled,
	}

	return c.ChatCompletionWithRequest(ctx, req)
}

// ChatCompletionWithRequest sends a custom chat completion request
func (c *Client) ChatCompletionWithRequest(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	url := c.config.Endpoint + "/chat/completions"

	respBody, err := c.doRequest(ctx, "POST", url, req)
	if err != nil {
		return nil, err
	}

	var response ChatCompletionResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response, nil
}

// GetEmbedding generates an embedding vector for the given text
func (c *Client) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := c.GetEmbeddings(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, errors.New("no embeddings returned")
	}
	return embeddings[0], nil
}

// GetEmbeddings generates embedding vectors for multiple texts
func (c *Client) GetEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	model := c.config.EmbeddingModel
	if model == "" {
		model = DefaultEmbeddingModel
	}

	req := EmbeddingRequest{
		Input: texts,
		Model: model,
	}

	url := c.config.Endpoint + "/embeddings"

	respBody, err := c.doRequest(ctx, "POST", url, req)
	if err != nil {
		return nil, err
	}

	var response EmbeddingResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embedding response: %w", err)
	}

	embeddings := make([][]float32, len(response.Data))
	for _, data := range response.Data {
		embeddings[data.Index] = data.Embedding
	}

	return embeddings, nil
}

// GetRecommendations generates AI-powered product recommendations
func (c *Client) GetRecommendations(ctx context.Context, userID string, products []ProductInfo) (*RecommendationResponse, error) {
	req := RecommendationRequest{
		UserID:           userID,
		Products:         products,
		MaxResults:       10,
		IncludeReasoning: true,
	}
	return c.GetRecommendationsWithRequest(ctx, req)
}

// GetRecommendationsWithRequest generates recommendations with a custom request
func (c *Client) GetRecommendationsWithRequest(ctx context.Context, req RecommendationRequest) (*RecommendationResponse, error) {
	if len(req.Products) == 0 {
		return nil, errors.New("products list cannot be empty")
	}

	if req.MaxResults <= 0 {
		req.MaxResults = 10
	}

	// Build the recommendation prompt
	prompt := buildRecommendationPrompt(req)

	// Use chat completion to generate recommendations
	messages := []ChatMessage{
		{
			Role: "system",
			Content: `You are an expert e-commerce recommendation engine. Analyze user behavior and product catalog to generate personalized product recommendations.

Output your recommendations as a JSON array with the following structure:
[
  {
    "productId": "string",
    "score": number (0-1),
    "reasoning": "string explaining why this product is recommended",
    "category": "string"
  }
]

Only output the JSON array, no additional text.`,
		},
		{
			Role:    "user",
			Content: prompt,
		},
	}

	resp, err := c.ChatCompletion(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("failed to generate recommendations: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("no recommendations generated")
	}

	// Parse the AI response
	content := resp.Choices[0].Message.Content
	content = strings.TrimSpace(content)

	// Handle potential markdown code blocks
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var recommendations []Recommendation
	if err := json.Unmarshal([]byte(content), &recommendations); err != nil {
		return nil, fmt.Errorf("failed to parse recommendations: %w (content: %s)", err, content)
	}

	// Limit results
	if len(recommendations) > req.MaxResults {
		recommendations = recommendations[:req.MaxResults]
	}

	return &RecommendationResponse{
		UserID:          req.UserID,
		Recommendations: recommendations,
		GeneratedAt:     time.Now(),
		Model:           resp.Model,
	}, nil
}

// buildRecommendationPrompt creates the prompt for recommendations
func buildRecommendationPrompt(req RecommendationRequest) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Generate %d personalized product recommendations for user %s.\n\n", req.MaxResults, req.UserID))

	sb.WriteString("## Available Products:\n")
	for _, p := range req.Products {
		sb.WriteString(fmt.Sprintf("- ID: %s, Name: %s, Category: %s, Price: $%.2f\n", p.ID, p.Name, p.Category, p.Price))
		if p.Description != "" {
			sb.WriteString(fmt.Sprintf("  Description: %s\n", p.Description))
		}
		if len(p.Tags) > 0 {
			sb.WriteString(fmt.Sprintf("  Tags: %s\n", strings.Join(p.Tags, ", ")))
		}
	}

	if len(req.PurchaseHistory) > 0 {
		sb.WriteString("\n## User Purchase History:\n")
		for _, id := range req.PurchaseHistory {
			sb.WriteString(fmt.Sprintf("- %s\n", id))
		}
	}

	if len(req.BrowsingHistory) > 0 {
		sb.WriteString("\n## User Browsing History:\n")
		for _, id := range req.BrowsingHistory {
			sb.WriteString(fmt.Sprintf("- %s\n", id))
		}
	}

	sb.WriteString("\nProvide recommendations based on the user's history and product catalog.")
	if req.IncludeReasoning {
		sb.WriteString(" Include detailed reasoning for each recommendation.")
	}

	return sb.String()
}

// CustomerSupport handles AI-powered customer support queries
func (c *Client) CustomerSupport(ctx context.Context, message string) (*CustomerSupportResponse, error) {
	req := CustomerSupportRequest{
		Message: message,
	}
	return c.CustomerSupportWithRequest(ctx, req)
}

// CustomerSupportWithRequest handles customer support with a custom request
func (c *Client) CustomerSupportWithRequest(ctx context.Context, req CustomerSupportRequest) (*CustomerSupportResponse, error) {
	if req.Message == "" {
		return nil, errors.New("message cannot be empty")
	}

	systemPrompt := buildSupportSystemPrompt(req)

	messages := []ChatMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
	}

	// Add conversation history
	for _, msg := range req.History {
		messages = append(messages, msg)
	}

	// Add the current message
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: req.Message,
	})

	resp, err := c.ChatCompletion(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("failed to generate support response: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("no response generated")
	}

	response := resp.Choices[0].Message.Content

	// Analyze sentiment and determine if human escalation is needed
	requiresHuman := detectEscalationNeeded(req.Message, response)
	sentiment := analyzeSentiment(req.Message)
	confidence := calculateConfidence(resp)

	return &CustomerSupportResponse{
		Response:      response,
		Sentiment:     sentiment,
		RequiresHuman: requiresHuman,
		Confidence:    confidence,
		SuggestedNext: generateSuggestedActions(response),
	}, nil
}

// buildSupportSystemPrompt creates the system prompt for customer support
func buildSupportSystemPrompt(req CustomerSupportRequest) string {
	var sb strings.Builder

	sb.WriteString(`You are a helpful and professional customer support agent for an e-commerce platform. Your goal is to:
1. Understand the customer's issue clearly
2. Provide accurate and helpful information
3. Be empathetic and professional
4. Escalate to human support when necessary

Guidelines:
- Be concise but thorough
- Never make up information about orders or products
- If you don't know something, admit it and offer to escalate
- Always end with asking if there's anything else you can help with
`)

	if req.Context != "" {
		sb.WriteString(fmt.Sprintf("\nAdditional Context: %s\n", req.Context))
	}

	if req.OrderContext != nil {
		sb.WriteString(fmt.Sprintf(`
Order Information:
- Order ID: %s
- Status: %s
- Created: %s
- Total: $%.2f
- Items: %s
`, req.OrderContext.OrderID, req.OrderContext.Status,
			req.OrderContext.CreatedAt.Format(time.RFC3339),
			req.OrderContext.TotalAmount,
			strings.Join(req.OrderContext.Items, ", ")))
	}

	return sb.String()
}

// detectEscalationNeeded determines if a human agent should take over
func detectEscalationNeeded(message, response string) bool {
	escalationKeywords := []string{
		"speak to human", "talk to person", "real person",
		"manager", "supervisor", "complaint", "refund",
		"legal", "lawsuit", "attorney", "lawyer",
		"fraud", "scam", "stolen",
	}

	messageLower := strings.ToLower(message)
	for _, keyword := range escalationKeywords {
		if strings.Contains(messageLower, keyword) {
			return true
		}
	}

	// Also check if the AI response indicates uncertainty
	uncertaintyIndicators := []string{
		"i'm not sure", "i cannot", "i don't have access",
		"please contact", "human agent",
	}

	responseLower := strings.ToLower(response)
	for _, indicator := range uncertaintyIndicators {
		if strings.Contains(responseLower, indicator) {
			return true
		}
	}

	return false
}

// analyzeSentiment performs basic sentiment analysis
func analyzeSentiment(message string) string {
	messageLower := strings.ToLower(message)

	negativeWords := []string{
		"angry", "frustrated", "terrible", "awful", "horrible",
		"worst", "hate", "disappointed", "unacceptable", "ridiculous",
	}

	positiveWords := []string{
		"thank", "great", "excellent", "amazing", "wonderful",
		"happy", "love", "perfect", "appreciate", "helpful",
	}

	negCount := 0
	posCount := 0

	for _, word := range negativeWords {
		if strings.Contains(messageLower, word) {
			negCount++
		}
	}

	for _, word := range positiveWords {
		if strings.Contains(messageLower, word) {
			posCount++
		}
	}

	if negCount > posCount {
		return "negative"
	} else if posCount > negCount {
		return "positive"
	}
	return "neutral"
}

// calculateConfidence estimates confidence in the AI response
func calculateConfidence(resp *ChatCompletionResponse) float64 {
	// Base confidence
	confidence := 0.8

	// Adjust based on response characteristics
	if len(resp.Choices) > 0 {
		content := resp.Choices[0].Message.Content
		contentLen := len(content)

		// Very short responses might indicate uncertainty
		if contentLen < 50 {
			confidence -= 0.1
		} else if contentLen > 200 {
			confidence += 0.05
		}

		// Check for hedging language
		hedges := []string{"might", "possibly", "perhaps", "maybe", "not sure"}
		for _, hedge := range hedges {
			if strings.Contains(strings.ToLower(content), hedge) {
				confidence -= 0.05
			}
		}
	}

	// Clamp confidence between 0 and 1
	if confidence < 0 {
		confidence = 0
	} else if confidence > 1 {
		confidence = 1
	}

	return confidence
}

// generateSuggestedActions creates follow-up action suggestions
func generateSuggestedActions(response string) []string {
	suggestions := []string{}
	responseLower := strings.ToLower(response)

	if strings.Contains(responseLower, "order") || strings.Contains(responseLower, "shipping") {
		suggestions = append(suggestions, "Track my order")
	}

	if strings.Contains(responseLower, "return") || strings.Contains(responseLower, "refund") {
		suggestions = append(suggestions, "Start a return")
		suggestions = append(suggestions, "Check refund status")
	}

	if strings.Contains(responseLower, "product") || strings.Contains(responseLower, "item") {
		suggestions = append(suggestions, "Browse products")
		suggestions = append(suggestions, "View recommendations")
	}

	// Always offer to speak with human
	suggestions = append(suggestions, "Speak with a human agent")

	return suggestions
}

// SimilaritySearch finds products similar to the given text using embeddings
func (c *Client) SimilaritySearch(ctx context.Context, query string, products []ProductInfo, topK int) ([]Recommendation, error) {
	if len(products) == 0 {
		return nil, errors.New("products list cannot be empty")
	}

	if topK <= 0 {
		topK = 5
	}

	// Get query embedding
	queryEmbedding, err := c.GetEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get query embedding: %w", err)
	}

	// Get product embeddings
	productTexts := make([]string, len(products))
	for i, p := range products {
		productTexts[i] = fmt.Sprintf("%s %s %s", p.Name, p.Description, strings.Join(p.Tags, " "))
	}

	productEmbeddings, err := c.GetEmbeddings(ctx, productTexts)
	if err != nil {
		return nil, fmt.Errorf("failed to get product embeddings: %w", err)
	}

	// Calculate similarities
	type scoredProduct struct {
		index      int
		similarity float64
	}

	scores := make([]scoredProduct, len(products))
	for i, embedding := range productEmbeddings {
		scores[i] = scoredProduct{
			index:      i,
			similarity: cosineSimilarity(queryEmbedding, embedding),
		}
	}

	// Sort by similarity (descending)
	for i := 0; i < len(scores)-1; i++ {
		for j := i + 1; j < len(scores); j++ {
			if scores[j].similarity > scores[i].similarity {
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}

	// Return top K results
	if topK > len(scores) {
		topK = len(scores)
	}

	results := make([]Recommendation, topK)
	for i := 0; i < topK; i++ {
		p := products[scores[i].index]
		results[i] = Recommendation{
			ProductID:  p.ID,
			Score:      scores[i].similarity,
			Similarity: scores[i].similarity,
			Category:   p.Category,
		}
	}

	return results, nil
}

// cosineSimilarity calculates the cosine similarity between two vectors
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (sqrt(normA) * sqrt(normB))
}

// sqrt is a simple square root implementation
func sqrt(x float64) float64 {
	if x < 0 {
		return 0
	}
	z := x / 2
	for i := 0; i < 100; i++ {
		z = z - (z*z-x)/(2*z)
	}
	return z
}

// ListModels returns the available AI models
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	url := c.config.Endpoint + "/models"

	respBody, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Object string      `json:"object"`
		Data   []ModelInfo `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal models response: %w", err)
	}

	return response.Data, nil
}

// ModelInfo represents information about an available model
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Premium bool   `json:"premium,omitempty"`
}

// ListCloudModels returns the available models from the Hanzo Cloud API
// (Go backend), which includes the full routing table with premium flags.
// Use this instead of ListModels when you need billing-aware model info.
func (c *Client) ListCloudModels(ctx context.Context) ([]ModelInfo, error) {
	url := c.config.CloudEndpoint + "/models"

	respBody, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Object string      `json:"object"`
		Data   []ModelInfo `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cloud models response: %w", err)
	}

	return response.Data, nil
}

// HealthCheck verifies the AI service is available
func (c *Client) HealthCheck(ctx context.Context) error {
	url := c.config.Endpoint + "/health"

	_, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		// Try models endpoint as fallback
		_, err = c.ListModels(ctx)
		if err != nil {
			return fmt.Errorf("AI service health check failed: %w", err)
		}
	}

	return nil
}

// Global client instance for convenience
var defaultClient *Client

// Initialize initializes the default AI client
func Initialize(config *AIConfig) {
	defaultClient = NewClient(config)
}

// GetClient returns the default AI client, initializing if necessary
func GetClient() *Client {
	if defaultClient == nil {
		defaultClient = NewClient(nil)
	}
	return defaultClient
}

// Convenience functions using the default client

// ChatCompletionDefault sends a chat completion using the default client
func ChatCompletionDefault(ctx context.Context, messages []ChatMessage) (*ChatCompletionResponse, error) {
	return GetClient().ChatCompletion(ctx, messages)
}

// GetEmbeddingDefault generates an embedding using the default client
func GetEmbeddingDefault(ctx context.Context, text string) ([]float32, error) {
	return GetClient().GetEmbedding(ctx, text)
}

// GetRecommendationsDefault generates recommendations using the default client
func GetRecommendationsDefault(ctx context.Context, userID string, products []ProductInfo) (*RecommendationResponse, error) {
	return GetClient().GetRecommendations(ctx, userID, products)
}

// CustomerSupportDefault handles support queries using the default client
func CustomerSupportDefault(ctx context.Context, message string) (*CustomerSupportResponse, error) {
	return GetClient().CustomerSupport(ctx, message)
}
