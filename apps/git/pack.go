package git

import (
	"context"
	"io"

	"github.com/hanzoai/cloud"
)

// pack.go is the SSH half of the ONE git pack seam (gitexec.go). SSH drives the
// SAME streaming git CLI the smart-HTTP transport uses, but over the native git
// protocol (plain `git upload-pack <dir>` / `git receive-pack <dir>` —
// advertise + negotiate + pack in one bidirectional stream on the channel's
// stdin/stdout), not stateless-rpc. So an SSH clone/push and an HTTPS clone/push
// converge on the same on-disk bare repo with the same org scope, and pack data
// streams to/from disk with bounded memory on both transports — the SSH channel
// carries arbitrarily large pushes with no request-body limit.
//
// The org/project/name are already resolved + org-checked by the caller (the SSH
// session from the presented key's bound org), so these funcs are
// transport-agnostic and take plain strings.

// sshUploadPack streams a clone/fetch over the SSH channel via
// `git upload-pack <bareDir>`. Read-only: no side effects.
func sshUploadPack(s *cloud.Service[state], ctx context.Context, org, project, name, protocol string, ch io.ReadWriteCloser) error {
	bareDir := s.State.storage.absRepoPath(org, project, name)
	return runPackSSH(ctx, bareDir, svcUploadPack, protocol, ch)
}

// sshReceivePack streams a push over the SSH channel via
// `git receive-pack <bareDir>`, then re-meters storage and fires push-to-deploy
// for every branch the push advanced — the SAME side effects a smart-HTTP push
// produces. The branch diff (before/after tips) is ground truth for what changed,
// so it runs regardless of a non-zero exit; both run on a cancel-immune context.
// pusher is the SSH key's bound user id (best-effort lifecycle attribution).
// An SSH push carries the SAME ref policy an HTTPS push does, from the same
// function (gitexec.go runPackSSHScreened). It used to carry none: this handler
// piped the channel straight into `git receive-pack`, so an org member who
// enrolled a key through POST /v1/git/keys could create, rewrite, force or
// DELETE any ref while the HTTP door next to it refused all four. A rule that
// one transport enforces and another does not is not a rule.
//
// There is no grant on this path — a grant is an HTTP bearer and SSH
// authenticates by key — so the confinement argument is "" and the namespace
// rules apply alone, which is the same thing a human pushing over HTTPS gets.
func sshReceivePack(s *cloud.Service[state], ctx context.Context, org, project, name, pusher, protocol string, ch io.ReadWriteCloser) error {
	bareDir := s.State.storage.absRepoPath(org, project, name)
	before := branchTips(ctx, bareDir)
	def := defaultBranchOf(ctx, bareDir)
	err := runPackSSHScreened(ctx, bareDir, svcReceivePack, protocol, ch,
		func(cmds []refCommand, _ string) error {
			verr := checkRefPolicy(cmds, def, "")
			if verr != nil {
				s.Log.Warn("git ssh push refused by ref policy", "org", org, "repo", name, "reason", verr.Error())
			}
			return verr
		})

	bg := context.WithoutCancel(ctx)
	recordUsage(s, bg, org, project, name)
	fireBranchBuilds(s, bg, org, project, name, pusher, before, branchTips(bg, bareDir))
	// Keep clones fast: opportunistic housekeeping (no-op until git's thresholds
	// trigger a repack). Detached + slot-yielding, never blocks the push.
	go autoMaintain(s.Log, bareDir)
	return err
}
