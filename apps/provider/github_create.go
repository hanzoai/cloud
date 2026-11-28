package provider

// github_create.go creates a new repository on GitHub under either the caller's
// connected personal GitHub account (POST /user/repos) or a GitHub organization
// (POST /orgs/{org}/repos), with idempotent 422 recovery and optional outbound
// mirror linking back to an existing Hanzo Git repository.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	gitpeer "github.com/hanzoai/cloud/client/git"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// linkGitMirror declares an outbound mirror target on a Hanzo Git repository
// over the peer plane. A test stands in for it.
var linkGitMirror = gitpeer.GitMirror

// githubCreateRepoReq describes the GitHub repository to create.
type githubCreateRepoReq struct {
	// Name is the repository name to create on GitHub. Required.
	Name string `json:"name"`
	// Description is an optional short description shown on GitHub.
	Description string `json:"description,omitempty"`
	// Private creates a private repository when true; defaults to false (public).
	Private bool `json:"private,omitempty"`
	// Org is the GitHub organization login to create the repository under.
	// Omit to create under the caller's connected personal GitHub account.
	Org string `json:"org,omitempty"`
	// LinkRepo is an optional Hanzo Git repository name in the caller's org;
	// when set, the new GitHub repository's clone URL is registered as an
	// outbound mirror target on that Hanzo Git repository so commits sync immediately.
	LinkRepo string `json:"link_repo,omitempty"`
	// LinkRepoCamel is an optional camelCase alias for link_repo.
	LinkRepoCamel string `json:"linkRepo,omitempty"`
}

// githubCreateRepoOut is the GitHub repository that was created or already existed.
type githubCreateRepoOut struct {
	// FullName is the repository's "owner/repo" on GitHub.
	FullName string `json:"full_name"`
	// HTMLURL is the repository's page on github.com.
	HTMLURL string `json:"html_url"`
	// CloneURL is the repository's https git remote.
	CloneURL string `json:"clone_url"`
	// DefaultBranch is the repository's default branch (for example "main").
	DefaultBranch string `json:"default_branch"`
	// Private is GitHub's visibility flag for the repository.
	Private bool `json:"private"`
	// Existing reports that a repository with this name already existed on the
	// target account and was returned idempotently rather than newly created.
	Existing bool `json:"existing"`
	// Linked reports whether the repository's clone URL was registered as an
	// outbound mirror target on the requested Hanzo Git repository.
	Linked bool `json:"linked,omitempty"`
}

