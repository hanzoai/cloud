package cloud_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/event"
	cloud "github.com/hanzoai/cloud/sdk"
	fiber "github.com/zap-proto/fiber/v3"
)

// TestAudienceThroughTheBoundary drives event.Audience behind the identity boundary
// itself: membership is read off a verified token and never off a header a client sent.
func TestAudienceThroughTheBoundary(t *testing.T) {
	boundary, mint := cloud.IdentityForTest(t)
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(boundary)
	var got string
	app.Raw(http.MethodGet, "/who", func(c *zip.Ctx) error {
		got = event.Audience(c, c.Query("org"), nil)
		return c.NoContent(http.StatusNoContent)
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	ask := func(org string, h map[string]string) string {
		t.Helper()
		got = ""
		r := httptest.NewRequest(http.MethodGet, "/who?org="+org, nil)
		r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36")
		r.Header.Set("X-Forwarded-For", "198.51.100.23")
		for k, v := range h {
			r.Header.Set(k, v)
		}
		if _, err := app.Fiber().Test(r, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	acme := "Bearer " + mint(authz.Membership{Org: "acme", Role: authz.Member}, authz.Membership{Org: "globex", Role: authz.Member})
	for _, tc := range []struct {
		name, org, want string
		h               map[string]string
	}{
		{"a member's bearer on its org's site", "acme", event.Internal, map[string]string{"Authorization": acme}},
		{"a member's bearer on another org's site", "initech", event.Person, map[string]string{"Authorization": acme}},
		{"no bearer", "acme", event.Person, nil},
		{"a forged membership", "initech", event.Person, map[string]string{"X-User-Id": "u-1", "X-User-Orgs": "initech", "X-Org-Id": "initech"}},
		{"a forged membership beside a real bearer", "initech", event.Person, map[string]string{"Authorization": acme, "X-User-Orgs": "initech"}},
		{"a customer of the signup org", "hanzo", event.Person, map[string]string{"Authorization": "Bearer " + mint(authz.Membership{Org: "hanzo", Role: authz.Member})}},
		{"an admin of the signup org", "hanzo", event.Internal, map[string]string{"Authorization": "Bearer " + mint(authz.Membership{Org: "hanzo", Role: authz.Admin})}},
		{"a SuperAdmin", "hanzo", event.Internal, map[string]string{"Authorization": "Bearer " + mint(authz.Membership{Org: "admin", Role: authz.Admin})}},
	} {
		if a := ask(tc.org, tc.h); a != tc.want {
			t.Errorf("%s: audience = %q, want %q", tc.name, a, tc.want)
		}
	}
}
