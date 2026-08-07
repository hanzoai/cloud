package functions

// The runner. Everything that decides WHAT executes lives in this one file, so
// the answer to "can a request influence the command line?" is read in a single
// place: it cannot. A request names a runtime; the runtime names an interpreter;
// the interpreter is resolved on PATH and handed exactly one argument, a file
// this package just wrote.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// runtime is one entry of the allow-list: the binary to resolve on PATH, and the
// extension the source is written with (interpreters and their tooling key off
// it).
type runtime struct {
	bin string
	ext string
}

// runtimes IS the allow-list. A package-level table, never assembled from
// configuration and never touched by a request, which is what makes "no caller
// can choose the interpreter" a property of the code rather than of a check
// somebody has to remember to write. Adding a language is an edit here and
// nowhere else.
var runtimes = map[string]runtime{
	"node":    {bin: "node", ext: ".js"},
	"python3": {bin: "python3", ext: ".py"},
	"bash":    {bin: "bash", ext: ".sh"},
}

// The ledger's status vocabulary. Four words, exhaustive: it ran and finished
// cleanly, it ran and failed, it ran too long and was killed, or nothing ran.
const (
	statusOK          = "ok"
	statusError       = "error"
	statusTimeout     = "timeout"
	statusUnavailable = "unavailable"
)

// noExit is the exit code recorded when the process produced none — killed at the
// deadline, or never started.
const noExit = -1

// waitDelay is how long Wait lingers after the deadline kill before giving up on
// the output pipes. A grandchild that inherited them can otherwise hold Wait open
// long after the process it was spawned by is gone.
const waitDelay = time.Second

// result is one attempt's outcome, in the shape the ledger and the response both
// read.
type result struct {
	status    string
	exitCode  int
	duration  time.Duration
	stdout    string
	stderr    string
	truncated bool
}

// run executes code under the named runtime and returns what happened. It never
// returns an error: every failure is an outcome with a status, because an
// execution surface whose failures are exceptions is one whose failures go
// unrecorded.
func run(ctx context.Context, name, code, input string, timeout time.Duration) result {
	started := time.Now()

	// The name→interpreter mapping happens HERE and only here. A stored row
	// naming a runtime the allow-list no longer carries is refused on this line,
	// so shrinking the list disables old functions rather than leaving them
	// running on a rule that was withdrawn.
	rt, ok := runtimes[name]
	if !ok {
		return unavailable(started, fmt.Sprintf("runtime %q is not available on this host", name))
	}
	bin, err := exec.LookPath(rt.bin)
	if err != nil {
		return unavailable(started, fmt.Sprintf("runtime %q needs %s, which is not installed on this host", name, rt.bin))
	}

	dir, err := os.MkdirTemp("", "hanzo-fn-")
	if err != nil {
		return unavailable(started, "cannot open a working directory for the run")
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// 0600 and no execute bit: the interpreter READS this file. Making it
	// executable would be handing out a second way to start it.
	src := filepath.Join(dir, "main"+rt.ext)
	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		return unavailable(started, "cannot write the function source for the run")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &capped{max: maxOutputBytes}
	errOut := &capped{max: maxOutputBytes}

	// argv, never a shell string. The code is a FILE and the file is an argument,
	// so there is no command line for it to break out of; bin came from the
	// allow-list and src is a path this function just made, so neither operand
	// carries anything a caller wrote.
	cmd := exec.CommandContext(ctx, bin, src)
	cmd.Dir = dir           // fixed; a request never picks where it runs
	cmd.Env = childEnv(dir) // minimal; see childEnv
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = out
	cmd.Stderr = errOut
	cmd.WaitDelay = waitDelay
	// The child leads its OWN process group and the deadline kills the GROUP.
	// Killing only the process we started is not enough: `sleep 30 &` outlives its
	// parent, holds the output pipes open, and keeps running long after the caller
	// got its timeout — on a surface anyone can call, that is a way to pile up work
	// the host never agreed to. The sweep after Wait catches the same trick from a
	// function that backgrounds something and then exits 0.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	runErr := cmd.Run()
	_ = killGroup(cmd)

	r := result{
		duration:  time.Since(started),
		stdout:    out.String(),
		stderr:    errOut.String(),
		truncated: out.cut || errOut.cut,
	}
	switch {
	case runErr == nil:
		// Success is decided before the clock: a run that finished as the deadline
		// landed still finished, and calling that a timeout would be a lie about a
		// process that exited 0.
		r.status, r.exitCode = statusOK, 0
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.status, r.exitCode = statusTimeout, noExit
	default:
		r.status, r.exitCode = statusError, noExit
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			r.exitCode = exit.ExitCode()
		}
	}
	return r
}

// killGroup SIGKILLs the run's whole process group. The NEGATIVE pid is the
// group, and the child leads one, so a single signal reaches the interpreter and
// everything it spawned. A group that is already gone answers os.ErrProcessDone,
// which is what os/exec reads as "nothing left to cancel" — returning the raw
// ESRCH instead would turn a run that finished on its own into a reported error.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return os.ErrProcessDone
	}
	return nil
}

// unavailable is the outcome when nothing ran. The reason goes on stderr, which
// is where a caller looks for why and is what the ledger keeps, so the record
// explains itself without a second field nobody reads.
func unavailable(started time.Time, reason string) result {
	return result{
		status:   statusUnavailable,
		exitCode: noExit,
		duration: time.Since(started),
		stderr:   reason,
	}
}

// childEnv is the ENTIRE environment a function gets: PATH so an ordinary script
// can find the ordinary tools, and HOME pointed at the run's own directory so an
// interpreter that wants a home finds a writable one that dies with the run.
//
// The server's environment carries the KMS master key and every other secret the
// process was started with. Inheriting it — which is what os/exec does when Env
// is nil — would hand all of that to code a caller wrote. This is the line that
// stops it, so it is stated rather than defaulted.
func childEnv(dir string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
}

// capped is a bounded sink for one output stream: it keeps the first max bytes
// and remembers that it dropped the rest. A function that prints in a loop then
// costs a fixed amount of memory and a fixed amount of ledger instead of as much
// as it feels like producing.
type capped struct {
	buf bytes.Buffer
	max int
	cut bool
}

// Write keeps what fits and reports the full length regardless. Reporting a short
// write would make os/exec's copier treat a deliberate cap as an I/O error and
// kill an otherwise healthy run.
func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) <= room {
			return c.buf.Write(p)
		}
		_, _ = c.buf.Write(p[:room])
	}
	c.cut = true
	return len(p), nil
}

func (c *capped) String() string { return c.buf.String() }

// names is the allow-list, sorted — for the "unknown runtime" message. Sorted
// because a map's order is not a thing to print at somebody.
func names() []string { return slices.Sorted(maps.Keys(runtimes)) }

// installed reports which runtimes this host can actually run. It is the boot
// line an operator reads to learn why python3 works here and node does not.
func installed() []string {
	out := make([]string, 0, len(runtimes))
	for _, name := range names() {
		if _, err := exec.LookPath(runtimes[name].bin); err == nil {
			out = append(out, name)
		}
	}
	return out
}
