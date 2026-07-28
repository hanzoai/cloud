// Package coding is the keystone that turns @hanzo from a chatbot into an
// engineer: it orchestrates ONE autonomous coding run — register a live agent
// session, dispatch the job to the bot-gateway sandbox runtime, mirror the
// sandbox's progress into the session live, verify the pushed branch landed in
// native git, and open a native "PR" work item — then returns a Result the
// trigger surface (Slack today, console/API tomorrow) renders.
//
// It is a LEAF, transport-agnostic library. It touches its collaborators only
// through interface seams (Sessions, Tracker, Runner) plus two git functions
// (CloneURL, VerifyRef), so the whole orchestration is unit-testable with fakes
// and — critically — coding does NOT import clients/git: git imports
// clients/integrations, integrations calls coding, so coding->git would cycle.
// The composition root assembles the real Dispatcher (adapters.go) and injects
// it into the trigger surface.
//
// ISOLATION: org is the ONLY tenant key and is threaded to every seam call
// (session, tracker, git, and the bot-gateway X-Org-Id). A run for org A can only
// ever open A's session, read/verify A's repo, and file A's PR. The clone URL is
// built from (org, repo) so the sandbox is pointed only at this org's namespace,
// and the credential (write-only) is scoped by IAM to this org at the edge.
package coding

import (
	"context"
	"encoding/json"
	"strings"
)

// Event kinds mirrored into the agent session. These are the agents session
// vocabulary (a stable wire contract): a phase is a tool-call, a free line is a
// log, a lifecycle transition is a status. Kept as local constants so coding does
// not import the agents package for three strings.
const (
	kindToolCall = "tool-call"
	kindLog      = "log"
	kindStatus   = "status"
)

const (
	statusDone  = "done"
	statusError = "error"
)

// Sessions is the live agent-session registry seam (clients/agents in-process).
type Sessions interface {
	Open(ctx context.Context, org, actor, agent, title string) (string, error)
	// OpenOn opens a session tagged with the run's dispatch TARGET, so a routed
	// run shows in mission-control on the machine it was sent to. An empty target
	// behaves exactly like Open.
	OpenOn(ctx context.Context, org, actor, agent, title, target string) (string, error)
	Log(ctx context.Context, org, sessionID, kind, actor string, payload []byte) error
	Close(ctx context.Context, org, sessionID, status string) error
}

// Tracker is the work-item seam (clients/tracker in-process).
type Tracker interface {
	CreatePR(ctx context.Context, in PRInput) (PRRef, error)
}

// Runner is the bot-gateway coding-task seam (clients/bot in-process client).
type Runner interface {
	Run(ctx context.Context, org, userID string, req RunRequest, onStep func(Step)) (RunResult, error)
}

// PRInput / PRRef mirror tracker's agent-PR shape without leaking its types into
// the seam (the adapter bridges).
type PRInput struct {
	Org      string
	Project  string
	Repo     string
	Base     string
	Head     string
	Title    string
	Body     string
	Assignee string
}

type PRRef struct {
	Identifier string
	ProjectKey string
	Number     int
}

// RunRequest / Step / RunResult mirror the bot coding contract.
type RunRequest struct {
	CloneURL          string
	BaseBranch        string
	Branch            string
	Prompt            string
	SessionID         string
	RunTimeoutSeconds int
	CredUser          string
	CredToken         string // write-only secret — never logged
}

type Step struct {
	Type    string
	Step    string
	Message string
	Status  string
}

type RunResult struct {
	Branch    string
	CommitSha string
	Diffstat  string
	Changed   bool
	OK        bool
	LogTail   string
	Error     string
}

// Req is one coding request the trigger surface dispatches. Credential is the
// per-org agent git secret the caller resolved from KMS fail-closed; it is
// relayed to the sandbox and NEVER logged or placed in a session event.
//
// TargetID, when set, ROUTES the run to a registered machine (#48): the run is
// enqueued as a durable task addressed to that target instead of executing in the
// cloud-side sandbox, and the credential is NOT used (the machine authenticates
// with its own). When empty, the local sandbox path runs unchanged.
type Req struct {
	Org            string
	UserID         string // linked Hanzo subject — session attribution + X-User-Id
	AgentRef       string // agent label (e.g. "hanzo")
	Repo           string
	Project        string // IAM project slug (tracker + git scope); "" = org default
	Base           string // base branch; "" = repo default
	Prompt         string
	CredUser       string
	CredToken      string
	TimeoutSeconds int
	TargetID       string // when set, route to this registered machine instead of the sandbox
}

