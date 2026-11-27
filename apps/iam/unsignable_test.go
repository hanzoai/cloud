// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
)

// AN ORGANIZATION'S APPLICATION THAT CANNOT SIGN IS TOLD WHICH CERT AND WHY
// (hanzo-inc/cloud#757).
//
// An org registers an application, adds a certificate, points the application at
// it, and every token exchange fails. That is IAM's trust boundary working: only
// a cert owned by a reserved platform org signs, because only the platform holds
// signing keys, so a cert an organization adds is never read for signing. What was
// wrong is what the exchange SAID — "this application has no signing cert" to an
// application whose row names one — which sent the operator back to add more
// certificates. The answer now names the registration, the cert it names, why that
// cert cannot sign, and who can fix it.
//
// Driven through the composed IAM, the way cloud serves it, so a cloud pinned to
// an IAM without the named answer fails here.
func TestAnOrgsApplicationNamesTheCertItCannotSignWith(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(StorePath(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open("sqlite", StorePath(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The platform's cert, keyed: the store can sign, so the refusal below is
	// about the application and nothing else.
	platform := orm.New[model.Cert](db)
	platform.Owner, platform.Name = "admin", "cert-unsignable-platform"
	platform.Type, platform.CryptoAlgorithm, platform.BitSize = "x509", "RS256", 2048
	platform.SetId("admin/cert-unsignable-platform")
	if err := platform.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	// The org's own cert, which is the reported shape: present, and never a
	// signing cert.
	own := orm.New[model.Cert](db)
	own.Owner, own.Name = "acme-test", "portal-test-cert"
	own.Type, own.CryptoAlgorithm, own.BitSize = "x509", "RS256", 2048
	own.SetId("acme-test/portal-test-cert")
	if err := own.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	a := orm.New[model.Application](db)
	a.Owner, a.Name, a.Organization = "acme-test", "portal-test", "acme-test"
	a.ClientId, a.ClientSecret, a.Cert = "portal-test", "portal-test-secret", "portal-test-cert"
	a.GrantTypes = []string{"client_credentials"}
	a.ExpireInHours = 1
	a.SetId("acme-test/portal-test")
	if err := a.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	mountSigningKey(t, "cert-unsignable-platform", "portal-test-cert")

	t.Setenv("IAM_ISSUER", "https://hanzo.id")
	t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	t.Setenv("CLOUD_DATA_DIR", dir)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	form := url.Values{
		"grant_type": {"client_credentials"}, "client_id": {"portal-test"}, "client_secret": {"portal-test-secret"},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/iam/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "hanzo.id"
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]string
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("token answer is not JSON: %s", raw)
	}
	if resp.StatusCode != http.StatusInternalServerError || body["error"] != "server_error" {
		t.Fatalf("token = %d %s, want 500 server_error", resp.StatusCode, raw)
	}
	if body["access_token"] != "" {
		t.Fatal("an organization's own cert signed a token")
	}
	desc := body["error_description"]
	for _, want := range []string{
		"acme-test/portal-test",             // which registration
		"'portal-test-cert'",                // the cert it names
		"not one of the platform's signing", // why it cannot sign
		"platform administrator",            // who can fix it
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("error_description %q does not say %q", desc, want)
		}
	}
}
