package fare

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// A held org's storage is reached by an admin of the org, a SuperAdmin, or the
// org's own application, and by no plain member of it; a customer's own org is
// unchanged. The headers are the ones the identity boundary mints.
func TestAHeldOrgsStorageIsNotItsMembers(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodGet, "/", func(c *zip.Ctx) error {
		// admit resolves the tenant through orgOf (namespace.Sanitize); mirror that
		// here, so held() reads the same lowercase slug the operation acts on.
		org, _ := orgOf(c)
		if err := held(c, org); err != nil {
			return err
		}
		return c.JSON(http.StatusNoContent, nil)
	})
	for _, tc := range []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"a member of hanzo", map[string]string{"X-Org-Id": "hanzo", "X-User-Id": "u1"}, http.StatusForbidden},
		{"an admin of hanzo", map[string]string{"X-Org-Id": "hanzo", "X-User-Id": "u2", "X-User-IsOrgAdmin": "true"}, http.StatusNoContent},
		{"hanzo's own application", map[string]string{"X-Org-Id": "hanzo", "X-User-Id": "app:hanzo/ci", "X-User-IsApp": "true"}, http.StatusNoContent},
		{"a SuperAdmin acting in hanzo", map[string]string{"X-Org-Id": "hanzo", "X-User-Id": "u3", "X-User-IsAdmin": "true"}, http.StatusNoContent},
		{"a member of a customer's org", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u4"}, http.StatusNoContent},
		// A name that only differs in case sanitises to a distinct slug, not a held
		// one, so it reaches no held org's buckets and is admitted as its own tenant.
		{"a member naming Hanzo in another case", map[string]string{"X-Org-Id": "Hanzo", "X-User-Id": "u5"}, http.StatusNoContent},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range tc.hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req, zip.TestConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}
