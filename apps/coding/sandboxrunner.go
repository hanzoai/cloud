package coding

// sandboxrunner.go runs a coding task in OUR sandbox — a gVisor pod in
// hanzo-sandboxes — reached over the internal plane.
//
// # Why this exists
//
// The Runner seam had exactly one implementation, and it could not run. It
// POSTed to bot's /v1/coding-tasks, which invokes the `docker` CLI —
// bot-gateway has neither that binary nor a socket, so every dispatch 503'd.
// The whole chain apps/coding → bots.Stream → /v1/coding-tasks was dead in
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
// SANDBOX_RUNTIME_CLASS stays one string (gvisor | kata-fc | kata-clh | empty)
// and never a fork in code. That is load-bearing rather than tidy: a benchmark
// inverted the expected answer — Firecracker beat gVisor on BOTH axes (git
// status 82ms vs 980ms, start 294ms vs 881ms, ~57 MiB either way) — so the
// boundary has to be switchable by deployment, not by rewrite.
//
// # Why the plane and not HTTP
//
// apps/bots' transport is net/http by its own admission — its doc says the bytes
// "should move over ZAP" and that the swap "is meant to be a change to THIS FILE
// plus each caller's one stub". This is that stub, and it skips the migration
// rather than performing it: a plane op IS ZAP, so there is no HTTP hop to port.
// apps/bots keeps its transport for actual bot traffic, which is what it is for.
//
// # The five ops are the whole vocabulary
//
// lease → run → end, with read/write for files. They are the same ops the fleet
// door now publishes to agents (lease_sandbox, run_in_sandbox, …), so a human
// driving a sandbox from chat and this Runner driving one for a coding task are
// using ONE surface. A second private path into a sandbox is the duplication
// this file exists to avoid.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
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
		return "dev" // dev | claude | codex | "" all need the toolchain
	}
}

// argvFor is the command that runs inside the sandbox.
//
// The runtime owns the real name→argv table (bot's TOOLS), and this is the
// minimum cloud has to know to start one. It is deliberately not a second table
// of flags: everything a harness needs beyond its name travels in the prompt.
func argvFor(tool, prompt string) []string {
	switch strings.TrimSpace(tool) {
	case "claude":
		return []string{"claude", "-p", prompt}
	case "codex":
		return []string{"codex", "exec", prompt}
	case "python":
		return []string{"python3", "-c", prompt}
	case "node":
		return []string{"node", "-e", prompt}
	default:
		return []string{"dev", "-p", prompt}
	}
}

