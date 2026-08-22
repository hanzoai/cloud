// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// THE TENANT RULE HAS TO HOLD HERE TOO, and that it did not was invisible until
// the door in front of it was fixed.
//
// Every app is its own child PROCESS, so `ai` cannot see an in-process reader
// hook and asks over HTTP bearing COMMERCE_SERVICE_TOKEN and no session. Billing's
// door learned to resolve that caller's tenant; the typed op behind it kept asking
// principal.OrgFrom alone, so the caller the door had just admitted was refused
// one layer down — 401 became 403 and nothing else changed:
//
//	GET /v1/billing/tier  org=hanzo  403  "tier: no validated org on the call"
//
// payingOrg checks the tenant BEFORE it checks co-residency, which is what makes
// this testable without a commerce embed: unresolved is 403, resolved-but-absent
// is 503. So 503 here is the PASS — it means the caller was identified and the
// only thing missing is a commerce this test never claimed to have.
func TestPayingOrgAdmitsATrustedService(t *testing.T) {
	const token = "test-commerce-service-token"
	t.Setenv("COMMERCE_SERVICE_TOKEN", token)

	app := zip.New(zip.Config{Logger: luxlog.New("payingorg"), DisableStartupMessage: true})
	app.Use(zip.H(cloud.Bridge()))
	app.Get("/probe", func(c *zip.Ctx) error {
		if _, err := payingOrg(c.Context(), "probe"); err != nil {
			return err
		}
		return c.Bytes(http.StatusOK, []byte(`{}`))
	})

	for _, tc := range []struct {
		what    string
		token   string
		org     string
		refused bool // 403: no tenant resolved
	}{
		{"a trusted service naming its org", token, "hanzo", false},
		{"no credential at all", "", "hanzo", true},
		{"a wrong token", "not-the-token", "hanzo", true},
		{"a valid token naming no org", token, "", true},
		{"a near-miss token", token[:len(token)-1], "hanzo", true},
	} {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		if tc.org != "" {
			req.Header.Set("X-Org-Id", tc.org)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30_000_000_000, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		got403 := resp.StatusCode == http.StatusForbidden &&
			strings.Contains(string(body), "no validated org on the call")
		if got403 != tc.refused {
			t.Errorf("%s: status %d %s — want tenant-refused=%v",
				tc.what, resp.StatusCode, strings.TrimSpace(string(body)), tc.refused)
		}
	}
}

// payingOrg must still refuse OFF the HTTP path. There is no request to read a
// tenant from, so there is nothing to admit — and a resolver that fell back to
// something else here would be inventing one.
func TestPayingOrgRefusesWithoutARequest(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "test-commerce-service-token")
	if _, err := payingOrg(context.Background(), "probe"); err == nil {
		t.Fatal("payingOrg resolved a tenant with no request behind it")
	}
}
