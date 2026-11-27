package provider

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
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// A queued job on GitHub becomes a runner the same way a forge job does. The
// App is told through its webhook, this mints a just-in-time configuration for
// exactly that job — a runner that registers with it takes one job and is gone
// server-side when it exits — and platform launches it.
//
// A delivery is not enough on its own. GitHub hands a queued job to whichever
// idle runner with matching labels asks first, so a runner started for one job
// may take another; and a runner that dies before taking anything, or a delivery
// that started none, leaves a job queued with no runner until GitHub fails it at
// a day. So platform's queue loop also asks for a reconcile (planeGitHubRunners):
// GitHub's queued jobs counted against the runners that will take them.

// githubJob is the subset of GitHub's workflow_job delivery this acts on.
type githubJob struct {
	Action string `json:"action"`
	Job    struct {
		ID     int64    `json:"id"`
		Labels []string `json:"labels"`
	} `json:"workflow_job"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// jitConfig mints a one-job runner registration in owner's organization with
// the installation's own token, and answers the configuration and the
// registration's id. Organization scope, because that is the permission the
// App carries: organization_self_hosted_runners. The repository endpoint wants
// a repository's administration, which the App does not hold, and answered 403
// to every job.
func jitConfig(ctx context.Context, token, owner, name string, labels []string) (string, int64, error) {
	body, _ := json.Marshal(map[string]any{
		"name": name, "runner_group_id": 1, "labels": labels, "work_folder": "_work",
	})
	u := githubAPIBase + "/orgs/" + url.PathEscape(owner) + "/actions/runners/generate-jitconfig"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", 0, fmt.Errorf("github jitconfig %s: %d %s", owner, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		JIT    string `json:"encoded_jit_config"`
		Runner struct {
			ID int64 `json:"id"`
		} `json:"runner"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.JIT == "" {
		return "", 0, fmt.Errorf("github jitconfig %s: no configuration in the answer", owner)
	}
	return out.JIT, out.Runner.ID, nil
}

