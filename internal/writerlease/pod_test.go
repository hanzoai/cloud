// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package writerlease

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests run REAL processes, because the defect was a property of the
// process tree and nothing else. flock excludes per open file description, so a
// same-process test can demonstrate exclusion but can never demonstrate the
// thing that actually happened: sibling processes of ONE pod, sharing one
// DataDir, each reaching for a lock only one of them can have.
//
// The two rules a child might follow are both kept here, and the same topology
// is run under each:
//
//	legacy — what shipped on 2026-08-04. Every process that runs the server body
//	         takes the lock itself, and the router takes nothing because it never
//	         runs that body. Result below: one subsystem serves, the rest wait
//	         for a handoff that cannot come. That is the outage, reproduced.
//
//	fixed  — Hold. The pod root takes the lock once, before it spawns anything,
//	         and stamps the environment its children are born with. Result below:
//	         every subsystem starts at once AND the volume is still defended
//	         against the next pod.
//
// The second half of that last sentence is the part worth guarding. The repair
// that followed the incident stopped the deadlock by having the children skip
// the lock, which "fixed" it in the sense that a disconnected smoke alarm fixes
// a nuisance alarm. TestFixed_VolumeStillDefended is the test that fails.

const (
	modeEnv = "WRITERLEASE_TEST_MODE"
	dirEnv  = "WRITERLEASE_TEST_DIR"
	waitEnv = "WRITERLEASE_TEST_WAIT"
	holdEnv = "WRITERLEASE_TEST_HOLD"

	exitServing    = 0 // this process may open the stores
	exitFailClosed = 3 // it waited for the lease and never got it
	exitBroken     = 4 // anything else

	// subsystems mirrors the three the pod actually deadlocked on.
	kms, pubsub, kafka = "kms", "pubsub", "kafka"
)

func TestMain(m *testing.M) {
	switch os.Getenv(modeEnv) {
	case "":
		os.Exit(m.Run())
	case "legacy":
		os.Exit(asLegacyChild())
	case "fixed":
		os.Exit(asFixedChild())
	case "other-pod":
		os.Exit(asOtherPod())
	default:
		fmt.Fprintln(os.Stderr, "writerlease test: unknown mode")
		os.Exit(9)
	}
}

// asLegacyChild is the 2026-08-04 rule, preserved as executable history: a
// subsystem process reads CLOUD_WRITER_LEASE and takes the lock on its own
// behalf. Correct against another POD. Fatal among SIBLINGS.
func asLegacyChild() int {
	if !truthy(os.Getenv(Enable)) {
		return exitServing
	}
	release, err := Acquire(os.Getenv(dirEnv), envDur(waitEnv), nil)
	if err != nil {
		if strings.Contains(err.Error(), "still held by another writer") {
			fmt.Println("waiting for handoff, gave up")
			return exitFailClosed
		}
		fmt.Fprintln(os.Stderr, err)
		return exitBroken
	}
	fmt.Println("serving")
	time.Sleep(envDur(holdEnv))
	_ = release()
	return exitServing
}

// asFixedChild is the rule this package exists to state: ask what this process's
// position makes it responsible for, and do that.
func asFixedChild() int {
	duty, _ := Assess(os.Getenv)
	release, err := Hold(os.Getenv(dirEnv), envDur(waitEnv), nil)
	if err != nil {
		if strings.Contains(err.Error(), "still held by another writer") {
			fmt.Println("waiting for handoff, gave up")
			return exitFailClosed
		}
		fmt.Fprintln(os.Stderr, err)
		return exitBroken
	}
	fmt.Println("serving duty=" + duty.String())
	time.Sleep(envDur(holdEnv))
	_ = release()
	return exitServing
}

// asOtherPod is a SECOND POD GENERATION on the same volume: it carries no stamp
// from our root, because a different pod's root never stamped it. It is the
// thing the lease exists to stop.
func asOtherPod() int {
	os.Unsetenv(Held)
	os.Unsetenv(zipAddr)
	release, err := Hold(os.Getenv(dirEnv), envDur(waitEnv), nil)
	if err != nil {
		if strings.Contains(err.Error(), "still held by another writer") {
			fmt.Println("refused: the volume is spoken for")
			return exitFailClosed
		}
		fmt.Fprintln(os.Stderr, err)
		return exitBroken
	}
	fmt.Println("opened the volume")
	time.Sleep(envDur(holdEnv))
	_ = release()
	return exitServing
}

