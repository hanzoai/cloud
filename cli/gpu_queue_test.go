package cli

// gpu_queue_test.go — the per-GPU claim contract: a job pinned to THIS machine's
// lane ("gpu:<identity>") is claimed BEFORE the shared any-GPU lane ("gpu-jobs"),
// both within the gpu-jobs namespace. A stub cloud records the taskQueue of every
// claim so the ORDER is the assertion.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubJobsCloud records the taskQueue of every claim and serves the queued job (if
// any) on the matching lane, once. A lane with no job (or already drained) answers
// 204; complete/fail/heartbeat answer 200.
func stubJobsCloud(t *testing.T, claims *[]string, jobsByLane map[string]*claimedActivity) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/activities/claim") {
			var body struct {
				TaskQueue string `json:"taskQueue"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			*claims = append(*claims, body.TaskQueue)
			if job := jobsByLane[body.TaskQueue]; job != nil {
				jobsByLane[body.TaskQueue] = nil // deliver once
				_ = json.NewEncoder(w).Encode(job)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK) // complete / fail / heartbeat
	}))
}

// echoJob is a GPU-free job the worker's echo handler runs to completion instantly,
// so a claim test never needs a real render backend.
func echoJob(id string) *claimedActivity {
	a := &claimedActivity{Input: json.RawMessage(`{}`)}
	a.Execution.WorkflowId, a.Execution.RunId = id, id
	a.Type.Name = "echo"
	return a
}

func testWorker(t *testing.T, url string) *worker {
	t.Helper()
	t.Setenv("HANZO_TOKEN", "test-token") // ensureToken honors this; no `hanzo login`
	return &worker{
		env:         &Env{CloudURL: url},
		http:        &http.Client{Timeout: 5 * time.Second},
		baseURL:     url,
		identity:    "spark",
		hostname:    "spark",
		jobsNS:      "gpu-jobs",
		handlers:    map[string]jobHandler{"echo": echoHandler},
		studioReady: true, // a render-capable node (preflight passed)
	}
}

func TestGPUQueueLaneName(t *testing.T) {
	w := &worker{identity: "spark"}
	if got := w.gpuQueue(); got != "gpu:spark" {
		t.Fatalf("gpuQueue() = %q, want gpu:spark", got)
	}
}

// A job pinned to THIS GPU's lane is claimed first — the shared lane is not even
// polled that cycle.
func TestClaimPrefersTargetedLane(t *testing.T) {
	var claims []string
	srv := stubJobsCloud(t, &claims, map[string]*claimedActivity{"gpu:spark": echoJob("j1")})
	defer srv.Close()
	if err := testWorker(t, srv.URL).claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("claimAndRun: %v", err)
	}
	if len(claims) != 1 || claims[0] != "gpu:spark" {
		t.Fatalf("claims = %v, want exactly [gpu:spark] (targeted first; shared not polled)", claims)
	}
}

// With its own lane empty, the worker falls through to the shared any-GPU lane.
func TestClaimFallsBackToSharedLane(t *testing.T) {
	var claims []string
	srv := stubJobsCloud(t, &claims, map[string]*claimedActivity{"gpu-jobs": echoJob("j2")})
	defer srv.Close()
	if err := testWorker(t, srv.URL).claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("claimAndRun: %v", err)
	}
	if len(claims) != 2 || claims[0] != "gpu:spark" || claims[1] != "gpu-jobs" {
		t.Fatalf("claims = %v, want [gpu:spark gpu-jobs] (targeted, then shared)", claims)
	}
}

// Both lanes empty polls targeted THEN shared and runs nothing.
func TestClaimBothLanesEmpty(t *testing.T) {
	var claims []string
	srv := stubJobsCloud(t, &claims, map[string]*claimedActivity{})
	defer srv.Close()
	if err := testWorker(t, srv.URL).claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("claimAndRun: %v", err)
	}
	if len(claims) != 2 || claims[0] != "gpu:spark" || claims[1] != "gpu-jobs" {
		t.Fatalf("claims = %v, want [gpu:spark gpu-jobs]", claims)
	}
}

// The render submit seam is the gated worker-mode execute path, not the open /prompt
// — the shared contract with the studio's --worker-mode gate.
func TestWorkerExecuteSeamIsGated(t *testing.T) {
	if localWorkerExecute != "http://127.0.0.1:8188/v1/worker/execute" {
		t.Fatalf("localWorkerExecute = %q, want the gated /v1/worker/execute seam", localWorkerExecute)
	}
}

// A HUNG nvidia-smi (blocks until its bounded context fires) must NOT block the
// caller: reportSample detaches the probe+POST onto a goroutine and returns at once,
// so the worker's select loop keeps heartbeating and claiming. Guards the
// worker-wedge regression (a synchronous probe that stalls the loop under GPU/driver
// pressure → the machine flaps offline mid-render).
func TestReportSampleNeverBlocksLoop(t *testing.T) {
	orig := nvidiaSmi
	defer func() { nvidiaSmi = orig }()
	probing := make(chan struct{})
	release := make(chan struct{})
	nvidiaSmi = func(ctx context.Context) ([]byte, error) {
		close(probing) // entered the probe (the nvidiaSmi var read already happened)
		select {
		case <-release: // the test lets us finish
		case <-ctx.Done(): // or the bounded probe timeout fires
		}
		return nil, ctx.Err()
	}
	t.Setenv("HANZO_TOKEN", "t")
	w := &worker{identity: "spark", hostname: "spark", http: &http.Client{Timeout: time.Second}, env: &Env{}, baseURL: "http://127.0.0.1:0"}

	done := make(chan struct{})
	go func() { w.reportSample(context.Background()); close(done) }()
	select {
	case <-done: // returned immediately — the select loop is never wedged
	case <-time.After(500 * time.Millisecond):
		t.Fatal("reportSample blocked the caller — a hung sampler would wedge the worker loop")
	}
	select {
	case <-probing: // the probe really ran, on the detached goroutine (off the critical path)
	case <-time.After(2 * time.Second):
		t.Fatal("probe never started")
	}
	close(release)
}

// A node that can't serve renders (preflight failed) claims NOTHING — it must never
// pull a render job onto a box that will only refuse it on the gated seam (poison
// loop). It still heartbeats presence; it just stays idle.
func TestNotStudioReadyClaimsNothing(t *testing.T) {
	var claims []string
	srv := stubJobsCloud(t, &claims, map[string]*claimedActivity{"gpu:spark": echoJob("j1")})
	defer srv.Close()
	w := testWorker(t, srv.URL)
	w.studioReady = false
	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("claimAndRun: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("a not-ready node claimed %v; want zero claims", claims)
	}
}

// The terminal report must hit the RIGHT activity — namespace gpu-jobs, the CLAIMED
// workflow+run ids, the correct verb. A stub that 200s every path lets an ns/id
// routing regression pass, so assert the exact paths for both complete and fail.
func TestTerminalReportsHitCorrectActivityPath(t *testing.T) {
	t.Setenv("HANZO_TOKEN", "t")
	var mu sync.Mutex
	terminal := map[string]string{}
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/activities/claim"):
			var body struct {
				TaskQueue string `json:"taskQueue"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.TaskQueue != "gpu:spark" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			mu.Lock()
			n := served
			served++
			mu.Unlock()
			switch n {
			case 0:
				_ = json.NewEncoder(w).Encode(echoJob("wf1")) // echo handler → complete
			case 1:
				j := echoJob("wf2")
				j.Type.Name = "nope" // no handler → fail
				_ = json.NewEncoder(w).Encode(j)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		case strings.HasSuffix(r.URL.Path, "/complete"):
			mu.Lock()
			terminal["complete"] = r.URL.Path
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/fail"):
			mu.Lock()
			terminal["fail"] = r.URL.Path
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	w := testWorker(t, srv.URL)
	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("run1 (echo→complete): %v", err)
	}
	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("run2 (unknown→fail): %v", err)
	}
	if got := terminal["complete"]; got != "/v1/tasks/namespaces/gpu-jobs/activities/wf1/wf1/complete" {
		t.Fatalf("complete path = %q, want the claimed activity's exact ns+ids", got)
	}
	if got := terminal["fail"]; got != "/v1/tasks/namespaces/gpu-jobs/activities/wf2/wf2/fail" {
		t.Fatalf("fail path = %q, want the claimed activity's exact ns+ids", got)
	}
}

