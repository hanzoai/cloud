package coding

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"golang.org/x/sys/unix"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A shell starts a background job with SIGINT and SIGQUIT ignored, and a child
// inherits that, so the harness runs under `env --default-signal=INT,QUIT`: a
// server it starts and a test of graceful shutdown get the handling they would
// in CI, as when the harness ran in the foreground.
//
// The group is recorded only when the wrapper leads its own — an exec's process
// does, in gVisor (pid = pgid = sid) — and removed when the harness exits with a
// status killedBy does not read as a signal. One that did leaves the record for
// end. A harness that exits 129–192 of its own accord reads as killed: a shell
// reports the two the same way.
//
// An image whose tail cannot follow a pid, or that has no setpriv or no env that
// resets a signal, runs argv as it is.
func solo(group string, argv []string) []string {
	const sh = `g=$1; shift
tail --pid=$$ -n 0 /dev/null 2>/dev/null && command -v setpriv >/dev/null && env --default-signal=INT,QUIT true 2>/dev/null || exec "$@"
d=$g.d; mkdir -m 700 "$d" || exec "$@"
[ "$(ps -o pgid= -p $$)" -eq $$ ] && echo $$ >"$g"
: >"$d/o"; : >"$d/e"
exec 3<&0 </dev/null
setpriv --pdeathsig KILL -- env --default-signal=INT,QUIT "$@" <&3 >"$d/o" 2>"$d/e" 3<&- & h=$!
exec 3<&-
setpriv --pdeathsig KILL -- tail -s 0.2 -n +1 -f --pid=$h "$d/o" & a=$!
setpriv --pdeathsig KILL -- tail -s 0.2 -n +1 -f --pid=$h "$d/e" >&2 & b=$!
wait $h; c=$?
wait $a $b; rm -rf "$d"
[ $c -gt 128 ] && [ $c -le 192 ] || rm -f "$g"
exit $c`
	return append([]string{"sh", "-c", sh, "sh", group}, argv...)
}

// turn runs one harness turn: argv, which names no task, told words on stdin
// after the two credential lines keyed reads (argvFor). It gets the run's
// working time (working) and, when it did not end on its own — it died of a
// signal, ran out of time, or was cancelled — the process group its wrapper
// recorded is stopped before anything reads the tree (end).
func (in sandbox) turn(ctx context.Context, run *client.RunIn, argv []string, words string, ttl int) (*client.Ran, error) {
	group := turnPath()
	// Proactively refresh a nearing-expiry delegated token before starting a
	// subsequent turn so multi-turn check/settle loops and child processes start
	// with a fresh token in both HANZO_API_KEY and the credential file.
	in.cred.ensureFresh(ctx)
	// WHAT EVERY COMMAND IS TOLD TO KEEP OUT OF ITS STREAMS is read when it starts, not when the run
	// began: run is the one value every turn shares, and a credential renewed since the first
	// turn is what this one is fed. A secret left out here streams unredacted into the session.
	token, held := in.cred.snapshot()
	run.Blind = in.secretsOf(held)
	if in.cred.refreshes() {
		wrap := lodged
		if !in.cred.isLaid() {
			wrap = planted
		}
		run.Argv = wrap(in.cred.file, in.sourced(solo(group, refreshing(in.cred.file, argv))))
	} else {
		run.Argv = keyed(in.sourced(solo(group, argv)))
	}
	run.Stdin = fed(token, in.ticket) + words
	run.TimeoutSec = working(ctx, ttl)
	if err := in.lend(false); err != nil {
		return nil, err
	}
	if in.onTurnSteer != nil {
		in.onTurnSteer(func(text string) bool { return in.queueTurn(ctx, group, text) })
		defer in.onTurnSteer(nil)
	}
	stop := in.beating(ctx, group)
	ran, err := sandboxpeer.SandboxRun(ctx, run)
	stop()
	if err == nil && ran != nil && ran.ExitCode == 0 && in.answered != nil {
		*in.answered = true
	}
	if err == nil && ran != nil && in.cred.refreshes() &&
		(ran.ExitCode == 0 || !strings.Contains(ran.Stderr, "could not place the model credential")) {
		in.cred.lay() // the file is written; a later turn leaves a newer one in it alone
	}
	if ran != nil && in.knownThread != nil {
		if t := outcomeOf(ran.Stdout).thread; threadRE.MatchString(t) {
			*in.knownThread = t
		}
	}
	if err != nil || ran == nil || killedBy(ran.ExitCode) != "" {
		in.end(group)
	}
	return ran, err
}

