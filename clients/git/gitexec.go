package git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	luxlog "github.com/luxfi/log"
)

// gitexec.go is the ONE seam that shells out to the streaming `git` CLI for the
// heavy object-plane operations — clone/fetch serve (upload-pack), push receive
// (receive-pack), and mirror-in (fetch). go-git's pure-Go server transport
// buffered whole packs in RAM (a 3 GB clone = 3 GB), which OOM-killed the 1 Gi
// cloud pod when large repos were mirrored. The git CLI (index-pack /
// upload-pack / receive-pack) streams packs to and from disk, so memory stays
// bounded by an OS pipe buffer regardless of repo size — the way real git hosts
// (gitea, GitLab) serve smart-HTTP. The patterns here are ported from gitea
// v1.24.7 (routers/web/repo/githttp.go, modules/git/command.go, cmd/serv.go).
//
// Every git invocation:
//   - takes an ARG SLICE (never a shell string) so a repo path or source URL can
//     never be interpreted by a shell;
//   - operates on the resolved ABSOLUTE bare-repo path the handler already
//     org-validated (tenant isolation) — never a client-controlled path;
//   - runs under a hardened, MINIMAL environment (baseGitEnv) that inherits none
//     of the server's secrets and disables system/global config + credential
//     prompts;
//   - carries any source credential ONLY via env-injected git-config
//     http.extraHeader (mirror.go), never argv or logs.

// gitBinary resolves the git executable once. A missing git is a hard,
// fail-closed error surfaced to the caller (the runtime image ships git).
var gitBinary = sync.OnceValues(func() (string, error) { return exec.LookPath("git") })

// stderrCap bounds how much of a git subprocess's stderr we retain for an error
// message — a chatty or hostile child can never balloon memory.
const stderrCap = 8 << 10

// packSubcommand maps the smart-HTTP service name (git-upload-pack /
// git-receive-pack) to the git subcommand (upload-pack / receive-pack). Only
// these two are ever spawned; the caller validates the service against the
// svcUploadPack / svcReceivePack allowlist first.
func packSubcommand(service string) string { return strings.TrimPrefix(service, "git-") }

// packetWrite pkt-line-encodes str: a 4-hex-digit length prefix (counting the 4
// prefix bytes) then the payload. Ported verbatim from gitea
// (routers/web/repo/githttp.go) — the smart-HTTP "# service=…\n" info/refs
// framing contract the git client requires before the advertisement body.
func packetWrite(str string) []byte {
	s := strconv.FormatInt(int64(len(str)+4), 16)
	if len(s)%4 != 0 {
		s = strings.Repeat("0", 4-len(s)%4) + s
	}
	return []byte(s + str)
}

// safeGitProtocolHeader validates a client Git-Protocol header before it is
// forwarded into the subprocess env as GIT_PROTOCOL (protocol v2 = far cheaper
// negotiation + partial clone on large repos). Ported from gitea: one or more
// alnum key=value pairs separated by colons — so a client can never smuggle
// extra env or arguments through the header.
var safeGitProtocolHeader = regexp.MustCompile(`^[0-9a-zA-Z]+=[0-9a-zA-Z]+(:[0-9a-zA-Z]+=[0-9a-zA-Z]+)*$`)

// gitProtocolEnv returns the GIT_PROTOCOL env pair when the client advertised a
// well-formed protocol version, else nil. The single validation point for the
// protocol passthrough shared by smart-HTTP (Git-Protocol header) and SSH (the
// GIT_PROTOCOL exec env).
func gitProtocolEnv(protocol string) []string {
	if protocol != "" && safeGitProtocolHeader.MatchString(protocol) {
		return []string{"GIT_PROTOCOL=" + protocol}
	}
	return nil
}

// baseGitEnv is the hardened, MINIMAL environment EVERY git subprocess runs
// under. It inherits NONE of the server's environment (no KMS keys, no
// GIT_MIRROR_TOKEN leaking to a child) — only PATH plus the isolation knobs
// gitea's modules/git uses:
//   - GIT_CONFIG_NOSYSTEM + GIT_CONFIG_GLOBAL=/dev/null: ignore /etc/gitconfig
//     and any ~/.gitconfig — no operator config, no credential helper, no LFS
//     smudge/clean filters (git >= 2.32, satisfied by the alpine 3.22 git).
//   - HOME=/nonexistent: belt-and-suspenders against a stray global config.
//   - GIT_TERMINAL_PROMPT=0: never block on an interactive credential prompt.
//   - GIT_NO_REPLACE_OBJECTS=1: ignore refs/replace remaps (gitea).
//   - LC_ALL=C: stable, parseable output.
//
// PATH is passed through (not a secret) so git can find its helper executables
// (git-remote-https for mirror fetch); the binary itself is resolved to an
// absolute path by gitBinary, so a child never re-resolves "git" via PATH.
func baseGitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=/nonexistent",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
	}
}

