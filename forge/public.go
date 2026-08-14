package forge

// public.go is the one bit of a repository the platform sets on somebody's
// behalf: whether a reader who is not on it may read it.
//
// Everything else here answers a question. This CHANGES a fact, and it is the
// change with the shortest fuse — a repository opened by mistake is opened to
// everyone at once, and a repository that stays open after it was meant to close
// is a leak that nobody gets a notification about. So this file is written
// around the closing direction, and the opening one comes along for free.
//
// # The answer is read back, not inferred from a status code
//
// A 200 on the PATCH says the forge accepted the request. It does not say the
// repository is now private: a field the fork does not accept is IGNORED rather
// than refused (Forgejo's EditRepoOption binds what it knows and drops what it
// does not), an empty body decodes into a zero value that reads as "public", and
// a proxy in front of the forge can answer for it. Each of those is a silent
// false success in the ONE direction where a false success is a leak.
//
// So the verdict comes from a fresh read of the repository, and the call fails
// unless the forge itself says the visibility is the one that was asked for. It
// costs one GET on a path that runs when a person changes a setting, which is
// the cheapest possible place to spend a round trip.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// SetPublic opens org/repo to anonymous readers, or closes it, and CONFIRMS the
// result against the forge.
//
// The wire field is `private`, so this sends its negation: Forgejo's repository
// edit takes the same `private` boolean the create path takes, which is why the
// two can be written in terms of one another and why [Client.Ensure] can be the
// only way a repository comes into being here — born closed, opened by this.
//
// It is IDEMPOTENT. Setting the visibility a repository already has is a
// successful no-op, which is what lets a caller fire it on every write of the
// row it derives from instead of tracking transitions — and tracking transitions
// is precisely how a repository ends up left open, because the transition that
// gets missed is the one nobody noticed happening.
//
// It acts as THIS CLIENT'S identity, machine or sudoed, and does not quietly
// escalate to the machine to make a refused change go through: a caller that
// wants the platform's authority asks for it with [Client.Machine], in writing.
// The read-back is made the same way for the same reason — a confirmation read
// by a stronger identity than the write would confirm something the writer could
// not have done.
func (c *Client) SetPublic(ctx context.Context, owner, repo string, public bool) error {
	if err := validOrg(owner); err != nil {
		return err
	}
	if err := validOrg(repo); err != nil {
		return fmt.Errorf("forge: repo: %w", err)
	}
	private := !public
	if err := c.send(ctx, sendOpts{
		method: http.MethodPatch,
		path:   "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo),
		body:   map[string]any{"private": private},
	}); err != nil {
		return fmt.Errorf("forge: visibility %s/%s: %w", owner, repo, err)
	}
	// The confirmation, and the whole point of the method. A read of the
	// repository AFTER the write is the only statement about its visibility that
	// comes from the thing that enforces it.
	got, err := c.Repo(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("forge: confirm %s/%s: %w", owner, repo, err)
	}
	if got.Private != private {
		return fmt.Errorf("forge: %s/%s did not change: private=%v, asked for %v",
			owner, repo, got.Private, private)
	}
	return nil
}
