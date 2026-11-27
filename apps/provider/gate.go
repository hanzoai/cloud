package provider

// gate.go — the forge judges a pull request, and its verdict is the status
// `hanzo/ci` on the exact head GitHub shows.
//
// The forge runs a workflow's `pull_request` trigger only for a pull request it
// holds, so a same-repository pull request is proposed there too (gateOpen) and
// every delivery of a forge job on it writes what the commit's runs say (gateJob).
// A status belongs to one commit: a new head starts bare. Green is proven (judge).
// A fork is never fetched, so no workflow of ours holds a secret beside its code;
// its head gets an error status that says so.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
)

const (
	gateContext = "hanzo/ci"
	gateBudget  = 90 * time.Second
	gateText    = 140 // the most of a description GitHub keeps
)

// The states of a GitHub commit status.
const (
	gatePending = "pending"
	gateSuccess = "success"
	gateFailure = "failure"
	gateError   = "error"
)

var (
	// gateForge is the forge as the deployment, which no person stands behind.
	gateForge = func(ctx context.Context, s *cloud.Service[state]) (*forge.Client, error) {
		fc, err := forge.Dial(ctx, s.State.kms)
		if err != nil {
			return nil, err
		}
		return fc.Machine(), nil
	}
	// gateToken is the App's installation token for the repository.
	gateToken = func(ctx context.Context, owner, repo string) (string, error) {
		id, err := ghApp.installationOf(ctx, owner, repo)
		if err != nil {
			return "", err
		}
		return ghApp.installationToken(ctx, id)
	}
)

// gatePull is the part of a GitHub pull request the gate reads.
type gatePull struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Head   struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// fork is whether the head lives in a repository other than owner/repo.
func (p gatePull) fork(owner, repo string) bool {
	return p.Head.Repo == nil || !strings.EqualFold(p.Head.Repo.FullName, owner+"/"+repo)
}

// gateRun runs f off the delivery that caused it, for an owner the forge maps to
// an org. A forge or GitHub that does not answer costs one status and a log line.
func gateRun(s *cloud.Service[state], owner, what string, f func(context.Context) error) {
	org, err := forge.Org(owner)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), gateBudget)
		defer cancel()
		if err := f(ctx); err != nil {
			s.Log.Warn("gate: "+what+" failed", "owner", owner, "err", err)
		}
	}()
}

// gateOpen has the forge hold p, once the repository runs workflows and is not a
// mirror and the branch is on the forge at the head GitHub names.
func gateOpen(ctx context.Context, s *cloud.Service[state], owner, repo string, p gatePull) error {
	head, base := p.Head.Ref, p.Base.Ref
	if !fullSHA.MatchString(p.Head.SHA) || !refName.MatchString(head) || !refName.MatchString(base) {
		return nil
	}
	tok, err := gateToken(ctx, owner, repo)
	if err != nil {
		return err
	}
	if p.fork(owner, repo) {
		return gateWrite(ctx, tok, owner, repo, p.Head.SHA, gateError, "a fork's pull request is not run on the forge", "")
	}
	fc, err := gateForge(ctx, s)
	if err != nil {
		return err
	}
	r, err := fc.Repo(ctx, owner, repo)
	if err != nil || r.Mirror || r.Actions == nil || !*r.Actions {
		return err
	}
	at, err := fc.Resolve(ctx, owner, repo, head)
	if errors.Is(err, forge.ErrNotFound) || err == nil && !strings.EqualFold(at, p.Head.SHA) {
		return nil // not here yet; the forge says so when it lands
	}
	if err != nil {
		return err
	}
	held, found, err := fc.PullFor(ctx, owner, repo, base, head)
	if err != nil || found && held.State == "open" {
		return err
	}
	_, err = fc.Propose(ctx, owner, repo, base, head, fmt.Sprintf("GitHub #%d: %s", p.Number, p.Title), "")
	return err
}

// gateClosed lets the forge's pull request go when GitHub closed its own.
func gateClosed(ctx context.Context, s *cloud.Service[state], owner, repo string, p gatePull) error {
	fc, err := gateForge(ctx, s)
	if err != nil {
		return err
	}
	held, found, err := fc.PullFor(ctx, owner, repo, p.Base.Ref, p.Head.Ref)
	if err != nil || !found || held.State != "open" {
		return err
	}
	return fc.Withdraw(ctx, owner, repo, held.Number)
}

