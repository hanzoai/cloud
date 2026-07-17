package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// gitea.go makes GITEA (hanzo-git.hanzo.svc) the ONE git store the sync engine's git
// provider drives. It replaces the retired cloud embedded-store seams (InboundGitSync
// / ImportGitRepo / EnsureGitMirror) with Gitea's own primitives, so a git sync moves
// bytes through Gitea, never through /var/lib/cloud/git:
//
//   - INBOUND (external → Gitea): a fast-forward-only go-git fetch(source)→push(Gitea)
//     with the admin token. Gitea is CANONICAL — a diverged ref is a conflict, never
//     overwritten (the split-brain guard, now on Gitea). One branch on a push advance,
//     every branch on a manual reconcile.
//   - OUTBOUND (Gitea → external): a Gitea push-mirror (sync_on_commit) so Gitea itself
//     propagates every commit to the upstream — no cloud-side reactor, no stale store.
//
// The admin token (GIT_ADMIN_TOKEN, secret gitea-secrets/admin-token) is env-only and
// never logged; the base URL is GITEA_URL (default the in-cluster service DNS).

const (
	giteaURLEnv     = "GITEA_URL"
	giteaTokenEnv   = "GIT_ADMIN_TOKEN"
	giteaDefaultURL = "http://hanzo-git.hanzo.svc"
)

// errGiteaUnavailable fails the provider closed when GIT_ADMIN_TOKEN is unset, rather
// than silently touching the retired embedded store — never a silent success.
var errGiteaUnavailable = errors.New("sync: gitea store not configured (GIT_ADMIN_TOKEN)")

// gitea is a minimal Gitea REST + go-git client scoped to the operations the sync git
// provider needs. Stateless except the lazily-resolved admin login (the BasicAuth
// username go-git pushes AS); one value serves every org.
type gitea struct {
	base  string
	token string // admin token; env-only, never logged
	http  *http.Client
	login string // resolved admin login (lazy, whoami)
}

// giteaFromEnv builds the Gitea client from the environment, failing closed when the
// admin token is unset.
func giteaFromEnv() (*gitea, error) {
	tok := strings.TrimSpace(os.Getenv(giteaTokenEnv))
	if tok == "" {
		return nil, errGiteaUnavailable
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(giteaURLEnv)), "/")
	if base == "" {
		base = giteaDefaultURL
	}
	return &gitea{base: base, token: tok, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// api issues one authed Gitea REST call under /api/v1. out (when non-nil) decodes a 2xx
// JSON body. Returns the HTTP status; the admin token rides the Authorization header
// and is never placed in a URL or a log.
func (g *gitea) api(ctx context.Context, method, path string, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+"/api/v1"+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "token "+g.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return resp.StatusCode, err
		}
		return resp.StatusCode, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// whoami resolves (and caches) the admin token's Gitea login — the BasicAuth username
// go-git authenticates the push AS (Gitea's git-over-http takes login + token).
func (g *gitea) whoami(ctx context.Context) (string, error) {
	if g.login != "" {
		return g.login, nil
	}
	var u struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	}
	st, err := g.api(ctx, http.MethodGet, "/user", nil, &u)
	if err != nil {
		return "", err
	}
	if st != http.StatusOK {
		return "", fmt.Errorf("gitea /user: %d", st)
	}
	g.login = firstNonEmpty(u.Login, u.Username)
	if g.login == "" {
		return "", fmt.Errorf("gitea /user: empty login")
	}
	return g.login, nil
}

// ensureOrg makes the Gitea org (repo owner) exist. Idempotent: a present org is a
// no-op, a concurrent create (422) is treated as success.
func (g *gitea) ensureOrg(ctx context.Context, org string) error {
	st, err := g.api(ctx, http.MethodGet, "/orgs/"+url.PathEscape(org), nil, nil)
	if err != nil {
		return err
	}
	if st == http.StatusOK {
		return nil
	}
	if st != http.StatusNotFound {
		return fmt.Errorf("gitea get org %q: %d", org, st)
	}
	st, err = g.api(ctx, http.MethodPost, "/orgs", map[string]any{"username": org, "visibility": "private"}, nil)
	if err != nil {
		return err
	}
	if st/100 == 2 || st == http.StatusUnprocessableEntity {
		return nil
	}
	return fmt.Errorf("gitea create org %q: %d", org, st)
}

