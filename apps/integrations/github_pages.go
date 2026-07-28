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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
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
	// Repo is the repository, from the :repo path segment.
	Repo string `json:"repo"`
	// Branch is the legacy source branch; empty defaults to the repo's own default
	// branch. Ignored when buildType is "workflow".
	Branch string `json:"branch"`
	// Path is the source directory within the branch: "/" (the default) or "/docs".
	// GitHub allows no others.
	Path string `json:"path"`
	// BuildType selects the builder: "workflow" builds via GitHub Actions, anything
	// else builds from the branch source above.
	BuildType string `json:"buildType"`
}

// ref is the repo this request addresses, normalized like every other :repo op.
func (r githubPagesEnableReq) ref() repoRef { return repoRef{Repo: r.Repo} }

// githubPagesUpdateReq updates a live site. A nil pointer leaves a field unchanged;
// CNAME=="" clears the custom domain, a non-empty CNAME sets it.
type githubPagesUpdateReq struct {
	// Repo is the repository, from the :repo path segment.
	Repo string `json:"repo"`
	// CNAME is the custom domain. Omit to leave it alone, "" to clear it, or a
	// valid FQDN to set it.
	CNAME *string `json:"cname"`
	// HTTPSEnforced toggles GitHub's enforce-HTTPS bit. Omit to leave it alone.
	HTTPSEnforced *bool `json:"httpsEnforced"`
	// BuildType switches the builder: "legacy" or "workflow". Empty leaves it.
	BuildType string `json:"buildType"`
	// Branch switches the legacy source branch. Empty leaves the source alone.
	Branch string `json:"branch"`
	// Path is the source directory to pair with Branch: "/" (the default) or
	// "/docs". Read only when Branch is given.
	Path string `json:"path"`
}

// ref is the repo this request addresses, normalized like every other :repo op.
func (r githubPagesUpdateReq) ref() repoRef { return repoRef{Repo: r.Repo} }

// githubPagesUpdatedOut acknowledges an update. The site's new state is a GET away;
// GitHub's PUT answers no body of its own.
type githubPagesUpdatedOut struct {
	// Repo is the repository that was updated.
	Repo string `json:"repo"`
	// Updated is always true — a failure is an HTTP error, never this shape.
	Updated bool `json:"updated"`
}

// githubPagesDisabledOut acknowledges that the site was deleted.
type githubPagesDisabledOut struct {
	// Repo is the repository whose site was deleted.
	Repo string `json:"repo"`
	// Disabled is always true — a failure is an HTTP error, never this shape.
	Disabled bool `json:"disabled"`
}

// ── response views (client, camelCase) ───────────────────────────────────────

type githubPagesSource struct {
	// Branch is the branch the site builds from.
	Branch string `json:"branch"`
	// Path is the directory within that branch: "/" or "/docs".
	Path string `json:"path"`
}