// withdraw deletes a registration no runner will take. The registration is
// named for its job, so one left behind by a launch the cluster refused makes
// every redelivery of that job answer 409 "already exists", and the job waits
// until GitHub fails it at a day.
func withdraw(ctx context.Context, token, owner string, id int64) error {
	u := fmt.Sprintf("%s/orgs/%s/actions/runners/%d", githubAPIBase, url.PathEscape(owner), id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("github runner %s/%d: %d %s", owner, id, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// jobStatus answers where GitHub has a workflow job now: queued, in_progress,
// completed, or waiting.
func jobStatus(ctx context.Context, token, owner, repo string, id int64) (string, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/actions/jobs/%d", githubAPIBase, url.PathEscape(owner), url.PathEscape(repo), id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github job %s/%s/%d: %d %s", owner, repo, id, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Status == "" {
		return "", fmt.Errorf("github job %s/%s/%d: no status in the answer", owner, repo, id)
	}
	return out.Status, nil
}

// ours reports whether a queued GitHub job asked for this fleet.
//
// GITHUB HAS RUNNERS OF ITS OWN AND THE FORGE DOES NOT. Every forge job is
// ours by construction; a GitHub job naming ubuntu-latest is answered by
// GitHub, and minting for it would put a pod on the cluster that nothing ever
// claims. So a runner is minted only for a job that named the fleet: the
// platform-and-architecture labels the hosts advertise, or the self-hosted
// label that means the same thing.
//
// A label names the fleet by ending in its architecture. linux-amd64 and
// linux-arm64 are the names to write; the org runners on the LAN also
// answered the older spellings their workflows still carry, lux-build-amd64
// among them, and nothing else answers those now.
//
// Only Linux is served here, because this mints a Kubernetes pod. macOS and
// Windows are real hosts registered with the forge, not pods, so a job asking
// for them is not something this path can answer — and it is refused before
// self-hosted is read, because self-hosted does not say Linux: GitHub's own
// spelling of a self-hosted Mac is [self-hosted, macOS, ARM64]. Which labels
// name them is [cloud.Podless], the rule platform refuses a launch by.
func ours(labels []string) bool {
	for _, l := range labels {
		if cloud.Podless(l) {
			return false
		}
	}
	for _, l := range labels {
		l = strings.ToLower(l)
		if l == "self-hosted" || strings.HasSuffix(l, "-amd64") || strings.HasSuffix(l, "-arm64") {
			return true
		}
	}
	return false
}

// handleGitHubJobEvent turns a queued job into a runner; every other action
// is answered 200 and ignored, so GitHub does not retry.
func handleGitHubJobEvent(s *cloud.Service[state], c *zip.Ctx, body []byte) error {
	var ev githubJob
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid workflow_job payload")
	}
	if ev.Action != "queued" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": ev.Action})
	}
	if ev.Installation.ID == 0 {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "no installation"})
	}
	if !ours(ev.Job.Labels) {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "not this fleet", "labels": ev.Job.Labels})
	}
	org, ok := OrgForExternalID("github", strconv.FormatInt(ev.Installation.ID, 10))
	if !ok {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "unknown installation"})
	}
	owner, repo := ev.Repository.Owner.Login, ev.Repository.Name
	token, err := ghApp.installationToken(c.Context(), ev.Installation.ID)
	if err != nil {
		s.Log.Error("github job: installation token", "org", org, "repo", repo, "err", err)
		return zip.Errorf(http.StatusBadGateway, "no installation token; redeliver")
	}
	// A delivery says the job WAS queued. The queue loop redelivers old
	// deliveries, and one for a job that has since run, failed or been cancelled
	// would start a runner GitHub never hands anything: it holds a slot until its
	// deadline. Ask GitHub where the job is now; if it cannot say, launch as before.
	if state, err := jobStatus(c.Context(), token, owner, repo, ev.Job.ID); err == nil && state != "queued" {
		// A runner is not bound to its job: GitHub hands the org's oldest queued
		// job with matching labels to whichever runner asks. So a job refused here
		// and then served by another job's runner leaves THAT job without one, and
		// the runner this delivery was for is still owed while any such job waits.
		if owed, oerr := queuedFor(c.Context(), token, owner, ev.Job.Labels); oerr != nil || !owed {
			return c.JSON(http.StatusOK, map[string]any{"ignored": "job is " + state, "job": ev.Job.ID})
		}
	}
	launched, err := startRunner(c.Context(), s, token, cloud.JobEvent{
		Org: org, Provider: "github", Repo: owner + "/" + repo, ID: ev.Job.ID, Labels: ev.Job.Labels,
	})
	if errors.Is(err, errMint) {
		s.Log.Error("github job: jit config", "org", org, "repo", repo, "job", ev.Job.ID, "err", err)
		return zip.Errorf(http.StatusBadGateway, "GitHub refused a runner registration; redeliver")
	}
	if err != nil {
		s.Log.Error("github job: runner not launched", "org", org, "repo", repo, "job", ev.Job.ID, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "the job was verified but no runner could be launched; redeliver")
	}
	s.Log.Info("github job queued", "org", org, "repo", repo, "job", ev.Job.ID, "labels", ev.Job.Labels, "runner", launched)
	return c.JSON(http.StatusOK, map[string]any{"org": org, "repo": repo, "job": ev.Job.ID, "runner": launched})
}

// errMint is GitHub refusing to mint a runner registration.
var errMint = errors.New("github refused a runner registration")

// startRunner mints a just-in-time registration for one queued job, under the
// name the Job that runs it takes, and launches that Job. A launch that fails
// withdraws the registration, because the next attempt mints the same name. A
// refused mint is errMint.
func startRunner(ctx context.Context, s *cloud.Service[state], token string, ev cloud.JobEvent) (string, error) {
	owner, repo, _ := strings.Cut(ev.Repo, "/")
	jit, id, err := jitConfig(ctx, token, owner, cloud.RunnerName("github", repo, ev.ID, ev.Try), ev.Labels)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errMint, err)
	}
	ev.Token = jit
	name, err := cloud.OnWorkflowJob(ctx, ev)
	if err != nil {
		if werr := withdraw(context.WithoutCancel(ctx), token, owner, id); werr != nil {
			s.Log.Error("github job: registration not withdrawn", "org", ev.Org, "repo", ev.Repo, "job", ev.ID, "err", werr)
		}
		return "", err
	}
	return name, nil
}

