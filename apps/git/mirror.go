package git

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/outbound"
)

// The git plumbing the outbound mirror (mirror_out.go) and the move to the forge share.
// A linked repository is filled from its host by linkstore.go.

// fillRefSpecs are what the store holds of a linked repository's host: every branch and every tag,
// forced, so the cache is the host's refs exactly. Not refs/pull or any other
// namespace a host keeps for itself. HEAD (not under refs/) is set separately.
var fillRefSpecs = []string{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"}

// mirrorOutAllowHostsEnv (comma-separated) overrides the OUTBOUND mirror-target
// allowlist; empty ⇒ the default {github.com, gitlab.com}. The local git host is
// never a default target (Red MED-1).
const mirrorOutAllowHostsEnv = "GIT_MIRROR_OUT_ALLOW_HOSTS"

// gitCred is a per-fetch/push basic-auth credential presented ONLY via the env-
// only http.extraHeader (never argv, never a log). User is the basic-auth
// username the host expects (x-access-token for GitHub, oauth2 for GitLab); Token
// is the secret (an App installation token / OAuth token). A zero gitCred is
// no credential at all.
type gitCred struct {
	User  string
	Token string
}

// admit is the SSRF gate: outbound.Host resolves a source host and refuses it if
// any answer is not public, so a mirror can only reach public hosts. git
// additionally runs with followRedirects=false so a public host can't 302 the
// fetch onto an internal address past this check. A variable only so this
// package's tests can admit their loopback stubs; nothing else assigns it.
var admit = outbound.Host

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

// mirrorOutHostAllowed reports whether host is a permitted OUTBOUND mirror TARGET
// (and may therefore receive the outbound credential). The default set is
// {github.com, gitlab.com} ONLY and DELIBERATELY EXCLUDES the local git host
// (git.hanzo.ai): admitting it would let a tenant make the server force-push with
// the shared token at an arbitrary internal path — an internal SSRF +
// privileged-credential presentation (Red MED-1). GIT_MIRROR_OUT_ALLOW_HOSTS
// overrides for a deployment that mirrors to additional external hosts; the local
// host must never be added.
func mirrorOutHostAllowed(host string) bool {
	if v := env.Or(mirrorOutAllowHostsEnv, ""); v != "" {
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
	for h := range strings.SplitSeq(env.Or(envName, ""), ",") {
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
