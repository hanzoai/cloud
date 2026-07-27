package git

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
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

// mirrorAllowHostsEnv (comma-separated) is the allowlist of hosts the mirror
// credential (GIT_MIRROR_TOKEN) may be sent to; empty ⇒ the default set
// {github.com, git.hanzo.ai}. Any other source fetches anonymously so a
// tenant-supplied URL can never capture the shared token.
const mirrorAllowHostsEnv = "GIT_MIRROR_ALLOW_HOSTS"

// mirrorOutAllowHostsEnv (comma-separated) overrides the OUTBOUND mirror-target
// allowlist; empty ⇒ the default {github.com, gitlab.com}. The local git host is
// never a default target (Red MED-1).
const mirrorOutAllowHostsEnv = "GIT_MIRROR_OUT_ALLOW_HOSTS"

// mirrorAllowPrivateEnv (comma-separated) allowlists hosts that may resolve into
// an otherwise-blocked internal range (loopback/private/link-local) — for tests
// and deliberate internal mirrors. Empty ⇒ all internal targets are refused.
const mirrorAllowPrivateEnv = "GIT_MIRROR_ALLOW_PRIVATE_HOSTS"

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
	// The org-supplied /mirror endpoint uses the SHARED, host-allowlisted mirror
	// credential (empty gitCred ⇒ mirrorGitEnv falls back to the env-token path).
	// The GitHub-App path passes a per-org installation token instead (github_import.go).
	if err := s.State.storage.mirrorInto(c.Context(), org, project, name, src, gitCred{}); err != nil {
		return zip.Errorf(http.StatusBadGateway, "mirror fetch: %v", err)
	}
	// Meter the mirrored bytes the same way a push is metered (the ONE storage
	// bound). Best-effort — a metering miss must not fail a landed mirror.
	r.SizeBytes = recordUsage(s, context.WithoutCancel(c.Context()), org, project, name)
	// Index-on-import: emit push.landed for the mirrored default branch so /v1/code
	// covers this repo now, exactly as a push would (the same reactor). Origin =
	// source host, so the outbound mirror suppresses the echo. Detached + best-effort.
	emitImportPush(s, context.WithoutCancel(c.Context()), org, project, name, src)
	branches, head := refState(c.Context(), s, org, project, name)
	return c.JSON(http.StatusOK, toView(s, r, branches, head))
}

// gitCred is a per-fetch/push basic-auth credential presented ONLY via the env-
// only http.extraHeader (never argv, never a log). User is the basic-auth
// username the host expects (x-access-token for GitHub, oauth2 for GitLab); Token
// is the secret (an App installation token / OAuth token). A zero gitCred means
// "no explicit credential" — the shared, host-allowlisted env-token path applies.
type gitCred struct {
	User  string
	Token string
}

