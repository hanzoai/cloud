// Copyright © 2026 Hanzo AI. MIT License.

package sandbox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The ops are also served on the public group with no route guard, so the tenant
// must come from cloud.Tenant and never from a caller-supplied header.
func TestTheTenantIsNeverJustAHeader(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := zip.New(zip.Config{Logger: luxlog.New("sandboxtenant"), DisableStartupMessage: true})
		app.Use(zip.H(cloud.Bridge()))
		// lease creates; the other ops act on what it returns.
		app.Post("/v1/sandbox/lease", planeLease,
			zip.WithOperationID("probe_lease_sandbox"))

		for _, tc := range []struct {
			what string
			org  string
		}{
			{"an org nobody vouched for", "other-org"},
			{"no org at all", ""},
		} {
			req := httptest.NewRequest(http.MethodPost, "/v1/sandbox/lease",
				strings.NewReader(`{"class":"exec"}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.org != "" {
				req.Header.Set("X-Org-Id", tc.org)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30_000_000_000, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("%s: %v", tc.what, err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			// 403 is the tenant refusing. Anything that gets PAST the tenant reaches the
			// mount check and answers 503 — which is the shape of the bug, not a
			// different error: it means the forged org was accepted and only the absent
			// service stopped it.
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s: status %d %s — want 401; a tenant resolved from a header "+
					"nobody stood behind is another org's store and another org's pod",
					tc.what, resp.StatusCode, strings.TrimSpace(string(body)))
			}
		}
	})
}

// And the plane shape still works, because that is the surface the ops were
// built for: a caller stated in-process resolves, and nothing stated does not.
func TestTheStatedCallerIsStillATenant(t *testing.T) {
	for _, tc := range []struct {
		what     string
		ctx      context.Context
		resolved bool
	}{
		{"a caller stating its org", cloud.For(context.Background(), "hanzo"), true},
		{"a caller stating an empty org", cloud.For(context.Background(), ""), false},
		{"no caller at all", context.Background(), false},
	} {
		org, ok := cloud.Tenant(tc.ctx)
		if ok != tc.resolved {
			t.Errorf("%s: cloud.Tenant = %q,%v — want resolved=%v", tc.what, org, ok, tc.resolved)
		}
	}
}
