package cloud

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/plane"
)

// sync_seam.go is the inversion layer between the universal sync ENGINE
// (clients/sync) and the two planes that trigger or execute it — the webhook
// triggers (clients/integrations GitHub App, clients/git Hanzo Git ingest) and the git
// object plane (clients/git). It is the SAME idiom as git_import.go's GitImporter:
// the engine registers itself here at Mount; triggers call the package funcs below
// with NO import of the engine package, so nothing imports the sync package except apps
// (which mounts it). One seam, one direction, no cycles.

// SyncEvent is a provider-agnostic sync trigger. A webhook (GitHub push, Hanzo Git
// push) or a manual run builds one and hands it to the registered engine via Sync;
// the engine resolves the Syncs whose SOURCE matches (Provider, Locator/Repo)
// for the org and applies each. Flat + string-typed so it crosses the trigger→
// engine seam without importing the engine.
type SyncEvent struct {
	Kind     string // sync kind, e.g. "git"
	Provider string // endpoint the event came from: "github" | "gitlab" | "hanzo-git"
	Org      string // tenant (from the signed installation / gateway identity)
	Locator  string // source repo locator (a clone URL, or "<org>/<repo>")
	Repo     string // short repo name (git)
	Ref      string // FULL ref: refs/heads/<branch> or refs/tags/<tag>
	Before   string
	After    string
	Actor    string // who made the upstream push — the loop guard compares it to the sync's own Actor
	Token    string // OPTIONAL short-lived credential the trigger already minted (git: installation token); never logged
	Manual   bool   // a manual /run or an initial sync reconcile (no specific push)
	Hop      int    // chained-propagation depth (bounded by the engine's hop limit)
}

// SyncResult is the outcome of dispatching one SyncEvent across the resolved syncs.
type SyncResult struct {
	Ran     int // syncs that reconciled a change
	Skipped int // syncs resolved but skipped (loop guard / idempotent / direction off)
}

// SyncFunc is the reconcile entry the universal sync engine registers — a function,
// not an engine noun. The one implementation (clients/sync) registers it at
// Mount; triggers reach it via Sync.
type SyncFunc func(ctx context.Context, ev SyncEvent) (SyncResult, error)

// syncFn is the registered reconcile func (nil until the sync engine mounts). Read on the
// hot webhook path, written once at Mount — a plain var is fine (Mount happens
// before serving, like RegisterGitImporter).
var syncFn SyncFunc

// RegisterSync installs the sync reconcile func. nil-safe.
func RegisterSync(fn SyncFunc) { syncFn = fn }

// ErrSyncUnavailable is returned when the engine is not mounted — a fail-closed
// sentinel, never a silent success, so a trigger logs precisely rather than
// pretending it synced.
var ErrSyncUnavailable = errors.New("cloud: sync engine not registered")

// Sync dispatches one event to the registered reconcile func.
//
// Co-resident, it is a Go call. Otherwise it asks the sync app over the plane —
// because the TRIGGERS and the ENGINE are different apps and therefore different
// processes. A webhook lands on integrations, a push lands on git, and neither
// of them is where the engine runs, so the in-process seam is nil on every path
// that actually fires. It answered ErrSyncUnavailable for all of them while the
// engine was up next door: every mirror and every chained propagation silently
// stopped happening, reported as "not registered" rather than as the reachable
// call it was.
//
// ErrSyncUnavailable is kept for the case it always described honestly — a
// registration this process was supposed to have and does not.
func Sync(ctx context.Context, ev SyncEvent) (SyncResult, error) {
	if syncFn != nil {
		return syncFn(ctx, ev)
	}
	out, err := Ask[plane.SyncIn, plane.SyncRan](For(ctx, ev.Org), "sync", plane.SyncRun, &plane.SyncIn{
		Kind: ev.Kind, Provider: ev.Provider, Locator: ev.Locator, Repo: ev.Repo,
		Ref: ev.Ref, Before: ev.Before, After: ev.After, Actor: ev.Actor,
		Token: ev.Token, Manual: ev.Manual, Hop: ev.Hop,
	})
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Ran: out.Ran, Skipped: out.Skipped}, nil
}

// ── git object-plane control seam (outbound mirror ensure/remove) ─────────────

// GitMirrorController lets the sync engine's git provider ENSURE or REMOVE a
// native repo's outbound mirror target without importing clients/git (which owns
// the per-org repo store + the mirror_out reactor that does the actual pushing).
// enabled=true registers the target (idempotent); enabled=false removes it. The
// push itself stays with mirror_out on the native push lifecycle — the engine only
// declares the target, exactly the "cloud ensures the mirror exists; the git plane
// does the pushing" split.
type GitMirrorController interface {
	EnsureMirror(ctx context.Context, org, project, repo, url string, enabled bool) error
}

var gitMirrorCtl GitMirrorController

// RegisterGitMirrorController installs the git-plane mirror controller. nil-safe.
func RegisterGitMirrorController(c GitMirrorController) { gitMirrorCtl = c }

// ErrGitMirrorControllerUnavailable is returned when the git object plane is not
// mounted — fail-closed, never a silent success.
var ErrGitMirrorControllerUnavailable = errors.New("cloud: git mirror controller not registered")

// EnsureGitMirror registers (enabled) or removes (disabled) the outbound mirror
// target url on the native repo. Idempotent.
//
// The DECIDER is the sync engine and the OWNER is the git app, so absent
// co-residency this asks git over the plane rather than reporting a controller
// that was never going to be in this process. Declaring a mirror is the whole
// point of configuring one; refusing it here meant a sync that reconciled
// inbound forever and never pushed anything back.
func EnsureGitMirror(ctx context.Context, org, project, repo, url string, enabled bool) error {
	if gitMirrorCtl != nil {
		return gitMirrorCtl.EnsureMirror(ctx, org, project, repo, url, enabled)
	}
	_, err := Ask[plane.MirrorIn, plane.Mirrored](For(ctx, org), "git", plane.GitMirror,
		&plane.MirrorIn{Project: project, Repo: repo, URL: url, Enabled: enabled})
	return err
}