type githubPagesView struct {
	// Repo is the repository the site belongs to.
	Repo string `json:"repo"`
	// Status is GitHub's build state: "built", "building" or "errored". Absent
	// before the first build.
	Status string `json:"status,omitempty"`
	// URL is the live site (GitHub's html_url).
	URL string `json:"url,omitempty"`
	// CNAME is the custom domain, absent when none is set.
	CNAME string `json:"cname,omitempty"`
	// Custom404 is whether the repo ships its own 404 page.
	Custom404 bool `json:"custom404"`
	// BuildType is the builder in use: "legacy" (branch source) or "workflow".
	BuildType string `json:"buildType,omitempty"`
	// HTTPSEnforced is GitHub's enforce-HTTPS bit.
	HTTPSEnforced bool `json:"httpsEnforced"`
	// Source is the branch + path the site builds from. Absent under "workflow".
	Source *githubPagesSource `json:"source,omitempty"`
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

// grantTTL bounds how long a cached installation grant set is trusted. A revoked repo
// can widen the isolation window by at most grantTTL, so keep it short.
const grantTTL = 45 * time.Second

type grantEntry struct {
	repos []githubRepo
	exp   time.Time
}

// grantCache memoizes each installation's granted repo set for grantTTL, keyed by
// INSTALLATION ID — never the org name. Per-installation keying is the isolation
// invariant: an org that reconnects to a different installation gets a different key
// (never a stale grant from the old one), and the TTL bounds how long a revoked grant
// is honored. It caps installationRepos (up to 100 upstream calls) to at most once per
// grantTTL per installation — collapsing the status-poll self-DoS (LOW-1) without ever
// widening cross-tenant scope.
var (
	grantMu    sync.Mutex
	grantCache = map[int64]grantEntry{}
)

// resetGrantCache drops every cached grant set. Called by resetGithubApp so a test's
// fresh mock is never shadowed by a prior test's grants.
func resetGrantCache() {
	grantMu.Lock()
	grantCache = map[int64]grantEntry{}
	grantMu.Unlock()
}

// grantedRepos returns the installation's granted repo set — from the per-installation
// cache when fresh, else one installationRepos fetch that is then cached for grantTTL.
func grantedRepos(ctx context.Context, instID int64, token string) ([]githubRepo, error) {
	grantMu.Lock()
	if e, ok := grantCache[instID]; ok && time.Now().Before(e.exp) {
		repos := e.repos
		grantMu.Unlock()
		return repos, nil
	}
	grantMu.Unlock()
	repos, err := installationRepos(ctx, token)
	if err != nil {
		return nil, err
	}
	grantMu.Lock()
	grantCache[instID] = grantEntry{repos: repos, exp: time.Now().Add(grantTTL)}
	grantMu.Unlock()
	return repos, nil
}

// installationID resolves the org's connected GitHub installation id — the grant-cache
// key. githubTokenForOrg has already proven the connection exists (409 otherwise), so
// this only re-reads the custodied id; a malformed id is an honest 502.
func installationID(org string) (int64, error) {
	conn, ok := ConnectionFor(org, "github")
	if !ok {
		return 0, zip.Errorf(http.StatusConflict, "github is not connected for this organization")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(conn.ExternalID), 10, 64)
	if err != nil {
		return 0, zip.Errorf(http.StatusBadGateway, "invalid github installation id")
	}
	return id, nil
}

// resolveGrantedRepo mints the org's installation token and confirms repoName is in
// the installation's GRANTED set (via grantedRepos, cached per-installation), returning
// that token plus the repo's server-side full_name and default branch (from GitHub,
// never the client). Fail-closed: an unknown or ungranted repo yields a 404 — an org
// can never address a repo its installation was not granted, and the owner in the
// GitHub API path is never taken from the request.
func resolveGrantedRepo(ctx context.Context, org, repoName string) (pagesRepo, error) {
	tok, herr := githubTokenForOrg(ctx, org)
	if herr != nil {
		return pagesRepo{}, herr
	}
	instID, herr := installationID(org)
	if herr != nil {
		return pagesRepo{}, herr
	}
	repos, err := grantedRepos(ctx, instID, tok)
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
func (pr pagesRepo) request(ctx context.Context, method, subpath string, body []byte) (int, []byte, http.Header, error) {
	owner, name, ok := splitFullName(pr.fullName)
	if !ok {
		return 0, nil, nil, fmt.Errorf("invalid repository full name")
	}
	endpoint := strings.TrimRight(githubAPIBase, "/") + "/repos/" +
		url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pages" + subpath
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pr.token)
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

// pagesErr maps a GitHub non-2xx status to an honest client error. A 403/429 that is
// really a rate limit (x-ratelimit-remaining: 0, a retry-after header, or a rate-limit
// message body) is surfaced as 429 with the retry hint — NOT as a permissions problem
// (LOW-2), so a self-inflicted rate-limit is not misreported as "re-authorize". A
// genuine permission 403 keeps the actionable re-authorize message. GitHub error
// bodies carry only a validation message (never the token, which lives only in the
// request header — and truncateBody redacts any credential-shaped substring anyway).
func pagesErr(status int, body []byte, hdr http.Header) error {
	if rl, retry := rateLimited(status, body, hdr); rl {
		if retry != "" {
			return zip.Errorf(http.StatusTooManyRequests, "github rate limit reached; retry after %ss", retry)
		}
		return zip.Errorf(http.StatusTooManyRequests, "github rate limit reached; retry shortly")
	}
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

// rateLimited reports whether a GitHub response is a rate-limit rejection (primary:
// 403 + x-ratelimit-remaining 0; secondary: 403/429 + retry-after or a rate-limit
// message), plus the Retry-After seconds when GitHub supplied them. A plain 403 with
// no rate-limit signal is NOT rate-limited — it falls through to the permission case.
func rateLimited(status int, body []byte, hdr http.Header) (bool, string) {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return false, ""
	}
	var retry string
	if hdr != nil {
		retry = strings.TrimSpace(hdr.Get("Retry-After"))
		if hdr.Get("X-RateLimit-Remaining") == "0" {
			return true, retry
		}
		if retry != "" {
			return true, retry
		}
	}
	if b := strings.ToLower(string(body)); strings.Contains(b, "rate limit") || strings.Contains(b, "secondary rate") {
		return true, retry
	}
	return status == http.StatusTooManyRequests, retry
}

// ── handlers ─────────────────────────────────────────────────────────────────

// pagesTarget validates the principal + the {repo} segment and resolves the granted
// repo. It is the single front gate every Pages handler runs first, so isolation
// (principal org, DNS-1123 org, valid repo grammar, grant check) is enforced in ONE
// place and every action is uniformly 404 for a repo the org's installation cannot
// touch — before any request body is read.
// pagesTarget is ONE resolver for both handler shapes: the org comes off the
// request CONTEXT (cloud.Bridge parks it at /v1/integrations), so a typed op and
// the raw /builds handler resolve their target through the same three steps —
// principal gate, repo-name grammar, grant lookup.
func pagesTarget(ctx context.Context, repo string) (pagesRepo, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return pagesRepo{}, err
	}
	if !validRepoName(repo) {
		return pagesRepo{}, zip.ErrBadRequest("repo must be a valid repository name")
	}
	return resolveGrantedRepo(ctx, org, repo)
}

// githubPagesGet returns the repo's Pages status, live URL, custom domain and build
// source. The repo is resolved against the org installation's GRANTED set, so a
// caller can never address a repo the App was not granted; 404 when the repo has no
// Pages site.
//
// Example: {"repo":"widgets"}
// Response: {"repo":"widgets","status":"built","url":"https://acme.github.io/widgets/","cname":"docs.acme.com","custom404":false,"buildType":"legacy","httpsEnforced":true,"source":{"branch":"main","path":"/docs"}}
func (o ops) githubPagesGet(ctx context.Context, in *repoRef) (*githubPagesView, error) {
	repo := in.name()
	pr, err := pagesTarget(ctx, repo)
	if err != nil {
		return nil, err
	}
	status, body, hdr, callErr := pr.request(ctx, http.MethodGet, "", nil)
	if callErr != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status == http.StatusNotFound {
		return nil, zip.Errorf(http.StatusNotFound, "github pages is not enabled for this repository")
	}
	if status/100 != 2 {
		return nil, pagesErr(status, body, hdr)
	}
	var site githubPagesSite
	if err := json.Unmarshal(body, &site); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github decode: %v", err)
	}
	v := pagesView(repo, site)
	return &v, nil
}

