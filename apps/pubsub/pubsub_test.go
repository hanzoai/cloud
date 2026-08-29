package pubsub

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func testDeps() cloud.Deps { return cloud.Deps{} }

// compose gives the test app what every real composer gives its program: the
// fused host installs cloud.Bridge at its root and a plugin program's
// constructor does the same, so a bare test app that skipped it would answer
// 403 for a reason production can never produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func testApp() *zip.App {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	return app
}

// TestMountServesAndShutsDown: Mount always opens the embedded server (on a
// random port here) and Shutdown tears it down cleanly.
func TestMountServesAndShutsDown(t *testing.T) {
	srv = nil
	t.Setenv("CLOUD_PUBSUB_PORT", "-1") // random free port
	t.Setenv("CLOUD_PUBSUB_STORE_DIR", t.TempDir())

	if err := Use(testApp(), testDeps()); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	if srv == nil {
		t.Fatal("Mount did not start a server")
	}
	if srv.ClientURL() == "" {
		t.Fatal("empty client URL")
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if srv != nil {
		t.Fatal("shutdown did not clear server ref")
	}
}