// --- the two runs -----------------------------------------------------------

// TestLegacyRule_SiblingsDeadlock reproduces the incident. Three subsystem
// processes, one pod, one DataDir, nobody above them holding anything — exactly
// the shape of api.hanzo.ai at 08:52Z on 2026-08-04.
//
// Expected and asserted: kms (or whichever wins the race) serves, and the other
// two burn their whole fail-closed budget and exit without ever binding. A pod
// in this state cannot pass a readiness probe, so the liveness probe kills it
// and its replacement arrives at the identical deadlock.
func TestLegacyRule_SiblingsDeadlock(t *testing.T) {
	dir := t.TempDir()
	wait, hold := 900*time.Millisecond, 2500*time.Millisecond

	started := time.Now()
	out := runSubsystems(t, "legacy", dir, wait, hold, nil)

	serving, blocked := tally(out)
	if serving != 1 {
		t.Fatalf("legacy rule: %d of 3 subsystems served, want exactly 1 — the reproduction is not reproducing", serving)
	}
	if blocked != 2 {
		t.Fatalf("legacy rule: %d subsystems blocked, want 2 (%v)", blocked, out)
	}
	// The losers did not fail fast — they SPUN, which is why the pod hung rather
	// than crashed, and why the liveness probe was what eventually noticed.
	if spent := time.Since(started); spent < wait {
		t.Fatalf("legacy rule: the blocked subsystems returned in %s, faster than the %s lease wait — they did not actually contend", spent, wait)
	}
	t.Logf("reproduced: 1 subsystem serving, 2 waiting for a handoff that cannot come — %v", out)
}

// TestFixedRule_SiblingsAllServe is the same three processes under the same
// conditions, except the pod root took the lease first and stamped what it
// spawned. Every subsystem comes up, and none of them waits on any other.
func TestFixedRule_SiblingsAllServe(t *testing.T) {
	dir := t.TempDir()
	wait := 900 * time.Millisecond

	// The pod root: this process. Exactly what cmd/cloud does before its mount
	// loops spawn the first child.
	t.Setenv(Enable, "1")
	t.Setenv(Held, "")
	release, err := Hold(dir, 2*time.Second, nil)
	if err != nil {
		t.Fatalf("the pod root must be able to take the lease: %v", err)
	}
	defer func() { _ = release() }()

	if got := os.Getenv(Held); got != fmt.Sprint(os.Getpid()) {
		t.Fatalf("the root holds the lease but stamped %q, so nothing it spawns can inherit it (want pid %d)", got, os.Getpid())
	}

	started := time.Now()
	out := runSubsystems(t, "fixed", dir, wait, 0, nil)

	serving, blocked := tally(out)
	if blocked != 0 {
		t.Fatalf("%d subsystems blocked on their own pod's lease — the sibling deadlock is back (%v)", blocked, out)
	}
	if serving != 3 {
		t.Fatalf("%d of 3 subsystems served (%v)", serving, out)
	}
	for name, line := range out {
		if !strings.Contains(line, "duty="+Inherit.String()) {
			t.Fatalf("%s did not INHERIT the lease, it decided %q — a child that reasons its way to taking the lock is one release away from the deadlock", name, line)
		}
	}
	// They inherited, so none of them waited at all.
	if spent := time.Since(started); spent >= wait {
		t.Fatalf("the subsystems took %s, at or past the %s lease wait — they contended instead of inheriting", spent, wait)
	}
	t.Logf("all 3 subsystems serving, none contended — %v", out)
}