// ensureRepo makes owner/repo exist in Gitea (creating the org first when needed).
// Idempotent: a present repo is a no-op, a concurrent create (409) is success.
func (g *gitea) ensureRepo(ctx context.Context, owner, repo string) error {
	st, err := g.api(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), nil, nil)
	if err != nil {
		return err
	}
	if st == http.StatusOK {
		return nil
	}
	if st != http.StatusNotFound {
		return fmt.Errorf("gitea get repo %q/%q: %d", owner, repo, st)
	}
	if err := g.ensureOrg(ctx, owner); err != nil {
		return err
	}
	st, err = g.api(ctx, http.MethodPost, "/orgs/"+url.PathEscape(owner)+"/repos",
		map[string]any{"name": repo, "private": true, "auto_init": false}, nil)
	if err != nil {
		return err
	}
	if st/100 == 2 || st == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("gitea create repo %q/%q: %d", owner, repo, st)
}

// branchSHA returns the tip SHA of owner/repo@branch (ok=false when the repo or branch
// does not exist yet).
func (g *gitea) branchSHA(ctx context.Context, owner, repo, branch string) (string, bool, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	st, err := g.api(ctx, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/branches/"+url.PathEscape(branch), nil, &b)
	if err != nil {
		return "", false, err
	}
	if st == http.StatusNotFound {
		return "", false, nil
	}
	if st != http.StatusOK {
		return "", false, fmt.Errorf("gitea branch %q/%q@%q: %d", owner, repo, branch, st)
	}
	return b.Commit.ID, true, nil
}

// repoGitURL is the smart-HTTP clone/push URL for owner/repo on the Gitea host.
func (g *gitea) repoGitURL(owner, repo string) string {
	return g.base + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + ".git"
}

// mirrorIn fetches from sourceURL and fast-forward-only pushes into the Gitea repo
// owner/repo — the split-brain guard: a diverged Gitea ref is a conflict, left as-is
// (native/Gitea stays canonical). branch != "" advances that one branch (a source
// push); "" mirrors every branch (a manual reconcile). Returns whether Gitea moved.
// sourceToken authenticates the source fetch ("" ⇒ anonymous, a public repo).
func (g *gitea) mirrorIn(ctx context.Context, owner, repo, sourceURL, sourceToken, branch string) (bool, error) {
	if err := g.ensureRepo(ctx, owner, repo); err != nil {
		return false, fmt.Errorf("ensure repo: %w", err)
	}
	login, err := g.whoami(ctx)
	if err != nil {
		return false, fmt.Errorf("gitea whoami: %w", err)
	}
	fetchSpec, pushSpec := gitconfig.RefSpec("+refs/heads/*:refs/heads/*"), gitconfig.RefSpec("refs/heads/*:refs/heads/*")
	if branch != "" {
		ref := "refs/heads/" + branch
		fetchSpec, pushSpec = gitconfig.RefSpec("+"+ref+":"+ref), gitconfig.RefSpec(ref+":"+ref)
	}
	return fetchThenPush(ctx, sourceURL, sourceAuth(sourceToken), g.repoGitURL(owner, repo),
		&githttp.BasicAuth{Username: login, Password: g.token}, fetchSpec, pushSpec)
}

// fetchThenPush fetches fetchSpec from src into an ephemeral in-memory repo, then pushes
// pushSpec to dst. A non-force pushSpec keeps the push fast-forward-only: a diverged dst
// ref returns changed=false (conflict, dst preserved) rather than an error. Reports
// whether dst actually moved. Split out from mirrorIn so the transport core is testable
// with local remotes, no live Gitea.
func fetchThenPush(ctx context.Context, srcURL string, srcAuth transport.AuthMethod, dstURL string, dstAuth transport.AuthMethod, fetchSpec, pushSpec gitconfig.RefSpec) (bool, error) {
	mem, err := gogit.Init(memory.NewStorage(), nil)
	if err != nil {
		return false, err
	}
	src, err := mem.CreateRemote(&gitconfig.RemoteConfig{Name: "source", URLs: []string{srcURL}})
	if err != nil {
		return false, err
	}
	if err := src.FetchContext(ctx, &gogit.FetchOptions{
		RefSpecs: []gitconfig.RefSpec{fetchSpec}, Auth: srcAuth, Tags: gogit.NoTags,
	}); err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return false, fmt.Errorf("fetch source: %w", err)
	}
	dst, err := mem.CreateRemote(&gitconfig.RemoteConfig{Name: "gitea", URLs: []string{dstURL}})
	if err != nil {
		return false, err
	}
	err = dst.PushContext(ctx, &gogit.PushOptions{
		RemoteName: "gitea", RefSpecs: []gitconfig.RefSpec{pushSpec}, Auth: dstAuth,
	})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, gogit.NoErrAlreadyUpToDate):
		return false, nil
	case strings.Contains(err.Error(), "non-fast-forward"):
		// Gitea diverged from the source — canonical side preserved (split-brain guard).
		return false, nil
	default:
		return false, fmt.Errorf("push gitea: %w", err)
	}
}

