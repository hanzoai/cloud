// studio.go — local Hanzo Studio render-backend supervision for `hanzo gpu
// connect --studio-dir <checkout>`. The gpu-jobs claim loop renders on the
// LOCAL studio server (127.0.0.1:8188); this keeps that server alive so the
// box needs no separate watchdog script or hand-rolled systemd unit — the
// hanzo CLI is the one way a BYO box joins the fleet, render backend included.
//
// Semantics (ported from the GB10 watchdog it replaces): health-probe
// /system_stats; on failure, one grace re-check, then free the port (a stale
// main.py holding :8188 makes the new one crash on EADDRINUSE) and relaunch
// from the checkout's venv. Emits a line only on restart events.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	studioAddr        = "127.0.0.1:8188"
	studioHealthURL   = "http://" + studioAddr + "/system_stats"
	studioProbeEvery  = 45 * time.Second
	studioGraceWait   = 8 * time.Second
	studioStartWindow = 180 * time.Second
)

func studioHealthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, studioHealthURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// studioPython picks the checkout's venv interpreter, falling back to python3.
func studioPython(dir string) string {
	venv := filepath.Join(dir, ".venv", "bin", "python")
	if _, err := os.Stat(venv); err == nil {
		return venv
	}
	return "python3"
}

// launchStudio starts the render backend from dir, logging to
// <dir>/studio-local.log. The process gets its own group so a restart can
// kill stragglers (torch dataloader workers and the like) in one signal.
func launchStudio(dir string) (*exec.Cmd, error) {
	logf, err := os.OpenFile(filepath.Join(dir, "studio-local.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(studioPython(dir), "main.py",
		"--listen", "0.0.0.0", "--port", "8188",
		"--normalvram", "--disable-auto-launch",
		"--output-directory", filepath.Join(dir, "output"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"STUDIO_PERSIST_QUEUE=1",
		"PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True",
	)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, err
	}
	logf.Close() // the child holds its own descriptor
	// Shield the render backend from the OOM killer ahead of pack/model RAM
	// spikes. Negative adj needs privilege; best-effort.
	_ = os.WriteFile("/proc/"+strconv.Itoa(cmd.Process.Pid)+"/oom_score_adj",
		[]byte("-700"), 0o644)
	go func() { _ = cmd.Wait() }() // reap; the probe loop decides on restarts
	return cmd, nil
}

// stopStudio kills the supervised process group (if any) and any foreign
// main.py still holding the port, then waits for the listener to clear.
func stopStudio(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	_ = exec.Command("pkill", "-f", "main.py --listen").Run()
	for i := 0; i < 15; i++ {
		conn, err := net.DialTimeout("tcp", studioAddr, time.Second)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(time.Second)
	}
}

// studioRecycle carries at most one pending recycle request; the render
// handler signals it after each completed render (see gpu.go).
var studioRecycle = make(chan struct{}, 1)

// staging guards the claim-to-submit window: a claimed job is real work the
// engine queue cannot see yet, so the supervisor must never recycle over it
// (observed: jobs claimed during a recycle failed staging on a dead engine
// and were consumed).
var staging atomic.Int32

func requestStudioRecycle() {
	select {
	case studioRecycle <- struct{}{}:
	default:
	}
}

// studioBusy reports whether the engine holds queued or running prompts.
// A generous timeout: a saturated GB10 answers slowly mid-render — slow is
// alive, and killing a live render costs 8-70 minutes of GPU work.
func studioBusy(ctx context.Context) (busy, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+studioAddr+"/queue", nil)
	if err != nil {
		return false, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	var q struct {
		Running []json.RawMessage `json:"queue_running"`
		Pending []json.RawMessage `json:"queue_pending"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&q); err != nil {
		return false, false
	}
	return len(q.Running)+len(q.Pending) > 0, true
}

// superviseStudio keeps the local render backend on :8188 alive until ctx
// ends. Quiet by design: one line per restart event, not a probe firehose.
func superviseStudio(ctx context.Context, dir string, out io.Writer) {
	var cmd *exec.Cmd
	restart := func(reason string) {
		stopStudio(cmd)
		c, err := launchStudio(dir)
		if err != nil {
			fmt.Fprintf(out, "studio: launch failed (%s): %v\n", reason, err)
			return
		}
		cmd = c
		deadline := time.Now().Add(studioStartWindow)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if studioHealthy(ctx) {
				fmt.Fprintf(out, "studio: serving on %s (pid %d, %s)\n", studioAddr, c.Process.Pid, reason)
				return
			}
			time.Sleep(4 * time.Second)
		}
		fmt.Fprintf(out, "studio: started pid %d (%s) but %s not healthy yet\n", c.Process.Pid, reason, studioAddr)
	}

	if !studioHealthy(ctx) {
		restart("boot")
	} else {
		fmt.Fprintf(out, "studio: already serving on %s\n", studioAddr)
	}

	tick := time.NewTicker(studioProbeEvery)
	defer tick.Stop()
	// recyclePending defers the post-render recycle until the queue is EMPTY:
	// short jobs complete while a long render is mid-sample, and recycling on
	// their completion killed the live render (observed: every direct render
	// died within ~6 minutes while probe jobs cycled).
	recyclePending := false
	unhealthy := 0
	for {
		select {
		case <-ctx.Done():
			if cmd != nil && cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			}
			return
		case <-studioRecycle:
			recyclePending = true
		case <-tick.C:
			busy, ok := studioBusy(ctx)
			if recyclePending && ok && !busy && staging.Load() == 0 {
				recyclePending = false
				unhealthy = 0
				restart("recycle")
				continue
			}
			if studioHealthy(ctx) {
				unhealthy = 0
				continue
			}
			if ok && busy {
				// Alive-busy: slow health under render load is not death.
				unhealthy = 0
				continue
			}
			// Sustained silence with an idle or unreadable queue = actually dead.
			unhealthy++
			if unhealthy < 3 {
				continue
			}
			unhealthy = 0
			restart("unresponsive")
		}
	}
}
