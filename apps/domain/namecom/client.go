// Package namecom is a minimal client for the name.com Core API v4 — the
// wholesale registrar surface Hanzo Domains resells: check availability, price,
// register, renew, transfer, and set nameservers/contacts on a domain.
//
// Contract (name.com Core API v4):
//   - Base URL: https://api.name.com (production), https://api.dev.name.com (test).
//   - Auth: HTTP Basic — username + API token.
//   - Actions use a ":verb" suffix on the collection/resource, e.g.
//     POST /v4/domains:checkAvailability, POST /v4/domains/{domain}:setNameservers.
//
// Credentials are NEVER hard-coded here: the caller passes the username/token it
// read from the platform secret store (KMS), exactly as the Cloudflare purger reads
// CF_API_TOKEN. This package holds no secret custody of its own.
package namecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Base URLs for the two name.com environments. Test is a full sandbox connected to
// the registries' own test systems — register/renew/transfer are exercisable there
// with no real charge.
const (
	BaseProd = "https://api.name.com"
	BaseTest = "https://api.dev.name.com"
)

// BaseFor maps an environment slug to its base URL. Anything other than "prod"/
// "production"/"mainnet" resolves to the TEST sandbox — fail-safe: an unset or
// misspelled env can never accidentally hit the live, billable registrar.
func BaseFor(env string) string {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "prod", "production", "mainnet", "live":
		return BaseProd
	default:
		return BaseTest
	}
}

// Client calls the name.com v4 API with HTTP Basic auth. It is safe to share across
// goroutines. A zero token/user yields a client whose calls fail closed at name.com
// (401/403) rather than panicking — the caller checks Configured() to degrade early.
type Client struct {
	user  string
	token string
	base  string
	http  *http.Client
}

// New builds a client for (user, token) against the base URL for env. httpClient is
// optional (nil ⇒ a 20s-timeout default).
func New(user, token, env string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{
		user:  strings.TrimSpace(user),
		token: strings.TrimSpace(token),
		base:  BaseFor(env),
		http:  httpClient,
	}
}

// NewWithBase is New with an explicit base URL — used by tests to point at an
// httptest server.
func NewWithBase(user, token, base string, httpClient *http.Client) *Client {
	c := New(user, token, "", httpClient)
	c.base = strings.TrimRight(base, "/")
	return c
}

// Configured reports whether both a username and a token are present, i.e. whether
// a call has any chance of authenticating.
func (c *Client) Configured() bool { return c.user != "" && c.token != "" }

// APIError is a non-2xx response from name.com. name.com returns
// {"message":"...","details":"..."} on error; both are surfaced.
type APIError struct {
	Status  int    `json:"-"`
	Message string `json:"message"`
	Details string `json:"details"`
}

func (e *APIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("name.com %d: %s (%s)", e.Status, e.Message, e.Details)
	}
	return fmt.Sprintf("name.com %d: %s", e.Status, e.Message)
}

// do issues one request. method+path are the HTTP verb and the "/v4/..." path
// (including any ":verb" action suffix); body is JSON-marshaled when non-nil; out
// is JSON-unmarshaled from a 2xx response when non-nil.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("namecom: marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("namecom: build request: %w", err)
	}
	req.SetBasicAuth(c.user, c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("namecom: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode/100 != 2 {
		apiErr := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, apiErr)
		if apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(string(raw))
			if apiErr.Message == "" {
				apiErr.Message = http.StatusText(resp.StatusCode)
			}
		}
		return apiErr
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("namecom: decode response: %w", err)
		}
	}
	return nil
}
