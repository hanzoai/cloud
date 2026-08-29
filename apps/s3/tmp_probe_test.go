package s3

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

func TestTmpUploadBucketBinding(t *testing.T) {
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	t.Setenv("S3_ADMIN_ENDPOINT", "127.0.0.1:1")
	t.Setenv("CLOUD_S3_FEE_CENTS", "0")
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Use(app, cloud.Deps{Env: "mainnet"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/s3/buckets/photos/objects", strings.NewReader(`{"key":"a.txt"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u-acme")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	n, _ := resp.Body.Read(buf)
	t.Logf("PATH-ONLY BUCKET -> %d %s", resp.StatusCode, buf[:n])

	req2 := httptest.NewRequest("POST", "/v1/s3/buckets/photos/objects", strings.NewReader(`{"bucket":"photos","key":"a.txt"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Org-Id", "acme")
	req2.Header.Set("X-User-Id", "u-acme")
	resp2, _ := app.Test(req2)
	n2, _ := resp2.Body.Read(buf)
	t.Logf("BODY BUCKET      -> %d %s", resp2.StatusCode, buf[:n2])
}
