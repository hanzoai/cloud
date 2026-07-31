package git

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
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

// publish applies one project's visibility to its canonical repo. It is
// IDEMPOTENT by construction: an existing repo is reconciled to the event rather
// than rejected, so projects can fire on every create, visibility change and
// moderation without tracking transitions. A missed transition would leave a
// private project world-readable, which is the one failure here that cannot be
// taken back — so the cheap redundant write is the right trade.
func publish(ctx context.Context, org string, ev plane.Visibility) error {
	s := mounted.Load()
	if s == nil {
		return nil // git plane not mounted (or shutting down): nothing to apply
	}
	store, err := storeFor(s, org)
	if err != nil {
		return fmt.Errorf("community: open %s store: %w", org, err)
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
		ID: id, Org: org, Name: ev.Slug,
		Description: ev.Description, DefaultBranch: defaultBranchName,
		Public:    ev.Listed,
		CreatedAt: now, UpdatedAt: now,
	})
	switch {
	case err == nil:
		// Created with the right visibility already on it; still attach (or skip)
		// the replica, so a brand-new public project is mirrored like any other.
		return mirror(ctx, org, ev)
	case !errors.Is(err, errConflict):
		return fmt.Errorf("community: provision %s/%s: %w", org, ev.Slug, err)
	}

	// Already there: reconcile the one field this seam owns.
	if err := store.SetPublic(ctx, org, "", ev.Slug, ev.Listed, now); err != nil {
		return fmt.Errorf("community: set visibility %s/%s: %w", org, ev.Slug, err)
	}
	return mirror(ctx, org, ev)
}

// mirror gives the project a REAL GitHub repo under the community org
// and keeps its visibility in step with the canonical one, so a public project
// is public in both places and a private one is private in both.
//
// Visibility lives on the REPO, not on the mirror registration, so the mirror
// stays enabled either way and a project that goes private keeps receiving its
// own pushes — just where nobody else can read them. Deregistering instead would
// silently stop replicating, and the day it went public again the GitHub side
// would be stale by however long it was private.
//
// The registration itself routes through gitMirrorController.EnsureMirror — the
// SAME idempotent, host-allowlisted path the sync engine and the /mirror
// endpoint use — so there is one outbound target list and no second way to add
// to it. No credential ⇒ ensure returns "" and the whole replica is
// skipped, rather than registering a push that could never land.
func mirror(ctx context.Context, org string, ev plane.Visibility) error {
	url, err := ensure(ctx, org, ev.Slug, ev.Description, ev.Listed)
	if err != nil {
		return err
	}
	if url == "" {
		return nil
	}
	if err := (gitMirrorController{}).EnsureMirror(ctx, org, "", ev.Slug, url, true); err != nil {
		return fmt.Errorf("community: mirror %s/%s: %w", org, ev.Slug, err)
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
