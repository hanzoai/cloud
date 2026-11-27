package coding

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"log/slog"
	"strings"
)

func (in sandbox) settleAndDeliver(
	ctx context.Context,
	org, id string,
	req RunRequest,
	run *client.RunIn,
	branch, base, task, ask, model string,
	ttl, told int,
	escalated string,
	emptyRetried bool,
	servers []Server,
	said outcome,
	freshArgv func(string) []string,
	acceptRolloutTeardown func(*client.Ran) bool,
	step func(name, msg, status string),
	out RunResult,
	keepPtr *bool,
) (RunResult, error) {
	// A BUILD THAT CHANGED NOTHING IS ASKED AGAIN, in the same conversation, to
	// make its change or say none is needed (outcome.go settle). A turn that ends on
	// "Let me create the spec document…" has a last message and no tool call after
	// it, and was read as an answer that nothing needed changing; one that runs
	// more reads when resumed and pauses again before editing is continued while it
	// keeps working (then.items > 1), up to maxSettle resumes.
	//
	// A CHECKOUT THAT STARTED ON THE RUN'S OWN BRANCH holds work already: an earlier
	// run's, or a branch the run named that the remote has. Adding nothing to it
	// is not "changed nothing" (#553): a follow-up whose edits were already in was
	// asked to make its change, and failed as having made none. It is asked only to
	// finish a turn it ended on an announcement.
	carried := strings.TrimSpace(req.BaseBranch) == branch
	// cut is a check turn that did not end on its own — interrupted, out of time,
	// its sandbox failing under it (gone) or its commit failing — and what it wrote
	// is committed before the push (below).
	cut, gone := false, false
	const maxSettle = 8
	// unactedCount tracks consecutive message-only turns while Unacted. The first
	// turn counts when it is itself a non-settled announcement so two check turns of
	// prose escalate on both re-prompts and stop at maxUnacted.
	unactedCount := 0
	var lastNarration string
	if !out.Changed && !carried && !settled(out.Answer) && narrating(out.Answer) {
		unactedCount = 1
		lastNarration = out.Answer
	}
	seenShas := map[string]int{}
	if out.CommitSha != "" {
		seenShas[out.CommitSha] = 1
	}
	reversions := 0
	prevWorked := said.worked
	checksRan := 0
	checkFreshRetried := false
	for checks := 0; (!(out.Changed || carried) || narrating(out.Answer)) && checks < maxSettle && run.Events && runsDev(req.Tool) && threadRE.MatchString(said.thread) && ctx.Err() == nil; checks++ {
		checksRan++
		askPrompt := settle
		if out.Changed || carried {
			step("check", "the agent stopped after announcing its next step; asking it to finish", "running")
			askPrompt = finishTurn
		} else {
			step("check", "the agent changed nothing; asking it to make its change or say none is needed", "running")
			if unactedCount == 1 {
				askPrompt = settle + "\n\nYour last reply again only described a step. Call a tool now: edit the file you named."
			} else if unactedCount >= 2 {
				snippet := strings.TrimSpace(lastNarration)
				if len(snippet) > 200 {
					snippet = snippet[:200] + "…"
				}
				askPrompt = settle + "\n\nYour last reply was: " + snippet + "\n\nDo it now with a tool call, or answer exactly 'No change needed: <reason>'."
			}
		}
		_, _ = runIn(in.ctx, in.id, []string{"rm", "-f", answerPath}, in.ttl, "", "")
		checkArgv := func(m string) []string {
			return withDeveloperInstructions(req.Tool, resumeFor(said.thread, ask, m, req.Effort, servers...), req.DeveloperInstructions)
		}
		again, err := in.turn(ctx, run, checkArgv(model), askPrompt, ttl)
		if err == nil && again != nil {
			acceptRolloutTeardown(again)
		}
		// If resuming the prior thread fails due to a thread-gone or rollout crash,
		// retry ONCE on a fresh thread in the same workspace instead of failing or
		// misclassifying the crash as Unacted.
		freshUsed := false
		if err == nil && again != nil && again.ExitCode != 0 && killedBy(again.ExitCode) == "" && outcomeOf(again.Stdout).repeated == "" && ctx.Err() == nil && harnessThreadCrash(again) && !checkFreshRetried {
			checkFreshRetried = true
			freshUsed = true
			step("check", "the harness hit a thread/rollout error while settling; retrying on a fresh thread in the same workspace", "running")
			_, _ = runIn(in.ctx, in.id, []string{"rm", "-f", answerPath}, in.ttl, "", "")
			retryPrompt := noChangeRetryPrompt + task
			if out.Changed || carried {
				retryPrompt = threadRecoveryLine + task
			}
			again, err = in.turn(ctx, run, freshArgv(model), retryPrompt, ttl)
			if err == nil && again != nil {
				acceptRolloutTeardown(again)
			}
		}
		if err == nil && again != nil && again.ExitCode != 0 && killedBy(again.ExitCode) == "" && outcomeOf(again.Stdout).repeated == "" &&
			delegatedAuthRefused(again) && in.cred != nil && ctx.Err() == nil {
			step("credential", "the model credential was rejected; minting a fresh delegated token and continuing", "running")
			if rerr := in.cred.reissue(ctx); rerr == nil && ctx.Err() == nil {
				if freshUsed {
					again, err = in.turn(ctx, run, freshArgv(model), noChangeRetryPrompt+task, ttl)
				} else {
					again, err = in.turn(ctx, run, checkArgv(model), askPrompt, ttl)
				}
				if err == nil && again != nil {
					acceptRolloutTeardown(again)
				}
			} else if rerr != nil {
				slog.Warn("coding: could not reissue delegated credential after 401 in check turn", "org", org, "session", req.SessionID, "err", rerr)
			}
		}
		if err == nil && again != nil && again.ExitCode != 0 && killedBy(again.ExitCode) == "" && outcomeOf(again.Stdout).repeated == "" && paymentRefused(again) && model != codingFallback && ctx.Err() == nil {
			step(req.Tool, "the model was refused for balance; asking "+codingFallback, "running")
			model = codingFallback
			if freshUsed {
				again, err = in.turn(ctx, run, freshArgv(model), noChangeRetryPrompt+task, ttl)
			} else {
				again, err = in.turn(ctx, run, checkArgv(model), askPrompt, ttl)
			}
			if err == nil && again != nil {
				acceptRolloutTeardown(again)
			}
		}
		if err == nil && again != nil && again.ExitCode != 0 && killedBy(again.ExitCode) == "" && outcomeOf(again.Stdout).repeated == "" && ctx.Err() == nil {
			if wait, ok := transientLimit(again); ok {
				wait = capTransientWait(wait)
				if alt := limitFallback(model, req.Model != ""); alt != "" {
					model = alt
					step(req.Tool, "rate limit reached; waiting before retrying with "+model, "running")
				} else {
					step(req.Tool, "rate limit reached; waiting before retrying", "running")
				}
				if waitForRetry(ctx, wait) {
					if freshUsed {
						again, err = in.turn(ctx, run, freshArgv(model), noChangeRetryPrompt+task, ttl)
					} else {
						again, err = in.turn(ctx, run, checkArgv(model), askPrompt, ttl)
					}
					if err == nil && again != nil {
						acceptRolloutTeardown(again)
					}
				}
			} else if modelCapRefused(again) {
				if alt := limitFallback(model, req.Model != ""); alt != "" {
					model = alt
					step(req.Tool, "model cap reached; falling back to "+model, "running")
					if freshUsed {
						again, err = in.turn(ctx, run, freshArgv(model), noChangeRetryPrompt+task, ttl)
					} else {
						again, err = in.turn(ctx, run, checkArgv(model), askPrompt, ttl)
					}
					if err == nil && again != nil {
						acceptRolloutTeardown(again)
					}
				}
			}
		}
		for err == nil && again != nil && again.ExitCode != 0 && ctx.Err() == nil {
			loopSaid := outcomeOf(again.Stdout)
			out.Usage = out.Usage.plus(loopSaid.usage)
			next, ok := repeat(model, told, req.Model != "")
			if loopSaid.repeated == "" || !ok || (!freshUsed && loopSaid.thread != said.thread) {
				break
			}
			name, _ := call(loopSaid.repeated)
			if next == model {
				step("loop", "the agent repeated "+cmp.Or(name, "a tool call")+"; telling it to change course", "running")
			} else {
				step("loop", "the agent repeated "+cmp.Or(name, "a tool call")+" again; handing the turn to "+next, "running")
				escalated = next
			}
			model, told = next, told+1
			if freshUsed && threadRE.MatchString(loopSaid.thread) {
				said.thread = loopSaid.thread
			}
			again, err = in.turn(ctx, run, checkArgv(model), nudge(loopSaid.repeated, in.scrub, told > 1), ttl)
		}
		// A turn that failed or answered in another conversation leaves the run as
		// the earlier turn left it, marked Unacted when it neither changed nor settled.
		// A turn that was KILLED fails the run, delivered work and all: it was cut
		// off finishing what the earlier turn announced, so its work is kept on the
		// branch, said to be kept, and not proposed (terminate).
		//
		// A REFUSAL DURING A CHECK TURN (429 limit, 402 payment, 5xx fault) parks or
		// surfaces the refusal explicitly instead of silently marking the run Unacted
		// ("the agent said what it would do and changed nothing").
		//
		// A HARNESS THREAD/ROLLOUT CRASH DURING A CHECK TURN surfaces the harness
		// error explicitly and commits any uncommitted edits instead of misclassifying
		// the crash as Unacted.
		//
		// A TURN THE SANDBOX FAILED UNDER fails the run with what failed (#553). It
		// was read as a turn the agent left unacted, so a pod evicted mid-`make test`
		// reported "the agent said what it would do and changed nothing". Its work
		// is kept on the branch, as a killed turn's is. A stop, a pause or a steer is
		// not a failure: it is the run's cause, read below.
		threadMismatch := !freshUsed && again != nil && outcomeOf(again.Stdout).thread != said.thread
		if err != nil || again == nil || again.ExitCode != 0 || threadMismatch {
			if again != nil {
				againSaid := outcomeOf(again.Stdout)
				settleRepeated(&out, againSaid.repeated, in.scrub)
				if killedBy(again.ExitCode) != "" {
					terminate(&out, req.Tool, again.ExitCode)
				} else if err == nil && again.ExitCode != 0 && againSaid.repeated == "" && ctx.Err() == nil {
					if reason := refusedFor(again); reason != "" {
						last := cmp.Or(in.scrub(againSaid.failed), lastLine(in.scrub(again.Stderr)))
						if req.Plan || (!req.Local && strings.TrimSpace(req.Remote) != "") {
							parked, kerr := in.park(ctx, org, id, reason, firstLine(last), again, req, branch, base, out, step)
							if kerr == nil {
								*keepPtr = false
								return parked, nil
							}
							step("park", "the run could not wait for the refusal to clear: "+in.scrub(kerr.Error()), "error")
						}
						out.OK = false
						if tailStr := in.scrub(tail(out.Answer, again.Stderr)); tailStr != "" {
							out.LogTail = tailStr
						}
						if reason == agents.ParkLimit {
							out.Error = refusedSaid
							if last != "" {
								out.Error += " (" + last + ")"
							}
						} else {
							out.Error = fmt.Sprintf("the %s harness exited %d", harness(req.Tool), again.ExitCode)
							if last != "" {
								out.Error += ": " + last
							}
						}
						break
					}
					if harnessThreadCrash(again) {
						last := cmp.Or(in.scrub(againSaid.failed), lastLine(in.scrub(again.Stderr)))
						out.OK = false
						if tailStr := in.scrub(tail(out.Answer, again.Stderr)); tailStr != "" {
							out.LogTail = tailStr
						}
						out.Error = fmt.Sprintf("the %s harness exited %d", harness(req.Tool), again.ExitCode)
						if last != "" {
							out.Error += ": " + last
						}
						cut = true
						break
					}
				}
			}
			// Only a turn the sandbox failed under is committed after it: one whose
			// harness failed, or answered in another conversation, is not the run's
			// work, and its edits stay in the tree as they did.
			if err != nil && ctx.Err() == nil {
				out.OK, out.Error = false, "the sandbox failed under the agent's turn: "+in.scrub(err.Error())
				if outOfTime(ctx, err) {
					out.Error = "the agent ran out of time before it finished"
				} else {
					gone = true
				}
				cut = true
			}
			out.Unacted = !out.Changed && !carried && !settled(out.Answer)
			break
		}
		then := outcomeOf(again.Stdout)
		if freshUsed && threadRE.MatchString(then.thread) {
			said.thread = then.thread
			if in.knownThread != nil {
				*in.knownThread = then.thread
			}
		}
		answer, final := in.answer(then, again.Stdout)
		answer = in.scrub(answer)
		out.Usage = out.Usage.plus(then.usage)
		out.Answer, out.LogTail, out.Unanswered = planOf(answer), in.scrub(tail(answer, again.Stderr)), run.Events && !final
		prevSha := out.CommitSha
		if err := in.commit(req, base, &out, step); err != nil {
			// What the turns before it committed is still pushed below.
			out.OK, out.Error, cut = false, in.scrub(err.Error()), true
			break
		}
		if out.CommitSha != prevSha {
			if out.CommitSha == "" && prevSha != "" {
				reversions++
				if reversions >= 2 {
					out.Unacted = !carried && !settled(answer)
					break
				}
			} else if out.CommitSha != "" {
				seenShas[out.CommitSha]++
				if seenShas[out.CommitSha] >= 2 {
					break
				}
			}
		}
		out.Unacted = !out.Changed && !carried && !settled(answer)
		messageOnly := !then.worked && then.items <= 1
		if !carried && out.Unacted && messageOnly && (narrating(answer) || !settled(answer)) {
			unactedCount++
			lastNarration = answer
			if unactedCount >= maxUnacted {
				break
			}
			continue
		}
		if !messageOnly || !out.Unacted || settled(answer) {
			unactedCount = 0
			lastNarration = ""
		}
		stillNarrating := narrating(answer) && (then.worked || prevWorked)
		prevWorked = then.worked
		if (!out.Unacted && !stillNarrating) || (!then.worked && then.items <= 1 && final && !stillNarrating) {
			break
		}
	}
	// When a build turn changed nothing and could not enter the resumable settle loop
	// (e.g. codex/claude, or a dev turn with no thread ID that ended narrating or
	// without a final answer), re-prompt ONCE on a fresh thread instead of immediately
	// failing as hollow/Unacted. If still unchanged after retry, terminate cleanly as Unacted.
	if checksRan == 0 && !out.Changed && !carried && !settled(out.Answer) && !outOfBudget(out.Answer) && !emptyRetried && out.OK && ctx.Err() == nil {
		toolName := harness(req.Tool)
		nonDevAgent := agentic(req.Tool) && !runsDev(req.Tool) && (!said.plain || strings.TrimSpace(out.Answer) != "")
		unresumableHollowDev := runsDev(req.Tool) && said.thread == "" && !said.plain && (out.Unanswered || narrating(out.Answer))
		if nonDevAgent || unresumableHollowDev {
			step("check", "the agent changed nothing; re-prompting on a fresh thread to make its change or say none is needed", "running")
			_, _ = runIn(in.ctx, in.id, []string{"rm", "-f", answerPath}, in.ttl, "", "")
			again, err := in.turn(ctx, run, freshArgv(model), noChangeRetryPrompt+task, ttl)
			if err == nil && again != nil {
				acceptRolloutTeardown(again)
			}
			if err == nil && again != nil && again.ExitCode == 0 {
				then := outcomeOf(again.Stdout)
				answer, final := in.answer(then, again.Stdout)
				answer = in.scrub(answer)
				out.Usage = out.Usage.plus(then.usage)
				out.Answer, out.LogTail, out.Unanswered = planOf(answer), in.scrub(tail(answer, again.Stderr)), run.Events && !final
				if cerr := in.commit(req, base, &out, step); cerr != nil {
					out.OK, out.Error, cut = false, in.scrub(cerr.Error()), true
				} else {
					out.Unacted = !out.Changed && !carried && !settled(answer)
				}
			} else if again != nil && killedBy(again.ExitCode) != "" {
				terminate(&out, req.Tool, again.ExitCode)
			} else if err == nil && again != nil && again.ExitCode != 0 && ctx.Err() == nil {
				againSaid := outcomeOf(again.Stdout)
				last := cmp.Or(in.scrub(againSaid.failed), lastLine(in.scrub(again.Stderr)))
				if reason := refusedFor(again); reason != "" {
					if req.Plan || (!req.Local && strings.TrimSpace(req.Remote) != "") {
						if parked, kerr := in.park(ctx, org, id, reason, firstLine(last), again, req, branch, base, out, step); kerr == nil {
							*keepPtr = false
							return parked, nil
						}
					}
				}
				out.OK = false
				if tailStr := in.scrub(tail(out.Answer, again.Stderr)); tailStr != "" {
					out.LogTail = tailStr
				}
				out.Error = fmt.Sprintf("the %s harness exited %d", toolName, again.ExitCode)
				if last != "" {
					out.Error += ": " + last
				}
				if harnessThreadCrash(again) {
					cut = true
				}
			} else if err != nil && ctx.Err() == nil {
				out.OK, out.Error = false, "the sandbox failed under the agent's turn: "+in.scrub(err.Error())
				if outOfTime(ctx, err) {
					out.Error = "the agent ran out of time before it finished"
				} else {
					gone = true
				}
				cut = true
			}
		}
	}
	// A turn that ends on an announcement, again when asked to finish, or on its
	// spent token budget, is not done, whatever the branch already held. A turn
	// that already failed explicitly (refusal, harness crash, sandbox error) keeps
	// its explicit error rather than being misclassified as Unacted.
	if out.OK && !out.Changed && !settled(out.Answer) && (narrating(out.Answer) || outOfBudget(out.Answer)) {
		out.Unacted = true
	}
	// A STEER DURING THE CHECKS leaves the tree to its follow-up, as one during the
	// first turn does: what this run committed is in it, and the follow-up pushes
	// it with its own work (workspace measures from what the remote holds).
	cause := context.Cause(ctx)
	if errors.Is(cause, errFollowed) {
		return in.steered(org, base, step), nil
	}
	// The checks are over, so the work leaves: on a context of its own when the
	// run's ended under them (stopped, paused, out of time), as keep's does.
	if ctx.Err() != nil {
		in.halt()
		kept, done := context.WithTimeout(context.WithoutCancel(in.ctx), keepWait)
		defer done()
		in.ctx = kept
		out.TimeUp = errors.Is(cause, errTimeUp)
		out.Unrenewable = errors.Is(cause, errUnrenewable)
		cut = true
	}
	// A CHECK TURN CUT SHORT left what it wrote uncommitted, and one the sandbox
	// failed under may have lost its pod: the pod is replaced with its disk and
	// without /tmp or $HOME, so the run's key and its trust in the checkout are put
	// back first (rearm). Each is asked twice, because the first call to reach a
	// replaced pod is the one that replaces it, and is refused.
	if cut && !req.Plan {
		if gone && in.rearm(req) != nil {
			_ = in.rearm(req)
		}
		if cerr := in.commit(req, base, &out, step); cerr != nil {
			if in.rearm(req) != nil {
				_ = in.rearm(req)
			}
			if cerr = in.commit(req, base, &out, step); cerr != nil && out.OK {
				return RunResult{}, cerr
			}
		}
	}
	if err := in.push(req, branch, &out, step); err != nil {
		if out.OK {
			return RunResult{}, err
		}
		out.Error += "; its work could not be pushed: " + in.scrub(err.Error())
	} else if !out.OK && out.Changed && out.Terminated == "" && !req.Local {
		out.Error += "; its work so far is on " + branch
	}
	// What was pushed is put on the project's site, so the project's own address
	// shows it (publish.go).
	if out.Changed && req.Site.Project != "" && !req.Local {
		in.site(org, req.Site, out.CommitSha, &out, step)
	}
	in.save(org, base, &out, step)
	step("done", "finished", statusOf(out.OK))
	return out, nil
}
