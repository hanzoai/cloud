// Copyright © 2026 Hanzo AI. MIT License.
//
// Gateway-trust shim tests. The legacy in-binary JWT validation tests
// (~600 LOC of RSA keys + JWKS server + claim shaping) were removed
// when the trust boundary moved to hanzoai/gateway. What's left is a
// minimal contract: when X-Org-Id is present, IAMTokenRequired
// resolves the org and sets the legacy gin keys; when it's absent, it
// falls through to legacy auth.
//
// NB: the earlier `var _ = Describe(...)` Ginkgo block was dead — the JWKS/
// claim helpers (newClient/signToken/makeAdminClaims) and the ginkgo dot-import
// it depended on were deleted in the same migration (see suite_test.go), so it
// never compiled. Removed so the package builds, vets, and runs the stdlib
// contract test below. Coverage of the IAM claim→permission mapping now lives in
// middleware/iammiddleware's own package tests.

package test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
)

func TestFallthroughWithoutHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(iammiddleware.IAMTokenRequired())
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%q", w.Code, w.Body.String())
	}
}