const (
	queueWait          = 15 * time.Second
	threadStateFile    = ".git/hanzo-thread"
	threadSessionsFile = ".git/hanzo-dev-sessions.tar"
	threadStateWait    = 10 * time.Second
)

// queueTurn injects a steering message into an active `dev` turn via `dev queue`
// without interrupting the running process. Returns false when no thread is open
// yet or `dev queue` refuses, letting the caller fall back to stop-and-follow-up.
func (in sandbox) queueTurn(ctx context.Context, group, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || ctx.Err() != nil {
		return false
	}
	thread := ""
	if in.knownThread != nil {
		thread = *in.knownThread
	}
	if !threadRE.MatchString(thread) {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, err := runIn(readCtx, in.id, []string{"head", "-c", "65536", group + ".d/o"}, 5, "", "", in.secrets()...)
		cancel()
		if err == nil && out.ExitCode == 0 {
			thread = outcomeOf(out.Stdout).thread
			if threadRE.MatchString(thread) && in.knownThread != nil {
				*in.knownThread = thread
			}
		}
	}
	if !threadRE.MatchString(thread) {
		return false
	}
	qctx, cancel := context.WithTimeout(ctx, queueWait)
	defer cancel()
	const queueSh = `m=$(cat); exec dev queue --thread "$1" --message "$m"`
	ran, err := runIn(qctx, in.id, in.sourced([]string{"sh", "-c", queueSh, "sh", thread}), int(queueWait/time.Second), "", text, in.secrets()...)
	return err == nil && ran.ExitCode == 0
}

// loadThread restores persisted dev session rollouts and SQLite thread inventory
// (if the pod was parked and woken with a fresh $HOME) and returns the last dev
// thread UUID from .git only when a matching rollout file is actually present on
// disk, preventing `dev exec resume` from failing with a missing rollout/thread.
func (in sandbox) loadThread() string {
	const sh = `h="${DEV_HOME:-$HOME/.hanzo/dev}"
[ -f "$1" ] && [ ! -L "$1" ] || exit 0
t=$(head -c 64 "$1" | tr -d '[:space:]')
[ -n "$t" ] || exit 0
has_rollout() {
  [ -d "$h/sessions" ] && find "$h/sessions" -type f -name "*$t*.jsonl*" 2>/dev/null | head -n 1 | grep -q .
}
if ! has_rollout && [ -f "$2" ]; then
  mkdir -p "$h" && tar -xf "$2" -C "$h" 2>/dev/null || true
fi
has_rollout && echo "$t" || true`
	ctx, cancel := context.WithTimeout(in.ctx, threadStateWait)
	defer cancel()
	ran, err := runIn(ctx, in.id, []string{"sh", "-c", sh, "sh", threadStateFile, threadSessionsFile}, int(threadStateWait/time.Second), "", "", in.secrets()...)
	if err != nil || ran.ExitCode != 0 {
		return ""
	}
	t := strings.TrimSpace(ran.Stdout)
	if !threadRE.MatchString(t) {
		return ""
	}
	return t
}