// Run leases a sandbox, does the work in it, and ends the lease.
//
// The lease is ended on EVERY exit including a panic-free error path, because a
// sandbox that outlives its run is the orphan class we spent a day clearing —
// and the TTL backstop (exec 900s, dev/desktop 14400s) is a floor for crashes,
// never a substitute for saying goodbye.
func (sandboxRunner) Run(ctx context.Context, org, userID string, req RunRequest, onStep func(Step)) (RunResult, error) {
	step := func(name, msg, status string) {
		if onStep != nil {
			onStep(Step{Type: "step", Step: name, Message: msg, Status: status})
		}
	}

	// THE ONE REF THIS RUN MAY WRITE, checked before anything is leased. The name
	// is cloud's (BranchFor, a pure function of the session), the forge refuses
	// anything else independently (apps/git/refpolicy.go), and this is the third
	// statement of the same rule at the only other place that could break it — the
	// process holding the credential. A run handed `main` stops here rather than
	// discovering at push time that it was never allowed.
	branch := strings.TrimSpace(req.Branch)
	if strings.TrimSpace(req.CloneURL) != "" && !strings.HasPrefix(branch, agentPrefix) {
		return RunResult{}, fmt.Errorf("coding: %q is not an agent branch", branch)
	}

	class := classFor(req.Tool, req.Desktop)
	ttl := req.RunTimeoutSeconds
	if ttl <= 0 {
		ttl = 3600
	}

	// COMPUTE IS NOT FREE, and the gate is here rather than after the work
	// because a lease holds a pod whether or not anyone can pay for it.
	if err := affordable(ctx, org); err != nil {
		return RunResult{}, fmt.Errorf("coding: %w", err)
	}

	step("lease", "leasing a "+class+" sandbox", "running")
	leased, err := plane.Ask[plane.LeaseIn, plane.Leased](ctx, "sandboxes", plane.SandboxLease,
		&plane.LeaseIn{Class: class, Project: req.SessionID, TTLSec: ttl})
	if err != nil {
		return RunResult{}, fmt.Errorf("coding: lease sandbox: %w", err)
	}
	if leased == nil || strings.TrimSpace(leased.ID) == "" {
		return RunResult{}, fmt.Errorf("coding: lease sandbox: no id")
	}
	id := leased.ID

	// THE SANDBOX'S ID IS SAID OUT LOUD, because it is the handle for the only two
	// things a person watching a run can do to it: stop the work (stop_run) or
	// release it (end_sandbox). A run that never named its sandbox could be watched
	// and not touched.
	step("leased", "sandbox "+id+" ("+class+")", "running")

	// END ON EVERY PATH. A detached context because the caller's may already be
	// cancelled by the time we unwind — the lease still has to be released, and
	// releasing it is not the caller's deadline to spend.
	defer func() {
		end, cancel := context.WithTimeout(cloud.For(context.Background(), org), 30*time.Second)
		defer cancel()
		_, _ = plane.Ask[plane.EndIn, struct{}](end, "sandboxes", plane.SandboxEnd,
			&plane.EndIn{ID: id})
		// Said AFTER the lease is actually gone, so "ended" means the pod is
		// released and not that we intended to release it.
		step("ended", "released sandbox "+id, "running")
	}()

	// The checkout, when there is one. No repo means no clone and no credential —
	// the request shape already guarantees the second (CredToken must be empty
	// when CloneURL is), so there is nothing to strip here.
	in := sandbox{ctx: ctx, id: id, ttl: ttl, session: req.SessionID, token: req.CredToken}
	base := ""
	if u := strings.TrimSpace(req.CloneURL); u != "" {
		step("clone", "cloning "+u, "running")
		// The clone narrates into the session like everything else. It is the step
		// that most often hangs — a big repo, a slow forge — and a watcher seeing
		// git count objects knows the difference between slow and stuck.
		if _, err := in.do(cloneArgv(req), "clone"); err != nil {
			return RunResult{}, err
		}
		// The run's branch exists BEFORE the first edit, so a tool that commits for
		// itself commits onto the run's branch and never onto the base.
		if _, err := in.do([]string{"git", "switch", "-c", branch}, "branch"); err != nil {
			return RunResult{}, err
		}
		var err error
		if base, err = in.tip(); err != nil {
			return RunResult{}, err
		}
	}

	step(req.Tool, "running the task", "running")
	ran, err := runIn(ctx, id, argvFor(req.Tool, req.Prompt), ttl, req.SessionID)
	if err != nil {
		return RunResult{}, fmt.Errorf("coding: run: %w", err)
	}

	// Branch is deliberately NOT reported. coding.Run does not read it — the branch
	// is the one cloud issued, and a sandbox that answers a different one is
	// reporting something it was never asked — so filling it in would only offer a
	// value nobody may trust.
	out := RunResult{
		OK:      ran.ExitCode == 0,
		LogTail: in.scrub(tail(ran.Stdout, ran.Stderr)),
	}
	if !out.OK {
		// A tool that failed produced nothing anyone should review, so its checkout
		// stays here and dies with the lease. Pushing it would file work against a
		// branch whose own run says it did not finish.
		out.Error = fmt.Sprintf("exit %d", ran.ExitCode)
		step("done", "finished", statusOf(false))
		return out, nil
	}
	if base != "" {
		if err := in.deliver(req, branch, base, &out, step); err != nil {
			return RunResult{}, err
		}
	}
	step("done", "finished", statusOf(out.OK))
	return out, nil
}