// githubPagesEnable creates the repo's Pages site and answers 201 Created with it.
// With buildType "workflow" the site builds via GitHub Actions; otherwise it builds
// from a branch source, defaulting to the repo's own default branch when none is
// given. Only "/" and "/docs" are legal source paths (GitHub's rule).
//
// Example: {"repo":"widgets","branch":"main","path":"/docs"}
// Response: {"repo":"widgets","status":"building","url":"https://acme.github.io/widgets/","custom404":false,"buildType":"legacy","httpsEnforced":true,"source":{"branch":"main","path":"/docs"}}
func (o ops) githubPagesEnable(ctx context.Context, in *githubPagesEnableReq) (*githubPagesView, error) {
	repo := in.ref().name()
	pr, err := pagesTarget(ctx, repo)
	if err != nil {
		return nil, err
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
			return nil, zip.ErrBadRequest("provide branch (source) or buildType=workflow")
		}
		if !validGitRef(branch) {
			return nil, zip.ErrBadRequest("branch is not a valid git ref")
		}
		path, perr := normalizePagesPath(in.Path)
		if perr != nil {
			return nil, perr
		}
		out["source"] = map[string]string{"branch": branch, "path": path}
	}
	raw, _ := json.Marshal(out)
	status, body, hdr, callErr := pr.request(ctx, http.MethodPost, "", raw)
	if callErr != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status/100 != 2 {
		return nil, pagesErr(status, body, hdr)
	}
	var site githubPagesSite
	_ = json.Unmarshal(body, &site)
	// A creator, so 201 — the one status a typed op cannot state in its signature.
	cloud.Created(ctx)
	v := pagesView(repo, site)
	return &v, nil
}

