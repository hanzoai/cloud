package coding

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// wrapped is a turn's wrapper, started as a pod starts an exec's process: the
// leader of its own session, so the group it records is its own (solo). It
// returns the command, its group file and its two streams.
func wrapped(t *testing.T, harness, stdin string) (*exec.Cmd, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	if exec.Command("sh", "-c", "tail --pid=$$ -n 0 /dev/null && command -v setpriv && command -v ps").Run() != nil {
		t.Skip("this host's tail cannot follow a pid or it has no setpriv, so solo runs argv as it is")
	}
	group := filepath.Join(t.TempDir(), "turn")
	argv := keyed(solo(group, []string{"sh", "-c", harness}))
	cmd := exec.Command(argv[0], argv[1:]...)
	var o, e bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &o, &e
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Whatever a test leaves running in the turn's group goes with it.
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	return cmd, group, &o, &e
}

// ends waits for cmd as an exec does — for its exit AND for every holder of its
// streams to close them — and fails if that takes longer than within.
func ends(t *testing.T, cmd *exec.Cmd, within time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return exit.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	case <-time.After(within):
		t.Fatalf("the command was still open after %v", within)
		return -1
	}
}

// stops runs end's script over a turn's group file, as the runner does after a
// turn that did not end on its own, and reports whether the group is gone.
func stops(t *testing.T, group string, pgid int) {
	t.Helper()
	if out, err := exec.Command("sh", "-c", ended, "sh", group).CombinedOutput(); err != nil {
		t.Fatalf("end: %v: %s", err, out)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !alive(pgid) {
			if _, err := os.Stat(group); !os.IsNotExist(err) {
				t.Fatalf("end left the group's record behind: %v", err)
			}
			return
		}
	}
	t.Fatalf("a process of group %d outlived end", pgid)
}

// alive reports whether any process of group pgid is still running: a zombie
// waiting for its reaper is not.
func alive(pgid int) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
		if len(f) > 2 && f[2] == strconv.Itoa(pgid) && f[0] != "Z" {
			return true
		}
	}
	return false
}

// groupOf waits for the wrapper to write its group, and answers it.
func groupOf(t *testing.T, group string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(group); err == nil && len(b) > 0 {
			g, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatalf("the group record reads %q", b)
			}
			return g
		}
	}
	t.Fatal("the wrapper never recorded its group")
	return 0
}

// A HARNESS'S EXIT ENDS ITS RUN. The program below prints on both streams, reads
// its credentials and its task on stdin, leaves a process running with both
// streams inherited, and exits 3. Run bare, the pipes stay open for as long as
// that process lives; through solo the command answers when the program exits,
// with its output, its exit code and its stdin intact, and no record is left.
func TestSoloEndsWhenItsProgramDoes(t *testing.T) {
	cmd, group, o, e := wrapped(t,
		`t=$(cat); echo "out:$HANZO_API_KEY:$HANZO_RUN:$t"; echo err >&2; sleep 30 & exit 3`,
		fed("key", "tick")+"fix the build")
	began := time.Now()
	if code := ends(t, cmd, 10*time.Second); code != 3 {
		t.Fatalf("exit = %d, want the program's 3", code)
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("answered after %v: the process the program left behind held the command open", took)
	}
	if o.String() != "out:key:tick:fix the build\n" || e.String() != "err\n" {
		t.Fatalf("stdout %q stderr %q", o.String(), e.String())
	}
	if _, err := os.Stat(group); !os.IsNotExist(err) {
		t.Fatalf("a turn that ended on its own left its group record: %v", err)
	}
}

// THE TASK IS IN NO COMMAND LINE. A task that says "go build" reached the
// cmdline of dev and of its wrapper, and the model's own `pkill -f "go build"`
// killed its harness (sess_06e8f87b…). Read from stdin, it is in no process's
// /proc/<pid>/cmdline, and a pkill of its words finds nothing to kill.
func TestATurnSurvivesAPkillOfItsOwnTask(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc to read command lines from")
	}
	mark := "TASK" + strconv.Itoa(os.Getpid()) + " go build ./..."
	cmd, _, o, _ := wrapped(t, `t=$(cat); sleep 2; echo "survived:$t"`, fed("key", "")+mark)
	time.Sleep(500 * time.Millisecond)
	ents, _ := os.ReadDir("/proc")
	for _, ent := range ents {
		if b, err := os.ReadFile(filepath.Join("/proc", ent.Name(), "cmdline")); err == nil && bytes.Contains(b, []byte(mark)) {
			t.Fatalf("pid %s carries the task in its command line: %q", ent.Name(), bytes.ReplaceAll(b, []byte{0}, []byte{' '}))
		}
	}
	if err := exec.Command("pkill", "-f", mark).Run(); err == nil {
		t.Fatal("pkill of the task's words matched a process")
	}
	if code := ends(t, cmd, 10*time.Second); code != 0 || o.String() != "survived:"+mark+"\n" {
		t.Fatalf("exit %d, stdout %q: the turn did not survive", code, o.String())
	}
}

