// Package health probes an upstream's health endpoint (e.g. o11y's
// /v1/o11y/health) so the admin overview can report System Health honestly. A
// non-2xx or unreachable upstream reads as down — never masked.
package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client probes one health URL.
type Client struct {
	url  string
	http *http.Client
}

// New builds a health probe for url.
func New(u string) *Client {
	return &Client{url: strings.TrimSpace(u), http: &http.Client{Timeout: 8 * time.Second}}
}

// Ready reports whether a health URL is wired.
func (c *Client) Ready() bool { return c != nil && c.url != "" }

// Up reports whether the health endpoint answers 2xx.
func (c *Client) Up(ctx context.Context) (bool, error) {
	if !c.Ready() {
		return false, fmt.Errorf("health endpoint not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("health unreachable: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("health status %d", resp.StatusCode)
	}
	return true, nil
}
