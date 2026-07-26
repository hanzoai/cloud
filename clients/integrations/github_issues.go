package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// github_issues.go mirrors GitHub issues into the native tracker (cloud.UpsertIssue):
// the App webhook keeps them LIVE (`issues` / `issue_comment` events), and the
// backfill endpoint seeds the EXISTING issues across an org's granted repos. Both
// build the SAME cloud.IssueUpsert and go through the ONE tracker sink — no second
// path, no tracker import (the tracker_seam inversion).

// The tracker team every mirrored GitHub issue files under (repo is the per-issue
// discriminator within it).
const (
	githubTrackerProjectKey  = "GH"
	githubTrackerProjectName = "GitHub"
)

// githubIssueEvent is the slice of GitHub's `issues` / `issue_comment` webhook
// payloads we mirror — both carry issue + repository + installation.
type githubIssueEvent struct {
	Action     string      `json:"action"`
	Issue      githubIssue `json:"issue"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// githubIssue is the issue object, shared by the webhook payload and the REST list.
type githubIssue struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	State    string `json:"state"`
	HTMLURL  string `json:"html_url"`
	Assignee *struct {
		Login string `json:"login"`
	} `json:"assignee"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	// PullRequest is set when the REST /issues endpoint returns a PR (it mixes both
	// in); a real `issues` webhook never sets it. We SKIP these — a PR is not an issue.
	PullRequest *json.RawMessage `json:"pull_request,omitempty"`
}

// assigneeOf resolves the single assignee, preferring the (deprecated) `assignee`
// then the first of `assignees`.
func assigneeOf(is githubIssue) string {
	if is.Assignee != nil && is.Assignee.Login != "" {
		return is.Assignee.Login
	}
	for _, a := range is.Assignees {
		if a.Login != "" {
			return a.Login
		}
	}
	return ""
}

// mirrorGitHubIssue upserts one GitHub issue into org's tracker via the seam,
// returning (created, error). The ExtRef ("github:owner/repo#N") anchors idempotency
// across webhook redeliveries and backfill re-runs.
func mirrorGitHubIssue(ctx context.Context, org, repo, fullName string, is githubIssue) (bool, error) {
	labels := make([]string, 0, len(is.Labels))
	for _, l := range is.Labels {
		if n := strings.TrimSpace(l.Name); n != "" {
			labels = append(labels, n)
		}
	}
	res, err := cloud.UpsertIssue(ctx, cloud.IssueUpsert{
		Org:         org,
		ProjectKey:  githubTrackerProjectKey,
		ProjectName: githubTrackerProjectName,
		Repo:        repo,
		ExtRef:      fmt.Sprintf("github:%s#%d", strings.TrimSpace(fullName), is.Number),
		Kind:        "issue",
		Source:      "git",
		Title:       is.Title,
		Description: is.Body,
		State:       is.State,
		Assignee:    assigneeOf(is),
		Labels:      labels,
	})
	if err != nil {
		return false, err
	}
	return res.Created, nil
}

// handleGitHubIssueEvent processes a verified `issues` / `issue_comment` webhook: it
// resolves the org from the SIGNED installation id and mirrors the parent issue (a
// comment carries the issue's current state, so both events re-sync the one row). It
// ALWAYS answers a benign 200 for a no-op (PR, no installation, unknown org) so
// GitHub does not retry-storm; only a sink failure is a 502.
func handleGitHubIssueEvent(c *zip.Ctx, body []byte) error {
	var ev githubIssueEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid issue payload")
	}
	if ev.Issue.PullRequest != nil {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "pull request"})
	}
	if ev.Installation.ID == 0 {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "no installation"})
	}
	if ev.Issue.Number == 0 || ev.Repository.FullName == "" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "no issue"})
	}
	org, ok := OrgForExternalID("github", strconv.FormatInt(ev.Installation.ID, 10))
	if !ok {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "unknown installation"})
	}
	created, err := mirrorGitHubIssue(c.Context(), org, ev.Repository.Name, ev.Repository.FullName, ev.Issue)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "mirror issue: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"mirrored": true, "created": created, "action": ev.Action})
}

