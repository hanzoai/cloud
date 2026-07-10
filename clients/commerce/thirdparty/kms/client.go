// Package kms provides a thin HTTP client for Hanzo KMS secret management.
package kms

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config holds KMS client configuration.
type Config struct {
	Enabled      bool
	URL          string
	ClientID     string
	ClientSecret string
	ProjectID    string
	Environment  string
}

// Client is a thin HTTP client wrapping the KMS REST API.
type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	projectID    string
	environment  string

	accessToken string
	tokenExpiry time.Time
	httpClient  *http.Client
	mu          sync.RWMutex
}

// NewClient creates a new KMS client.
func NewClient(cfg *Config) *Client {
	return &Client{
		baseURL:      strings.TrimRight(cfg.URL, "/"),
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		projectID:    cfg.ProjectID,
		environment:  cfg.Environment,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// authResponse is the response from the KMS auth endpoint.
type authResponse struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"` // seconds
}

// authenticate obtains or refreshes the access token via Universal Auth.
func (c *Client) authenticate() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Skip if token is still valid (with 30s buffer)
	if c.accessToken != "" && time.Now().Add(30*time.Second).Before(c.tokenExpiry) {
		return nil
	}

	body, err := json.Marshal(map[string]string{
		"clientId":     c.clientID,
		"clientSecret": c.clientSecret,
	})
	if err != nil {
		return fmt.Errorf("kms auth encode error: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL+"/v1/kms/auth/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("kms auth request build error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kms auth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kms auth failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var authResp authResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return fmt.Errorf("kms auth decode error: %w", err)
	}

	c.accessToken = authResp.AccessToken
	if authResp.ExpiresIn > 0 {
		c.tokenExpiry = time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)
	} else {
		// Default to 5 minutes if not specified
		c.tokenExpiry = time.Now().Add(5 * time.Minute)
	}

	return nil
}

// secretResponse is the response from the KMS secrets endpoint.
type secretResponse struct {
	Secret struct {
		SecretValue string `json:"secretValue"`
	} `json:"secret"`
}

// GetSecretRaw fetches a secret from KMS by path and name.
func (c *Client) GetSecretRaw(secretPath, secretName string) (string, error) {
	if err := c.authenticate(); err != nil {
		return "", err
	}

	c.mu.RLock()
	token := c.accessToken
	c.mu.RUnlock()

	// Build request URL — v4 API uses projectId (not workspaceId)
	reqURL := fmt.Sprintf(
		"%s/api/v4/secrets/%s?projectId=%s&environment=%s&secretPath=%s",
		c.baseURL,
		url.PathEscape(secretName),
		url.QueryEscape(c.projectID),
		url.QueryEscape(c.environment),
		url.QueryEscape(secretPath),
	)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("kms request build error: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("kms request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("kms get secret failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var secretResp secretResponse
	if err := json.NewDecoder(resp.Body).Decode(&secretResp); err != nil {
		return "", fmt.Errorf("kms secret decode error: %w", err)
	}

	return secretResp.Secret.SecretValue, nil
}

// SetSecret writes a secret to KMS at the given path.
func (c *Client) SetSecret(secretPath, secretName, secretValue string) error {
	if err := c.authenticate(); err != nil {
		return err
	}

	c.mu.RLock()
	token := c.accessToken
	c.mu.RUnlock()

	// v4 API: secretName in URL path, projectId (not workspaceId) in body
	payload := fmt.Sprintf(
		`{"secretValue":%q,"secretPath":%q,"projectId":%q,"environment":%q,"type":"shared"}`,
		secretValue, secretPath, c.projectID, c.environment,
	)

	req, err := http.NewRequest("POST", c.baseURL+"/api/v4/secrets/"+secretName, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("kms set secret request build error: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kms set secret request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kms set secret failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}
