// Copyright © 2026 Hanzo AI. MIT License.

package iammiddleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	pkgAuth "github.com/hanzoai/cloud/clients/commerce/pkg/auth"
)

// TestIsIAMAuthenticated pins the hardened contract: authentication is proven
// ONLY by the validated iam_authenticated gin key (set by IAMTokenRequired after
// it resolves the org from the trusted, post-strip X-Org-Id). A bare client
// X-Org-Id header must NOT count — trusting header presence let an unvalidated
// opaque bearer + client X-Org-Id forge an IAM principal for any org (the
// checkout money-surface bypass).
func TestIsIAMAuthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name  string
		setup func(*gin.Context)
		want  bool
	}{
		{"validated gin key true", func(c *gin.Context) {
			c.Set("iam_authenticated", true)
		}, true},
		{"bare X-Org-Id header is NOT authentication", func(c *gin.Context) {
			c.Request.Header.Set(pkgAuth.HeaderOrgID, "hanzo")
		}, false},
		{"nothing set", func(c *gin.Context) {}, false},
		{"explicit false", func(c *gin.Context) {
			c.Set("iam_authenticated", false)
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/checkout/sessions", nil)
			tc.setup(c)
			if got := IsIAMAuthenticated(c); got != tc.want {
				t.Fatalf("IsIAMAuthenticated=%v, want %v", got, tc.want)
			}
		})
	}
}
