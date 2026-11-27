package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewWorkerAdvertisesResearchBench(t *testing.T) {
	w, err := newWorker(&Env{CloudURL: "https://api.hanzo.ai"}, "")
	if err != nil {
		t.Fatalf("newWorker: %v", err)
	}
	for _, capName := range []string{researchCap, researchTickCap, hanzodRunCap} {
		if _, ok := w.handlers[capName]; !ok {
			t.Fatalf("newWorker must register %s unconditionally", capName)
		}
		if !slices.Contains(w.capabilities(), capName) {
			t.Fatalf("capabilities = %v, want %q included unconditionally", w.capabilities(), capName)
		}
	}
	if !w.hasNonRenderLane() {
		t.Fatal("hasNonRenderLane() must be true so claimAndRun polls even without ComfyUI studio")
	}
}

func TestResearchValidateSecurityAndBounds(t *testing.T) {
	// Neither script nor command.
	if _, err := researchValidate(json.RawMessage(`{"name":"empty"}`)); err == nil {
		t.Error("empty script+command must be rejected")
	}
	// Both script and command.
	if _, err := researchValidate(json.RawMessage(`{"script":"print(1)","command":["python3","-V"]}`)); err == nil {
		t.Error("both script and command must be rejected")
	}
	// Disallowed command binaries (including shell interpreters and traversal).
	for _, bad := range [][]string{
		{"sh", "-c", "id"},
		{"bash", "-c", "id"},
		{"curl", "https://example.com"},
		{"rm", "-rf", "/tmp/x"},
		{"../python3", "-V"},
		{""},
	} {
		raw, _ := json.Marshal(map[string]any{"command": bad})
		if _, err := researchValidate(raw); err == nil {
			t.Errorf("disallowed command %v must be rejected", bad)
		}
	}
	// Every allowlisted research binary passes validation.
	for _, okBin := range []string{"cargo", "python3", "uv", "hanzo-bench", "hanzo-server", "llama-bench", "nvidia-smi", "vulkaninfo"} {
		raw, _ := json.Marshal(map[string]any{"command": []string{okBin, "--version"}})
		in, err := researchValidate(raw)
		if err != nil {
			t.Errorf("allowlisted command %q rejected: %v", okBin, err)
		}
		if in.TimeoutSeconds != 3600 {
			t.Errorf("default timeout = %d, want 3600", in.TimeoutSeconds)
		}
	}
	// Timeout cap at 14400s.
	in, err := researchValidate(json.RawMessage(`{"command":["nvidia-smi"],"timeoutSeconds":999999}`))
	if err != nil || in.TimeoutSeconds != 14400 {
		t.Fatalf("timeout cap = %d (err=%v), want 14400", in.TimeoutSeconds, err)
	}
	// Requirement flag injection rejected.
	if _, err := researchValidate(json.RawMessage(`{"script":"print(1)","requirements":["--index-url=evil"]}`)); err == nil {
		t.Error("flag-shaped requirement must be rejected")
	}
	// Forbidden env keys (case-insensitive).
	for _, key := range []string{
		"LD_PRELOAD", "ld_preload", "LD_LIBRARY_PATH", "PATH", "HOME",
		"HANZO_TOKEN", "HANZO_TOKEN_COMMAND", "STUDIO_WORKER_TOKEN", "SSH_AUTH_SOCK",
	} {
		raw, _ := json.Marshal(map[string]any{
			"command": []string{"nvidia-smi"},
			"env":     map[string]string{key: "override"},
		})
		if _, err := researchValidate(raw); err == nil {
			t.Errorf("forbidden env key %q must be rejected", key)
		}
	}
}

func TestResearchValidateWorkdirRestrictions(t *testing.T) {
	allowedRoot := t.TempDir()
	outsideRoot := t.TempDir()
	subDir := filepath.Join(allowedRoot, "exp-2026-11-22")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(allowedRoot, "escape")
	if err := os.Symlink(outsideRoot, escapeLink); err != nil {
		t.Fatal(err)
	}

	origRoots := researchWorkdirRoots
	researchWorkdirRoots = func() []string { return []string{allowedRoot} }
	t.Cleanup(func() { researchWorkdirRoots = origRoots })

	// Allowed subdirectory succeeds.
	rawOK, _ := json.Marshal(map[string]any{"command": []string{"nvidia-smi"}, "workdir": subDir})
	if _, err := researchValidate(rawOK); err != nil {
		t.Fatalf("valid workdir rejected: %v", err)
	}
	// Outside root, .. traversal, symlink escape, and relative path all fail.
	for _, bad := range []string{
		outsideRoot,
		subDir + "/../../",
		escapeLink,
		"relative/dir",
		filepath.Join(allowedRoot, "does-not-exist"),
	} {
		raw, _ := json.Marshal(map[string]any{"command": []string{"nvidia-smi"}, "workdir": bad})
		if _, err := researchValidate(raw); err == nil {
			t.Errorf("unsafe workdir %q must be rejected", bad)
		}
	}
}

