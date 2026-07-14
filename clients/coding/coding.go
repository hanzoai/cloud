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
		return d.fail(ctx, org, sessionID, actor, res, "coding run failed: "+rerr.Error(), "")
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
		return d.fail(ctx, org, sessionID, actor, res, nonEmpty(runRes.Error, "coding run reported failure"), runRes.LogTail)
	}

	// 3. No changes is a legitimate, non-error outcome — nothing to PR.
	if !runRes.Changed {
		d.mirror(ctx, org, sessionID, actor, kindStatus, map[string]any{"status": "done", "changed": false})
		_ = d.Sessions.Close(ctx, org, sessionID, statusDone)
		res.OK = true
		return res
	}

	// 4. Independently confirm the branch LANDED in native git (integrity: trust
	// the branch tips we can read, not the runner's self-report). When the verify
	// seam is wired and the ref is absent, fail closed.
	if d.VerifyRef != nil {
		sha, ok := d.VerifyRef(ctx, org, repo, branch)
		if !ok {
			return d.fail(ctx, org, sessionID, actor, res,
				"pushed branch "+branch+" was not found in native git", runRes.LogTail)
		}
		res.Verified = true
		if sha != "" {
			res.CommitSha = sha // authoritative tip from our own storage
		}
	}

	// 5. Open the native PR work item (Kind:pr, Source:agent). A tracker failure
	// does NOT fail the run — the branch is pushed and verified; the PR row is a
	// side-effect — but it is recorded.
	pr, perr := d.Tracker.CreatePR(ctx, PRInput{
		Org: org, Project: strings.TrimSpace(req.Project), Repo: repo,
		Base: baseOr(req.Base), Head: branch, Title: codingTitle(repo, prompt),
		Body: prBody(prompt, req.Base, branch, res.CommitSha, runRes.Diffstat, sessionID), Assignee: agentRef,
	})
	if perr != nil {
		d.logf("coding: tracker PR create failed", "org", org, "repo", repo, "err", perr)
		d.mirror(ctx, org, sessionID, actor, kindLog, map[string]any{"message": "tracker PR not created: " + perr.Error()})
	} else {
		res.PR = pr
	}

	d.mirror(ctx, org, sessionID, actor, kindStatus, map[string]any{
		"status": "done", "changed": true, "branch": branch, "commit": res.CommitSha, "pr": pr.Identifier,
	})
	_ = d.Sessions.Close(ctx, org, sessionID, statusDone)
	res.OK = true
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