// mirrorInto fetches every ref/object from srcURL into the repo's on-disk bare
// storage with mirror semantics and points HEAD at the source's default branch
// so a clone-back resolves a default. Idempotent: an up-to-date source is a
// no-op. The pack streams to disk via `git fetch` (bounded memory). cred is the
// per-call credential (a GitHub-App installation token); a zero cred falls back
// to the shared, host-allowlisted env token (the org-supplied /mirror path).
func (s *storage) mirrorInto(ctx context.Context, org, project, name, srcURL string, cred gitCred) error {
	bareDir := s.absRepoPath(org, project, name)
	env := mirrorGitEnv(srcURL, cred)

	// Discover the source default branch (HEAD symref) — bounded ls-remote — so a
	// clone of the mirror resolves the same default the source has.
	head, err := remoteHead(ctx, srcURL, env)
	if err != nil {
		return fmt.Errorf("list source: %w", err)
	}

	// Fetch all refs + tags with mirror semantics, streaming the pack to disk
	// under a pack-concurrency slot + memory bounds (packConfigArgs) so a multi-GB
	// mirror can't OOM the pod. protocol.version=2 = cheaper negotiation;
	// credential.helper= disables any interactive/leaky helper (gitea);
	// --no-write-fetch-head keeps a bare mirror clean.
	args := append(packConfigArgs(""),
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"--git-dir="+bareDir, "fetch", "--prune", "--tags", "--no-write-fetch-head",
		srcURL, mirrorRefSpec)
	fetch, err := gitCmd(ctx, env, args...)
	if err != nil {
		return err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	fetch.Stderr = stderr
	if err := withPackSlot(ctx, fetch.Run); err != nil {
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

// mirrorSource validates + hardens the org-supplied source URL. It must be a
// well-formed http/https URL with a host; any embedded userinfo is STRIPPED
// (credentials go via env only, never a ps-visible argv — LOW-7); and the host
// is SSRF-guarded (mirrorGuardHost) so a tenant can't point the server's fetch
// at an internal service (IMDS, SeaweedFS, cluster svcs). The git subprocess
// additionally runs under GIT_ALLOW_PROTOCOL=http:https + http.followRedirects=
// false so a source can never smuggle a file:///ext:: protocol or bounce the
// fetch to an internal host via a redirect.
func mirrorSource(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", zip.ErrBadRequest("source is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", zip.ErrBadRequest("source must be an http(s) git URL")
	}
	u.User = nil // credentials only via env (GIT_MIRROR_TOKEN), never argv
	if err := mirrorGuardHost(u.Hostname()); err != nil {
		// Generic message — never confirm whether a host is internal (probe oracle).
		return "", zip.ErrBadRequest("source host is not permitted")
	}
	return u.String(), nil
}

// mirrorGuardHost is the SSRF gate: it resolves host and rejects any address in
// a loopback / private / link-local / metadata (IMDS) / unspecified / multicast
// range, so a mirror can only reach public hosts. Specific internal hosts can be
// allowlisted via GIT_MIRROR_ALLOW_PRIVATE_HOSTS (tests, or a deliberate
// internal mirror). git additionally runs with followRedirects=false so a public
// host can't 302 the fetch onto an internal address past this check.
func mirrorGuardHost(host string) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if hostInList(host, mirrorAllowPrivateEnv) {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve host: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no address for host")
	}
	for _, ip := range ips {
		if isInternalIP(ip) {
			return fmt.Errorf("host resolves to a disallowed address")
		}
	}
	return nil
}

// isInternalIP reports whether ip is in a range a tenant-supplied mirror must
// never reach (SSRF): loopback, RFC1918/ULA private, link-local (incl. the
// 169.254.169.254 cloud metadata endpoint), unspecified, or multicast.
func isInternalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast()
}

// mirrorGitEnv builds the git subprocess environment for a mirror fetch:
// GIT_ALLOW_PROTOCOL restricts outbound to http/https; http.followRedirects=
// false stops a redirect from carrying the token to another host or bouncing the
// fetch to an internal address; and a PRIVATE https source whose host is on the
// credential allowlist gets the token via env-only git-config http.extraHeader
// (GitHub's token-as-basic-auth form) — NEVER on argv or in logs. Everything
// else fetches anonymously.
func mirrorGitEnv(srcURL string, cred gitCred) []string {
	env := []string{"GIT_ALLOW_PROTOCOL=http:https"}
	cfg := []string{"http.followRedirects=false"}
	if hdr := credAuthHeader(srcURL, cred); hdr != "" {
		cfg = append(cfg, "http.extraHeader=Authorization: Basic "+hdr)
	}
	return append(env, gitConfigEnv(cfg...)...)
}

// credAuthHeader resolves the basic-auth credential attached to a fetch. An
// EXPLICIT per-call cred (a GitHub-App installation token) wins — but only over
// https, so a token can never ride an http URL, and http.followRedirects=false
// (set by mirrorGitEnv) stops a redirect from carrying it to another host. With no
// explicit cred it falls back to mirrorAuthHeader — the shared env token, gated to
// the host allowlist so a tenant-supplied /mirror source can never capture it.
func credAuthHeader(srcURL string, cred gitCred) string {
	if cred.Token != "" {
		u, err := url.Parse(srcURL)
		if err != nil || u.Scheme != "https" {
			return "" // never send a token over a non-https source
		}
		return base64.StdEncoding.EncodeToString([]byte(cred.User + ":" + cred.Token))
	}
	return mirrorAuthHeader(srcURL)
}