func TestExtractResearchJSON(t *testing.T) {
	// RESEARCH_JSON: line parsed even with preceding log lines.
	got := extractResearchJSON("warming up...\nRESEARCH_JSON: {\"tps\": 142.5, \"date\": \"2026-11-22\"}\n")
	if len(got) == 0 || !strings.Contains(string(got), `"tps":142.5`) {
		t.Fatalf("extractResearchJSON(RESEARCH_JSON:) = %s", got)
	}
	// Final JSON object line parsed.
	got2 := extractResearchJSON("step 1\n{\"gpu\": \"GB10\", \"ok\": true}\n")
	if len(got2) == 0 || !strings.Contains(string(got2), `"gpu":"GB10"`) {
		t.Fatalf("extractResearchJSON(bare object) = %s", got2)
	}
	// Plain text returns nil.
	if got3 := extractResearchJSON("just plain benchmark text\n"); got3 != nil {
		t.Fatalf("plain text yielded resultJson = %s", got3)
	}
}

func TestResearchBenchHandlerCommandProvenanceAndEnvScrub(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	t.Setenv("HANZO_TOKEN", "secret-bearer-must-not-leak")
	t.Setenv("STUDIO_WORKER_TOKEN", "secret-studio-token")

	w := &worker{
		hostname: "dgx",
		arch:     "aarch64",
		gpus:     []gpuInfo{{Name: "NVIDIA GB10", MemoryTotal: "131072 MiB", Arch: "sm_121", Unified: true}},
	}
	py := `import json, os; print("hello bench"); print("RESEARCH_JSON:" + json.dumps({"leaked": bool(os.environ.get("HANZO_TOKEN") or os.environ.get("STUDIO_WORKER_TOKEN")), "benchVar": os.environ.get("BENCH_TAG"), "hasCudaPath": "/usr/local/cuda/bin" in os.environ.get("PATH", ""), "shellArg": "$(evil)"}))`
	payload, _ := json.Marshal(researchBenchInput{
		Name:         "gb10-unit-2026-11-22",
		HypothesisID: "hyp-2026-11-22",
		ExperimentID: "exp-2026-11-22",
		Command:      []string{"python3", "-c", py},
		Env:          map[string]string{"BENCH_TAG": "sm121"},
	})
	rawRes, err := w.researchBenchHandler(context.Background(), payload)
	if err != nil {
		t.Fatalf("researchBenchHandler: %v", err)
	}
	b, err := json.Marshal(rawRes)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Name         string          `json:"name"`
		HypothesisID string          `json:"hypothesisId"`
		ExperimentID string          `json:"experimentId"`
		ExitCode     int             `json:"exitCode"`
		Hostname     string          `json:"hostname"`
		Arch         string          `json:"arch"`
		ExclusiveGPU bool            `json:"exclusiveGpu"`
		GPUs         []gpuInfo       `json:"gpus"`
		ResultJSON   json.RawMessage `json:"resultJson"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.Hostname != "dgx" || res.Arch != "aarch64" || !res.ExclusiveGPU || len(res.GPUs) != 1 {
		t.Fatalf("unexpected provenance: %+v", res)
	}
	var parsed struct {
		Leaked      bool   `json:"leaked"`
		BenchVar    string `json:"benchVar"`
		HasCudaPath bool   `json:"hasCudaPath"`
		ShellArg    string `json:"shellArg"`
	}
	if err := json.Unmarshal(res.ResultJSON, &parsed); err != nil {
		t.Fatalf("decode resultJson %s: %v", res.ResultJSON, err)
	}
	if parsed.Leaked {
		t.Fatal("daemon credentials leaked into benchmark child process")
	}
	if parsed.BenchVar != "sm121" || !parsed.HasCudaPath || parsed.ShellArg != "$(evil)" {
		t.Fatalf("unexpected child env/argv result: %+v", parsed)
	}
}

func TestResearchBenchExclusiveGPULockSerializes(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	w := &worker{
		hostname: "dgx",
		arch:     "aarch64",
		gpus:     []gpuInfo{{Name: "NVIDIA GB10"}},
	}
	lockFile := filepath.Join(t.TempDir(), "gpu.lock")
	var active atomic.Int32
	var maxActive atomic.Int32

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			unlock, err := acquireGPULock(context.Background(), lockFile)
			if err != nil {
				errs[idx] = err
				return
			}
			defer unlock()
			cur := active.Add(1)
			for {
				prev := maxActive.Load()
				if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(120 * time.Millisecond)
			active.Add(-1)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d lock failed: %v", i, err)
		}
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent holders under exclusive GPU lock = %d, want 1", got)
	}
	_ = w
}

func TestIsNodeBusyLockAndGPUUtil(t *testing.T) {
	defaults := defaultBusyLockPaths()
	if len(defaults) < 3 {
		t.Fatalf("defaultBusyLockPaths = %v, want temp + controller + research lock paths", defaults)
	}

	lockFile := filepath.Join(t.TempDir(), "gpu.lock")
	w := &worker{
		hostname:      "dgx",
		gpus:          []gpuInfo{{Name: "NVIDIA GB10"}},
		busyLockPaths: []string{lockFile},
		sampleFn:      func(context.Context) sampleReport { return sampleReport{GPUs: 1, GPUUtil: 0.25} },
	}
	if busy, reason := w.isNodeBusy(context.Background()); busy || reason != "" {
		t.Fatalf("idle worker reported busy=(%v,%q), want (false,\"\")", busy, reason)
	}

	// Active local GPU lock flips node busy immediately.
	unlock, err := acquireGPULock(context.Background(), lockFile)
	if err != nil {
		t.Fatal(err)
	}
	if busy, reason := w.isNodeBusy(context.Background()); !busy || reason != "local_gpu_lock_held" {
		unlock()
		t.Fatalf("locked worker reported busy=(%v,%q), want (true,\"local_gpu_lock_held\")", busy, reason)
	}
	unlock()
	if busy, reason := w.isNodeBusy(context.Background()); busy || reason != "" {
		t.Fatalf("unlocked worker reported busy=(%v,%q), want (false,\"\")", busy, reason)
	}

	// High GPU utilization (>0.80 default) marks node busy; HANZO_LINK_MAX_GPU_UTIL overrides ceiling.
	w.sampleFn = func(context.Context) sampleReport { return sampleReport{GPUs: 1, GPUUtil: 0.85} }
	if busy, reason := w.isNodeBusy(context.Background()); !busy || reason != "gpu_utilization_high" {
		t.Fatalf("high-util worker reported busy=(%v,%q), want (true,\"gpu_utilization_high\")", busy, reason)
	}
	t.Setenv("HANZO_LINK_MAX_GPU_UTIL", "0.90")
	if busy, reason := w.isNodeBusy(context.Background()); busy || reason != "" {
		t.Fatalf("worker below 0.90 override reported busy=(%v,%q), want (false,\"\")", busy, reason)
	}
}

func TestHeartbeatReportsBusyDetails(t *testing.T) {
	t.Setenv("HANZO_TOKEN", "test-token")
	lockFile := filepath.Join(t.TempDir(), "gpu.lock")

	var lastDetails map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			var body struct {
				Details map[string]any `json:"details"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			lastDetails = body.Details
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := testWorker(t, srv.URL)
	w.busyLockPaths = []string{lockFile}
	if err := w.heartbeat(context.Background()); err != nil {
		t.Fatalf("idle heartbeat: %v", err)
	}
	if b, _ := lastDetails["busy"].(bool); b {
		t.Fatalf("idle heartbeat details = %+v, want busy=false", lastDetails)
	}
	if _, hasReason := lastDetails["busyReason"]; hasReason {
		t.Fatalf("idle heartbeat must omit busyReason: %+v", lastDetails)
	}

	unlock, err := acquireGPULock(context.Background(), lockFile)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := w.heartbeat(context.Background()); err != nil {
		t.Fatalf("busy heartbeat: %v", err)
	}
	if b, _ := lastDetails["busy"].(bool); !b || lastDetails["busyReason"] != "local_gpu_lock_held" {
		t.Fatalf("busy heartbeat details = %+v, want busy=true busyReason=local_gpu_lock_held", lastDetails)
	}
}

