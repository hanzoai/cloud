package compute

// fleet_jobs_test.go — the gpu-jobs queue view + manage surface. The load-bearing
// logic is pure (taskQueue→gpu parse, status normalize, filter, per-node counts, the
// worker-sample builder) and tested directly; the routes are tested for tenancy +
// fail-soft against an unwired engine (gpuJobs → empty, cancel → 503).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/samples"
	"github.com/hanzoai/cloud/sdk/plane"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func TestGPUTarget(t *testing.T) {
	for in, want := range map[string]string{
		"gpu:spark": "spark",
		"gpu:evo-2": "evo-2",
		"gpu-jobs":  "", // the shared any-GPU lane
		"":          "",
		"gpu:":      "", // prefix only → no node
	} {
		if got := gpuTarget(in); got != want {
			t.Fatalf("gpuTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeStatus(t *testing.T) {
	for in, want := range map[string]string{
		"ACTIVITY_TASK_STATE_SCHEDULED": "queued",
		"ACTIVITY_TASK_STATE_STARTED":   "running",
		"ACTIVITY_TASK_STATE_COMPLETED": "completed",
		"ACTIVITY_TASK_STATE_FAILED":    "failed",
		"ACTIVITY_TASK_STATE_CANCELED":  "canceled",
		"ACTIVITY_TASK_STATE_WEIRD":     "weird", // unknown → lower-cased, never faked
	} {
		if got := normalizeStatus(in); got != want {
			t.Fatalf("normalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderLabel(t *testing.T) {
	input := map[string]any{"prompt": map[string]any{
		"9":  map[string]any{"class_type": "KSampler", "inputs": map[string]any{}},
		"15": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{"filename_prefix": "fixes/model01"}},
	}}
	if got := renderLabel(input); got != "fixes/model01" {
		t.Fatalf("renderLabel = %q, want fixes/model01", got)
	}
	for _, bad := range []any{nil, "x", 3, map[string]any{}, map[string]any{"prompt": "x"}} {
		if got := renderLabel(bad); got != "" {
			t.Fatalf("renderLabel(%v) = %q, want empty", bad, got)
		}
	}
}

func TestFilterGPUJobs(t *testing.T) {
	jobs := []gpuJob{
		{ID: "a", GPU: "spark", Status: "queued"},
		{ID: "b", Worker: "spark", Status: "running"}, // shared lane, spark claimed it
		{ID: "c", GPU: "evo", Status: "queued"},
		{ID: "d", Status: "queued"}, // shared, unclaimed
	}
	// "spark's queue" = targeted at spark OR claimed by spark.
	if got := gpuJobIDs(filterGPUJobs(jobs, "spark", "")); !sliceEqStr(got, []string{"a", "b"}) {
		t.Fatalf("gpu=spark => %v, want [a b]", got)
	}
	// status filter (normalized vocabulary).
	if got := gpuJobIDs(filterGPUJobs(jobs, "", "queued")); !sliceEqStr(got, []string{"a", "c", "d"}) {
		t.Fatalf("status=queued => %v, want [a c d]", got)
	}
	// shared lane only = no target and no claimant.
	if got := gpuJobIDs(filterGPUJobs(jobs, "shared", "")); !sliceEqStr(got, []string{"d"}) {
		t.Fatalf("gpu=shared => %v, want [d]", got)
	}
	// combined narrowers.
	if got := gpuJobIDs(filterGPUJobs(jobs, "spark", "running")); !sliceEqStr(got, []string{"b"}) {
		t.Fatalf("gpu=spark status=running => %v, want [b]", got)
	}
}

func TestGPUJobCounts(t *testing.T) {
	c := gpuJobCounts([]gpuJob{
		{GPU: "spark", Status: "queued"},
		{GPU: "spark", Status: "queued"},
		{Worker: "spark", Status: "running"}, // shared job spark is running
		{Status: "queued"},                   // shared, unclaimed → attributed to no node
		{GPU: "evo", Worker: "evo", Status: "running"},
	})
	if c["spark"].queued != 2 || c["spark"].running != 1 {
		t.Fatalf("spark counts = %+v, want {queued:2 running:1}", c["spark"])
	}
	if c["evo"].running != 1 {
		t.Fatalf("evo counts = %+v, want running:1", c["evo"])
	}
	if _, ok := c[""]; ok {
		t.Fatal("a shared-lane job must not be attributed to a node")
	}
}

func TestSampleIngestBuild(t *testing.T) {
	smp, err := sampleIngest{Unit: "spark", Host: "spark", GPUUtil: 0.42, GPUs: 1, GPUModel: "GB10", MemUsed: 100, MemFree: 200}.sample("acme")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if smp.Org != "acme" || smp.Source != samples.SourceBYO || smp.Kind != samples.KindWorker {
		t.Fatalf("sample identity = %+v, want acme/byo/worker (server-authoritative)", smp)
	}
	if smp.Unit != "spark" || smp.GPUUtil != 0.42 || smp.GPUModel != "GB10" {
		t.Fatalf("sample metrics = %+v", smp)
	}
	if smp.At.IsZero() {
		t.Fatal("sample At must be set")
	}
	if _, err := (sampleIngest{Unit: "  "}).sample("acme"); err == nil {
		t.Fatal("empty unit must error")
	}
}

// ---- route tenancy + fail-soft (no engine wired in unit tests) ----

func TestFleetJobsRequiresTenant(t *testing.T) {
	app := mountApp(t, &fakeVisor{})
	if code, _ := do(t, app, http.MethodGet, "/v1/compute/fleet/jobs", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no-org /v1/fleet/jobs want 401, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/fleet/jobs", "", map[string]any{"gpu": "dgx"}); code != http.StatusUnauthorized {
		t.Fatalf("no-org POST /v1/fleet/jobs want 401, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/compute/fleet/jobs/j1", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no-org GET /v1/fleet/jobs/j1 want 401, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/fleet/samples", "", map[string]any{"unit": "spark"}); code != http.StatusUnauthorized {
		t.Fatalf("no-org POST /v1/fleet/samples want 401, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/fleet/jobs/j1/cancel", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no-org cancel want 401, got %d", code)
	}
}

// With a validated tenant but no tasks engine wired, the queue reads empty (never a
// 500) and cancel reports the engine is not ready — the fail-soft contract.
func TestFleetJobsFailSoftAndIngest(t *testing.T) {
	app := mountApp(t, &fakeVisor{})

	code, body := do(t, app, http.MethodGet, "/v1/compute/fleet/jobs", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/fleet/jobs want 200, got %d: %s", code, body)
	}
	var got struct {
		Jobs []gpuJob `json:"jobs"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	if len(got.Jobs) != 0 {
		t.Fatalf("want empty jobs on an unwired engine, got %d", len(got.Jobs))
	}

	// Sample ingest accepts a well-formed report (the warehouse write is detached).
	if code, b := do(t, app, http.MethodPost, "/v1/compute/fleet/samples", "acme", map[string]any{"unit": "spark", "gpuUtil": 0.5}); code != http.StatusAccepted {
		t.Fatalf("POST /v1/fleet/samples want 202, got %d: %s", code, b)
	}
	// A report with no unit is a 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/fleet/samples", "acme", map[string]any{"gpuUtil": 0.5}); code != http.StatusBadRequest {
		t.Fatalf("unit-less sample want 400, got %d", code)
	}
	// Cancel with no engine wired is a clean 503, not a panic/500.
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/fleet/jobs/j1/cancel", "acme", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("cancel with unwired engine want 503, got %d", code)
	}
	// Submit + Get with no workflow peer wired are clean 503s.
	sockDir, _ := os.MkdirTemp("", "nowf")
	defer os.RemoveAll(sockDir)
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	defer plane.Unbind()
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/fleet/jobs", "acme", map[string]any{"gpu": "dgx", "input": map[string]any{"name": "probe"}}); code != http.StatusServiceUnavailable {
		t.Fatalf("submit with unwired workflow peer want 503, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/compute/fleet/jobs/j1", "acme", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("get with unwired workflow peer want 503, got %d", code)
	}
}

func TestSubmitAndGetFleetJobRoundTrip(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "wfpeer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	var lastEnq client.EnqueueIn
	peer := zip.New(zip.Config{Logger: luxlog.New("test")})
	peer.Post("/workflow/enqueue", func(_ context.Context, in *client.EnqueueIn) (*client.Activity, error) {
		lastEnq = *in
		row, _ := json.Marshal(tasks.StandaloneActivity{
			Execution: tasks.ExecutionRef{WorkflowId: in.ID, RunId: in.ID},
			Type:      tasks.TypeRef{Name: in.Type},
			TaskQueue: in.Queue,
			Status:    "ACTIVITY_TASK_STATE_SCHEDULED",
		})
		return &client.Activity{Row: row}, nil
	}, zip.WithOperationID(client.WorkflowEnqueue))
	peer.Post("/workflow/activity", func(_ context.Context, in *client.ActivityIn) (*client.Activity, error) {
		if in.ID == "missing" {
			return nil, zip.ErrNotFound("activity: no such activity")
		}
		row, _ := json.Marshal(tasks.StandaloneActivity{
			Execution:    tasks.ExecutionRef{WorkflowId: in.ID, RunId: in.ID},
			Type:         tasks.TypeRef{Name: "research.bench"},
			TaskQueue:    "gpu:dgx",
			Identity:     "dgx",
			Status:       "ACTIVITY_TASK_STATE_COMPLETED",
			Attempt:      1,
			StartTime:    "2026-11-22T23:00:00Z",
			CloseTime:    "2026-11-22T23:00:02Z",
			Result:       map[string]any{"exitCode": 0, "hostname": "dgx", "resultJson": map[string]any{"gpu": "NVIDIA GB10"}},
			FailureCause: "",
		})
		return &client.Activity{Row: row}, nil
	}, zip.WithOperationID(client.WorkflowActivity))

	sock := zip.SocketPath("workflow")
	go func() { _ = peer.Listen(sock) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	for range 300 {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	app := mountApp(t, &fakeVisor{})

	// Submit omitting type: defaults to research.bench and routes gpu="DGX" -> "gpu:dgx".
	code, body := do(t, app, http.MethodPost, "/v1/compute/fleet/jobs", "acme", map[string]any{
		"id":             "bench-2026-11-22",
		"gpu":            "DGX",
		"timeoutSeconds": 600,
		"input":          map[string]any{"name": "gb10-probe-2026-11-22", "command": []string{"nvidia-smi"}},
	})
	if code != http.StatusOK {
		t.Fatalf("submitFleetJob want 200, got %d: %s", code, body)
	}
	var sub jobSubmitted
	if err := json.Unmarshal(body, &sub); err != nil {
		t.Fatalf("decode submit: %v", err)
	}
	if sub.ID != "bench-2026-11-22" || sub.RunID != "bench-2026-11-22" || sub.Type != "research.bench" || sub.Status != "queued" || sub.GPU != "dgx" {
		t.Fatalf("submit response = %+v", sub)
	}
	if lastEnq.Namespace != "gpu-jobs" || lastEnq.Queue != "gpu:dgx" || lastEnq.Type != "research.bench" || lastEnq.Close != "10m0s" {
		t.Fatalf("enqueued payload = %+v", lastEnq)
	}

	// Poll result via GET /v1/compute/fleet/jobs/:id.
	code, body = do(t, app, http.MethodGet, "/v1/compute/fleet/jobs/bench-2026-11-22", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("getFleetJob want 200, got %d: %s", code, body)
	}
	var got gpuJob
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode getFleetJob: %v", err)
	}
	if got.Status != "completed" || got.GPU != "dgx" || got.Worker != "dgx" || len(got.Result) == 0 {
		t.Fatalf("getFleetJob = %+v", got)
	}

	// Missing job returns 404.
	if code, _ := do(t, app, http.MethodGet, "/v1/compute/fleet/jobs/missing", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("missing job want 404, got %d", code)
	}
}

func TestFleetWorkersExposeBusyAndHeartbeatDetails(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "wffleet")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	now := time.Now().UTC().Format(time.RFC3339)
	peer := zip.New(zip.Config{Logger: luxlog.New("test")})
	peer.Post("/workflow/activities", func(_ context.Context, in *client.ActivitiesIn) (*client.Activities, error) {
		if in.Namespace != "fleet" {
			return &client.Activities{Rows: json.RawMessage(`[]`)}, nil
		}
		rows, _ := json.Marshal([]map[string]any{
			{
				"execution":         map[string]any{"workflowId": "dgx", "runId": "dgx"},
				"status":            "ACTIVITY_TASK_STATE_SCHEDULED",
				"lastHeartbeatTime": now,
				"input": map[string]any{
					"hostname":     "dgx",
					"os":           "linux",
					"version":      "v2026.11.22",
					"jobQueue":     "gpu-jobs",
					"capabilities": []string{"fn.run", "research.bench", "research.tick", "hanzod.run"},
					"busy":         false,
				},
			},
			{
				"execution":            map[string]any{"workflowId": "spark", "runId": "spark"},
				"status":               "ACTIVITY_TASK_STATE_SCHEDULED",
				"lastHeartbeatTime":    now,
				"lastHeartbeatDetails": map[string]any{"busy": true, "busyReason": "local_gpu_lock_held"},
				"input": map[string]any{
					"hostname": "spark",
					"os":       "linux",
					"version":  "v2026.11.22",
					"jobQueue": "gpu-jobs",
					"busy":     false,
				},
			},
			{
				"execution":            map[string]any{"workflowId": "mac-studio", "runId": "mac-studio"},
				"status":               "ACTIVITY_TASK_STATE_SCHEDULED",
				"lastHeartbeatTime":    now,
				"lastHeartbeatDetails": map[string]any{"busy": false},
				"input": map[string]any{
					"hostname":   "mac-studio",
					"os":         "darwin",
					"version":    "v0.3.2",
					"jobQueue":   "gpu-jobs",
					"busy":       true,
					"busyReason": "gpu_utilization_high",
				},
			},
		})
		return &client.Activities{Rows: rows}, nil
	}, zip.WithOperationID(client.WorkflowActivities))

	sock := zip.SocketPath("workflow")
	go func() { _ = peer.Listen(sock) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	for range 300 {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	app := mountApp(t, &fakeVisor{})
	code, body := do(t, app, http.MethodGet, "/v1/compute/fleet/workers", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/compute/fleet/workers want 200, got %d: %s", code, body)
	}
	var wl workerList
	if err := json.Unmarshal(body, &wl); err != nil {
		t.Fatalf("decode workers: %v", err)
	}
	byID := map[string]byoWorker{}
	for _, w := range wl.Workers {
		byID[w.ID] = w
	}
	if byID["dgx"].Busy || byID["dgx"].BusyReason != "" {
		t.Fatalf("dgx busy=(%v,%q), want (false,\"\")", byID["dgx"].Busy, byID["dgx"].BusyReason)
	}
	if !byID["spark"].Busy || byID["spark"].BusyReason != "local_gpu_lock_held" {
		t.Fatalf("spark busy=(%v,%q), want (true,\"local_gpu_lock_held\") from lastHeartbeatDetails", byID["spark"].Busy, byID["spark"].BusyReason)
	}
	if byID["mac-studio"].Busy || byID["mac-studio"].BusyReason != "" {
		t.Fatalf("mac-studio busy=(%v,%q), want (false,\"\") after idle heartbeat cleared registration busy", byID["mac-studio"].Busy, byID["mac-studio"].BusyReason)
	}
}

// F1: past 100 lifetime rows, every LIVE job survives and terminal history is capped,
// recency-first — the truncation regression that hid live work.
func TestOrderAndBoundJobsPast100(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var all []gpuJob
	for i := range 60 { // 60 live: queued/running/stalled
		st := []string{"queued", "running", "stalled"}[i%3]
		all = append(all, gpuJob{ID: fmt.Sprintf("live-%d", i), Status: st,
			StartTime: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)})
	}
	for i := range 120 { // 120 terminal, all MORE recent than the live set
		all = append(all, gpuJob{ID: fmt.Sprintf("term-%d", i), Status: "completed",
			CloseTime: base.Add(time.Duration(1000+i) * time.Minute).Format(time.RFC3339)})
	}
	got := orderAndBoundJobs(all)

	live, term := 0, 0
	for _, j := range got {
		if isTerminalJob(j.Status) {
			term++
		} else {
			live++
		}
	}
	if live != 60 {
		t.Fatalf("live jobs kept = %d, want 60 (never truncated)", live)
	}
	if term != maxTerminalJobs {
		t.Fatalf("terminal kept = %d, want %d (bounded)", term, maxTerminalJobs)
	}
	// The kept terminals are the newest (term-119..term-70); term-69 is dropped.
	if !containsJob(got, "term-119") || !containsJob(got, "term-70") || containsJob(got, "term-69") {
		t.Fatal("terminal cap must keep the most-recent maxTerminalJobs, dropping older")
	}
	for i := 1; i < len(got); i++ { // recency-descending overall
		if jobRecency(got[i-1]) < jobRecency(got[i]) {
			t.Fatalf("not recency-descending at position %d", i)
		}
	}
}

// F4: a running job whose lease elapsed reads STALLED (its worker died; the reaper
// hasn't reclaimed it yet), not "running" forever.
func TestToGPUJobStall(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mk := func(status, lease string) gpuJob {
		return toGPUJob(tasks.StandaloneActivity{
			Execution:   tasks.ExecutionRef{WorkflowId: "j", RunId: "j"},
			Status:      status,
			LeaseExpiry: lease,
		}, now)
	}
	if g := mk("ACTIVITY_TASK_STATE_STARTED", now.Add(-time.Minute).Format(time.RFC3339)); g.Status != "stalled" {
		t.Fatalf("expired-lease running = %q, want stalled", g.Status)
	}
	if g := mk("ACTIVITY_TASK_STATE_STARTED", now.Add(time.Minute).Format(time.RFC3339)); g.Status != "running" {
		t.Fatalf("live-lease running = %q, want running", g.Status)
	}
	if g := mk("ACTIVITY_TASK_STATE_SCHEDULED", ""); g.Status != "queued" {
		t.Fatalf("queued (no lease) = %q, want queued", g.Status)
	}
	if g := mk("ACTIVITY_TASK_STATE_COMPLETED", now.Add(-time.Hour).Format(time.RFC3339)); g.Status != "completed" {
		t.Fatalf("completed = %q, want completed (terminal, lease irrelevant)", g.Status)
	}
}

// F3: the ?gpu= filter is case-insensitive (node ids are lower-case).
func TestFilterGPUJobsCaseInsensitive(t *testing.T) {
	jobs := []gpuJob{{ID: "a", GPU: "spark", Status: "queued"}}
	if got := gpuJobIDs(filterGPUJobs(jobs, "Spark", "")); !sliceEqStr(got, []string{"a"}) {
		t.Fatalf("?gpu=Spark => %v, want [a]", got)
	}
	if got := gpuJobIDs(filterGPUJobs(jobs, "SPARK", "QUEUED")); !sliceEqStr(got, []string{"a"}) {
		t.Fatalf("?gpu=SPARK&status=QUEUED => %v, want [a]", got)
	}
}

// The cancel error→HTTP mapping (paired with the tasks engine's own success/terminal/
// cross-org test) covers success (200), missing (404), finished (409), else 502.
func TestCancelErrStatus(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, http.StatusOK},
		{fmt.Errorf("activity not found"), http.StatusNotFound},
		{fmt.Errorf("activity terminal: status=ACTIVITY_TASK_STATE_COMPLETED"), http.StatusConflict},
		{fmt.Errorf("some other failure"), http.StatusBadGateway},
	} {
		if got := cancelErrStatus(c.err); got != c.want {
			t.Fatalf("cancelErrStatus(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// ---- helpers ----

func containsJob(js []gpuJob, id string) bool {
	for _, j := range js {
		if j.ID == id {
			return true
		}
	}
	return false
}

func gpuJobIDs(js []gpuJob) []string {
	out := make([]string, len(js))
	for i, j := range js {
		out[i] = j.ID
	}
	return out
}

func sliceEqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