// RoutedRun is the NON-SECRET spec coding hands the Route seam to enqueue on the
// durable engine. It mirrors agents.RoutedRun so coding.go stays pure (no agents
// import); the adapter bridges the two, exactly as PRInput/RunRequest mirror
// their downstream types. It carries no credential by design — the executing
// machine authenticates git + model routing with its own already-held creds.
type RoutedRun struct {
	Org            string
	TargetID       string
	SessionID      string
	Repo           string
	Project        string
	Base           string
	Branch         string
	Prompt         string
	CloneURL       string
	TimeoutSeconds int
	// Actor + AgentRef are the dispatching user + agent label, carried so the durable
	// completion path can attribute the session close and file the PR with the same
	// assignee the local path uses. Neither is a secret and neither crosses to the
	// machine (the durable view the machine claims omits them).
	Actor    string
	AgentRef string
}

// Result is the terminal outcome the trigger surface renders.
type Result struct {
	SessionID string
	Repo      string
	Branch    string
	CommitSha string
	Diffstat  string
	Changed   bool
	OK        bool
	Verified  bool // pushed branch confirmed present in native git
	PR        PRRef
	LogTail   string
	Error     string
	// Routed reports that the run was ENQUEUED to a target machine rather than run
	// in the cloud sandbox. When true, OK means "accepted + queued" (not
	// "completed"): the terminal outcome flows through the session stream as the
	// machine executes. TargetID is the machine it was routed to.
	Routed   bool
	TargetID string
}

// Runner-facing behavioral defaults.
const (
	defaultTimeoutSeconds = 1200 // 20 min sandbox budget when the caller sets none
	maxTitlePrompt        = 120
	maxPromptLen          = 32 << 10 // 32 KiB — a task prompt, not a document
)

// Dispatcher wires the seams. The two git functions are injected (not an
// interface) because they are pure reads with no cloud-side state.
type Dispatcher struct {
	Sessions  Sessions
	Tracker   Tracker
	Runner    Runner
	CloneURL  func(org, repo string) string
	VerifyRef func(ctx context.Context, org, repo, branch string) (string, bool)
	// Log is an optional structured log seam for best-effort mirror failures; nil
	// is fine (mirror failures are non-fatal and simply dropped).
	Log func(msg string, kv ...any)
	// Route enqueues a routed run on the durable engine (the tasks-engine binding
	// in routed.go). Nil disables routing — a run with a TargetID then fails
	// closed rather than silently running in the sandbox.
	Route func(ctx context.Context, run RoutedRun) error
	// TargetGate is the fail-closed liveness+existence check for a routed run's
	// target (agents.TargetDispatchable): the target exists in this org, is
	// online, and has a live runner. Nil disables routing.
	TargetGate func(ctx context.Context, org, targetID string) error
}

