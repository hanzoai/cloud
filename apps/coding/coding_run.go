package coding

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/zap-proto/zip"
)

func (d Dispatcher) Run(ctx context.Context, req Req) Result {
	org := strings.TrimSpace(req.Org)
	repo := strings.TrimSpace(req.Repo)
	prompt := strings.TrimSpace(req.Prompt)
	res := Result{Repo: repo}

	// Boundary validation (fail closed): a coding run needs a tenant and a task,
	// and a run that pushes needs a credential to reach git. A run on no
	// repository works in an empty workspace.
	if org == "" {
		res.Error = "org is required"
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

	// ONE check, because there is now one fact. The grant IS the remote and the
	// credential: Start resolved them together, so a run that pushes has a
	// repository it can push to, and one that keeps its work local needs neither.
	if !req.Local && (strings.TrimSpace(req.Remote) == "" || strings.TrimSpace(req.Key) == "") {
		res.Error = "git is not available"
		return res
	}

	agentRef := strings.TrimSpace(req.AgentRef)
	if agentRef == "" {
		agentRef = "hanzo"
	}
	actor := strings.TrimSpace(req.UserID)

	// 1. Register the live session (the durable record + live stream root), or
	// adopt the one the entry point already opened to answer its caller with.
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		var err error
		sessionID, err = d.Sessions.Open(ctx, org, actor, agentRef, codingTitle(repo, prompt))
		if err != nil {
			res.Error = "could not start a session: " + err.Error()
			return res
		}
	}
	res.SessionID = sessionID
	branch := cmp.Or(req.Branch, BranchFor(sessionID))
	res.Branch = branch
	d = d.tracking(org, sessionID, req, repo, prompt)

	// Terminal bookkeeping (final status mirror, session close, PR row) runs on a
	// cancel-immune context: a run that hit its deadline still transitions the
	// session out of "running" and files its PR, instead of leaving a zombie
	// (same discipline receive-pack uses for its post-push side effects).
	term := context.WithoutCancel(ctx)

	// The person's whole ask, as they wrote it, opens the record. The title is its
	// first line (headline) and nothing else said the rest: the run page drew the
	// title as the person's words and Try again resubmitted it, so an ask whose
	// link sat on its second line came back as "check this pr -" with no link,
	// and the agent asked which pull request.
	switch {
	case req.Parked:
		// The session is the one the run was parked in: its task is already its first
		// message, so the run says only that it goes on.
		continued := map[string]any{"status": statusContinued, "repo": repo, "base": baseOr(req.Base), "mode": modeOf(req.Mode), "trigger": req.Trigger}
		if !answers(req.Mode) {
			continued["branch"] = branch
		}
		d.mirror(ctx, org, sessionID, actor, kindStatus, continued)
		d.report(ctx, org, sessionID, actor, phaseRunning, "continuing after a usage limit")
	case req.Resumed:
		resumed := map[string]any{"status": "resumed", "repo": repo, "base": baseOr(req.Base), "mode": modeOf(req.Mode), "sandbox": req.Sandbox}
		if !answers(req.Mode) {
			resumed["branch"] = branch
		}
		d.mirror(ctx, org, sessionID, actor, kindStatus, resumed)
		d.report(ctx, org, sessionID, actor, phaseRunning, "continuing in sandbox after platform update")
	default:
		d.mirror(ctx, org, sessionID, actor, kindMessage, map[string]any{"role": "user", "text": prompt})

		// A plan pushes no branch, so its start names none.
		started := map[string]any{"status": "started", "repo": repo, "base": baseOr(req.Base), "mode": modeOf(req.Mode)}
		if !answers(req.Mode) {
			started["branch"] = branch
		}
		if req.Tool != "" {
			started["tool"] = req.Tool
		}
		if req.Model != "" {
			started["model"] = req.Model
		}
		if req.Effort != "" {
			started["effort"] = req.Effort
		}
		if req.Desktop {
			started["desktop"] = true
		}
		if req.Issue != "" {
			started["issue"] = req.Issue
		}
		d.mirror(ctx, org, sessionID, actor, kindStatus, started)
		d.report(ctx, org, sessionID, actor, phaseRunning, "starting")
	}

	// The codebase's environment, resolved on the run's own context — which acts
	// for this org, and is the only authority KMS opens a tenant's value to. A
	// secret that cannot be read fails the run rather than starting its agent
	// without a credential the codebase was set up to have.
	//
	// An environment is keyed by the forge's name for the codebase, so a run on a
	// repository GitHub alone holds — whose name is owner/name — carries none.
	var prepared Prepared
	if d.Environment != nil && RepoRE.MatchString(repo) {
		var err error
		if prepared, err = d.Environment(ctx, org, repo, req.Mode == modeSetup); err != nil {
			return d.fail(term, org, sessionID, actor, res, err.Error(), "")
		}
	}
	// The org's skills and MCP servers, resolved the same way and for the same
	// reason: an agent started without what its org set up for it would report
	// failures that are not its own. An interpreter has no harness to hand them to.
	var kit Kit
	if d.Equip != nil && agentic(req.Tool) {
		var err error
		if kit, err = d.Equip(ctx, actor); err != nil {
			return d.fail(term, org, sessionID, actor, res, err.Error(), "")
		}
		// The run's ticket opens exactly these servers at the relay (steer.go).
		stow(req.Ticket, kit)
	}

	// 2. Dispatch to the sandbox runtime, mirroring every progress line live.
	runReq := RunRequest{
		Tool: req.Tool, Desktop: req.Desktop,
		Remote: req.Remote, BaseBranch: strings.TrimSpace(cmp.Or(req.From, req.Base)), Branch: branch,
		Prompt: prompt, SessionID: sessionID, RunTimeoutSeconds: timeoutOr(req.TimeoutSeconds),
		Key: req.Key, Known: req.Known, RenewKey: req.RenewKey, ReadKey: req.ReadKey, RenewRead: req.RenewRead, Ticket: req.Ticket,
		Plan: answers(req.Mode), Setup: req.Mode == modeSetup, Model: req.Model, Effort: req.Effort,
		Prepared: prepared, Instructions: req.Instructions, DeveloperInstructions: req.DeveloperInstructions,
		Kit: kit, OnTurnSteer: req.OnTurnSteer, Author: req.Author, Site: req.Site,
		Sandbox: req.Sandbox, Resumed: req.Resumed, Local: req.Local, Parked: req.Parked,
	}
	onStep := func(s Step) {
		// A BEAT IS PROGRESS AND NOTHING ELSE: what a long turn is doing, said once
		// a minute (sandboxrunner.go beating). It moves the session's progress line
		// and its time, and adds no step to the transcript or the thread's message.
		if s.Type == "beat" {
			d.report(ctx, org, sessionID, actor, phaseRunning, s.Message)
			return
		}
		kind := kindLog
		if s.Type == "step" {
			kind = kindToolCall
		}
		payload := map[string]any{"step": s.Step, "message": s.Message, "status": s.Status}
		if s.Sandbox != "" {
			// THE SESSION NAMES ITS SANDBOX, as well as the step saying it: a view
			// that shows the run's terminal or its screen reads the id off the
			// session, never out of a sentence in its transcript.
			payload["sandbox"], payload["class"] = s.Sandbox, s.Class
			if err := d.Sessions.Tag(ctx, org, sessionID, Tag{Sandbox: s.Sandbox}); err != nil {
				d.logf("coding: the session was not told its sandbox", "org", org, "session", sessionID, "err", err)
			}
		}
		d.mirror(ctx, org, sessionID, actor, kind, payload)
		if s.Type == "step" && s.Message != "" {
			d.report(ctx, org, sessionID, actor, phaseRunning, s.Message)
		}
	}
	runRes, rerr := d.Runner.Run(ctx, org, req.UserID, runReq, onStep)
	// WHAT THE RUN LEFT IS RECORDED BESIDE IT whatever way it ended, with where its
	// work went once that is known (artifact.go).
	record := func(pr, live string) {
		ix := index{Repo: repo, Base: baseOr(req.Base), Commits: runRes.Commits}
		if !answers(req.Mode) {
			ix.Head = branch
		}
		ix.Artifacts = append(runRes.Artifacts, links(pr, live)...)
		if err := saveIndex(term, org, sessionID, ix); err != nil {
			d.logf("coding: the run's artifacts were not indexed", "org", org, "session", sessionID, "err", err)
			return
		}
		// What the run made goes to its person's Library, after its end is
		// recorded and without holding it up (library.go).
		go d.shelve(term, org, actor, sessionID, ix)
	}
	cause := context.Cause(ctx)
	if runRes.TimeUp && !kept(cause) {
		cause = errTimeUp // the runner held the run to its lease (sandboxrunner.go leaseMargin)
	}
	if runRes.Unrenewable && !kept(cause) {
		cause = errUnrenewable // the runner held the run to the credential its harness holds (key.go)
	}
	// A deadline that fired while the runner was delivering is not "kept": the push
	// it was making is what it aborted. Only a runner that kept the work says so.
	if kept(cause) && runRes.Park == nil && (runRes.TimeUp || !errors.Is(cause, errTimeUp)) {
		if answers(req.Mode) {
			branch = "" // a plan or a setup kept nothing, on no branch
		}
		record("", "")
		return d.interrupted(term, org, sessionID, actor, res, runRes, branch, cause)
	}
	// A RUN THE MODEL API REFUSED waits for that to clear (park.go). A run that went on
	// from a park and was refused before its harness began (the pre-flight or the lease)
	// waits again, for the reason it was refused.
	if runRes.Park == nil && req.Parked && refusedAdmission(rerr) {
		reason, refused := refusedReason(rerr)
		if refused && reason == agents.ParkLimit && !standsFor(ctx, reason).holds {
			// A 429 from a lease is the model API's usage limit only if the authority that makes
			// them says one stands. Otherwise it is the sandbox app's own (the org's running
			// sandboxes are at their quota), which no credits and no window reopening clear.
			refused = false
		}
		if refused {
			runRes.Park = &Parking{Reason: reason, At: clearsAt(ctx, reason)}
		} else {
			// Nobody refused it: no capacity, a timeout, an app that is rolling. It is parked as it
			// was, for the time its next attempt is due, and says what failed.
			at := within(time.Now().Add(retryAfter(req.Tries+1)).Unix(), chainStart(req.Chain))
			runRes.Park = &Parking{Reason: cmp.Or(req.Reason, agents.ParkLimit), Retry: true, Note: firstLine(rerr.Error())}
			if at > 0 {
				runRes.Park.At = time.Unix(at, 0)
			}
		}
		runRes.Changed, runRes.CommitSha, runRes.Diffstat, runRes.Subject = req.Kept.Commit != "", req.Kept.Commit, req.Kept.Diffstat, req.Kept.Subject
		rerr = nil
	}
	if runRes.Park != nil {
		record("", "")
		return d.parked(term, org, sessionID, actor, res, runRes, branch, req)
	}
	if rerr != nil {
		return d.fail(term, org, sessionID, actor, res, rerr.Error(), "")
	}
	res.Diffstat = runRes.Diffstat
	res.Changed = runRes.Changed
	res.LogTail = runRes.LogTail
	// runRes.Branch is NOT read. The branch is the one Start issued (AgentBranch,
	// or BranchFor(sessionID)) — cloud's, never one the sandbox chose — and adopting the
	// sandbox's self-report let a compromised one answer `main`, which then flowed
	// into VerifyRef (which only asks whether a ref EXISTS, and main does) and out
	// as PR.Open{Head: "main"}. A PR headed at the trunk, filed by us, from a run
	// that never had permission to write there.
	//
	// The sandbox has nothing to report here: it was TOLD which branch to push,
	// and its grant admits that one ref and no other, so a disagreement between
	// what it claims and what we issued is not new information — it is the
	// signal that something is wrong.
	res.CommitSha = runRes.CommitSha

	// A run that went on from a park and added nothing more has still left its work on the
	// branch, where the park put it: it is done with that work, not with nothing.
	if runRes.OK && !runRes.Changed && req.Parked && req.Kept.Commit != "" && !answers(req.Mode) && !req.Local {
		runRes.Changed, runRes.CommitSha, runRes.Diffstat, runRes.Subject = true, req.Kept.Commit, req.Kept.Diffstat, cmp.Or(runRes.Subject, req.Kept.Subject)
		res.Changed, res.CommitSha, res.Diffstat = true, runRes.CommitSha, runRes.Diffstat
	}

	if !runRes.OK {
		record("", "")
		res.Terminated = runRes.Terminated
		return d.fail(term, org, sessionID, actor, res, nonEmpty(runRes.Error, "coding run reported failure"), runRes.LogTail)
	}

	// A PLAN ENDS WITH ITS ANSWER. It was handed a credential that can only read
	// and its runner delivered nothing, so there is no branch to verify and no
	// proposal to file: the plan is the result, and it is the terminal status.
	//
	// A SETUP ends the same way, and its answer's environment block is kept as the
	// codebase's proposal. A proposal that could not be kept is said beside the
	// answer rather than failing a run whose work — the answer — is done.
	if answers(req.Mode) {
		done := map[string]any{"status": "done", "mode": req.Mode, "changed": false, "plan": runRes.Plan}
		if req.Mode == modeSetup {
			done["environment"] = nil
			if d.Suggest != nil {
				if kept, err := d.Suggest(term, org, repo, sessionID, runRes.Plan); err != nil {
					if d.Log != nil {
						d.Log("coding: a setup run's environment was not kept", "org", org, "repo", repo, "session", sessionID, "err", err)
					}
				} else {
					done["environment"] = kept
				}
			}
		}
		d.mirror(term, org, sessionID, actor, kindStatus, done)
		_ = d.Sessions.Close(term, org, sessionID, statusDone)
		res.OK = true
		return res
	}

	// A BUILD THAT DID NOTHING IS NOT DONE when the run can say why (hollow): it
	// had nothing to work on, or its agent stopped mid-task. Reported done, it
	// read as a success with no work in it.
	if why := hollow(req, runRes); why != "" {
		record("", "")
		return d.fail(term, org, sessionID, actor, res, why, runRes.LogTail)
	}

	// 3. No changes is a legitimate, non-error outcome — nothing to PR. A local
	// run's change stays in its workspace, committed there, and nothing proposes
	// it: it is kept as its artifacts.
	if !runRes.Changed || req.Local {
		record("", "")
		d.mirror(term, org, sessionID, actor, kindStatus, finished(runRes, map[string]any{
			"status": "done", "changed": runRes.Changed, "local": req.Local, "commit": runRes.CommitSha}))
		_ = d.Sessions.Close(term, org, sessionID, statusDone)
		res.OK = true
		return res
	}

	// 4+5. Verify the branch landed and file the PR — the SAME completion a routed
	// run's terminal report runs (completeChanged), so cloud-side integrity + the PR
	// row are identical whether the run executed in the sandbox or on a machine.
	res = d.completeChanged(term, completion{
		org: org, repo: repo, project: strings.TrimSpace(req.Project), base: req.Base,
		prompt: prompt, sessionID: sessionID, branch: branch, actor: actor, agentRef: agentRef,
		forgeActor: req.Actor,
		diffstat:   runRes.Diffstat, logTail: runRes.LogTail, subject: runRes.Subject,
		issue: req.Issue, live: runRes.Live, unpublished: runRes.Unpublished, run: runRes,
		pull: req.Pull, proposal: req.Proposal,
	}, res)
	record(res.PR.URL, runRes.Live)
	return res
}

