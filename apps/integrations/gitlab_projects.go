package integrations

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/zap-proto/zip"
)

// gitlab_projects.go answers the question the GitLab connector was built to
// answer and never could: WHICH projects does this connection reach.
//
// The token is sealed in the org's KMS namespace and there is no route that
// hands it out — deliberately. So a caller that must ACT on the connection asks
// this package to act instead, exactly as the GitHub App routes beside it do.
// hanzo.app's import panel is the first caller: it lists these rows and clones
// the one a person picks onto git.hanzo.ai.
//
// Org-authed: the org comes from the validated principal, so a caller can only
// ever see the projects of the connection their own org holds.

// gitlabProjectView is one project the connection can reach. Names mirror
// githubRepoView so a client renders both lists through one shape.
type gitlabProjectView struct {
	// Name is the project's path segment ("widgets"), not its display name.
	Name string `json:"name"`
	// FullName is the namespace path ("acme/widgets", "acme/team/widgets" for a
	// subgroup) — the string GitLab calls path_with_namespace.
	FullName string `json:"fullName"`
	// Private is true for anything not publicly visible (private or internal).
	Private bool `json:"private"`
	// Description is the project's own, empty when it has none.
	Description string `json:"description,omitempty"`
	// DefaultBranch is the branch a clone lands on ("main" when GitLab names none,
	// which is what an empty project reports).
	DefaultBranch string `json:"defaultBranch"`
	// PushedAt is RFC3339 last activity, so a client can sort or say "2h ago".
	PushedAt string `json:"pushedAt,omitempty"`
	// CloneURL is the https remote to clone.
	CloneURL string `json:"cloneUrl"`
	// HTMLURL is the project's page.
	HTMLURL string `json:"htmlUrl,omitempty"`
}

// gitlabProjectsOut is the connection's reachable project set.
type gitlabProjectsOut struct {
	// Projects is every project the token reaches, newest activity first. Never
	// null; [] when the account has none.
	Projects []gitlabProjectView `json:"projects"`
	// Account is the connected GitLab username, so a client can label the list
	// without a second call. Empty when the connection recorded none.
	Account string `json:"account,omitempty"`
}

// gitlabProjects lists the projects the org's GitLab connection can reach —
// membership projects, most recently active first.
//
// Response: {"projects":[{"name":"widgets","fullName":"acme/widgets","private":true,"defaultBranch":"main","pushedAt":"2026-07-01T10:00:00Z","cloneUrl":"https://gitlab.com/acme/widgets.git","htmlUrl":"https://gitlab.com/acme/widgets"}],"account":"acme"}
func (o ops) gitlabProjects(ctx context.Context, _ *cloud.Unit) (*gitlabProjectsOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := orgProvider(s, "gitlab")
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	conns, err := s.State.store.ListFor(ctx, org, p.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if len(conns) == 0 {
		return nil, zip.ErrNotFound("gitlab is not connected")
	}
	if !kmsReady(s) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", errCredentialStore)
	}
	tok, err := kmsGet(s, kmsPath(org, p.ID), accessSecret)
	if err != nil || len(tok) == 0 {
		return nil, zip.ErrBadRequest("stored credential unavailable")
	}
	projects, err := gitlabListProjects(ctx, string(tok))
	if err != nil {
		return nil, err
	}
	return &gitlabProjectsOut{Projects: projects, Account: conns[0].AccountLabel}, nil
}

// gitlabProject is the subset of GitLab's project object this surface reads.
type gitlabProject struct {
	Name              string `json:"name"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	Visibility        string `json:"visibility"`
	Description       string `json:"description"`
	LastActivityAt    string `json:"last_activity_at"`
	DefaultBranch     string `json:"default_branch"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	WebURL            string `json:"web_url"`
}

// gitlabProjectsPerPage is GitLab's maximum page size, and gitlabProjectsPages
// bounds the walk: 5 pages is 500 projects, past which a person is filtering,
// not scrolling. An unbounded walk would let one account with thousands of
// projects hold a request open against a rate-limited API.
const (
	gitlabProjectsPerPage = 100
	gitlabProjectsPages   = 5
)

// gitlabListProjects walks the membership projects the token reaches. It stops
// at the first short page — GitLab returns fewer than per_page only on the last
// one — so an account with 12 projects costs exactly one call.
func gitlabListProjects(ctx context.Context, token string) ([]gitlabProjectView, error) {
	out := make([]gitlabProjectView, 0, gitlabProjectsPerPage)
	for page := 1; page <= gitlabProjectsPages; page++ {
		q := url.Values{
			"membership": {"true"},
			"per_page":   {strconv.Itoa(gitlabProjectsPerPage)},
			"page":       {strconv.Itoa(page)},
			"order_by":   {"last_activity_at"},
			"sort":       {"desc"},
			// An archived project cannot be pushed to, so importing one produces a
			// repository nobody can move. Ask GitLab to leave them out rather than
			// filtering after the fact.
			"archived": {"false"},
		}
		rows, err := gitlabGetProjects(ctx, token, gitlabBase+"/api/v4/projects?"+q.Encode())
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			name := r.Path
			if name == "" {
				name = r.Name
			}
			out = append(out, gitlabProjectView{
				Name:     name,
				FullName: r.PathWithNamespace,
				// GitLab has three visibilities and only "public" is reachable
				// without the token, so anything else is private to a client.
				Private:       r.Visibility != "public",
				Description:   r.Description,
				DefaultBranch: branchOr(r.DefaultBranch, "main"),
				PushedAt:      r.LastActivityAt,
				CloneURL:      r.HTTPURLToRepo,
				HTMLURL:       r.WebURL,
			})
		}
		if len(rows) < gitlabProjectsPerPage {
			break
		}
	}
	return out, nil
}

// gitlabGetProjects performs one page request against the GitLab API with the
// connection's token. A 401 is reported as such — the token was revoked at
// GitLab, which the caller must be able to tell from an outage.
func gitlabGetProjects(ctx context.Context, token, endpoint string) ([]gitlabProject, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "gitlab request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := gitlabHTTP.Do(req)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "gitlab call: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "gitlab read: %v", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, zip.Errorf(http.StatusUnauthorized, "the GitLab connection was revoked — connect it again")
	}
	if resp.StatusCode/100 != 2 {
		return nil, zip.Errorf(http.StatusBadGateway, "gitlab http %d: %s", resp.StatusCode, truncateBody(body))
	}
	var rows []gitlabProject
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "gitlab decode: %v", err)
	}
	return rows, nil
}

// branchOr names the branch a clone lands on. GitLab reports an empty
// default_branch for a project with no commits yet, and a client that renders
// that draws a blank where a branch name belongs.
func branchOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
