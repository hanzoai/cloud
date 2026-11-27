package workflow

// The plane writes are only worth having if the fleet sees what they write: a job
// enqueued over the plane must be the job a GPU worker claims through
// /v1/workflow, in the same org's shard, and nothing another org can read.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	workflowpeer "github.com/hanzoai/cloud/client/workflow"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
)

// linked is an engine where acme has a GPU lane — a worker linked and registered it.
func linked(t *testing.T) *tasks.Embedded {
	t.Helper()
	srv := testEngine(t)
	prev := engine
	engine = func() *tasks.Embedded { return srv }
	t.Cleanup(func() { engine = prev })
	if err := srv.View(tasks.Org("acme")).RegisterNamespace(tasks.Namespace{NamespaceInfo: tasks.NamespaceInfo{Name: "gpu-jobs"}}); err != nil {
		t.Fatal(err)
	}
	return srv
}

// job is a well-formed render job.
func job(id string) *client.EnqueueIn {
	return &client.EnqueueIn{
		Namespace: "gpu-jobs", ID: id, Type: "studio.render", Queue: "gpu:dgx",
		Input:    []byte(`{"prompt":{"1":{"class_type":"SaveImage","inputs":{}}},"org":"acme"}`),
		Attempts: 1, Heartbeat: "1h0m0s", Start: "168h0m0s", Close: "4h0m0s",
	}
}