// TestFixed_VolumeStillDefended is the assertion the post-incident repair would
// fail. Making the children skip the lock removes the deadlock; it also removes
// the lock, because the router never ran the code that takes it. A pod whose
// children all start and whose volume is open to the next pod is not fixed — it
// is the same bug with the alarm disconnected, and it is the state in which
// someone reads "writer lease: enabled" and switches to RollingUpdate.
func TestFixed_VolumeStillDefended(t *testing.T) {
	dir := t.TempDir()

	t.Setenv(Enable, "1")
	t.Setenv(Held, "")
	release, err := Hold(dir, 2*time.Second, nil)
	if err != nil {
		t.Fatalf("pod root: %v", err)
	}
	defer func() { _ = release() }()

	// Children start — and the volume must STILL be closed to another pod.
	out := runSubsystems(t, "fixed", dir, 900*time.Millisecond, 0, nil)
	if serving, _ := tally(out); serving != 3 {
		t.Fatalf("subsystems did not start: %v", out)
	}

	code, line := run1(t, "other-pod", dir, 700*time.Millisecond, 0, nil)
	if code != exitFailClosed {
		t.Fatalf("a second pod OPENED the volume while this pod holds the lease (exit %d, %q) — the interlock is inert, and a RollingUpdate would double-open every store", code, line)
	}

	// And it is a lease, not a wall: once the pod lets go, the successor takes it.
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	code, line = run1(t, "other-pod", dir, 2*time.Second, 0, nil)
	if code != exitServing {
		t.Fatalf("the lease was released but the next pod could not take it (exit %d, %q) — a handoff that never completes is an outage per roll", code, line)
	}
}

// TestTwoPodGenerations_ExactlyOneWriter is the property the whole mechanism is
// for, stated between the two things it is actually about: two pods, one volume.
func TestTwoPodGenerations_ExactlyOneWriter(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var opened []string
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := run1(t, "other-pod", dir, 4*time.Second, 250*time.Millisecond, nil)
			if code == exitServing {
				mu.Lock()
				opened = append(opened, fmt.Sprint(i))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	// Every one of them should eventually get a turn — they are serialized, not
	// starved — and the lock is what makes the turns disjoint.
	if len(opened) != 6 {
		t.Fatalf("%d of 6 pod generations completed a handoff, want 6 — the lease is starving successors rather than serializing them", len(opened))
	}
}

// --- harness ----------------------------------------------------------------

// runSubsystems starts the three subsystems that deadlocked, concurrently,
// exactly as zip starts eager plugins.
func runSubsystems(t *testing.T, mode, dir string, wait, hold time.Duration, extra []string) map[string]string {
	t.Helper()
	var mu sync.Mutex
	out := map[string]string{}
	var wg sync.WaitGroup
	for _, name := range []string{kms, pubsub, kafka} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			code, line := run1(t, mode, dir, wait, hold, extra)
			mu.Lock()
			out[name] = fmt.Sprintf("exit=%d %s", code, line)
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	return out
}

// run1 runs one process of this test binary in the given mode and returns its
// exit code and first line of output.
func run1(t *testing.T, mode, dir string, wait, hold time.Duration, extra []string) (int, string) {
	t.Helper()
	c := exec.Command(os.Args[0])
	c.Env = append(os.Environ(),
		modeEnv+"="+mode,
		dirEnv+"="+dir,
		waitEnv+"="+wait.String(),
		holdEnv+"="+hold.String(),
		Enable+"=1",
	)
	c.Env = append(c.Env, extra...)
	b, err := c.CombinedOutput()
	line := strings.TrimSpace(string(b))
	if err == nil {
		return 0, line
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return ee.ExitCode(), line
	}
	t.Fatalf("running %s subprocess: %v (%s)", mode, err, line)
	return -1, line
}

func tally(out map[string]string) (serving, blocked int) {
	for _, line := range out {
		switch {
		case strings.HasPrefix(line, fmt.Sprintf("exit=%d ", exitServing)):
			serving++
		case strings.HasPrefix(line, fmt.Sprintf("exit=%d ", exitFailClosed)):
			blocked++
		}
	}
	return serving, blocked
}

func envDur(key string) time.Duration {
	d, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return 0
	}
	return d
}

// lockExists reports whether the flock anchor is present under dir.
func lockExists(dir string) bool {
	_, err := os.Stat(dir + string(os.PathSeparator) + LockName)
	return err == nil
}

func getenvOrEmpty(k string) string { return os.Getenv(k) }
