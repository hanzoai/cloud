package iam

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"context"
	"github.com/hanzoai/cloud"
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
	db, err := store.Open("sqlite", StorePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "hanzo", "cert-hanzo"
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("hanzo/cert-hanzo")
	if err := cert.CreateCtx(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	t.Setenv("IAM_ISSUER", "https://hanzo.id")
	t.Setenv("IAM_ISSUER_MAP", `{"hanzo.id":"https://hanzo.id","lux.id":"https://lux.id","zoolabs.id":"https://zoolabs.id"}`)
	t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))

	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{DataDir: dir}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	for host, want := range map[string]string{
		"hanzo.id":   "https://hanzo.id",
		"lux.id":     "https://lux.id",
		"zoolabs.id": "https://zoolabs.id",
	} {
		req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
		req.Host = host
		resp, err := app.Test(req)
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
