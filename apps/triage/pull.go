package triage

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/zap-proto/zip"
)

// delivery is the slice of GitHub's payloads triage reads: an issue, a pull request,
// a status or a check suite, and where each is.
type delivery struct {
	Action string `json:"action"`
	Label  struct {
		Name string `json:"name"`
	} `json:"label"`
	Sender struct {
		Type string `json:"type"`
	} `json:"sender"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number      int `json:"number"`
		PullRequest any `json:"pull_request"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue"`
	Pull struct {
		Number int    `json:"number"`
		Merged bool   `json:"merged"`
		Merge  string `json:"merge_commit_sha"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	// SHA is a status's commit; Suite is a check suite's.
	SHA   string `json:"sha"`
	Suite struct {
		Head string `json:"head_sha"`
	} `json:"check_suite"`
}

// told is one delivery from GitHub. It acts only on work triage handed to a run, or
// on an issue a person labelled for one; any other delivery is answered and left.
func (s *state) told(ctx context.Context, org string, in *client.GitHubDelivery) (string, error) {
	var d delivery
	if json.Unmarshal(in.Body, &d) != nil {
		return "", nil
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return "", err
	}
	repo, ok := cfg.repo(org, d.Repository.FullName)
	if !ok {
		return "", nil
	}
	switch in.Event {
	case "issues":
		return s.labelled(ctx, cfg, repo, d)
	case "pull_request":
		return s.pulled(ctx, cfg, repo, d)
	case "status", "check_suite":
		w, ok, err := s.store.Head(ctx, repo.Org, repo.Repo, cmp.Or(d.SHA, d.Suite.Head))
		if err != nil || !ok {
			return "", err
		}
		return "", s.gate(ctx, cfg, repo, w)
	}
	return "", nil
}

// labelled queues an issue a person handed to a run: labelled with the label, or
// opened carrying it. The platform's own writes are never read back: an issue it
// filed is already queued with the fingerprint it stands for.
func (s *state) labelled(ctx context.Context, cfg Config, repo Repo, d delivery) (string, error) {
	if !cfg.Enabled || !repo.Auto || d.Sender.Type == "Bot" || d.Issue.PullRequest != nil || d.Issue.Number == 0 {
		return "", nil
	}
	handed := d.Action == "labeled" && d.Label.Name == cfg.Label
	if d.Action == "opened" {
		handed = slices.ContainsFunc(d.Issue.Labels, func(l struct {
			Name string `json:"name"`
		}) bool {
			return l.Name == cfg.Label
		})
	}
	if !handed {
		return "", nil
	}
	w := Work{Org: repo.Org, Repo: repo.Repo, Issue: d.Issue.Number, Queued: s.now().Unix()}
	fresh, err := s.store.Enqueue(ctx, w)
	if err != nil {
		return "", err
	}
	if !fresh {
		// An issue handed over again after its run failed is another attempt; one that is
		// running, proposed or merged is left as it is.
		old, ok, err := s.store.Issue(ctx, w.Org, w.Repo, w.Issue)
		if err != nil || !ok || old.State != failed {
			return "", err
		}
		if err := s.store.Requeue(ctx, w.Org, w.Repo, w.Issue, w.Queued); err != nil {
			return "", err
		}
	}
	return "queued", s.drain(ctx, cfg, repo)
}

// pulled follows the pull request of a run: opened or moved, it is where the run's
// work stands and is judged; closed, it is the end of that work.
func (s *state) pulled(ctx context.Context, cfg Config, repo Repo, d delivery) (string, error) {
	w, ok, err := s.store.Branch(ctx, repo.Org, repo.Repo, d.Pull.Head.Ref)
	if err != nil || !ok || w.PR != 0 && w.PR != d.Pull.Number {
		return "", err
	}
	switch d.Action {
	case "opened", "reopened", "synchronize", "ready_for_review":
		if err := s.store.Propose(ctx, w, d.Pull.Number, d.Pull.Head.SHA); err != nil {
			return "", err
		}
		w.PR, w.Head = d.Pull.Number, d.Pull.Head.SHA
		if err := s.drain(ctx, cfg, repo); err != nil {
			return "", err
		}
		return "proposed", s.gate(ctx, cfg, repo, w)
	case "closed":
		state, note := failed, "its pull request was closed without merging"
		if d.Pull.Merged {
			state, note = merged, d.Pull.Merge
			if w.FP != "" {
				if err := s.store.Fix(ctx, w.Org, w.FP, d.Pull.Merge, s.now().Unix()); err != nil {
					return "", err
				}
			}
		}
		if err := s.store.Settle(ctx, w, state, note); err != nil {
			return "", err
		}
		return "closed", s.drain(ctx, cfg, repo)
	}
	return "", nil
}

// gate merges a run's pull request when it is safe to: the repository lets it, every
// check it names reads success on the head, the whole change was read, and no path in
// it is one a person must merge. The merge is at the head that was read.
func (s *state) gate(ctx context.Context, cfg Config, repo Repo, w Work) error {
	if !cfg.Enabled || !repo.Merge || w.PR == 0 {
		return nil
	}
	owner, name := ownerName(repo.Repo)
	as := s.as(ctx, cfg, repo.Org)
	pr, err := providerpeer.IntegrationsGitHubPull(as, &client.GitHubPullIn{Owner: owner, Repo: name, Number: w.PR})
	if err != nil {
		return err
	}
	if pr.State != "open" || pr.Draft || pr.Head != w.Head {
		return nil
	}
	if why, human := judge(repo, pr); why != "" {
		if human && w.Note != why {
			if _, err := s.write(ctx, cfg, repo, client.GitHubIssueIn{Number: w.Issue, Comment: fmt.Sprintf("A person merges #%d: %s.", w.PR, why)}); err != nil {
				return err
			}
			return s.store.Settle(ctx, w, w.State, why)
		}
		return nil
	}
	if err := s.record(ctx, cfg, repo.Org, "triage.pr.merge", fmt.Sprintf("%s#%d", repo.Repo, w.PR), map[string]any{"head": pr.Head, "files": len(pr.Files)}); err != nil {
		return err
	}
	_, err = providerpeer.IntegrationsGitHubMerge(as, &client.GitHubMergeIn{Owner: owner, Repo: name, Number: w.PR, Sha: pr.Head})
	if he, ok := errors.AsType[*zip.HTTPError](err); ok && he.Status == http.StatusConflict {
		return nil // the branch moved or GitHub would not merge it: the next delivery asks again
	}
	return err
}

// judge says why a pull request may not merge itself, and whether that is for a
// person to settle (a path that needs one) rather than a state that will change.
func judge(repo Repo, pr *client.GitHubPull) (why string, human bool) {
	if len(pr.Files) < pr.Changed {
		return "the change is larger than it could be read", true
	}
	for _, f := range pr.Files {
		if highRisk(f) {
			return "it touches " + f, true
		}
	}
	state := map[string]string{}
	for _, c := range pr.Checks {
		if state[c.Name] != "failure" {
			state[c.Name] = c.State
		}
	}
	for _, name := range repo.Required {
		if state[name] != "success" {
			return "check " + name + " is not green", false
		}
	}
	if len(repo.Required) == 0 && len(state) == 0 {
		return "no check has run", false
	}
	for name, st := range state {
		if st != "success" {
			return "check " + name + " is not green", false
		}
	}
	return "", false
}

// highRisk is the review gate's own rule for a path a person must sign off
// (.hanzo/review/review.sh): a workflow or the reviewer itself, or a path with a
// segment or a file stem naming identity, secrets, authorization, billing, payment,
// installation or release. TestRiskIsTheReviewGates holds the two in step.
func highRisk(file string) bool {
	for _, p := range []string{".github/workflows/", ".github/actions/", ".hanzo/workflows/", ".hanzo/review/"} {
		if strings.HasPrefix(file, p) {
			return true
		}
	}
	stem := strings.TrimSuffix(path.Base(file), path.Ext(path.Base(file)))
	return slices.ContainsFunc(strings.Split(file, "/"), func(p string) bool { return riskWords[p] }) || riskWords[stem]
}

var riskWords = map[string]bool{"iam": true, "kms": true, "auth": true, "authz": true, "billing": true, "pay": true, "installer": true, "release": true}
