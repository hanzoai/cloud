package git

import (
	"github.com/hanzoai/cloud/internal/environ"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/mint"
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

// mirrorExcludeAgent removes the machine namespace from every mirror fetch.
//
// A mirror is the most powerful ref writer in the forge: `+refs/*:refs/*` with
// --prune force-overwrites every ref from a source the CALLER chose, and deletes
// any ref that source does not have. Pointed at an attacker-chosen upstream it
// therefore does, in one call, everything the ref policy refuses at the push
// endpoint — including replacing the branch under a PR a human is reading, and
// deleting one outright.
//
// The policy cannot be applied to it command-by-command, because the commands
// are whatever the remote turns out to advertise. So the refusal is structural
// instead: a negative refspec takes refs/heads/agent/* out of the fetch's refmap
// entirely, which removes it from --prune's consideration too. The mirror
// cannot write a machine ref and cannot delete one, because it can no longer
// see them. Verified against git 2.43: with this present, an agent branch
// survives a mirror from a source that lacks it, and an agent branch the SOURCE
// carries is not created.
//
// (Negative refspecs are git >= 2.29. An older git ignores nothing and errors on
// the unknown refspec, which fails the mirror closed — the safe direction.)
const mirrorExcludeAgent = "^" + agentRefPrefix + "*"

// mirrorTokenStem is the STEM of the env var — KMS-injected via a KMSSecret sync,
// never hardcoded, never logged. The HOST is the rest of the name, so the
// credential for github.com is GIT_MIRROR_TOKEN_GITHUB_COM (see mirrorCredential).
// Nothing held for a host means an anonymous fetch, which is all a public source
// needs.
//
// A stem and not a whole name, in EVERY direction. It briefly meant both: a stem
// for the inbound fetch and a complete variable for the outbound push and the
// community reader — one constant with two meanings, which is one too many. Every
// credential now resolves through mirrorCredential.
const mirrorTokenStem = "GIT_MIRROR_TOKEN"

// mirrorOutAllowHostsEnv (comma-separated) overrides the OUTBOUND mirror-target
// allowlist; empty ⇒ the default {github.com, gitlab.com}. The local git host is
// never a default target (Red MED-1).
const mirrorOutAllowHostsEnv = "GIT_MIRROR_OUT_ALLOW_HOSTS"

// mirrorAllowPrivateEnv (comma-separated) allowlists hosts that may resolve into
// an otherwise-blocked internal range (loopback/private/link-local) — for tests
// and deliberate internal mirrors. Empty ⇒ all internal targets are refused.
const mirrorAllowPrivateEnv = "GIT_MIRROR_ALLOW_PRIVATE_HOSTS"

// mirrorReq imports an external repository into one of the caller's repos.
type mirrorReq struct {
	// Name is the local repo to mirror into, from the :name path segment. It is
	// CREATED on first use.
	Name string `json:"name"`
	// Source is the http(s) git URL to fetch from. The host is SSRF-guarded, and a
	// credential is sent only if we hold one NAMED FOR that host — so a
	// tenant-supplied URL to anywhere else fetches anonymously.
	Source string `json:"source"`
	// Project is the sub-scope to land the repo in; empty uses the caller's own,
	// exactly as a create would.
	Project string `json:"project"`
}

// mirror imports an external git repository into the caller's repo, provisioning
// it on first use. Fetch is FORCED and covers every ref, so a first call clones
// the source and a repeat call re-syncs it — the endpoint is idempotent by mirror
// semantics. Mirrored bytes are metered exactly like a push, and a push.landed
// event is emitted for the default branch so the code index picks the repo up.
//
// Example: {"name": "widgets", "source": "https://github.com/acme/widgets.git"}
func (o ops) mirror(ctx context.Context, in *mirrorReq) (*repoView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := normalizeName(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	src, err := mirrorSource(in.Source)
	if err != nil {
		return nil, err
	}
	// Project sub-scope: explicit body value wins, else the principal's sub-scope —
	// identical to create so a mirror lands in the same scope a create would.
	project := strings.TrimSpace(in.Project)
	if project == "" {
		project = t.project
	} else if !projectRE.MatchString(project) {
		return nil, zip.ErrBadRequest("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}

	r, err := ensureRepo(o.s, ctx, store, t.org, project, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "ensure repo: %v", err)
	}
	// The org-supplied /mirror endpoint uses the PER-HOST mirror credential (empty
	// gitCred ⇒ mirrorGitEnv falls back to mirrorCredential for the source's host).
	// The GitHub-App path passes a per-org installation token instead (github_import.go).
	if err := o.s.State.storage.mirrorInto(ctx, t.org, project, name, src, gitCred{}); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "mirror fetch: %v", err)
	}
	// The fetch just proved these bytes can be fetched again, so record where
	// from. That is what admits the repo to the bound in reclaim.go: until a
	// source has actually answered, the copy on disk is the only one there is.
	// src carries no userinfo (mirrorSource strips it), so this writes a URL and
	// never a credential.
	if err := store.SetOrigin(ctx, t.org, project, name, src); err != nil {
		// Best-effort, and the failure direction is safe: no origin means pinned,
		// which costs disk rather than data.
		o.s.Log.Warn("git mirror: record origin", "org", t.org, "repo", name, "err", err)
	}
	// Meter the mirrored bytes the same way a push is metered (the ONE storage
	// bound). Best-effort — a metering miss must not fail a landed mirror.
	r.SizeBytes = recordUsage(o.s, context.WithoutCancel(ctx), t.org, project, name)
	// Index-on-import: emit push.landed for the mirrored default branch so /v1/code
	// covers this repo now, exactly as a push would (the same reactor). Origin =
	// source host, so the outbound mirror suppresses the echo. Detached + best-effort.
	emitImportPush(o.s, context.WithoutCancel(ctx), t.org, project, name, src)
	branches, head := refState(ctx, o.s, t.org, project, name)
	view := toView(o.s, r, branches, head)
	return &view, nil
}

// gitCred is a per-fetch/push basic-auth credential presented ONLY via the env-
// only http.extraHeader (never argv, never a log). User is the basic-auth
// username the host expects (x-access-token for GitHub, oauth2 for GitLab); Token
// is the secret (an App installation token / OAuth token). A zero gitCred means
// "no explicit credential" — the per-host env-token path applies.
type gitCred struct {
	User  string
	Token string
}

// mirrorInto fetches every ref/object from srcURL into the repo's on-disk bare
// storage with mirror semantics and points HEAD at the source's default branch
// so a clone-back resolves a default. Idempotent: an up-to-date source is a
// no-op. The pack streams to disk via `git fetch` (bounded memory). cred is the
// per-call credential (a GitHub-App installation token); a zero cred falls back
// to the env token named for the source's host (the org-supplied /mirror path).
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
	// credential.helper= disables any interactive/leaky helper;
	// --no-write-fetch-head keeps a bare mirror clean.
	args := append(packConfigArgs(""),
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"--git-dir="+bareDir, "fetch", "--prune", "--tags", "--no-write-fetch-head",
		srcURL, mirrorRefSpec, mirrorExcludeAgent)
	fetch, err := gitCmd(ctx, env, args...)
	if err != nil {
		return err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	fetch.Stderr = stderr
	if err := withPackSlot(ctx, fetch.Run); err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, sanitizeGitErr(stderr.String()))
	}

	// The source chooses this, so it is checked: HEAD is outside refs/ and the
	// refspec above does not reach it (writer 5 in refpolicy.go).
	if head != "" && checkHeadRef(head) != nil {
		head = ""
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
	for line := range strings.SplitSeq(out.String(), "\n") {
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
	now := time.Now().Unix()
	fresh := Repo{
		ID: mint.ID("repo"), Org: org, Project: project, Name: name,
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
// at an internal service (IMDS, the S3 gateway, cluster svcs). The git subprocess
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
	if slices.ContainsFunc(ips, isInternalIP) {
		return fmt.Errorf("host resolves to a disallowed address")
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

// mirrorAuthHeader returns the base64 "<user>:<token>" basic-auth credential for
// an https source, or "" for http and for a host we hold no credential for. The
// username comes from mirrorBasicUser, so a token held for GitLab is presented
// the one way GitLab accepts. A tenant-supplied URL cannot capture a credential
// (HIGH-1) because there is none to capture: see mirrorCredential.
func mirrorAuthHeader(srcURL string) string {
	u, err := url.Parse(srcURL)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	host := u.Hostname()
	tok := mirrorCredential(host)
	if tok == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(mirrorBasicUser(host) + ":" + tok))
}

// mirrorCredential returns the token minted FOR host, or "" if we hold none.
//
// ONE TOKEN PER HOST, and it is not a refinement — a credential minted for one
// forge is not a credential for another. One env var served every allowlisted
// host, so our GitHub token was offered to git.hanzo.ai: it authenticates
// nothing there, git falls back to prompting, and the fetch dies "could not read
// Username for 'https://git.hanzo.ai'" — which reads as a MISSING credential and
// is really a WRONG one. That cost the whole mirror path: cloud could not import
// a repository from our own forge. It also handed a GitHub token to a different
// trust domain on the way, which is the part that would matter if the two hosts
// were not both ours.
//
// POSSESSION IS THE ALLOWLIST, so the separate host list is gone. It existed to
// stop a tenant-supplied URL from capturing a shared token; with the credential
// named after the host it wants, a URL to any host we hold nothing for finds
// nothing and fetches anonymously. One fact, one place, nothing to drift.
func mirrorCredential(host string) string {
	name := strings.TrimSpace(host)
	if name == "" {
		return ""
	}
	return environ.Or(mirrorTokenStem + "_" + envHost(name), "")
}

// envHost renders a hostname as the tail of an environment-variable name: upper
// case, and every character that cannot appear in one becomes an underscore. So
// git.hanzo.ai names GIT_MIRROR_TOKEN_GIT_HANZO_AI, and adding a host is a
// deployment writing a secret rather than a code change.
func envHost(host string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(host) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
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
	if v := environ.Or(mirrorOutAllowHostsEnv, ""); v != "" {
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
	for h := range strings.SplitSeq(environ.Or(envName, ""), ",") {
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