// Run executes one coding job end to end and returns its Result. It never
// panics on a seam failure (each is turned into a recorded error); the caller
// runs it under a bounded, recovered goroutine with a deadline ctx.
func (d Dispatcher) Run(ctx context.Context, req Req) Result {
	org := strings.TrimSpace(req.Org)
	repo := strings.TrimSpace(req.Repo)
	prompt := strings.TrimSpace(req.Prompt)
	res := Result{Repo: repo}

	// Boundary validation (fail closed): a coding run needs a tenant, a repo, a
	// task, and a credential to reach native git.
	if org == "" || repo == "" {
		res.Error = "org and repo are required"
		return res
	}
	if prompt == "" {
		res.Error = "empty task"
		return res
	}
	if len(prompt) > maxPromptLen {
		prompt = prompt[:maxPromptLen]
	}

	// ROUTED PATH (#48): a run with a chosen target is ENQUEUED as a durable task
	// addressed to that machine and executed THERE, never in the cloud sandbox. It
	// carries no credential (the machine uses its own), so it branches BEFORE the
	// credential gate; everything below — the local sandbox path — is unchanged.
	if strings.TrimSpace(req.TargetID) != "" {
		return d.routed(ctx, req, org, repo, prompt, res)
	}

	if strings.TrimSpace(req.CredToken) == "" {
		res.Error = "no agent credential for this org"
		return res
	}
	cloneURL := ""
	if d.CloneURL != nil {
		cloneURL = d.CloneURL(org, repo)
	}
	if cloneURL == "" {
		res.Error = "git is not available"
		return res
	}

	agentRef := strings.TrimSpace(req.AgentRef)
	if agentRef == "" {
		agentRef = "hanzo"
	}
	actor := strings.TrimSpace(req.UserID)

	// 1. Register the live session (the durable record + live stream root).
	sessionID, err := d.Sessions.Open(ctx, org, actor, agentRef, codingTitle(repo, prompt))
	if err != nil {
		res.Error = "could not start a session: " + err.Error()
		return res
	}
	res.SessionID = sessionID
	branch := "agent/" + shortID(sessionID)
	res.Branch = branch

	// Terminal bookkeeping (final status mirror, session close, PR row) runs on a
	// cancel-immune context: a run that hit its deadline still transitions the
	// session out of "running" and files its PR, instead of leaving a zombie
	// (same discipline receive-pack uses for its post-push side effects).
	term := context.WithoutCancel(ctx)

	d.mirror(ctx, org, sessionID, actor, kindStatus, map[string]any{
		"status": "started", "repo": repo, "branch": branch, "base": baseOr(req.Base),
	})

	// 2. Dispatch to the sandbox runtime, mirroring every progress line live.
	runReq := RunRequest{
		CloneURL: cloneURL, BaseBranch: strings.TrimSpace(req.Base), Branch: branch,
		Prompt: prompt, SessionID: sessionID, RunTimeoutSeconds: timeoutOr(req.TimeoutSeconds),
		CredUser: req.CredUser, CredToken: req.CredToken,
	}
	onStep := func(s Step) {
		kind := kindLog
		if s.Type == "step" {
			kind = kindToolCall
		}
		d.mirror(ctx, org, sessionID, actor, kind, map[string]any{
			"step": s.Step, "message": s.Message, "status": s.Status,
		})
	}
	runRes, rerr := d.Runner.Run(ctx, org, req.UserID, runReq, onStep)
	if rerr != nil {
		return d.fail(term, org, sessionID, actor, res, "coding run failed: "+rerr.Error(), "")
	}
	res.Diffstat = runRes.Diffstat
	res.Changed = runRes.Changed
	res.LogTail = runRes.LogTail
	if runRes.Branch != "" {
		res.Branch = runRes.Branch
		branch = runRes.Branch
	}
	res.CommitSha = runRes.CommitSha

	if !runRes.OK {
		return d.fail(term, org, sessionID, actor, res, nonEmpty(runRes.Error, "coding run reported failure"), runRes.LogTail)
	}

	// 3. No changes is a legitimate, non-error outcome — nothing to PR.
	if !runRes.Changed {
		d.mirror(term, org, sessionID, actor, kindStatus, map[string]any{"status": "done", "changed": false})
		_ = d.Sessions.Close(term, org, sessionID, statusDone)
		res.OK = true
		return res
	}

	// 4+5. Verify the branch landed and file the PR — the SAME completion a routed
	// run's terminal report runs (completeChanged), so cloud-side integrity + the PR
	// row are identical whether the run executed in the sandbox or on a machine.
	return d.completeChanged(term, completion{
		org: org, repo: repo, project: strings.TrimSpace(req.Project), base: req.Base,
		prompt: prompt, sessionID: sessionID, branch: branch, actor: actor, agentRef: agentRef,
		diffstat: runRes.Diffstat, logTail: runRes.LogTail,
	}, res)
}

// completion bundles the run context the shared changed-run completion needs, so the
// local sandbox path and the routed path hand it the same values.
type completion struct {
	org, repo, project, base  string
	prompt, sessionID, branch string
	actor, agentRef           string
	diffstat, logTail         string
}

// completeChanged is the shared terminal for a run that reported CHANGES: confirm the
// pushed branch LANDED in native git (integrity — trust the tips we can read, not a
// self-report), open the native PR work item, mirror the done status, and close the
// session done. Fail-closed: when the verify seam is wired and the ref is absent, the
// session closes ERROR and NO PR is filed. A tracker failure is recorded but does not
// fail the run (the branch is pushed + verified). ctx is the cancel-immune terminal
// context. Used by the local path (Run) and the routed completion (finalizeRouted).
func (d Dispatcher) completeChanged(ctx context.Context, c completion, res Result) Result {
	if d.VerifyRef != nil {
		sha, ok := d.VerifyRef(ctx, c.org, c.repo, c.branch)
		if !ok {
			return d.fail(ctx, c.org, c.sessionID, c.actor, res,
				"pushed branch "+c.branch+" was not found in native git", c.logTail)
		}
		res.Verified = true
		if sha != "" {
			res.CommitSha = sha // authoritative tip from our own storage
		}
	}
	pr, perr := d.Tracker.CreatePR(ctx, PRInput{
		Org: c.org, Project: strings.TrimSpace(c.project), Repo: c.repo,
		Base: baseOr(c.base), Head: c.branch, Title: codingTitle(c.repo, c.prompt),
		Body: prBody(c.prompt, c.base, c.branch, res.CommitSha, c.diffstat, c.sessionID), Assignee: c.agentRef,
	})
	if perr != nil {
		d.logf("coding: tracker PR create failed", "org", c.org, "repo", c.repo, "err", perr)
		d.mirror(ctx, c.org, c.sessionID, c.actor, kindLog, map[string]any{"message": "tracker PR not created: " + perr.Error()})
	} else {
		res.PR = pr
	}
	d.mirror(ctx, c.org, c.sessionID, c.actor, kindStatus, map[string]any{
		"status": "done", "changed": true, "branch": c.branch, "commit": res.CommitSha, "pr": pr.Identifier,
	})
	_ = d.Sessions.Close(ctx, c.org, c.sessionID, statusDone)
	res.OK = true
	return res
}

