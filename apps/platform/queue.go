// queue.go — a queued workflow job whose delivery started no runner.
//
// A delivery starts a runner (job.go), and a delivery can fail to: the cluster is
// at its runner ceiling, or cloud is restarting when it lands. Neither the forge
// nor GitHub delivers a job twice, so every queueInterval, while a runner slot is
// free, this loop starts what the deliveries did not:
//
//   - the forge: each job its queue still holds gets a runner through launch.
//     The Job name is the job's own, so a job that has a runner collides and is
//     passed over.
//   - GitHub: each queued workflow_job delivery answered 429 or 5xx, and never
//     answered 2xx since, is delivered again, and its receiver starts the runner.
//   - GitHub, every githubEvery ticks: its queue is counted against the runners
//     that will take it (reconcileGitHub), and a runner is started for each job
//     beyond them.
//
// GitHub hands a queued job to any idle runner whose labels match, so a runner is
// not bound to the job it was started for. What keeps the fleet whole is one
// runner per queued job. A delivery cannot see that: a runner that died before
// taking a job, a delivery answered 200 that started none, or a runner taken by
// another job leave a job queued with no runner, and GitHub fails it at a day.
// Only counting finds those.

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	// queueInterval is how often the queues are read.
	queueInterval = 30 * time.Second
	// githubHorizon bounds how far back the delivery log is read. GitHub fails a
	// job that has waited a day for a runner, so an older delivery starts nothing.
	githubHorizon = 24 * time.Hour
	// githubPages bounds one read of the delivery log, at 100 deliveries a page.
	githubPages = 50
	// githubTries is how many failed redeliveries leave a job alone.
	githubTries = 5
	// githubEvery is how many ticks pass between reconciles of GitHub's queue —
	// ten, five minutes — because a reconcile is not cheap. Each installation
	// spends 1 call listing its runners, 1 per hundred repositories it was
	// granted, 60 on the queued and in-progress runs of the 30 most recently
	// pushed, and 1 per such run on its jobs: 62 and one per active run for an
	// installation of up to a hundred repositories, about 750 an hour of the
	// 5,000 its token may spend. Every tick would be 7,440 an hour, past the
	// limit. The App itself spends 1, listing its installations.
	githubEvery = 10
	// githubReconcile bounds the wait for one reconcile. Provider bounds its own
	// pass under it and runs one at a time, so a pass this gave up on cannot
	// overlap the next.
	githubReconcile = 2 * time.Minute
)

func runQueue(s *cloud.Service[state], ctx context.Context) {
	lead := s.Coord.Lead("platform:queue")
	t := time.NewTicker(queueInterval)
	defer t.Stop()
	due := false // a GitHub reconcile is owed, and waits for a pass that can run it
	for tick := 1; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			due = due || tick%githubEvery == 0
			lead.Do(ctx, func(ctx context.Context) {
				if drain(s, ctx, due) {
					due = false
				}
			})
		}
	}
}

// drain fills the free runner slots from the forge's queue, then from GitHub's
// failed deliveries, then — when github is set — from GitHub's queue counted
// against its runners, and answers whether it reconciled GitHub's queue. A full
// cluster reads none.
func drain(s *cloud.Service[state], ctx context.Context, github bool) bool {
	k := s.State.k8s
	ns, err := ownNamespace()
	if err != nil || k.ready() != nil {
		return false
	}
	r, err := k.activeRunners(ctx, ns)
	if err != nil {
		s.Log.Warn("runner queue: count runners", "err", err)
		return false
	}
	free := k.limits.maxConcurrentRunners() - r.bound.total()
	if free <= 0 {
		return false
	}
	if fc, err := forge.Dial(ctx, cloud.KMSPeer{}); err != nil {
		s.Log.Warn("runner queue: no forge client", "err", err)
	} else {
		free -= queued(s, ctx, fc.Machine())
	}
	if free <= 0 {
		return false
	}
	hc, base, err := githubApp()
	if err != nil {
		s.Log.Warn("runner queue: github app", "err", err)
		return false
	}
	if hc == nil {
		return false
	}
	n, err := redeliver(ctx, hc, base, free)
	if err != nil {
		s.Log.Warn("runner queue: github deliveries", "redelivered", n, "err", err)
	} else if n > 0 {
		s.Log.Info("runner queue: github jobs delivered again", "count", n)
	}
	// A redelivery starts its runner when it lands, seconds from now, and a
	// reconcile reading GitHub meanwhile would find that runner registered with
	// no Job. Provider withdraws such a registration only when two reconciles in
	// a row find it so; a pass that redelivered also leaves the reconcile to the
	// next.
	if !github || n > 0 {
		return false
	}
	reconcileGitHub(s, ctx, ns, free)
	return true
}

