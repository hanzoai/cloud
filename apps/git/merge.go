package git

// merge.go is the one operation a pull request performs on the repository:
// advance base to head. It sits beside gitbackend.go, push.go and storage.go on
// the go-git side of the boundary the Repository model draws (repository.go), so
// the control plane above it — pulls.go — states proposals in branch names and
// revisions and never touches a plumbing.Hash.
//
// # Fast-forward only, and it says so
//
// A three-way merge needs a tree-level merge with conflict detection, which
// go-git v5 does not implement; writing one here would be writing a merge engine
// rather than a pull request. So base moves to head exactly when base is already
// an ancestor of head — head then contains every commit base had, so the move
// loses nothing and invents nothing — and every other case is REFUSED, naming
// the reason and the fix. The refusal is the honest half: the alternative is an
// op reporting a merge for a base whose commits are nowhere in the result.
//
// # A merge is a push, and is judged as one
//
// Advancing base is a ref write, and a ref write that skipped the ref policy
// would be a door around it — the hole push.go closed when the policy guarded
// only receive-pack. The move is therefore stated as the same refCommand the
// wire door parses out of pkt-lines, judged by the same checkRefPolicy, and it
// fires the same reactions, because a branch that moved is a branch that moved
// whichever door moved it.

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogitstorage "github.com/go-git/go-git/v5/storage"
	"github.com/zap-proto/zip"
)

// branchExists reports whether a branch is present in the repo — what "head must
// already exist" is checked with, so a proposal cannot name a branch nobody
// pushed.
func branchExists(o ops, t tenant, repo, branch string) bool {
	st, err := o.s.State.storage.storer(t.org, t.project, repo)
	if err != nil {
		return false
	}
	_, err = st.Reference(plumbing.NewBranchReferenceName(branch))
	return err == nil
}

// fastForward moves base to head when base is an ancestor of it, and returns
// what base points at afterwards. It is the ONLY thing a pull request writes.
//
// The returned errors are the op's: this is reached from exactly one caller, and
// keeping the refusal text beside the condition that produces it is what stops
// the two from drifting.
func fastForward(o ops, ctx context.Context, t tenant, repo string, p Pull) (string, error) {
	st, err := o.s.State.storage.storer(t.org, t.project, repo)
	if err != nil {
		return "", internalErr(err)
	}
	baseName := plumbing.NewBranchReferenceName(p.Base)
	base, err := st.Reference(baseName)
	if err != nil {
		return "", zip.ErrConflict("base branch no longer exists: " + p.Base)
	}
	head, err := st.Reference(plumbing.NewBranchReferenceName(p.Head))
	if err != nil {
		return "", zip.ErrConflict("head branch no longer exists: " + p.Head)
	}
	// Already the same commit: head's work IS what base points at, so there is
	// nothing to move. This is also how a merge that moved the ref and then
	// failed to settle its row converges on a retry.
	if base.Hash() == head.Hash() {
		return base.Hash().String(), nil
	}
	baseCommit, err := object.GetCommit(st, base.Hash())
	if err != nil {
		return "", internalErr(fmt.Errorf("read base commit %s: %w", base.Hash(), err))
	}
	headCommit, err := object.GetCommit(st, head.Hash())
	if err != nil {
		return "", internalErr(fmt.Errorf("read head commit %s: %w", head.Hash(), err))
	}
	ff, err := baseCommit.IsAncestor(headCommit)
	if err != nil {
		return "", internalErr(fmt.Errorf("walk history: %w", err))
	}
	if !ff {
		return "", zip.ErrConflict(fmt.Sprintf(
			"%s has commits %s does not contain, so merging them needs a three-way merge; "+
				"this forge fast-forwards only and nothing was written. Rebase %s onto %s and merge again.",
			p.Base, p.Head, p.Head, p.Base))
	}

	// THE REF POLICY, on the merge door. Its refusal answers 400, which is what
	// pushFiles answers for the identical refusal (push.go maps errBadInput).
	// Two doors, one policy, one status.
	cmd := refCommand{Old: base.Hash().String(), New: head.Hash().String(), Ref: baseName.String()}
	if verr := checkRefPolicy([]refCommand{cmd},
		defaultBranchOf(ctx, o.s.State.storage.absRepoPath(t.org, t.project, repo)), ""); verr != nil {
		return "", zip.ErrBadRequest(verr.Error())
	}

	// Compare-and-set, not set: base is read above and written here, and in
	// between it can move under a concurrent push. Handing go-git the value we
	// judged makes the write FAIL rather than silently discard that push.
	if err := st.CheckAndSetReference(plumbing.NewHashReference(baseName, head.Hash()), base); err != nil {
		if errors.Is(err, gogitstorage.ErrReferenceHasChanged) {
			return "", zip.ErrConflict("base branch moved while merging and nothing was written; read the pull request again and retry")
		}
		return "", internalErr(fmt.Errorf("advance %s: %w", p.Base, err))
	}

	// Same side effects as any other push that lands on this branch: meter the
	// storage and fire the reactions, so a merge deploys exactly as a push to
	// base does.
	bg := context.WithoutCancel(ctx)
	recordUsage(o.s, bg, t.org, t.project, repo)
	fireBranchBuild(o.s, bg, t.org, t.project, repo, p.Base, cmd.Old, cmd.New, t.user)
	return cmd.New, nil
}
