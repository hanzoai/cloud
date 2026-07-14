package git

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// mirror_out.go is the OUTBOUND half of the mirror: native → GitHub/GitLab. It is
// a git-lifecycle subscriber (registered in Mount) that, when a push lands on a
// repo with configured downstream targets, force-pushes JUST the branch that
// advanced to each target. Canonical-source, one-way: the downstreams are
// read-only replicas, so a force is safe — but only the advanced branch is pushed
// (never a blanket +refs/*), driven by the LifecycleEvent's Branch.
//
// Isolation from the shared git plane (Red HIGH-1): outbound pushes run on their
// OWN bounded semaphore (mirrorSem), strictly smaller than the shared pack pool, so
// a slow/blackholed downstream can never consume slots that clones / receive-pack /
// mirror-in need. Each push has a hard deadline (context.WithTimeout over the
// cancel-immune lifecycle context) AND git-level low-speed abort, so a stalled
// connection frees its slot fast and never leaks a goroutine.
//
// Credentials reuse the mirror hardening verbatim (gitexec.go env discipline,
// env-only http.extraHeader): the token attaches ONLY when the target host is on
// the OUTBOUND allowlist (github.com / gitlab.com — the local git host deliberately
// excluded, Red MED-1), never on argv or in a log.

// mirrorSem bounds outbound mirror pushes with a DEDICATED pool, separate from the
// shared pack semaphore (gitexec.go packSem) — so a stalled target can never starve
// the git data plane for other tenants. GIT_MIRROR_OUT_CONCURRENCY tunes it
// (default 1, floor 1); a dedicated single slot keeps outbound strictly below the
// shared pool.
var mirrorSem = make(chan struct{}, mirrorOutConcurrency())

func mirrorOutConcurrency() int {
	n := 1
	if v := strings.TrimSpace(os.Getenv("GIT_MIRROR_OUT_CONCURRENCY")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 1 {
			n = p
		}
	}
	return n
}

// mirrorWaitDelay bounds how long cmd.Wait blocks AFTER the push ctx is cancelled
// before it force-closes the subprocess pipes and returns — so a network-helper
// child that outlives the killed git parent can never wedge the mirror slot.
const mirrorWaitDelay = 5 * time.Second

// mirrorPushTimeout is the hard ceiling on one outbound push (slot wait + transfer).
// GIT_MIRROR_OUT_TIMEOUT (seconds) overrides; default 300s.
func mirrorPushTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("GIT_MIRROR_OUT_TIMEOUT")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 1 {
			return time.Duration(secs) * time.Second
		}
	}
	return 300 * time.Second
}

// withMirrorSlot runs fn while holding an outbound-mirror slot, bounded by ctx: a
// push whose ctx expires while WAITING for a slot unblocks and fails rather than
// piling up a goroutine forever.
func withMirrorSlot(ctx context.Context, fn func() error) error {
	select {
	case mirrorSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-mirrorSem }()
	return fn()
}

