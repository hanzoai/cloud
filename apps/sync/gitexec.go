package sync

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// gitexec.go is the ONE seam in this package that shells out to the `git` CLI,
// and it carries the hardening that used to live beside the embedded git store.
//
// The store moved to the forge; the hardening could not move with it, because
// the forge speaks git over the network and something on this side still has to
// run a client against an upstream a TENANT chose. That is the dangerous part
// and it always was: the upstream URL is attacker-influenced, the credential is
// ours, and the process runs in our pod. So every rule the retired object plane
// applied to a mirror fetch applies here, unchanged, and is written here so it
// cannot be lost when that package goes:
//
//   - an ARG SLICE, never a shell string, so a URL can never be interpreted;
//   - a MINIMAL environment (baseGitEnv) inheriting none of the server's
//     secrets, with system/global config and credential prompts disabled;
//   - a credential carried ONLY by env-injected http.extraHeader — never argv
//     (world-readable in /proc), never a URL (written to logs and reflogs);
//   - http/https only, redirects refused, so a source cannot smuggle a
//     file:///ext:: transport or bounce a fetch onto an internal address;
//   - an SSRF guard on every tenant-supplied host;
//   - pack subprocesses bounded in number and in memory, because index-pack
//     holds O(object-count) state in the pod's cgroup and the Go runtime cannot
//     see it;
//   - stderr captured through a capped buffer and redacted before it is read.

// gitBinary resolves the git executable once. A missing git is a hard,
// fail-closed error surfaced to the caller (the runtime image ships git).
var gitBinary = sync.OnceValues(func() (string, error) { return exec.LookPath("git") })

// stderrCap bounds how much of a git subprocess's stderr we retain for an error
// message — a chatty or hostile child can never balloon memory.
const stderrCap = 8 << 10

// packSem bounds how many pack subprocesses (fetch / push) run at once.
// index-pack and pack-objects each hold O(object-count) state plus delta caches
// in the SAME pod cgroup, so N concurrent multi-GB syncs would multiply RAM and
// OOM the pod. GIT_PACK_MAX_CONCURRENCY tunes it (default 2, floor 1). A cheap
// ref list (ls-remote) is NOT gated — only the memory-heavy pack ops are.
var packSem = make(chan struct{}, packConcurrency())

func packConcurrency() int {
	n := 2
	if v := strings.TrimSpace(os.Getenv("GIT_PACK_MAX_CONCURRENCY")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 1 {
			n = p
		}
	}
	return n
}

// withPackSlot runs a pack op while holding a slot. A call cancelled while
// waiting fails fast rather than piling up.
func withPackSlot(ctx context.Context, fn func() error) error {
	select {
	case packSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-packSem }()
	return fn()
}

// packConfigArgs returns the `-c` config flags for a pack subprocess: memory
// bounds on every op (single-threaded delta search with capped window and delta
// cache, and a big-file threshold) so index-pack / pack-objects cannot balloon
// the cgroup during a multi-GB sync. These are main git options, before the
// subcommand.
func packConfigArgs() []string {
	return []string{
		"-c", "pack.threads=1",
		"-c", "pack.windowMemory=64m",
		"-c", "pack.deltaCacheSize=64m",
		"-c", "core.bigFileThreshold=16m",
	}
}

// baseGitEnv is the hardened, MINIMAL environment EVERY git subprocess runs
// under. It inherits NONE of the server's environment — no KMS material, no
// forge token leaking to a child — only PATH plus the isolation knobs:
//
//   - GIT_CONFIG_NOSYSTEM + GIT_CONFIG_GLOBAL=/dev/null: ignore /etc/gitconfig
//     and any ~/.gitconfig — no operator config, no credential helper, no LFS
//     smudge/clean filters.
//   - HOME=/nonexistent: belt-and-suspenders against a stray global config.
//   - GIT_TERMINAL_PROMPT=0: never block on an interactive credential prompt.
//   - GIT_NO_REPLACE_OBJECTS=1: ignore refs/replace remaps.
//   - LC_ALL=C: stable, parseable output — the rejection classifier reads it.
//   - GIT_CONFIG_COUNT pack/mmap bounds: pack generation is proportional to
//     REPO size, not request size, and an unbounded allocation there is one the
//     Go runtime cannot govern (GOMEMLIMIT bounds only the Go heap), landing as
//     a kernel OOM kill of the whole process.
//
// PATH is passed through (it is not a secret) so git can find its helper
// executables (git-remote-https); the binary itself is resolved to an absolute
// path by gitBinary, so a child never re-resolves "git" via PATH.
func baseGitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=/nonexistent",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
		"GIT_CONFIG_COUNT=5",
		"GIT_CONFIG_KEY_0=pack.windowMemory", "GIT_CONFIG_VALUE_0=64m",
		"GIT_CONFIG_KEY_1=pack.threads", "GIT_CONFIG_VALUE_1=2",
		"GIT_CONFIG_KEY_2=core.packedGitLimit", "GIT_CONFIG_VALUE_2=256m",
		"GIT_CONFIG_KEY_3=core.packedGitWindowSize", "GIT_CONFIG_VALUE_3=32m",
		"GIT_CONFIG_KEY_4=pack.deltaCacheSize", "GIT_CONFIG_VALUE_4=64m",
	}
}