// finalizeRouted is the CLOUD-SIDE completion for a routed run whose machine reported
// a terminal result. The machine pushed with its OWN credential and streamed into the
// session; cloud still owns the integrity gate + the PR row + the session's terminal
// state (the machine never closes the session), exactly as the local keystone path
// does after a sandbox push. No secret crosses — cloud only reads the ref it can see.
//
//   - reported failure        -> session closed ERROR (no PR).
//   - reported no changes      -> session closed DONE  (no PR).
//   - reported a changed push  -> completeChanged: VerifyRef the branch LANDED (fail
//     closed to a session ERROR + no PR if absent), file
//     the native PR, close DONE — the shared path.
//
// Best-effort + cancel-immune: it runs on its own terminal context so a run near its
// deadline still transitions out of "running". It is invoked from the durable delivery
// activity once, after the report is in hand, so it never re-executes the run.
func (d Dispatcher) finalizeRouted(ctx context.Context, in RoutedRun, res RoutedResult) {
	if d.Sessions == nil {
		return
	}
	out := Result{SessionID: in.SessionID, Repo: in.Repo, Routed: true, TargetID: in.TargetID}
	if !res.OK {
		d.fail(ctx, in.Org, in.SessionID, in.Actor, out, nonEmpty(res.Error, "the routed run reported failure"), "")
		return
	}
	if !res.Changed {
		d.mirror(ctx, in.Org, in.SessionID, in.Actor, kindStatus, map[string]any{"status": "done", "changed": false})
		_ = d.Sessions.Close(ctx, in.Org, in.SessionID, statusDone)
		return
	}
	branch := strings.TrimSpace(res.Branch)
	if branch == "" {
		branch = in.Branch
	}
	out.Branch = branch
	out.CommitSha = res.CommitSha
	out.Changed = true
	agentRef := strings.TrimSpace(in.AgentRef)
	if agentRef == "" {
		agentRef = "hanzo"
	}
	_ = d.completeChanged(ctx, completion{
		org: in.Org, repo: in.Repo, project: in.Project, base: in.Base,
		prompt: in.Prompt, sessionID: in.SessionID, branch: branch, actor: in.Actor, agentRef: agentRef,
		diffstat: res.Diffstat, logTail: "",
	}, out)
}

// RoutedResult mirrors agents.RoutedResult so coding.go stays free of an agents import
// on the completion path (the adapter bridges). It is the terminal a machine reports.
type RoutedResult struct {
	OK        bool
	Changed   bool
	Branch    string
	CommitSha string
	Diffstat  string
	Error     string
}

