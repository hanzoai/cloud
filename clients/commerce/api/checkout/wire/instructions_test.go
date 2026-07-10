// Copyright © 2026 Hanzo AI. MIT License.

package wire

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

func init() { gin.SetMode(gin.TestMode) }

// TestInstructions_NoOrgFailsClosed proves the RED finding is fixed: an org-less
// request (e.g. an anonymous caller after EdgeAuth strips a spoofed X-Org-Id)
// returns a clean 401 instead of panicking on GetOrganization's MustGet (500).
func TestInstructions_NoOrgFailsClosed(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/checkout/wire/instructions", nil)

	// Must not panic.
	Instructions(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("org-less wire instructions: status=%d, want 401 (fail closed, no panic)", w.Code)
	}
}

// TestInstructions_ConfiguredOrgReturns200 proves a resolved org with wire config
// still gets its instructions (no regression from the fail-closed fix).
func TestInstructions_ConfiguredOrgReturns200(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/checkout/wire/instructions", nil)

	org := &organization.Organization{}
	org.Wire.BankName = "Test Bank"
	org.Wire.AccountHolder = "Hanzo AI"
	c.Set("organization", org)

	Instructions(c)

	if w.Code != http.StatusOK {
		t.Fatalf("configured-org wire instructions: status=%d, want 200 (no regression)", w.Code)
	}
}
