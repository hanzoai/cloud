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
	"sync/atomic"

	luxlog "github.com/luxfi/log"
)

// gitexec.go is the ONE client that shells out to the streaming `git` CLI for the
// heavy object-plane operations — clone/fetch serve (upload-pack), push receive
// (receive-pack), and mirror-in (fetch). go-git's pure-Go server transport
// buffered whole packs in RAM (a 3 GB clone = 3 GB), which OOM-killed the 1 Gi
// cloud pod when large repos were mirrored. The git CLI (index-pack /
// upload-pack / receive-pack) streams packs to and from disk, so memory stays
// bounded by an OS pipe buffer regardless of repo size — the way real git hosts
// (GitLab, and every other real forge) serve smart-HTTP. The patterns here are
// ported from the upstream forge this plane was seeded from (routers/web/repo/githttp.go, modules/git/command.go, cmd/serv.go).
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

// packSem bounds how many pack subprocesses (upload-pack / receive-pack / fetch)
// run at once. index-pack / pack-objects each hold O(object-count) state plus
// delta caches in the SAME pod cgroup, so N concurrent multi-GB clones would
// multiply RAM and re-OOM the pod. GIT_PACK_MAX_CONCURRENCY tunes it (default 2,
// floor 1). A cheap ref-list (advertise-refs / for-each-ref / ls-remote) is NOT
// gated — only the memory-heavy pack ops are.
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

