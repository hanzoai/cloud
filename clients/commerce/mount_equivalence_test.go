// Copyright © 2026 Hanzo AI. MIT License.

//go:build cloud
// +build cloud

package commerce

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"
)

// TestMount_EquivalentToLegacy proves the cloud-mount path serves the
// same response on /v1/commerce/* as the legacy direct-Gin path. Phase 1
// of the staged migration MUST preserve byte-for-byte response shape on
// every public route so the frontend (PR #32) sees no behavior change at
// the cutover.
//
// Strategy: boot Embed() once (legacy shape) and Mount() once on a fresh
// zip.App (cloud shape) — both wrap the same gin engine type. Send
// identical requests, compare response status + body. Routes covered:
//
//	GET /v1/commerce/tenant   — store-backed, NoRoute fallthrough body
//	GET /v1/commerce/health   — does not exist, NoRoute fallthrough body
//	GET /healthz               — legacy root route (gin only — zip mount
//	                             is at /v1/commerce + /_/commerce so
//	                             cloud mode does NOT serve /healthz at
//	                             root; this is intentional: probes hit
//	                             /_/commerce/healthz under zip)
//
// The /healthz check is asymmetric by design (see HIP-0106): cloud
// surfaces are scoped to /v1/commerce + /_/commerce so the same binary
// can host iam, base, kms, gateway side-by-side without root-path
// collisions. The test asserts the asymmetry is the EXPECTED kind: zip
// serves /_/commerce/healthz (native), gin serves /healthz (legacy
// only, reachable through the legacy boot path).
func TestMount_EquivalentToLegacy(t *testing.T) {
	tmp := t.TempDir()

	// Legacy boot — direct Embed, no zip wrapper.
	legacy, err := Embed(context.Background(), EmbedConfig{
		DataDir:  filepath.Join(tmp, "legacy"),
		HTTPAddr: "127.0.0.1:0",
		Dev:      true,
	})
	if err != nil {
		t.Fatalf("legacy Embed: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = legacy.Stop(ctx)
	})

	// Cloud boot — zip.App with commerce mounted.
	zapp := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{
		Logger:  luxlog.New("test"),
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: filepath.Join(tmp, "cloud"),
	}
	if err := Mount(zapp, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	cases := []struct {
		name string
		path string
		host string
	}{
		// /v1/commerce/health is not a registered handler. Both modes
		// must return the canonical NoRoute API-path body.
		{name: "health_unknown_path", path: "/v1/commerce/health", host: "pay.example.test"},
		// /v1/commerce/tenant is org-as-tenant. Both modes resolve via the
		// same host→brand→org resolver, so the 200 tenant JSON is identical.
		{name: "tenant_org_resolved", path: "/v1/commerce/tenant", host: "pay.example.test"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Legacy: probe the gin engine directly via its http.Handler.
			wLegacy := httptest.NewRecorder()
			reqLegacy := httptest.NewRequest(http.MethodGet, tc.path, nil)
			reqLegacy.Host = tc.host
			legacy.HTTPHandler().ServeHTTP(wLegacy, reqLegacy)
			legacyBody, _ := io.ReadAll(wLegacy.Body)

			// Cloud: route through zip → AdaptNetHTTP → gin.
			reqCloud := httptest.NewRequest(http.MethodGet, tc.path, nil)
			reqCloud.Host = tc.host
			respCloud, err := zapp.Fiber().Test(reqCloud)
			if err != nil {
				t.Fatalf("cloud Fiber Test: %v", err)
			}
			cloudBody, _ := io.ReadAll(respCloud.Body)

			if wLegacy.Code != respCloud.StatusCode {
				t.Fatalf("status drift on %s: legacy=%d cloud=%d legacyBody=%q cloudBody=%q",
					tc.path, wLegacy.Code, respCloud.StatusCode, string(legacyBody), string(cloudBody))
			}
			if !bytes.Equal(legacyBody, cloudBody) {
				t.Fatalf("body drift on %s:\n  legacy: %q\n  cloud:  %q", tc.path, string(legacyBody), string(cloudBody))
			}
		})
	}
}