// SharePolicy.reject's fallback: an ABSENT or unparseable input field skips its gate
// (permissive), never a hard error, so a policy only ever narrows on fields it can read.
func TestSharePolicyRejectFallback(t *testing.T) {
	var nilp *SharePolicy
	if r := nilp.reject("studio.render", nil); r != "" {
		t.Fatalf("nil policy must allow everything: %q", r)
	}
	p := &SharePolicy{AllowedJobTypes: []string{"studio.render"}}
	if p.reject("echo", nil) == "" {
		t.Fatal("a disallowed job type must be rejected")
	}
	if r := p.reject("studio.render", nil); r != "" {
		t.Fatalf("an allowed job type must pass: %q", r)
	}
	p2 := &SharePolicy{AllowedOrgs: []string{"acme"}}
	if r := p2.reject("studio.render", json.RawMessage(`{}`)); r != "" {
		t.Fatalf("absent org must SKIP the org gate (fallback), not reject: %q", r)
	}
	if r := p2.reject("studio.render", json.RawMessage(`{"org":"acme"}`)); r != "" {
		t.Fatalf("a matching org must pass: %q", r)
	}
	if p2.reject("studio.render", json.RawMessage(`{"org":"other"}`)) == "" {
		t.Fatal("a non-allowed org must be rejected")
	}
	if r := p2.reject("studio.render", json.RawMessage(`not json`)); r != "" {
		t.Fatalf("unparseable input must skip input gates, not reject: %q", r)
	}
}

// studioCap is advertised ONLY when the node can actually render: a missing worker
// token is never ready (the gated seam would 403), and the block reason is explicit.
func TestStudioReadyGatesCapability(t *testing.T) {
	t.Setenv("STUDIO_WORKER_TOKEN", "")
	w := &worker{launchesStudio: true, http: &http.Client{}}
	w.refreshStudioReady(context.Background())
	if w.studioReady {
		t.Fatal("no token must never be studio-ready")
	}
	if contains(w.capabilities(), studioCap) {
		t.Fatalf("studioCap advertised without a token: %v", w.capabilities())
	}
	if w.studioBlockReason() == "" {
		t.Fatal("a not-ready node must explain why it won't render")
	}
	// Token present + we launch the studio ⇒ ready ⇒ studioCap advertised.
	t.Setenv("STUDIO_WORKER_TOKEN", "tok")
	if changed := w.refreshStudioReady(context.Background()); !changed {
		t.Fatal("adding the token should flip readiness")
	}
	if !w.studioReady || !contains(w.capabilities(), studioCap) {
		t.Fatalf("token + launchesStudio must be ready + advertise studioCap: ready=%v caps=%v", w.studioReady, w.capabilities())
	}
}