type githubRepoPayload struct {
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// githubCreateRepo creates a repository on GitHub under the caller's connected
// personal GitHub account (when org is omitted) or under a GitHub organization
// (when org is set).
//
// Creating under a personal account uses the caller's connected GitHub user OAuth
// token (/v1/provider/github/user/connect). Creating under a GitHub organization
// prefers the caller's connected GitHub user token and falls back to the org's
// bound GitHub App installation token when that installation is an Organization.
// If a repository with the same name already exists on the target account (422),
// the existing repository is fetched and returned idempotently with existing:true.
// When link_repo is set, the repository's clone URL is also registered as an
// outbound mirror target on that Hanzo Git repository.
//
// Example: {"name":"my-app","description":"Starter app","private":false,"link_repo":"my-app"}
// Response: {"full_name":"octocat/my-app","html_url":"https://github.com/octocat/my-app","clone_url":"https://github.com/octocat/my-app.git","default_branch":"main","private":false,"existing":false,"linked":true}
func (o ops) githubCreateRepo(ctx context.Context, in *githubCreateRepoReq) (*githubCreateRepoOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !githubConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github integration is not configured on this deployment")
	}
	name := strings.TrimSpace(in.Name)
	if !validRepoName(name) {
		return nil, zip.ErrBadRequest("name must be a valid repository name")
	}
	ghOrg := strings.TrimSpace(in.Org)
	if ghOrg != "" && !validRepoName(ghOrg) {
		return nil, zip.ErrBadRequest("org must be a valid GitHub organization name")
	}
	linkRepo := cmp.Or(strings.TrimSpace(in.LinkRepo), strings.TrimSpace(in.LinkRepoCamel))
	if linkRepo != "" && !validRepoName(linkRepo) {
		return nil, zip.ErrBadRequest("link_repo must be a valid repository name")
	}

	user := factsOf(ctx).user
	tok, userLogin, hasUser, err := personToken(ctx, o.s, org, user)
	if err != nil {
		return nil, err
	}

	var (
		endpoint    string
		targetOwner string
		usedUser    bool
	)
	base := strings.TrimRight(githubAPIBase, "/")
	if ghOrg == "" {
		if !hasUser {
			return nil, zip.Errorf(http.StatusPreconditionFailed,
				"Connect your personal GitHub account (/v1/provider/github/user/connect) to create repositories under your GitHub user")
		}
		endpoint = base + "/user/repos"
		targetOwner = userLogin
		usedUser = true
	} else {
		endpoint = base + "/orgs/" + url.PathEscape(ghOrg) + "/repos"
		targetOwner = ghOrg
		if hasUser {
			usedUser = true
		} else {
			instTok, ierr := orgInstallTokenForCreate(ctx, org, ghOrg)
			if ierr != nil {
				return nil, ierr
			}
			tok = instTok
		}
	}

	reqBody := map[string]any{
		"name":    name,
		"private": in.Private,
	}
	if desc := strings.TrimSpace(in.Description); desc != "" {
		reqBody["description"] = desc
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	code, raw, hdr, err := githubCall(ctx, tok, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized && usedUser {
		forgetPerson(ctx, o.s, org, user)
		return nil, zip.Errorf(http.StatusPreconditionFailed,
			"Connect your personal GitHub account (/v1/provider/github/user/connect) to create repositories under your GitHub user")
	}

	var (
		got      githubRepoPayload
		existing bool
	)
	switch {
	case code >= 200 && code <= 299:
		if jerr := json.Unmarshal(raw, &got); jerr != nil {
			return nil, fmt.Errorf("github create repo: %w", jerr)
		}
	case code == http.StatusUnprocessableEntity && isAlreadyExistsErr(raw):
		owner := targetOwner
		if owner == "" && usedUser {
			var who struct {
				Login string `json:"login"`
			}
			if gerr := githubGet(ctx, tok, "/user", &who); gerr == nil {
				owner = strings.TrimSpace(who.Login)
			}
		}
		if owner == "" {
			return nil, githubRepoErr(code, raw, hdr)
		}
		getEndpoint := base + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
		gcode, graw, ghdr, gerr := githubCall(ctx, tok, http.MethodGet, getEndpoint, nil)
		if gerr != nil {
			return nil, gerr
		}
		if gcode < 200 || gcode > 299 {
			return nil, githubRepoErr(gcode, graw, ghdr)
		}
		if jerr := json.Unmarshal(graw, &got); jerr != nil {
			return nil, fmt.Errorf("github get existing repo: %w", jerr)
		}
		existing = true
	default:
		return nil, githubRepoErr(code, raw, hdr)
	}

	// Refresh cached repository views so GET /v1/provider/github/repos sees the new repo.
	repoIndex.forgetOrg(org)
	if user != "" {
		repoIndex.forgetUser(org, user)
	}

	out := &githubCreateRepoOut{
		FullName:      got.FullName,
		HTMLURL:       got.HTMLURL,
		CloneURL:      got.CloneURL,
		DefaultBranch: got.DefaultBranch,
		Private:       got.Private,
		Existing:      existing,
	}

	if linkRepo != "" && strings.TrimSpace(out.CloneURL) != "" {
		mctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, merr := linkGitMirror(mctx, &client.MirrorIn{
			Repo:    linkRepo,
			URL:     out.CloneURL,
			Enabled: true,
		})
		cancel()
		if merr != nil {
			o.s.Log.Warn("github create repo: register outbound mirror failed",
				"org", org, "repo", linkRepo, "clone_url", out.CloneURL, "err", merr)
		} else {
			out.Linked = true
		}
	}

	return out, nil
}

// orgInstallTokenForCreate resolves an installation access token for ghOrg when
// the caller's Hanzo org holds a bound GitHub App installation for ghOrg AND
// GitHub reports that installation's account type is "Organization".
func orgInstallTokenForCreate(ctx context.Context, org, ghOrg string) (string, error) {
	var conn Connection
	found := false
	for _, c := range Connections(org, "github") {
		if strings.EqualFold(c.Label, ghOrg) || strings.EqualFold(c.AccountLabel, ghOrg) {
			conn = c
			found = true
			break
		}
	}
	if !found {
		return "", zip.Errorf(http.StatusPreconditionFailed,
			"Connect your personal GitHub account (/v1/provider/github/user/connect) or install the Hanzo GitHub App on organization %q to create repositories", ghOrg)
	}
	id, err := parseInstallation(conn.ExternalID)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "invalid github installation id")
	}
	ins, err := appInstallationByID(ctx, id)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "lookup github installation: %v", err)
	}
	if !strings.EqualFold(ins.Type, "Organization") {
		return "", zip.Errorf(http.StatusPreconditionFailed,
			"Connect your personal GitHub account (/v1/provider/github/user/connect) to create repositories under %q", ghOrg)
	}
	tok, err := ghApp.installationToken(ctx, id)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "mint github installation token: %v", err)
	}
	return tok, nil
}