// gateLanded is the forge's word that a branch landed: the open pull request
// GitHub holds from it, if any, is held there too.
func gateLanded(ctx context.Context, s *cloud.Service[state], owner, repo, branch string) error {
	if !refName.MatchString(branch) {
		return nil
	}
	tok, err := gateToken(ctx, owner, repo)
	if err != nil {
		return err
	}
	var open []gatePull
	q := url.Values{"state": {"open"}, "head": {owner + ":" + branch}, "per_page": {"10"}}
	if err := githubGet(ctx, tok, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls?"+q.Encode(), &open); err != nil {
		return err
	}
	var errs []error
	for _, p := range open {
		errs = append(errs, gateOpen(ctx, s, owner, repo, p))
	}
	return errors.Join(errs...)
}

// gateJob writes what the runs on the commit of run id say, for a pull request's
// run. Deliveries of one commit are written in turn, so the last to arrive writes
// last, from what the forge says by then.
func gateJob(ctx context.Context, s *cloud.Service[state], owner, repo string, id int64) error {
	if id <= 0 {
		return nil
	}
	fc, err := gateForge(ctx, s)
	if err != nil {
		return err
	}
	run, err := fc.Run(ctx, owner, repo, id)
	if err != nil || run.Event != "pull_request" || !fullSHA.MatchString(run.SHA) {
		return err
	}
	defer s.State.flight.lock("gate\x00" + owner + "/" + repo + "@" + run.SHA)()
	all, err := fc.Runs(ctx, owner, repo, forge.RunQuery{SHA: run.SHA})
	if err != nil {
		return err
	}
	runs := slices.DeleteFunc(all, func(r forge.Run) bool { return r.Event != "pull_request" })
	jobs := make([][]forge.Job, len(runs))
	for _, i := range forge.Counted(runs) {
		if jobs[i], err = fc.Jobs(ctx, owner, repo, runs[i].ID); err != nil {
			return err
		}
	}
	state, text, target := judge(runs, jobs)
	tok, err := gateToken(ctx, owner, repo)
	if err != nil {
		return err
	}
	return gateWrite(ctx, tok, owner, repo, run.SHA, state, text, target)
}

// judge is the status the runs on one commit add up to, and the run it speaks
// for. jobs[i] are runs[i]'s, and only the counted runs' are read. No run is
// pending; success is every run completed as success with a job that ran.
func judge(runs []forge.Run, jobs [][]forge.Job) (state, text, target string) {
	counted := forge.Counted(runs)
	if len(counted) == 0 {
		return gatePending, "waiting for the forge to run this commit", ""
	}
	// The newest the forge numbered speaks, a later attempt beating an earlier one.
	speaks := slices.MaxFunc(counted, func(a, b int) int {
		return cmp.Or(cmp.Compare(runs[a].Number, runs[b].Number), cmp.Compare(runs[a].Attempt, runs[b].Attempt))
	})
	target = runs[speaks].URL
	attempt := ""
	if runs[speaks].Attempt > 1 {
		attempt = " (attempt " + strconv.Itoa(runs[speaks].Attempt) + ")"
	}
	switch forge.Verdict(runs, jobs) {
	case forge.Queued:
		return gatePending, "queued on the forge" + attempt, target
	case forge.Running:
		return gatePending, "running on the forge" + attempt, target
	case forge.Failed:
		for _, i := range counted {
			for _, j := range jobs[i] {
				if j.Conclusion != "" && j.Conclusion != "success" && j.Conclusion != "skipped" {
					state = gateFailure
					if j.Conclusion == "cancelled" {
						state = gateError
					}
					return state, j.Name + " " + j.Conclusion + attempt, runs[i].URL
				}
			}
		}
		return gateFailure, runs[speaks].Workflow + " " + runs[speaks].Conclusion + attempt, target
	case forge.Passed:
		for _, i := range counted {
			if runs[i].Conclusion != "success" || !slices.ContainsFunc(jobs[i], func(j forge.Job) bool { return j.Conclusion == "success" }) {
				return gateError, "the forge ran none of this change's jobs" + attempt, target
			}
		}
		return gateSuccess, "passed on the forge" + attempt, target
	}
	return gatePending, "waiting for the forge to run this commit", ""
}

// gateWrite sets the status on a commit of owner/repo, linked to target when the
// status speaks for a run.
func gateWrite(ctx context.Context, tok, owner, repo, sha, state, text, target string) error {
	if !fullSHA.MatchString(sha) {
		return fmt.Errorf("gate: %q is not a full commit", sha)
	}
	desc := []rune(strings.Join(strings.Fields(text), " "))
	if len(desc) > gateText {
		desc = append(desc[:gateText-1], '…')
	}
	body := map[string]string{"state": state, "context": gateContext, "description": string(desc)}
	if target != "" {
		body["target_url"] = target
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(githubAPIBase, "/")+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/statuses/"+sha, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return githubStatus{code: resp.StatusCode, body: truncateBody(b)}
	}
	return nil
}
