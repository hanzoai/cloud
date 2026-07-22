package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// github_pages.go surfaces GitHub Pages management on the SAME installation token the
// repo sync uses (github_app.go). Five org-authed routes, siblings of the repo
// list/import, addressing one repo as a resource:
//
//	GET    /v1/integrations/github/repos/{repo}/pages         — status + live URL + custom domain
//	POST   /v1/integrations/github/repos/{repo}/pages         — enable/configure (source branch or Actions)
//	PUT    /v1/integrations/github/repos/{repo}/pages         — set/clear custom domain, HTTPS, source
//	DELETE /v1/integrations/github/repos/{repo}/pages         — disable
//	POST   /v1/integrations/github/repos/{repo}/pages/builds  — request a build
//
// Isolation is the whole point. The org comes from the validated principal, never a
// request field; the requested repo NAME is resolved against the installation's
// GRANTED set (installationRepos) and the owner is taken SERVER-SIDE from GitHub's
// own full_name — so a caller can neither address a repo its installation was not
// granted nor inject an owner into the GitHub API path. The installation token rides
// only the Authorization header — never a log, never argv, never the response we
// surface. Fail-closed at every step: unconfigured ⇒ 503, unconnected ⇒ 409,
// ungranted ⇒ 404.
//
// These are control-plane management actions in the exact class as the repo
// list/import above — they carry no bespoke metering; the platform's request-tracing
// middleware is the one observability path, and org-scoped auth the one gate.

// repoNameRE is the addressable repository grammar for the {repo} path segment. A
// leading alphanumeric bars "."/".."/leading-dash traversal shapes; the segment can
// never contain a slash (one path label), so it can never inject extra GitHub API
// path. A user/org Pages repo is `<owner>.github.io`, so internal dots are allowed.
var repoNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

func validRepoName(s string) bool { return repoNameRE.MatchString(s) }

// gitRefRE is a conservative branch-name grammar for a Pages source. Combined with
// the no-".."/no-leading-or-trailing-slash guard in validGitRef, it rejects the
// dangerous ref shapes while allowing normal branches (main, gh-pages, release/1.2).
var gitRefRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

func validGitRef(s string) bool {
	if strings.Contains(s, "..") || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
		return false
	}
	return gitRefRE.MatchString(s)
}

// domainRE is a strict FQDN grammar for a custom domain (CNAME). It requires at least
// one dot (a real public host), bounds each label, and — being a fixed character
// class — rejects spaces, CR/LF, and control characters, so a crafted cname can never
// smuggle a header or an extra host into GitHub's API body.
var domainRE = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

func validCustomDomain(s string) bool { return len(s) <= 253 && domainRE.MatchString(s) }

// ── GitHub API shapes ────────────────────────────────────────────────────────

// githubPagesSite is the GitHub Pages object (GET/POST responses). status/cname are
// pointers because GitHub returns them as null before the first build / with no
// custom domain — a pointer distinguishes "absent" from the empty string.
type githubPagesSite struct {
	Status        *string `json:"status"`
	CNAME         *string `json:"cname"`
	Custom404     bool    `json:"custom_404"`
	HTMLURL       string  `json:"html_url"`
	BuildType     string  `json:"build_type"`
	HTTPSEnforced bool    `json:"https_enforced"`
	Source        struct {
		Branch string `json:"branch"`
		Path   string `json:"path"`
	} `json:"source"`
}

// ── request bodies (client, camelCase) ───────────────────────────────────────

type githubPagesEnableReq struct {
	Branch    string `json:"branch"`    // legacy source branch; defaults to the repo's default branch
	Path      string `json:"path"`      // "/" or "/docs"
	BuildType string `json:"buildType"` // "workflow" builds via GitHub Actions; else a branch source
}