// ── the work leaves the sandbox ──────────────────────────────────────────────
//
// Everything above is how a run HAPPENS; this is how it SURVIVES. Without it the
// rest is a model talking to itself: the sandbox is per-run and reaped, so an
// edit that stays in the checkout is deleted by the lease ending. The Runner did
// lease → clone → run → end and threw the work away.
//
// It is four facts and one rule.
//
//	the branch   is the one cloud issued for the session, created before the
//	             first edit.
//	the change   is the difference between two object ids — what we checked out
//	             and what is there now. Not a status parse and not a guess: it is
//	             the only reading that stays true both when a tool leaves the tree
//	             dirty and when it commits for itself, and a run that reports "no
//	             changes" because it never looked is the failure this file was.
//	the tip      is what rev-parse says AFTER the commit, so the sha we report is
//	             one that exists.
//	the diffstat is measured over exactly that range, so what a reviewer reads and
//	             what was pushed are the same thing.
//
//	THE RULE: the credential is applied to ONE INVOCATION and never recorded.

// agentPrefix is the machine namespace, spelled as coding's own BranchFor builds
// it. apps/git states the same rule over full refs (refpolicy.go agentRefPrefix)
// and the two are deliberately independent: this one keeps an honest run inside
// its lane, that one keeps a compromised one there.
const agentPrefix = "agent/"

// Who the commit is by. An agent is not a person and does not borrow one: the
// author of a run's commit is the run's harness, and the human who asked for it
// is on the PR and in the session.
const (
	authorName  = "hanzo-agent"
	authorEmail = "agent@hanzo.ai"
)

// sandbox is one leased sandbox, addressed. It carries what every command of a
// run needs — where, for how long, who is watching, and the grant that must never
// come back out — so no call site repeats them and no error path can forget the
// last one.
type sandbox struct {
	ctx     context.Context
	id      string
	ttl     int
	session string
	token   string
}

// deliver commits what the tool left behind, pushes it to the run's ref, and
// fills in what actually happened.
//
// The commit's EXIT CODE IS NOT READ. `git commit` with nothing staged fails, and
// that failure is indistinguishable from a tool that had already committed — so
// the question is answered by the sha instead, which is a fact rather than an
// opinion about one. When the tip has not moved, nothing changed, and nothing is
// pushed: a PR against a branch with no commits teaches a reviewer to ignore the
// agent, which is worse than the missing PR it replaces.
func (in sandbox) deliver(req RunRequest, branch, base string, out *RunResult, step func(name, msg, status string)) error {
	if _, err := in.do([]string{"git", "add", "-A"}, "stage"); err != nil {
		return err
	}
	// The identity rides the invocation for the same reason the credential does:
	// `git -c` before a subcommand is not written into the repository's config.
	if _, err := runIn(in.ctx, in.id, []string{"git",
		"-c", "user.name=" + authorName, "-c", "user.email=" + authorEmail,
		"commit", "--quiet", "-m", message(req.Prompt, in.session)}, in.ttl, in.session); err != nil {
		return fmt.Errorf("coding: commit: %w", err)
	}

	tip, err := in.tip()
	if err != nil {
		return err
	}
	if tip == base {
		step("done", "no changes were needed", "running")
		return nil // and Changed stays false, which is now a measurement
	}

	stat, err := in.do([]string{"git", "diff", "--stat", base, tip}, "diff")
	if err != nil {
		return err
	}
	step("push", "pushing "+branch, "running")
	if _, err := in.do(pushArgv(req, branch), "push"); err != nil {
		return err
	}
	out.Changed, out.CommitSha, out.Diffstat = true, tip, strings.TrimSpace(stat.Stdout)
	return nil
}

// cloneArgv checks out the repo. The credential is a per-invocation rewrite, so
// what git records as the remote is the plain URL and the grant is gone the
// moment the process is.
func cloneArgv(req RunRequest) []string {
	argv := append(gitAs(req), "clone", "--depth", "1")
	if b := strings.TrimSpace(req.BaseBranch); b != "" {
		argv = append(argv, "-b", b)
	}
	return append(argv, req.CloneURL, ".")
}

