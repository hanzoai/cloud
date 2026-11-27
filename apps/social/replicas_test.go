package social

// Social runs on every replica. These tests stand a second replica beside the first:
// on hanzo-sql a second pool on the same store, which is what another pod is; on
// SQLite the same file, because a file belongs to one process.

import (
	"context"
	"github.com/hanzoai/cloud/internal/kmstest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// replicaOf is another replica's store beside s.
func replicaOf(t *testing.T, s *Store) *Store {
	t.Helper()
	if !dialect.Of(s.db).Postgres() {
		return s
	}
	other, err := openStore(t.TempDir(), testMaster, kmstest.New())
	if err != nil {
		t.Fatalf("a second replica's store: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	return other
}

func filed(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.CreatePost(context.Background(), socialPost{ID: id, Org: "o", Content: "c", Channel: "x", Status: statusScheduled, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
}

// A replica starting while another is publishing fails nothing the other holds: the
// post stays claimed, cannot be claimed again, and the publisher's outcome lands.
func TestAStartingReplicaLeavesALivePublishAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		publisher := testStore(t)
		starting := replicaOf(t, publisher)
		filed(t, publisher, "p")
		now := time.Now().Unix()
		if _, ok, err := publisher.ClaimForPublish(ctx, "o", "p", now); err != nil || !ok {
			t.Fatalf("the publisher's claim: ok=%v err=%v", ok, err)
		}

		// The starting replica recovers a minute into the attempt.
		n, err := starting.RecoverStuckPublishing(ctx, now+60, int64(publishStale.Seconds()))
		if err != nil || n != 0 {
			t.Fatalf("a starting replica reset %d live publishes (%v), want 0", n, err)
		}
		p, err := starting.GetPost(ctx, "o", "p")
		if err != nil || p.Status != "publishing" {
			t.Fatalf("the post is %q (%v), want it still publishing", p.Status, err)
		}
		if _, ok, err := starting.ClaimForPublish(ctx, "o", "p", now+60); err != nil || ok {
			t.Fatalf("the starting replica claimed a post being published: ok=%v err=%v", ok, err)
		}
		if err := publisher.MarkPublished(ctx, "o", "p", "a", "ext", "", now+61); err != nil {
			t.Fatal(err)
		}
		if p, _ := starting.GetPost(ctx, "o", "p"); p.Status != statusPublished {
			t.Fatalf("the publisher's outcome was lost: %q", p.Status)
		}
	})
}

// A post whose publisher is gone is failed once its claim is older than the longest
// attempt, by any replica, and is then claimable again; one a moment younger is not.
func TestAPublishWhoseProcessIsGoneIsFailedOnceStale(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		dead := testStore(t)
		survivor := replicaOf(t, dead)
		filed(t, dead, "p")
		now := time.Now().Unix()
		stale := int64(publishStale.Seconds())
		if _, ok, err := dead.ClaimForPublish(ctx, "o", "p", now); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if n, err := survivor.RecoverStuckPublishing(ctx, now+stale-1, stale); err != nil || n != 0 {
			t.Fatalf("a claim a second short of stale was reset: %d %v", n, err)
		}
		n, err := survivor.RecoverStuckPublishing(ctx, now+stale, stale)
		if err != nil || n != 1 {
			t.Fatalf("a stale claim was not reset: %d %v", n, err)
		}
		if p, _ := survivor.GetPost(ctx, "o", "p"); p.Status != statusFailed {
			t.Fatalf("the post is %q, want failed", p.Status)
		}
		if _, ok, err := survivor.ClaimForPublish(ctx, "o", "p", now+stale+1); err != nil || !ok {
			t.Fatalf("a failed post is retryable: ok=%v err=%v", ok, err)
		}
	})
}

// Mounting is the start of a replica: it must not fail a post another replica
// claimed a moment ago. (Use is what a surge pod runs at boot.) The claim is on
// record before the mount, as it is on the pod that made it; a file is read by one
// process at a time, so the claiming handle is let go first.
func TestMountingLeavesALivePublishAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_ = Shutdown()
		dir := t.TempDir()
		t.Setenv("CLOUD_DATA_DIR", dir)
		publisher, err := openStore(dir, testMaster, kmstest.New())
		if err != nil {
			t.Fatal(err)
		}
		filed(t, publisher, "p")
		ctx := context.Background()
		if _, ok, err := publisher.ClaimForPublish(ctx, "o", "p", time.Now().Unix()); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if err := publisher.Close(); err != nil {
			t.Fatal(err)
		}

		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		if err := Use(app, cloud.Deps{MasterKey: testMaster}); err != nil {
			t.Fatalf("Use: %v", err)
		}
		t.Cleanup(func() { _ = Shutdown() })
		if p, err := mounted.State.store.GetPost(ctx, "o", "p"); err != nil || p.Status != "publishing" {
			t.Fatalf("mounting reset a live publish: %q (%v)", p.Status, err)
		}
	})
}

// The replica that leads the scheduler finds the publish of a pod that died, without
// waiting for a restart: a sweep fails a post whose claim is stale and leaves a fresh one.
func TestASweepFailsAPublishWhoseProcessIsGone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s := testService(t, &fakePublisher{extID: "x"})
		st := s.State.store
		filed(t, st, "gone")
		filed(t, st, "live")
		now := time.Now().Unix()
		if _, ok, err := st.ClaimForPublish(ctx, "o", "gone", now-int64(publishStale.Seconds())-1); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if _, ok, err := st.ClaimForPublish(ctx, "o", "live", now); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		sweepDue(s)
		if p, _ := st.GetPost(ctx, "o", "gone"); p.Status != statusFailed {
			t.Fatalf("the stale claim is %q, want failed", p.Status)
		}
		if p, _ := st.GetPost(ctx, "o", "live"); p.Status != "publishing" {
			t.Fatalf("the fresh claim is %q, want it left alone", p.Status)
		}
	})
}
