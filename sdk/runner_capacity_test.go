package cloud

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/zap-proto/zip"
)

// platformPeer serves the platform_job op over the plane and answers every
// launch with refuse.
func platformPeer(t *testing.T, refuse error) {
	t.Helper()
	t.Setenv(zip.RuntimeDirEnv, kit.Sockets(t))
	RegisterRunner(nil)
	app := zip.New(zip.Config{AppName: "platform"})
	app.Post("/platform/job", func(context.Context, *client.JobIn) (*client.Launched, error) {
		return nil, refuse
	}, zip.WithOperationID(client.PlatformJob))
	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	sp := zip.SocketPath("platform")
	go func() { _ = app.Listen(sp) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(sp); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the platform socket never appeared at %s", sp)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func queuedJob() JobEvent {
	return JobEvent{Org: "hanzo", Provider: "forge", Repo: "hanzoai/gui", ID: 7, Labels: []string{"linux-amd64"}, URL: "https://git.hanzo.ai", Token: "t"}
}

// A full cluster crosses the plane as the platform op's 429 and arrives as
// ErrRunnerCapacity, so a receiver decides on the error value.
func TestAFullClusterArrivesAsErrRunnerCapacity(t *testing.T) {
	platformPeer(t, zip.Errorf(http.StatusTooManyRequests, "platform job: platform: too many concurrent runners on this cluster"))
	_, err := OnWorkflowJob(context.Background(), queuedJob())
	if !errors.Is(err, ErrRunnerCapacity) {
		t.Fatalf("a 429 from platform = %v, want ErrRunnerCapacity", err)
	}
}

// Any other platform failure is not a capacity refusal.
func TestAPlatformFailureIsNotErrRunnerCapacity(t *testing.T) {
	platformPeer(t, zip.Errorf(http.StatusInternalServerError, "platform job: apiserver unreachable"))
	_, err := OnWorkflowJob(context.Background(), queuedJob())
	if err == nil || errors.Is(err, ErrRunnerCapacity) {
		t.Fatalf("a 500 from platform = %v, want a failure that is not ErrRunnerCapacity", err)
	}
}
