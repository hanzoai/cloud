// Package middleware provides HTTP middleware for the Commerce API.
//
// This file implements Cloudflare-aware HTTP cache control middleware.
// Routes served via api.hanzo.ai sit behind CF; correct Cache-Control
// headers are the only lever we have to control what CF caches.
//
// Strategy:
//   - All authenticated routes: Cache-Control: private, no-store
//     (CF must not cache these — they carry per-user data)
//   - Public read-only routes (billing plans, product catalog): Cache-Control: public
//     with a TTL appropriate to how often the data changes.
//   - All mutation routes (POST/PUT/PATCH/DELETE): Cache-Control: no-store
//     regardless of the route's other classification.
//
// CF Cache-Tag headers allow targeted cache purging when data changes.
// Add tags in individual handlers via SetCFCacheTags(c, "plans", "org:xyz").
package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// CachePublic returns middleware that sets public cache headers with the given TTL.
//
// CF caches for ttl seconds (s-maxage). Browsers cache for ttl/2 seconds
// to ensure fresh content at browser re-visits. stale-while-revalidate
// allows CF to serve stale content while fetching fresh in background.
//
// Mutations (POST/PUT/PATCH/DELETE) are always no-store regardless.
func CachePublic(ttl int) gin.HandlerFunc {
	browserTTL := ttl / 2
	if browserTTL < 30 {
		browserTTL = 30
	}
	cc := fmt.Sprintf("public, max-age=%d, s-maxage=%d, stale-while-revalidate=60", browserTTL, ttl)
	cdnCC := fmt.Sprintf("max-age=%d", ttl)

	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			c.Header("Cache-Control", "no-store")
		default:
			c.Header("Cache-Control", cc)
			c.Header("CDN-Cache-Control", cdnCC)
			c.Header("Vary", "Accept-Encoding")
		}
		c.Next()
	}
}

// CachePublicTTL is CachePublic accepting a time.Duration.
func CachePublicTTL(ttl time.Duration) gin.HandlerFunc {
	return CachePublic(int(ttl.Seconds()))
}

// CachePrivate sets Cache-Control: private, no-store.
// Use on all authenticated per-user or per-org routes.
// CF will not cache these responses.
func CachePrivate() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Next()
	}
}

// CacheNoStore disables all caching unconditionally.
// Use on auth flows, checkout, and payment callbacks.
func CacheNoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Next()
	}
}

// SetCFCacheTags adds Cloudflare Cache-Tag header values to the response.
// Tags are used for targeted cache purging (e.g. purge all "plans" entries).
// Multiple calls accumulate; tags are comma-joined as CF requires.
//
// Example: SetCFCacheTags(c, "plans", "org:hanzo")
func SetCFCacheTags(c *gin.Context, tags ...string) {
	if len(tags) == 0 {
		return
	}
	if existing := c.Writer.Header().Get("Cache-Tag"); existing != "" {
		tags = append([]string{existing}, tags...)
	}
	c.Header("Cache-Tag", strings.Join(tags, ","))
}

// CFCacheTags returns middleware that sets Cache-Tag header(s).
// Use on route groups whose entries should be purgeable as a unit.
func CFCacheTags(tags ...string) gin.HandlerFunc {
	header := strings.Join(tags, ",")
	return func(c *gin.Context) {
		// Accumulate with any previously set tags
		if existing := c.Writer.Header().Get("Cache-Tag"); existing != "" {
			c.Header("Cache-Tag", existing+","+header)
		} else {
			c.Header("Cache-Tag", header)
		}
		c.Next()
	}
}