// saveThread records the active dev thread UUID and archives $DEV_HOME/sessions
// alongside its SQLite state DB files inside .git on the workspace volume so
// follow-up runs resume the exact thread with an intact rollout inventory.
func (in sandbox) saveThread(thread string) {
	if !threadRE.MatchString(thread) {
		return
	}
	const sh = `[ -d .git ] || exit 0
printf '%s\n' "$1" >"$2"
out=$3
h="${DEV_HOME:-$HOME/.hanzo/dev}"
if [ -d "$h/sessions" ]; then
  set -- sessions
  for f in "$h"/state_*.sqlite*; do
    [ -f "$f" ] && set -- "$@" "$(basename "$f")"
  done
  tar -cf "$out" -C "$h" "$@" 2>/dev/null || true
fi`
	ctx, cancel := context.WithTimeout(context.WithoutCancel(in.ctx), threadStateWait)
	defer cancel()
	_, _ = runIn(ctx, in.id, []string{"sh", "-c", sh, "sh", thread, threadStateFile, threadSessionsFile}, int(threadStateWait/time.Second), "", "", in.secrets()...)
}

// turnPath is where one turn's wrapper records its process group: one file per
// turn, so a stop never reads the record of a turn that ended long ago and took
// down the server it left running.
func turnPath() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return "/tmp/hanzo-run/turn." + hex.EncodeToString(b)
}

// beatEvery is how often a harness turn in flight says what it is doing, and
// beatRead bounds reading it. A var so a test can make the beat short.
var beatEvery = time.Minute

const beatRead = 20 * time.Second