// hollow is why a build that changed nothing and kept nothing failed, or "" when
// it may be done: an agent that read a real checkout and answered that nothing
// needed changing is done.
//
//	EMPTY        its checkout held no code, so whatever the task was, it was
//	             not about this repository. Measured: `cloud` resolved to a
//	             one-README placeholder, the agent answered "This is a fresh
//	             repository", and the run closed done with 0 artifacts.
//	UNANSWERED   its agent ended the turn with no last message. What it said
//	             last was an announcement ("I'll create the specification…")
//	             of work it never did.
//	UNACTED      its agent ended on such an announcement as its last message,
//	             and asked to make the change or say none was needed, did
//	             neither (hanzo-inc/cloud#451).
func hollow(req Req, r RunResult) string {
	if r.Changed {
		return ""
	}
	switch {
	case r.Empty && !req.Made && len(r.Artifacts) == 0:
		return fmt.Sprintf("the run changed nothing: %s holds no code, only a README or a licence, so it is not the repository "+
			"this task is about. Name the repository by its GitHub address, github.com/<owner>/<name>", cmp.Or(req.Repo, "the repository"))
	case r.Unacted && outOfBudget(r.Answer):
		// Asking a conversation with no budget left again gets the same sentence.
		return "the agent's conversation has used its token budget, so it wrapped up without doing the work; " +
			"start a new run from this branch rather than continuing this one. The last thing it said was: " + lead(r.Answer, 400)
	case r.Unacted && strings.TrimSpace(r.Answer) != "" && req.From != "":
		return "the agent said what it would do and added nothing to the work already on " + req.From +
			", even when asked to finish; the last thing it said was: " + lead(r.Answer, 400)
	case r.Unacted && strings.TrimSpace(r.Answer) != "":
		return "the agent said what it would do and changed nothing, even when asked to make the change or say none was needed; " +
			"the last thing it said was: " + lead(r.Answer, 400)
	case r.Unacted:
		return "the agent changed nothing, even when asked to make the change or say none was needed"
	case r.Unanswered:
		// A run that started on its own branch had work in it already (req.From),
		// so it did not change nothing: it added nothing to that work (#553).
		why := "the agent stopped mid-task without a final answer and changed nothing"
		if req.From != "" {
			why = "the agent stopped mid-task without a final answer and added nothing to the work already on " + req.From
		}
		if strings.TrimSpace(r.Answer) != "" {
			why += "; the last thing it said was: " + lead(r.Answer, 400)
		}
		return why
	}
	return ""
}