// queuedRepos bounds how many repositories eachQueued reads: the most recently
// pushed of those the installation was granted. A queued job belongs to a push,
// a dispatch or a schedule, and only a push moves a repository up this order —
// a dispatch or a schedule in a repository pushed to less recently than the
// thirtieth is not seen.
const queuedRepos = 30

// queuedJob is one workflow job GitHub holds queued for a runner.
type queuedJob struct {
	ID      int64     `json:"id"`
	Status  string    `json:"status"`
	Labels  []string  `json:"labels"`
	Created time.Time `json:"created_at"`
	// Repo is the repository's own name, which the jobs listing does not carry.
	Repo string `json:"-"`
}

// eachQueued hands visit each queued job, once, in runs of the given statuses
// in owner's queuedRepos most recently pushed repositories the installation was
// granted, until visit declines one. A job waits in a queued run and also in
// one in progress — a matrix leg, or a job whose needs just finished — but
// reading both costs a second call per repository, so only the reconcile asks
// for in-progress runs; a delivery asks for queued ones.
func eachQueued(ctx context.Context, token, owner string, statuses []string, visit func(queuedJob) bool) error {
	repos, err := installationRepos(ctx, token)
	if err != nil {
		return err
	}
	repos = slices.DeleteFunc(repos, func(r githubRepo) bool {
		return r.Archived || r.Disabled || !strings.EqualFold(r.Owner.Login, owner)
	})
	slices.SortStableFunc(repos, func(a, b githubRepo) int { return cmp.Compare(b.PushedAt, a.PushedAt) })
	seen := map[int64]bool{} // a run that starts between two listings is read in both
	for _, r := range repos[:min(len(repos), queuedRepos)] {
		for _, status := range statuses {
			var runs struct {
				Runs []struct {
					ID int64 `json:"id"`
				} `json:"workflow_runs"`
			}
			if err := githubGet(ctx, token, fmt.Sprintf("/repos/%s/%s/actions/runs?status=%s&per_page=20", url.PathEscape(owner), url.PathEscape(r.Name), status), &runs); err != nil {
				return err
			}
			for _, run := range runs.Runs {
				var jobs struct {
					Jobs []queuedJob `json:"jobs"`
				}
				if err := githubGet(ctx, token, fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=100", url.PathEscape(owner), url.PathEscape(r.Name), run.ID), &jobs); err != nil {
					return err
				}
				for _, j := range jobs.Jobs {
					if j.Status != "queued" || len(j.Labels) == 0 || seen[j.ID] {
						continue
					}
					seen[j.ID] = true
					j.Repo = r.Name
					if !visit(j) {
						return nil
					}
				}
			}
		}
	}
	return nil
}

// queuedFor answers whether any repository of owner holds a queued workflow job
// that a runner registered with labels can take.
func queuedFor(ctx context.Context, token, owner string, labels []string) (bool, error) {
	found := false
	err := eachQueued(ctx, token, owner, []string{"queued"}, func(j queuedJob) bool {
		found = fits(j.Labels, labels)
		return !found
	})
	return found, err
}

// fits reports whether a runner with labels can take a job asking for want,
// which is GitHub's rule: every label the job asks for, in any case.
func fits(want, labels []string) bool {
	have := make(map[string]bool, len(labels))
	for _, l := range labels {
		have[strings.ToLower(l)] = true
	}
	for _, l := range want {
		if !have[strings.ToLower(l)] {
			return false
		}
	}
	return true
}

// exposeRunners publishes the reconcile on the internal plane. Mount calls it.
func exposeRunners() {
	cloud.Plane().Post("/integrations/github/runners", planeGitHubRunners,
		zip.WithOperationID(client.IntegrationsGitHubRunners),
		zip.WithSummary("Start a runner for each queued GitHub job no runner will take"))
}