// A HARNESS KILLED MID-TURN ENDS ITS RUN AT ONCE, and what it left running is
// stopped by end. The record stays for end, since the harness did not exit on
// its own.
func TestSoloEndsWhenItsHarnessIsKilled(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "harness")
	cmd, group, _, _ := wrapped(t, `echo $$ >`+pidFile+`; sleep 60 & sleep 61`, fed("", ""))
	pgid := groupOf(t, group)
	if err := syscall.Kill(pidIn(t, pidFile), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if code := ends(t, cmd, 5*time.Second); code != 137 {
		t.Fatalf("exit = %d, want 137 for a harness killed by SIGKILL", code)
	}
	if !alive(pgid) {
		t.Fatal("the harness's child should still be running until end stops it")
	}
	stops(t, group, pgid)
}

// A WRAPPER KILLED MID-TURN ENDS ITS RUN AT ONCE: the harness and both tails die
// with it (setpriv --pdeathsig), so nothing holds the exec open and nothing goes
// on writing — the 32-minute hang this replaces left a sleep, the tails and the
// exec alive. What the harness left running is stopped by end. (The pod's PID 1
// reaps nothing, so there the harness stays a zombie that `tail --pid` reads as
// alive, and only the tails' own pdeathsig closes the exec; this host's init
// reaps, so that half is measured in the pod, not here.)
func TestSoloEndsWhenItsWrapperIsKilled(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "harness")
	cmd, group, _, _ := wrapped(t, `echo $$ >`+pidFile+`; sleep 60 & sleep 61`, fed("", ""))
	pgid := groupOf(t, group)
	h := pidIn(t, pidFile)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if code := ends(t, cmd, 5*time.Second); code != 137 {
		t.Fatalf("exit = %d, want 137 for a wrapper killed by SIGKILL", code)
	}
	for deadline := time.Now().Add(2 * time.Second); running(h); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the harness outlived its wrapper")
		}
	}
	stops(t, group, pgid)
}

// pidIn waits for a harness to write its pid to path, and answers it.
func pidIn(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(path)
		if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			return p
		}
	}
	t.Fatal("the harness never wrote its pid")
	return 0
}

// running reports whether pid is a live process: a zombie is not.
func running(pid int) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	return len(f) > 0 && f[0] != "Z"
}

// A HARNESS STARTS WITH THE SIGNALS IT WOULD HAVE IN THE FOREGROUND. A shell
// starts a background job with SIGINT and SIGQUIT ignored, and every child
// inherits that, so a server the model starts could not be interrupted and a
// repository's own test of graceful shutdown would hang.
//
// The harness sets a trap for each and signals itself. A shell cannot trap a
// signal that was ignored when it started, so a handled signal says its name and
// an ignored one says nothing. The mask is not read from /proc/self/status: the
// sandbox the gate runs in (visor) leaves SigIgn out of that file.
//
// MUTATION: drop `env --default-signal=INT,QUIT` from solo and neither name is said.
func TestTheHarnessStartsWithInterruptAndQuitHandled(t *testing.T) {
	cmd, _, o, _ := wrapped(t, `trap 'echo INT' INT; trap 'echo QUIT' QUIT; kill -INT $$; kill -QUIT $$; echo done`, fed("", ""))
	if code := ends(t, cmd, 10*time.Second); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got, want := o.String(), "INT\nQUIT\ndone\n"; got != want {
		t.Fatalf("the harness starts with SIGINT or SIGQUIT ignored: it said %q, want %q", got, want)
	}
}

