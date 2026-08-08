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
	// Repos is how many granted repos were walked (archived/disabled are skipped).
	Repos int `json:"repos"`
	// Issues is how many upstream issues were seen.
	Issues int `json:"issues"`
	// Created is how many native issues this pass created.
	Created int `json:"created"`
	// Updated is how many existing native issues this pass refreshed.
	Updated int `json:"updated"`
	// Failed is how many repos or issues errored; the pass continues past each.
	Failed int `json:"failed"`
	// Truncated is set when the time budget or the issue cap stopped the pass early.
	// Re-run to continue — the mirror is idempotent by ExtRef, so nothing duplicates.
	Truncated bool `json:"truncated,omitempty"`
}

// githubBackfillIn selects which upstream issues to mirror.
type githubBackfillIn struct {
	// State is the GitHub issue state to walk: "open" (the default), "closed" or
	// "all". Anything else is a 400.
	State string `json:"state"`
}

const (
	backfillBudget    = 4 * time.Minute
	backfillMaxIssues = 5000
)

// githubIssuesBackfill seeds the native tracker with the EXISTING issues across the
// org's granted repos (default state=open); the webhook keeps them live thereafter.
// Org-scoped by the validated principal — a caller only ever backfills its OWN org.
// Synchronous + bounded (a total time budget and an issue cap) so it returns the
// counts directly; idempotent by ExtRef, so a re-run continues where a truncated
// pass left off and never duplicates.
//
// Example: {"state":"all"}
// Response: {"repos":12,"issues":430,"created":410,"updated":20,"failed":0}
func (o ops) githubIssuesBackfill(ctx context.Context, in *githubBackfillIn) (*githubBackfillResult, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return nil, err
	}
	issueState := strings.ToLower(strings.TrimSpace(in.State))
	switch issueState {
	case "", "open":
		issueState = "open"
	case "closed", "all":
	default:
		return nil, zip.ErrBadRequest("state must be open|closed|all")
	}
	repos, _, err := reachableRepos(ctx, org)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, backfillBudget)
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
		// The token must come from the account that owns this repo — one account's
		// token grants nothing on another's. Minting is cached per installation, so
		// resolving per repo costs a map lookup, not an API call.
		owner, _, _ := splitFullName(r.FullName)
		tok, terr := InstallationToken(ctx, org, owner)
		if terr != nil {
			out.Failed++
			o.s.Log.Warn("github backfill: token", "org", org, "repo", r.Name, "err", terr)
			continue
		}
		issues, ierr := installationIssues(ctx, tok, r.FullName, issueState)
		if ierr != nil {
			out.Failed++
			o.s.Log.Warn("github backfill: list issues", "org", org, "repo", r.Name, "err", ierr)
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
				o.s.Log.Warn("github backfill: mirror", "org", org, "repo", r.Name, "num", is.Number, "err", uerr)
				continue
			}
			if created {
				out.Created++
			} else {
				out.Updated++
			}
		}
	}
	o.s.Log.Info("github issues backfill", "org", org, "repos", out.Repos, "issues", out.Issues,
		"created", out.Created, "updated", out.Updated, "failed", out.Failed, "truncated", out.Truncated)
	return &out, nil
}
