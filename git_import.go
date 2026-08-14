package cloud

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/plane"
)

// git_import.go is the INBOUND half of the GitHub-App bidirectional sync —
// the companion of RegisterPushBuilder / RegisterLifecycleSubscriber. The
// integrations plane (apps/integrations/github*) owns the GitHub App:
// installation-token minting, the repo list, webhook signature verification. The
// SYNC engine (apps/sync) owns the other half: creating the repository on the
// forge, advancing an upstream into it fast-forward-only, and remembering what
// that advance did. apps/integrations MUST NOT import apps/sync (sync already
// imports integrations for token custody), so sync registers a GitImporter here
// at Mount and integrations calls the package funcs below. The installation
// token flows THROUGH as a field — minted per call, never stored, never logged
// (values, not places).
//
// It used to be the embedded git object plane that answered these, against bare
// repositories on this fleet's own disks. The forge holds them now; the seam did
// not move, only what is behind it.

// GitImportReq imports one external repo onto the forge: create the repo if
// absent, then advance every ref from CloneURL onto it — FAST-FORWARD ONLY —
// using the short-lived installation Token. Idempotent: a re-import advances
// whatever has moved and touches nothing else. When MirrorURL is non-empty an
// outbound target is registered, and the same refs are advanced out to it.
type GitImportReq struct {
	Org, Project, Repo string
	CloneURL           string // https://github.com/<owner>/<repo>.git (we construct it)
	Token              string // installation access token; env-only downstream, never argv/logs
	MirrorURL          string // outbound target to register; "" ⇒ don't register
}

// GitInboundReq fast-forward-only advances ONE ref from an upstream push (a
// signature-verified webhook). The FORGE is CANONICAL: the push that carries the
// update is non-forcing, so it NEVER overwrites a forge ref — a divergence is
// reported as a Conflict and the forge is left unchanged (the split-brain
// guard). Origin stamps the source host so an outbound target on that same host
// suppresses the echo (loop prevention).
type GitInboundReq struct {
	Org, Project, Repo string
	Ref                string // FULL ref, e.g. refs/heads/main or refs/tags/v1.2.3
	CloneURL           string
	Token              string
	Origin             string // source host, e.g. "github.com"
}

// GitSyncResult is the outcome of an inbound fast-forward.
type GitSyncResult struct {
	Applied  bool   // the forge advanced (fast-forward); a push.landed was emitted
	NoOp     bool   // already up to date (tip equal — the loop echo) or not imported
	Conflict bool   // the forge diverged and was NOT overwritten (split-brain guard)
	Detail   string // human reason (conflict / skip)
	Before   string // the forge's tip before (set on Applied)
	After    string // the forge's tip after (set on Applied)
}

// GitRepoStatus is the per-repo import + sync status for the console repo list.
type GitRepoStatus struct {
	Imported     bool  // the forge holds a repo of this name
	Conflict     bool  // a ref diverged on a prior advance (the forge was preserved)
	LastSyncedAt int64 // unix seconds of the last advance (0 = never)
}

// GitImporter is what apps/sync registers at Mount.
type GitImporter interface {
	ImportRepo(ctx context.Context, req GitImportReq) error
	InboundSync(ctx context.Context, req GitInboundReq) (GitSyncResult, error)
	RepoStatus(ctx context.Context, org, project string, names []string) (map[string]GitRepoStatus, error)
}

// gitImporter is the registered importer. apps/sync installs it in Mount;
// apps/integrations calls the package funcs below. The single inversion point
// that lets the integrations plane drive a sync with no integrations⇄sync import
// cycle — the same idiom as pushBuilder.
var gitImporter GitImporter

// RegisterGitImporter installs the importer. nil-safe.
func RegisterGitImporter(g GitImporter) { gitImporter = g }

// ErrGitImporterUnavailable is returned when the sync engine is not mounted
// (no importer registered) — a fail-closed sentinel, never a silent success.
var ErrGitImporterUnavailable = errors.New("cloud: git importer not registered")

// ImportGitRepo creates the repo on the forge and advances an upstream into it.
func ImportGitRepo(ctx context.Context, req GitImportReq) error {
	if gitImporter != nil {
		return gitImporter.ImportRepo(ctx, req)
	}
	// Not co-resident: this process is not the one that answers for a sync, so the
	// in-process call is nil and the request travels the internal plane instead.
	// Every subsystem runs as its own process, so the app that DECIDES to import
	// (integrations, holding the provider credential) is never the app that runs
	// the advance — an import answered "git importer not registered" while both
	// were healthy. The org is not sent: the callee reads it from the plane
	// identity, so an argument can never widen the tenant an import lands in.
	if _, err := Ask[plane.ImportIn, plane.Imported](ctx, "sync", plane.GitImport, &plane.ImportIn{
		Repo:      req.Repo,
		Project:   req.Project,
		CloneURL:  req.CloneURL,
		Token:     req.Token,
		MirrorURL: req.MirrorURL,
	}); err != nil {
		return err
	}
	return nil
}

// InboundGitSync fast-forward-only advances one forge ref from an upstream
// push. Never force-overwrites the forge; a divergence returns Conflict.
func InboundGitSync(ctx context.Context, req GitInboundReq) (GitSyncResult, error) {
	if gitImporter != nil {
		return gitImporter.InboundSync(ctx, req)
	}
	// Not co-resident — the app that receives the push is not the app that runs
	// the advance, so the request travels the plane. See ImportGitRepo.
	out, err := Ask[plane.InboundIn, plane.Synced](ctx, "sync", plane.GitInbound, &plane.InboundIn{
		Project: req.Project, Repo: req.Repo, Ref: req.Ref,
		CloneURL: req.CloneURL, Token: req.Token, Origin: req.Origin,
	})
	if err != nil {
		return GitSyncResult{}, err
	}
	return GitSyncResult{
		Applied: out.Applied, NoOp: out.NoOp, Conflict: out.Conflict,
		Detail: out.Detail, Before: out.Before, After: out.After,
	}, nil
}

// GitRepoStatuses returns the per-repo import + sync status for names (org-scoped).
//
// Absent co-residency it asks the sync app, like the two calls above. It used to
// return ErrGitImporterUnavailable here, which was honest about this process and
// wrong about the world: the statuses exist, in the process next door, and the
// console repo list rendered every repo as never-imported because the app that
// draws the list is not the app that answers for the sync.
//
// A name with nothing to say is simply MISSING from the reply, and stays missing
// from the map — the same answer the in-process importer gives, so the two legs
// cannot be told apart by their result.
func GitRepoStatuses(ctx context.Context, org, project string, names []string) (map[string]GitRepoStatus, error) {
	if gitImporter != nil {
		return gitImporter.RepoStatus(ctx, org, project, names)
	}
	out, err := Ask[plane.StatusIn, plane.Statuses](For(ctx, org), "sync", plane.GitStatus,
		&plane.StatusIn{Project: project, Names: names})
	if err != nil {
		return nil, err
	}
	st := make(map[string]GitRepoStatus, len(out.Rows))
	for _, r := range out.Rows {
		st[r.Name] = GitRepoStatus{
			Imported:     r.Imported,
			Conflict:     r.Conflict,
			LastSyncedAt: r.LastSyncedAt,
		}
	}
	return st, nil
}
