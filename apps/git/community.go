package git

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// community.go — git's half of the visibility seam.
//
// clients/projects decides what a project's source must BE (public by default,
// private is paid, hidden is moderation, deleted is deleted) and EMITS that as
// one fact. This subscriber applies it to BOTH copies git holds — the repo at
// git.hanzo.ai/<org>/<slug> and its GitHub replica:
//
//	Open ⇒ the repo exists and allows anonymous read
//	Shut ⇒ the repo exists and does NOT
//	Gone ⇒ the repo does not exist, and neither does the replica
//
// A repo is created on the first Open or Shut either way, so a private project
// still has somewhere for its code to live and going public later is a flag flip
// rather than a migration. That is what makes "share it" instant and, more
// importantly, what makes un-sharing instant too.
//
// GONE IS A DELETE, not the closure that used to stand in for it. A repo is
// found by NAME, so one left behind by a deleted project is one the next project
// of that name adopts — commits and all — and the first thing that project does
// is publish. Closing it only moves the leak to whoever reclaims the slug.
//
// ONE way a repo comes into being: provision(), the same call the REST create
// handler uses; and one way it stops, coreDelete(), the same call the REST
// delete handler uses. This file adds no second construction path — an
// already-existing repo is not an error here, it is the steady state.

// publish applies one project's resolved visibility to the two copies git holds.
// It is IDEMPOTENT by construction: an existing repo is reconciled to the event
// rather than rejected and a missing one is already gone, so projects can fire
// on every create, visibility change, moderation and delete without tracking
// transitions. A missed transition would leave a private project world-readable,
// which is the one failure here that cannot be taken back — so the cheap
// redundant write is the right trade.
func publish(ctx context.Context, org string, ev plane.Visibility) error {
	s := mounted.Load()
	if s == nil {
		return nil // git plane not mounted (or shutting down): nothing to apply
	}
	// The name is spelled ONCE, here, through the same normalisation and the same
	// alphabet the REST surface goes through. This op is its own trust boundary —
	// the caller across the socket is one of our own processes, not a reason to
	// take a name unread — and a create and a delete that normalised differently
	// would address two different repos, which is a delete that never deletes.
	slug := normalizeName(ev.Slug)
	if !nameRE.MatchString(slug) {
		return fmt.Errorf("community: %s/%q is not a repository name", org, ev.Slug)
	}
	if ev.State == plane.Gone {
		return retire(s, ctx, org, slug)
	}
	store, err := storeFor(s, org)
	if err != nil {
		return fmt.Errorf("community: open %s store: %w", org, err)
	}
	// Open is named and everything else is CLOSED, rather than the other way
	// round: a state this does not recognise — an empty one, a newer peer's — is
	// answered with the readable bit off, which is the half that can be taken
	// back.
	listed := ev.State == plane.Open

	// Name/Description seed the repo only at creation. Re-imposing them on every
	// event would overwrite an author who edited their own repo description —
	// visibility is ours to enforce, their prose is not.
	id := mint.ID("repo")
	now := time.Now().Unix()
	err = provision(s, ctx, store, Repo{
		ID: id, Org: org, Name: slug,
		Description: ev.Description, DefaultBranch: defaultBranchName,
		Public:    listed,
		CreatedAt: now, UpdatedAt: now,
	})
	switch {
	case err == nil:
		// Created with the right visibility already on it; still attach (or skip)
		// the replica, so a brand-new public project is mirrored like any other.
		return mirror(ctx, org, slug, ev.Description, listed)
	case !errors.Is(err, errConflict):
		return fmt.Errorf("community: provision %s/%s: %w", org, slug, err)
	}

	// Already there: reconcile the one field this seam owns.
	if err := store.SetPublic(ctx, org, "", slug, listed, now); err != nil {
		return fmt.Errorf("community: set visibility %s/%s: %w", org, slug, err)
	}
	return mirror(ctx, org, slug, ev.Description, listed)
}

// retire takes a deleted project's source off BOTH copies.
//
// Both are attempted whatever the other answers, and the failures are reported
// together: they are two independent readable copies, and skipping one because
// the other refused would leave the project's source readable at the very moment
// there is no row left to say it must not be. The caller retries, which is why
// each half reads an absence as the state it asked for.
func retire(s *cloud.Service[state], ctx context.Context, org, slug string) error {
	local := coreDelete(s, ctx, org, "", slug)
	if errors.Is(local, errNotFound) {
		local = nil // absence is the state asked for, and what a retry finds
	}
	if local != nil {
		local = fmt.Errorf("community: delete %s/%s: %w", org, slug, local)
	}
	return errors.Join(local, remove(ctx, org, slug))
}

// mirror gives the project a REAL GitHub repo under the community org
// and keeps its visibility in step with the canonical one, so a public project
// is public in both places and a private one is private in both.
//
// Visibility lives on the REPO, not on the mirror registration, so the mirror
// stays enabled either way and a project that goes private keeps receiving its
// own pushes — just where nobody else can read them. Deregistering instead would
// silently stop replicating, and the day it went public again the GitHub side
// would be stale by however long it was private. A DELETED project is the other
// case entirely and does not come here: there is no project left to keep in
// step, so the replica goes with it ([remove]).
//
// The registration itself routes through gitMirrorController.EnsureMirror — the
// SAME idempotent, host-allowlisted path the sync engine and the /mirror
// endpoint use — so there is one outbound target list and no second way to add
// to it. No credential ⇒ ensure returns "" and the whole replica is
// skipped, rather than registering a push that could never land.
func mirror(ctx context.Context, org, slug, description string, listed bool) error {
	url, err := ensure(ctx, org, slug, description, listed)
	if err != nil {
		return err
	}
	if url == "" {
		return nil
	}
	if err := (gitMirrorController{}).EnsureMirror(ctx, org, "", slug, url, true); err != nil {
		return fmt.Errorf("community: mirror %s/%s: %w", org, slug, err)
	}
	return nil
}

// exposePublish serves the visibility method on the internal plane.
//
// This replaces cloud.RegisterPublisher. The seam resolved in-process, and each
// app is its own process now (cmd/cloud "mounts every subsystem as its own
// process"), so projects' call reached a nil publisher and returned silently —
// public projects stopped getting repos the day the fleet split, with nothing
// to see. A socket call cannot fail that way: git either answers or the caller
// gets an error naming the app it could not reach.
func exposePublish() {
	zip.Post[plane.Visibility, struct{}](cloud.Plane(), "/git/publish", planePublish,
		zip.WithOperationID(plane.GitPublish),
		zip.WithSummary("Reconcile a project's repo visibility"))
}

// planePublish reconciles a project's canonical repo to the project's published
// visibility: it provisions the repo on first publish and thereafter flips only
// the public bit, then keeps the GitHub replica's visibility in step.
// Idempotent, so projects can fire it on every create, visibility change and
// moderation event. The org is the CALLER's plane identity, never the argument —
// a caller that could name the org would be publishing into another tenant's
// repos — and an anonymous caller is refused. A named handler, not a closure, so
// zipdoc can lift this prose into the registry.
func planePublish(ctx context.Context, ev *plane.Visibility) (*struct{}, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("git publish: org required")
	}
	return nil, publish(ctx, org, *ev)
}