// githubPagesUpdate sets or clears the custom domain (cname) and updates HTTPS
// enforcement, build type, or source. ONLY the provided fields are sent to GitHub,
// so an update never resets a setting the caller did not mention.
//
// Example: {"repo":"widgets","cname":"docs.acme.com","httpsEnforced":true}
// Response: {"repo":"widgets","updated":true}
func (o ops) githubPagesUpdate(ctx context.Context, in *githubPagesUpdateReq) (*githubPagesUpdatedOut, error) {
	repo := in.ref().name()
	pr, err := pagesTarget(ctx, repo)
	if err != nil {
		return nil, err
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
			return nil, zip.ErrBadRequest("cname must be a valid domain name")
		}
	}
	if in.HTTPSEnforced != nil {
		out["https_enforced"] = *in.HTTPSEnforced
	}
	if bt := strings.TrimSpace(in.BuildType); bt != "" {
		if bt != "legacy" && bt != "workflow" {
			return nil, zip.ErrBadRequest("buildType must be legacy or workflow")
		}
		out["build_type"] = bt
	}
	if br := strings.TrimSpace(in.Branch); br != "" {
		if !validGitRef(br) {
			return nil, zip.ErrBadRequest("branch is not a valid git ref")
		}
		path, perr := normalizePagesPath(in.Path)
		if perr != nil {
			return nil, perr
		}
		out["source"] = map[string]string{"branch": br, "path": path}
	}
	if len(out) == 0 {
		return nil, zip.ErrBadRequest("no fields to update (cname, httpsEnforced, buildType, or branch)")
	}
	raw, _ := json.Marshal(out)
	status, body, hdr, callErr := pr.request(ctx, http.MethodPut, "", raw)
	if callErr != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status == http.StatusNotFound {
		return nil, zip.Errorf(http.StatusNotFound, "github pages is not enabled for this repository")
	}
	if status/100 != 2 {
		return nil, pagesErr(status, body, hdr)
	}
	return &githubPagesUpdatedOut{Repo: repo, Updated: true}, nil
}

// githubPagesDisable deletes the repo's Pages site. 404 when there is none, so a
// caller can tell "turned it off" from "there was nothing on".
//
// Example: {"repo":"widgets"}
// Response: {"repo":"widgets","disabled":true}
func (o ops) githubPagesDisable(ctx context.Context, in *repoRef) (*githubPagesDisabledOut, error) {
	repo := in.name()
	pr, err := pagesTarget(ctx, repo)
	if err != nil {
		return nil, err
	}
	status, body, hdr, callErr := pr.request(ctx, http.MethodDelete, "", nil)
	if callErr != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status == http.StatusNotFound {
		return nil, zip.Errorf(http.StatusNotFound, "github pages is not enabled for this repository")
	}
	if status/100 != 2 {
		return nil, pagesErr(status, body, hdr)
	}
	return &githubPagesDisabledOut{Repo: repo, Disabled: true}, nil
}

// githubPagesBuild requests a Pages rebuild and returns the queued build's status.
// RAW (202 Accepted): the build is queued at GitHub, not completed here.
func githubPagesBuild(_ *cloud.Service[state], c *zip.Ctx) error {
	repo := strings.TrimSuffix(strings.TrimSpace(c.Param("repo")), ".git")
	pr, err := pagesTarget(c.Context(), repo)
	if err != nil {
		return err
	}
	status, body, hdr, callErr := pr.request(c.Context(), http.MethodPost, "/builds", nil)
	if callErr != nil {
		return zip.Errorf(http.StatusBadGateway, "github pages: %v", callErr)
	}
	if status == http.StatusNotFound {
		return zip.Errorf(http.StatusNotFound, "github pages is not enabled for this repository")
	}
	if status/100 != 2 {
		return pagesErr(status, body, hdr)
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