func TestBusyClaimDeferralReleaseAndLeaseLapse(t *testing.T) {
	t.Setenv("HANZO_TOKEN", "test-token")
	lockFile := filepath.Join(t.TempDir(), "gpu.lock")

	var (
		mu              sync.Mutex
		claims          []string
		claimLeaseGot   int
		releaseCalls    []map[string]any
		failCalls       int
		completeCalls   int
		releaseHTTPCode = http.StatusOK
		nextTargetedJob *claimedActivity
		nextSharedJob   *claimedActivity
	)

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/activities/claim"):
			var body struct {
				TaskQueue    string `json:"taskQueue"`
				LeaseSeconds int    `json:"leaseSeconds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			claims = append(claims, body.TaskQueue)
			claimLeaseGot = body.LeaseSeconds
			if body.TaskQueue == "gpu:spark" && nextTargetedJob != nil {
				job := nextTargetedJob
				nextTargetedJob = nil
				_ = json.NewEncoder(rw).Encode(job)
				return
			}
			if body.TaskQueue == "gpu-jobs" && nextSharedJob != nil {
				job := nextSharedJob
				nextSharedJob = nil
				_ = json.NewEncoder(rw).Encode(job)
				return
			}
			rw.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/release"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			releaseCalls = append(releaseCalls, body)
			rw.WriteHeader(releaseHTTPCode)
		case strings.HasSuffix(r.URL.Path, "/fail"):
			failCalls++
			rw.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/complete"):
			completeCalls++
			rw.WriteHeader(http.StatusOK)
		default:
			rw.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	w := testWorker(t, srv.URL)
	w.gpus = []gpuInfo{{Name: "NVIDIA GB10", Arch: "sm_121"}}
	w.busyLockPaths = []string{lockFile}
	w.sampleFn = func(context.Context) sampleReport { return sampleReport{GPUs: 1, GPUUtil: 0.10} }
	w.handlers[researchTickCap] = echoHandler
	w.handlers[hanzodRunCap] = echoHandler

	unlock, err := acquireGPULock(context.Background(), lockFile)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	// 1) Pre-claim skip: while busy, shared `gpu-jobs` lane is NOT polled when targeted lane is empty.
	nextSharedJob = echoJob("shared-1")
	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("busy shared skip: %v", err)
	}
	if !slices.Equal(claims, []string{"gpu:spark"}) || completeCalls != 0 || failCalls != 0 {
		t.Fatalf("busy worker polled shared lane: claims=%v complete=%d fail=%d", claims, completeCalls, failCalls)
	}
	if claimLeaseGot != 30 {
		t.Fatalf("claim leaseSeconds = %d, want 30", claimLeaseGot)
	}

	// 2) Targeted `research.tick` (default when_idle_only=true) while busy -> POST .../release (delaySeconds=10), zero /fail.
	claims = nil
	tJob := &claimedActivity{Input: json.RawMessage(`{"name":"tick-2026-11-22"}`)}
	tJob.Execution.WorkflowId, tJob.Execution.RunId = "wf-tick", "wf-tick"
	tJob.Type.Name = researchTickCap
	nextTargetedJob = tJob
	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("targeted defer via release: %v", err)
	}
	if len(releaseCalls) != 1 || failCalls != 0 || completeCalls != 0 {
		t.Fatalf("expected 1 release and 0 fail/complete, got releases=%+v fail=%d complete=%d", releaseCalls, failCalls, completeCalls)
	}
	if releaseCalls[0]["reason"] != "local_gpu_lock_held" || releaseCalls[0]["delaySeconds"] != float64(10) {
		t.Fatalf("unexpected release payload: %+v", releaseCalls[0])
	}

	// 3) Pre-release server (404 on /release) -> returns nil without calling /fail so 30s claim lease lapses!
	releaseHTTPCode = http.StatusNotFound
	tJob2 := &claimedActivity{Input: json.RawMessage(`{"name":"tick-legacy"}`)}
	tJob2.Execution.WorkflowId, tJob2.Execution.RunId = "wf-legacy", "wf-legacy"
	tJob2.Type.Name = researchTickCap
	nextTargetedJob = tJob2
	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("legacy 404 release fallback: %v", err)
	}
	if failCalls != 0 || completeCalls != 0 {
		t.Fatalf("404 release fallback must NEVER call /fail or /complete: fail=%d complete=%d", failCalls, completeCalls)
	}

	// 4) Targeted `hanzod.run` with allow_when_busy=true runs immediately even while busy!
	releaseHTTPCode = http.StatusOK
	allowJob := &claimedActivity{Input: json.RawMessage(`{"allow_when_busy":true}`)}
	allowJob.Execution.WorkflowId, allowJob.Execution.RunId = "wf-allow", "wf-allow"
	allowJob.Type.Name = hanzodRunCap
	nextTargetedJob = allowJob
	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("allow_when_busy run: %v", err)
	}
	if completeCalls != 1 || failCalls != 0 {
		t.Fatalf("allow_when_busy should complete: complete=%d fail=%d", completeCalls, failCalls)
	}

	// 5) required_backend mismatch ("metal" on a CUDA GB10 worker) defers via /release without failing!
	releaseCalls = nil
	mismatchJob := &claimedActivity{Input: json.RawMessage(`{"allowWhenBusy":true,"required_backend":"metal"}`)}
	mismatchJob.Execution.WorkflowId, mismatchJob.Execution.RunId = "wf-metal", "wf-metal"
	mismatchJob.Type.Name = hanzodRunCap
	nextTargetedJob = mismatchJob
	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("backend mismatch defer: %v", err)
	}
	if len(releaseCalls) != 1 || releaseCalls[0]["reason"] != "required_backend_mismatch:metal" || failCalls != 0 {
		t.Fatalf("backend mismatch expected release without fail, got releases=%+v fail=%d", releaseCalls, failCalls)
	}
}

func TestWatchEventsSSEWakeupAndReconnect(t *testing.T) {
	t.Setenv("HANZO_TOKEN", "test-token")

	streamStep := make(chan string, 4)
	var connects atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workflow/events" {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		n := connects.Add(1)
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		fl, _ := rw.(http.Flusher)
		if fl != nil {
			fl.Flush()
		}
		if n == 1 {
			for frame := range streamStep {
				if frame == "DROP" {
					return
				}
				_, _ = io.WriteString(rw, frame)
				if fl != nil {
					fl.Flush()
				}
			}
			return
		}
		// Second connection after reconnect: emit deferred event and wait for ctx cancel.
		_, _ = io.WriteString(rw, "data: {\"kind\":\"activity.deferred\",\"namespace\":\"gpu-jobs\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	w := testWorker(t, srv.URL)
	w.sseBackoff = 15 * time.Millisecond
	wakeCh := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.watchEvents(ctx, wakeCh)

	// Initial connect triggers an immediate sweep wakeup.
	select {
	case <-wakeCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for initial SSE connect wakeup")
	}

	// Non-matching namespace ("fleet") does NOT wake gpu-jobs worker.
	streamStep <- "data: {\"kind\":\"activity.scheduled\",\"namespace\":\"fleet\"}\n\n"
	select {
	case <-wakeCh:
		t.Fatal("fleet namespace event must not wake gpu-jobs worker")
	case <-time.After(40 * time.Millisecond):
	}

	// Matching gpu-jobs event wakes worker in <50ms!
	start := time.Now()
	streamStep <- "data: {\"kind\":\"activity.scheduled\",\"namespace\":\"gpu-jobs\"}\n\n"
	select {
	case <-wakeCh:
		if d := time.Since(start); d > 250*time.Millisecond {
			t.Fatalf("SSE wakeup took %s, want immediate wakeup", d)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for SSE activity.scheduled wakeup")
	}

	// Drop stream 1 -> watchEvents reconnects and wakes on stream 2's activity.deferred!
	streamStep <- "DROP"
	select {
	case <-wakeCh:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for SSE reconnect wakeup")
	}
	if connects.Load() < 2 {
		t.Fatalf("SSE connects = %d, want >= 2 after reconnect", connects.Load())
	}
}

func TestEvoWSLAliasQueuesAndZeroLatencySlotWakeup(t *testing.T) {
	t.Setenv("HANZO_NODE_ID", "evo")
	named, err := newWorker(&Env{CloudURL: "https://api.hanzo.ai"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if named.identity != "evo" || named.hostname != "evo" {
		t.Fatalf("HANZO_NODE_ID=evo -> identity=%q hostname=%q, want evo/evo", named.identity, named.hostname)
	}

	// A worker on `evo-wsl` claims both `gpu:evo-wsl` and `gpu:evo` before shared `gpu-jobs`,
	// and signals `wakeCh` immediately when the job finishes!
	var claims []string
	srv := stubJobsCloud(t, &claims, map[string]*claimedActivity{"gpu:evo": echoJob("evo-job-1")})
	defer srv.Close()

	w := testWorker(t, srv.URL)
	w.identity = "evo-wsl"
	w.hostname = "evo-wsl"
	w.rawHostname = "evo-wsl"
	wakeCh := make(chan struct{}, 1)
	w.wakeCh = wakeCh

	if err := w.claimAndRun(context.Background(), io.Discard); err != nil {
		t.Fatalf("evo-wsl claimAndRun: %v", err)
	}
	if !slices.Equal(claims, []string{"gpu:evo-wsl", "gpu:evo"}) {
		t.Fatalf("evo-wsl claims = %v, want [gpu:evo-wsl gpu:evo]", claims)
	}
	select {
	case <-wakeCh:
	default:
		t.Fatal("expected immediate 0ms wakeCh signal after job completed")
	}
}