// appInstallationByID fetches one GitHub App installation by id using the App JWT.
func appInstallationByID(ctx context.Context, id int64) (githubInstallation, error) {
	tr, err := ghApp.transport()
	if err != nil {
		return githubInstallation{}, err
	}
	httpClient := &http.Client{Transport: tr, Timeout: 20 * time.Second}
	endpoint := fmt.Sprintf("%s/app/installations/%d", strings.TrimRight(githubAPIBase, "/"), id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return githubInstallation{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return githubInstallation{}, fmt.Errorf("github call: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return githubInstallation{}, fmt.Errorf("github http %d: %s", resp.StatusCode, truncateBody(body))
	}
	var got struct {
		ID      int64 `json:"id"`
		Account struct {
			Login   string `json:"login"`
			Type    string `json:"type"`
			HTMLURL string `json:"html_url"`
		} `json:"account"`
		Selection string `json:"repository_selection"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return githubInstallation{}, fmt.Errorf("github decode: %w", err)
	}
	return githubInstallation{
		ID:      got.ID,
		Login:   got.Account.Login,
		Type:    got.Account.Type,
		HTMLURL: got.Account.HTMLURL,
		Grant:   got.Selection,
	}, nil
}

// isAlreadyExistsErr reports whether a GitHub 422 response indicates that a
// repository with the requested name already exists on the target account.
func isAlreadyExistsErr(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "already exists")
}

// githubRepoErr maps a non-2xx GitHub repository API response to an HTTP error.
func githubRepoErr(status int, body []byte, hdr http.Header) error {
	if rl, retry := rateLimited(status, body, hdr); rl {
		if retry != "" {
			return zip.Errorf(http.StatusTooManyRequests, "github rate limit reached; retry after %ss", retry)
		}
		return zip.Errorf(http.StatusTooManyRequests, "github rate limit reached; retry shortly")
	}
	switch status {
	case http.StatusUnauthorized:
		return zip.Errorf(http.StatusPreconditionFailed, "github rejected the credential: %s", truncateBody(body))
	case http.StatusForbidden:
		return zip.Errorf(http.StatusForbidden, "github refused the repository request: %s", truncateBody(body))
	case http.StatusNotFound:
		return zip.Errorf(http.StatusNotFound, "github repository not found: %s", truncateBody(body))
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		return zip.Errorf(status, "github rejected the repository request: %s", truncateBody(body))
	default:
		return zip.Errorf(http.StatusBadGateway, "github http %d: %s", status, truncateBody(body))
	}
}
