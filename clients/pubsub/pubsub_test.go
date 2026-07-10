package pubsub

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func testDeps() cloud.Deps { return cloud.Deps{Logger: luxlog.New("test")} }
func testApp() *zip.App    { return zip.New(zip.Config{Logger: luxlog.New("test")}) }

// TestMountDisabledIsNoop: with CLOUD_PUBSUB_ENABLED unset, Mount binds nothing
// and starts no server — existing deployments are untouched.
func TestMountDisabledIsNoop(t *testing.T) {
	srv = nil
	if err := Mount(testApp(), testDeps()); err != nil {
		t.Fatalf("disabled Mount: %v", err)
	}
	if srv != nil {
		t.Fatal("disabled Mount started a server")
	}
}

// TestMountEnabledServesAndShutsDown: enabled, Mount opens the embedded server
// on a random port and Shutdown tears it down cleanly.
func TestMountEnabledServesAndShutsDown(t *testing.T) {
	srv = nil
	t.Setenv("CLOUD_PUBSUB_ENABLED", "true")
	t.Setenv("CLOUD_PUBSUB_PORT", "-1") // random free port
	t.Setenv("CLOUD_PUBSUB_STORE_DIR", t.TempDir())

	if err := Mount(testApp(), testDeps()); err != nil {
		t.Fatalf("enabled Mount: %v", err)
	}
	if srv == nil {
		t.Fatal("enabled Mount did not start a server")
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
