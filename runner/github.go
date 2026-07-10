// GitHub REST client adapted to the JIT daemon's needs: enumerate
// queued jobs across an org's repos, mint JIT runner configs. Direct
// HTTP (one stdlib client) keeps deps minimal and allows per-request
// token selection.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const userAgent = "hanzo-arcd/1"

type ghClient struct {
	httpc *http.Client
	tp    *TokenProvider
	base  string

	// repoTTL bounds how long ListRepos returns a cached snapshot before
	// re-fetching. Zero disables the cache (re-fetch every call).
	repoTTL time.Duration

	mu          sync.Mutex
	repoCache   map[string][]Repo    // org -> repos
	repoCacheAt map[string]time.Time // org -> last successful fetch
}

type Repo struct {
	Owner    string `json:"-"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Disabled bool   `json:"disabled"`
	Archived bool   `json:"archived"`
	Fork     bool   `json:"fork"`
}

type WorkflowRun struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	HeadSHA string `json:"head_sha"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url"`
	Repo    *Repo  `json:"repository,omitempty"`
}

type WorkflowJob struct {
	ID           int64    `json:"id"`
	RunID        int64    `json:"run_id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Labels       []string `json:"labels"`
	HTMLURL      string   `json:"html_url"`
	WorkflowName string   `json:"workflow_name"`
}

type JITRunnerConfig struct {
	EncodedJITConfig string `json:"encoded_jit_config"`
	Runner           struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"runner"`
}

func newGHClient(tp *TokenProvider, base string, repoTTL time.Duration) *ghClient {
	return &ghClient{
		httpc:       &http.Client{Timeout: 45 * time.Second},
		tp:          tp,
		base:        strings.TrimRight(base, "/"),
		repoTTL:     repoTTL,
		repoCache:   make(map[string][]Repo),
		repoCacheAt: make(map[string]time.Time),
	}
}

func (c *ghClient) do(ctx context.Context, method, org, path string, body, out any) (int, http.Header, error) {
	tok, err := c.tp.Token(ctx, org)
	if err != nil {
		return 0, nil, err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return resp.StatusCode, resp.Header, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		c.tp.RotatePAT()
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, resp.Header, fmt.Errorf("decode %s %s: %w (body: %s)", method, path, err, snippet(b))
		}
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, resp.Header, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, snippet(b))
	}
	return resp.StatusCode, resp.Header, nil
}

func snippet(b []byte) string {
	if len(b) > 512 {
		return string(b[:512]) + "..."
	}
	return string(b)
}

// ListRepos lists all repos in the org. Returns a cached snapshot if one
// was fetched within repoTTL; otherwise paginates /orgs/{org}/repos and
// repopulates the cache.
//
// The repo set of an org changes on the order of days (new repo, archive,
// transfer) so re-fetching every poll cycle is wasted budget. Without a
// cache, orgs with hundreds of repos use 5-50 API calls per cycle just to
// re-read the same list — exhausting the per-token rate budget before
// the daemon ever gets to the /actions/runs polls that actually surface
// queued jobs.
func (c *ghClient) ListRepos(ctx context.Context, org string) ([]Repo, error) {
	if c.repoTTL > 0 {
		c.mu.Lock()
		cached, ok := c.repoCache[org]
		fetchedAt := c.repoCacheAt[org]
		c.mu.Unlock()
		if ok && time.Since(fetchedAt) < c.repoTTL {
			return cached, nil
		}
	}

	all := []Repo{}
	page := 1
	for {
		u := fmt.Sprintf("/orgs/%s/repos?per_page=100&type=all&page=%d", url.PathEscape(org), page)
		var batch []Repo
		_, _, err := c.do(ctx, "GET", org, u, nil, &batch)
		if err != nil {
			// Cache fall-through: if we have a previous successful snapshot,
			// keep using it instead of bubbling the error up and starving the
			// daemon. Stale repo list is strictly better than no scan at all
			// — a brand-new repo is missed for one TTL, every other repo
			// keeps getting its queued jobs picked up.
			if c.repoTTL > 0 {
				c.mu.Lock()
				cached, ok := c.repoCache[org]
				c.mu.Unlock()
				if ok {
					return cached, nil
				}
			}
			return nil, fmt.Errorf("list repos org=%s: %w", org, err)
		}
		for i := range batch {
			batch[i].Owner = org
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
		page++
		if page > 50 {
			break // sanity
		}
	}
	c.mu.Lock()
	c.repoCache[org] = all
	c.repoCacheAt[org] = time.Now()
	c.mu.Unlock()
	return all, nil
}

// ListQueuedJobs returns every queued job across all queued runs in a repo.
func (c *ghClient) ListQueuedJobs(ctx context.Context, repo Repo) ([]WorkflowJob, error) {
	runs := []WorkflowRun{}
	page := 1
	for {
		u := fmt.Sprintf("/repos/%s/%s/actions/runs?status=queued&per_page=50&page=%d", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), page)
		var resp struct {
			TotalCount   int           `json:"total_count"`
			WorkflowRuns []WorkflowRun `json:"workflow_runs"`
		}
		_, _, err := c.do(ctx, "GET", repo.Owner, u, nil, &resp)
		if err != nil {
			return nil, fmt.Errorf("list runs %s/%s: %w", repo.Owner, repo.Name, err)
		}
		for i := range resp.WorkflowRuns {
			resp.WorkflowRuns[i].Repo = &repo
			runs = append(runs, resp.WorkflowRuns[i])
		}
		if len(resp.WorkflowRuns) < 50 {
			break
		}
		page++
		if page > 5 {
			break
		}
	}
	jobs := []WorkflowJob{}
	for _, run := range runs {
		var jr struct {
			Jobs []WorkflowJob `json:"jobs"`
		}
		u := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=50", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), run.ID)
		_, _, err := c.do(ctx, "GET", repo.Owner, u, nil, &jr)
		if err != nil {
			return nil, fmt.Errorf("list jobs run=%d repo=%s/%s: %w", run.ID, repo.Owner, repo.Name, err)
		}
		for _, j := range jr.Jobs {
			if j.Status == "queued" {
				jobs = append(jobs, j)
			}
		}
	}
	return jobs, nil
}

// MintJITConfig requests a new JIT runner config for the given org.
// Runner is registered into the org's default runner group (id=1) with
// the host's labels. Runner name is unique per call so concurrent
// daemons can't collide.
func (c *ghClient) MintJITConfig(ctx context.Context, org, name string, labels []string, runnerGroupID int64) (*JITRunnerConfig, error) {
	body := map[string]any{
		"name":            name,
		"runner_group_id": runnerGroupID,
		"labels":          labels,
		"work_folder":     "_work",
	}
	out := &JITRunnerConfig{}
	_, _, err := c.do(ctx, "POST", org, "/orgs/"+url.PathEscape(org)+"/actions/runners/generate-jitconfig", body, out)
	if err != nil {
		return nil, fmt.Errorf("mint jitconfig org=%s: %w", org, err)
	}
	return out, nil
}
