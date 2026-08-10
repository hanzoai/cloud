package cloud_test

// roundtrip_test.go — the other half of absent_test.go.
//
// That file shows a call ERRORS when the app it wants is gone. Alone that proves
// little: a call that always failed would pass it. This one stands the app up on
// a real socket and shows the same call arrives, carries the caller's org, and
// comes back with the app's answer.
//
// Together they pin both sides — gone means a loud, readable error; there means
// the capability. Nothing returns a zero value either way.
//
// These go through the public functions (OnGitPush, Sync, UpsertIssue), not
// through Ask, so what is tested is what production calls: the op name, the wire
// types and the org forwarding. Any of those drifting would leave absent_test.go
// green and production broken.

import (
	"github.com/hanzoai/cloud/internal/planetest"
	"context"
	"net"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// serve binds one app on a real socket and returns once it accepts. The ops go
// on that app's own zip.App, not on cloud.Plane(), which is process-wide and
// would leak registrations between tests.
func serve(t *testing.T, name string, declare func(*zip.App)) {
	t.Helper()
	app := zip.New(zip.Config{AppName: name})
	declare(app)
	go func() { _ = app.Listen(zip.SocketPath(name)) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", zip.SocketPath(name)); err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never began listening", zip.SocketPath(name))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// alone points this test's sockets at its own directory and claims no router, so
// nothing here can reach a real app or be reached by one.
func alone(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	t.Setenv("ZIP_ADDR", "")
	t.Setenv("CLOUD_RUN_DIR", "")
}

// TestPushReachesPlatform is the one that matters most.
//
// git holds the push, platform holds the builder. Before this the builder was nil
// in git's process and OnGitPush returned nil, so every push in the fleet built
// nothing and reported success.
func TestPushReachesPlatform(t *testing.T) {
	alone(t)
	cloud.RegisterPushBuilder(nil) // nothing co-resident: force the plane
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

	type got struct {
		org string
		in  plane.PushIn
	}
	seen := make(chan got, 1)
	serve(t, "platform", func(app *zip.App) {
		zip.Post[plane.PushIn, plane.Built](app, "/platform/push",
			func(ctx context.Context, in *plane.PushIn) (*plane.Built, error) {
				seen <- got{org: cloud.Who(ctx).Org, in: *in}
				return &plane.Built{Repo: in.Repo}, nil
			}, zip.WithOperationID(plane.PlatformPush))
	})

	err := cloud.OnGitPush(context.Background(), cloud.GitPushEvent{
		Org: "acme", Project: "web", Repo: "site",
		Ref: "refs/heads/main", Commit: "deadbeef",
		CloneURL: "https://git.hanzo.ai/v1/git/acme/site.git",
	})
	if err != nil {
		t.Fatalf("OnGitPush over the plane: %v", err)
	}

	select {
	case g := <-seen:
		// The tenant arrives from the CALLER. plane.PushIn has no Org field on
		// purpose — a push able to name one would enqueue builds against another
		// tenant's apps and spend that tenant's compute.
		if g.org != "acme" {
			t.Errorf("platform saw org %q, want acme (the org rides the caller)", g.org)
		}
		if g.in.Repo != "site" || g.in.Ref != "refs/heads/main" || g.in.Commit != "deadbeef" {
			t.Errorf("platform got %+v, want the pushed repo/ref/commit", g.in)
		}
		if g.in.CloneURL == "" {
			t.Error("CloneURL did not cross; the builder resolves which app tracks the repo from it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("platform never got the push")
	}
}

// TestSyncReachesEngine — integrations and git trigger, sync holds the engine, so
// this was nil on every path that fires and answered "not registered" while the
// engine was healthy next door.
func TestSyncReachesEngine(t *testing.T) {
	alone(t)
	cloud.RegisterSync(nil)
	t.Cleanup(func() { cloud.RegisterSync(nil) })

	serve(t, "sync", func(app *zip.App) {
		zip.Post[plane.SyncIn, plane.SyncRan](app, "/sync/run",
			func(ctx context.Context, in *plane.SyncIn) (*plane.SyncRan, error) {
				if cloud.Who(ctx).Org != "acme" {
					return nil, zip.ErrForbidden("sync run: wrong org")
				}
				return &plane.SyncRan{Ran: 2, Skipped: 1}, nil
			}, zip.WithOperationID(plane.SyncRun))
	})

	res, err := cloud.Sync(context.Background(), cloud.SyncEvent{
		Kind: "git", Provider: "github", Org: "acme",
		Repo: "site", Ref: "refs/heads/main",
	})
	if err != nil {
		t.Fatalf("Sync over the plane: %v", err)
	}
	if res.Ran != 2 || res.Skipped != 1 {
		t.Fatalf("Sync = %+v, want the engine's own counts {Ran:2 Skipped:1}", res)
	}
}

// TestUpsertReachesTracker — integrations holds the GitHub App, tracker holds the
// store, so every mirrored issue was refused.
func TestUpsertReachesTracker(t *testing.T) {
	alone(t)
	cloud.RegisterIssueSink(nil)
	t.Cleanup(func() { cloud.RegisterIssueSink(nil) })

	serve(t, "tracker", func(app *zip.App) {
		zip.Post[plane.IssueIn, plane.IssueUpserted](app, "/tracker/upsert",
			func(ctx context.Context, in *plane.IssueIn) (*plane.IssueUpserted, error) {
				if cloud.Who(ctx).Org != "acme" {
					return nil, zip.ErrForbidden("tracker upsert: wrong org")
				}
				if in.ExtRef != "github:acme/site#42" {
					return nil, zip.ErrBadRequest("tracker upsert: ExtRef did not cross intact")
				}
				return &plane.IssueUpserted{Created: true, Number: 42, Identifier: "GH-42"}, nil
			}, zip.WithOperationID(plane.TrackerUpsert))
	})

	res, err := cloud.UpsertIssue(context.Background(), cloud.IssueUpsert{
		Org: "acme", ProjectKey: "GH", ProjectName: "GitHub",
		Repo: "site", ExtRef: "github:acme/site#42",
		Kind: "issue", Source: "git", Title: "a bug", State: "open",
	})
	if err != nil {
		t.Fatalf("UpsertIssue over the plane: %v", err)
	}
	if !res.Created || res.Number != 42 || res.Identifier != "GH-42" {
		t.Fatalf("UpsertIssue = %+v, want the tracker's own {Created:true Number:42 GH-42}", res)
	}
}

// TestLocalWins: a co-resident app must not pay a socket round trip to reach
// itself. There is no listener here, so bypassing the local call could only fail.
func TestLocalWins(t *testing.T) {
	alone(t)

	called := false
	cloud.RegisterSync(func(_ context.Context, ev cloud.SyncEvent) (cloud.SyncResult, error) {
		called = true
		return cloud.SyncResult{Ran: 7}, nil
	})
	t.Cleanup(func() { cloud.RegisterSync(nil) })

	res, err := cloud.Sync(context.Background(), cloud.SyncEvent{Kind: "git", Org: "acme"})
	if err != nil {
		t.Fatalf("co-resident Sync = %v, want the local call with no socket present", err)
	}
	if !called || res.Ran != 7 {
		t.Fatalf("co-resident Sync did not take the local path (called=%v res=%+v)", called, res)
	}
}