// routed dispatches one run to a chosen target machine (#48). It opens the live
// session tagged with the target (so mission-control shows it on that machine),
// enqueues a DURABLE task addressed to the target on the tasks engine, and
// returns immediately — the machine claims and executes it, streaming the
// terminal outcome into the SAME session. It never runs locally: an unavailable
// target, a disabled router, or a failed enqueue FAILS CLOSED with an honest
// error.
func (d Dispatcher) routed(ctx context.Context, req Req, org, repo, prompt string, res Result) Result {
	target := strings.TrimSpace(req.TargetID)
	res.Routed = true
	res.TargetID = target

	// Routing must be wired (composition root binds both). A half-wired dispatcher
	// must not fall through to the sandbox with a target set.
	if d.Route == nil || d.TargetGate == nil {
		res.Error = "routing is not available"
		return res
	}

	// The machine clones the org's repo with its OWN credential; we still need the
	// clone URL (non-secret) to hand it.
	cloneURL := ""
	if d.CloneURL != nil {
		cloneURL = d.CloneURL(org, repo)
	}
	if cloneURL == "" {
		res.Error = "git is not available"
		return res
	}

	// Liveness + existence gate: only dispatch to a target that exists in this
	// org, is online, and has a live runner. Fail closed — never elsewhere.
	if err := d.TargetGate(ctx, org, target); err != nil {
		res.Error = "target " + target + " is not available: " + err.Error()
		return res
	}

	agentRef := strings.TrimSpace(req.AgentRef)
	if agentRef == "" {
		agentRef = "hanzo"
	}
	actor := strings.TrimSpace(req.UserID)

	sessionID, err := d.Sessions.OpenOn(ctx, org, actor, agentRef, codingTitle(repo, prompt), target)
	if err != nil {
		res.Error = "could not start a session: " + err.Error()
		return res
	}
	res.SessionID = sessionID
	branch := "agent/" + shortID(sessionID)
	res.Branch = branch

	d.mirror(ctx, org, sessionID, actor, kindStatus, map[string]any{
		"status": "routed", "repo": repo, "branch": branch, "base": baseOr(req.Base), "target": target,
	})

	run := RoutedRun{
		Org: org, TargetID: target, SessionID: sessionID,
		Repo: repo, Project: strings.TrimSpace(req.Project), Base: strings.TrimSpace(req.Base),
		Branch: branch, Prompt: prompt, CloneURL: cloneURL, TimeoutSeconds: timeoutOr(req.TimeoutSeconds),
		Actor: actor, AgentRef: agentRef,
	}
	// Enqueue on the durable engine. A failure fails the run closed (session
	// error) rather than leaving a zombie "running" session or running locally.
	if err := d.Route(ctx, run); err != nil {
		term := context.WithoutCancel(ctx)
		return d.fail(term, org, sessionID, actor, res, "could not queue the routed run: "+err.Error(), "")
	}

	res.OK = true // accepted + queued; the machine drives it to terminal from here
	return res
}

// fail records the error into the session, closes it error, and stamps the Result.
func (d Dispatcher) fail(ctx context.Context, org, sessionID, actor string, res Result, msg, logTail string) Result {
	d.mirror(ctx, org, sessionID, actor, kindStatus, map[string]any{"status": "error", "error": msg})
	_ = d.Sessions.Close(ctx, org, sessionID, statusError)
	res.OK = false
	res.Error = msg
	if logTail != "" {
		res.LogTail = logTail
	}
	return res
}

// mirror appends one event to the session, best-effort (a live-fan-out failure
// must never fail the run — the run + its side effects already happened). The
// credential is never a field of any payload passed here.
func (d Dispatcher) mirror(ctx context.Context, org, sessionID, actor, kind string, payload map[string]any) {
	if d.Sessions == nil {
		return
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if err := d.Sessions.Log(ctx, org, sessionID, kind, actor, b); err != nil {
		d.logf("coding: session event mirror failed", "org", org, "session", sessionID, "err", err)
	}
}

func (d Dispatcher) logf(msg string, kv ...any) {
	if d.Log != nil {
		d.Log(msg, kv...)
	}
}

// ---- pure helpers ----

func codingTitle(repo, prompt string) string {
	p := firstLine(prompt)
	if len(p) > maxTitlePrompt {
		p = strings.TrimSpace(p[:maxTitlePrompt]) + "…"
	}
	if p == "" {
		return "Agent changes to " + repo
	}
	return repo + ": " + p
}

// prBody renders the PR description: the task, the base..head, the tip, a
// diffstat, and the session link so a reviewer can open the live run.
func prBody(prompt, base, branch, commit, diffstat, sessionID string) string {
	var b strings.Builder
	b.WriteString("Opened by the @hanzo coding agent.\n\n")
	b.WriteString("**Task:** " + firstLine(prompt) + "\n\n")
	b.WriteString("**Branch:** `" + branch + "` → base `" + baseOr(base) + "`\n")
	if commit != "" {
		b.WriteString("**Commit:** `" + commit + "`\n")
	}
	if strings.TrimSpace(diffstat) != "" {
		b.WriteString("\n```\n" + strings.TrimSpace(diffstat) + "\n```\n")
	}
	b.WriteString("\nSession: `/v1/agents/sessions/" + sessionID + "`\n")
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// shortID returns the last 12 hex chars of a "sess_<hex>" id for a stable, unique
// branch suffix. Falls back to the whole id when it is short/unprefixed.
func shortID(id string) string {
	if i := strings.LastIndexByte(id, '_'); i >= 0 && i+1 < len(id) {
		id = id[i+1:]
	}
	if len(id) > 12 {
		return id[len(id)-12:]
	}
	return id
}

func baseOr(base string) string {
	if b := strings.TrimSpace(base); b != "" {
		return b
	}
	return "main"
}

func timeoutOr(s int) int {
	if s > 0 {
		return s
	}
	return defaultTimeoutSeconds
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
