// Package cloudflare provides a Cloudflare API v4 client for Commerce.
//
// Used for:
//   - Purging cached URLs or Cache-Tag groups after writes
//   - Wiring CF cache invalidation into model AfterSave/AfterDelete hooks
package cloudflare

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	"github.com/gin-gonic/gin"
)

// Client is a Cloudflare API v4 client.
type Client struct {
	Email    string
	Key      string
	Zone     string
	Endpoint string

	client *http.Client
}

// New creates a Client from the gin request context using global config.
func New(c *gin.Context) *Client {
	ctx := middleware.GetContext(c)

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Second*55)
	defer cancel()

	return &Client{
		Email:    config.Cloudflare.Email,
		Key:      config.Cloudflare.Key,
		Zone:     config.Cloudflare.Zone,
		Endpoint: "https://api.cloudflare.com/client/v4/",
		client:   &http.Client{Timeout: 55 * time.Second},
	}
}

// NewBackground creates a Client suitable for background goroutines (no gin context).
func NewBackground() *Client {
	return &Client{
		Email:    config.Cloudflare.Email,
		Key:      config.Cloudflare.Key,
		Zone:     config.Cloudflare.Zone,
		Endpoint: "https://api.cloudflare.com/client/v4/",
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Request sends an authenticated request to the CF API.
func (c *Client) Request(method, url string, data interface{}) (*http.Response, error) {
	var payload *bytes.Reader

	if data != nil {
		payload = bytes.NewReader(json.EncodeBytes(data))
	}

	req, err := http.NewRequest(method, c.Endpoint+url, payload)
	if err != nil {
		return nil, err
	}

	req.Header.Add("X-AUTH-EMAIL", c.Email)
	req.Header.Add("X-AUTH-KEY", c.Key)
	req.Header.Add("Content-Type", "application/json")

	return c.client.Do(req)
}

// Purge removes specific URLs from CF cache.
// zone defaults to the configured zone if empty.
func (c *Client) Purge(zone string, files []string) {
	if zone == "" {
		zone = c.Zone
	}
	type purgeReq struct {
		Files []string `json:"files"`
	}
	c.Request("DELETE", "zones/"+zone+"/purge_cache", &purgeReq{Files: files})
}

// PurgeTags removes all CF cache entries that match the given Cache-Tag values.
// This is the most targeted invalidation strategy — tag writes at insertion time
// with SetCFCacheTags() and purge by tag on mutations.
//
// Tags are auto-split on commas so a single comma-joined string is also accepted.
func (c *Client) PurgeTags(tags ...string) {
	if len(tags) == 0 {
		return
	}
	zone := c.Zone
	if zone == "" {
		return // CF zone required for tag-based purge
	}

	// Flatten any comma-joined tags
	var flat []string
	for _, t := range tags {
		for _, part := range strings.Split(t, ",") {
			if p := strings.TrimSpace(part); p != "" {
				flat = append(flat, p)
			}
		}
	}

	type purgeReq struct {
		Tags []string `json:"tags"`
	}
	// Fire-and-forget — CF purge is idempotent and best-effort
	go func() {
		c.Request("DELETE", "zones/"+zone+"/purge_cache", &purgeReq{Tags: flat})
	}()
}

// PurgeEverything purges the entire CF cache for the zone.
// Use sparingly — only for full deploys or catastrophic staleness.
func (c *Client) PurgeEverything() {
	if c.Zone == "" {
		return
	}
	type purgeReq struct {
		PurgeEverything bool `json:"purge_everything"`
	}
	go func() {
		c.Request("DELETE", "zones/"+c.Zone+"/purge_cache", &purgeReq{PurgeEverything: true})
	}()
}

// PurgeAfterWrite is a helper that fires a background CF cache-tag purge
// after a model write. Call from AfterSave/AfterDelete model hooks.
//
//	cf := cloudflare.NewBackground()
//	cf.PurgeAfterWrite("plans", "org:hanzo")
func PurgeAfterWrite(tags ...string) {
	c := NewBackground()
	c.PurgeTags(tags...)
}

// IsConfigured returns true when CF credentials and zone are set.
func IsConfigured() bool {
	return config.Cloudflare.Key != "" && config.Cloudflare.Zone != ""
}

// purgeContext is unused for now — kept for future sync purge use.
var _ = context.Background

