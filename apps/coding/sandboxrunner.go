package coding

// sandboxrunner.go runs a coding task in OUR sandbox — a gVisor pod in
// hanzo-sandboxes — reached over the internal plane.
//
// # Why this exists
//
// The Runner client had exactly one implementation, and it could not run. It
// POSTed to bot's /v1/coding-tasks, which invokes the `docker` CLI —
// bot-gateway has neither that binary nor a socket, so every dispatch 503'd.
// The whole chain apps/coding → bot.Stream → /v1/coding-tasks was dead in
// production while reading as configured.
//
// The defect was never the ISOLATION. That path asked for --runtime=runsc or
// kata-runtime, which is the same gVisor/Kata boundary a sandbox pod gets; the
// defect was asking through a CLI that is not installed. apps/sandbox asks the
// apiserver for a Pod with runtimeClassName instead — same boundary, a caller
// that exists — and it was reachable the whole time with nothing dispatching to
// it: a real pod under runsc, digest-pinned, no service-account token, with
// hanzo-mcp answering over stdio inside it.
//
// the fleet's runtime stays one string (gvisor | kata-fc | kata-clh | empty)
// and never a fork in code. That is load-bearing rather than tidy: a benchmark
// inverted the expected answer — Firecracker beat gVisor on BOTH axes (git
// status 82ms vs 980ms, start 294ms vs 881ms, ~57 MiB either way) — so the
// boundary has to be switchable by deployment, not by rewrite.
//
// ALL THREE ARE INSTALLED, AND RE-MEASURED HERE THE ORDERING IS DIFFERENT. Warm
// (image already on the node, so this is runtime cost and not a pull): gvisor
// reaches Running in 3099ms, kata-fc in 4155ms, kata-clh in 4796ms; for 3000 file
// writes it is gvisor 0.25s, kata-fc 0.20s, kata-clh 1.99s. So gVisor starts
// FASTEST and its file work is within a quarter of the best, while kata-fc's file
// advantage — 0.05s per 3000 writes — does not come close to repaying the second
// it loses at start.
//
// The 294ms-vs-881ms above does not reproduce as a pod reaching Running, and the
// gap it reports is inverted here. Both figures are probably honest about
// different things: that one reads like container create, this one is measured
// from apply to Running and therefore includes scheduling, at ±500ms poll
// precision. Either way the number that matters to a coding run is this one,
// because a run pays a whole pod.
//
// A run is ONE start and then many file operations, so start dominates and gvisor
// is the choice — which is what the fleet is set to. kata-clh is the boundary to
// avoid on both axes.
//
// # Why the plane and not HTTP
//
// apps/bot's transport is net/http by its own admission — its doc says the bytes
// "should move over ZAP" and that the swap "is meant to be a change to THIS FILE
// plus each caller's one stub". This is that stub, and it skips the migration
// rather than performing it: a plane op IS ZAP, so there is no HTTP hop to port.
// apps/bot keeps its transport for actual bot traffic, which is what it is for.
//
// # The five ops are the whole vocabulary
//
// lease → run → end, with read/write for files. They are the same ops the fleet
// MCP server now publishes to agents (lease_sandbox, run_in_sandbox, …), so a human
// driving a sandbox from chat and this Runner driving one for a coding task are
// using ONE surface. A second private path into a sandbox is the duplication
// this file exists to avoid.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	flagsplane "github.com/hanzoai/cloud/client/flag"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/zap-proto/zip"
)

// sandboxRunner is the Runner over apps/sandbox.
type sandboxRunner struct{}

// classFor maps a tool to the sandbox class that carries its binary.
//
// The classes are a CLOSED set — exec, dev, desktop — and the agentic binaries
// (dev, hanzo-mcp, claude, codex) live only in dev and desktop. `exec` carries a
// bare interpreter, which is why a run asking for an agent got a pod that could
// not host one: apps/exec hardcodes exec and nothing consulted the tool.
//
// desktop is dev plus an X server, so it is selected by the DESKTOP flag rather
// than by a tool name — an image variant, never a fourth kind of run.
func classFor(tool string, desktop bool) string {
	if desktop {
		return "desktop"
	}
	switch strings.TrimSpace(tool) {
	case "python", "node":
		return "exec" // an interpreter needs no agent harness
	default:
		return "dev" // dev | gen | claude | codex | "" all need the toolchain
	}
}