// pushMirror is the subset of Gitea's push-mirror record the provider matches on.
type pushMirror struct {
	RemoteName    string `json:"remote_name"`
	RemoteAddress string `json:"remote_address"`
}

// listPushMirrors returns owner/repo's push-mirrors (nil when the repo is absent).
func (g *gitea) listPushMirrors(ctx context.Context, owner, repo string) ([]pushMirror, error) {
	var out []pushMirror
	st, err := g.api(ctx, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/push_mirrors", nil, &out)
	if err != nil {
		return nil, err
	}
	if st == http.StatusNotFound {
		return nil, nil
	}
	if st != http.StatusOK {
		return nil, fmt.Errorf("gitea list push_mirrors %q/%q: %d", owner, repo, st)
	}
	return out, nil
}

// ensurePushMirror declares a Gitea push-mirror from owner/repo to remoteURL with
// sync_on_commit, so Gitea propagates every commit to the upstream. Idempotent: an
// existing mirror to the same target is left untouched. remoteToken is stored as the
// mirror's write credential ("" ⇒ anonymous, a public upstream).
func (g *gitea) ensurePushMirror(ctx context.Context, owner, repo, remoteURL, remoteToken string) error {
	if err := g.ensureRepo(ctx, owner, repo); err != nil {
		return err
	}
	mirrors, err := g.listPushMirrors(ctx, owner, repo)
	if err != nil {
		return err
	}
	for _, m := range mirrors {
		if sameRemote(m.RemoteAddress, remoteURL) {
			return nil
		}
	}
	body := map[string]any{"remote_address": remoteURL, "sync_on_commit": true, "interval": "8h0m0s"}
	if strings.TrimSpace(remoteToken) != "" {
		body["remote_username"] = "x-access-token"
		body["remote_password"] = remoteToken
	}
	st, err := g.api(ctx, http.MethodPost,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/push_mirrors", body, nil)
	if err != nil {
		return err
	}
	if st/100 != 2 {
		return fmt.Errorf("gitea create push_mirror %q/%q: %d", owner, repo, st)
	}
	return nil
}

// removePushMirror tears down any Gitea push-mirror from owner/repo pointing at
// remoteURL (best-effort per mirror; a missing repo/mirror is a no-op).
func (g *gitea) removePushMirror(ctx context.Context, owner, repo, remoteURL string) error {
	mirrors, err := g.listPushMirrors(ctx, owner, repo)
	if err != nil {
		return err
	}
	for _, m := range mirrors {
		if !sameRemote(m.RemoteAddress, remoteURL) {
			continue
		}
		st, err := g.api(ctx, http.MethodDelete,
			"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/push_mirrors/"+url.PathEscape(m.RemoteName), nil, nil)
		if err != nil {
			return err
		}
		if st/100 != 2 && st != http.StatusNotFound {
			return fmt.Errorf("gitea delete push_mirror %q: %d", m.RemoteName, st)
		}
	}
	return nil
}

// sourceAuth builds the source-fetch credential. A GitHub App installation token (or a
// PAT) rides as HTTP basic "x-access-token:<token>" (GitHub's documented form; a
// token-as-password is accepted by Gitea/GitLab too). "" ⇒ anonymous.
func sourceAuth(token string) transport.AuthMethod {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return &githttp.BasicAuth{Username: "x-access-token", Password: token}
}

// sameRemote reports whether two git remotes name the same repo, ignoring userinfo,
// scheme, a trailing ".git", and case — Gitea masks the stored credential in the
// address it returns, so only host+path can be compared.
func sameRemote(a, b string) bool { return canonRemote(a) == canonRemote(b) }

func canonRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSuffix(raw, ".git"))
	}
	// host + path, case-folded: the sync source is always github.com/gitlab.com, whose
	// owner/repo is case-insensitive, so a case variant is the same mirror (never a dup).
	return strings.ToLower(u.Host + strings.TrimSuffix(u.Path, ".git"))
}

// firstNonEmpty returns the first non-blank string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
