package authors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Providers an author repo can live on. The provider is derived from the canonical
// repo host (github.com → github, gitlab.com → gitlab) so ONE code path serves both.
const (
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
)

// providerForHost maps a canonical repo host to its forge provider.
func providerForHost(host string) string {
	if host == "gitlab.com" {
		return ProviderGitLab
	}
	return ProviderGitHub
}

// forge is the narrow identity seam the author verification path needs, across the
// supported code forges (GitHub, GitLab). Repo ownership is proven ONE of two ways,
// and this interface makes BOTH testable with a fake — the HTTP impl below is the ONE
// production binding:
//
//  1. OAuth (preferred): the caller's IAM-linked forge account yields an access
//     token; the forge API then confirms the caller has ADMIN/PUSH permission on the
//     repo. This is the strong proof — it needs a live, user-authorized token.
//  2. File (fallback): when no linked token is available, the author places a
//     hanzo.json on the repo's DEFAULT branch containing their per-author verify
//     code; fetching it from the forge's raw host proves default-branch control
//     (i.e. push/admin rights) without any OAuth scope.
type forge interface {
	// linkedAccount returns the caller's linked forge login + OAuth token from IAM for
	// `provider`. ok=false (no error) when none is linked or IAM is unconfigured — the
	// caller then falls back to the file method. An error is a genuine IAM failure.
	linkedAccount(ctx context.Context, provider, org, userSub string) (login, token string, ok bool, err error)
	// repoAdmin reports whether the token's user has admin/push permission on
	// owner/repo at `host` (GitHub or GitLab, dispatched on host).
	repoAdmin(ctx context.Context, host, token, owner, repo string) (bool, error)
	// fetchFile fetches the raw file at owner/repo/branch/path on `host`, returning the
	// body. A 404 returns (nil, nil) so a missing file is "not proven" rather than an error.
	fetchFile(ctx context.Context, host, owner, repo, branch, path string) ([]byte, error)
}

// httpForge is the production identity binding. It talks to IAM (for the linked
// account) with a service token, and to the forge public APIs + raw hosts directly.
type httpForge struct {
	iamBase  string // e.g. https://cloud-api.hanzo.svc:8000 (IAM linked-accounts host)
	iamToken string // IAM service token (S2S)
	http     *http.Client
}

func newGitHubClient(iamBase, iamToken string) *httpForge {
	return &httpForge{
		iamBase:  strings.TrimRight(strings.TrimSpace(iamBase), "/"),
		iamToken: strings.TrimSpace(iamToken),
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// linkedAccount reads the caller's linked forge identity from IAM for `provider`. IAM
// exposes the per-user linked social accounts behind the service token; we select the
// requested provider and return its login + access token. When IAM is unconfigured OR
// the user has no such link, ok=false steers verify to the file method (never an
// error, so a partial deploy still verifies via hanzo.json).
func (g *httpForge) linkedAccount(ctx context.Context, provider, org, userSub string) (string, string, bool, error) {
	if g.iamBase == "" || g.iamToken == "" || userSub == "" {
		return "", "", false, nil
	}
	if provider == "" {
		provider = ProviderGitHub
	}
	q := url.Values{"provider": {provider}, "user": {userSub}}
	u := g.iamBase + "/v1/iam/linked-accounts?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.iamToken)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if userSub != "" {
		req.Header.Set("X-User-Id", userSub)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return "", "", false, fmt.Errorf("iam unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", false, nil // no linked account for this provider
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", false, fmt.Errorf("iam status %d", resp.StatusCode)
	}
	var out struct {
		Login       string `json:"login"`
		Username    string `json:"username"`
		AccessToken string `json:"accessToken"`
		Token       string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", false, fmt.Errorf("iam linked-account decode: %w", err)
	}
	login := out.Login
	if login == "" {
		login = out.Username
	}
	token := out.AccessToken
	if token == "" {
		token = out.Token
	}
	if login == "" && token == "" {
		return "", "", false, nil
	}
	return login, token, true, nil
}

// repoAdmin queries the forge API (GitHub or GitLab, dispatched on host) for the
// token's permission on owner/repo.
func (g *httpForge) repoAdmin(ctx context.Context, host, token, owner, repo string) (bool, error) {
	if token == "" {
		return false, nil
	}
	if providerForHost(host) == ProviderGitLab {
		return g.gitlabRepoAdmin(ctx, token, owner, repo)
	}
	return g.githubRepoAdmin(ctx, token, owner, repo)
}

// githubRepoAdmin: GitHub API GET /repos/{owner}/{repo} → permissions.admin/push.
func (g *httpForge) githubRepoAdmin(ctx context.Context, token, owner, repo string) (bool, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("github unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return false, nil // no visibility → not admin
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("github status %d", resp.StatusCode)
	}
	var out struct {
		Permissions struct {
			Admin bool `json:"admin"`
			Push  bool `json:"push"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("github repo decode: %w", err)
	}
	// Admin OR push both prove write control sufficient for authorship attribution.
	return out.Permissions.Admin || out.Permissions.Push, nil
}

// gitlabRepoAdmin: GitLab API GET /projects/{urlencoded owner/repo} →
// permissions.{project_access,group_access}.access_level ≥ 30 (Developer) proves
// write control; ≥ 40 (Maintainer) is admin. We require ≥ 30 (push), mirroring
// GitHub's admin-OR-push rule.
func (g *httpForge) gitlabRepoAdmin(ctx context.Context, token, owner, repo string) (bool, error) {
	id := url.PathEscape(owner + "/" + repo) // GitLab wants the URL-encoded full path
	u := "https://gitlab.com/api/v4/projects/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("gitlab unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return false, nil // no visibility → not a member with write
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("gitlab status %d", resp.StatusCode)
	}
	var out struct {
		Permissions struct {
			ProjectAccess struct {
				AccessLevel int `json:"access_level"`
			} `json:"project_access"`
			GroupAccess struct {
				AccessLevel int `json:"access_level"`
			} `json:"group_access"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("gitlab project decode: %w", err)
	}
	const developer = 30 // GitLab access level: Developer (push) or above
	return out.Permissions.ProjectAccess.AccessLevel >= developer ||
		out.Permissions.GroupAccess.AccessLevel >= developer, nil
}

// fetchFile pulls a file off the forge's raw host (GitHub or GitLab, dispatched on
// host). 404 → (nil, nil) "not present".
func (g *httpForge) fetchFile(ctx context.Context, host, owner, repo, branch, path string) ([]byte, error) {
	var u string
	if providerForHost(host) == ProviderGitLab {
		// GitLab raw: https://gitlab.com/{owner}/{repo}/-/raw/{branch}/{path}
		u = fmt.Sprintf("https://gitlab.com/%s/%s/-/raw/%s/%s",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch), path)
	} else {
		u = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch), path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("raw forge unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("raw forge status %d", resp.StatusCode)
	}
	return body, nil
}
