// Package dashv2 — login deprecated. Auth is unified through Hanzo IAM.
package dashv2

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// login is intentionally a 410 Gone redirect to Hanzo IAM.
//
// All dashboard auth flows are now unified at hanzo.id. The dash UI should
// open the IAM OAuth dialog and pass the resulting Bearer token to commerce.
func login(c *gin.Context) {
	iam := os.Getenv("IAM_ISSUER")
	if iam == "" {
		iam = "https://hanzo.id"
	}
	c.Header("Location", iam+"/v1/iam/oauth/authorize")
	c.JSON(http.StatusGone, gin.H{
		"error":      "endpoint_deprecated",
		"message":    "Dashboard auth is now via Hanzo IAM. Use the IAM OAuth flow.",
		"redirectTo": iam + "/v1/iam/oauth/authorize",
	})
}
