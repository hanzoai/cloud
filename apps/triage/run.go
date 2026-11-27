package triage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	"github.com/hanzoai/cloud/sdk/plane"
)

// task is what a run is told beside the issue's own words.
const task = "\n\n---\nThis issue was handed to you to fix. Reproduce it first: write a test that fails for the reason it describes. " +
	"Then make the smallest change that makes it pass, and open one pull request that closes the issue."

// enqueue hands an issue to the runs and starts it if there is room.
func (s *state) enqueue(ctx context.Context, cfg Config, repo Repo, w Work) error {
	w.Queued = s.now().Unix()
	if _, err := s.store.Enqueue(ctx, w); err != nil {
		return err
	}
	return s.drain(ctx, cfg, repo)
}

// drain starts the oldest waiting issues of repo while the repository has room: the
// kill switch is off, it is allowed to run, fewer than its limit are running, and the
// org's day has runs left. What does not fit waits, and is tried again whenever a run
// ends or another issue arrives.
func (s *state) drain(ctx context.Context, cfg Config, repo Repo) error {
	for cfg.Enabled && repo.Auto {
		w, ok, err := s.store.Next(ctx, repo.Org, repo.Repo)
		if err != nil || !ok {
			return err
		}
		started, err := s.start(ctx, cfg, repo, w)
		if err != nil || !started {
			return err
		}
	}
	return nil
}

// branchOf is the branch an attempt writes: one issue, one branch for each attempt.
func branchOf(w Work) string {
	if w.Attempt > 1 {
		return fmt.Sprintf("autofix-%d-%d", w.Issue, w.Attempt)
	}
	return "autofix-" + strconv.Itoa(w.Issue)
}

// start claims w and starts its run. It answers false, leaving the issue waiting,
// when the repository or the day is full.
func (s *state) start(ctx context.Context, cfg Config, repo Repo, w Work) (bool, error) {
	ref := fmt.Sprintf("%s#%d", repo.Repo, w.Issue)
	now := s.now()
	if w.Attempt > cfg.Runs.Attempts {
		return true, s.giveUp(ctx, cfg, repo, w, "it has had its attempts")
	}
	w.Branch = branchOf(w)
	budget := time.Duration(cfg.Runs.Seconds) * time.Second
	if budget <= 0 {
		return false, nil
	}
	if won, err := s.store.Claim(ctx, w, now.Unix(), now.Add(budget).Unix()); err != nil || !won {
		return false, err
	}
	running, err := s.store.Running(ctx, repo.Org, repo.Repo, now.Unix())
	if err != nil {
		return false, err
	}
	today, err := s.store.Started(ctx, repo.Org, now.Truncate(day).Unix())
	if err != nil {
		return false, err
	}
	if running > repo.Concurrent || today > cfg.Runs.PerDay {
		return false, s.store.Unclaim(ctx, w)
	}
	if err := s.record(ctx, cfg, repo.Org, "triage.run.start", ref, map[string]any{"branch": w.Branch, "attempt": w.Attempt, "seconds": cfg.Runs.Seconds}); err != nil {
		_ = s.store.Unclaim(ctx, w)
		return false, err
	}
	issue, err := s.write(ctx, cfg, repo, client.GitHubIssueIn{Number: w.Issue})
	if err != nil {
		_ = s.store.Unclaim(ctx, w)
		return false, err
	}
	in := client.Work("github.com/"+repo.Repo, w.Issue, issue.Title, issue.Body+task, client.Reply{})
	in.Pull, in.Branch, in.TimeoutSeconds = true, w.Branch, cfg.Runs.Seconds
	started, err := plane.Call[client.CodingStartIn, client.CodingStarted](s.as(ctx, cfg, repo.Org), agentpeer.App, client.AgentsCodingStart, &in)
	if err != nil {
		_ = s.store.Settle(ctx, w, failed, err.Error())
		_, _ = s.write(ctx, cfg, repo, client.GitHubIssueIn{Number: w.Issue, Comment: "The run did not start: " + clip(err.Error(), maxReason)})
		return true, nil
	}
	return true, s.store.Begun(ctx, w, started.SessionID, started.Branch)
}

// giveUp ends an issue's runs and says so where people look.
func (s *state) giveUp(ctx context.Context, cfg Config, repo Repo, w Work, why string) error {
	if err := s.store.Settle(ctx, w, failed, why); err != nil {
		return err
	}
	_, err := s.write(ctx, cfg, repo, client.GitHubIssueIn{Number: w.Issue, Comment: "No more runs for this issue: " + why + "."})
	return err
}

// ended is a run's own error fact: the run is over, its issue is told why, and its
// place goes to the next issue in line.
func (s *state) ended(ctx context.Context, cfg Config, f event.Fault) error {
	w, ok, err := s.store.Session(ctx, f.Attributes["fault.request"])
	if err != nil || !ok || w.State != running {
		return err
	}
	if err := s.store.Settle(ctx, w, failed, clip(f.Message, maxReason)); err != nil {
		return err
	}
	repo, ok := cfg.repo(w.Org, w.Repo)
	if !ok {
		return nil
	}
	if _, err := s.write(ctx, cfg, repo, client.GitHubIssueIn{Number: w.Issue, Comment: "The run ended in error: " + clip(f.Message, maxReason)}); err != nil {
		return err
	}
	return s.drain(ctx, cfg, repo)
}

// stopAll ends every live run: what the kill switch does besides refusing the next.
func (s *state) stopAll(ctx context.Context, cfg Config) error {
	live, err := s.store.Live(ctx)
	if err != nil {
		return err
	}
	for _, w := range live {
		if w.Session != "" {
			_, _ = agentpeer.AgentsSessionsStop(s.as(ctx, cfg, w.Org), &client.SessionMatchIn{Subject: cfg.Actor, SessionID: w.Session})
		}
		if err := s.store.Settle(ctx, w, failed, "stopped by the kill switch"); err != nil {
			return err
		}
	}
	return nil
}