// gitCmd builds a hardened git subprocess: `git <args...>` under baseGitEnv +
// extraEnv. The ONE constructor, so the env hardening and the arg-slice
// discipline live in exactly one place.
func gitCmd(ctx context.Context, extraEnv []string, args ...string) (*exec.Cmd, error) {
	bin, err := gitBinary()
	if err != nil {
		return nil, fmt.Errorf("git binary not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(baseGitEnv(), extraEnv...)
	return cmd, nil
}

// ── the credential ───────────────────────────────────────────────────────────

// gitCred is a per-call basic-auth credential presented ONLY via the env-only
// http.extraHeader. User is the basic-auth username the host expects
// (x-access-token for GitHub and for the forge, oauth2 for GitLab); Token is the
// secret. A zero gitCred means "no explicit credential" — the per-host env token
// applies, and where there is none the call is anonymous, which is all a public
// source needs.
type gitCred struct {
	User  string
	Token string
}

// mirrorTokenStem is the STEM of the per-host credential env var — KMS-injected
// via a KMSSecret sync, never hardcoded, never logged. The HOST is the rest of
// the name, so the credential for github.com is GIT_MIRROR_TOKEN_GITHUB_COM.
//
// ONE TOKEN PER HOST, and it is not a refinement: a credential minted for one
// forge authenticates nothing at another, and offering it there hands a
// credential to a different trust domain on the way to failing.
const mirrorTokenStem = "GIT_MIRROR_TOKEN"

// mirrorOutAllowHostsEnv (comma-separated) overrides the OUTBOUND target
// allowlist; empty ⇒ {github.com, gitlab.com}.
const mirrorOutAllowHostsEnv = "GIT_MIRROR_OUT_ALLOW_HOSTS"

// mirrorAllowPrivateEnv (comma-separated) allowlists hosts that may resolve into
// an otherwise-blocked internal range — for tests and deliberate internal
// sources. Empty ⇒ every internal target is refused.
const mirrorAllowPrivateEnv = "GIT_MIRROR_ALLOW_PRIVATE_HOSTS"

// mirrorGitEnv builds the git subprocess environment for reaching remoteURL:
// GIT_ALLOW_PROTOCOL restricts outbound to http/https; http.followRedirects=
// false stops a redirect from carrying the token to another host or bouncing
// the transfer onto an internal address; and the credential rides an env-only
// git-config http.extraHeader — never argv, never a log.
//
// extra is more "key=value" git config for the one call, which is how the push
// adds its low-speed abort without a second environment builder.
func mirrorGitEnv(remoteURL string, cred gitCred, extra ...string) []string {
	env := []string{"GIT_ALLOW_PROTOCOL=http:https"}
	cfg := append([]string{"http.followRedirects=false"}, extra...)
	if hdr := credAuthHeader(remoteURL, cred); hdr != "" {
		cfg = append(cfg, "http.extraHeader=Authorization: Basic "+hdr)
	}
	return append(env, gitConfigEnv(cfg...)...)
}

// credAuthHeader resolves the basic-auth credential attached to one call. An
// EXPLICIT per-call cred wins — but only over https, so a token can never ride
// an http URL — except on loopback, which is the local forge a test serves and
// is not a network anyone else is on. With no explicit cred it falls back to the
// token NAMED FOR THAT HOST, so a tenant-supplied URL to a host we hold nothing
// for finds nothing and proceeds anonymously.
func credAuthHeader(remoteURL string, cred gitCred) string {
	if cred.Token != "" {
		if !confidential(remoteURL) {
			return "" // never send a token in clear to somewhere it could be read
		}
		return base64.StdEncoding.EncodeToString([]byte(cred.User + ":" + cred.Token))
	}
	if !confidential(remoteURL) {
		return ""
	}
	tok := mirrorCredential(hostOf(remoteURL))
	if tok == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
}

// confidential reports whether a credential may be sent to this URL: https
// anywhere, or plain http to LOOPBACK ONLY.
//
// The loopback exemption is the same one [forge.New] makes and for the same
// reason — a test's forge, and a local one, have no certificate — and it is
// bounded to an address no other machine can reach.
func confidential(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
}

// gitConfigEnv encodes "key=value" pairs as git's GIT_CONFIG_COUNT/KEY_n/VALUE_n
// environment, so per-invocation config (credentials, redirect policy) passes
// WITHOUT argv and without a file — the ONE way this package injects git config.
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

// mirrorCredential returns the token held FOR host, or "" if we hold none.
// POSSESSION IS THE ALLOWLIST: with the credential named after the host it is
// for, a URL to any other host finds nothing.
func mirrorCredential(host string) string {
	name := strings.TrimSpace(host)
	if name == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(mirrorTokenStem + "_" + envHost(name)))
}

// envHost renders a hostname as the tail of an environment-variable name: upper
// case, and every character that cannot appear in one becomes an underscore. So
// github.com names GIT_MIRROR_TOKEN_GITHUB_COM, and adding a host is a
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

// mirrorBasicUser maps a host to the basic-auth username its token is presented
// with: GitLab takes "oauth2", GitHub and the forge take "x-access-token".
func mirrorBasicUser(host string) string {
	if strings.ToLower(host) == "gitlab.com" {
		return "oauth2"
	}
	return "x-access-token"
}

// ── the two URL gates ────────────────────────────────────────────────────────

// mirrorSource validates + hardens a tenant-supplied UPSTREAM URL. It must be a
// well-formed http/https URL with a host; any embedded userinfo is STRIPPED
// (credentials go via env only, never a ps-visible argv); and the host is
// SSRF-guarded so a tenant cannot point our fetch at an internal service (IMDS,
// cluster services). The subprocess additionally runs under
// GIT_ALLOW_PROTOCOL=http:https + http.followRedirects=false, so a source can
// neither smuggle another transport nor bounce the fetch to an internal host.
func mirrorSource(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("source is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("source must be an http(s) git URL")
	}
	u.User = nil
	if err := mirrorGuardHost(u.Hostname()); err != nil {
		// Generic — never confirm whether a host is internal (a probe oracle).
		return "", fmt.Errorf("source host is not permitted")
	}
	return u.String(), nil
}

// validateMirrorTarget validates + canonicalizes an OUTBOUND target: https, no
// userinfo, and on the outbound allowlist. Stricter than a source, because a
// host we will PUSH tenant code to is a stricter question than one we read from
// — and because the local forge must never be a target (that would be this
// process force-feeding its own canonical store through the tenant-facing door).
func validateMirrorTarget(raw string) (canonical, host string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("url is required")
	}
	u, perr := url.Parse(raw)
	if perr != nil || u.Host == "" || u.Scheme != "https" {
		return "", "", fmt.Errorf("url must be an https git URL")
	}
	u.User = nil
	h := strings.ToLower(u.Hostname())
	if !mirrorOutHostAllowed(h) {
		return "", "", fmt.Errorf("host is not an allowed mirror target")
	}
	return u.String(), h, nil
}

