package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	wire "github.com/hanzoai/git/modules/actions/runner"
	"github.com/zap-proto/zip"
)

// runner.go is the runner daemon's half of this plugin: five typed operations
// under /v1/runner, which a runner reaches over HTTP as POST /v1/runner/<name>
// and over ZAP by asking for post_runner_<name> on the call plane. zip derives
// the second face from the first, so the address and the operation are one
// registration.
//
// It sits BESIDE /v1/git rather than inside it, and that is a boundary and not a
// spelling. /v1/git is the public control surface a person or an agent calls,
// gated by the org a request carries. This is machine plumbing: a runner holds
// its own credential, meets none of the principal middleware, and is not a
// tenant of anything — it is capacity that has entered one declared pool. Naming
// it under /v1/git would put a daemon's poll loop behind a gate designed for a
// person's session, which is the mistake, not the tidiness.
//
// NOTHING HERE BUILDS ANYTHING. The build endpoint is platform's
// (POST /v1/platform/runner): a caller says "build this image for me" and the
// cluster does it. These five say the opposite thing — "I am capacity, give me
// work" — and share no code, no store and no address with it.
//
// The values on the wire come from github.com/hanzoai/git/modules/actions/runner,
// the protocol's own module: a stdlib-only leaf that the runner daemon already
// imports. One definition, so a server copy and a client copy cannot drift.

// RunnerRoute is where the operations are addressed. One constant, and the op
// names both ends use are derived from it.
const RunnerRoute = "/v1/runner"

// cancelling is the capability a runner advertises when it understands the
// transitional cancelling state and runs post-step cleanup before finalizing.
const cancelling = "cancelling"

// summary is the capability this side advertises: a job may publish a summary.
const summary = "job-summary"

// serverSays is what this forge tells a runner it understands.
func serverSays() []string { return []string{summary} }

// Persist thresholds. A fleet polling every few seconds would otherwise write
// the runner row on every call, so liveness is written at most this often and
// "currently executing" at most this often while logs stream.
const (
	onlineEvery = 30 * time.Second
	activeEvery = 5 * time.Second
)

// runnerOps builds the five operations as their OWN app and mounts it. It takes
// the same service every other part of this plugin holds, because a runner's
// work is this plugin's runs and its tasks.
//
// An app of its own, rather than five registrations on the host, for two
// reasons that are the same reason. It carries no middleware from /v1/git,
// which is the boundary this file is about. And zip stamps a mounted op with
// the app that DECLARED it, which qualifies the type names it reaches: the
// protocol calls its values State, Task, Line, Pair and Context — the words
// half the fleet also uses — and unqualified they would collide in the composed
// document with whichever app published the name first. Mounted, they are the
// runner's, and they say so.
//
// The app carries its own root, so it is mounted onto the host unchanged: what
// it declares is the address a request arrives at, and no prefix is added by the
// mounting. The root is written once, on the group.
func runnerOps(app cloud.Router, s *cloud.Service[state]) error {
	d := daemon{s: s}
	a := zip.New(zip.Config{AppName: "runner", DisableStartupMessage: true})
	g := a.Group(RunnerRoute)
	zip.Post(g, "/register", d.register)
	zip.Post(g, "/declare", d.declare)
	zip.Post(g, "/task", d.task)
	zip.Post(g, "/state", d.state)
	zip.Post(g, "/log", d.log)
	if err := a.Build(); err != nil {
		return fmt.Errorf("git runner: build operations: %w", err)
	}
	// Onto the concrete router, which is where every typed op in this plugin
	// lands anyway: the composition scope delegates route registration straight
	// to it and gates MIDDLEWARE, and this app installs none. Mounting through
	// the scope would repeat the app under each prefix git owns — the shape for
	// a child meant to appear beneath every one of them, which this is not.
	cloud.ZipApp(app).Use(a)
	return nil
}

// daemon binds the service to the five handlers. It carries state and no logic,
// exactly as ops does for the control plane.
type daemon struct{ s *cloud.Service[state] }

