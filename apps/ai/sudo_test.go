// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"net/http"
	"testing"

	"github.com/hanzoai/authz"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// ai's SuperAdmin is this boundary's verdict: what principal.Super reads off the
// request, and nothing ai could decide from the token or an org itself.
func TestAiReadsTheBoundarysVerdict(t *testing.T) {
	sudo()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"the verdict", map[string]string{authz.HeaderUserAdmin: "true", authz.HeaderUserOwner: "admin"}, true},
		{"owner admin, no verdict", map[string]string{authz.HeaderUserOwner: "admin", authz.HeaderOrg: "admin"}, false},
		{"an org admin", map[string]string{authz.HeaderUserOrgAdmin: "true", authz.HeaderUserOwner: "acme"}, false},
		{"nothing", nil, false},
	} {
		c := app.TestCtx(http.MethodGet, "/v1/models")
		for k, v := range tc.headers {
			c.Fiber().Request().Header.Set(k, v)
		}
		if got := aiobject.Super(c); got != tc.want {
			t.Errorf("%s: ai Super = %v, want %v", tc.name, got, tc.want)
		}
	}
}
