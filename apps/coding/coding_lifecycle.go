package coding

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

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
	// clone URL (non-secret) to hand it. A run in a workspace the platform made has
	// none yet: the machine holds no credential there, and the run's own key names its
	// address when the run is offered.
	//
	// IT IS RESOLVED AS THE ACTOR. The machine's credentials are broader than the
	// caller's, so confirming the repository as a site administrator and then
	// handing the address to that machine is a confused deputy — the caller would
	// be reading a repository they have no access to, through a machine that
	// does. The address is only produced for someone who could have found it.
	//
	// A RUN ON NO REPOSITORY (Start admitted one with no `repo`) is handed no
	// address at all: the machine works in an empty directory of its own, clones
	// nothing and pushes nothing (#779). There is no repository to read on anyone's
	// behalf, so there is no actor to resolve and no branch to name.
	bare := repo == "" && strings.TrimSpace(req.Home) == ""
	cloneURL := ""
	if !bare && !req.Made {
		if strings.TrimSpace(req.Actor) == "" {
			res.Error = "git is not available"
			return res
		}
		switch {
		case req.Home != "":
			// Admitted on GitHub at Start (github.go); the machine clones the origin
			// with its own credential, as it would a forge address.
			cloneURL = "https://github.com/" + req.Home + ".git"
		case d.CloneURL != nil:
			cloneURL = d.CloneURL(ctx, org, req.Actor, repo)
		}
		if cloneURL == "" {
			res.Error = "git is not available"
			return res
		}
	}

	// Liveness + existence gate: only dispatch to a target that exists in this
	// org, is online, and has a live runner. Fail closed — never elsewhere.
	if err := d.TargetGate(ctx, org, target, req.Dir, req.ReadOnly); err != nil {
		res.Error = "target " + target + " is not available: " + err.Error()
		return res
	}

	agentRef := strings.TrimSpace(req.AgentRef)
	if agentRef == "" {
		agentRef = "hanzo"
	}
	actor := strings.TrimSpace(req.UserID)

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		var err error
		sessionID, err = d.Sessions.OpenOn(ctx, org, actor, agentRef, codingTitle(repo, prompt), target, "")
		if err != nil {
			res.Error = "could not start a session: " + err.Error()
			return res
		}
	}
	res.SessionID = sessionID
	branch := cmp.Or(req.Branch, BranchFor(sessionID))
	if bare {
		branch = ""
	}
	res.Branch = branch
	d = d.tracking(org, sessionID, req, repo, prompt)

	d.mirror(ctx, org, sessionID, actor, kindMessage, map[string]any{"role": "user", "text": prompt})
	d.mirror(ctx, org, sessionID, actor, kindStatus, map[string]any{
		"status": "routed", "repo": repo, "branch": branch, "base": baseOr(req.Base), "target": target,
	})

	run := RoutedRun{
		Org: org, TargetID: target, SessionID: sessionID,
		Repo: repo, Project: strings.TrimSpace(req.Project), Base: strings.TrimSpace(req.Base),
		Branch: branch, Prompt: prompt, CloneURL: cloneURL, TimeoutSeconds: routedTimeoutOr(req.TimeoutSeconds),
		Actor: actor, AgentRef: agentRef, ForgeActor: req.Actor, Home: req.Home,
		Model: req.Model, Effort: req.Effort, Instructions: req.Instructions,
		Pull: req.Pull, Made: req.Made, Read: answers(req.Mode) || req.ReadOnly, Dir: req.Dir,
		ReadOnly: req.ReadOnly,
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

// interrupted is how a run that was stopped, paused or redirected ends: the
// runner has kept its work on its branch, and this says where and ends the way it
// was asked to (steer.go). A paused run's session stays live — the pause already
// said so on the row — and every other one ends stopped.
func (d Dispatcher) interrupted(ctx context.Context, org, sessionID, actor string, res Result, run RunResult, branch string, cause error) Result {
	status := statusStopped
	if errors.Is(cause, agents.ErrPaused) {
		status = statusPaused
	}
	d.mirror(ctx, org, sessionID, actor, kindStatus, map[string]any{
		"status": status, "branch": branch, "changed": run.Changed, "commit": run.CommitSha,
		"followed": errors.Is(cause, errFollowed), "timeUp": errors.Is(cause, errTimeUp),
		"unrenewable": errors.Is(cause, errUnrenewable),
	})
	if status == statusStopped {
		_ = d.Sessions.Close(ctx, org, sessionID, statusStopped)
	}
	res.Changed, res.CommitSha, res.Diffstat, res.Error = run.Changed, run.CommitSha, run.Diffstat, status
	return res
}

// parked is how a run the model API refused ends when the refusal can be waited out: its
// work is kept on its branch and its sandbox is given back (the runner), and here the park
// is written in one durable step with the session's update (Park), then said where the
// session, its stream and its thread's card say what a run says: parked, why, and when it
// goes on. The session stays live, paused. A park that cannot be written ends the run
// failed, saying where its work is, as a run out of time does.
func (d Dispatcher) parked(ctx context.Context, org, sessionID, actor string, res Result, run RunResult, branch string, req Req) Result {
	p := run.Park
	var at int64
	if !p.At.IsZero() {
		at = p.At.Unix()
	}
	if p.Reason == agents.ParkFault || (p.Reason == agents.ParkLimit && p.Retry) {
		// A fault or transient rate limit is tried again after a wait that doubles
		// with each failure in a row, never earlier than any server-advised time.
		if backoff := time.Now().Add(retryAfter(req.Tries + 1)).Unix(); backoff > at {
			at = backoff
		}
	}
	// The day a run goes on by itself for is the chain's, not this park's.
	at = within(at, chainStart(req.Chain))
	say := agents.ParkSaid(p.Reason, at)
	// What the run left on its branch, else what the park it went on from had left: a run
	// refused again before it changed anything still has that work to be done with.
	kept := Kept{Commit: run.CommitSha, Diffstat: run.Diffstat, Subject: run.Subject}
	if kept.Commit == "" {
		kept = req.Kept
	}
	run.Changed = run.Changed || kept.Commit != ""
	res.Changed, res.CommitSha, res.Diffstat = run.Changed, kept.Commit, kept.Diffstat
	// A fault is counted per error: the same one again and again ends the run, with its work kept.
	var fault Fault
	switch {
	case p.Reason == agents.ParkRestart: // the pod went away: nothing new failed
		fault = req.Fault
	case p.Reason != agents.ParkFault:
	case p.Print == "": // a start of the run that could not be made: nothing new failed
		fault = req.Fault
	default:
		fault = req.Fault.next(p.Print, p.Progressed)
	}
	if fault.Count >= faultCap {
		res.Fault, res.Branch = fault, branch
		msg := fmt.Sprintf("the model API failed this run %d times in a row with the same error (%s), so it stopped trying", fault.Count, fault.Print)
		if run.Changed {
			msg += "; its work so far is on " + branch + ", and a run started with `after` set to this session goes on from it"
		}
		return d.fail(ctx, org, sessionID, actor, res, msg, run.LogTail)
	}
	if d.Park == nil {
		return d.fail(ctx, org, sessionID, actor, res, "the run's AI calls were refused ("+p.Reason+") and it cannot wait for that to clear", run.LogTail)
	}
	var tries int64
	if p.Retry {
		tries = req.Tries + 1
	}
	if err := d.Park(ctx, Parked{Reason: p.Reason, At: at, Kept: kept, Tries: tries, Chain: req.Chain, Fault: fault}); err != nil {
		if errors.Is(err, errHalted) {
			// Somebody stopped the session first: it has said so, and there is nothing to wait for.
			res.Error = statusStopped
			return res
		}
		msg := "the run was refused (" + p.Reason + ") and could not wait for that to clear: " + err.Error()
		if run.Changed {
			msg += "; its work so far is on " + branch
		}
		return d.fail(ctx, org, sessionID, actor, res, msg, run.LogTail)
	}
	status := map[string]any{"status": statusParked, "reason": p.Reason, "message": say, "changed": run.Changed, "commit": kept.Commit}
	if !answers(req.Mode) {
		status["branch"] = branch
	}
	if at > 0 {
		status["resumeAt"] = time.Unix(at, 0).UTC().Format(time.RFC3339)
	}
	if p.Note != "" {
		status["note"] = p.Note
	}
	d.mirror(ctx, org, sessionID, actor, kindStatus, status)
	d.report(ctx, org, sessionID, actor, phaseBlocked, say)
	res.Parked, res.Error = true, say
	return res
}

// refusedAdmission reports an error from before a run's harness began, which is the pre-flight
// or the lease refusing it (admission).
func refusedAdmission(err error) bool {
	var a admission
	return errors.As(err, &a)
}

// refusedReason is why admission refused a run, and whether anyone did. A 429 may be a limit (the
// caller checks that an authority holds), a 402 and a spent cap are money, and anything else (no
// sandbox capacity, a timeout, a gateway error, an app that is rolling) is a failure to start that
// is nobody's refusal: it is not told to the person as one, and no authority's reopening is
// waited for.
func refusedReason(err error) (reason string, refused bool) {
	var he *zip.HTTPError
	var spent unpayable
	switch {
	case errors.As(err, &he) && he.Status == http.StatusTooManyRequests:
		return agents.ParkLimit, true
	case errors.As(err, &he) && he.Status == http.StatusPaymentRequired, errors.As(err, &spent):
		return agents.ParkPayment, true
	}
	return "", false
}

// chainStart is when the chain of parks a run is in began, from the instant its request names
// (unix milliseconds), and now when it names none.
func chainStart(ms int64) time.Time {
	if ms <= 0 {
		return time.Now()
	}
	return time.UnixMilli(ms)
}

// fail records the error into the session, closes it error, and stamps the Result.
func (d Dispatcher) fail(ctx context.Context, org, sessionID, actor string, res Result, msg, logTail string) Result {
	status := map[string]any{"status": "error", "error": msg}
	// A harness killed mid-turn is told apart from one that failed: the signal
	// rides the status, as timeUp and followed ride a stop's (interrupted).
	if res.Terminated != "" {
		status["terminated"] = res.Terminated
	}
	// A run that failed with its work pushed (out of time: sandboxrunner.go
	// outOfTime) says where that work is, as a stopped one does (interrupted), so a
	// follow-up starts from it and the person reads that it was kept.
	if res.Changed && res.CommitSha != "" {
		status["changed"], status["branch"], status["commit"] = true, res.Branch, res.CommitSha
	}
	// A run a model-API fault ended names it, as a field a reader of the last status can rely on.
	if res.Fault.Count > 0 {
		status["reason"], status["fingerprint"], status["faults"] = endFaults, res.Fault.Print, res.Fault.Count
	}
	d.mirror(ctx, org, sessionID, actor, kindStatus, status)
	_ = d.Sessions.Close(ctx, org, sessionID, statusError, msg)
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
	if d.Watch != nil {
		d.Watch(ctx, kind, b)
	}
	if status, _ := payload["status"].(string); kind == kindStatus && d.track != nil {
		d.track(ctx, status)
	}
}

// report says what shape the run is in and what it is doing, in its own words: the
// session's progress is this until the run ends, so a board reads queued, then
// running, rather than a phase nobody has estimated. Best-effort, as mirror is.
func (d Dispatcher) report(ctx context.Context, org, sessionID, actor, phase, activity string) {
	activity = text.Cut(strings.Join(strings.Fields(activity), " "), activityMost)
	d.mirror(ctx, org, sessionID, actor, kindProgress, map[string]any{"phase": phase, "activity": activity})
}

// activityMost is the longest line a progress report may carry, in bytes: the
// session refuses a longer one whole (agents progressLine).
const activityMost = 120

// column is the board column for a run's status.
var column = map[string]string{
	"started": "in_progress",
	"routed":  "in_progress",
	"done":    "done",
	"error":   "todo", // it needs someone; the issue says why
}

// tracking returns d bound to this run's issue: filed when the run starts and
// moved on every status after it, from the one place a status is recorded. The
// issue is the run's — assigned to the agent, keyed by its session — so stopping,
// failing and finishing each show on the board without a second write path.
func (d Dispatcher) tracking(org, sessionID string, req Req, repo, prompt string) Dispatcher {
	// A run an issue asked for is tracked BY that issue: its proposal closes it,
	// and a second card for the same work would be two answers to its state.
	if d.Issue == nil || req.Issue != "" {
		return d
	}
	agent := nonEmpty(strings.TrimSpace(req.AgentRef), "hanzo")
	d.track = func(ctx context.Context, status string) {
		col, ok := column[status]
		if !ok {
			return
		}
		in := client.IssueIn{
			Project: req.Project, Key: "AGT", TeamName: "Agents", Repo: repo,
			ExtRef: "agent:" + sessionID, Kind: "issue",
			Title: codingTitle(repo, prompt), Description: prompt,
			State: col, Assignee: agent, Labels: []string{"agent"},
		}
		if err := d.Issue(context.WithoutCancel(ctx), org, in); err != nil {
			d.logf("coding: board", "org", org, "session", sessionID, "err", err)
		}
	}
	return d
}

func (d Dispatcher) logf(msg string, kv ...any) {
	if d.Log != nil {
		d.Log(msg, kv...)
	}
}
