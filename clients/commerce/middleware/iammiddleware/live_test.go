// Copyright © 2026 Hanzo AI. MIT License.

package iammiddleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestLiveFromHeaders pins the gateway test-mode contract: a request is
// live unless the gateway forwarded X-Hanzo-Test: true. This is the
// authority payment.ProcessorsForOrg keys Square sandbox-vs-production
// off of (org.Live=false ⇒ sandbox). Mirrors middleware/accesstoken.go.
func TestLiveFromHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name     string
		header   string // value of X-Hanzo-Test; "" means unset
		set      bool
		wantLive bool
	}{
		{"unset ⇒ live", "", false, true},
		{"empty ⇒ live", "", true, true},
		{"true ⇒ test", "true", true, false},
		{"TRUE (case-insensitive) ⇒ test", "TRUE", true, false},
		{"  true  (trimmed) ⇒ test", "  true  ", true, false},
		{"false ⇒ live", "false", true, true},
		{"1 (not true) ⇒ live", "1", true, true},
		{"yes (not true) ⇒ live", "yes", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/billing/topup/token", nil)
			if tc.set {
				c.Request.Header.Set(HeaderTest, tc.header)
			}
			if got := liveFromHeaders(c); got != tc.wantLive {
				t.Fatalf("liveFromHeaders(%q set=%v) = %v, want %v",
					tc.header, tc.set, got, tc.wantLive)
			}
		})
	}
}
