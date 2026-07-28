package git

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
)

// community.go — git's half of the visibility seam.
//
// clients/projects decides whether a project may be seen (public by default,
// private is paid, hidden is moderation) and EMITS that as one fact. This
// subscriber applies it to the canonical repo at git.hanzo.ai/<org>/<slug>:
//
//	Listed  ⇒ the repo exists and allows anonymous read
//	!Listed ⇒ the repo exists and does NOT
//
// The repo is created on the FIRST event either way, so a private project still
// has somewhere for its code to live and going public later is a flag flip
// rather than a migration. That is what makes "share it" instant and, more
// importantly, what makes un-sharing instant too.
//
// ONE way a repo comes into being: provision(), the same call the REST create
// handler uses. This file adds no second construction path — an already-existing
// repo is not an error here, it is the steady state.

// publishCommunity applies one project's visibility to its canonical repo. It is
// IDEMPOTENT by construction: an existing repo is reconciled to the event rather
// than rejected, so projects can fire on every create, visibility change and
// moderation without tracking transitions. A missed transition would leave a
// private project world-readable, which is the one failure here that cannot be
// taken back — so the cheap redundant write is the right trade.
func publishCommunity(ctx context.Context, ev cloud.CommunityEvent) error {
	s := mounted.Load()
	if s == nil {
		return nil // git plane not mounted (or shutting down): nothing to apply
	}
	store, err := storeFor(s, ev.Org)
	if err != nil {
		return fmt.Errorf("community: open %s store: %w", ev.Org, err)
	}

	// Name/Description seed the repo only at creation. Re-imposing them on every
	// event would overwrite an author who edited their own repo description —
	// visibility is ours to enforce, their prose is not.
	id, err := genID("repo")
	if err != nil {
		return fmt.Errorf("community: id: %w", err)
	}
	now := time.Now().Unix()
	err = provision(s, ctx, store, Repo{
		ID: id, Org: ev.Org, Name: ev.Slug,
		Description: ev.Description, DefaultBranch: defaultBranchName,
		Public:    ev.Listed,
		CreatedAt: now, UpdatedAt: now,
	})
	switch {
	case err == nil:
		return nil // created with the right visibility already on it
	case !errors.Is(err, errConflict):
		return fmt.Errorf("community: provision %s/%s: %w", ev.Org, ev.Slug, err)
	}

	// Already there: reconcile the one field this seam owns.
	if err := store.SetPublic(ctx, ev.Org, "", ev.Slug, ev.Listed, now); err != nil {
		return fmt.Errorf("community: set visibility %s/%s: %w", ev.Org, ev.Slug, err)
	}
	return nil
}
