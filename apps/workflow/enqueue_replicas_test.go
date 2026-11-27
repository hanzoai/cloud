package workflow

// Two replicas enqueue one id at once and get one job. The enqueue used to be held to
// one by a mutex in the process, which excludes the pod it is in; it is held by the
// engine now, under a lock every replica holds, so the same ids sent through
// different pods are still one job each, and a job a worker has claimed is not reset
// by an enqueue that arrives after.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
)

// replica is one pod's engine, on its own pool onto the one schema.
func replica(t *testing.T, name string) *tasks.Embedded {
	t.Helper()
	db, err := locus.Open(namespace.System(), "tasks-workflow", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv, err := tasks.Embed(t.Context(), tasks.EmbedConfig{
		Address:  kit.Sockets(t) + "/" + name + ".sock",
		Database: db,
		NodeID:   name,
	})
	if err != nil {
		t.Fatalf("tasks.Embed %s: %v", name, err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	return srv
}

func TestTwoReplicasEnqueueOneIdOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("two replicas share a server; a SQLite file has one")
		}
		a, b := replica(t, "a"), replica(t, "b")
		var turn atomic.Int64
		prev := engine
		engine = func() *tasks.Embedded { return []*tasks.Embedded{a, b}[turn.Add(1)%2] }
		t.Cleanup(func() { engine = prev })
		if err := a.View(tasks.Org("acme")).RegisterNamespace(tasks.Namespace{NamespaceInfo: tasks.NamespaceInfo{Name: "gpu-jobs"}}); err != nil {
			t.Fatal(err)
		}
		ctx := cloud.For(context.Background(), "acme")

		const jobs, tries = 20, 8
		var wg sync.WaitGroup
		release := make(chan struct{})
		for j := 0; j < jobs; j++ {
			in := job(fmt.Sprintf("run_x_%02d", j))
			for k := 0; k < tries; k++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-release
					if _, err := planeEnqueue(ctx, in); err != nil {
						t.Errorf("enqueue %s: %v", in.ID, err)
					}
				}()
			}
		}
		close(release)
		wg.Wait()
		rows, _, err := b.ActivitiesPageForOrg("acme", "gpu-jobs", "", 0)
		if err != nil || len(rows) != jobs {
			t.Fatalf("%d jobs were enqueued and the lane holds %d (%v)", jobs, len(rows), err)
		}
		for _, r := range rows {
			if r.HistoryLength != 1 {
				t.Fatalf("%s holds %d events after %d enqueues: it was made more than once", r.Execution.WorkflowId, r.HistoryLength, tries)
			}
		}

		// A worker claims one through one pod; a late enqueue through either leaves it claimed.
		code, body := req(t, httpMux(a), http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities/claim", "acme",
			map[string]any{"taskQueue": "gpu:dgx", "identity": "dgx", "leaseSeconds": 120})
		if code != http.StatusOK {
			t.Fatalf("claim = %d %s", code, body)
		}
		for k := 0; k < 6; k++ {
			for j := 0; j < jobs; j++ {
				if _, err := planeEnqueue(ctx, job(fmt.Sprintf("run_x_%02d", j))); err != nil {
					t.Fatal(err)
				}
			}
		}
		claimed := 0
		after, _, _ := b.ActivitiesPageForOrg("acme", "gpu-jobs", "", 0)
		for _, r := range after {
			if r.Status == "ACTIVITY_TASK_STATE_STARTED" {
				claimed++
			}
		}
		if claimed != 1 {
			t.Fatalf("%d jobs are claimed after a retried enqueue of every id; exactly the one a worker claimed", claimed)
		}
	})
}