// beating tells in.beat, every beatEvery until stop is called, what the turn's
// harness is doing: the command it started and has not finished, read from its
// own output (solo writes it to group.d/o), or how long the turn has run. A turn
// is ONE exec that answers when it ends, so a `make test` that ran for twenty
// minutes was twenty minutes in which the run's progress said nothing and the
// run read as stalled (#553).
func (in sandbox) beating(ctx context.Context, group string) (stop func()) {
	if in.beat == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		began, seen := time.Now(), map[string]time.Time{}
		steeredRevert := false
		t := time.NewTicker(beatEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				_ = in.lend(false)
				line := "working, " + minutes(now.Sub(began)) + " into the agent's turn"
				read, end := context.WithTimeout(ctx, beatRead)
				ran, err := runIn(read, in.id, []string{"tail", "-c", "262144", group + ".d/o"}, int(beatRead/time.Second), "", "", in.secrets()...)
				end()
				if err == nil && ran.ExitCode == 0 {
					if id, cmd := underway(ran.Stdout); id != "" {
						if _, ok := seen[id]; !ok {
							seen[id] = now
						}
						line = "running `" + in.scrub(cmd) + "`"
						if took := now.Sub(seen[id]); took >= time.Minute {
							line += " for " + minutes(took)
						}
					}
					if n := revertCycleCount(ran.Stdout); n >= 5 {
						in.end(group)
						return
					} else if n >= 3 && !steeredRevert {
						steeredRevert = true
						in.queueTurn(ctx, group, "Stop reverting and rewriting the same file in a loop. Keep your cleanest working change, run targeted verification once, and finish the turn now — or state clearly what is blocking you.")
					}
				}
				if ctx.Err() == nil {
					in.beat(line)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

// revertCycleCount returns the maximum number of times an identical git revert/restore
// command (`git checkout -- ...` / `git restore ...`) completed in a turn's JSONL stream,
// catching rewrite-and-revert loops that evade adjacent-call duplicate detection.
func revertCycleCount(out string) int {
	counts := map[string]int{}
	best := 0
	for _, line := range strings.Split(out, "\n") {
		var e struct {
			Type string `json:"type"`
			Item struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &e) != nil || e.Type != "item.completed" || e.Item.Type != "command_execution" {
			continue
		}
		c := strings.TrimSpace(e.Item.Command)
		if inner, ok := strings.CutPrefix(c, "bash -lc "); ok {
			c = strings.TrimSpace(strings.Trim(inner, `"'`))
		}
		low := strings.ToLower(c)
		if !strings.Contains(low, "git checkout --") && !strings.Contains(low, "git checkout head --") && !strings.Contains(low, "git restore ") {
			continue
		}
		counts[c]++
		if counts[c] > best {
			best = counts[c]
		}
	}
	return best
}

// underway is the command a harness's JSON output says it started and has not
// finished, the latest of them: its item's id and its command line, the shell
// that runs it left out.
func underway(out string) (id, command string) {
	open := map[string]string{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		var e struct {
			Type string `json:"type"`
			Item struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &e) != nil || e.Item.Type != "command_execution" || e.Item.ID == "" {
			continue
		}
		switch e.Type {
		case "item.started":
			open[e.Item.ID] = e.Item.Command
			order = append(order, e.Item.ID)
		case "item.completed":
			delete(open, e.Item.ID)
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		if c, ok := open[order[i]]; ok {
			c = strings.TrimSpace(c)
			if inner, ok := strings.CutPrefix(c, "bash -lc "); ok {
				c = strings.Trim(inner, `"'`)
			}
			return order[i], c
		}
	}
	return "", ""
}

// minutes is a duration as a person reads it in a status line: whole minutes.
func minutes(d time.Duration) string { return strconv.Itoa(int(d/time.Minute)) + "m" }

// endWait bounds stopping what a turn left running.
const endWait = 30 * time.Second

// ended stops the process group recorded in $1 — TERM, a second, then KILL —
// and removes the record. No record is a turn that ended on its own. The wrapper
// dies on the TERM and takes the harness with it (pdeathsig); the second is for
// what the harness started.
//
// The record is in /tmp, where the model's commands can reach it, so only a
// regular file naming a group above 1 is acted on: written `1` it would have
// been `kill -1`, every process of the sandbox's user, and a FIFO in its place
// would have held this exec for its whole wait.
const ended = `[ -f "$1" ] && [ ! -L "$1" ] || exit 0
g=$(head -c 16 "$1"); rm -f "$1"
case $g in ''|*[!0-9]*) exit 0 ;; esac
[ "$g" -gt 1 ] || exit 0
kill -TERM -"$g" 2>/dev/null; sleep 1; kill -KILL -"$g" 2>/dev/null; true`

// end stops the process group of a turn that did not end on its own: the
// harness's children after it died, and the whole turn when it ran out of time or
// was cancelled. Closing an exec stops nothing in the pod — measured: a turn past
// its timeout kept its wrapper, harness, tails and children running — so a
// stopped run's harness would go on writing while its work was committed (keep).
// It runs on a context of its own, since the run's may be the one that ended.
func (in sandbox) end(group string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(in.ctx), endWait)
	defer cancel()
	_, _ = runIn(ctx, in.id, []string{"sh", "-c", ended, "sh", group}, int(endWait/time.Second), "", "")
}

// killedBy names the signal a harness died of, from the status a shell reports
// for it (128+n), or "" for a status the harness chose.
func killedBy(code int) string {
	if code <= 128 || code > 128+64 {
		return ""
	}
	return cmp.Or(unix.SignalName(syscall.Signal(code-128)), "signal "+strconv.Itoa(code-128))
}

// terminate marks a run failed because its harness was killed: a command it ran,
// the sandbox's memory, or a person stopped it mid-turn. It is told apart from a
// harness that exited with an error, and the run is not retried — a turn cut off
// mid-way may have half-done what it was doing.
func terminate(out *RunResult, tool string, code int) {
	out.OK, out.Terminated = false, killedBy(code)
	out.Error = fmt.Sprintf("the %s harness was killed by %s (exit %d) before it finished its turn", harness(tool), out.Terminated, code)
}

// sourced runs argv with the codebase's secrets exported, when it has any.
func (in sandbox) sourced(argv []string) []string {
	if len(in.env) == 0 {
		return argv
	}
	return append([]string{"sh", "-c", `. "$0" && exec "$@"`, envPath}, argv...)
}

// exports is the env file: one `export NAME='value'` per secret, each value
// single-quoted so no byte of it — a quote, a newline, a `$(`— is read as shell.
func exports(env map[string]string) string {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(env)) {
		b.WriteString("export " + name + "='" + strings.ReplaceAll(env[name], "'", `'\''`) + "'\n")
	}
	return b.String()
}

// values is the env's values, which is what the run blinds.
func values(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, name := range slices.Sorted(maps.Keys(env)) {
		out = append(out, env[name])
	}
	return out
}

// halt stops what the run's task left running in the pod, on a context of its
// own: the run's may be the one that ended.
func (in sandbox) halt() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(in.ctx), keepWait)
	defer cancel()
	_, _ = sandboxpeer.SandboxStop(ctx, &client.StopIn{ID: in.id})
}