func TestEnqueuedJobIsTheOneTheFleetClaims(t *testing.T) {
	srv := linked(t)
	ctx := cloud.For(context.Background(), "acme")

	in := job("run_x_0_1")
	for range 2 { // the second is a retry after a lost reply: one job, not two
		if _, err := planeEnqueue(ctx, in); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	rows, _, err := srv.ActivitiesPageForOrg("acme", "gpu-jobs", "", 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("acme's gpu-jobs holds %d jobs (%v), want 1", len(rows), err)
	}

	// The fleet claims it through the HTTP surface, as the org it belongs to.
	mux := httpMux(srv)
	code, body := req(t, mux, http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities/claim", "acme",
		map[string]any{"taskQueue": "gpu:dgx", "identity": "dgx", "leaseSeconds": 120})
	if code != http.StatusOK {
		t.Fatalf("claim = %d %s", code, body)
	}
	var claimed tasks.StandaloneActivity
	if err := json.Unmarshal([]byte(body), &claimed); err != nil || claimed.Execution.WorkflowId != in.ID {
		t.Fatalf("claimed %s (%v), want %s", body, err, in.ID)
	}
	if got := status(t, ctx, in.ID); got != "ACTIVITY_TASK_STATE_STARTED" {
		t.Fatalf("after the claim the plane reads %s", got)
	}
	code, body = req(t, mux, http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities/"+in.ID+"/"+in.ID+"/complete", "acme",
		map[string]any{"result": map[string]any{"gallery": []string{"studio/r/j_00001_.mp4"}}, "identity": "dgx"})
	if code != http.StatusOK {
		t.Fatalf("complete = %d %s", code, body)
	}
	out, err := planeActivity(ctx, &client.ActivityIn{Namespace: "gpu-jobs", ID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	var done tasks.StandaloneActivity
	_ = json.Unmarshal(out.Row, &done)
	if done.Status != "ACTIVITY_TASK_STATE_COMPLETED" || done.Result == nil || done.Input != nil {
		t.Fatalf("finished job reads %+v (a read carries the state, not the input)", done)
	}

	// An ended job cannot be cancelled; an unknown one is 404; another org sees nothing.
	if _, err := planeCancel(ctx, &client.CancelIn{Namespace: "gpu-jobs", ID: in.ID}); httpStatus(err) != http.StatusConflict {
		t.Fatalf("cancel of an ended job: %v, want 409", err)
	}
	if _, err := planeCancel(ctx, &client.CancelIn{Namespace: "gpu-jobs", ID: "nope"}); httpStatus(err) != http.StatusNotFound {
		t.Fatalf("cancel of an unknown job: %v, want 404", err)
	}
	other := cloud.For(context.Background(), "other")
	if _, err := planeActivity(other, &client.ActivityIn{Namespace: "gpu-jobs", ID: in.ID}); httpStatus(err) != http.StatusNotFound {
		t.Fatalf("another org read acme's job: %v", err)
	}
}

func TestCancelAQueuedJob(t *testing.T) {
	srv := linked(t)
	ctx := cloud.For(context.Background(), "acme")
	in := job("j")
	in.Queue = "gpu-jobs"
	if _, err := planeEnqueue(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := planeCancel(ctx, &client.CancelIn{Namespace: "gpu-jobs", ID: "j", Reason: "run cancelled"}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := status(t, ctx, "j"); got != "ACTIVITY_TASK_STATE_CANCELED" {
		t.Fatalf("cancelled job reads %s", got)
	}
	// Nothing claims a cancelled job.
	code, _ := req(t, httpMux(srv), http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities/claim", "acme",
		map[string]any{"taskQueue": "gpu-jobs", "identity": "dgx", "leaseSeconds": 120})
	if code != http.StatusNoContent {
		t.Fatalf("claim after cancel = %d, want 204", code)
	}
}

func TestPlaneWritesRefuseACallWithNoOrg(t *testing.T) {
	ctx := context.Background()
	if _, err := planeEnqueue(ctx, &client.EnqueueIn{Namespace: "gpu-jobs", ID: "j", Type: "t", Queue: "q"}); err == nil {
		t.Fatal("an unattributed enqueue was answered")
	}
	if _, err := planeActivity(ctx, &client.ActivityIn{Namespace: "gpu-jobs", ID: "j"}); err == nil {
		t.Fatal("an unattributed read was answered")
	}
	if _, err := planeCancel(ctx, &client.CancelIn{Namespace: "gpu-jobs", ID: "j"}); err == nil {
		t.Fatal("an unattributed cancel was answered")
	}
}

func status(t *testing.T, ctx context.Context, id string) string {
	t.Helper()
	out, err := planeActivity(ctx, &client.ActivityIn{Namespace: "gpu-jobs", ID: id})
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	var a tasks.StandaloneActivity
	if err := json.Unmarshal(out.Row, &a); err != nil {
		t.Fatal(err)
	}
	return a.Status
}

func httpStatus(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// Over the plane itself, through the generated client a renderer calls: the three
// writes answer under their names, and a job the shard does not hold comes back as
// a 404 the caller can tell from an outage (apps/studio reads it as a job gone from
// the lane, and every other error as a lane that did not answer).
func TestPlaneWritesOverTheSocket(t *testing.T) {
	linked(t)
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeActivities()
	stop, err := cloud.ServePlane(workflowpeer.App, nil)
	if err != nil {
		t.Fatalf("ServePlane: %v", err)
	}
	defer func() { _ = stop() }()

	ctx := cloud.For(context.Background(), "acme")
	in := job("j")
	in.Input = []byte(`{"org":"acme"}`)
	if _, err := workflowpeer.WorkflowEnqueue(ctx, in); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	a, err := workflowpeer.WorkflowActivity(ctx, &client.ActivityIn{Namespace: "gpu-jobs", ID: "j"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var row tasks.StandaloneActivity
	if err := json.Unmarshal(a.Row, &row); err != nil || row.TaskQueue != "gpu:dgx" || row.MaximumAttempts != 1 {
		t.Fatalf("row %s (%v)", a.Row, err)
	}
	if row.Input != nil {
		t.Fatalf("a read relayed the input: %v", row.Input)
	}
	if _, err := workflowpeer.WorkflowCancel(ctx, &client.CancelIn{Namespace: "gpu-jobs", ID: "j"}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	_, err = workflowpeer.WorkflowActivity(ctx, &client.ActivityIn{Namespace: "gpu-jobs", ID: "nope"})
	if he, ok := errors.AsType[*zip.HTTPError](err); !ok || he.Status != http.StatusNotFound {
		t.Fatalf("an unknown job read as %T %v, want a 404", err, err)
	}
}

// The writes take the GPU lane's render jobs and nothing else, stated whole.
func TestEnqueueTakesOnlyARenderJob(t *testing.T) {
	linked(t)
	ctx := cloud.For(context.Background(), "acme")
	for name, mod := range map[string]func(*client.EnqueueIn){
		"another namespace": func(in *client.EnqueueIn) { in.Namespace = "fleet" },
		"another type":      func(in *client.EnqueueIn) { in.Type = "fleet.worker" },
		"no attempts":       func(in *client.EnqueueIn) { in.Attempts = 0 },
		"no lease":          func(in *client.EnqueueIn) { in.Heartbeat = "" },
		"a bad duration":    func(in *client.EnqueueIn) { in.Start = "a week" },
		"a negative window": func(in *client.EnqueueIn) { in.Close = "-1h" },
		"too long a window": func(in *client.EnqueueIn) { in.Start = "8760h" },
		"input not JSON":    func(in *client.EnqueueIn) { in.Input = []byte("{") },
		"input too large":   func(in *client.EnqueueIn) { in.Input = []byte(`"` + strings.Repeat("x", maxInput) + `"`) },
	} {
		in := job("j-" + strings.ReplaceAll(name, " ", "-"))
		mod(in)
		if _, err := planeEnqueue(ctx, in); httpStatus(err) != http.StatusBadRequest && httpStatus(err) != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
	for _, allowedType := range []string{"research.bench", "research.tick", "hanzod.run"} {
		bench := job("j-" + strings.ReplaceAll(allowedType, ".", "-"))
		bench.Type = allowedType
		bench.Input = []byte(`{"name":"gb10-probe-2026-11-22","command":["nvidia-smi"]}`)
		if _, err := planeEnqueue(ctx, bench); err != nil {
			t.Errorf("%s enqueue failed: %v", allowedType, err)
		}
	}
	if _, err := planeActivity(ctx, &client.ActivityIn{Namespace: "fleet", ID: "dgx"}); httpStatus(err) != http.StatusBadRequest {
		t.Errorf("a read of another namespace: %v, want 400", err)
	}
	if _, err := planeCancel(ctx, &client.CancelIn{Namespace: "fleet", ID: "dgx"}); httpStatus(err) != http.StatusBadRequest {
		t.Errorf("a cancel in another namespace: %v, want 400", err)
	}
	// An org no worker ever linked has no lane, and is not given one.
	if _, err := planeEnqueue(cloud.For(context.Background(), "nobody"), job("j")); httpStatus(err) != http.StatusConflict {
		t.Errorf("an enqueue in an org with no lane: %v, want 409", err)
	}
}

// An id enqueued twice at once is one job, and an enqueue of a job a worker has
// claimed answers it as it is — the claim is never reset.
func TestEnqueueNeverResetsAJob(t *testing.T) {
	srv := linked(t)
	ctx := cloud.For(context.Background(), "acme")
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := planeEnqueue(ctx, job("once")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rows, _, err := srv.ActivitiesPageForOrg("acme", "gpu-jobs", "", 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("16 concurrent enqueues of one id left %d jobs (%v)", len(rows), err)
	}
	code, body := req(t, httpMux(srv), http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities/claim", "acme",
		map[string]any{"taskQueue": "gpu:dgx", "identity": "dgx", "leaseSeconds": 120})
	if code != http.StatusOK {
		t.Fatalf("claim = %d %s", code, body)
	}
	if _, err := planeEnqueue(ctx, job("once")); err != nil {
		t.Fatal(err)
	}
	if got := status(t, ctx, "once"); got != "ACTIVITY_TASK_STATE_STARTED" {
		t.Fatalf("an enqueue of a claimed job left it %s", got)
	}
}
