package triage

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// observe takes one error fact off the event plane: it counts it under its
// fingerprint, and when the count crosses its class's threshold files the one issue
// that stands for it. A fault whose issue was fixed and comes back in a release that
// holds the fix is a regression.
func (s *state) observe(ctx context.Context, f event.Fault) error {
	if f.Issue == "" || isSynthetic(f) {
		return nil
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return err
	}
	if f.Attributes["fault.class"] == "run" {
		if err := s.ended(ctx, cfg, f); err != nil {
			return err
		}
	}
	repo, ok := cfg.route(f)
	if !ok {
		return nil
	}
	class := cmp.Or(f.Attributes["fault.class"], "client")
	lim := cfg.Threshold[class]
	row, err := s.store.Count(ctx, Hit{
		Org: f.Org, FP: f.Issue, Repo: repo.Repo, Class: class,
		Code: cmp.Or(f.Attributes["fault.code"], f.Class), Op: cmp.Or(f.Attributes["fault.op"], f.Path),
		At: f.Time, Release: cmp.Or(f.Attributes["release"], f.Release), Reason: f.Message, Service: f.Service,
		Request: f.Attributes["fault.request"], Site: attribute(f),
	}, cmp.Or(time.Duration(lim.Minutes)*time.Minute, day))
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	switch {
	case row.State == fixed:
		return s.regressed(ctx, cfg, repo, row, f)
	case row.State == seen && lim.Count > 0 && row.Hits >= int64(lim.Count):
		return s.file(ctx, cfg, repo, row)
	case row.State == open && row.Total >= 2*row.Reported:
		return s.restate(ctx, cfg, repo, row)
	}
	return nil
}

// isSynthetic reports whether a fault was tagged as a synthetic verification probe
// (e.g. end-to-end error ingest check) that should land in event.fact/o11y without
// filing or mutating GitHub issues.
func isSynthetic(f event.Fault) bool {
	if len(f.Attributes) == 0 {
		return false
	}
	for _, k := range []string{"synthetic", "triage.skip", "test"} {
		if strings.EqualFold(strings.TrimSpace(f.Attributes[k]), "true") {
			return true
		}
	}
	return false
}

// day is the window of a class that names none, and the length of a run budget's day.
const day = 24 * time.Hour

// as is ctx as the actor, in the org whose GitHub and runs these are.
func (s *state) as(ctx context.Context, cfg Config, org string) context.Context {
	return zip.WithCaller(cloud.For(ctx, org), zip.Caller{Org: org, User: cfg.Actor})
}

// record puts one autonomous act on the audit trail, before it is done: an act the
// trail cannot hold is not done.
func (s *state) record(ctx context.Context, cfg Config, org, action, resource string, detail map[string]any) error {
	after, _ := json.Marshal(detail)
	if s.audit == nil {
		s.log.Info("triage: "+action, "org", org, "resource", resource)
		return nil
	}
	_, err := s.audit.Append(ctx, audit.Record{
		Actor:    audit.Actor{Org: org, Sub: cfg.Actor},
		Action:   action,
		Resource: audit.Resource{Type: "github_issue", ID: resource},
		Auth:     audit.AuthContext{Method: "plane"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK},
		After:    audit.Redact(after),
	})
	if err != nil {
		return fmt.Errorf("triage: the audit trail did not take %s: %w", action, err)
	}
	return nil
}

func ownerName(repo string) (string, string) {
	owner, name, _ := strings.Cut(repo, "/")
	return owner, name
}

// write is one write to an issue, as the actor.
func (s *state) write(ctx context.Context, cfg Config, repo Repo, in client.GitHubIssueIn) (*client.GitHubIssue, error) {
	in.Owner, in.Repo = ownerName(repo.Repo)
	return providerpeer.IntegrationsGitHubIssue(s.as(ctx, cfg, repo.Org), &in)
}

// file opens the one issue for a fault. The replica that moves the fault from seen
// to filing is the only one that does; a failure gives the right back. A fault at an
// op whose issue already stands open is folded into that issue instead.
func (s *state) file(ctx context.Context, cfg Config, repo Repo, f Fault) error {
	var host Fault
	var standing bool
	if f.Op != "" {
		var err error
		if host, standing, err = s.store.OpenFor(ctx, f.Org, repo.Repo, f.Class, f.Op, f.FP); err != nil {
			return err
		}
		if standing && host.Issue == f.Issue {
			return nil // already folded into the issue that still stands
		}
	}
	if won, err := s.store.Move(ctx, f.Org, f.FP, seen, filing); err != nil || !won {
		return err
	}
	if standing {
		folded, err := s.fold(ctx, cfg, repo, f, host)
		if err != nil {
			_, _ = s.store.Move(ctx, f.Org, f.FP, filing, seen)
		}
		if err != nil || folded {
			return err
		}
	}
	title, body := issueText(f, cfg)
	var labels []string
	if repo.Auto {
		labels = []string{cfg.Label}
	}
	opened, err := s.filed(ctx, cfg, repo, f, title, body, labels)
	if err != nil {
		_, _ = s.store.Move(ctx, f.Org, f.FP, filing, seen)
		return err
	}
	if err := s.store.Filed(ctx, f.Org, f.FP, opened.Number, f.Total); err != nil {
		return err
	}
	if repo.Auto {
		return s.enqueue(ctx, cfg, repo, Work{Org: repo.Org, Repo: repo.Repo, Issue: opened.Number, FP: f.FP})
	}
	return nil
}

