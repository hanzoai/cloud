package integrations

// github_fork.go — take a copy of a repository into the org, and find one to take.
//
// Both go through the SAME grant resolution as every other GitHub op
// (resolveGrantedRepo / Connections): the App's installation is what the org may
// reach, and a caller can never address a repository the App was not granted.
// Search is the one exception and says so — it asks GitHub's public index, which
// is public by definition, and returns nothing an installation token unlocks.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/zap-proto/zip"
)

// githubForkReq names the repository to fork.
type githubForkReq struct {
	// Repo is a repository the org's installation was GRANTED, by name.
	Repo string `json:"repo"`
	// Org is the GitHub account to fork INTO; empty forks to the installation's
	// own account, which is the common case.
	Org string `json:"org"`
}

// githubForkOut is the fork GitHub created, or the one that already existed.
type githubForkOut struct {
	// FullName is the fork's "owner/repo". The owner is the account it landed in
	// — the request's org, or the installation's own account when none was named.
	FullName string `json:"full_name"`
	// HTMLURL is the fork's page on github.com.
	HTMLURL string `json:"html_url"`
	// CloneURL is the fork's https git remote. GitHub populates a new fork in the
	// background, so a clone issued the moment this answers can still find it empty.
	CloneURL string `json:"clone_url"`
	// DefaultBranch is the branch the fork checks out, inherited from upstream.
	DefaultBranch string `json:"default_branch"`
	// Existing reports that the fork was already there. GitHub answers 202 either
	// way, so without this a caller cannot tell "made you one" from "you had one".
	Existing bool `json:"existing"`
}