// finished is a run's terminal status with what its harness said: its last
// message, the tokens it used, and how many artifacts it left.
func finished(r RunResult, status map[string]any) map[string]any {
	if r.Answer != "" {
		status["answer"] = r.Answer
	}
	if r.Usage != (Usage{}) {
		status["usage"] = r.Usage
	}
	status["artifacts"] = len(r.Artifacts)
	return status
}

// completion bundles the run context the shared changed-run completion needs, so the
// local sandbox path and the routed path hand it the same values.
type completion struct {
	org, repo, project, base  string
	prompt, sessionID, branch string
	actor, agentRef           string
	// forgeActor is the login the proposal is opened as. It is separate from
	// actor, which is the attributed subject the session records — one is who the
	// work is FOR, the other is who the forge acts AS, and they are only the same
	// string when the caller's subject happens to be their forge login.
	forgeActor                 string
	diffstat, logTail, subject string
	issue                      string
	// live is where the run's work was published, and unpublished why a run
	// with a site did not publish it.
	live, unpublished string
	// run is what the sandbox reported, for the terminal status.
	run RunResult
	// pull is a run asked to open a pull request for what it pushed. Unset, the
	// branch is pushed and verified and nothing is proposed.
	pull bool
	// proposal is how that pull request is opened (Req.Proposal).
	proposal Proposal
}

