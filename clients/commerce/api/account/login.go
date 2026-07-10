// Package account — login deprecated. Auth is unified through Hanzo IAM.
package account

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// login is intentionally a 410 Gone redirect to Hanzo IAM.
//
// All commerce auth flows are now unified at hanzo.id. Clients that POST to
// /v1/commerce/account/login receive a 410 + Location header pointing at the
// IAM OAuth authorize endpoint. There is no commerce-local password store.
//
// Migration: callers should request an IAM JWT via PKCE flow and pass it as
// `Authorization: Bearer <token>` to commerce. The iammiddleware validates
// the JWT and populates `c.Get("user")` automatically.
func login(c *gin.Context) {
	iam := os.Getenv("IAM_ISSUER")
	if iam == "" {
		iam = "https://hanzo.id"
	}
	c.Header("Location", iam+"/v1/iam/oauth/authorize")
	c.JSON(http.StatusGone, gin.H{
		"error":      "endpoint_deprecated",
		"message":    "Commerce no longer issues passwords. Use Hanzo IAM (hanzo.id) and pass a Bearer token.",
		"redirectTo": iam + "/v1/iam/oauth/authorize",
		"docs":       "https://docs.hanzo.ai/iam/oauth",
	})
}
