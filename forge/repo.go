package forge

// repo.go is the repository itself: whether it is there, whether this person may
// write it, and what its branches point at.
//
// These are the facts a run needs before it starts and after it finishes, and
// they are reads OF THE FORGE for the same reason everything else here is — the
// forge holds the refs, so it is the only thing that can say what a ref is. A
// second answer read from a copy is how a run files a pull request for a branch
// nobody can fetch.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Writable is the forge's own verdict that this client's ACTOR may push to
// org/repo, and it returns the repository so a caller does not read it twice.
//
// It is deliberately a read AS THE HUMAN. The alternative — checking a table
// here, or trusting that the caller already knows — makes this package's
// judgement the authority on a permission the forge owns, and the two disagree
// the moment somebody is removed from a team. Sudo means the answer comes from
// the forge's ACL applied to that person.
//
// A repository the actor cannot SEE is 404, which surfaces as [ErrUnknownActor]
// and is the same answer as one that does not exist. That is right: it declines
// to tell a caller that a private repository is there.
func (c *Client) Writable(ctx context.Context, owner, repo string) (Repo, error) {
	r, err := c.Repo(ctx, owner, repo)
	if err != nil {
		return Repo{}, err
	}
	if r.Perm == nil || !r.Perm.Push {
		// Named separately from "cannot see it": this person is on the repository
		// and may read it, and the thing they may not do is write. Collapsing the
		// two would tell a reader to ask for access they already have.
		return Repo{}, fmt.Errorf("forge: %s cannot push to %s/%s", c.actor, owner, repo)
	}
	if r.Archived {
		return Repo{}, fmt.Errorf("forge: %s/%s is archived", owner, repo)
	}
	return r, nil
}

// Ensure returns org/repo, creating it under the actor when it is not there.
//
// It exists because the two hosts were being migrated one repository at a time,
// so a repository that is real to a caller may simply not have arrived yet — and
// a run pointed at one that has not gets a 404 from `git clone`, minutes after
// it was admitted, with a session already open and a sandbox already leased.
//
// The repository is born WITH A COMMIT (auto_init). An empty repository clones
// successfully and then has no HEAD, so the run's first `rev-parse` fails and
// the failure reads as a broken sandbox rather than as an empty repository.
//
// A 409 is SUCCESS. Two runs admitted at once for a repository that does not
// exist yet both try to create it; one wins, and the loser wanted exactly what
// the winner made.
func (c *Client) Ensure(ctx context.Context, owner, repo, description string) (Repo, error) {
	if err := validOrg(owner); err != nil {
		return Repo{}, err
	}
	if err := validOrg(repo); err != nil {
		return Repo{}, fmt.Errorf("forge: repo: %w", err)
	}
	got, err := c.Writable(ctx, owner, repo)
	if err == nil {
		return got, nil
	}
	// Only absence justifies creating one. A repository that IS there and that
	// this actor may not write must stay a refusal — creating past it would let a
	// caller make a repository whose name is already taken by one they cannot see.
	if !isMissing(err) {
		return Repo{}, err
	}
	var made Repo
	cerr := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/orgs/" + url.PathEscape(owner) + "/repos",
		body: map[string]any{
			"name":           repo,
			"description":    description,
			"private":        true, // a repository is born closed; opening it is a decision
			"auto_init":      true,
			"default_branch": "main",
		},
		out: &made,
	})
	if cerr != nil && !errors.Is(cerr, ErrExists) {
		return Repo{}, fmt.Errorf("forge: create %s/%s: %w", owner, repo, cerr)
	}
	// Read it back either way: on the conflict path we have no body, and on the
	// create path the response carries no permissions — and the permission is
	// the thing the caller is about to rely on.
	out, err := c.Writable(ctx, owner, repo)
	if err != nil {
		return Repo{}, err
	}
	// BORN PROTECTED. A run's key is per-repository, so the only thing standing
	// between it and the default branch is a rule the forge enforces (protect.go).
	// Applying it at creation is the one moment it changes nobody's workflow, and
	// it is what lets [Client.Grant] refuse an unprotected repository without
	// refusing every new one.
	if perr := c.Protect(ctx, owner, repo, out.Branch); perr != nil {
		return Repo{}, perr
	}
	return out, nil
}

// Tip is the commit a branch points at, and whether the branch is there at all.
//
// found is carried separately from the sha because the two absences must not be
// spelled the same way: a branch that is missing and a read that failed both
// have to fail the integrity gate this feeds, and an absent tip arriving as a
// present-but-empty one would file a pull request for a branch nobody can fetch.
//
// It reads as the MACHINE. The question is not "may this person see this branch"
// — it is "did the push this platform dispatched actually land", and answering
// it through the human's visibility would make a permissions change look like a
// failed push.
func (c *Client) Tip(ctx context.Context, owner, repo, branch string) (sha string, found bool, err error) {
	if err := validOrg(owner); err != nil {
		return "", false, err
	}
	if err := validOrg(repo); err != nil {
		return "", false, fmt.Errorf("forge: repo: %w", err)
	}
	if strings.TrimSpace(branch) == "" {
		return "", false, fmt.Errorf("forge: empty branch")
	}
	// The wire is [Client.branch], which [Client.Resolve] also reads — one
	// spelling of the branch route, and the only difference between the two
	// callers is the POLICY stated here: this one is the machine's, and it reads
	// an absent branch as an answer rather than as a failure.
	id, err := c.Machine().branch(ctx, owner, repo, branch)
	switch {
	case errors.Is(err, ErrNotFound):
		return "", false, nil // a real answer: the branch is not there
	case err != nil:
		return "", false, err
	case id == "":
		return "", false, nil
	}
	return id, true, nil
}