// gitConfigEnv encodes "key=value" pairs as the git GIT_CONFIG_COUNT/KEY_n/
// VALUE_n environment so per-invocation config (credentials, redirect policy) is
// passed WITHOUT argv or a file — the ONE way this package injects git config.
func gitConfigEnv(kv ...string) []string {
	env := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(kv))}
	for i, pair := range kv {
		k, v, _ := strings.Cut(pair, "=")
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, k),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, v),
		)
	}
	return env
}

// mirrorAuthHeader returns the base64 "x-access-token:<token>" basic-auth
// credential for an https source ONLY when the source host is on the credential
// allowlist (GIT_MIRROR_ALLOW_HOSTS, default {github.com, git.hanzo.ai}). A
// tenant-supplied URL to any other host fetches anonymously, so the shared
// GIT_MIRROR_TOKEN can never be captured by an attacker-controlled source
// (HIGH-1). Returns "" for http, no token, or a non-allowlisted host.
func mirrorAuthHeader(srcURL string) string {
	tok := strings.TrimSpace(os.Getenv(mirrorEnvToken))
	if tok == "" {
		return ""
	}
	u, err := url.Parse(srcURL)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	if !mirrorHostAllowed(u.Hostname()) {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
}

// mirrorHostAllowed reports whether host may receive the INBOUND mirror credential
// (GIT_MIRROR_TOKEN) when fetching a private source. GIT_MIRROR_ALLOW_HOSTS
// (comma-separated) overrides the default {github.com, git.hanzo.ai}. This gate is
// DISTINCT from the outbound-target gate (mirrorOutHostAllowed): a host we trust to
// FETCH from is not automatically a host we will PUSH tenant code to.
func mirrorHostAllowed(host string) bool {
	if v := strings.TrimSpace(os.Getenv(mirrorAllowHostsEnv)); v != "" {
		return hostInList(host, mirrorAllowHostsEnv)
	}
	host = strings.ToLower(host)
	return host == "github.com" || host == "git.hanzo.ai"
}

// mirrorOutHostAllowed reports whether host is a permitted OUTBOUND mirror TARGET
// (and may therefore receive the outbound credential). The default set is
// {github.com, gitlab.com} ONLY and DELIBERATELY EXCLUDES the local git host
// (git.hanzo.ai): admitting it would let a tenant make the server force-push with
// the shared token at an arbitrary internal path — an internal SSRF +
// privileged-credential presentation (Red MED-1). GIT_MIRROR_OUT_ALLOW_HOSTS
// overrides for a deployment that mirrors to additional external hosts; the local
// host must never be added.
func mirrorOutHostAllowed(host string) bool {
	if v := strings.TrimSpace(os.Getenv(mirrorOutAllowHostsEnv)); v != "" {
		return hostInList(host, mirrorOutAllowHostsEnv)
	}
	host = strings.ToLower(host)
	return host == "github.com" || host == "gitlab.com"
}

// mirrorBasicUser maps a downstream host to the basic-auth username its token is
// presented with over http.extraHeader: GitHub takes "x-access-token", GitLab
// takes "oauth2". The token itself is the password, injected env-only (never
// argv/logs).
func mirrorBasicUser(host string) string {
	if strings.ToLower(host) == "gitlab.com" {
		return "oauth2"
	}
	return "x-access-token"
}

// hostInList reports whether host (case-insensitive) is a member of the
// comma-separated allowlist held in env var envName.
func hostInList(host, envName string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, h := range strings.Split(os.Getenv(envName), ",") {
		if strings.ToLower(strings.TrimSpace(h)) == host {
			return true
		}
	}
	return false
}

// credURLRE matches a "scheme://userinfo@" prefix so any credential embedded in
// a source URL is redacted out of an error surfaced to the client or the log.
var credURLRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*)://[^/@\s]*@`)

// sanitizeGitErr trims a git subprocess's stderr and redacts any credential
// embedded in a URL before it reaches a client error or a log line.
func sanitizeGitErr(msg string) string {
	return strings.TrimSpace(credURLRE.ReplaceAllString(msg, "$1://***@"))
}
