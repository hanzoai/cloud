// Billing admin SPA HTTP handler.
//
// The embedded SPA (ui/dist, populated by the Dockerfile billing-build stage
// from github.com/hanzoai/billing) is served at /admin/billing/*. Access is
// gated on IAM roles: admin, billing_admin, owner, or superadmin — and the
// legacy IsAdmin claim. Non-admin callers (including unauthenticated) get a
// plain 404 so the route's existence does not leak.
//
// IAM auth is performed here, not by the global /v1/commerce middleware
// chain: this handler sits under /admin, outside that group. We accept the
// Bearer token, parse the IAM JWT, and check roles.
package billing

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
)

// adminRoles is the set of IAM roles authorized to load the billing admin
// SPA. IsAdmin (Casdoor bool flag) is also sufficient.
var adminRoles = map[string]struct{}{
	"admin":         {},
	"billing_admin": {},
	"owner":         {},
	"superadmin":    {},
}

// isAuthorized returns true if the IAM claims grant access to the billing
// admin SPA. The check is purely role-based; per-tenant authorization
// happens at the API layer against /v1/commerce/billing/*.
func isAuthorized(claims *auth.IAMClaims) bool {
	if claims == nil {
		return false
	}
	if claims.IsAdmin {
		return true
	}
	for _, r := range claims.Roles {
		if _, ok := adminRoles[r]; ok {
			return true
		}
	}
	return false
}

// extractBearer returns the Bearer token from the Authorization header, or
// an empty string if absent/malformed.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// UIHandler returns a gin handler that serves the embedded billing admin
// SPA at the given mount prefix (typically "/admin/billing"). Unauthorized
// requests get a plain 404 — no existence leak.
//
// The IAMClient is optional in test harnesses; a nil client (IAM disabled)
// means every request is treated as unauthorized, which produces the same
// 404 — safer fail-closed default.
func UIHandler(prefix string, iam *auth.IAMClient) gin.HandlerFunc {
	root := UISub()
	fileServer := http.FileServer(http.FS(root))

	return func(c *gin.Context) {
		// Authorize first — non-admin requests never touch the FS.
		token := extractBearer(c.Request)
		if token == "" {
			http.NotFound(c.Writer, c.Request)
			return
		}
		if iam == nil {
			http.NotFound(c.Writer, c.Request)
			return
		}
		claims, err := iam.ValidateToken(c.Request.Context(), token)
		if err != nil {
			http.NotFound(c.Writer, c.Request)
			return
		}
		if !isAuthorized(claims) {
			http.NotFound(c.Writer, c.Request)
			return
		}

		// Strip the mount prefix so FS lookups match embedded paths.
		path := strings.TrimPrefix(c.Request.URL.Path, prefix)
		if path == "" || path == "/" {
			path = "index.html"
		}
		path = strings.TrimPrefix(path, "/")

		// Asset request (has a file extension) — serve directly with caching.
		if i := strings.LastIndexByte(path, '.'); i >= 0 && !strings.Contains(path[i:], "/") {
			if _, err := fs.Stat(root, path); err == nil {
				c.Writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				r2 := c.Request.Clone(c.Request.Context())
				r2.URL.Path = "/" + path
				fileServer.ServeHTTP(c.Writer, r2)
				return
			}
		}

		// SPA fallback — serve index.html for any unmatched route under the
		// mount prefix. Client-side router resolves the rest.
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		idx, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(c.Writer, "billing SPA not built", http.StatusServiceUnavailable)
			return
		}
		_, _ = c.Writer.Write(idx)
	}
}
