package cli

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

// The studio a previous run left behind is found by the PID it recorded, and
// only while that PID is still a process working in the checkout.
func TestLaunchedIsOnlyTheStudioThisCheckoutStarted(t *testing.T) {
	if _, err := os.Stat("/proc/self/cwd"); err != nil {
		t.Skip("no /proc: nothing is recognised, so nothing is signalled")
	}
	dir, other := t.TempDir(), t.TempDir()
	if launched(dir) != 0 {
		t.Fatal("no PID file names no process")
	}
	start := func(cwd string) *exec.Cmd {
		c := exec.Command("sleep", "30")
		c.Dir = cwd
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
		return c
	}
	ours := start(dir)
	if err := os.WriteFile(studioPIDFile(dir), []byte(strconv.Itoa(ours.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := launched(dir); got != ours.Process.Pid {
		t.Fatalf("launched = %d, want the recorded %d", got, ours.Process.Pid)
	}
	theirs := start(other)
	if err := os.WriteFile(studioPIDFile(dir), []byte(strconv.Itoa(theirs.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := launched(dir); got != 0 {
		t.Fatalf("a PID working elsewhere is not this checkout's studio, got %d", got)
	}
	stopStudio(nil, dir)
	if theirs.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("stopStudio signalled a process it did not start")
	}
	if _, err := os.Stat(studioPIDFile(dir)); !os.IsNotExist(err) {
		t.Fatal("stopStudio leaves no PID file behind")
	}
}