const (
	// runnerStarting is how long a runner may take to come online. An offline
	// one whose Job is younger is starting, and so is one whose Job does not
	// exist yet while the job it was minted for was queued less long ago — a
	// delivery launching it as the reconcile reads. Either counts as taking a
	// job. An older one is stuck, on a full node or an image, and does not.
	runnerStarting = 3 * time.Minute
	// runnerTries bounds the runners the reconcile names for one job past the
	// delivery's: -r1 to -r9, each started only once the one before was taken by
	// another job or finished.
	runnerTries = 9
	// reconcileTimeout bounds one reconcile, under the two minutes platform
	// waits for its answer, so a pass ends before its caller gives up on it.
	reconcileTimeout = 90 * time.Second
)

// reconciling is a reconcile in flight. Two never overlap: the second would
// read the queue the first is filling and start a second runner for each job.
var reconciling atomic.Bool

// sightings is, per installation, each registration the last reconcile found
// holding an owed job's runner name with no Job behind it.
var sightings = struct {
	sync.Mutex
	by map[int64]map[int64]bool
}{by: map[int64]map[int64]bool{}}

// failed is when each job's runner last failed to start for a reason other than
// a full cluster. For failedFor such a job is tried after every other: one that
// fails the same way each pass — a repository name or label no Job can carry, an
// installation GitHub refuses runners — would otherwise hold the front of the
// oldest-first queue, every org's, and spend each pass's attempts on itself.
var failed = struct {
	sync.Mutex
	at map[int64]time.Time
}{at: map[int64]time.Time{}}

// failedFor is how long a job whose runner failed to start waits behind the rest.
const failedFor = 30 * time.Minute

