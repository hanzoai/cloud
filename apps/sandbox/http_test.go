package sandbox

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// mountHTTP mounts the sandbox app as its plugin does, on a data directory of the
// test's own.
func mountHTTP(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	// Use starts the reaper, and nothing stops it: it outlives this test and sweeps
	// whatever store CLOUD_SQL_DSN names when it next wakes, which is a later test's.
	// An open store there asks for that test's leases under its own holder and takes
	// them, so the later test's park is refused as "no longer this replica's". The
	// drain closes the stores, and a closed store sweeps nothing and owns nothing.
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

func req(t *testing.T, app *zip.App, method, path, org, body string) (int, []byte) {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if org != "" {
		r.Header.Set("X-Org-Id", org)
		r.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 180 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