// AN EXIT THE HARNESS CHOSE LEAVES NO RECORD, above the signals as below them:
// 255 is not a signal, and a record left for it would wait for an end that is
// never asked for.
func TestAnExitAboveTheSignalsLeavesNoRecord(t *testing.T) {
	cmd, group, _, _ := wrapped(t, `exit 255`, fed("", ""))
	if code := ends(t, cmd, 10*time.Second); code != 255 {
		t.Fatalf("exit %d, want 255", code)
	}
	if _, err := os.Stat(group); !os.IsNotExist(err) {
		t.Fatalf("exit 255 left its group record: %v", err)
	}
}

// END ACTS ONLY ON A RECORD THE WRAPPER COULD HAVE WRITTEN. The record is in
// /tmp, within reach of the model's commands: written `1` it would be `kill -1`,
// every process the sandbox's user runs, written `0` it is `kill 0`, end's own
// group, and a FIFO in its place would hold end for its whole wait. Each is
// refused at once.
//
// end's script runs with kill and sleep shadowed by functions that only log
// what they were asked, so a regression here records a kill instead of sending
// one: a real `kill -1` on this host is every process of the user running the
// test, and a real `kill 0` took down the shell that ran it.
//
// MUTATION: drop the digits case from ended and "signed" or "spaced" logs a
// kill; drop the `-gt 1` test and "one" or "zero" does.
func TestEndRefusesARecordTheWrapperDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		make func(string) error
		kill string // what end asks kill, "" for nothing
	}{
		"the wrapper's": {make: func(p string) error { return os.WriteFile(p, []byte("4321\n"), 0o600) }, kill: "-TERM -4321\n-KILL -4321\n"},
		"one":           {make: func(p string) error { return os.WriteFile(p, []byte("1\n"), 0o600) }},
		"zero":          {make: func(p string) error { return os.WriteFile(p, []byte("0\n"), 0o600) }},
		"signed":        {make: func(p string) error { return os.WriteFile(p, []byte("+4321\n"), 0o600) }},
		"spaced":        {make: func(p string) error { return os.WriteFile(p, []byte(" 4321\n"), 0o600) }},
		"negative":      {make: func(p string) error { return os.WriteFile(p, []byte("-4321\n"), 0o600) }},
		"fifo":          {make: func(p string) error { return syscall.Mkfifo(p, 0o600) }},
		"symlink":       {make: func(p string) error { return os.Symlink(filepath.Join(dir, "the wrapper's"), p) }},
		"nothing":       {make: func(string) error { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			p, log := filepath.Join(dir, name), filepath.Join(dir, name+".log")
			if err := c.make(p); err != nil {
				t.Fatal(err)
			}
			shadowed := `kill() { printf '%s\n' "$*" >>"$LOG"; }; sleep() { :; }
` + ended
			cmd := exec.Command("sh", "-c", shadowed, "sh", p)
			cmd.Env = append(os.Environ(), "LOG="+log)
			began := time.Now()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("end: %v: %s", err, out)
			}
			if took := time.Since(began); took > 2*time.Second {
				t.Fatalf("end took %v", took)
			}
			asked, _ := os.ReadFile(log)
			if got := strings.ReplaceAll(string(asked), " 2>/dev/null", ""); got != c.kill {
				t.Fatalf("end asked kill %q, want %q", got, c.kill)
			}
		})
	}
}

// A TURN'S OUTPUT IS READABLE WHILE IT RUNS, at the path its beat reads (#553):
// group.d/o holds what the harness has written so far, so a turn twenty minutes
// into `make test` says so instead of reading as stalled.
//
// MUTATION: put solo's outputs back in a mktemp directory and nothing is at
// group.d/o while the turn runs.
func TestATurnsOutputIsReadableWhileItRuns(t *testing.T) {
	started := `{"type":"item.started","item":{"id":"item_7","type":"command_execution","command":"bash -lc \"make test\""}}`
	cmd, group, _, _ := wrapped(t, `echo '`+started+`'; sleep 3`, fed("", ""))
	var got string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(group + ".d/o"); err == nil && len(b) > 0 {
			got = string(b)
			break
		}
	}
	if id, c := underway(got); id != "item_7" || c != "make test" {
		t.Fatalf("read %q from the turn's output (%q), want item_7 running make test", c, got)
	}
	if code := ends(t, cmd, 10*time.Second); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(group + ".d"); !os.IsNotExist(err) {
		t.Fatalf("a turn that ended left its output behind: %v", err)
	}
}