// pushArgv writes ONE ref, named in full, and never forces.
//
// The refspec is explicit rather than `push origin <branch>` because the local
// name and the remote ref are then the same string we checked and the grant
// names, with no configured remote and no push.default in between to resolve it
// into something else.
func pushArgv(req RunRequest, branch string) []string {
	return append(gitAs(req), "push", req.CloneURL, "HEAD:refs/heads/"+branch)
}

// gitAs is `git`, carrying the grant when there is one — as a URL rewrite that
// applies to this invocation ALONE.
//
// The credential used to ride the clone URL, on the argument that "nothing
// survives the process that used it". That was false: git writes the URL it was
// given into .git/config verbatim, userinfo and all, so the grant sat readable in
// the checkout for every later step of the run — including the one that executes
// untrusted model output. A rewrite given with `git -c` BEFORE the subcommand is
// not persisted (unlike `git clone -c`, which is), so the remote git records is
// the plain URL and the grant lives only in the environment of the one command
// that needed it.
//
// It is one function because clone and push are the same act — reach that URL as
// this bearer — and a second spelling of it is a second place to get it wrong.
func gitAs(req RunRequest) []string {
	rest, https := strings.CutPrefix(req.CloneURL, "https://")
	if req.CredToken == "" || !https {
		return []string{"git"}
	}
	user := req.CredUser
	if user == "" {
		user = "x-access-token"
	}
	return []string{"git", "-c",
		"url.https://" + user + ":" + req.CredToken + "@" + rest + ".insteadOf=" + req.CloneURL}
}

// tip reads the checkout's current commit.
func (in sandbox) tip() (string, error) {
	ran, err := in.do([]string{"git", "rev-parse", "HEAD"}, "read the tip")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(ran.Stdout), nil
}

// do is a command whose failure is the RUN's failure.
//
// runIn answers a non-zero exit as data, deliberately — "the tests failed" and
// "the sandbox is broken" are different facts. For the commands here they are the
// same fact: a clone that did not clone or a push that did not push leaves
// nothing to report, so the exit code is read and the run stops.
func (in sandbox) do(argv []string, what string) (*plane.Ran, error) {
	ran, err := runIn(in.ctx, in.id, argv, in.ttl, in.session)
	if err != nil {
		return nil, fmt.Errorf("coding: %s: %s", what, in.scrub(err.Error()))
	}
	if ran.ExitCode != 0 {
		return nil, fmt.Errorf("coding: %s: exit %d: %s", what, ran.ExitCode,
			in.scrub(strings.TrimSpace(tail(ran.Stdout, ran.Stderr))))
	}
	return ran, nil
}

// message is what the commit says: the task, and the run that did it.
//
// The session is in the trailer because the commit outlives every other record of
// the run — the Slack thread scrolls away, the sandbox is reaped — and a commit
// nobody can trace back to the conversation that asked for it is an orphan in
// somebody's history.
func message(prompt, session string) string {
	m := firstLine(prompt)
	if len(m) > maxTitlePrompt {
		m = strings.TrimSpace(m[:maxTitlePrompt]) + "…"
	}
	if m == "" {
		m = "agent changes"
	}
	return m + "\n\nRun: " + session + "\n"
}

// scrub takes the grant out of anything that leaves the sandbox.
//
// git redacts the userinfo from the URLs it prints — verified, including through
// an insteadOf rewrite — and this does not rely on that. Everything here becomes a
// session event, a Slack line, a log field and a span; a credential is one string
// away from all four, and being sure costs four lines. It hangs off the sandbox
// rather than sitting loose so that every path out of a command goes through it
// without anyone having to remember.
func (in sandbox) scrub(s string) string {
	if strings.TrimSpace(in.token) == "" {
		return s
	}
	return strings.ReplaceAll(s, in.token, "[redacted]")
}