// ── backfill: seed existing issues ───────────────────────────────────────────

// installationIssues lists a repo's issues (state open|closed|all), following
// pagination and SKIPPING pull requests (GitHub's /issues endpoint mixes them in).
func installationIssues(ctx context.Context, token, fullName, state string) ([]githubIssue, error) {
	const perPage = 100
	const maxPages = 50 // 5k issues/repo ceiling
	var all []githubIssue
	for page := 1; page <= maxPages; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/issues?state=%s&per_page=%d&page=%d",
			strings.TrimRight(githubAPIBase, "/"), fullName, state, perPage, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := githubHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github call: %w", err)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("github http %d: %s", resp.StatusCode, truncateBody(b))
		}
		var page1 []githubIssue
		if err := json.Unmarshal(b, &page1); err != nil {
			return nil, fmt.Errorf("github decode: %w", err)
		}
		for _, is := range page1 {
			if is.PullRequest == nil {
				all = append(all, is)
			}
		}
		if len(page1) < perPage {
			break
		}
	}
	return all, nil
}

// githubBackfillResult is the count the operator asked for.
type githubBackfillResult struct {
	Repos     int  `json:"repos"`
	Issues    int  `json:"issues"`
	Created   int  `json:"created"`
	Updated   int  `json:"updated"`
	Failed    int  `json:"failed"`
	Truncated bool `json:"truncated,omitempty"`
}

const (
	backfillBudget    = 4 * time.Minute
	backfillMaxIssues = 5000
)

// githubIssuesBackfill seeds the native tracker with the EXISTING issues across the
// org's granted repos (default state=open). Org-scoped by the validated principal —
// a caller only ever backfills its OWN org. Synchronous + bounded (a total time
// budget + an issue cap) so it returns the counts directly; idempotent by ExtRef, so
// a re-run continues where a truncated pass left off (never duplicates).
func githubIssuesBackfill(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	state := strings.ToLower(strings.TrimSpace(c.Query("state")))
	switch state {
	case "", "open":
		state = "open"
	case "closed", "all":
	default:
		return zip.ErrBadRequest("state must be open|closed|all")
	}
	tok, herr := githubTokenForOrg(c.Context(), org)
	if herr != nil {
		return herr
	}
	repos, err := installationRepos(c.Context(), tok)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "list github repositories: %v", err)
	}
	ctx, cancel := context.WithTimeout(c.Context(), backfillBudget)
	defer cancel()

	var out githubBackfillResult
	for _, r := range repos {
		if r.Archived || r.Disabled {
			continue // un-fetchable — skip, never fabricate a mirror
		}
		if ctx.Err() != nil || out.Issues >= backfillMaxIssues {
			out.Truncated = true
			break
		}
		out.Repos++
		issues, ierr := installationIssues(ctx, tok, r.FullName, state)
		if ierr != nil {
			out.Failed++
			s.Log.Warn("github backfill: list issues", "org", org, "repo", r.Name, "err", ierr)
			continue
		}
		for _, is := range issues {
			if out.Issues >= backfillMaxIssues {
				out.Truncated = true
				break
			}
			out.Issues++
			created, uerr := mirrorGitHubIssue(ctx, org, r.Name, r.FullName, is)
			if uerr != nil {
				out.Failed++
				s.Log.Warn("github backfill: mirror", "org", org, "repo", r.Name, "num", is.Number, "err", uerr)
				continue
			}
			if created {
				out.Created++
			} else {
				out.Updated++
			}
		}
	}
	s.Log.Info("github issues backfill", "org", org, "repos", out.Repos, "issues", out.Issues,
		"created", out.Created, "updated", out.Updated, "failed", out.Failed, "truncated", out.Truncated)
	return c.JSON(http.StatusOK, out)
}