// gitCmd builds a hardened git subprocess: `git <args...>` under
// baseGitEnv + extraEnv. The ONE constructor — mirror fetch, serve
// advertise/rpc, SSH pack, and ref snapshots all build their *exec.Cmd here so
// the env hardening and arg-slice discipline live in exactly one place.
func gitCmd(ctx context.Context, extraEnv []string, args ...string) (*exec.Cmd, error) {
	bin, err := gitBinary()
	if err != nil {
		return nil, fmt.Errorf("git binary not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(baseGitEnv(), extraEnv...)
	return cmd, nil
}

// advertiseRefs runs `git <sub> --stateless-rpc --advertise-refs <bareDir>` and
// returns the raw ref advertisement (bounded by ref count, not pack size — safe
// to buffer). The smart-HTTP info/refs handler wraps it with the pkt-line
// service header. protocol forwards the client Git-Protocol (v2 advertisement
// differs, and must match the subsequent RPC).
func advertiseRefs(ctx context.Context, bareDir, service, protocol string) ([]byte, error) {
	cmd, err := gitCmd(ctx, gitProtocolEnv(protocol),
		packSubcommand(service), "--stateless-rpc", "--advertise-refs", bareDir)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s advertise-refs: %w: %s", packSubcommand(service), err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// startPackRPC spawns `git <sub> --stateless-rpc <bareDir>` for a smart-HTTP
// upload-pack POST (the clone/fetch result), wiring the request body to git
// stdin and returning a *gitPackStream over git stdout. The caller streams the
// returned ReadCloser as the response body (SendStream); fasthttp Close()s it
// after the body is drained (or on client disconnect, after CommandContext has
// killed git), and Close reaps the process. NO pack bytes are buffered in this
// process — git streams the multi-GB pack from disk through an OS pipe.
func startPackRPC(ctx context.Context, log luxlog.Logger, bareDir, service, protocol string, stdin io.Reader) (*gitPackStream, error) {
	sub := packSubcommand(service)
	cmd, err := gitCmd(ctx, gitProtocolEnv(protocol), sub, "--stateless-rpc", bareDir)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git %s stdout: %w", sub, err)
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start git %s: %w", sub, err)
	}
	return &gitPackStream{cmd: cmd, stdout: stdout, stderr: stderr, log: log, sub: sub}, nil
}

// gitPackStream adapts a running `git <sub> --stateless-rpc` subprocess to an
// io.ReadCloser so fasthttp can stream git stdout straight to the client. Read
// pulls from git stdout; Close (called by fasthttp after the body is fully sent,
// or on disconnect) reaps the process. A non-zero exit AFTER streaming began can
// only be logged — the 200 headers are already on the wire — which is exactly how
// smart-HTTP surfaces late errors: git writes them into the pack sideband.
type gitPackStream struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *cappedBuffer
	log    luxlog.Logger
	sub    string
}

func (g *gitPackStream) Read(p []byte) (int, error) { return g.stdout.Read(p) }

func (g *gitPackStream) Close() error {
	if err := g.cmd.Wait(); err != nil {
		g.log.Warn("git pack rpc exited non-zero", "sub", g.sub, "err", err, "stderr", strings.TrimSpace(g.stderr.String()))
	}
	return nil
}

// runPackSSH drives `git <sub> <bareDir>` (plain native protocol — advertise +
// negotiate + pack in one stream) over an SSH channel, streaming both directions
// with bounded memory. Ported from gitea cmd/serv.go. The channel is a
// bidirectional io.ReadWriteCloser (not an *os.File), so exec would deadlock on
// Wait if it copied the channel itself; we own the pipes and drive completion
// off git stdout closing (git exiting). The lingering client→git copy goroutine
// unblocks when the caller closes the channel after we return.
func runPackSSH(ctx context.Context, bareDir, service, protocol string, ch io.ReadWriteCloser) error {
	sub := packSubcommand(service)
	cmd, err := gitCmd(ctx, gitProtocolEnv(protocol), sub, bareDir)
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("git %s stdin: %w", sub, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("git %s stdout: %w", sub, err)
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start git %s: %w", sub, err)
	}
	go func() { _, _ = io.Copy(stdin, ch); _ = stdin.Close() }() // client → git
	_, _ = io.Copy(ch, stdout)                                   // git → client (until git exits)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git %s: %w: %s", sub, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// branchTips snapshots refs/heads/* → {shortName: fullHash} via a fresh git
// process, so the reading reflects on-disk state (no go-git object-cache
// staleness) and is shared by every push transport. Output is bounded (one line
// per branch). Best-effort: a read error yields a nil map, which the diff treats
// as "no branches" — a missing snapshot can only UNDER-fire a build, never
// mis-fire one against the wrong repo.
func branchTips(ctx context.Context, bareDir string) map[string]string {
	cmd, err := gitCmd(ctx, nil, "--git-dir="+bareDir, "for-each-ref",
		"--format=%(refname:short) %(objectname)", "refs/heads/")
	if err != nil {
		return nil
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil
	}
	tips := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			tips[line[:sp]] = line[sp+1:]
		}
	}
	return tips
}

// cappedBuffer captures at most cap bytes of a subprocess's stderr — bounded so a
// chatty or hostile child can never balloon memory, while still surfacing the
// head of the error for logs. Write always reports a full write so git never
// observes a short-write error on its stderr.
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
