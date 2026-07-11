// Package health probes an upstream's health endpoint (e.g. o11y's
// /v1/o11y/health) so the admin overview can report System Health honestly. A
// non-2xx or unreachable upstream is reported as not-ok — never masked.
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

// Configured reports whether a health URL is set.
func (h *Client) Configured() bool { return h != nil && h.url != "" }

// OK reports whether the health endpoint answers 2xx.
func (h *Client) OK(ctx context.Context) (bool, error) {
	if !h.Configured() {
		return false, fmt.Errorf("o11y health not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		return false, err
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("o11y unreachable: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("o11y health %d", resp.StatusCode)
	}
	return true, nil
}
