package git

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Mirror-in: import an EXTERNAL git repository into the embedded server so a repo
// that lives on GitHub (e.g. github.com/hanzoai/cloud) becomes a first-class
// Hanzo-hosted repo at <domain>/v1/git/<org>/<name>.git. Once mirrored, a push
// there fires git-push-to-deploy exactly like any native repo (smart_http.go),
// so cloud can host — and deploy — its OWN source without depending on GitHub.
//
// The fetch shells out to the streaming `git fetch` CLI against the on-disk bare
// repo (index-pack streams the pack to disk), so mirroring a multi-GB repo stays
// bounded in memory — go-git's in-process FetchContext buffered the whole pack in
// RAM and OOM-killed the pod. Idempotent by mirror semantics (+refs/*:refs/*,
// forced): re-mirroring force-updates refs. Org-scoped exactly like create — the
// org (X-Org-Id) owns the mirror. recordUsage meters the mirrored bytes against
// the org's commerce quota, the same bound a push is measured by.

// mirrorRefSpec is git's mirror refspec: force every source ref onto the same
// destination ref. HEAD (not under refs/) is set separately by mirrorInto.
const mirrorRefSpec = "+refs/*:refs/*"

// mirrorEnvToken names the env var — KMS-injected via a KMSSecret sync, never
// hardcoded, never logged — holding the credential for mirroring a PRIVATE
// https source. Empty means an anonymous fetch (public sources need none).
const mirrorEnvToken = "GIT_MIRROR_TOKEN"

type mirrorReq struct {
	Source  string `json:"source"`
	Project string `json:"project"`
}

// mirror imports body.Source into the org's repo at :name. It provisions the
// repo on first use (idempotent, race-safe) then force-fetches every ref, so a
// first call clones the source and a repeat call syncs it.
func mirror(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := normalizeName(c.Param("name"))
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	var body mirrorReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	src, err := mirrorSource(body.Source)
	if err != nil {
		return err
	}
	// Project sub-scope: explicit body value wins, else the header sub-scope —
	// identical to create so a mirror lands in the same scope a create would.
	project := strings.TrimSpace(body.Project)
	if project == "" {
		project = projectScope(c)
	} else if !projectRE.MatchString(project) {
		return zip.ErrBadRequest("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}

	r, err := ensureRepo(s, c.Context(), store, org, project, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "ensure repo: %v", err)
	}
	if err := s.State.storage.mirrorInto(c.Context(), org, project, name, src); err != nil {
		return zip.Errorf(http.StatusBadGateway, "mirror fetch: %v", err)
	}
	// Meter the mirrored bytes the same way a push is metered (the ONE storage
	// bound). Best-effort — a metering miss must not fail a landed mirror.
	r.SizeBytes = recordUsage(s, context.WithoutCancel(c.Context()), org, project, name)
	branches, head := refState(s, org, project, name)
	return c.JSON(http.StatusOK, toView(s, r, branches, head))
}

// mirrorInto fetches every ref/object from srcURL into the repo's on-disk bare
// storage with mirror semantics and points HEAD at the source's default branch
// so a clone-back resolves a default. Idempotent: an up-to-date source is a
// no-op. The pack streams to disk via `git fetch` (bounded memory).
func (s *storage) mirrorInto(ctx context.Context, org, project, name, srcURL string) error {
	bareDir := s.absRepoPath(org, project, name)
	env := mirrorGitEnv(srcURL)

	// Discover the source default branch (HEAD symref) — bounded ls-remote — so a
	// clone of the mirror resolves the same default the source has.
	head, err := remoteHead(ctx, srcURL, env)
	if err != nil {
		return fmt.Errorf("list source: %w", err)
	}

	// Fetch all refs + tags with mirror semantics, streaming the pack to disk.
	// protocol.version=2 = cheaper negotiation on large repos; credential.helper=
	// disables any interactive/leaky helper (gitea). --no-write-fetch-head keeps
	// a bare mirror clean.
	fetch, err := gitCmd(ctx, env,
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"--git-dir="+bareDir, "fetch", "--prune", "--tags", "--no-write-fetch-head",
		srcURL, mirrorRefSpec)
	if err != nil {
		return err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	fetch.Stderr = stderr
	if err := fetch.Run(); err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, sanitizeGitErr(stderr.String()))
	}

	if head != "" {
		set, err := gitCmd(ctx, env, "--git-dir="+bareDir, "symbolic-ref", "HEAD", head)
		if err != nil {
			return err
		}
		serr := &cappedBuffer{cap: stderrCap}
		set.Stderr = serr
		if err := set.Run(); err != nil {
			return fmt.Errorf("set HEAD: %w: %s", err, sanitizeGitErr(serr.String()))
		}
	}
	return nil
}

