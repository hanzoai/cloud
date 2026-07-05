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

// github is the narrow identity seam the author verification path needs. Repo
// ownership is proven ONE of two ways, and this interface makes BOTH testable with a
// fake — the HTTP impl below is the ONE production binding:
//
//  1. OAuth (preferred): the caller's IAM-linked GitHub account yields an access
//     token; the GitHub API then confirms the caller has ADMIN permission on the
//     repo. This is the strong proof — it needs a live, user-authorized token.
//  2. File (fallback): when no linked token is available, the author places a
//     hanzo.json on the repo's DEFAULT branch containing their per-author verify
//     code; fetching it from raw.githubusercontent.com proves default-branch control
//     (i.e. push/admin rights) without any OAuth scope.
type github interface {
	// linkedAccount returns the caller's linked GitHub login + OAuth token from IAM.
	// ok=false (no error) when none is linked or IAM is unconfigured — the caller then
	// falls back to the file method. An error is a genuine IAM failure.
	linkedAccount(ctx context.Context, org, userSub string) (login, token string, ok bool, err error)
	// repoAdmin reports whether the token's user has admin permission on owner/repo
	// (GitHub API GET /repos/{owner}/{repo} → permissions.admin).
	repoAdmin(ctx context.Context, token, owner, repo string) (bool, error)
	// fetchFile fetches raw.githubusercontent.com/{owner}/{repo}/{branch}/{path},
	// returning the body. A 404 returns (nil, nil) so a missing file is "not proven"
	// rather than a hard error.
	fetchFile(ctx context.Context, owner, repo, branch, path string) ([]byte, error)
}

// httpGitHub is the production identity binding. It talks to IAM (for the linked
// account) with a service token, and to GitHub's public API + raw host directly.
type httpGitHub struct {
	iamBase  string // e.g. https://cloud-api.hanzo.svc:8000 (IAM linked-accounts host)
	iamToken string // IAM service token (S2S)
	http     *http.Client
}

func newGitHubClient(iamBase, iamToken string) *httpGitHub {
	return &httpGitHub{
		iamBase:  strings.TrimRight(strings.TrimSpace(iamBase), "/"),
		iamToken: strings.TrimSpace(iamToken),
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// linkedAccount reads the caller's linked GitHub identity from IAM. IAM exposes the
// per-user linked social accounts (Casdoor linked providers) behind the service
// token; we select provider=github and return its login + access token. When IAM is
// unconfigured OR the user has no github link, ok=false steers verify to the file
// method (never an error, so a partial deploy still verifies via hanzo.json).
func (g *httpGitHub) linkedAccount(ctx context.Context, org, userSub string) (string, string, bool, error) {
	if g.iamBase == "" || g.iamToken == "" || userSub == "" {
		return "", "", false, nil
	}
	q := url.Values{"provider": {"github"}, "user": {userSub}}
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
		return "", "", false, nil // no linked github account
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

// repoAdmin queries the GitHub API for the token's permission on owner/repo.
func (g *httpGitHub) repoAdmin(ctx context.Context, token, owner, repo string) (bool, error) {
	if token == "" {
		return false, nil
	}
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

// fetchFile pulls a file off the repo's raw host. 404 → (nil, nil) "not present".
func (g *httpGitHub) fetchFile(ctx context.Context, owner, repo, branch, path string) ([]byte, error) {
	u := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch), path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("raw github unreachable: %w", err)
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
		return nil, fmt.Errorf("raw github status %d", resp.StatusCode)
	}
	return body, nil
}
