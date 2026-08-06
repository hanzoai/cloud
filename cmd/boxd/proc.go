// Running things: the one place in this repo where os/exec is correct.
//
// Everywhere else in cloud, os/exec is a bug — apps/exec says so in its own
// header ("there is no os/exec anywhere in this package"), and apps/sandbox
// repeats it. That rule is not "never run code"; it is "never run code on the
// SAME side of the isolation boundary as the credentials". boxd is on the other
// side. This file is what all of those packages are refusing to be.
package main

import (
	"bytes"
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/sandbox/wire"

	"github.com/zap-proto/zip"
)

const (
	defaultTimeoutSec = 120
	maxTimeoutSec     = 3600 // an hour: a cold `pnpm install` on a big repo is minutes
	maxCapture        = 4 << 20
)

func (b *box) exec(c *zip.Ctx) error {
	var req wire.ExecRequest
	if err := c.Bind(&req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "%s", "body: "+err.Error())
	}
	res, err := b.runCmd(c.Context(), req)
	if err != nil {
		return zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	// 200 with a non-zero ExitCode, always. "Your tests failed" and "the box is
	// broken" are different facts; a caller that cannot separate them retries
	// the wrong one, forever.
	return c.JSON(http.StatusOK, res)
}

// runCmd is the HTTP-facing exec path: it resolves the caller's `Cwd` under the
// project root, then runs. Everything that arrives from the network comes
// through here, so the containment check has exactly one home.
func (b *box) runCmd(ctx context.Context, req wire.ExecRequest) (wire.ExecResult, error) {
	cwd := b.workdir
	if req.Cwd != "" {
		abs, ok := b.resolve(req.Cwd)
		if !ok {
			return wire.ExecResult{}, errBadReq("cwd escapes the project")
		}
		cwd = abs
	}
	return b.runIn(ctx, cwd, req)
}

// runIn runs in a directory this process already chose — a git checkout, an
// interpreter session under /tmp. It exists because those directories are ours,
// not the caller's, and forcing them back through `resolve` would either be a
// lie (a /tmp session is not under the project) or a hole (a resolve that
// accepts absolute paths accepts them from the network too). The split is the
// point: `runCmd` is the untrusted door, `runIn` is the trusted one.
func (b *box) runIn(ctx context.Context, cwd string, req wire.ExecRequest) (wire.ExecResult, error) {
	argv := req.Argv
	if len(argv) == 0 {
		if strings.TrimSpace(req.Command) == "" {
			return wire.ExecResult{}, errBadReq("argv or command required")
		}
		// The shell form is second, and explicit. `sh -lc` is what the coding
		// agent's own steps are written as; argv skips the shell entirely and is
		// what a caller should use when it is not writing shell.
		argv = []string{"/bin/sh", "-lc", req.Command}
	}

	sec := req.TimeoutSec
	if sec <= 0 {
		sec = defaultTimeoutSec
	} else if sec > maxTimeoutSec {
		sec = maxTimeoutSec
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(sec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...) //nolint:gosec // running submitted code IS this binary's job; the pod is the boundary
	cmd.Dir = cwd
	cmd.Env = b.env(req.Env)

	// A timeout must actually STOP the work, and by default it does not.
	//
	// exec.CommandContext kills only the direct child. `sh -c "sleep 300"` forks
	// sleep, so killing sh leaves sleep alive holding the stdout pipe — Run()
	// then blocks until the grandchild finishes on its own. Measured: a 1-second
	// timeout took 30 seconds to return. In a warm pool that is a box that looks
	// idle, gets claimed, and is already busy.
	//
	// So: put the child in its own process group and signal the GROUP. TERM
	// first because a build that is mid-write deserves the chance to finish the
	// line; KILL after a grace period because a sandbox does not negotiate.
	confine(cmd, b.execUID, b.execGID)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 5 * time.Second
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	var out, errb cappedBuf
	out.max, errb.max = maxCapture, maxCapture
	cmd.Stdout, cmd.Stderr = &out, &errb

	start := time.Now()
	runErr := cmd.Run()
	res := wire.ExecResult{
		Stdout:     out.String(),
		Stderr:     errb.String(),
		DurationMs: time.Since(start).Milliseconds(),
	}
	if cctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			// Never launched at all (no such binary). -1 is distinguishable from
			// every real exit status, and the reason goes in stderr where a
			// caller already looks.
			res.ExitCode = -1
			res.Stderr = strings.TrimSpace(res.Stderr + "\n" + runErr.Error())
		}
	}
	return res, nil
}

// env is the child's environment: the box's own, plus the caller's overrides.
// The API key is stripped — a box runs submitted code, and handing that code the
// credential that opens every other box in the pool would make the pod boundary
// decorative.
func (b *box) env(extra map[string]string) []string {
	base := map[string]string{
		"HOME":     b.workdir,
		"PATH":     envOr("PATH", "/usr/local/bin:/usr/bin:/bin"),
		"LANG":     "C.UTF-8",
		"CI":       "1",
		"TERM":     "dumb",
		"NO_COLOR": "1",
	}
	for k, v := range extra {
		if k == "" || strings.EqualFold(k, "CODE_EXEC_API_KEY") || strings.EqualFold(k, "HANZO_TARGET_KEY") {
			continue
		}
		base[k] = v
	}
	out := make([]string, 0, len(base))
	for k, v := range base {
		out = append(out, k+"="+v)
	}
	return out
}

// cappedBuf keeps the first `max` bytes and counts the rest. A build that emits
// a gigabyte of warnings must not OOM the box reporting it, and silently
// truncating without saying so makes a caller debug output that was never sent.
type cappedBuf struct {
	bytes.Buffer
	max     int
	dropped int
}

func (c *cappedBuf) Write(p []byte) (int, error) {
	if room := c.max - c.Buffer.Len(); room > 0 {
		if len(p) <= room {
			return c.Buffer.Write(p)
		}
		_, _ = c.Buffer.Write(p[:room])
		c.dropped += len(p) - room
		return len(p), nil
	}
	c.dropped += len(p)
	return len(p), nil
}

func (c *cappedBuf) String() string {
	s := c.Buffer.String()
	if c.dropped > 0 {
		s += "\n… truncated, " + itoa(c.dropped) + " more bytes"
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}

type errBadReq string

func (e errBadReq) Error() string { return string(e) }