// runIn runs one command in the sandbox and narrates it into the run's session.
//
// The session travels with the command rather than the output travelling back
// here, because the bytes are IN THE SANDBOX and this call does not return until
// the command is over. Streamed from where they are produced, a twenty-five
// minute agent edit loop is watchable; collected here, it is a silence.
func runIn(ctx context.Context, id string, argv []string, ttl int, session string) (*plane.Ran, error) {
	ran, err := plane.Ask[plane.RunIn, plane.Ran](ctx, "sandboxes", plane.SandboxRun,
		&plane.RunIn{ID: id, Argv: argv, TimeoutSec: ttl, Session: session})
	if err != nil {
		return nil, err
	}
	if ran == nil {
		return nil, fmt.Errorf("no result")
	}
	return ran, nil
}

// tail keeps the end of the output, which is where a failure says why. The cap
// is a Slack message's worth: this is read by a person in a thread, not archived.
func tail(stdout, stderr string) string {
	s := stdout
	if strings.TrimSpace(stderr) != "" {
		s = strings.TrimRight(s, "\n") + "\n" + stderr
	}
	const cap = 3000
	if len(s) > cap {
		return "…" + s[len(s)-cap:]
	}
	return s
}

func statusOf(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}

// ── the funded-account gate ──────────────────────────────────────────────────

// minFundedSeconds is how much compute an org must be able to pay for BEFORE a
// sandbox is leased.
//
// Fifteen minutes, and the number is the argument: a lease is not a request, it
// is a pod held on our nodes for as long as the run wants it — dev and desktop
// carry a 4-HOUR TTL. Charging afterwards only works if there is something to
// charge, so the check has to happen before the pod exists. Fifteen minutes is
// long enough that no honest run is refused for a rounding error, and short
// enough that an unfunded account cannot open a four-hour hole and walk away.
const minFundedSeconds = 15 * 60

// minFundedDecimal is minFundedSeconds of compute as money — the amount the gate
// is asked to authorize.
//
// A constant rather than a catalog read on purpose: this figure exists to refuse
// an EMPTY account, not to quote a bill. The real charge is metered as the lease
// runs, and a gate that tried to predict it exactly would be a second pricing
// implementation. It only has to be the right order of magnitude.
const minFundedDecimal = "0.04" // ~15 min at ~$0.15/hour

// affordable asks COMMERCE whether org can pay for minFundedSeconds of compute.
//
// It asks the gate rather than reading a balance and doing arithmetic here.
// finance_authorize is labelled "the prepaid gate" in plane.go and it already
// knows things a subtraction does not: spend caps, which is why Verdict carries
// NoFunds and CapSpent as SEPARATE bits — one says add money, the other says
// wait for the period to roll. A balance comparison in this file would be a
// second pricing implementation that can disagree with the first.
//
// The org is stated on a DETACHED context because commerce takes the org from
// the CALLER identity and never from an argument, and a Runner has no inbound
// request to carry one. cloud.For is read only where no request is behind the
// ctx — on a request-bearing one it is a silent no-op that cost four deploys.
//
// It fails CLOSED on an unreadable verdict, and plane.go says why in the type
// itself: Reason "is an upstream failure the caller must treat as UNKNOWN and
// fail closed on, and never read as permission." Failing open costs a pod held
// for four hours by an account that cannot pay; failing closed costs a retry.
func affordable(ctx context.Context, org string) error {
	v, err := plane.Ask[plane.AuthorizeIn, plane.Verdict](
		cloud.For(context.Background(), org), "commerce", plane.FinanceAuthorize,
		&plane.AuthorizeIn{
			Subject: org,
			Amount:  plane.Money{Decimal: minFundedDecimal, Currency: "usd"},
			Service: "sandbox",
		})
	if err != nil {
		return fmt.Errorf("compute is not free and the prepaid gate is unreachable: %w", err)
	}
	if v == nil {
		return fmt.Errorf("compute is not free and the prepaid gate answered nothing")
	}
	switch {
	case v.OK:
		return nil
	case v.NoFunds:
		return fmt.Errorf("a sandbox needs at least %d minutes of funded compute (%s); top up to start a run",
			minFundedSeconds/60, minFundedDecimal)
	case v.CapSpent:
		return fmt.Errorf("this org has spent its cap for the period; a sandbox cannot start until it rolls over")
	default:
		return fmt.Errorf("the prepaid gate refused a sandbox: %s", v.Reason)
	}
}
