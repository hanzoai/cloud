package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RunnerToken mints a registration token for one Actions runner. The forge
// hands the same kind of token to a person on its runner page; this is the
// machine asking for it, because the runner it registers is the one the cloud
// launches for a job the forge has just queued, and it lives exactly that long.
//
// Global, not scoped to an owner: the job's labels decide which runner takes
// it, and a runner registered under one namespace cannot take another's job
// even when the labels match. The forge answers its current global token and
// mints one only when none is active, so every runner may register with it.
func (c *Client) RunnerToken(ctx context.Context) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/admin/actions/runners/registration-token",
		out:    &out,
	}); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Token) == "" {
		return "", errors.New("forge: empty runner registration token")
	}
	return out.Token, nil
}

// Job is one Actions job. [Client.Queued] reads the queue and fills the first
// four fields; [Client.Jobs] reads a run and fills them all.
type Job struct {
	// ID is the job's id on the forge.
	ID int64
	// Owner is the namespace the job's repository lives under.
	Owner string
	// Repo is the repository's name.
	Repo string
	// Labels is what the job's runs-on asked for.
	Labels []string
	// Runner is the name of the runner that took the job; empty until one has.
	Runner string
	// Run is the id of the run the job belongs to.
	Run int64
	// Name is the job's name as the workflow declares it.
	Name string
	// Status and Conclusion are the forge's words, as on [Run].
	Status     string
	Conclusion string
	// URL is the job's page on the forge.
	URL string
	// StartedAt and EndedAt are zero while unknown.
	StartedAt time.Time
	EndedAt   time.Time
	// Steps are the job's steps, in order.
	Steps []Step
}

// queuePage is how many jobs one read of the queue asks for: the forge's
// largest page.
const queuePage = 50

// Queued lists every job waiting for a runner across the whole forge, oldest
// first. The listing is the site administrator's, so it answers on the machine
// client.
func (c *Client) Queued(ctx context.Context) ([]Job, error) {
	var jobs []Job
	for page := 1; page <= maxPages; page++ {
		var out struct {
			Jobs []struct {
				ID     int64    `json:"id"`
				URL    string   `json:"url"`
				Labels []string `json:"labels"`
			} `json:"jobs"`
			Total int `json:"total_count"`
		}
		q := url.Values{
			"status": {"queued"}, "sort": {"id"}, "order": {"asc"},
			"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(queuePage)},
		}
		if err := c.do(ctx, "/admin/actions/jobs", q, &out); err != nil {
			return nil, err
		}
		for _, j := range out.Jobs {
			owner, repo, ok := jobRepo(j.URL, j.ID)
			if !ok {
				return nil, fmt.Errorf("forge: job %d names no repository in %q", j.ID, j.URL)
			}
			jobs = append(jobs, Job{ID: j.ID, Owner: owner, Repo: repo, Labels: j.Labels})
		}
		if len(out.Jobs) < queuePage || page*queuePage >= out.Total {
			return jobs, nil
		}
	}
	return nil, fmt.Errorf("forge: more than %d jobs queued", maxPages*queuePage)
}

// jobRepo reads the repository out of a job's API address,
// <forge>/v1/repos/<owner>/<repo>/actions/jobs/<id>, which is the one place the
// listing names it.
func jobRepo(raw string, id int64) (owner, repo string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	p, found := strings.CutSuffix(u.Path, "/actions/jobs/"+strconv.FormatInt(id, 10))
	seg := strings.Split(p, "/")
	if !found || len(seg) < 3 || seg[len(seg)-3] != "repos" || seg[len(seg)-2] == "" || seg[len(seg)-1] == "" {
		return "", "", false
	}
	return seg[len(seg)-2], seg[len(seg)-1], true
}

// Runner is one Actions runner registered on the forge.
type Runner struct {
	// ID is the runner's id on the forge.
	ID int64
	// Name is what the runner registered as. A runner the platform starts for one
	// queued job is named for that job ([cloud.RunnerName]).
	Name string
	// Online is whether the forge heard from it within its offline window.
	Online bool
	// Busy is whether it is executing a job now.
	Busy bool
	// Disabled is whether the forge has stopped handing it jobs.
	Disabled bool
	// Ephemeral means it takes one job and leaves, so its absence afterwards is
	// not a fault.
	Ephemeral bool
	// Labels are what a job's runs-on selects it by.
	Labels []string
	// LastOnline is when it last called the forge, and LastActive when it last
	// worked a job; zero when the forge has not said.
	LastOnline time.Time
	LastActive time.Time
}

// runnerPage is how many runners one read of the registry asks for.
const runnerPage = 50

// Runners lists every runner registered on the forge. The fleet's runners
// register site-wide ([Client.RunnerToken]), so the listing is the site
// administrator's and answers on the machine client.
func (c *Client) Runners(ctx context.Context) ([]Runner, error) {
	var out []Runner
	for p := 1; p <= maxPages; p++ {
		var w struct {
			Runners []struct {
				ID        int64  `json:"id"`
				Name      string `json:"name"`
				Status    string `json:"status"`
				Busy      bool   `json:"busy"`
				Disabled  bool   `json:"disabled"`
				Ephemeral bool   `json:"ephemeral"`
				Labels    []struct {
					Name string `json:"name"`
				} `json:"labels"`
				LastOnline string `json:"last_online"`
				LastActive string `json:"last_active"`
			} `json:"runners"`
			Total int `json:"total_count"`
		}
		q := url.Values{"page": {strconv.Itoa(p)}, "limit": {strconv.Itoa(runnerPage)}}
		if err := c.Machine().do(ctx, "/admin/actions/runners", q, &w); err != nil {
			return nil, err
		}
		for _, r := range w.Runners {
			labels := make([]string, 0, len(r.Labels))
			for _, l := range r.Labels {
				labels = append(labels, l.Name)
			}
			out = append(out, Runner{
				ID: r.ID, Name: r.Name, Online: r.Status == "online", Busy: r.Busy,
				Disabled: r.Disabled, Ephemeral: r.Ephemeral, Labels: labels,
				LastOnline: when(r.LastOnline), LastActive: when(r.LastActive),
			})
		}
		if len(w.Runners) < runnerPage || len(out) >= w.Total {
			return out, nil
		}
	}
	return nil, fmt.Errorf("forge: more than %d runners registered", maxPages*runnerPage)
}