// ghRunner is one runner registered with an organization, as GitHub lists it.
type ghRunner struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Busy   bool   `json:"busy"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// labels is every label the runner answers to, the ones GitHub adds among them.
func (r ghRunner) labels() []string {
	out := make([]string, len(r.Labels))
	for i, l := range r.Labels {
		out[i] = l.Name
	}
	return out
}

// orgRunners lists every runner registered with owner.
func orgRunners(ctx context.Context, token, owner string) ([]ghRunner, error) {
	return walk(ctx, func(ctx context.Context, page int) ([]ghRunner, int, error) {
		var p struct {
			Total   int        `json:"total_count"`
			Runners []ghRunner `json:"runners"`
		}
		if err := githubGet(ctx, token, fmt.Sprintf("/orgs/%s/actions/runners?per_page=100&page=%d", url.PathEscape(owner), page), &p); err != nil {
			return nil, 0, err
		}
		return p.Runners, p.Total, nil
	})
}

// owed is a queued job no runner will take, and the runner to start for it.
type owed struct {
	job queuedJob
	// try names the runner (cloud.RunnerName).
	try int
	// stale is a dead registration holding that name, withdrawn before minting;
	// 0 for none.
	stale int64
}

// plan is what a reconcile does in one organization.
type plan struct {
	// owed is every job to start a runner for, oldest first.
	owed []owed
	// reclaim names the finished Jobs holding an owed job's runner names, which
	// platform deletes.
	reclaim []string
	// sighted is every registration found holding an owed job's runner name with
	// no Job behind it.
	sighted map[int64]bool
}

// short plans one organization: the queued jobs no runner will take, oldest
// first, and the runner to start for each.
//
// A runner takes one job whose labels are all among its own, and counts while it
// is idle: not busy, and online or starting. A runner whose Job finished never
// counts, whatever GitHub says, because GitHub reports a runner online for a
// while after it is gone. Each job, oldest first, takes the idle runner with the
// fewest labels that fits it, so a narrow runner is not spent on a job a wider
// one could take. jobs is every runner Job platform holds for GitHub, by name.
//
// A job's runners are named for it — try 0 the delivery's, 1 to runnerTries the
// reconcile's — and the first name nothing holds is the one started. A name is
// passed over while what holds it will not take this job: a runner busy with
// another, one counted for an older job, a finished Job (reclaimed). The job is
// left alone while what holds a name may yet take it: a runner stuck on the
// cluster, where a second would wait beside it, or a registration with no Job.
// That one is dead, or minted by a delivery whose Job is not listed yet, so it
// is withdrawn and its name reused only once the reconcile before this one found
// it so too (seen).
func short(queued []queuedJob, regs []ghRunner, jobs map[string]client.RunnerJob, seen map[int64]bool, now time.Time) plan {
	young := map[string]bool{}
	for _, q := range queued {
		if now.Sub(q.Created) < runnerStarting {
			young[cloud.RunnerName("github", q.Repo, q.ID, 0)] = true
		}
	}
	idle := func(r ghRunner) bool {
		if r.Busy {
			return false
		}
		j, ok := jobs[r.Name]
		switch {
		case !ok:
			return r.Status == "online" || young[r.Name]
		case j.Done:
			return false
		}
		return r.Status == "online" || now.Sub(time.Unix(j.Created, 0)) < runnerStarting
	}
	byName := make(map[string]ghRunner, len(regs))
	var free []ghRunner
	for _, r := range regs {
		byName[r.Name] = r
		if idle(r) {
			free = append(free, r)
		}
	}
	slices.SortStableFunc(free, func(a, b ghRunner) int { return cmp.Compare(len(a.Labels), len(b.Labels)) })
	queued = slices.Clone(queued)
	slices.SortStableFunc(queued, func(a, b queuedJob) int { return a.Created.Compare(b.Created) })
	p := plan{sighted: map[int64]bool{}}
	for _, q := range queued {
		if i := slices.IndexFunc(free, func(r ghRunner) bool { return fits(q.Labels, r.labels()) }); i >= 0 {
			free = slices.Delete(free, i, i+1)
			continue
		}
	names:
		for try := 0; try <= runnerTries; try++ {
			name := cloud.RunnerName("github", q.Repo, q.ID, try)
			j, job := jobs[name]
			r, reg := byName[name]
			switch {
			case job && j.Done:
				p.reclaim = append(p.reclaim, name)
			case job:
				if reg && !r.Busy && !idle(r) {
					break names // stuck
				}
			case reg:
				if r.Busy || idle(r) {
					continue
				}
				p.sighted[r.ID] = true
				if seen[r.ID] {
					p.owed = append(p.owed, owed{job: q, try: try, stale: r.ID})
				}
				break names
			default:
				p.owed = append(p.owed, owed{job: q, try: try})
				break names
			}
		}
	}
	return p
}

// planeGitHubRunners starts a runner for each job GitHub holds queued, in every
// organization an org connected, that no runner will take: oldest first, as a
// delivery starts one, until Free have been tried or the cluster refuses one.
// It answers the finished Jobs holding those jobs' runner names in Reclaim.
//
// It reads every connected installation, so no org rides the call: the caller
// states the cluster. A wrong statement starts at most Free runners a pass,
// each for a job GitHub holds queued — Free is that bound, not the cluster's
// ceiling, which counts only the runners a node has placed — and two in a row
// stating too few Jobs, none at all at worst, can withdraw a registration whose
// runner is starting. One reconcile runs at a time, within reconcileTimeout. A
// job whose runner failed to start is tried after the others for failedFor.
func planeGitHubRunners(ctx context.Context, in *client.GitHubRunnersIn) (*client.GitHubRunnersOut, error) {
	if in == nil {
		return nil, zip.ErrBadRequest("github runners: the cluster's runners are required")
	}
	s := mounted
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github runners: integrations is not serving")
	}
	if !reconciling.CompareAndSwap(false, true) {
		return nil, zip.Errorf(http.StatusConflict, "github runners: a reconcile is already running")
	}
	defer reconciling.Store(false)
	// DETACHED from the request that asked. That request is platform's internal
	// call, made for no org (queue.go), and while a request sits behind ctx zip
	// forwards ITS identity: every launch's cloud.For(ctx, org) was ignored and
	// platform refused each one with "platform job: org required" (2026-11-07:
	// no reconcile-started runner had run since it shipped). Detached, the org
	// OrgForExternalID found for the installation — the rule a delivery is held
	// to — is the org each launch states. The caller's own identity carries over
	// unchanged, so this names no org the installation's connection did not.
	ctx, cancel := context.WithTimeout(zip.Detach(ctx), reconcileTimeout)
	defer cancel()
	out := &client.GitHubRunnersOut{Reclaim: []string{}}
	if in.Free <= 0 {
		return out, nil
	}
	installs, err := appInstallations(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github runners: %v", err)
	}
	jobs := make(map[string]client.RunnerJob, len(in.Jobs))
	for _, j := range in.Jobs {
		jobs[j.Name] = j
	}
	type due struct {
		owed
		org, owner, token string
	}
	var (
		now = time.Now()
		mu  sync.Mutex
		wg  sync.WaitGroup
		all []due
	)
	for _, inst := range installs {
		// A runner registers with an organization, and a job is served only where
		// an org connected the installation — the rule a delivery is held to.
		if inst.Type != "Organization" {
			continue
		}
		org, ok := OrgForExternalID("github", strconv.FormatInt(inst.ID, 10))
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := ghApp.installationToken(ctx, inst.ID)
			var regs []ghRunner
			var queued []queuedJob
			if err == nil {
				regs, err = orgRunners(ctx, token, inst.Login)
			}
			if err == nil {
				err = eachQueued(ctx, token, inst.Login, []string{"queued", "in_progress"}, func(j queuedJob) bool {
					if ours(j.Labels) {
						queued = append(queued, j)
					}
					return true
				})
			}
			if err != nil {
				s.Log.Warn("github runners: queue not read", "org", org, "owner", inst.Login, "err", err)
				return
			}
			sightings.Lock()
			p := short(queued, regs, jobs, sightings.by[inst.ID], now)
			sightings.by[inst.ID] = p.sighted
			sightings.Unlock()
			mu.Lock()
			defer mu.Unlock()
			out.Reclaim = append(out.Reclaim, p.reclaim...)
			for _, o := range p.owed {
				all = append(all, due{o, org, inst.Login, token})
			}
		}()
	}
	wg.Wait()
	failed.Lock()
	behind := map[int64]bool{}
	for id, at := range failed.at {
		if now.Sub(at) >= failedFor {
			delete(failed.at, id)
		} else {
			behind[id] = true
		}
	}
	failed.Unlock()
	mark := func(id int64, bad bool) {
		failed.Lock()
		defer failed.Unlock()
		if bad {
			failed.at[id] = now
		} else {
			delete(failed.at, id)
		}
	}
	slices.SortStableFunc(all, func(a, b due) int {
		switch x, y := behind[a.job.ID], behind[b.job.ID]; {
		case x && !y:
			return 1
		case y && !x:
			return -1
		}
		return a.job.Created.Compare(b.job.Created)
	})
	started := map[string]int{}
	for i, d := range all {
		// Every attempt counts, not only a launch: a failed one still spends a
		// mint and a withdraw, and a fault that fails them all would spend both
		// on every owed job each pass.
		if i == in.Free {
			break
		}
		err := error(nil)
		if d.stale != 0 {
			err = withdraw(ctx, d.token, d.owner, d.stale)
		}
		if err == nil {
			_, err = startRunner(ctx, s, d.token, cloud.JobEvent{
				Org: d.org, Provider: "github", Repo: d.owner + "/" + d.job.Repo, ID: d.job.ID, Labels: d.job.Labels, Try: d.try,
			})
		}
		if errors.Is(err, cloud.ErrRunnerCapacity) {
			break
		}
		if err != nil {
			mark(d.job.ID, true)
			s.Log.Warn("github runners: runner not started", "org", d.org, "repo", d.owner+"/"+d.job.Repo, "job", d.job.ID, "err", err)
			continue
		}
		mark(d.job.ID, false)
		out.Started++
		started[d.org]++
	}
	for org, n := range started {
		s.Log.Info("github runners started", "org", org, "count", n)
	}
	return out, nil
}
