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
func sshReceivePack(s *cloud.Service[state], ctx context.Context, org, project, name, protocol string, ch io.ReadWriteCloser) error {
	bareDir := s.State.storage.absRepoPath(org, project, name)
	before := branchTips(ctx, bareDir)
	err := runPackSSH(ctx, bareDir, svcReceivePack, protocol, ch)

	bg := context.WithoutCancel(ctx)
	recordUsage(s, bg, org, project, name)
	fireBranchBuilds(s, bg, org, project, name, before, branchTips(bg, bareDir))
	return err
}