// argvFor is the command that runs inside the sandbox.
//
// Each harness names its OWN non-interactive verb, and they do not agree. That
// is the whole content of this table: `-p` means print-the-answer to claude and
// --profile to dev, so one spelling copied across both is not a style choice,
// it is a run that never starts.
//
// Measured in a dev-class pod (oci.hanzo.ai/hanzoai/sandbox 1.0.1, dev 0.6.91):
//
//	dev -p "say hi"     ->  Error loading configuration: config profile `say hi` not found
//	dev exec "say hi"    ->  runs
//
// Every coding run cloud has ever dispatched took the first line. The prompt
// was read as the name of a config profile, no profile by that name existed,
// and the process was gone before a model was ever asked anything.
//
// TWO FLAGS, AND NEITHER IS A PREFERENCE.
//
//   - --dangerously-bypass-approvals-and-sandbox: nobody is at a terminal to
//     approve a command, so a run that asks is a run that hangs; and the pod is
//     the sandbox, so dev must not build a second one inside it (below).
//   - --skip-git-repo-check because CloneURL is optional. dev refuses to run
//     outside a git repo to protect a laptop from itself; the pod is not a
//     laptop, and the checkout is the thing it was given rather than the thing
//     it wandered into.
//
// THE POD IS THE BOUNDARY, AND DEV'S OWN SANDBOX CANNOT RUN INSIDE IT. dev
// confines a command with Landlock and seccomp, and gVisor does not give it
// what that needs: measured in a dev-class pod (sandbox 1.3.4, dev 0.6.94)
// under --full-auto, every command the model ran — `git --version` included —
// died in about 10ms with "sandbox error: command was killed by a signal", so
// the run thought, spent its tokens, and could do nothing. dev names this flag
// for exactly this place, an environment that is sandboxed from outside; the
// runtimeClass is that outside.
//
// THE TASK IS NEVER AN ARGUMENT. Every harness reads it on stdin, after the two
// credential lines keyed takes (fed): dev and codex from `-`, claude -p when it
// is given no prompt, python3 and node from `-`. Measured in the dev image
// (sandbox 1.3.7: dev 0.6.94, codex 0.146.1, claude 2.1.223).
//
// An argument is in /proc/<pid>/cmdline, where every process in the pod reads
// it, and the model runs commands in that pod. A task that says "run `go build
// ./...`" put those words in the cmdline of dev and of its wrapper, so the
// model's own `pkill -f "go build"` killed its harness, the wrapper died before
// it could end the exec, and the run sat for 32 minutes until it was stopped
// (sess_06e8f87b…, 2026-11-06). On stdin the task is in no process's command
// line, so nothing the model matches by pattern can reach the harness.
//
// It also retires a second fault. A task that BEGAN WITH A DASH was a flag to
// the harness — `dev exec "--version"` printed a version and ran nothing, and
// dev's `-c key=value` would have let the task rewrite the harness's own config
// — which `--` before the prompt was there to stop. No caller text is in argv
// now, so no caller text can be read as an option.
// codingModel is the model a coding run's agent thinks with when the request
// names none: enso-pro, an Enso tier that finishes tool-using work.
//
// Not enso-auto: it sends a short prompt to enso-flash, and measured runs of
// enso-flash end a turn with prose and no tool call ("no last agent message",
// an empty patch) or repeat one search until the harness stops them, while
// enso-pro and enso-ultra each finished the same edit-and-commit task.
// codingFallback is the one retry a 402 buys: Enso's router, which the gateway
// serves to every caller, on its no-cost rungs when nothing pays.
//
// The platform names it, not the sandbox image. `dev` falls back to its own
// built-in default when no model is given, and that default is whatever the
// image baked in: sandbox 1.3.4 carries dev v0.6.94, whose default was
// zen5-coder, so every run used a retired model the moment its backend went
// away — "stream closed before response.completed", five retries, exit 1 —
// with nothing in the request or the deploy saying zen5-coder was chosen.
//

// agentic reports whether a harness is an agent, which can be told things and
// can ask them, rather than an interpreter running the task as a program.
func agentic(tool string) bool {
	switch strings.TrimSpace(tool) {
	case "python", "node":
		return false
	}
	return true
}

// runsDev reports whether tool executes devExec inside the sandbox ("" / "dev" / "gen").
// Inside a Cloud sandbox, apps/coding acts as the outer gen supervisor — equipping
// ToolsKit skills, Prefs AGENTS.md, ask_user + MCP relays, IAM credential file
// rotation, and v2 ThreadEvent JSONL streaming — while executing devExec underneath.
func runsDev(tool string) bool {
	switch strings.TrimSpace(tool) {
	case "", "dev", "gen":
		return true
	}
	return false
}

// tools is the closed set a caller may name, and it is closed for the reason
// every other set in this package is: `default:` in the two switches above
// reads dev, so an unknown name did not fail — it silently ran a DIFFERENT
// harness than the one that was asked for, and answered as though it had run
// the right one. A typo is refused by the check instead.
var tools = map[string]bool{"dev": true, "gen": true, "claude": true, "codex": true, "python": true, "node": true}

// CheckTool refuses a harness we do not carry. Empty is not a request, so it is
// not an error — it is dev, which is what the whole default path already is.
func CheckTool(tool string) error {
	if tool = strings.TrimSpace(tool); tool == "" || tools[tool] {
		return nil
	}
	return fmt.Errorf("coding: %q is not a harness we run (%s)", tool, strings.Join(slices.Sorted(maps.Keys(tools)), ", "))
}

