// Copyright © 2026 Hanzo AI. MIT License.

// Package auth wires gateway-supplied identity headers (X-Org-Id,
// X-User-Id, X-User-Email) into request context. commerced does not
// validate JWTs itself — hanzoai/gateway already did that. The
// middleware is the trust boundary: in production, only the gateway
// can reach commerced, and COMMERCED_REQUIRE_IDENTITY=true rejects any
// request without identity headers.
package auth

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Header names — vendor-free X-* convention (see /Users/z/work/hanzo/CLAUDE.md).
const (
	HeaderOrgID     = "X-Org-Id"
	HeaderUserID    = "X-User-Id"
	HeaderUserEmail = "X-User-Email"
)

type ctxKey int

const (
	ctxKeyOrgID ctxKey = iota
	ctxKeyUserID
	ctxKeyUserEmail
)

// RequireIdentity reads identity headers and attaches them to ctx.
// When require=true and no headers are present, responds 401. When
// require=false, missing headers yield empty ctx values — the
// embedded/dev path.
func RequireIdentity(require bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			org := r.Header.Get(HeaderOrgID)
			user := r.Header.Get(HeaderUserID)
			email := r.Header.Get(HeaderUserEmail)

			if require && org == "" && user == "" {
				http.Error(w, `{"error":"identity required","code":401}`, http.StatusUnauthorized)
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, ctxKeyOrgID, org)
			ctx = context.WithValue(ctx, ctxKeyUserID, user)
			ctx = context.WithValue(ctx, ctxKeyUserEmail, email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Gin returns a Gin middleware that mirrors RequireIdentity. Used by
// pkg/commerce/server.go to gate the /v1/commerce and /_/commerce groups.
func Gin(require bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		org := c.GetHeader(HeaderOrgID)
		user := c.GetHeader(HeaderUserID)
		email := c.GetHeader(HeaderUserEmail)

		if require && org == "" && user == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "identity required",
				"code":  401,
			})
			return
		}

		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, ctxKeyOrgID, org)
		ctx = context.WithValue(ctx, ctxKeyUserID, user)
		ctx = context.WithValue(ctx, ctxKeyUserEmail, email)
		c.Request = c.Request.WithContext(ctx)

		// Mirror onto Gin keys for legacy handlers that read from c.Get.
		if org != "" {
			c.Set("iam_org", org)
			c.Set("iam_authenticated", true)
		}
		if user != "" {
			c.Set("iam_user_id", user)
		}
		if email != "" {
			c.Set("iam_email", email)
		}
		c.Next()
	}
}

// OrgID returns the org id attached by RequireIdentity, or "".
func OrgID(ctx context.Context) string { return strFromCtx(ctx, ctxKeyOrgID) }

// UserID returns the user id attached by RequireIdentity, or "".
func UserID(ctx context.Context) string { return strFromCtx(ctx, ctxKeyUserID) }

// UserEmail returns the user email attached by RequireIdentity, or "".
func UserEmail(ctx context.Context) string { return strFromCtx(ctx, ctxKeyUserEmail) }

func strFromCtx(ctx context.Context, k ctxKey) string {
	if v, ok := ctx.Value(k).(string); ok {
		return v
	}
	return ""
}