// remoteHead resolves the source's default branch via
// `git ls-remote --symref <src> HEAD`, returning e.g. "refs/heads/main" or ""
// when the server advertises no HEAD symref (an empty repo, or a server that
// doesn't send one — HEAD is then left as the bare repo's default). Output is
// tiny + bounded (one symref line for HEAD).
func remoteHead(ctx context.Context, srcURL string, env []string) (string, error) {
	cmd, err := gitCmd(ctx, env,
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"ls-remote", "--symref", srcURL, "HEAD")
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ls-remote: %w: %s", err, sanitizeGitErr(stderr.String()))
	}
	// "ref: refs/heads/main\tHEAD"
	for _, line := range strings.Split(out.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: "); ok {
			if tab := strings.IndexByte(rest, '\t'); tab > 0 {
				return strings.TrimSpace(rest[:tab]), nil
			}
		}
	}
	return "", nil
}

// ensureRepo returns the org's repo, provisioning it (metadata row + empty bare
// storage) on first use. Idempotent and race-safe: a concurrent create is
// reconciled by reloading the canonical row, never surfaced as a conflict — a
// mirror must be repeatable.
func ensureRepo(s *cloud.Service[state], ctx context.Context, store *Store, org, project, name string) (Repo, error) {
	r, err := store.Get(ctx, org, project, name)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, errNotFound) {
		return Repo{}, err
	}
	id, err := genID("repo")
	if err != nil {
		return Repo{}, err
	}
	now := time.Now().Unix()
	fresh := Repo{
		ID: id, Org: org, Project: project, Name: name,
		DefaultBranch: defaultBranchName, CreatedAt: now, UpdatedAt: now,
	}
	if err := provision(s, ctx, store, fresh); err != nil && !errors.Is(err, errConflict) {
		return Repo{}, err
	}
	return store.Get(ctx, org, project, name)
}

// mirrorSource validates the org-supplied source URL: a well-formed http/https
// git URL with a host. This is the boundary check for the one place a
// org-supplied address enters the server's outbound network path; the git
// subprocess additionally runs under GIT_ALLOW_PROTOCOL=http:https so a source
// can never smuggle a file:// / ext:: protocol past this check.
func mirrorSource(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", zip.ErrBadRequest("source is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", zip.ErrBadRequest("source must be an http(s) git URL")
	}
	return u.String(), nil
}

// mirrorGitEnv builds the git subprocess environment for a mirror fetch:
// GIT_ALLOW_PROTOCOL restricts outbound to http/https (blocks file://, ext::,
// etc.), and a PRIVATE https source's credential is injected via env-only
// git-config http.extraHeader (GitHub's token-as-basic-auth form) — NEVER on
// argv (ps-visible) or in logs. A public/http source stays anonymous.
func mirrorGitEnv(srcURL string) []string {
	env := []string{"GIT_ALLOW_PROTOCOL=http:https"}
	if hdr := mirrorAuthHeader(srcURL); hdr != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+hdr,
		)
	}
	return env
}

// mirrorAuthHeader returns the base64 "x-access-token:<token>" basic-auth
// credential for an https source from the KMS-injected env token, or "" for http
// (loopback) / when no token is set — so public mirrors stay anonymous and a
// token is never sent in cleartext.
func mirrorAuthHeader(srcURL string) string {
	tok := strings.TrimSpace(os.Getenv(mirrorEnvToken))
	if tok == "" {
		return ""
	}
	if u, err := url.Parse(srcURL); err != nil || u.Scheme != "https" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
}

// credURLRE matches a "scheme://userinfo@" prefix so any credential embedded in
// a source URL is redacted out of an error surfaced to the client or the log.
var credURLRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*)://[^/@\s]*@`)

// sanitizeGitErr trims a git subprocess's stderr and redacts any credential
// embedded in a URL before it reaches a client error or a log line.
func sanitizeGitErr(msg string) string {
	return strings.TrimSpace(credURLRE.ReplaceAllString(msg, "$1://***@"))
}