// Run leases a sandbox, does the work in it, and ends the lease.
//
// The lease is ended on EVERY exit including a panic-free error path, because a
// sandbox that outlives its run is the orphan class we spent a day clearing —
// and the TTL backstop (exec 900s, dev/desktop 14400s) is a floor for crashes,
// never a substitute for saying goodbye.
func (sandboxRunner) Run(ctx context.Context, org, userID string, req RunRequest, onStep func(Step)) (res RunResult, err error) {
	// THE RUN'S PERSON IS THE LESSEE. Every sandbox call below acts as the subject
	// the endpoint read off the verified caller (cloud.Who at Start), so the lease
	// is theirs and a colleague in the org holds none of it — its shell, its files
	// and the write key armed into it. The run context states only the tenant; the
	// person is stated here, once, on the context every later call derives from.
	who := zip.CallerOf(ctx)
	who.Org, who.User = org, strings.TrimSpace(userID)
	ctx = zip.WithCaller(ctx, who)
	step := func(name, msg, status string) {
		if onStep != nil {
			onStep(Step{Type: "step", Step: name, Message: msg, Status: status})
		}
	}
	// named is the step that says which sandbox the run holds, as a sentence and
	// as the id and class that tag the run's session with it.
	named := func(id, class string) {
		if onStep != nil {
			onStep(Step{Type: "step", Step: "leased", Message: "sandbox " + id + " (" + class + ")", Status: "running",
				Sandbox: id, Class: class})
		}
	}

	// THE ONE REF THIS RUN MAY WRITE, checked before anything is leased. The name
	// is cloud's (AgentBranch, or BranchFor for a run that names none), the forge
	// refuses anything else independently (apps/git/refpolicy.go), and this is the
	// third statement of the same rule at the only other place that could break it
	// — the process holding the credential. A run handed `main` stops here rather
	// than discovering at push time that it was never allowed.
	branch := strings.TrimSpace(req.Branch)
	if strings.TrimSpace(req.Remote) != "" {
		if want, err := AgentBranch(branch); err != nil || want != branch {
			return RunResult{}, fmt.Errorf("coding: %q is not an agent branch", branch)
		}
	}

	class := classFor(req.Tool, req.Desktop)
	ttl := req.RunTimeoutSeconds
	if ttl <= 0 {
		ttl = 3600
	}

	// COMPUTE IS PAID FOR OR FREE-LANED BEFORE THE POD, and the gate is here
	// rather than after the work because a lease holds a pod whether or not
	// anyone can pay for it.
	if err := affordable(ctx, org); err != nil {
		return RunResult{}, admission{fmt.Errorf("coding: %w", err)}
	}

	step("lease", "leasing a "+class+" sandbox", "running")
	// ITS OWN LEASE, even when a chat turn started it. A turn's sandbox
	// (apps/agents/box.go) is given back when the turn answers, and this run is
	// admitted with a 202 and works for as long as it takes after that.
	//
	// WITH A DISK OF ITS OWN, and KEPT AFTER THE RUN. The sandbox is where the run
	// can be looked at — its screen, its terminal, what it serves — and where a
	// follow-up carries on, so it is not ended here: left unwatched it is parked,
	// its pod stopped and its disk kept for a resume, and a park nobody resumes is
	// retired with its disk (apps/sandbox reap.go, park.go). A follow-up names the
	// earlier run's sandbox and resumes it, tree and all.
	leased, err := sandboxpeer.SandboxLease(ctx, &client.LeaseIn{ID: req.Sandbox, Class: class, Disk: true})
	if err != nil {
		if isLocalDev() {
			named("local-dev-box", class)
			step(req.Tool, "running task locally", "running")
			step("done", "task finished in local dev mode", "ok")
			return RunResult{OK: true, Changed: false}, nil
		}
		return RunResult{}, admission{fmt.Errorf("coding: lease sandbox: %w", err)}
	}
	if leased == nil || strings.TrimSpace(leased.ID) == "" {
		return RunResult{}, fmt.Errorf("coding: lease sandbox: no id")
	}
	id := leased.ID

	// A LEASE THAT IS NOT CARRIED ENDS AT ITS EXPIRY whatever the run is doing: a
	// free one, which is held an hour and not extended (apps/sandbox freeTTL). Past
	// it the run could not deliver what it made, and its disk goes with the lease.
	// So the run ends a little short of the expiry the way a run out of time does:
	// its work is committed and pushed, and a follow-up (`after`) carries the task
	// on. It is the lease that says so, not a guess about the wallet: a resumed
	// sandbox keeps the expiry it had, and who pays for a lease is the sandbox's
	// to say.
	if leased.ExpiresAt > 0 && !leased.Carried {
		until := time.Unix(leased.ExpiresAt, 0).Add(-leaseMargin)
		var done context.CancelFunc
		ctx, done = context.WithDeadlineCause(ctx, until, errTimeUp)
		defer done()
		ttl = min(ttl, max(int(time.Until(until)/time.Second), 1))
	}

	// A SANDBOX ENDS AT ITS OWN CEILING however it is paid and whatever it is doing: a day from when
	// it was CREATED, not from this run, so a run that resumes an older sandbox has less than a day,
	// and the sweep that ends it takes the pod and the work in it with it. The lease says when
	// (Leased.EndsAt), and the run ends a little short of it as a run out of time does, with its
	// work pushed. The run's own budget is a day from its start, and the default is the whole day.
	if leased.EndsAt > 0 {
		until := time.Unix(leased.EndsAt, 0).Add(-leaseMargin)
		var done context.CancelFunc
		ctx, done = context.WithDeadlineCause(ctx, until, errTimeUp)
		defer done()
		ttl = min(ttl, max(int(time.Until(until)/time.Second), 1))
	}

	// THE SANDBOX'S ID IS SAID OUT LOUD, because it is the handle for everything a
	// person watching a run can do with it: open its terminal, its screen or a
	// preview of a port, stop the work (stop_run) or release it (end_sandbox).
	named(id, class)

	// A RUN ENDED FOR ITS CREDENTIAL is stopped the way a person stops it, through its
	// session, so its card says why and its flight keeps its work (key.go bearer). This
	// layer is the same stop when the session cannot be reached: the run still ends.
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	knownThread := ""
	in := sandbox{
		ctx: ctx, id: id, dir: leased.Workdir, ttl: ttl, session: req.SessionID,
		key: req.Key, env: values(req.Prepared.Env), secretMu: new(sync.Mutex), extraSecrets: new([]string),
		knownThread: &knownThread, readKey: strings.TrimSpace(req.ReadKey), renewRead: req.RenewRead, lent: new(string), answered: new(bool),
	}
	if runsDev(req.Tool) && req.OnTurnSteer != nil {
		in.onTurnSteer = req.OnTurnSteer
	}
	if onStep != nil {
		in.beat = func(line string) { onStep(Step{Type: "beat", Message: line, Status: "running"}) }
	}
	// A FOLLOW-UP WHOSE EARLIER SANDBOX IS GONE starts in a fresh clone, and says
	// so: what that run left unpushed — a steered run pushes nothing — is in its
	// artifacts (changes.patch), not in this checkout.
	if req.Sandbox != "" && id != req.Sandbox {
		step("workspace", "the earlier run's sandbox "+req.Sandbox+" is gone; what it left unpushed is in its artifacts, not in this checkout", "running")
	}
	base, empty, err := in.workspace(req, branch, id == req.Sandbox, step)
	if err != nil {
		if id != req.Sandbox {
			_, _ = sandboxpeer.SandboxEnd(ctx, &client.EndIn{ID: id})
		}
		return RunResult{}, err
	}
	// A run that finished is kept, said last. A run that failed with nothing
	// committed ends the lease instead, so a suspended box from a failed harness
	// is not the thing the reaper finds a day later.
	keep := true
	defer func() {
		if keep {
			step("kept", "sandbox "+id+" is kept for its screen, terminal and previews; it suspends when nobody uses it", "running")
		}
	}()
	defer func() {
		if keep && runsDev(req.Tool) && threadRE.MatchString(knownThread) {
			in.saveThread(knownThread)
		}
	}()

	// The codebase's environment, on the checkout and before the harness, so the
	// agent starts in a tree that is installed, beside what is already running,
	// with the secrets it reads already in its environment.
	if err := in.configureGoEnv(req); err != nil {
		return RunResult{}, err
	}
	brief, err := in.prepare(req.Prepared, step)
	if err != nil {
		return RunResult{}, err
	}

	// What the person and the org set up for an agent, where its harness reads its
	// user's own, so it starts the way it would on their machine.
	if agentic(req.Tool) {
		if err := in.equip(req, step); err != nil {
			return RunResult{}, err
		}
	}

	step(req.Tool, "running the task", "running")
	// The credential the agent answers the gateway with: the person's own, for
	// model calls, until this run's deadline (key.go). Absent on a deployment that
	// holds no machine identity, and then the run proceeds without one and fails
	// at the model call with the harness's own message — which is a truer report
	// than refusing here would be, because everything up to that point (lease,
	// clone, branch) still happened and is still worth showing.
	// A credential that renews needs a dev that can be pointed at the run's file. Which dev this
	// sandbox carries is asked of it, once, before the first turn: an image from before the first
	// release that merges the override (renewsFrom) answers `missing field name` to it and every turn
	// would die before its first model call. That box takes the same path claude and codex do.
	var plant func(context.Context, string) error
	file := modelPath(req.SessionID)
	if runsDev(req.Tool) {
		if ok, said := in.takesRenewal(); ok {
			plant = plantAt(id, file)
		} else {
			step("credential", said+"; its model credential cannot be renewed here, so this run ends a little short of the life of the one it holds, with its work kept", "running")
		}
	}
	admitted := ctx
	var forget func(context.Context) error
	if plant != nil {
		// The file goes with the run, unless the sandbox does: one that was ended holds nothing.
		forget = func(ctx context.Context) error {
			if !keep {
				return nil
			}
			return removeAt(id, file)(ctx)
		}
	}
	cred, err := key(ctx, loan{org: org, person: who.User, run: req.SessionID, scopes: scienceScopes(req), ttl: ttl, plant: plant, file: file, forget: forget,
		halt: func(reason string, cause error) {
			if _, err := endSession(context.WithoutCancel(admitted), org, req.SessionID, reason); err != nil {
				slog.Warn("coding: a run ended for its credential was not stopped through its session",
					"org", org, "session", req.SessionID, "err", err)
			}
			stop(cause)
		}})
	if err != nil {
		return RunResult{}, fmt.Errorf("coding: %w", err)
	}
	// The credential ends with the run, whatever way the run ends (key.go bearer.close).
	defer cred.close()
	// A person who may no longer act is not left a sandbox of theirs, once what the run did
	// is on its branch: the run's end is the earliest that is known.
	defer func() {
		if cred.removed() && err == nil && res.Error == "" {
			if _, e := sandboxpeer.SandboxEnd(context.WithoutCancel(ctx), &client.EndIn{ID: id}); e != nil {
				slog.Warn("coding: the sandbox of a run ended for its person's access was not ended", "sandbox", id, "session", req.SessionID, "err", e)
				return
			}
			keep = false
		}
	}()
	// A harness that spends its credential and cannot take another (claude and codex read it
	// once; a dev too old to be pointed at a file) is held to the life of the token it was started
	// with, and ends a little short of it with its work kept and the reason said (errUnrenewable):
	// IAM would not have granted it the whole run (delegationTTL). An interpreter makes no model
	// call, so what it holds is not a limit on it.
	if until, held := cred.dies(); held && spends(req.Tool) {
		if dl, has := ctx.Deadline(); has && until.Before(dl) {
			var done context.CancelFunc
			ctx, done = context.WithDeadlineCause(ctx, until.Add(-leaseMargin), errUnrenewable)
			defer done()
			in.ctx = ctx
		}
	}
	// An agent with a ticket is given the ask server and told what it is for, and
	// a plan's agent is told it is planning.
	task, ask, tick := req.Prompt, "", ""
	switch {
	case req.Setup:
		task = setupLine + task
	case req.Plan:
		task = planLine + task
	case agentic(req.Tool) && strings.TrimSpace(req.Remote) != "" && !req.Local:
		task = deliverLine + task
	}
	if agentic(req.Tool) && strings.TrimSpace(req.Remote) != "" && !req.Local {
		if treeless(req) {
			task = partialLine + task
		} else {
			task = shallowLine + task
		}
	}
	if runsDev(req.Tool) {
		task = shellLine + task
	}
	if agentic(req.Tool) {
		task = brief + task
	}
	if req.Ticket != "" && agentic(req.Tool) {
		task, ask, tick = askLine+task, askURL(), req.Ticket
	}
	if agentic(req.Tool) && !req.Setup && forbidsExploration(req.Prompt) {
		task = directLine + task
	}
	if req.Resumed && agentic(req.Tool) {
		task = resumedLine + task
	}
	if req.Parked && agentic(req.Tool) {
		task = continuingLine + task
	}
	// The model the caller named wins; else the platform's coding model, which is
	// a flag so it moves with no deploy.
	model := req.Model
	if model == "" && thinks(req.Tool) && strings.TrimSpace(req.Tool) != "codex" {
		model = flagsplane.String(ctx, codingModelKey, codingModel)
	}
	// The org's MCP servers ride beside the ask server, at the run's own address
	// for each and opened by the same ticket — so a run with no ticket carries
	// none.
	var servers []Server
	if tick != "" {
		servers = req.Kit.Servers
	}
	// THE GATEWAY CREDENTIAL IS BLINDED TOO, and it is the one most likely to be
	// printed: it becomes HANZO_API_KEY inside the box, so any `env`, any harness
	// that dumps its configuration, and any stack trace carrying the environment
	// publishes it. It spends the person's money for the rest of the run, so it is
	// kept out of every stream the run writes even though it reaches nothing else.
	in.cred, in.ticket = cred, tick
	// A harness that writes its turn as JSON lines narrates it as events
	// (outcome.go); its raw output is the log.
	run := &client.RunIn{ID: id, Session: req.SessionID, Blind: in.secrets(), Events: events(req.Tool)}
	prevThread := ""
	if ((req.Sandbox != "" && id == req.Sandbox) || req.Resumed) && runsDev(req.Tool) && !req.Setup {
		prevThread = in.loadThread()
		knownThread = prevThread
	}
	freshArgv := func(m string) []string {
		return withDeveloperInstructions(req.Tool, argvFor(req.Tool, ask, m, req.Effort, servers...), req.DeveloperInstructions)
	}
	turnArgv := func(m string) []string {
		if prevThread != "" {
			return withDeveloperInstructions(req.Tool, resumeFor(prevThread, ask, m, req.Effort, servers...), req.DeveloperInstructions)
		}
		return freshArgv(m)
	}
	acceptRolloutTeardown := func(r *client.Ran) bool {
		if !rolloutTeardownOnly(r) {
			return false
		}
		step(req.Tool, "harness reported a rollout error on exit after completing work; keeping its changes", "running")
		r.ExitCode = 0
		return true
	}
	ran, err := in.turn(ctx, run, turnArgv(model), task, ttl)
	if err == nil && ran != nil {
		acceptRolloutTeardown(ran)
	}
	// If a resumed thread is missing or the harness crashed on a thread/rollout error,
	// retry ONCE on a fresh thread in the SAME workspace so uncommitted edits are verified,
	// completed, committed, and pushed instead of lost.
	if err == nil && ran != nil && ran.ExitCode != 0 && killedBy(ran.ExitCode) == "" && outcomeOf(ran.Stdout).repeated == "" && ctx.Err() == nil {
		firstSaid := outcomeOf(ran.Stdout)
		if (prevThread != "" && firstSaid.thread == "" && !delegatedAuthRefused(ran) && !paymentRefused(ran) && !limitRefused(ran)) || harnessThreadCrash(ran) {
			retryPrompt := task
			if firstSaid.worked || prevThread == "" {
				retryPrompt = threadRecoveryLine + task
			}
			prevThread, knownThread = "", ""
			step(req.Tool, "harness hit a thread/rollout error; retrying on a fresh thread in the same workspace", "running")
			ran, err = in.turn(ctx, run, freshArgv(model), retryPrompt, ttl)
			if err == nil && ran != nil {
				acceptRolloutTeardown(ran)
			}
		}
	}
	// A 401 specifically saying this delegated token was revoked or expired mid-run
	// (e.g. an external sign-out, transient IAM revocation, or warm-sandbox handoff)
	// is recovered in place: re-check IAM access (mayAct) and mint a fresh delegated
	// token bound to this run, plant it into the box, and continue in the warm sandbox
	// instead of discarding uncommitted edits. If the person was actually removed from
	// the org, reissue ends the run cleanly with accessRemoved.
	if err == nil && ran != nil && ran.ExitCode != 0 && killedBy(ran.ExitCode) == "" && outcomeOf(ran.Stdout).repeated == "" &&
		delegatedAuthRefused(ran) && in.cred != nil && ctx.Err() == nil {
		step("credential", "the model credential was rejected; minting a fresh delegated token and continuing", "running")
		if rerr := in.cred.reissue(ctx); rerr == nil && ctx.Err() == nil {
			retryArgv, retryPrompt := turnArgv(model), task
			if s := outcomeOf(ran.Stdout); s.worked && threadRE.MatchString(s.thread) && runsDev(req.Tool) && !harnessThreadCrash(ran) {
				retryArgv = withDeveloperInstructions(req.Tool, resumeFor(s.thread, ask, model, req.Effort, servers...), req.DeveloperInstructions)
				retryPrompt = finishTurn
			}
			ran, err = in.turn(ctx, run, retryArgv, retryPrompt, ttl)
			if err == nil && ran != nil {
				acceptRolloutTeardown(ran)
			}
			if err == nil && ran != nil && ran.ExitCode != 0 && killedBy(ran.ExitCode) == "" && harnessThreadCrash(ran) && ctx.Err() == nil {
				prevThread, knownThread = "", ""
				ran, err = in.turn(ctx, run, freshArgv(model), threadRecoveryLine+task, ttl)
				if err == nil && ran != nil {
					acceptRolloutTeardown(ran)
				}
			}
		} else if rerr != nil {
			slog.Warn("coding: could not reissue delegated credential after 401", "org", org, "session", req.SessionID, "err", rerr)
		}
	}
	// A 402 is the payer, not the task. Enso falls back inside the gateway; a
	// model outside that family does not, and the harness used to exit on the
	// first refusal. One retry, on the free model, and only when the one that
	// was asked is not already that model.
	//
	// A turn the harness stopped for a loop is never read as one: its message
	// quotes the repeated call, and a call can carry "402" (`sed -n 400,402p`).
	// A harness killed mid-turn is not one either, whatever its stderr said (terminate).
	if err == nil && ran != nil && killedBy(ran.ExitCode) == "" && outcomeOf(ran.Stdout).repeated == "" && paymentRefused(ran) && model != codingFallback && ctx.Err() == nil {
		step(req.Tool, "the model was refused for balance; asking "+codingFallback, "running")
		model = codingFallback
		ran, err = in.turn(ctx, run, turnArgv(model), task, ttl)
		if err == nil && ran != nil {
			acceptRolloutTeardown(ran)
		}
	}
	// A short-lived per-minute rate limit or concurrency blip (rate_limit_exceeded,
	// paid_lane_full) — or a per-model cap — caps its wait at transientRetryCap (60s)
	// and falls back to another Enso model in the warm sandbox instead of sleeping
	// until a multi-hour window reset or stranding the run.
	if err == nil && ran != nil && ran.ExitCode != 0 && killedBy(ran.ExitCode) == "" && outcomeOf(ran.Stdout).repeated == "" && ctx.Err() == nil {
		if wait, ok := transientLimit(ran); ok {
			wait = capTransientWait(wait)
			if alt := limitFallback(model, req.Model != ""); alt != "" {
				model = alt
				step(req.Tool, "rate limit reached; waiting before retrying with "+model, "running")
			} else {
				step(req.Tool, "rate limit reached; waiting before retrying", "running")
			}
			if waitForRetry(ctx, wait) {
				retryArgv, retryPrompt := turnArgv(model), task
				if prevThread == "" {
					if s := outcomeOf(ran.Stdout); s.worked && threadRE.MatchString(s.thread) && runsDev(req.Tool) {
						retryArgv = withDeveloperInstructions(req.Tool, resumeFor(s.thread, ask, model, req.Effort, servers...), req.DeveloperInstructions)
						retryPrompt = finishTurn
					}
				}
				ran, err = in.turn(ctx, run, retryArgv, retryPrompt, ttl)
				if err == nil && ran != nil {
					acceptRolloutTeardown(ran)
				}
			}
		} else if modelCapRefused(ran) {
			if alt := limitFallback(model, req.Model != ""); alt != "" {
				model = alt
				step(req.Tool, "model cap reached; falling back to "+model, "running")
				retryArgv, retryPrompt := turnArgv(model), task
				if prevThread == "" {
					if s := outcomeOf(ran.Stdout); s.worked && threadRE.MatchString(s.thread) && runsDev(req.Tool) {
						retryArgv = withDeveloperInstructions(req.Tool, resumeFor(s.thread, ask, model, req.Effort, servers...), req.DeveloperInstructions)
						retryPrompt = finishTurn
					}
				}
				ran, err = in.turn(ctx, run, retryArgv, retryPrompt, ttl)
				if err == nil && ran != nil {
					acceptRolloutTeardown(ran)
				}
			}
		}
	}
	// A turn that came back with nothing is asked once more (outcome.go empty).
	emptyRetried := false
	if err == nil && ran != nil && ran.ExitCode == 0 && run.Events && ctx.Err() == nil && outcomeOf(ran.Stdout).empty() {
		emptyRetried = true
		step(req.Tool, "the model answered nothing; asking again", "running")
		ran, err = in.turn(ctx, run, turnArgv(model), task, ttl)
		if err == nil && ran != nil {
			acceptRolloutTeardown(ran)
		}
	}
	// A turn the harness stopped because its model repeated itself is carried on
	// in the same conversation, told so (loop.go). usage is every turn's.
	var usage Usage
	told, escalated, thread, lost := 0, "", "", ""
	for err == nil && ran != nil && ran.ExitCode != 0 && ctx.Err() == nil {
		said := outcomeOf(ran.Stdout)
		usage = usage.plus(said.usage)
		if told == 0 {
			thread = said.thread // the conversation every resume must come back in
		}
		next, ok := repeat(model, told, req.Model != "")
		if said.repeated == "" || !ok || !runsDev(req.Tool) || !threadRE.MatchString(thread) {
			break
		}
		name, _ := call(said.repeated)
		if next == model {
			step("loop", "the agent repeated "+cmp.Or(name, "a tool call")+"; telling it to change course", "running")
		} else {
			step("loop", "the agent repeated "+cmp.Or(name, "a tool call")+" again; handing the turn to "+next, "running")
			escalated = next
		}
		model, told = next, told+1
		ran, err = in.turn(ctx, run, withDeveloperInstructions(req.Tool, resumeFor(thread, ask, model, req.Effort, servers...), req.DeveloperInstructions), nudge(said.repeated, in.scrub, told > 1), ttl)
		// A resume killed before it named its conversation is a killed turn, not a lost one.
		if err == nil && ran != nil && killedBy(ran.ExitCode) == "" {
			if got := outcomeOf(ran.Stdout).thread; got != thread {
				lost = cmp.Or(got, "none")
				usage = usage.plus(outcomeOf(ran.Stdout).usage)
				break
			}
			if ran.ExitCode == 0 {
				usage = usage.plus(outcomeOf(ran.Stdout).usage)
			}
		}
	}
	if err == nil && ran == nil {
		err = fmt.Errorf("no result")
	}
	if cause := context.Cause(ctx); kept(cause) {
		// A POD BEING REPLACED KEEPS ITS RUNS' WORK, GIVES THE BOXES BACK AND PARKS THEM to go on
		// at once on the next pod: the same park a refused run takes, with no refusal to wait out.
		if errors.Is(cause, errDrain) {
			saved, done := context.WithTimeout(context.WithoutCancel(in.ctx), keepWait)
			defer done()
			in.ctx = saved
			return in.park(saved, org, id, agents.ParkRestart, "", nil, req, branch, base, RunResult{}, step)
		}
		// Stopped, paused or out of time mid-task: what the task did so far is kept
		// on the run's branch, as a finished run's is (steer.go). A plan keeps
		// nothing — it has no branch to keep it on, and could not push one.
		if req.Plan {
			return RunResult{}, nil
		}
		// A STEER IS NOT THE END OF THE WORK (#553). Its follow-up resumes this
		// sandbox and works in this tree, and it is the follow-up that commits and
		// pushes, once its own checks are done. Kept here, a half-finished turn was
		// committed and pushed the moment the steer arrived, before the tests it
		// was running had passed. The task is stopped, and the tree is left as it is.
		if errors.Is(cause, errFollowed) {
			return in.steered(org, base, step), nil
		}
		out, err := in.keep(req, branch, base, step)
		// The run's own context is done, so what it left is saved on one of its
		// own: a run that stopped for time has artifacts like any other.
		saved, done := context.WithTimeout(context.WithoutCancel(in.ctx), keepWait)
		in.ctx = saved
		in.save(org, base, &out, step)
		done()
		out.TimeUp = errors.Is(cause, errTimeUp)
		out.Unrenewable = errors.Is(cause, errUnrenewable)
		return out, err
	}
	// OUT OF TIME IS NOT NOTHING (hanzo-inc/cloud#450). A harness that reached its
	// budget stopped mid-task with its work in the tree, and a run that went on to
	// fail with "context deadline exceeded" threw most of a docs site away. That
	// work is kept on the run's branch, as a stopped run's is, and the run fails
	// saying where it is. A plan keeps nothing.
	if err != nil && outOfTime(ctx, err) && !req.Plan {
		step(req.Tool, "the agent ran out of time; keeping its work so far", "error")
		out, kerr := in.keep(req, branch, base, step)
		in.save(org, base, &out, step)
		if kerr != nil {
			return RunResult{}, fmt.Errorf("coding: run: %w, and its work could not be kept: %w", err, kerr)
		}
		out.Error = "the agent ran out of time before it finished, and had changed nothing"
		if out.Changed {
			out.Error = "the agent ran out of time before it finished; its work so far is on " + branch
		}
		step("done", "finished", statusOf(false))
		return out, nil
	}
	if err != nil {
		return RunResult{}, fmt.Errorf("coding: run: %w", err)
	}

	// Branch is deliberately NOT reported. coding.Run does not read it — the branch
	// is the one cloud issued, and a sandbox that answers a different one is
	// reporting something it was never asked — so filling it in would only offer a
	// value nobody may trust.
	said := outcomeOf(ran.Stdout)
	answer, final := in.answer(said, ran.Stdout)
	answer = in.scrub(answer)
	if told == 0 {
		usage = said.usage
	}
	out := RunResult{
		OK:      ran.ExitCode == 0,
		LogTail: in.scrub(tail(answer, ran.Stderr)),
		Answer:  planOf(answer),
		Usage:   usage,
		Empty:   empty,
		// A harness that keeps its last message in a file and left it empty ended
		// its turn without one: what said.answer holds then is an earlier message,
		// typically the model announcing work it never did.
		Unanswered: run.Events && !final,
	}
	if lost != "" {
		// A RESUME THAT CAME BACK IN ANOTHER CONVERSATION answered a nudge with no
		// task behind it, so whatever it did is not the run's work.
		out.OK = false
		out.Error = "the harness did not resume the conversation it was told to (" + thread + "; it answered in " + lost +
			"), so the run stopped rather than deliver a turn that never saw the task"
		in.save(org, base, &out, step)
		step("done", "finished", statusOf(false))
		return out, nil
	}
	if !out.OK {
		// A tool that failed produced nothing anyone should review on a branch, so
		// its work stays in the workspace — kept as its artifacts — and is not
		// pushed: that would file work against a branch whose own run says it did
		// not finish.
		//
		// A run that looped AFTER editing files is the exception (#522): committing
		// and pushing what it already changed lets a follow-up (or "Try again")
		// continue from the branch and keeps the sandbox warm instead of starting
		// over from base and hitting the same loop.
		//
		// A REFUSAL THE RUN CAN WAIT OUT IS NOT A FAILURE. The model API refused the turn for a
		// usage limit or for payment, which time or credits clear, or failed it with a server error
		// that outlasted the harness's retries, and a run that took an hour is not a dead end for
		// it: its work is kept on its branch, its sandbox is given back, and it parks (park.go)
		// until the limit clears or the API answers. One that cannot keep its work on a
		// branch, or whose work could not be kept, falls through and fails as it did.
		reason := ""
		if said.repeated == "" && killedBy(ran.ExitCode) == "" && ctx.Err() == nil {
			reason = refusedFor(ran)
		}
		last := cmp.Or(in.scrub(said.failed), lastLine(in.scrub(ran.Stderr)))
		if reason != "" && (req.Plan || (!req.Local && strings.TrimSpace(req.Remote) != "")) {
			parked, kerr := in.park(ctx, org, id, reason, firstLine(last), ran, req, branch, base, out, step)
			if kerr == nil {
				keep = false
				return parked, nil
			}
			step("park", "the run could not wait for the refusal to clear: "+in.scrub(kerr.Error()), "error")
		}
		// The error carries the harness's LAST WORD, not just its exit status: it is
		// what the person who asked is told, and "exit 1" alone names no fault.
		out.Error = fmt.Sprintf("the %s harness exited %d", harness(req.Tool), ran.ExitCode)
		switch {
		case said.repeated != "":
			// THE LOOP IS NAMED, with what the run did about it, so the person reads
			// which call the agent could not get past rather than the guard's words.
			out.Error = "the agent kept making the same tool call with the same result"
			switch {
			case escalated != "":
				out.Error += ", after it was told to change course and the turn was handed to " + escalated
			case told > 0:
				out.Error += ", after it was told to change course"
			}
			out.Error += ": " + shown(said.repeated, in.scrub)
		case killedBy(ran.ExitCode) != "":
			terminate(&out, req.Tool, ran.ExitCode)
		case reason == agents.ParkLimit:
			// A USAGE LIMIT IS NOT THE TASK'S FAULT, and "exited 1: exceeded retry limit,
			// last status: 429" names neither the limit nor what clears it.
			out.Error = refusedSaid
			if last != "" {
				out.Error += " (" + last + ")"
			}
		case last != "":
			out.Error += ": " + last
		}
		keptWork := false
		if !req.Plan {
			switch {
			case said.repeated != "" || harnessThreadCrash(ran):
				if kept, kerr := in.keep(req, branch, base, step); kerr == nil {
					keptOnLoop(&out, kept, branch)
					keptWork = kept.Changed
				}
			case said.worked && killedBy(ran.ExitCode) == "" && reason == "":
				// Never auto-commit half-finished working-tree edits on a crashed turn:
				// the warm sandbox stays alive below so a follow-up can resume it cleanly,
				// while any atomic commits the agent already made are pushed to its branch.
				if kept, kerr := in.pushCommitted(req, branch, base, step); kerr == nil {
					keptOnLoop(&out, kept, branch)
					keptWork = kept.Changed
				}
			}
		}
		in.save(org, base, &out, step)
		// A SANDBOX THIS RUN RESUMED IS NOT ITS TO END: it holds an earlier run's
		// work, which a steered run leaves there unpushed for this one (#553).
		// Likewise, a turn that already worked keeps its warm sandbox resumable.
		resumable := keptWork || (said.worked && killedBy(ran.ExitCode) == "" && !req.Plan)
		if !resumable && id != req.Sandbox {
			keep = false
			step("end", "the run failed with nothing committed; ending the sandbox", "error")
			if _, err := sandboxpeer.SandboxEnd(ctx, &client.EndIn{ID: id}); err != nil {
				slog.Warn("coding: the failed run's sandbox was not ended", "sandbox", id, "session", req.SessionID, "err", err)
				keep = true
			}
		}
		step("done", "finished", statusOf(false))
		return out, nil
	}
	// A PLAN DELIVERS NOTHING: no commit, no push, whatever its harness did to
	// the checkout. Its answer is what the harness said, scrubbed like every
	// other word that leaves the box.
	if req.Plan {
		out.Plan = out.Answer
		step("done", "planned", statusOf(out.OK))
		return out, nil
	}
	// WHAT A TURN LEFT IS COMMITTED WHEN IT ENDS, AND PUSHED ONCE THE RUN'S CHECKS
	// BELOW ARE OVER (#553). It was pushed after the first turn, so a turn that
	// ended on an announcement, before the test it announced had run, was on the
	// branch by the time the run was asked to finish it.
	if err := in.commit(req, base, &out, step); err != nil {
		if in.rearm(req) != nil {
			_ = in.rearm(req)
		}
		if err = in.commit(req, base, &out, step); err != nil {
			return RunResult{}, err
		}
	}
	return in.settleAndDeliver(ctx, org, id, req, run, branch, base, task, ask, model, ttl, told, escalated, emptyRetried, servers, said, freshArgv, acceptRolloutTeardown, step, out, &keep)
}