// githubPagesUpdateReq updates a live site. A nil pointer leaves a field unchanged;
// CNAME=="" clears the custom domain, a non-empty CNAME sets it.
type githubPagesUpdateReq struct {
	CNAME         *string `json:"cname"`
	HTTPSEnforced *bool   `json:"httpsEnforced"`
	BuildType     string  `json:"buildType"`
	Branch        string  `json:"branch"`
	Path          string  `json:"path"`
}

// ── response views (client, camelCase) ───────────────────────────────────────

type githubPagesSource struct {
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

type githubPagesView struct {
	Repo          string             `json:"repo"`
	Status        string             `json:"status,omitempty"` // "built" | "building" | "errored"
	URL           string             `json:"url,omitempty"`    // the live site URL (html_url)
	CNAME         string             `json:"cname,omitempty"`  // custom domain, if set
	Custom404     bool               `json:"custom404"`
	BuildType     string             `json:"buildType,omitempty"`
	HTTPSEnforced bool               `json:"httpsEnforced"`
	Source        *githubPagesSource `json:"source,omitempty"`
}

func pagesView(repo string, s githubPagesSite) githubPagesView {
	v := githubPagesView{
		Repo:          repo,
		URL:           s.HTMLURL,
		Custom404:     s.Custom404,
		BuildType:     s.BuildType,
		HTTPSEnforced: s.HTTPSEnforced,
	}
	if s.Status != nil {
		v.Status = *s.Status
	}
	if s.CNAME != nil {
		v.CNAME = *s.CNAME
	}
	if s.Source.Branch != "" || s.Source.Path != "" {
		v.Source = &githubPagesSource{Branch: s.Source.Branch, Path: s.Source.Path}
	}
	return v
}

// ── grant resolution + GitHub call ───────────────────────────────────────────

// pagesRepo carries the two things every Pages call needs, both server-derived: the
// org's short-lived installation token and the repo's GitHub full_name (owner/name).
type pagesRepo struct {
	token         string
	fullName      string
	defaultBranch string
}

// resolveGrantedRepo mints the org's installation token and confirms repoName is in
// the installation's GRANTED set, returning that token plus the repo's server-side
// full_name and default branch (from GitHub, never the client). Fail-closed: an
// unknown or ungranted repo yields a 404 — an org can never address a repo its
// installation was not granted, and the owner in the GitHub API path is never taken
// from the request.
func resolveGrantedRepo(ctx context.Context, org, repoName string) (pagesRepo, error) {
	tok, herr := githubTokenForOrg(ctx, org)
	if herr != nil {
		return pagesRepo{}, herr
	}
	repos, err := installationRepos(ctx, tok)
	if err != nil {
		return pagesRepo{}, zip.Errorf(http.StatusBadGateway, "list github repositories: %v", err)
	}
	for _, r := range repos {
		if r.Name == repoName {
			return pagesRepo{token: tok, fullName: r.FullName, defaultBranch: r.DefaultBranch}, nil
		}
	}
	return pagesRepo{}, zip.Errorf(http.StatusNotFound, "repository is not granted to this organization's installation")
}

// splitFullName splits a GitHub "owner/name" into its parts. owner and name each hold
// exactly one path label (GitHub allows no slash in either), so the result can never
// widen the GitHub API path.
func splitFullName(full string) (owner, name string, ok bool) {
	owner, name, found := strings.Cut(full, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}

// pagesRequest performs one GitHub Pages API call for the resolved repo. subpath is
// "" for the site resource or "/builds" for a build. The token rides only the
// Authorization header; owner/name are path-escaped (defense in depth — they come
// from GitHub). Returns the raw status and a bounded body.
func (pr pagesRepo) request(ctx context.Context, method, subpath string, body []byte) (int, []byte, error) {
	owner, name, ok := splitFullName(pr.fullName)
	if !ok {
		return 0, nil, fmt.Errorf("invalid repository full name")
	}
	endpoint := strings.TrimRight(githubAPIBase, "/") + "/repos/" +
		url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pages" + subpath
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pr.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("github call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

// pagesErr maps a GitHub non-2xx status to an honest client error. GitHub error
// bodies carry a validation message (never the token, which lives only in the request
// header), so surfacing a truncated body aids the operator without leaking a secret.
// A 403 means the App lacks the Pages permission — actionable, so it is called out.
func pagesErr(status int, body []byte) error {
	switch status {
	case http.StatusForbidden:
		return zip.Errorf(http.StatusForbidden,
			"the Hanzo GitHub App is not authorized for Pages on this repository; re-authorize the installation with Pages access")
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		return zip.Errorf(status, "github rejected the pages request: %s", truncateBody(body))
	default:
		return zip.Errorf(http.StatusBadGateway, "github pages http %d: %s", status, truncateBody(body))
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

// pagesTarget validates the principal + the {repo} segment and resolves the granted
// repo. It is the single front gate every Pages handler runs first, so isolation
// (principal org, DNS-1123 org, valid repo grammar, grant check) is enforced in ONE
// place and every action is uniformly 404 for a repo the org's installation cannot
// touch — before any request body is read.
func pagesTarget(c *zip.Ctx) (string, pagesRepo, error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", pagesRepo{}, zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return "", pagesRepo{}, zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	repo := strings.TrimSuffix(strings.TrimSpace(c.Param("repo")), ".git")
	if !validRepoName(repo) {
		return "", pagesRepo{}, zip.ErrBadRequest("repo must be a valid repository name")
	}
	pr, err := resolveGrantedRepo(c.Context(), org, repo)
	if err != nil {
		return "", pagesRepo{}, err
	}
	return repo, pr, nil
}

// githubPagesGet returns the repo's Pages status, live URL, and custom domain.
func githubPagesGet(_ *cloud.Service[state], c *zip.Ctx) error {
	repo, pr, err := pagesTarget(c)
	if err != nil {
		return err
	}
	status, body, callErr := pr.request(c.Context(), http.MethodGet, "", nil)
	if callErr != nil {
		return zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status == http.StatusNotFound {
		return zip.Errorf(http.StatusNotFound, "github pages is not enabled for this repository")
	}
	if status/100 != 2 {
		return pagesErr(status, body)
	}
	var site githubPagesSite
	if err := json.Unmarshal(body, &site); err != nil {
		return zip.Errorf(http.StatusBadGateway, "github decode: %v", err)
	}
	return c.JSON(http.StatusOK, pagesView(repo, site))
}

// githubPagesEnable creates/configures the Pages site. With buildType=="workflow" the
// site builds via GitHub Actions; otherwise it builds from a branch source (defaulting
// to the repo's default branch when none is given).
func githubPagesEnable(_ *cloud.Service[state], c *zip.Ctx) error {
	repo, pr, err := pagesTarget(c)
	if err != nil {
		return err
	}
	var in githubPagesEnableReq
	if err := c.Bind(&in); err != nil {
		return err
	}
	out := map[string]any{}
	if strings.EqualFold(strings.TrimSpace(in.BuildType), "workflow") {
		out["build_type"] = "workflow"
	} else {
		branch := strings.TrimSpace(in.Branch)
		if branch == "" {
			branch = pr.defaultBranch
		}
		if branch == "" {
			return zip.ErrBadRequest("provide branch (source) or buildType=workflow")
		}
		if !validGitRef(branch) {
			return zip.ErrBadRequest("branch is not a valid git ref")
		}
		path, perr := normalizePagesPath(in.Path)
		if perr != nil {
			return perr
		}
		out["source"] = map[string]string{"branch": branch, "path": path}
	}
	raw, _ := json.Marshal(out)
	status, body, callErr := pr.request(c.Context(), http.MethodPost, "", raw)
	if callErr != nil {
		return zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status/100 != 2 {
		return pagesErr(status, body)
	}
	var site githubPagesSite
	_ = json.Unmarshal(body, &site)
	return c.JSON(http.StatusCreated, pagesView(repo, site))
}

// githubPagesUpdate sets/clears the custom domain (cname) and updates HTTPS
// enforcement, build type, or source. Only the provided fields are sent to GitHub.
func githubPagesUpdate(_ *cloud.Service[state], c *zip.Ctx) error {
	repo, pr, err := pagesTarget(c)
	if err != nil {
		return err
	}
	var in githubPagesUpdateReq
	if err := c.Bind(&in); err != nil {
		return err
	}
	out := map[string]any{}
	if in.CNAME != nil {
		cn := strings.TrimSpace(*in.CNAME)
		switch {
		case cn == "":
			out["cname"] = nil // clear the custom domain
		case validCustomDomain(cn):
			out["cname"] = cn
		default:
			return zip.ErrBadRequest("cname must be a valid domain name")
		}
	}
	if in.HTTPSEnforced != nil {
		out["https_enforced"] = *in.HTTPSEnforced
	}
	if bt := strings.TrimSpace(in.BuildType); bt != "" {
		if bt != "legacy" && bt != "workflow" {
			return zip.ErrBadRequest("buildType must be legacy or workflow")
		}
		out["build_type"] = bt
	}
	if br := strings.TrimSpace(in.Branch); br != "" {
		if !validGitRef(br) {
			return zip.ErrBadRequest("branch is not a valid git ref")
		}
		path, perr := normalizePagesPath(in.Path)
		if perr != nil {
			return perr
		}
		out["source"] = map[string]string{"branch": br, "path": path}
	}
	if len(out) == 0 {
		return zip.ErrBadRequest("no fields to update (cname, httpsEnforced, buildType, or branch)")
	}
	raw, _ := json.Marshal(out)
	status, body, callErr := pr.request(c.Context(), http.MethodPut, "", raw)
	if callErr != nil {
		return zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status == http.StatusNotFound {
		return zip.Errorf(http.StatusNotFound, "github pages is not enabled for this repository")
	}
	if status/100 != 2 {
		return pagesErr(status, body)
	}
	return c.JSON(http.StatusOK, map[string]any{"repo": repo, "updated": true})
}

// githubPagesDisable deletes the Pages site.
func githubPagesDisable(_ *cloud.Service[state], c *zip.Ctx) error {
	repo, pr, err := pagesTarget(c)
	if err != nil {
		return err
	}
	status, body, callErr := pr.request(c.Context(), http.MethodDelete, "", nil)
	if callErr != nil {
		return zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status == http.StatusNotFound {
		return zip.Errorf(http.StatusNotFound, "github pages is not enabled for this repository")
	}
	if status/100 != 2 {
		return pagesErr(status, body)
	}
	return c.JSON(http.StatusOK, map[string]any{"repo": repo, "disabled": true})
}

// githubPagesBuild requests a Pages rebuild and returns the queued build's status.
func githubPagesBuild(_ *cloud.Service[state], c *zip.Ctx) error {
	repo, pr, err := pagesTarget(c)
	if err != nil {
		return err
	}
	status, body, callErr := pr.request(c.Context(), http.MethodPost, "/builds", nil)
	if callErr != nil {
		return zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status == http.StatusNotFound {
		return zip.Errorf(http.StatusNotFound, "github pages is not enabled for this repository")
	}
	if status/100 != 2 {
		return pagesErr(status, body)
	}
	var b struct {
		URL    string `json:"url"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &b)
	return c.JSON(http.StatusAccepted, map[string]any{"repo": repo, "status": b.Status, "url": b.URL})
}

// normalizePagesPath enforces GitHub Pages' only two legal source paths: the repo
// root ("/") or "/docs". An empty path defaults to "/".
func normalizePagesPath(p string) (string, error) {
	switch strings.TrimSpace(p) {
	case "", "/":
		return "/", nil
	case "/docs":
		return "/docs", nil
	default:
		return "", zip.ErrBadRequest(`path must be "/" or "/docs"`)
	}
}
