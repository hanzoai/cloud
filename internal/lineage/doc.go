// Package lineage answers one question about one commit: was it ever a state of the
// branch that hands out version numbers?
//
// A version number is only meaningful inside one lineage. "v1.801.551" means the
// commit after v1.801.550 along ONE line of development; the same string laid over
// an unrelated history names nothing, because the ordering that gives it meaning
// does not exist there. So the register that allocates those numbers — the arbiter
// — may only allocate one to a commit its release branch has been.
//
// STATES OF THE BRANCH, NOT EVERYTHING REACHABLE FROM IT. Those are different sets
// and the difference is the whole guard. Merging a history into the branch makes
// every commit in it an ancestor, and an ancestor test would then admit all of them
// — permanently, from one ordinary merge that fast-forwards and needs no force. But
// a branch never WAS those commits: it went from the commit before the merge
// straight to the merge itself, and what the merge absorbed was never a state it
// passed through. `rev-list --first-parent` is exactly that sequence of states, so
// it is exactly the set a number may name.
//
// The question goes to git, because ancestry IS the commit graph and git is what
// computes over it. A `--filter=tree:0` fetch brings back that branch's commits and
// nothing else — no trees, no blobs — which is all either walk reads: a few
// megabytes and about a second for a repository of tens of thousands of commits,
// cheap enough to ask on every claim.
//
// Asking git also keeps the answer independent of what anything CALLS the commit or
// the repository. A clone URL, a repository slug and a checkout directory are all
// names, and a forge can answer one name with a different repository; the shape of
// the graph is a property of the objects themselves and cannot be renamed into being.
package lineage