// mirrorOutHostAllowed reports whether host may RECEIVE a push (and therefore
// the outbound credential). The default set is {github.com, gitlab.com} and
// deliberately excludes the forge's own host. GIT_MIRROR_OUT_ALLOW_HOSTS
// overrides for a deployment that mirrors elsewhere.
func mirrorOutHostAllowed(host string) bool {
	if v := strings.TrimSpace(os.Getenv(mirrorOutAllowHostsEnv)); v != "" {
		return hostInList(host, mirrorOutAllowHostsEnv)
	}
	host = strings.ToLower(host)
	return host == "github.com" || host == "gitlab.com"
}

// mirrorGuardHost is the SSRF gate: it resolves host and rejects any address in
// a loopback / private / link-local / metadata / unspecified / multicast range,
// so a tenant-supplied source can only reach public hosts. Specific internal
// hosts are allowlisted through GIT_MIRROR_ALLOW_PRIVATE_HOSTS (tests, or a
// deliberate internal source). git additionally runs with followRedirects=false
// so a public host cannot 302 the fetch onto an internal address past this.
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

// isInternalIP reports whether ip is in a range a tenant-supplied source must
// never reach: loopback, RFC1918/ULA private, link-local (including the
// 169.254.169.254 metadata endpoint), unspecified, or multicast.
func isInternalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast()
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

// ── what a subprocess says back ──────────────────────────────────────────────

// credURLRE matches a "scheme://userinfo@" prefix so any credential embedded in
// a URL is redacted out of an error before it reaches a client or a log.
var credURLRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*)://[^/@\s]*@`)

// sanitizeGitErr trims a git subprocess's stderr and redacts any credential
// embedded in a URL. Every read of a subprocess's output goes through it.
func sanitizeGitErr(msg string) string {
	return strings.TrimSpace(credURLRE.ReplaceAllString(msg, "$1://***@"))
}

// cappedBuffer captures at most cap bytes of a subprocess's stderr — bounded, so
// a chatty or hostile child can never balloon memory, while still surfacing the
// head of the error. Write always reports a full write so git never observes a
// short write on its stderr.
type cappedBuffer struct {
	buf bytes.Buffer
	cap int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if rem := b.cap - b.buf.Len(); rem > 0 {
		if len(p) > rem {
			b.buf.Write(p[:rem])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