// renewsFrom is the first dev release that takes `-c model_providers.hanzo.auth.*` as a merge
// into its provider table, so that a run can point its credential command at a file (refreshing).
// Measured against the published release binaries (linux, aarch64) with the endpoint on localhost:
// 0.6.94, 0.6.100 and 0.6.108 say "missing field name" and exit 1 before a model call;
// 0.6.115, 0.6.118, 0.6.119, 0.6.120, 0.6.122, 0.7.0, 0.7.9, 0.7.10 and 0.7.14 send the file's bearer
// with HANZO_API_KEY set and, answered 401 with the file replaced, ask the command again and send the
// new one. No release was published between 0.6.108 and 0.6.115.
var renewsFrom = [3]int{0, 6, 115}

var devVersion = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// atLeast reports whether version, as `dev --version` prints it, is at or above min. A version it
// cannot read is not.
func atLeast(version string, min [3]int) bool {
	m := devVersion.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	for i := range min {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return false
		}
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
}

// takesRenewal asks the sandbox's dev which version it is and answers whether it can be pointed at
// the run's credential file, and, when it cannot, why in a sentence. An answer that cannot be read
// (no dev, a failing exec, output that names no version) is a dev that cannot, and is said so.
func (in sandbox) takesRenewal() (bool, string) {
	ctx, cancel := context.WithTimeout(in.ctx, clientTimeout)
	defer cancel()
	ran, err := runIn(ctx, in.id, []string{"dev", "--version"}, int(clientTimeout/time.Second), "", "", in.secrets()...)
	if err != nil || ran.ExitCode != 0 {
		return false, "the version of dev in this sandbox could not be read"
	}
	out := strings.TrimSpace(ran.Stdout)
	if !devVersion.MatchString(out) {
		return false, "the version of dev in this sandbox could not be read"
	}
	if !atLeast(out, renewsFrom) {
		return false, fmt.Sprintf("dev in this sandbox (%s) is older than %d.%d.%d, the first that can be pointed at a renewed credential",
			firstLine(out), renewsFrom[0], renewsFrom[1], renewsFrom[2])
	}
	return true, ""
}

// spends reports a harness that makes model calls with the delegated credential. python and node
// run a program and make none of their own.
func spends(tool string) bool { return harness(tool) != "python" && harness(tool) != "node" }

// keepWait bounds keeping an interrupted run's work: stopping its task, a
// commit and a push.
const keepWait = 2 * time.Minute

// leaseMargin is how far short of a lease's expiry a run that cannot be carried
// stops: the keep (keepWait) and the seconds around it.
var leaseMargin = keepWait + 3*time.Minute

// working is how long the harness may work: the run's ttl, or less when the run's
// own deadline comes first, so that keepWait and a minute more are left after it
// to keep what it did (outOfTime). The run's deadline starts at admission, before
// the lease, the clone and the install, so a harness given the whole ttl outlived
// the run, and its work went with it.
func working(ctx context.Context, ttl int) int {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ttl
	}
	left := int((time.Until(deadline) - keepWait - time.Minute) / time.Second)
	if left > 0 && left < ttl {
		return left
	}
	return ttl
}

// outOfTime reports a harness call that ended because time ran out: the
// sandbox's bound on the command (working), or the run's own deadline. Over the
// wire the sandbox's says only "context deadline exceeded".
func outOfTime(ctx context.Context, err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "deadline exceeded")
}

const (
	transientRetryCap     = time.Minute
	transientRetryDefault = 5 * time.Second
)

// waitForRetry pauses for d or until ctx is done; tests swap it to avoid wall-clock waits.
var waitForRetry = func(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}
