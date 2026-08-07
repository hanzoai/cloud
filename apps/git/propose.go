package git

// propose.go answers the one question left when a coding run finishes: where
// does a person go to read what it did?
//
// There are two answers, and which one applies is a property of WHERE THE CODE
// LIVES, never of who is asking:
//
//	github.com  the repository mirrors into a GitHub account, so the proposal is
//	            a real pull request there, opened against the base branch.
//	here        the repository lives only in the forge, which has no pull request
//	            of its own. The branch's page is where the work is read; the
//	            tracker row filed beside it is where it is tracked.
//
// One seam, two backends. A caller asks for the address and never for the host,
// which is what keeps "paste a link from either and it works" from becoming two
// orchestrations that drift apart.
//
// # The head is pushed here and not left to the mirror
//
// mirror_out.go already replicates a landed branch to every target — but it is a
// best-effort subscriber to a lifecycle event, running on a one-slot semaphore
// with a five-minute ceiling, so "the branch is on GitHub" is not true at any
// particular moment. GitHub refuses a pull request whose head it cannot see, so
// this pushes the head itself, through the SAME function, before asking. A force
// push of one already-identical ref is a no-op, so doing it twice costs nothing
// and removes a race that would otherwise fail a fraction of runs for no reason a
// user could act on.
//
// # Credentials
//
// The GitHub App installation token is minted per call and never stored, exactly
// as the outbound mirror mints it (mirror_out.go outboundAuthHeader). It rides an
// Authorization header — never argv, never a log line, never the returned error.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/integrations"
	"github.com/hanzoai/cloud/brand"
)

// proposeTimeout bounds the API call. The mirror push before it carries its own,
// much larger, ceiling; this one is a single small POST.
const proposeTimeout = 20 * time.Second

// Propose offers head for merging into base and returns where to read it.
//
// It is fail-closed on the GitHub path — an org whose repository mirrors to
// GitHub and whose pull request could not be opened gets an error, not a quiet
// forge link, because the two are different outcomes and only one of them is
// "your change is waiting for review over there".
func Propose(ctx context.Context, org, project, repo, base, head, title, body string) (string, error) {
	s := mounted.Load()
	if s == nil {
		return "", fmt.Errorf("git: the forge is not mounted")
	}
	if !branchRE.MatchString(head) {
		return "", fmt.Errorf("git: %q is not a branch", head)
	}
	store, err := storeFor(s, org)
	if err != nil {
		return "", fmt.Errorf("git: open store: %w", err)
	}
	targets, err := store.ListMirrors(ctx, org, project, repo)
	if err != nil {
		return "", fmt.Errorf("git: read mirrors: %w", err)
	}
	for _, t := range targets {
		if !strings.EqualFold(strings.TrimSpace(t.Host), "github.com") {
			continue
		}
		owner, name := githubRepoOf(t.URL)
		if owner == "" || name == "" {
			return "", fmt.Errorf("github: %q names no repository", t.URL)
		}
		// The credential is resolved BEFORE the head is pushed. Not an
		// optimisation: a mirror push holds the forge's single outbound slot for up
		// to five minutes, and spending it to replicate a branch we then cannot
		// propose is a cost paid for nothing.
		tok, err := integrations.InstallationToken(ctx, org, owner)
		if err != nil || strings.TrimSpace(tok) == "" {
			return "", fmt.Errorf("github: no installation for %s", owner)
		}
		bare := s.State.storage.absRepoPath(org, project, repo)
		if err := pushBranchToMirror(ctx, org, bare, t, head); err != nil {
			return "", fmt.Errorf("git: mirror %s: %s", t.Host, sanitizeGitErr(err.Error()))
		}
		return pullRequest(ctx, tok, owner, name, base, head, title, body)
	}
	return branchPage(s, org, project, repo, head), nil
}

// pullRequest opens the pull request on GitHub and returns its address.
//
// A 422 is GitHub's answer for "one already exists for this head", which for an
// agent branch can only mean a retry of a run that already proposed — so it is
// reported as what it is rather than dressed up as a failure.
func pullRequest(ctx context.Context, tok, owner, name, base, head, title, body string) (string, error) {
	payload, err := json.Marshal(struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body,omitempty"`
	}{Title: title, Head: head, Base: base, Body: body})
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, proposeTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls", api, url.PathEscape(owner), url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: proposeTimeout}).Do(req)
	if err != nil {
		return "", fmt.Errorf("github: open pull request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		HTMLURL string `json:"html_url"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode == http.StatusCreated && out.HTMLURL != "" {
		return out.HTMLURL, nil
	}
	return "", fmt.Errorf("github: %s/%s pull request: status %d: %s", owner, name, resp.StatusCode, out.Message)
}

// branchPage is where a branch is read when the repository lives only here.
//
// Empty for a project-scoped repository: the browse routes address an org and a
// repo and nothing else (ui.go uiRoutes), so there is no page to point at, and an
// address that 404s is worse than none.
func branchPage(s *cloud.Service[state], org, project, repo, branch string) string {
	if strings.TrimSpace(project) != "" {
		return ""
	}
	host := s.Domain
	if host == "" {
		host = brand.APIHost(brand.Default)
	}
	return fmt.Sprintf("https://%s/git/%s/%s?ref=%s", host, org, repo, url.QueryEscape(branch))
}

// githubRepoOf reads the account and repository out of a GitHub remote —
// https://github.com/<owner>/<repo>.git. Empty when the URL names neither, which
// lets the single-connection case resolve as it always did.
func githubRepoOf(remote string) (owner, name string) {
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git")
}
