package cloud

import "context"

// community.go — the seam that makes a published project browsable.
//
// A project's visibility is decided in clients/projects (public by default,
// private is paid, hidden is moderation). Its SOURCE lives in the canonical git
// plane (clients/git, git.hanzo.ai). Those are two packages, and projects must
// not import git to tell it what changed — so visibility is EMITTED as a fact
// and git subscribes to it, the same inversion as RegisterServiceReleaser and
// pushBuilder.
//
// One fact, one direction. projects never learns what a repo is; git never
// learns what a plan costs. What crosses the seam is only "this project is
// visible to the world, or it is not".

// Visibility is one project's visibility as the world should see it.
//
//   - Org/Slug identify the project, and Org is also its AUTHORSHIP: the account
//     that pays for it. There is no separate author field because there is no
//     second copy of that fact.
//   - Listed is the resolved answer to "may a stranger see this" — public AND
//     not moderated. The subscriber never re-derives it from parts, so the rule
//     lives in exactly one place (projects.Project.listed).
//   - Name/Description seed the repo the first time it is created; they are
//     never re-imposed afterwards, so an author who edits their own README or
//     repo description keeps it.
type Visibility struct {
	Org         string
	Slug        string
	Name        string
	Description string
	Listed      bool
}

// publisher is the registered visibility subscriber. clients/git (the
// owner of the canonical repo plane) installs it in Mount. Exactly one
// registration, like every other seam in this file's family.
var publisher func(ctx context.Context, ev Visibility) error

// RegisterPublisher installs the visibility subscriber. clients/git
// calls this from its Mount when co-resident; it is the ONE inversion point that
// lets a project's visibility reach its repo with no projects⇄git import cycle.
func RegisterPublisher(f func(ctx context.Context, ev Visibility) error) {
	publisher = f
}

// PublisherRegistered reports whether the canonical git plane is
// co-resident. A caller uses it to be honest about whether a publish actually
// reached a repo or was a no-op in a binary that does not host git.
func PublisherRegistered() bool { return publisher != nil }

// Publish applies a project's visibility to its canonical repo. It is
// a no-op when nothing is registered (a binary without the git plane), and it is
// deliberately IDEMPOTENT at the subscriber: callers fire it on every create,
// visibility change and moderation rather than trying to detect transitions,
// because a missed transition leaves a private project world-readable and no
// caller-side diffing is worth that risk.
func Publish(ctx context.Context, ev Visibility) error {
	if publisher == nil {
		return nil
	}
	return publisher(ctx, ev)
}