// completeChanged is the shared terminal for a run that reported CHANGES: confirm the
// pushed branch LANDED in native git (integrity — trust the tips we can read, not a
// self-report), open the native PR work item, mirror the done status, and close the
// session done. Fail-closed: when the verify client is wired and the ref is absent, the
// session closes ERROR and NO PR is filed. A todo failure is recorded but does not
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
	// A run not asked to open a pull request ends with its branch pushed and verified.
	var pr PRRef
	var perr error
	if c.pull {
		pr, perr = d.PR.Open(ctx, PRInput{
			Org: c.org, Actor: c.forgeActor, Project: strings.TrimSpace(c.project), Repo: c.repo,
			Base: baseOr(c.base), Head: c.branch, Title: codingTitle(c.repo, cmp.Or(c.subject, c.prompt)),
			Body: prBody(c.prompt, c.base, c.branch, res.CommitSha, c.diffstat, c.sessionID, c.forgeActor, c.issue, c.run.Commits...), Assignee: c.agentRef,
			Draft: c.proposal.Draft, Assignees: c.proposal.Assignees, Reviewers: c.proposal.Reviewers,
		})
	}
	// Whatever came back is kept, even beside an error: opening a PR is two acts
	// in two places, and a run that filed its work item but could not reach the
	// forge has a real handle to report. Discarding it would hide the half that
	// worked.
	res.PR = pr
	done := finished(c.run, map[string]any{
		"status": "done", "changed": true, "branch": c.branch,
		"commit": res.CommitSha, "pr": pr.Identifier, "url": pr.URL,
	})
	if pr.URL != "" {
		// The session says where the proposal is read, for a board that lists runs
		// rather than reading their transcripts.
		if err := d.Sessions.Tag(ctx, c.org, c.sessionID, Tag{PR: pr.URL}); err != nil {
			d.logf("coding: the session was not told its pull request", "org", c.org, "session", c.sessionID, "err", err)
		}
	}
	if c.live != "" {
		done["live"] = c.live
	}
	if c.unpublished != "" {
		done["unpublished"] = c.unpublished
	}
	if perr != nil {
		// ON THE TERMINAL STATUS, not beside it. The branch is pushed and the run
		// is done, but the person who asked for a pull request did not get one, and
		// the one event every reader waits for is the place that has to say so.
		d.logf("coding: PR open failed", "org", c.org, "repo", c.repo, "err", perr)
		done["prError"] = perr.Error()
	}
	d.mirror(ctx, c.org, c.sessionID, c.actor, kindStatus, done)
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
	// The durable engine ends a routed run on a context that names nobody, and the
	// session ops refuse a call with no org: the completion acts as the run's person.
	ctx = zip.WithCaller(ctx, zip.Caller{Org: in.Org, User: in.Actor})
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
	// A read-only run that reports a change did what it was told not to do: fail
	// closed to a session ERROR. No ref verification, no push, no PR, no "done" —
	// even if the machine also reported a commit sha.
	if in.ReadOnly {
		d.fail(ctx, in.Org, in.SessionID, in.Actor, out,
			"a read-only run reported changed=true; nothing was pushed or proposed", "")
		return
	}
	// res.Branch is NOT read, for the reason stated in Run: the branch is the one
	// cloud issued and put in the RoutedRun, and a machine that reports a
	// different one is reporting something it was never asked.
	branch := in.Branch
	out.Branch = branch
	out.CommitSha = res.CommitSha
	out.Changed = true
	agentRef := strings.TrimSpace(in.AgentRef)
	if agentRef == "" {
		agentRef = "hanzo"
	}
	// A run in a workspace the platform made is verified and proposed in it.
	if in.Made {
		ws, err := workspace(ctx, in.Org, false)
		if err != nil {
			d.fail(ctx, in.Org, in.SessionID, in.Actor, out, "the run's workspace could not be read: "+err.Error(), "")
			return
		}
		d.VerifyRef, d.PR = verifyIn(ws.Owner), planePR{ws: ws}
	}
	// A run on GitHub is verified and proposed THERE, on a token minted now as
	// the run's person and ended with the completion. Admitted again rather than
	// carried: the start's token died with the start, and a completion is a new
	// act on the repository. The call names the person and nothing about what they
	// may do — the mint asks their own GitHub access and IAM as they stand NOW, so
	// someone demoted since the start is minted nothing.
	if in.Home != "" {
		owner, name, _ := strings.Cut(in.Home, "/")
		h, _, _, err := admit(ctx, owner, name, false)
		if err != nil {
			d.fail(ctx, in.Org, in.SessionID, in.Actor, out,
				"GitHub would not admit the run's completion on "+in.Home+": "+err.Error(), "")
			return
		}
		defer func() {
			if err := h.revoke(context.WithoutCancel(ctx)); err != nil {
				d.logf("coding: the completion's GitHub token was not revoked", "org", in.Org, "repo", in.Home, "err", err)
			}
		}()
		d.VerifyRef, d.PR = h.landed, planePR{home: h}
	}
	_ = d.completeChanged(ctx, completion{
		org: in.Org, repo: in.Repo, project: in.Project, base: in.Base,
		prompt: in.Prompt, sessionID: in.SessionID, branch: branch, actor: in.Actor, agentRef: agentRef,
		forgeActor: in.ForgeActor,
		diffstat:   res.Diffstat, logTail: "",
		pull: in.Pull,
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
