package git

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
)

// mirror_out.go is the OUTBOUND half of the mirror: native → GitHub/GitLab. It is
// a git-lifecycle subscriber (registered in Mount) that, when a push lands on a
// repo with configured downstream targets, force-pushes JUST the branch that
// advanced to each target. Canonical-source, one-way: the downstreams are
// read-only replicas, so a force is safe — but only the advanced branch is pushed
// (never a blanket +refs/*), driven by the LifecycleEvent's Branch.
//
// It reuses the mirror-in hardening verbatim (gitexec.go env discipline, the
// pack-slot semaphore, the host allowlist, credential-via-env-only): the token
// attaches ONLY when the target host is on the allowlist (github.com / gitlab.com
// / git.hanzo.ai), via http.extraHeader, never on argv or in a log.

// mirrorOutbound force-pushes the advanced branch of a landed push to every
// configured downstream target. Best-effort: a target failure is logged (with any
// credential redacted) and never affects the push or the other targets. Runs on
// the cancel-immune lifecycle context under the pack-slot semaphore, so it neither
// blocks the git path nor contends with clones/pushes for memory.
func mirrorOutbound(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	if ev.Kind != cloud.LifecyclePushLanded || ev.Branch == "" {
		return
	}
	store, err := storeFor(s, ev.Org)
	if err != nil {
		s.Log.Warn("git mirror-out: open store", "org", ev.Org, "err", err)
		return
	}
	targets, err := store.ListMirrors(ctx, ev.Org, ev.Repo)
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
// bare repo to target t. The leading '+' in the refspec forces that ONE ref — never
// a blanket +refs/*:*, so nothing but the advanced branch is touched downstream.
func pushBranchToMirror(ctx context.Context, bareDir string, t MirrorTarget, branch string) error {
	if !branchRE.MatchString(branch) {
		return fmt.Errorf("invalid branch")
	}
	refspec := "+refs/heads/" + branch + ":refs/heads/" + branch
	args := append(packConfigArgs(""),
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"--git-dir="+bareDir, "push", t.URL, refspec)
	cmd, err := gitCmd(ctx, mirrorPushEnv(t.Host), args...)
	if err != nil {
		return err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	if err := withPackSlot(ctx, cmd.Run); err != nil {
		return fmt.Errorf("git push: %w: %s", err, sanitizeGitErr(stderr.String()))
	}
	return nil
}

// mirrorPushEnv builds the outbound-push git environment: outbound restricted to
// http/https, redirects disabled (a redirect can neither carry the token to
// another host nor bounce the push), and — only for an allowlisted host with a
// configured token — the credential via env-only http.extraHeader. Mirrors
// mirrorGitEnv (the inbound form) but presents the token with the host's basic-auth
// username (mirrorBasicUser).
func mirrorPushEnv(host string) []string {
	env := []string{"GIT_ALLOW_PROTOCOL=http:https"}
	cfg := []string{"http.followRedirects=false"}
	if hdr := mirrorPushAuthHeader(host); hdr != "" {
		cfg = append(cfg, "http.extraHeader=Authorization: Basic "+hdr)
	}
	return append(env, gitConfigEnv(cfg...)...)
}

// mirrorPushAuthHeader returns the base64 "<user>:<token>" basic-auth credential
// for a downstream push ONLY when the target host is on the allowlist and a token
// is configured (GIT_MIRROR_TOKEN). Empty for a non-allowlisted host or no token —
// the push then proceeds anonymously and simply fails closed if the remote
// requires auth, never leaking the token to an untrusted host.
func mirrorPushAuthHeader(host string) string {
	tok := strings.TrimSpace(os.Getenv(mirrorEnvToken))
	if tok == "" || !mirrorHostAllowed(host) {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(mirrorBasicUser(host) + ":" + tok))
}
