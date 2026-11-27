package cli

// studio_bootgrace_test.go — a studio that is still loading its model must not be
// mistaken for a dead one.
//
// The probes cannot tell the two apart: while the 41GB Qwen-Edit model pages in, the
// HTTP server is not listening, so studioHealthy() is false AND studioBusy() returns
// ok=false, which disqualifies the "alive-busy" guard. The supervisor therefore killed
// the process ~5 minutes into a load that needs 5-11 minutes and relaunched it into the
// same fate — 5 restarts in 30 minutes with zero renders. The distinguishing fact is
// whether the process still EXISTS, which is what studioExited answers.

import (
	"os/exec"
	"testing"
	"time"
)

// A live-but-silent child inside the grace window must be left alone; the same child
// once the grace has elapsed must be declared dead so a truly wedged studio recovers.
func TestBootGraceSpareLiveStudioNotDeadOne(t *testing.T) {
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stub studio: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	if studioExited(cmd) {
		t.Fatal("studioExited = true for a running process, want false")
	}

	// This is the exact predicate the supervisor uses to skip the death counter.
	spared := func(launchedAt time.Time) bool {
		return !studioExited(cmd) && time.Since(launchedAt) < studioBootGrace
	}
	if !spared(time.Now()) {
		t.Error("a live studio that just launched was not spared — this is the restart loop")
	}
	if spared(time.Now().Add(-studioBootGrace - time.Minute)) {
		t.Error("a live studio past its boot grace was spared — a wedged studio would never recover")
	}
}

// A process that actually died must be restartable immediately, regardless of grace,
// so a crash is not papered over for 15 minutes.
func TestExitedStudioIsNeverSpared(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil { // Run waits, so it has definitely exited
		t.Fatalf("run stub: %v", err)
	}
	if !studioExited(cmd) {
		t.Fatal("studioExited = false for an exited process, want true")
	}
	if !studioExited(nil) {
		t.Fatal("studioExited(nil) = false, want true (nothing launched yet)")
	}
	// Freshly "launched" yet already dead => must NOT be spared.
	if !studioExited(cmd) && time.Since(time.Now()) < studioBootGrace {
		t.Error("an exited studio was spared by boot grace; a crash would stall the worker")
	}
}
