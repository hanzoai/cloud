package basetest

import (
	"context"
	"os"
	"testing"

	"github.com/hanzoai/cloud/apps/base"
	basepeer "github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Serve mounts the base app with the embed on and serves the plane as the base
// peer for the rest of the test. It sets CLOUD_DATA_DIR, so a test that mounts
// another store beside it calls Serve first.
func Serve(t *testing.T) {
	t.Helper()
	t.Setenv("CLOUD_BASE_EMBED", "1")
	t.Setenv("BASE_API_PREFIX", "/v1/base")
	if os.Getenv("CLOUD_DATA_DIR") == "" {
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	}
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	if err := base.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("basetest: mount base: %v", err)
	}
	t.Cleanup(func() { _ = base.Shutdown(context.Background()) })
	// A peer of the test's own, not the process plane: a plane that has served is
	// frozen, and the next test's mount registers the ops again.
	peer := zip.New(zip.Config{AppName: basepeer.App, Logger: luxlog.NewNoOpLogger()})
	base.PlaneOps(peer)
	planetest.Bind(t, basepeer.App, peer)
}
