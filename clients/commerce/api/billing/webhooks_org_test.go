// Copyright © 2026 Hanzo AI. MIT License.

package billing

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestResolveWebhookOrgSessionlessDoesNotPanic is the regression guard for the
// webhook-ingress 500: resolveWebhookOrg ran middleware.GetOrganization —
// gin MustGet — on requests that never passed the auth-token group, so every
// signature-VALID provider delivery panicked ("key organization does not
// exist") after passing HMAC verification. Sessionless resolution must fall
// through to header/env/default lookup instead.
func TestResolveWebhookOrgSessionlessDoesNotPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/billing/webhooks/square", nil)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("resolveWebhookOrg panicked on a sessionless request: %v", r)
		}
	}()
	// No "organization" key set — the sessionless webhook shape. The resolver
	// may return nil here (no datastore in a bare test context); the contract
	// under test is strictly "no panic", which the handler turns into an
	// honest 503 rather than a recovered-panic 500.
	_ = resolveWebhookOrg(c)
}