// unknown is the one answer to a caller this plugin cannot place, whether its
// handle is unknown or its token does not match: telling the two apart would let
// anyone probe which handles exist.
func unknown() error {
	return &zip.HTTPError{Status: http.StatusUnauthorized, Code: "unregistered", Msg: "unregistered runner"}
}

// who resolves the runner behind a call from the credential it carries, and
// records that it called. It is the first line of every operation but register,
// and a return value rather than middleware: a handler cannot forget to hold the
// result, and there is no path that reaches a handler with no runner behind it.
//
// working says whether reaching the operation means the runner is executing a
// job; both that and plain liveness ride the same write.
func (d daemon) who(ctx context.Context, c wire.Credential, working bool) (Runner, *Store, error) {
	if c.UUID == "" || c.Token == "" {
		// Refused before the disk is asked anything: a caller with no credential
		// at all should not cost a read.
		return Runner{}, nil, unknown()
	}
	org := orgOf(c.UUID)
	if org == "" {
		return Runner{}, nil, unknown()
	}
	st, err := storeFor(d.s, org)
	if err != nil {
		return Runner{}, nil, unknown()
	}
	r, err := st.Runner(ctx, c.UUID, c.Token)
	if errors.Is(err, errNoRunner) {
		return Runner{}, nil, unknown()
	}
	if err != nil {
		return Runner{}, nil, internalErr(err)
	}
	now := time.Now()
	stale := now.Sub(time.Unix(r.LastOnline, 0)) >= onlineEvery
	active := working && now.Sub(time.Unix(r.LastActive, 0)) >= activeEvery
	if stale || active {
		if err := st.Seen(ctx, r.UUID, active, now); err != nil {
			d.s.Log.Warn("git runner: record call", "runner", r.Name, "err", err)
		}
	}
	return r, st, nil
}

// identity is what a runner is told about itself.
func identity(r Runner, token string) wire.Identity {
	return wire.Identity{
		ID:        r.CreatedAt,
		UUID:      r.UUID,
		Token:     token,
		Name:      r.Name,
		Version:   r.Version,
		Labels:    r.Labels,
		Ephemeral: r.Ephemeral,
	}
}

