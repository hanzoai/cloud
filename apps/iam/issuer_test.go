package iam

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"context"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
)

// Each brand issues as itself. `iss` is what a relying party pins, so one issuer
// for every brand means hanzo.id looks perfect while every other brand's tokens
// are rejected by its own clients — which is how it shipped: a repoint passed
// the gate that existed (the key set matched) and was still wrong.
//
// This is the in-process half. The other was the Host not surviving the plugin
// hop, which no config could reach: zip v1.27.3 / zap-proto/http v0.3.2.
func TestDiscoveryIssuesAsTheBrandItWasAskedAs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(StorePath(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open("sqlite", StorePath(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	// Owned by the reserved org, with its key mounted: a signing cert is trusted
	// only under a reserved owner, and its private half comes from the deployment
	// rather than the row. Discovery is served by a store that can actually sign.
	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "admin", "cert-hanzo"
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("admin/cert-hanzo")
	if err := cert.CreateCtx(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	mountSigningKey(t, "cert-hanzo")

	t.Setenv("IAM_ISSUER", "https://hanzo.id")
	t.Setenv("IAM_ISSUER_MAP", `{"hanzo.id":"https://hanzo.id","lux.id":"https://lux.id","zoolabs.id":"https://zoolabs.id"}`)
	t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))

	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	t.Setenv("CLOUD_DATA_DIR", dir)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}

	for host, want := range map[string]string{
		"hanzo.id":   "https://hanzo.id",
		"lux.id":     "https://lux.id",
		"zoolabs.id": "https://zoolabs.id",
	} {
		req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
		req.Host = host
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("Test(%s): %v", host, err)
		}
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		_ = resp.Body.Close()
		body := string(buf[:n])
		if !strings.Contains(body, `"issuer":"`+want+`"`) {
			t.Errorf("Host %s issued as something other than %s — its relying parties would reject every token:\n  %.200s", host, want, body)
		}
	}
}

// A loopback boot pins no issuer — IAM_ISSUER must be https — and opts into
// host-relative ones instead, so the instance is its own issuer at the address it
// was reached on. The brand map is not derived there: IAM refuses a map with no
// default, and deriving one made every identity address on `make dev` answer 503.
func TestALoopbackBootIssuesAsItsOwnAddress(t *testing.T) {
	dir := storeWithSigningCert(t, "cert-loopback")
	mountSigningKey(t, "cert-loopback")
	t.Setenv("IAM_ISSUER", "")
	t.Setenv("IAM_ISSUER_MAP", "")
	t.Setenv("IAM_DEV_HOST_RELATIVE", "1")
	t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))
	t.Setenv("CLOUD_DATA_DIR", dir)

	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if m := os.Getenv("IAM_ISSUER_MAP"); m != "" {
		t.Errorf("IAM_ISSUER_MAP derived with no IAM_ISSUER to back it: %.120s", m)
	}

	req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	req.Host = "127.0.0.1:18080"
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	_ = resp.Body.Close()
	body := string(buf[:n])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery on a loopback boot = %d, want 200: %.200s", resp.StatusCode, body)
	}
	if want := `"issuer":"http://127.0.0.1:18080"`; !strings.Contains(body, want) {
		t.Errorf("discovery does not name the address it was asked at (%s):\n  %.200s", want, body)
	}
}

// An issuer config IAM refuses is an outage, answered as one. The store here can
// sign, so the issuer config is the only thing that differs between the cases: a
// malformed IAM_ISSUER_MAP leaves IAM with no issuer to mint under, and every
// identity address answers 503 rather than a discovery document that names none.
func TestARefusedIssuerConfigAnswers503(t *testing.T) {
	for _, tc := range []struct {
		name, cert, issuerMap string
		status                int
	}{
		{name: "malformed IAM_ISSUER_MAP", cert: "cert-issuer-refused", issuerMap: `{not json}`, status: http.StatusServiceUnavailable},
		{name: "valid IAM_ISSUER_MAP", cert: "cert-issuer-pinned", issuerMap: `{"hanzo.id":"https://hanzo.id","lux.id":"https://lux.id"}`, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := storeWithSigningCert(t, tc.cert)
			mountSigningKey(t, tc.cert)
			t.Setenv("IAM_ISSUER", "https://hanzo.id")
			t.Setenv("IAM_ISSUER_MAP", tc.issuerMap)
			t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))

			app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
			t.Setenv("CLOUD_DATA_DIR", dir)
			if err := Use(app, cloud.Deps{}); err != nil {
				t.Fatalf("Use must stay up whatever the issuer config, cloud depends on it: %v", err)
			}
			if got, want := DB() != nil, tc.status == http.StatusOK; got != want {
				t.Errorf("DB() non-nil = %v, want %v", got, want)
			}

			for _, path := range []string{
				"/v1/iam/.well-known/jwks",
				"/v1/iam/.well-known/openid-configuration",
				"/.well-known/openid-configuration",
			} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Host = "hanzo.id"
				resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
				if err != nil {
					t.Fatalf("Test(%s): %v", path, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != tc.status {
					t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, tc.status)
				}
			}
		})
	}
}
