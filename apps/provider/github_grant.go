package provider

// github_grant.go mints the one credential this process hands out: a token that
// reaches ONE repository, so a coding run can clone it, push its branch and
// propose it on GitHub, where the repository lives.
//
// The App key never leaves. What leaves is scoped three ways, and each is
// GitHub's to enforce rather than ours: the installation is one the caller's
// org connected, the token names one repository, and it carries two
// permissions. The caller revokes it when its run ends; GitHub expires it
// within the hour if nothing does.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// grantPerms is everything a run's token may do: write the branch, and open the
// pull request that proposes it. Not workflows, so a run cannot change the CI
// that would judge it; not administration, so it cannot lift a branch rule.
var grantPerms = map[string]string{"contents": "write", "pull_requests": "write"}

// readPerms is a plan run's token: it reads the repository's contents and can
// write nothing, not a branch and not a pull request.
var readPerms = map[string]string{"contents": "read"}

// permsFor is the permission set a mint asks for.
func permsFor(read bool) map[string]string {
	if read {
		return readPerms
	}
	return grantPerms
}

// ghName is GitHub's shape for an account or repository name. The owner is
// compared and the repository travels in a JSON body, so this refuses garbage
// early rather than guarding a path.
var ghName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// exposeGrant publishes the mint on the internal plane. Mount calls it.
func exposeGrant() {
	cloud.Plane().Post("/integrations/github/grant", planeGitHubGrant,
		zip.WithOperationID(client.IntegrationsGitHubGrant),
		zip.WithSummary("Mint a GitHub token for one repository of an account the org connected"))
}

// planeGitHubGrant mints for the CALLER'S OWN org, read from the peer context
// and never from an argument: the org decides which installations exist to mint
// on, so an org a caller could name is another tenant's repositories.
//
// The repository is resolved to where it lives NOW before anything is minted,
// and the answer names it. A transferred repository keeps answering at its old
// name through a redirect, but an installation token names repositories of its
// own account, so a token asked for at the old name reaches nothing.
func planeGitHubGrant(ctx context.Context, in *client.GitHubGrantIn) (*client.GitHubGrant, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("github grant: no org on the call")
	}
	if in == nil || !ghName.MatchString(in.Owner) || !ghName.MatchString(in.Repo) {
		return nil, zip.ErrBadRequest("github grant: owner and repo are required")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github grant: integrations is not serving")
	}
	owner, repo, _, err := canonical(ctx, org, in.Owner, in.Repo)
	if err != nil {
		return nil, zip.ErrForbidden(err.Error())
	}
	conn, err := githubConnection(org, owner)
	if err != nil {
		return nil, zip.ErrForbidden(err.Error())
	}
	id, err := strconv.ParseInt(strings.TrimSpace(conn.ExternalID), 10, 64)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github grant: invalid installation id")
	}
	tok, err := ghApp.grant(ctx, id, owner, repo, permsFor(in.Read))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return &client.GitHubGrant{Token: tok, Owner: owner, Repo: repo}, nil
}

// canonical is where owner/repo lives now, as GitHub names it, and its default
// branch: a transferred or renamed repository answers its old name with a
// redirect to its id, and the repository there carries its full name.
//
// It is asked with the org's own installations, the named owner's first, until
// one can see it: a private repository answers only to the installation that
// holds it, which after a transfer is not the one its old name suggests.
func canonical(ctx context.Context, org, owner, repo string) (string, string, string, error) {
	conns := Connections(org, "github")
	sort.SliceStable(conns, func(i, j int) bool {
		return strings.EqualFold(conns[i].Label, owner) && !strings.EqualFold(conns[j].Label, owner)
	})
	u := strings.TrimRight(githubAPIBase, "/") + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	for _, conn := range conns {
		id, err := strconv.ParseInt(strings.TrimSpace(conn.ExternalID), 10, 64)
		if err != nil {
			continue
		}
		tok, err := ghApp.installationToken(ctx, id)
		if err != nil {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", "", "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := githubHTTP.Do(req) // follows the redirect, and keeps the header on the same host
		if err != nil {
			continue
		}
		var r struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		}
		ok := resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r) == nil
		_ = resp.Body.Close()
		if o, n, found := strings.Cut(r.FullName, "/"); ok && found && ghName.MatchString(o) && ghName.MatchString(n) {
			return o, n, r.DefaultBranch, nil
		}
	}
	return "", "", "", fmt.Errorf("github grant: none of this org's GitHub connections can see %s/%s", owner, repo)
}

// grant mints a token for owner/repo on installation id, carrying grantPerms.
//
// NEVER CACHED. installationToken shares one token across a burst; this one is
// a run's, and the run revokes it — a shared one would be revoked under its
// neighbours.
//
// GitHub's ANSWER is checked, not just its status. A connection resolved by the
// single-account fallback can be the wrong account, and a same-named repository
// there would come back as a token for somebody else's code. A token that does
// not reach exactly owner/repo with exactly grantPerms never leaves this process.
func (g *ghAppState) grant(ctx context.Context, id int64, owner, repo string, perms map[string]string) (string, error) {
	tr, err := g.transport()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{"repositories": []string{repo}, "permissions": perms})
	if err != nil {
		return "", err
	}
	u := fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimRight(githubAPIBase, "/"), id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: tr, Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("github grant: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", refused(owner, resp.StatusCode, b)
	}
	var out struct {
		Token        string            `json:"token"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("github grant: no token in the answer")
	}
	if len(out.Repositories) != 1 || !strings.EqualFold(out.Repositories[0].FullName, owner+"/"+repo) {
		return "", fmt.Errorf("github grant: this org's GitHub connection does not reach %s/%s; connect %s", owner, repo, owner)
	}
	for p, level := range out.Permissions {
		if want, ok := perms[p]; (ok && level == want) || (p == "metadata" && level == "read") {
			continue // GitHub adds metadata:read to every token
		}
		return "", fmt.Errorf("github grant: GitHub answered %s:%s, which was not asked for", p, level)
	}
	for p, want := range perms {
		if out.Permissions[p] != want {
			return "", fmt.Errorf("github grant: GitHub did not grant %s:%s", p, want)
		}
	}
	return out.Token, nil
}

// refused turns GitHub's refusal into what a person does next. A 422 naming
// permissions is the App lacking one on the account, which is an owner's
// approval to give and never something to work around.
func refused(owner string, status int, body []byte) error {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	if status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(e.Message), "permission") {
		return fmt.Errorf("github grant: the App lacks contents:write or pull_requests:write on %s; an owner of %s must approve them", owner, owner)
	}
	return fmt.Errorf("github grant: %d %s", status, truncateBody(body))
}