// register trades a pool's join secret for a runner identity and the token that
// authenticates every later call. It is the one operation with no credential to
// check, because a runner has none until this answers.
//
// The secret names the pool it opens, and a pool exists only because somebody
// declared it. A daemon that starts against capacity nobody declared is refused
// here, which is where the rule that pools are declared state actually holds.
func (d daemon) register(ctx context.Context, in *wire.RegisterIn) (*wire.RegisterOut, error) {
	if in.Token == "" || in.Name == "" {
		return nil, zip.ErrBadRequest("missing runner token or name")
	}
	org, pool, clear, ok := split(in.Token)
	if !ok {
		return nil, zip.ErrUnauthorized("runner registration token not found")
	}
	st, err := storeFor(d.s, org)
	if err != nil {
		return nil, zip.ErrUnauthorized("runner registration token not found")
	}
	r, token, err := st.Enter(ctx, org, pool, clear, Runner{
		Name:       ellipsis(in.Name, 255),
		Version:    in.Version,
		Labels:     in.Labels,
		Ephemeral:  in.Ephemeral,
		Cancelling: slices.Contains(in.Capabilities, cancelling),
	})
	if errors.Is(err, errNoPool) {
		return nil, zip.ErrUnauthorized("runner registration token not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	d.s.Log.Info("git runner: registered", "org", org, "pool", pool, "runner", r.Name)
	return &wire.RegisterOut{Runner: identity(r, token)}, nil
}

// declare republishes what a registered runner can do, and answers with what
// this side understands, so the two learn about each other from one exchange.
func (d daemon) declare(ctx context.Context, in *wire.DeclareIn) (*wire.DeclareOut, error) {
	r, st, err := d.who(ctx, in.Credential, false)
	if err != nil {
		return nil, err
	}
	can := slices.Contains(in.Capabilities, cancelling)
	if err := st.Redeclare(ctx, r.UUID, in.Version, in.Labels, can); err != nil {
		return nil, internalErr(err)
	}
	r.Version, r.Labels, r.Cancelling = in.Version, in.Labels, can
	return &wire.DeclareOut{Runner: identity(r, ""), Capabilities: serverSays()}, nil
}

// task hands the runner a job to execute, if its pool has one, and answers
// immediately either way. A runner sends the queue version it last saw; when it
// matches, nothing has been queued since and no lease transaction is opened.
func (d daemon) task(ctx context.Context, in *wire.TaskIn) (*wire.TaskOut, error) {
	r, st, err := d.who(ctx, in.Credential, false)
	if err != nil {
		return nil, err
	}
	latest, err := st.Queue(ctx, r.Org)
	if err != nil {
		return nil, internalErr(err)
	}
	if latest == 0 {
		if err := st.bump(ctx, r.Org); err != nil {
			return nil, internalErr(err)
		}
		// Answering zero would tell the runner this side is too old to version
		// its queue at all.
		latest++
	}
	if in.Queue == latest {
		return &wire.TaskOut{Queue: latest}, nil
	}
	t, run, leased, err := st.Lease(ctx, r)
	if err != nil {
		return nil, internalErr(err)
	}
	if !leased {
		return &wire.TaskOut{Queue: latest}, nil
	}
	d.s.Log.Info("git runner: task leased", "org", r.Org, "pool", r.Pool,
		"runner", r.Name, "task", t.ID, "run", run.ID, "job", t.Job)
	return &wire.TaskOut{Task: d.handed(t, run), Queue: latest}, nil
}

// handed is the task as the runner receives it: the workflow document, and the
// context act evaluates github.* against.
func (d daemon) handed(t Task, run Run) *wire.Task {
	host := origin(d.s)
	return &wire.Task{
		ID:       t.ID,
		Workflow: run.Doc,
		Context: wire.Context{
			Event:           event(run),
			EventName:       run.Event,
			Job:             t.Job,
			RunID:           run.ID,
			RunNumber:       fmt.Sprint(run.Number),
			RunAttempt:      "1",
			Actor:           run.Actor,
			Repository:      run.Org + "/" + run.Repo,
			RepositoryOwner: run.Org,
			Ref:             run.Ref,
			RefName:         strings.TrimPrefix(run.Ref, "refs/heads/"),
			RefType:         "branch",
			Sha:             run.Commit,
			ServerURL:       host,
			APIURL:          host + "/v1/git",
			RetentionDays:   "90",
			ActionsURL:      host,
		},
	}
}

// event is the payload a workflow evaluates github.event.* against. It carries
// what this plugin knows about the push and nothing invented: a field nobody can
// compute is a field a workflow would read as true.
func event(run Run) []byte {
	b, err := json.Marshal(map[string]any{
		"ref":    run.Ref,
		"after":  run.Commit,
		"pusher": map[string]string{"name": run.Actor},
		"repository": map[string]any{
			"name":      run.Repo,
			"full_name": run.Org + "/" + run.Repo,
			"owner":     map[string]string{"name": run.Org, "login": run.Org},
		},
	})
	if err != nil {
		return []byte("{}")
	}
	return b
}

// state records a task's progress and that of its steps, and answers with the
// result this side now holds — which is how a runner learns its task was stopped
// from somewhere else.
func (d daemon) state(ctx context.Context, in *wire.StateIn) (*wire.StateOut, error) {
	r, st, err := d.who(ctx, in.Credential, true)
	if err != nil {
		return nil, err
	}
	steps, merr := json.Marshal(in.State.Steps)
	if merr != nil {
		steps = nil
	}
	t, err := st.Report(ctx, in.State.ID, status(in.State.Result), in.State.Stopped, string(steps))
	if errors.Is(err, errNoTask) {
		return nil, zip.ErrNotFound("task not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	if t.Runner != r.UUID {
		return nil, zip.ErrForbidden("invalid runner for task")
	}
	for _, out := range in.Outputs {
		if len(out.Name) > 255 {
			d.s.Log.Warn("git runner: output key too long", "task", t.ID, "name", out.Name)
			continue
		}
		if len(out.Value) > 1<<20 {
			d.s.Log.Warn("git runner: output too long", "task", t.ID, "name", out.Name, "bytes", len(out.Value))
			continue
		}
		// Not fatal: a runner resends an output it has had no acknowledgement for.
		if err := st.Record(ctx, t.ID, out.Name, out.Value); err != nil {
			d.s.Log.Warn("git runner: record output", "task", t.ID, "name", out.Name, "err", err)
		}
	}
	stored, err := st.Recorded(ctx, t.ID)
	if err != nil {
		d.s.Log.Warn("git runner: read outputs", "task", t.ID, "err", err)
	}
	return &wire.StateOut{
		State:  wire.State{ID: in.State.ID, Result: result(t.Status)},
		Stored: stored,
	}, nil
}

// status maps a wire result onto a stored one. Pending is a task still going, so
// it is stored as running rather than as an outcome nobody reported.
func status(r wire.Result) string {
	if r == wire.Pending {
		return Running
	}
	return string(r)
}

// result maps a stored status back onto the wire. Anything not final is Pending,
// which is what the wire's empty Result means.
func result(s string) wire.Result {
	if done(s) {
		return wire.Result(s)
	}
	return wire.Pending
}

// log adds console output to a task's log and answers with how far that log is
// durable, so the runner knows where to resend from.
func (d daemon) log(ctx context.Context, in *wire.LogIn) (*wire.LogOut, error) {
	r, st, err := d.who(ctx, in.Credential, true)
	if err != nil {
		return nil, err
	}
	t, err := st.Task(ctx, in.Task)
	if errors.Is(err, errNoTask) {
		return nil, zip.ErrNotFound("task not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	if t.Runner != r.UUID {
		return nil, zip.ErrForbidden("invalid runner for task")
	}
	ack := t.LogLength

	// Drop lines already acknowledged, keeping only what is new.
	var lines []wire.Line
	if in.Index <= ack && int64(len(in.Lines))+in.Index > ack {
		lines = in.Lines[ack-in.Index:]
	}

	// Acknowledge a resent seal idempotently. Appending past the seal is an error.
	if t.LogSealed {
		if len(lines) > 0 {
			return nil, zip.ErrConflict("log file has been archived")
		}
		return &wire.LogOut{Ack: ack}, nil
	}

	// Nothing to do unless there are new lines or a seal to apply. Even with a
	// seal, stop when the runner has outrun this side: sealing a log with a gap
	// in it is worse than asking the runner to retry.
	if len(lines) == 0 && (!in.Last || in.Index > ack) {
		return &wire.LogOut{Ack: ack}, nil
	}

	n, err := d.append(r.Org, t.ID, lines)
	if err != nil {
		return nil, internalErr(err)
	}
	if err := st.Wrote(ctx, t.ID, int64(len(lines)), n, in.Last); err != nil {
		return nil, internalErr(err)
	}
	return &wire.LogOut{Ack: t.LogLength + int64(len(lines))}, nil
}

// append writes lines to a task's log and reports the bytes written. One record
// per line as JSON, so a line carrying anything at all — a newline, a control
// byte, invalid UTF-8 — reads back as the line it was.
//
// Called even with no lines: at offset 0 it creates the empty file a task that
// printed nothing is still read from.
func (d daemon) append(org string, task int64, lines []wire.Line) (int64, error) {
	name, err := logPath(d.s, org, task)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open log: %w", err)
	}
	defer f.Close()
	var n int64
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return n, fmt.Errorf("write log: %w", err)
		}
		n += int64(len(l.Content)) + 1
	}
	return n, f.Sync()
}

// logPath is where one task's console output lives: under the git data root,
// partitioned by org like every other thing this plugin keeps on disk.
func logPath(s *cloud.Service[state], org string, task int64) (string, error) {
	if strings.ContainsAny(org, `/\.`) || org == "" {
		return "", fmt.Errorf("git runner: %q is not an org", org)
	}
	dir := filepath.Join(s.State.dataDir, "git", "logs", org)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("git runner: log dir: %w", err)
	}
	return filepath.Join(dir, fmt.Sprint(task)+".jsonl"), nil
}

// ellipsis bounds a name a caller supplies.
func ellipsis(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