func (s *state) filed(ctx context.Context, cfg Config, repo Repo, f Fault, title, body string, labels []string) (*client.GitHubIssue, error) {
	if err := s.record(ctx, cfg, f.Org, "triage.issue.file", repo.Repo, map[string]any{"fingerprint": f.FP, "class": f.Class, "op": f.Op}); err != nil {
		return nil, err
	}
	return s.write(ctx, cfg, repo, client.GitHubIssueIn{Title: title, Body: body, Labels: labels})
}

// fold tells the open issue for a fault's class and op about a new fingerprint there,
// once, in place of an issue of its own: one op that fails several ways is one issue,
// not one per way. An issue a person has closed is no home for it; it files its own.
func (s *state) fold(ctx context.Context, cfg Config, repo Repo, f, host Fault) (bool, error) {
	if err := s.record(ctx, cfg, f.Org, "triage.issue.fold", fmt.Sprintf("%s#%d", repo.Repo, host.Issue),
		map[string]any{"fingerprint": f.FP, "into": host.FP, "class": f.Class, "op": f.Op}); err != nil {
		return false, err
	}
	note := fmt.Sprintf("Another fault at `%s` (%s): `%s`, fingerprint `%s`, %d occurrences so far; the last: %s",
		clip(f.Op, 160), f.Class, clip(f.Code, 80), f.FP, f.Total, clip(f.Sample.Reason, maxReason))
	is, err := s.write(ctx, cfg, repo, client.GitHubIssueIn{Number: host.Issue, Comment: note})
	if err != nil {
		return false, err
	}
	if is.State != "open" {
		return false, nil
	}
	return true, s.store.Fold(ctx, f.Org, f.FP, host.Issue)
}

// restate brings the issue's text up to date once the count has doubled since it
// last said it, so a fault that keeps happening is a growing number on one issue and
// never a stream of comments.
func (s *state) restate(ctx context.Context, cfg Config, repo Repo, f Fault) error {
	_, body := issueText(f, cfg)
	if err := s.record(ctx, cfg, f.Org, "triage.issue.update", fmt.Sprintf("%s#%d", repo.Repo, f.Issue), map[string]any{"fingerprint": f.FP, "total": f.Total}); err != nil {
		return err
	}
	if _, err := s.write(ctx, cfg, repo, client.GitHubIssueIn{Number: f.Issue, Body: body}); err != nil {
		return err
	}
	return s.store.Reported(ctx, f.Org, f.FP, f.Total)
}

// regressed is a fixed fault seen again in a release that holds its fix. The issue is
// reopened with the evidence and, when the repository runs on its own, handed to a
// run again; nothing is reverted.
func (s *state) regressed(ctx context.Context, cfg Config, repo Repo, f Fault, last event.Fault) error {
	release := cmp.Or(last.Attributes["release"], last.Release)
	if release == "" || last.Time.Unix() <= f.FixedAt {
		return nil
	}
	// A release is asked about once: whether its history holds a commit does not change.
	if _, asked := s.asked.LoadOrStore(f.Org+"/"+f.FP+"/"+release, true); asked {
		return nil
	}
	owner, name := ownerName(repo.Repo)
	held, err := providerpeer.IntegrationsGitHubContains(s.as(ctx, cfg, repo.Org),
		&client.GitHubContainsIn{Owner: owner, Repo: name, Sha: f.FixedSHA, Ref: release})
	if err != nil || !held.Contains {
		return err
	}
	if err := s.record(ctx, cfg, f.Org, "triage.issue.reopen", fmt.Sprintf("%s#%d", repo.Repo, f.Issue), map[string]any{"fingerprint": f.FP, "fixed": f.FixedSHA, "release": release}); err != nil {
		return err
	}
	if err := s.store.Fix(ctx, f.Org, f.FP, "", 0); err != nil {
		return err
	}
	evidence := fmt.Sprintf("This fault is back in `%s`, which contains the fix (`%s`). %d occurrences so far; the last: %s",
		release, f.FixedSHA, f.Total, clip(last.Message, maxReason))
	if _, err := s.write(ctx, cfg, repo, client.GitHubIssueIn{Number: f.Issue, State: "open", Comment: evidence}); err != nil {
		return err
	}
	if !repo.Auto {
		return nil
	}
	if err := s.store.Requeue(ctx, repo.Org, repo.Repo, f.Issue, s.now().Unix()); err != nil {
		return err
	}
	return s.drain(ctx, cfg, repo)
}