// reconcileGitHub has provider start a runner for each job GitHub holds queued
// that no runner will take, at most free of them, and deletes the finished Jobs
// holding the names those runners need, so the next reconcile can start them.
//
// Provider reads GitHub, because it holds the installations and which org
// connected each — a job is served only where an org did, the rule a delivery is
// held to — and it mints the registrations. Platform holds the Jobs, so it
// states them: which exist, which finished, and when each was created.
func reconcileGitHub(s *cloud.Service[state], ctx context.Context, ns string, free int) {
	k := s.State.k8s
	jobs, err := k.githubRunners(ctx, ns)
	if err != nil {
		s.Log.Warn("runner queue: list github runners", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, githubReconcile)
	defer cancel()
	out, err := providerpeer.IntegrationsGitHubRunners(cloud.For(ctx, ""), &client.GitHubRunnersIn{Free: free, Jobs: jobs})
	if err != nil {
		s.Log.Warn("runner queue: github reconcile", "err", err)
		return
	}
	done := map[string]bool{}
	for _, j := range jobs {
		done[j.Name] = j.Done
	}
	for _, name := range out.Reclaim {
		if !done[name] {
			continue // only a Job this pass stated finished
		}
		gone, err := k.reclaimSpentRunner(ctx, ns, name)
		if err != nil {
			s.Log.Warn("runner queue: spent runner not reclaimed", "runner", name, "err", err)
		} else if gone {
			s.Log.Info("runner queue: spent runner reclaimed", "runner", name)
		}
	}
}

// fairOrder answers the forge's queue, oldest first, one repository at a time: each
// repository's oldest job, then each one's next, repositories in the order of their
// oldest job. A repository with a hundred jobs waiting does not hold every slot ahead of
// one with a single job.
func fairOrder(jobs []forge.Job) []forge.Job {
	var repos []string
	by := map[string][]forge.Job{}
	for _, j := range jobs {
		k := j.Owner + "/" + j.Repo
		if _, ok := by[k]; !ok {
			repos = append(repos, k)
		}
		by[k] = append(by[k], j)
	}
	out := make([]forge.Job, 0, len(jobs))
	for round := 0; len(out) < len(jobs); round++ {
		for _, k := range repos {
			if round < len(by[k]) {
				out = append(out, by[k][round])
			}
		}
	}
	return out
}

// queued starts a runner for each job the forge holds queued, a repository at a time
// (fairOrder), until the ceiling refuses one, and answers how many it started.
func queued(s *cloud.Service[state], ctx context.Context, fc *forge.Client) int {
	jobs, err := fc.Queued(ctx)
	if err != nil {
		if errors.Is(err, forge.ErrCredentialRejected) {
			forge.Invalidate()
		}
		s.Log.Warn("runner queue: forge queue", "err", err)
		return 0
	}
	started, token := 0, ""
	for _, j := range fairOrder(jobs) {
		org, err := forge.Org(j.Owner)
		if err != nil {
			continue // a namespace no org owns, refused on delivery the same way
		}
		if _, err := runnerNode(j.Labels); err != nil {
			continue // a macOS or Windows job, which a host takes and a pod cannot
		}
		if token == "" {
			if token, err = fc.RunnerToken(ctx); err != nil {
				s.Log.Warn("runner queue: forge registration token", "err", err)
				return started
			}
		}
		repo := j.Owner + "/" + j.Repo
		name, err := launch(s, ctx, cloud.JobEvent{
			Org: org, Provider: "forge", Repo: repo, ID: j.ID,
			Labels: j.Labels, URL: "https://" + fc.Host(), Token: token,
		})
		switch {
		case err == nil:
			started++
			s.Log.Info("runner queue: forge job started", "org", org, "repo", repo, "job", j.ID, "runner", name)
		case errors.Is(err, errForgeShareFull):
			continue // this architecture's share is full; an older job of it waits, a newer one of the other may start
		case errors.Is(err, errTooManyRunners):
			return started
		case apierrors.IsAlreadyExists(err):
			// The job has its runner — unless that runner is SPENT. An ephemeral
			// runner takes whatever task the forge hands it, so the Job named for
			// this job may have run another repo's and exited while this one is
			// still queued. Reclaiming it lets the next tick launch a fresh one
			// instead of waiting out the Job's TTL.
			ns, nsErr := ownNamespace()
			if nsErr != nil {
				break
			}
			gone, rErr := s.State.k8s.reclaimSpentRunner(ctx, ns, cloud.RunnerName("forge", j.Repo, j.ID, 0))
			if rErr != nil {
				s.Log.Warn("runner queue: spent runner not reclaimed", "repo", repo, "job", j.ID, "err", rErr)
			} else if gone {
				s.Log.Info("runner queue: spent runner reclaimed", "repo", repo, "job", j.ID)
			}
		default:
			s.Log.Warn("runner queue: forge job not started", "repo", repo, "job", j.ID, "err", err)
		}
	}
	return started
}

// githubApp is a client signing each request as the GitHub App, from the App
// credentials the deployment already carries, and the API it addresses. The
// client is nil on a deployment with no App.
func githubApp() (*http.Client, string, error) {
	id, key := env.Or("GITHUB_APP_ID", ""), env.Or("GITHUB_APP_PRIVATE_KEY", "")
	if id == "" || key == "" {
		return nil, "", nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("GITHUB_APP_ID must be numeric: %w", err)
	}
	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, n, []byte(key))
	if err != nil {
		return nil, "", fmt.Errorf("load github app key: %w", err)
	}
	base := strings.TrimRight(env.Or("GITHUB_API_URL", "https://api.github.com"), "/")
	tr.BaseURL = base
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}, base, nil
}

