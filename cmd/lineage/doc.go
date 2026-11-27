// Command lineage reports whether a commit is one the branch that hands out
// cloud's version numbers has been, and on stdout says who that branch is.
//
// The release workflow runs it immediately before the compare-and-swap that
// claims a number, which is the only place a number is allocated.
//
// Usage:
//
//	lineage <commit> [branch]
//
// The branch is main unless named; dev is the other one a lane builds
// (lineage.Of), and any other is refused.
//
// Exit 0 means the arbiter reaches that commit from its release branch. Any other
// exit means it does not, or that the arbiter could not be asked; the reason goes to
// standard error in both cases, and no version number may be claimed after either.
//
// STDOUT IS THE ARBITER, AS SHELL ASSIGNMENTS, AND ONLY ON A PASS. The lane needs
// four strings to cut a release — the repository to list tags from, the address to
// claim the tag at, the branch, and the image to publish — and every one of them is
// a property of the arbiter this command just verified against. Printing them here
// is what makes them unavailable to a run that did not pass: a build cannot name the
// image without the proof, so there is no order of steps that publishes bytes under
// a number their commit was never entitled to. Held apart in the workflow they were
// five literals, and a repository rename moved what one of them meant.
//
// The lines are `key=value` because that is at once what a shell evaluates and what
// GITHUB_OUTPUT reads, so the same bytes serve this job and every job after it with
// nothing in between to translate them. The values are constants of this program.
//
// GIT_TOKEN authenticates the read — the forge credential the claim step already
// carries, read here rather than introduced under a second name, and sent by Verify
// as a header rather than URL userinfo.
package main
