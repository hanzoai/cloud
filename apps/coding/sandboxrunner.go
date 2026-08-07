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

	// END ON EVERY PATH. A detached context because the caller's may already be
	// cancelled by the time we unwind — the lease still has to be released, and
	// releasing it is not the caller's deadline to spend.
	defer func() {
		end, cancel := context.WithTimeout(cloud.For(context.Background(), org), 30*time.Second)
		defer cancel()
		_, _ = plane.Ask[plane.EndIn, struct{}](end, "sandboxes", plane.SandboxEnd,
			&plane.EndIn{ID: id})
	}()

	// The checkout, when there is one. No repo means no clone and no credential —
	// the request shape already guarantees the second (CredToken must be empty
	// when CloneURL is), so there is nothing to strip here.
	if u := strings.TrimSpace(req.CloneURL); u != "" {
		step("clone", "cloning "+u, "running")
		if _, err := runIn(ctx, id, cloneArgv(req), ttl); err != nil {
			return RunResult{}, fmt.Errorf("coding: clone: %w", err)
		}
	}

	step(req.Tool, "running the task", "running")
	ran, err := runIn(ctx, id, argvFor(req.Tool, req.Prompt), ttl)
	if err != nil {
		return RunResult{}, fmt.Errorf("coding: run: %w", err)
	}

	out := RunResult{
		OK:      ran.ExitCode == 0,
		LogTail: tail(ran.Stdout, ran.Stderr),
	}
	if !out.OK {
		out.Error = fmt.Sprintf("exit %d", ran.ExitCode)
	}
	step("done", "finished", statusOf(out.OK))
	return out, nil
}

// cloneArgv checks out the repo with the credential in the URL rather than in a
// config file, so nothing survives the process that used it. The sandbox is
// per-run and reaped, but a token written to .git/config would still be readable
// by every later step of the same run, which is a wider window than the clone.
func cloneArgv(req RunRequest) []string {
	url := req.CloneURL
	if req.CredToken != "" {
		user := req.CredUser
		if user == "" {
			user = "x-access-token"
		}
		if rest, ok := strings.CutPrefix(url, "https://"); ok {
			url = "https://" + user + ":" + req.CredToken + "@" + rest
		}
	}
	argv := []string{"git", "clone", "--depth", "1"}
	if b := strings.TrimSpace(req.BaseBranch); b != "" {
		argv = append(argv, "-b", b)
	}
	return append(argv, url, ".")
}

func runIn(ctx context.Context, id string, argv []string, ttl int) (*plane.Ran, error) {
	ran, err := plane.Ask[plane.RunIn, plane.Ran](ctx, "sandboxes", plane.SandboxRun,
		&plane.RunIn{ID: id, Argv: argv, TimeoutSec: ttl})
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
