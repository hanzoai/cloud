// Copyright © 2026 Hanzo AI. MIT License.

package sandbox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/plane"
)

// THE SIX OPS ARE SERVED ON TWO DOORS AND THE TENANT RULE HOLDS ON BOTH.
//
// Mount registers each plane handler a second time on the public app, through
// cloud.ZipApp — which recovers the raw *zip.App behind the scope, and says in
// its own doc that scope "bounds middleware", not registration. So these leaves
// carry no route guard: what decides the tenant is whatever the handler reads.
//
// A handler written for the plane alone may read the caller directly, because
// off a request zip returns the caller a door stated in-process and nothing
// outside can write that. On a request the same call returns the headers, and
// the identity boundary deliberately restores an unvalidated caller's own org
// header for the data path. Those are different facts. principal.Acting is the
// one accessor that distinguishes them, and this pins that live() uses it.
//
// The tenant IS the datastore key and the namespace a pod is leased in, so the
// two ends of getting this wrong are another org's files and a computer running
// on another org's account.
func TestTheTenantIsNeverJustAHeader(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("sandboxtenant"), DisableStartupMessage: true})
	app.Use(zip.H(cloud.Bridge()))
	// The op under test is `lease`: it is the one that creates, and every other op
	// acts on what it returns. Registered exactly as Mount registers it.
	zip.Post[plane.LeaseIn, plane.Leased](app, "/v1/sandbox/lease", planeLease,
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
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d %s — want 403; a tenant resolved from a header "+
				"nobody stood behind is another org's store and another org's pod",
				tc.what, resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
}

// And the plane shape still works, because that is the door the ops were built
// for: a caller stated in-process resolves, and nothing stated does not.
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
		org, err := principal.Acting(tc.ctx)
		if (err == nil) != tc.resolved {
			t.Errorf("%s: principal.Acting = %q,%v — want resolved=%v", tc.what, org, err, tc.resolved)
		}
	}
}
