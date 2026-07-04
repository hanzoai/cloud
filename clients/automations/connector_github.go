package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GitHub connector. Name == the clients/integrations provider id "github", which
// is SCAFFOLDED there but custodies NO token yet. So RunContext.Token fails closed,
// and every action here returns an honest "github not connected" — the code below
// is complete and correct; it simply never reaches the API until integrations seals
// a GitHub App installation token under githubTokenSecret.

// githubTokenSecret is the KMS secret name a GitHub installation token WILL be
// custodied under once integrations mints it. Named here so the connector is
// complete the day that lands; today Token returns an error and this is unused
// beyond the fail-closed check.
const githubTokenSecret = "installation_token"

var githubAPIBase = "https://api.github.com"

var githubClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	register(&Connector{
		Name:        "github",
		DisplayName: "GitHub",
		AuthType:    "oauth2",
		AuthReq:     true,
		Actions: map[string]*Action{
			"create_issue": {
				Name:        "create_issue",
				DisplayName: "Create Issue",
				Description: "Open an issue on a repository.",
				Props: []PropSpec{
					{Name: "owner", Type: "string", Required: true, Description: "Repo owner/org."},
					{Name: "repo", Type: "string", Required: true, Description: "Repository name."},
					{Name: "title", Type: "string", Required: true, Description: "Issue title."},
					{Name: "body", Type: "string", Description: "Issue body."},
				},
				Run: runGithubCreateIssue,
			},
			"list_repos": {
				Name:        "list_repos",
				DisplayName: "List Repositories",
				Description: "List repositories the installation can access.",
				Props:       []PropSpec{},
				Run:         runGithubListRepos,
			},
		},
	})
}

// githubToken fetches the org's GitHub installation token, failing closed with an
// honest error when GitHub is not connected (the Phase-1 reality — no token is
// custodied yet). ONE resolution point both actions share.
func githubToken(rc RunContext) (string, error) {
	tok, err := rc.Token(githubTokenSecret)
	if err != nil {
		return "", fmt.Errorf("github not connected: %w", err)
	}
	return string(tok), nil
}

func runGithubCreateIssue(ctx context.Context, rc RunContext) (any, error) {
	tok, err := githubToken(rc)
	if err != nil {
		return nil, err
	}
	owner := strInput(rc.Input, "owner")
	repo := strInput(rc.Input, "repo")
	title := strInput(rc.Input, "title")
	if owner == "" || repo == "" || title == "" {
		return nil, fmt.Errorf("github create_issue: owner, repo, title are required")
	}
	payload, err := jsonBytes(map[string]any{"title": title, "body": strInput(rc.Input, "body")})
	if err != nil {
		return nil, fmt.Errorf("github create_issue: encode: %w", err)
	}
	url := fmt.Sprintf("%s/repos/%s/%s/issues", githubAPIBase, owner, repo)
	return githubDo(ctx, http.MethodPost, url, tok, payload)
}

func runGithubListRepos(ctx context.Context, rc RunContext) (any, error) {
	tok, err := githubToken(rc)
	if err != nil {
		return nil, err
	}
	return githubDo(ctx, http.MethodGet, githubAPIBase+"/installation/repositories", tok, nil)
}

// githubDo issues one GitHub REST call and returns the decoded JSON. Bounded body
// read; explicit non-2xx handling. Unreachable in Phase 1 (no token), but complete.
func githubDo(ctx context.Context, method, url, token string, body io.Reader) (any, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := githubClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("github: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("github http %d", resp.StatusCode)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("github: decode: %w", err)
	}
	return out, nil
}
