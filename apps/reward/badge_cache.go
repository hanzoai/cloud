// badge_cache.go — HOURLY CACHE FOR /v1/reward/badge/:handle.
//
// hanzoai/reward renders each profile SVG fresh from SQLite on every hit with
// Cache-Control: no-cache, max-age=0. This middleware caches rendered 200 SVG
// responses in-process for an hour (bounded) and sets hourly edge/browser
// Cache-Control with 24h stale-while-revalidate and ETag/304 validation, so
// GitHub README traffic does not recompute profile badges on every request.
// Profile edits (PUT /v1/reward/me/profile) clear the cache immediately so
// turning a profile private or renaming a handle takes effect at once.

package reward

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

const (
	rewardBadgePrefix       = "/v1/reward/badge/"
	rewardProfilePath       = "/v1/reward/me/profile"
	rewardBadgeTTL          = time.Hour
	rewardBadgeCacheMax     = 4096
	rewardBadgeCacheControl = "public, max-age=3600, s-maxage=3600, stale-while-revalidate=86400"
)

// rewardNowFn is the clock the badge cache reads; tests substitute it to advance time.
var rewardNowFn = time.Now

type cachedSVG struct {
	svg  []byte
	etag string
	at   time.Time
}

type rewardBadgeCache struct {
	mu      sync.Mutex
	entries map[string]cachedSVG
}

func newRewardBadgeCache() *rewardBadgeCache {
	return &rewardBadgeCache{entries: make(map[string]cachedSVG)}
}

func (bc *rewardBadgeCache) get(key string, now time.Time) (cachedSVG, bool) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	e, ok := bc.entries[key]
	if !ok || now.Sub(e.at) >= rewardBadgeTTL {
		return cachedSVG{}, false
	}
	return e, true
}

func (bc *rewardBadgeCache) set(key string, svg []byte, etag string, now time.Time) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if _, exists := bc.entries[key]; !exists && len(bc.entries) >= rewardBadgeCacheMax {
		for k, v := range bc.entries {
			if now.Sub(v.at) >= rewardBadgeTTL {
				delete(bc.entries, k)
			}
		}
		if len(bc.entries) >= rewardBadgeCacheMax {
			var oldestKey string
			var oldestAt time.Time
			for k, v := range bc.entries {
				if oldestKey == "" || v.at.Before(oldestAt) {
					oldestKey, oldestAt = k, v.at
				}
			}
			if oldestKey != "" {
				delete(bc.entries, oldestKey)
			}
		}
	}
	bc.entries[key] = cachedSVG{svg: svg, etag: etag, at: now}
}

func (bc *rewardBadgeCache) reset() {
	bc.mu.Lock()
	bc.entries = make(map[string]cachedSVG)
	bc.mu.Unlock()
}

func (bc *rewardBadgeCache) middleware(c *zip.Ctx) error {
	path := c.Path()
	method := c.Method()

	// Clear cached SVGs immediately when a user updates their public profile or handle.
	if method == http.MethodPut && path == rewardProfilePath {
		err := c.Continue()
		if err == nil && c.Fiber().Response().StatusCode() == http.StatusOK {
			bc.reset()
		}
		return err
	}

	if method != http.MethodGet || !strings.HasPrefix(path, rewardBadgePrefix) || !strings.HasSuffix(path, ".svg") {
		return c.Continue()
	}

	key := canonicalBadgeKey(c)
	now := rewardNowFn()
	if hit, ok := bc.get(key, now); ok {
		c.SetHeader("Content-Type", "image/svg+xml; charset=utf-8")
		c.SetHeader("Cache-Control", rewardBadgeCacheControl)
		c.SetHeader("ETag", hit.etag)
		c.SetHeader("X-Content-Type-Options", "nosniff")
		c.SetHeader("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		if rewardETagMatches(c.Header("If-None-Match"), hit.etag) {
			return c.Bytes(http.StatusNotModified, nil)
		}
		return c.Bytes(http.StatusOK, hit.svg)
	}

	if err := c.Continue(); err != nil {
		return err
	}
	resp := c.Fiber().Response()
	switch resp.StatusCode() {
	case http.StatusOK:
		if strings.HasPrefix(string(resp.Header.Peek("Content-Type")), "image/svg+xml") {
			body := bytes.Clone(resp.Body())
			etag := string(resp.Header.Peek("ETag"))
			resp.Header.Set("Cache-Control", rewardBadgeCacheControl)
			bc.set(key, body, etag, now)
		}
	case http.StatusNotModified:
		resp.Header.Set("Cache-Control", rewardBadgeCacheControl)
	}
	return nil
}

func canonicalBadgeKey(c *zip.Ctx) string {
	q := url.Values{}
	c.Fiber().RequestCtx().QueryArgs().VisitAll(func(k, v []byte) { q.Add(string(k), string(v)) })
	return strings.ToLower(c.Path()) + "?" + q.Encode()
}

func rewardETagMatches(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" || etag == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for tag := range strings.SplitSeq(ifNoneMatch, ",") {
		if strings.TrimPrefix(strings.TrimSpace(tag), "W/") == want {
			return true
		}
	}
	return false
}