// hook is one attempt at delivering an App webhook, as GitHub records it.
type hook struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
	Action      string    `json:"action"`
}

// redeliver asks GitHub to deliver again each queued workflow_job whose attempts
// were all answered 429 or 5xx, oldest first and at most free of them, and
// answers how many it asked for.
func redeliver(ctx context.Context, hc *http.Client, base string, free int) (int, error) {
	type job struct {
		id          int64     // the newest attempt
		first, last time.Time // the first attempt and the newest
		code        int       // the newest attempt's answer
		tries       int       // redeliveries that failed
	}
	now := time.Now()
	failed := map[string]*job{}
	if _, err := hooks(ctx, hc, base, "failure", now.Add(-githubHorizon), func(d hook) bool {
		if d.Event != "workflow_job" || d.Action != "queued" {
			return true
		}
		j := failed[d.GUID]
		if j == nil {
			j = &job{id: d.ID, last: d.DeliveredAt, code: d.StatusCode}
			failed[d.GUID] = j
		}
		j.first = d.DeliveredAt
		if d.Redelivery {
			j.tries++
		}
		return true
	}); err != nil {
		return 0, err
	}
	// Due: refused for capacity or a fault, under the cap, and past a wait that
	// grows by a minute with each failed redelivery.
	due := map[string]*job{}
	oldest := now
	for guid, j := range failed {
		if (j.code == http.StatusTooManyRequests || j.code >= 500) && j.tries < githubTries &&
			now.Sub(j.last) >= time.Duration(j.tries+1)*time.Minute {
			due[guid] = j
			if j.first.Before(oldest) {
				oldest = j.first
			}
		}
	}
	if len(due) == 0 {
		return 0, nil
	}
	// A job with any successful attempt has started its runner. Only a job whose
	// whole history the success log covered is asked again.
	reached, err := hooks(ctx, hc, base, "success", oldest, func(d hook) bool {
		delete(due, d.GUID)
		return len(due) > 0
	})
	if err != nil {
		return 0, err
	}
	var refused []error
	n := 0
	for _, j := range slices.SortedFunc(maps.Values(due), func(a, b *job) int { return a.first.Compare(b.first) }) {
		if n == free {
			break
		}
		if j.first.Before(reached) {
			continue
		}
		if err := attempt(ctx, hc, base, j.id); err != nil {
			refused = append(refused, err)
			continue
		}
		n++
	}
	return n, errors.Join(refused...)
}

// hooks reads the App's delivery log newest first for one status, success or
// failure, handing each delivery to visit until visit declines one, the log
// passes since, or githubPages have been read. It answers how far back the read
// reached: since, unless it stopped at the page bound.
func hooks(ctx context.Context, hc *http.Client, base, status string, since time.Time, visit func(hook) bool) (time.Time, error) {
	next := base + "/app/hook/deliveries?per_page=100&status=" + status
	for page := range githubPages {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return since, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := hc.Do(req)
		if err != nil {
			return since, fmt.Errorf("github deliveries: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return since, fmt.Errorf("github deliveries: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return since, fmt.Errorf("github deliveries: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var list []hook
		if err := json.Unmarshal(body, &list); err != nil {
			return since, fmt.Errorf("github deliveries: %w", err)
		}
		for _, d := range list {
			if d.DeliveredAt.Before(since) || !visit(d) {
				return since, nil
			}
		}
		if next = nextPage(resp.Header.Get("Link")); next == "" || !strings.HasPrefix(next, base+"/") {
			return since, nil
		}
		if len(list) > 0 && page == githubPages-1 {
			return list[len(list)-1].DeliveredAt, nil
		}
	}
	return since, nil
}

// nextPage is the rel="next" address of a Link header, or "" on the last page.
func nextPage(link string) string {
	for part := range strings.SplitSeq(link, ",") {
		if u, rel, ok := strings.Cut(part, ";"); ok && strings.Contains(rel, `rel="next"`) {
			return strings.Trim(strings.TrimSpace(u), "<>")
		}
	}
	return ""
}

// attempt asks GitHub to deliver one recorded delivery again.
func attempt(ctx context.Context, hc *http.Client, base string, id int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/app/hook/deliveries/"+strconv.FormatInt(id, 10)+"/attempts", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("github redeliver %d: %w", id, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("github redeliver %d: %d", id, resp.StatusCode)
	}
	return nil
}
