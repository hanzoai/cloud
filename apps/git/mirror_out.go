package git

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
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
// env-only http.extraHeader): a token attaches ONLY when the target host is on
// the OUTBOUND allowlist (github.com / gitlab.com — the local git host deliberately
// excluded, Red MED-1), never on argv or in a log, and it is the pushing org's own
// grant — the deployment's credential reaches only the replica this deployment
// owns (mirrorCredential).

// mirrorSem bounds outbound mirror pushes with a DEDICATED pool, separate from the
// shared pack semaphore (gitexec.go packSem) — so a stalled target can never starve
// the git data plane for other tenants. GIT_MIRROR_OUT_CONCURRENCY tunes it
// (default 1, floor 1); a dedicated single slot keeps outbound strictly below the
// shared pool.
var mirrorSem = make(chan struct{}, mirrorOutConcurrency())

func mirrorOutConcurrency() int {
	n := 1
	if v := env.Or("GIT_MIRROR_OUT_CONCURRENCY", ""); v != "" {
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
	if v := env.Or("GIT_MIRROR_OUT_TIMEOUT", ""); v != "" {
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
	// A repository kept in the store is written to a throwaway directory, with the one
	// branch that advanced, for the CLI to push from (export.go).
	if r, err := store.Get(ctx, ev.Org, ev.Project, ev.Repo); err == nil {
		if objs, err := s.State.vault.home(ctx, s, r); err != nil {
			s.Log.Warn("git mirror-out: open repo", "org", ev.Org, "repo", ev.Repo, "err", err)
			return
		} else if objs != nil {
			dir, cleanup, err := exportBranch(ctx, objs.Repo(ctx, r.ID), ev.Branch)
			if errors.Is(err, errNoBranch) {
				return // the branch is gone again; nothing to mirror
			}
			if err != nil {
				s.Log.Warn("git mirror-out: export", "org", ev.Org, "repo", ev.Repo, "err", err)
				return
			}
			defer cleanup()
			bareDir = dir
		}
	}
	now := time.Now()
	for _, t := range targets {
		// Loop prevention: refs that arrived via an inbound sync FROM this host must
		// not be re-mirrored straight back to it. Origin is "" for a native push
		// (always mirrors); a future inbound sync stamps the source host, and this
		// suppresses the echo. The client is wired now; inbound sync sets it later.
		if ev.Origin != "" && strings.EqualFold(strings.TrimSpace(ev.Origin), t.Host) {
			continue
		}
		if paused(t.ID, now) {
			continue
		}
		if err := pushBranchToMirror(ctx, ev.Org, bareDir, t, ev.Branch); err != nil {
			s.Log.Warn("git mirror-out push failed",
				"org", ev.Org, "repo", ev.Repo, "host", t.Host, "branch", ev.Branch,
				"retry_in", refused(t.ID, now).String(), "err", sanitizeGitErr(err.Error()))
			continue
		}
		accepted(t.ID)
		s.Log.Info("git mirror-out pushed", "org", ev.Org, "repo", ev.Repo, "host", t.Host, "branch", ev.Branch)
	}
}

// pushBranchToMirror force-pushes exactly one branch (refs/heads/<branch>) from the
// bare repo to target t, under a hard deadline + dedicated slot. The leading '+' in
// the refspec forces that ONE ref — never a blanket +refs/*:*, so nothing but the
// advanced branch is touched downstream.
func pushBranchToMirror(ctx context.Context, org, bareDir string, t MirrorTarget, branch string) error {
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
		hdr, end := mirrorCredential(ctx, org, t)
		defer end()
		cmd, err := gitCmd(ctx, mirrorPushEnv(hdr), args...)
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

// A target that refuses a push is not sent it again on the next landed push: it
// waits retryFirst, doubling with each refusal up to retryMost, and one success
// clears it. Without this a downstream that cannot take the pack — the public edge
// cut every push over 100 MB — was sent the whole history on every sync, and the
// receiver kept each partial upload. Process-local: a restart retries at once,
// which is one attempt, not a loop.
const (
	retryFirst = 15 * time.Minute
	retryMost  = 24 * time.Hour
)

type refusal struct {
	count int
	until time.Time
}

var refusals = struct {
	sync.Mutex
	by map[string]refusal
}{by: map[string]refusal{}}

// paused reports whether target id is waiting out a refusal at now.
func paused(id string, now time.Time) bool {
	refusals.Lock()
	defer refusals.Unlock()
	return now.Before(refusals.by[id].until)
}

// refused records a refusal by target id and returns how long it now waits.
func refused(id string, now time.Time) time.Duration {
	refusals.Lock()
	defer refusals.Unlock()
	r := refusals.by[id]
	wait := retryFirst << min(r.count, 7)
	if wait > retryMost {
		wait = retryMost
	}
	refusals.by[id] = refusal{count: r.count + 1, until: now.Add(wait)}
	return wait
}

// accepted clears target id's refusals.
func accepted(id string) {
	refusals.Lock()
	defer refusals.Unlock()
	delete(refusals.by, id)
}

// mirrorPushEnv builds the outbound-push git environment: outbound restricted to
// http/https; redirects disabled (a redirect can neither carry the token to another
// host nor bounce the push); a low-speed abort so a stalled connection fails fast
// instead of holding a slot; and the credential hdr, when there is one, via
// env-only http.extraHeader.
func mirrorPushEnv(hdr string) []string {
	env := []string{"GIT_ALLOW_PROTOCOL=http:https"}
	cfg := []string{
		"http.followRedirects=false",
		"http.lowSpeedLimit=1000", // if throughput stays under 1 KB/s ...
		"http.lowSpeedTime=30",    // ... for 30s, git aborts the transfer
	}
	if hdr != "" {
		cfg = append(cfg, "http.extraHeader=Authorization: Basic "+hdr)
	}
	return append(env, gitConfigEnv(cfg...)...)
}

// grantCall bounds one ask of the provider process for a token, and one revoke.
const grantCall = 15 * time.Second

// askGrant is the provider process's mint for a token that reaches one repository
// of an account the caller's org connected; a test stands in for it.
var askGrant = providerpeer.IntegrationsGitHubGrant

// mirrorCredential is the basic-auth credential for one push to target t, and
// what ends it.
//
// A target is a URL the org declared, so the push carries the ORG's credential:
// for a GitHub target, a token reaching that one repository on the org's own
// installation, minted by the provider process — the one holding the App key —
// and revoked once the push is done. This app and provider run as separate
// processes, so the token is asked for over the plane, with the org on the call's
// caller: the provider mints only on installations that org connected, so a
// repository mirrors back with its own org's credential and never another's.
//
// The deployment's credential reaches exactly one kind of target: the org's own
// community replica (ownReplica), the exact URL this deployment provisioned on
// its own account for that repository. Every other target with no grant —
// the account is not connected, no provider answers, any other allowed host —
// gets nothing, and a private remote then fails closed. A host off the outbound
// allowlist gets nothing at all.
func mirrorCredential(ctx context.Context, org string, t MirrorTarget) (string, func()) {
	none := func() {}
	host, target := t.Host, t.URL
	if !mirrorOutHostAllowed(host) {
		return "", none
	}
	if ownReplica(org, t) {
		if tok := secret(); tok != "" {
			return basicAuth(host, tok), none
		}
		return "", none
	}
	if strings.EqualFold(host, "github.com") {
		if owner, repo := githubRepoOf(target); owner != "" && repo != "" {
			call, cancel := context.WithTimeout(plane.For(ctx, org), grantCall)
			g, err := askGrant(call, &client.GitHubGrantIn{Owner: owner, Repo: repo})
			cancel()
			if err == nil && g != nil && strings.TrimSpace(g.Token) != "" {
				tok := strings.TrimSpace(g.Token)
				return basicAuth(host, tok), func() { revokeGrant(ctx, tok) }
			}
		}
	}
	return "", none
}

// revokeGrant ends a minted token now rather than at GitHub's hour, with itself.
// It runs after the push, whose deadline may have passed, so it takes its own. A
// failed revoke leaves the token to expire; it reaches one repository.
func revokeGrant(ctx context.Context, tok string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grantCall)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, api+"/installation/token", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if resp, err := (&http.Client{Timeout: grantCall}).Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// basicAuth is "<user>:<token>" in base64, with the username host takes.
func basicAuth(host, tok string) string {
	return base64.StdEncoding.EncodeToString([]byte(mirrorBasicUser(host) + ":" + tok))
}

// githubRepoOf reads the account and repository out of a GitHub remote —
// https://github.com/<owner>/<name>[.git]. Both empty when the URL names
// neither, which the caller reads as "this target is not GitHub".
func githubRepoOf(raw string) (owner, name string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git")
}