// githubFork forks a granted repository.
//
// GitHub's fork is ASYNCHRONOUS: it answers 202 with the target repo and
// populates it in the background, and it answers the same 202 when the fork
// already exists. So this reports what GitHub said rather than waiting — a call
// that blocked until the clone finished would time out on a large repository and
// tell the caller nothing it does not already know.
//
// Example: {"repo":"widgets"}
// Response: {"full_name":"acme/widgets","html_url":"https://github.com/acme/widgets","clone_url":"https://github.com/acme/widgets.git","default_branch":"main","existing":false}
func (o ops) githubFork(ctx context.Context, in *githubForkReq) (*githubForkOut, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return nil, err
	}
	repo := strings.TrimSpace(in.Repo)
	if !validRepoName(repo) {
		return nil, zip.ErrBadRequest("repo must be a valid repository name")
	}
	pr, err := resolveGrantedRepo(ctx, org, repo)
	if err != nil {
		return nil, err
	}
	owner, name, ok := splitFullName(pr.fullName)
	if !ok {
		return nil, fmt.Errorf("invalid repository full name")
	}

	var body []byte
	if target := strings.TrimSpace(in.Org); target != "" {
		body, _ = json.Marshal(map[string]string{"organization": target})
	}
	endpoint := strings.TrimRight(githubAPIBase, "/") + "/repos/" +
		url.PathEscape(owner) + "/" + url.PathEscape(name) + "/forks"
	code, raw, hdr, err := githubCall(ctx, pr.token, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	if code < 200 || code > 299 {
		return nil, pagesErr(code, raw, hdr)
	}
	var got struct {
		FullName      string `json:"full_name"`
		HTMLURL       string `json:"html_url"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
		CreatedAt     string `json:"created_at"`
		UpdatedAt     string `json:"updated_at"`
	}
	if jerr := json.Unmarshal(raw, &got); jerr != nil {
		return nil, fmt.Errorf("github fork: %w", jerr)
	}
	// A fork GitHub just made has created_at == updated_at; one that already
	// existed has drifted. It is a heuristic and it is the only signal GitHub
	// gives, so it is reported as a hint and never used to decide anything.
	return &githubForkOut{
		FullName: got.FullName, HTMLURL: got.HTMLURL, CloneURL: got.CloneURL,
		DefaultBranch: got.DefaultBranch,
		Existing:      got.CreatedAt != "" && got.CreatedAt != got.UpdatedAt,
	}, nil
}

// githubSearchReq is a query against GitHub's public repository index.
type githubSearchReq struct {
	// Q is GitHub's own search syntax, passed through: "tetris language:go",
	// "org:hanzoai stars:>10". Passing it through rather than inventing a
	// vocabulary means one thing to learn, and it is theirs.
	Q string `json:"q"`
	// Limit caps the answer; 0 takes the default and anything above the ceiling
	// is clamped rather than refused.
	Limit int `json:"limit"`
}

// githubSearchHit is one row of GitHub's public repository index, passed through.
type githubSearchHit struct {
	// FullName is the repository's "owner/repo" on GitHub. Finding it here does
	// NOT make it forkable: githubFork takes a repo the org's installation was
	// granted, and a hit from the public index usually is not one.
	FullName string `json:"full_name"`
	// Description is the blurb the repository's owner wrote. Empty when it has none.
	Description string `json:"description"`
	// HTMLURL is the repository's page on github.com.
	HTMLURL string `json:"html_url"`
	// CloneURL is the repository's https git remote.
	CloneURL string `json:"clone_url"`
	// DefaultBranch is the branch a clone checks out.
	DefaultBranch string `json:"default_branch"`
	// Stars is GitHub's stargazers_count as the SEARCH INDEX held it when the
	// query ran — a snapshot, not a live count off the repository.
	Stars int `json:"stars"`
	// Language is the primary language GitHub detected from the file mix ("Go",
	// "TypeScript"). Empty when GitHub attributes none.
	Language string `json:"language"`
	// Private is GitHub's visibility flag, passed through. This op reads the
	// public index — the org's token only charges the rate limit to the
	// installation — so it is false for everything a search can reach.
	Private bool `json:"private"`
}

// githubSearchOut is one page of search hits.
type githubSearchOut struct {
	// Repos are the matching repositories in GitHub's own relevance order, capped
	// at limit. Always an array, never null.
	Repos []githubSearchHit `json:"repos"`
	// Count is how many hits Repos carries. It is that array's length, NOT
	// GitHub's total_count, so it never exceeds limit and says nothing about how
	// many more repositories matched.
	Count int `json:"count"`
}

const (
	searchHitsDefault = 20
	searchHitsCeiling = 100
)

// githubSearch finds repositories on GitHub.
//
// This reads the PUBLIC index and returns nothing an installation unlocks: it is
// how you find a repository to fork, not a way to see inside one. The org's own
// token is used only so the query is rate-limited against the installation
// rather than anonymously — the results are the same ones anyone would get.
//
// Example: {"q":"language:go raft","limit":5}
// Response: {"repos":[{"full_name":"hashicorp/raft","stars":8000,"language":"Go"}],"count":1}
func (o ops) githubSearch(ctx context.Context, in *githubSearchReq) (*githubSearchOut, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Q)
	if q == "" {
		return nil, zip.ErrBadRequest("q is required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = searchHitsDefault
	}
	if limit > searchHitsCeiling {
		limit = searchHitsCeiling
	}

	// Any granted account's token will do — the index is public and identical
	// whichever one asks. Unconnected orgs are refused rather than falling back to
	// an anonymous call, so a deployment's rate limit is never spent on an org
	// that never connected.
	tok := ""
	for _, c := range Connections(org, "github") {
		if t, terr := githubTokenFor(ctx, org, c.Label); terr == nil {
			tok = t
			break
		}
	}
	if tok == "" {
		return nil, zip.Errorf(http.StatusConflict, "github is not connected for this organization")
	}

	endpoint := strings.TrimRight(githubAPIBase, "/") + "/search/repositories?per_page=" +
		fmt.Sprint(limit) + "&q=" + url.QueryEscape(q)
	code, raw, hdr, err := githubCall(ctx, tok, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if code < 200 || code > 299 {
		return nil, pagesErr(code, raw, hdr)
	}
	var got struct {
		Items []struct {
			FullName      string `json:"full_name"`
			Description   string `json:"description"`
			HTMLURL       string `json:"html_url"`
			CloneURL      string `json:"clone_url"`
			DefaultBranch string `json:"default_branch"`
			Stars         int    `json:"stargazers_count"`
			Language      string `json:"language"`
			Private       bool   `json:"private"`
		} `json:"items"`
	}
	if jerr := json.Unmarshal(raw, &got); jerr != nil {
		return nil, fmt.Errorf("github search: %w", jerr)
	}
	out := &githubSearchOut{Repos: make([]githubSearchHit, 0, len(got.Items))}
	for _, it := range got.Items {
		out.Repos = append(out.Repos, githubSearchHit{
			FullName: it.FullName, Description: it.Description, HTMLURL: it.HTMLURL,
			CloneURL: it.CloneURL, DefaultBranch: it.DefaultBranch,
			Stars: it.Stars, Language: it.Language, Private: it.Private,
		})
	}
	out.Count = len(out.Repos)
	return out, nil
}

// githubCall is one GitHub API request with the org's token. The token rides only
// the Authorization header; the endpoint is built by the caller with every
// path segment escaped.
func githubCall(ctx context.Context, token, method, endpoint string, body []byte) (int, []byte, http.Header, error) {
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("github call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, resp.Header, nil
}