// acquirePackSlot blocks until a pack slot is free or ctx is done; releasePackSlot
// returns it. A request that is cancelled while waiting fails fast rather than
// piling up.
func acquirePackSlot(ctx context.Context) error {
	select {
	case packSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releasePackSlot() { <-packSem }

// tryAcquirePackSlot takes a pack slot only if one is immediately free, else
// reports false without blocking. Opportunistic background housekeeping (post-push
// auto-gc) uses this to YIELD to active clones/pushes rather than queue behind
// them — the housekeeping simply runs on a later, quieter push.
func tryAcquirePackSlot() bool {
	select {
	case packSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// withPackSlot runs a synchronous pack op (receive-pack, mirror fetch) while
// holding a slot. The streaming upload-pack holds its slot across the response
// and releases it in gitPackStream.Close instead.
func withPackSlot(ctx context.Context, fn func() error) error {
	if err := acquirePackSlot(ctx); err != nil {
		return err
	}
	defer releasePackSlot()
	return fn()
}

// packConfigArgs returns the `-c` config flags for a pack subprocess: memory
// bounds on EVERY op (single-threaded delta search with capped window /
// delta-cache and a big-file threshold, so index-pack / pack-objects can't
// balloon the cgroup during a multi-GB clone/mirror), plus a received-input-size
// cap for receive-pack so a gzip-amplified or runaway push can't fill the pod
// disk and evict tenants. These are main git options (before the subcommand).
func packConfigArgs(service string) []string {
	args := []string{
		"-c", "pack.threads=1",
		"-c", "pack.windowMemory=64m",
		"-c", "pack.deltaCacheSize=64m",
		"-c", "core.bigFileThreshold=16m",
	}
	if service == svcReceivePack {
		args = append(args, "-c", "receive.maxInputSize="+receiveMaxInputSize())
	}
	return args
}

// receiveMaxInputSize caps the bytes git receive-pack will read from a push
// (git size syntax, e.g. "2g"). GIT_RECEIVE_MAX_INPUT_SIZE overrides; default 2g.
func receiveMaxInputSize() string {
	if v := strings.TrimSpace(os.Getenv("GIT_RECEIVE_MAX_INPUT_SIZE")); v != "" {
		return v
	}
	return "2g"
}

// packSubcommand maps the smart-HTTP service name (git-upload-pack /
// git-receive-pack) to the git subcommand (upload-pack / receive-pack). Only
// these two are ever spawned; the caller validates the service against the
// svcUploadPack / svcReceivePack allowlist first.
func packSubcommand(service string) string { return strings.TrimPrefix(service, "git-") }

// packetWrite pkt-line-encodes str: a 4-hex-digit length prefix (counting the 4
// prefix bytes) then the payload. Ported verbatim from the upstream forge
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
// negotiation + partial clone on large repos). Ported from upstream: one or more
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
// the upstream forge's git module uses:
//   - GIT_CONFIG_NOSYSTEM + GIT_CONFIG_GLOBAL=/dev/null: ignore /etc/gitconfig
//     and any ~/.gitconfig — no operator config, no credential helper, no LFS
//     smudge/clean filters (git >= 2.32, satisfied by the alpine 3.22 git).
//   - HOME=/nonexistent: belt-and-suspenders against a stray global config.
//   - GIT_TERMINAL_PROMPT=0: never block on an interactive credential prompt.
//   - GIT_NO_REPLACE_OBJECTS=1: ignore refs/replace remaps.
//   - LC_ALL=C: stable, parseable output.
//   - GIT_CONFIG_COUNT pack/mmap bounds: every git subprocess shares the
//     writer's cgroup, and pack generation is proportional to REPO size, not
//     request size — an unbounded upload-pack of a multi-GiB repo is a
//     multi-GiB allocation the Go runtime cannot see or govern (GOMEMLIMIT
//     bounds only the Go heap), landing as a kernel OOM kill of the whole
//     API. The caps trade clone speed on the biggest repos for a bounded
//     worst case: delta search 64m/thread x 2 threads, pack mmap 256m in 32m
//     windows, delta cache 64m — a few hundred MB ceiling per operation.
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
		"GIT_CONFIG_COUNT=5",
		"GIT_CONFIG_KEY_0=pack.windowMemory", "GIT_CONFIG_VALUE_0=64m",
		"GIT_CONFIG_KEY_1=pack.threads", "GIT_CONFIG_VALUE_1=2",
		"GIT_CONFIG_KEY_2=core.packedGitLimit", "GIT_CONFIG_VALUE_2=256m",
		"GIT_CONFIG_KEY_3=core.packedGitWindowSize", "GIT_CONFIG_VALUE_3=32m",
		"GIT_CONFIG_KEY_4=pack.deltaCacheSize", "GIT_CONFIG_VALUE_4=64m",
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
	if err := acquirePackSlot(ctx); err != nil {
		return nil, err
	}
	sub := packSubcommand(service)
	cmd, err := gitCmd(ctx, gitProtocolEnv(protocol), append(packConfigArgs(service), sub, "--stateless-rpc", bareDir)...)
	if err != nil {
		releasePackSlot()
		return nil, err
	}
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		releasePackSlot()
		return nil, fmt.Errorf("git %s stdout: %w", sub, err)
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		releasePackSlot()
		return nil, fmt.Errorf("start git %s: %w", sub, err)
	}
	return &gitPackStream{cmd: cmd, stdout: stdout, stderr: stderr, log: log, sub: sub, release: releasePackSlot}, nil
}

// gitPackStream adapts a running `git <sub> --stateless-rpc` subprocess to an
// io.ReadCloser so fasthttp can stream git stdout straight to the client. Read
// pulls from git stdout; Close (called by fasthttp after the body is fully sent,
// or on client disconnect) REAPS the process and releases the pack slot. A
// non-zero exit after streaming began can only be logged — the 200 headers are
// already on the wire — which is exactly how smart-HTTP surfaces late errors: git
// writes them into the pack sideband.
type gitPackStream struct {
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	stderr  *cappedBuffer
	log     luxlog.Logger
	sub     string
	release func() // returns the pack-concurrency slot; runs once in Close
}

func (g *gitPackStream) Read(p []byte) (int, error) { return g.stdout.Read(p) }

// hold makes fn run when the stream closes, after the pack slot goes back.
//
// A handler that streams cannot DEFER the release of anything the stream needs:
// SendStream hands the reader to the server and returns, so the deferred call
// runs before the first byte is written. Anything whose lifetime is the pack's —
// the cache reader that keeps a bare directory from being reclaimed mid-clone —
// attaches here instead, where Close is the one event that means "done".
func (g *gitPackStream) hold(fn func()) {
	prev := g.release
	g.release = func() {
		if prev != nil {
			prev()
		}
		fn()
	}
}

func (g *gitPackStream) Close() error {
	// The client is done (EOF) or gone (disconnect). On disconnect git may be
	// blocked writing to a full stdout pipe, so close the read end (EPIPE) AND
	// Kill to guarantee the process is reaped even if it's wedged — otherwise
	// Wait() would hang forever and leak the proc/goroutine/FDs (a git host sees
	// aborted clones constantly). On normal completion git has already exited, so
	// both are no-ops and Wait reports the true status.
	_ = g.stdout.Close()
	_ = g.cmd.Process.Kill()
	if err := g.cmd.Wait(); err != nil {
		g.log.Debug("git pack stream closed", "sub", g.sub, "err", err)
	}
	if g.release != nil {
		g.release()
	}
	return nil
}

// runPackSSH drives `git <sub> <bareDir>` (plain native protocol — advertise +
// negotiate + pack in one stream) over an SSH channel, streaming both directions
// with bounded memory. Ported from the upstream forge's serv command. The channel is a
// bidirectional io.ReadWriteCloser (not an *os.File), so exec would deadlock on
// Wait if it copied the channel itself; we own the pipes and drive completion
// off git stdout closing (git exiting). The lingering client→git copy goroutine
// unblocks when the caller closes the channel after we return.
func runPackSSH(ctx context.Context, bareDir, service, protocol string, ch io.ReadWriteCloser) error {
	return runPackSSHScreened(ctx, bareDir, service, protocol, ch, nil)
}

// screen decides whether a push may proceed, from the ref commands the client
// sent. It returns the refusal to write back, or nil to let git have the stream.
// A nil screen is a read (upload-pack), which has nothing to decide.
type screen func(cmds []refCommand, caps string) error

// runPackSSHScreened is runPackSSH with the ref policy standing between the
// client and git.
//
// The SSH transport gets git's NATIVE protocol, not stateless-rpc: one
// bidirectional stream carrying advertisement, commands and pack together. So
// unlike the HTTP door there is no request body to inspect before deciding —
// the commands arrive mid-conversation, and the multi-gigabyte pack arrives
// immediately behind them and must never be buffered.
//
// The sequence that makes a decision possible without buffering:
//
//  1. git starts and writes its ref advertisement, which we relay, because the
//     client will not send commands until it has read one.
//  2. We read the COMMAND SECTION off the channel — bounded, small, and framed
//     so we know exactly where it ends.
//  3. The policy judges it.
//  4. Admitted: git receives those exact bytes, then the rest of the stream is
//     spliced through untouched, so the pack still streams to disk.
//     Refused: git's stdin is closed instead. It has read no commands, so it
//     applies nothing and exits; we then write git's own report-status, and the
//     person who typed the push reads the reason rather than a broken pipe.
//
// Refusing at step 3 rather than inside git is what makes the SSH transport
// carry the SAME rule as the HTTP one, from the same function, rather than
// being the door the rule forgot.
func runPackSSHScreened(ctx context.Context, bareDir, service, protocol string, ch io.ReadWriteCloser, sc screen) error {
	if err := acquirePackSlot(ctx); err != nil {
		return err
	}
	defer releasePackSlot()
	sub := packSubcommand(service)
	cmd, err := gitCmd(ctx, gitProtocolEnv(protocol), append(packConfigArgs(service), sub, bareDir)...)
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("git %s stdin: %w", sub, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("git %s stdout: %w", sub, err)
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("start git %s: %w", sub, err)
	}

	// client → git. With a screen the head of the stream is read and judged here
	// FIRST; refused pushes never reach git's stdin at all.
	//
	// refused is written before stdin is closed and read only after git has been
	// reaped, so the decision is always visible by the time it is consulted. It
	// is a pointer rather than a "did the channel have anything" test because git
	// can also die on its own — and then this goroutine is still blocked reading
	// the client, having decided nothing, which must report GIT's failure and not
	// a refusal that was never made.
	var refusal atomic.Pointer[[]byte]
	go func() {
		defer func() { _ = stdin.Close() }()
		if sc != nil {
			report, ok := screenPush(ch, stdin, sc)
			if !ok {
				refusal.Store(&report)
				return
			}
		}
		_, _ = io.Copy(stdin, ch)
	}()

	_, copyErr := io.Copy(ch, stdout) // git → client (until git exits)
	// On an SSH disconnect the channel write fails; git may be blocked writing to
	// its (now-undrained) stdout, so Kill to guarantee reaping rather than hang.
	if copyErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()

	// git has finished and everything it wrote is already on the channel, so the
	// refusal goes last and is the last thing the client reads. It is written
	// here rather than in the goroutine precisely so it cannot interleave with
	// git's advertisement.
	if report := refusal.Load(); report != nil {
		_, _ = ch.Write(*report)
		return nil
	}
	if waitErr != nil {
		return fmt.Errorf("git %s: %w: %s", sub, waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// screenPush reads the command section off the client stream, judges it, and
// either forwards it to git verbatim (true) or returns the report-status to send
// instead (false).
//
// Every failure is a REFUSAL, never a pass: a section that will not frame, will
// not parse, or does not satisfy the policy all end the same way. The one thing
// this must never do is hand git bytes it did not understand.
func screenPush(client io.Reader, git io.Writer, sc screen) ([]byte, bool) {
	raw, err := readCommandSection(client)
	if err != nil {
		return refusalReport(nil, "", "unreadable push: "+err.Error()), false
	}
	cmds, _, caps, err := parseRefCommandsCaps(raw)
	if err != nil {
		return refusalReport(nil, caps, "unreadable push: "+err.Error()), false
	}
	if verr := sc(cmds, caps); verr != nil {
		return refusalReport(cmds, caps, verr.Error()), false
	}
	if _, err := git.Write(raw); err != nil {
		return refusalReport(cmds, caps, "the forge could not accept this push"), false
	}
	return nil, true
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
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			tips[line[:sp]] = line[sp+1:]
		}
	}
	return tips
}

// defaultBranchOf reads the repo's symbolic HEAD as a short branch name. It is
// the ref-policy's one input beyond the push itself, kept beside branchTips
// because it is the same thing: a small read off our own bare storage.
//
// An unreadable HEAD answers "" rather than an error, and the policy treats ""
// as "no branch is the default". That is the safe direction: it withholds one
// protection rather than refusing every push to a repo whose HEAD we could not
// read.
func defaultBranchOf(ctx context.Context, bareDir string) string {
	cmd, err := gitCmd(ctx, nil, "--git-dir="+bareDir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return ""
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
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