// mirrorOutbound force-pushes the advanced branch of a landed push to every
// configured downstream target. Best-effort: a target failure is logged (with any
// credential redacted) and never affects the push or the other targets. Runs on the
// cancel-immune lifecycle context under the dedicated mirror semaphore, so it
// neither blocks the git path nor contends with clones/pushes for memory or slots.
func mirrorOutbound(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	if ev.Kind != cloud.LifecyclePushLanded || ev.Branch == "" {
		return
	}
	store, err := storeFor(s, ev.Org)
	if err != nil {
		s.Log.Warn("git mirror-out: open store", "org", ev.Org, "err", err)
		return
	}
	targets, err := store.ListMirrors(ctx, ev.Org, ev.Project, ev.Repo)
	if err != nil {
		s.Log.Warn("git mirror-out: list targets", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	if len(targets) == 0 {
		return
	}
	bareDir := s.State.storage.absRepoPath(ev.Org, ev.Project, ev.Repo)
	for _, t := range targets {
		// Loop prevention: refs that arrived via an inbound sync FROM this host must
		// not be re-mirrored straight back to it. Origin is "" for a native push
		// (always mirrors); a future inbound sync stamps the source host, and this
		// suppresses the echo. The seam is wired now; inbound sync sets it later.
		if ev.Origin != "" && strings.EqualFold(strings.TrimSpace(ev.Origin), t.Host) {
			continue
		}
		if err := pushBranchToMirror(ctx, bareDir, t, ev.Branch); err != nil {
			s.Log.Warn("git mirror-out push failed",
				"org", ev.Org, "repo", ev.Repo, "host", t.Host, "branch", ev.Branch,
				"err", sanitizeGitErr(err.Error()))
			continue
		}
		s.Log.Info("git mirror-out pushed", "org", ev.Org, "repo", ev.Repo, "host", t.Host, "branch", ev.Branch)
	}
}

// pushBranchToMirror force-pushes exactly one branch (refs/heads/<branch>) from the
// bare repo to target t, under a hard deadline + dedicated slot. The leading '+' in
// the refspec forces that ONE ref — never a blanket +refs/*:*, so nothing but the
// advanced branch is touched downstream.
func pushBranchToMirror(ctx context.Context, bareDir string, t MirrorTarget, branch string) error {
	if !branchRE.MatchString(branch) {
		return fmt.Errorf("invalid branch")
	}
	// Hard per-push deadline over the (cancel-immune) lifecycle context: a stalled
	// downstream can never hold a slot or leak a goroutine indefinitely.
	ctx, cancel := context.WithTimeout(ctx, mirrorPushTimeout())
	defer cancel()
	refspec := "+refs/heads/" + branch + ":refs/heads/" + branch
	args := append(packConfigArgs(""),
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"--git-dir="+bareDir, "push", t.URL, refspec)
	return withMirrorSlot(ctx, func() error {
		cmd, err := gitCmd(ctx, mirrorPushEnv(t.Host), args...)
		if err != nil {
			return err
		}
		stderr := &cappedBuffer{cap: stderrCap}
		cmd.Stderr = stderr
		// A stalled downstream leaves git's network helper child (git-remote-http)
		// holding the stderr pipe open, which would make cmd.Wait() block PAST the
		// ctx deadline (killing the parent alone doesn't reap the child that owns the
		// pipe). WaitDelay bounds that: once ctx fires, Wait force-closes the pipes
		// and returns within the grace, so the slot + goroutine are always freed
		// (Red HIGH-1). Without it the timeout would not actually bound the push.
		cmd.WaitDelay = mirrorWaitDelay
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git push: %w: %s", err, sanitizeGitErr(stderr.String()))
		}
		return nil
	})
}

// mirrorPushEnv builds the outbound-push git environment: outbound restricted to
// http/https; redirects disabled (a redirect can neither carry the token to another
// host nor bounce the push); a low-speed abort so a stalled connection fails fast
// instead of holding a slot; and — only for an allowlisted host with a configured
// token — the credential via env-only http.extraHeader (presented with the host's
// basic-auth username, mirrorBasicUser).
func mirrorPushEnv(host string) []string {
	env := []string{"GIT_ALLOW_PROTOCOL=http:https"}
	cfg := []string{
		"http.followRedirects=false",
		"http.lowSpeedLimit=1000", // if throughput stays under 1 KB/s ...
		"http.lowSpeedTime=30",    // ... for 30s, git aborts the transfer
	}
	if hdr := mirrorPushAuthHeader(host); hdr != "" {
		cfg = append(cfg, "http.extraHeader=Authorization: Basic "+hdr)
	}
	return append(env, gitConfigEnv(cfg...)...)
}

// mirrorPushAuthHeader returns the base64 "<user>:<token>" basic-auth credential
// for a downstream push ONLY when the target host is on the OUTBOUND allowlist
// (mirrorOutHostAllowed) and a token is configured (GIT_MIRROR_TOKEN). Empty for a
// non-allowlisted host or no token — the push then proceeds anonymously and simply
// fails closed if the remote requires auth, never leaking the token to an untrusted
// host.
func mirrorPushAuthHeader(host string) string {
	tok := strings.TrimSpace(os.Getenv(mirrorEnvToken))
	if tok == "" || !mirrorOutHostAllowed(host) {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(mirrorBasicUser(host) + ":" + tok))
}
