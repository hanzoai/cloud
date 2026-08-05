package cloud

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/plane"
)

// git_import.go is the INBOUND half of the GitHub-App bidirectional-sync seam —
// the companion of RegisterPushBuilder / RegisterLifecycleSubscriber. The
// integrations plane (clients/integrations/github*) owns the GitHub App:
// installation-token minting, the repo list, webhook signature verification. The
// git object plane (clients/git) owns repo creation, mirror-in, and the
// fast-forward-only ref advance. clients/integrations MUST NOT import clients/git
// (git already imports integrations for token custody), so git registers a
// GitImporter here at Mount and integrations calls the package funcs below. The
// installation token flows THROUGH as a field — minted per call, never stored,
// never logged (values, not places).

// GitImportReq imports one external repo into the native git server: create the
// (Org, Project, Repo) repo if absent, then force-fetch every ref from CloneURL
// using the short-lived installation Token (mirror-in). Idempotent — a re-import
// is a re-fetch. When MirrorURL is non-empty an outbound mirror target is
// registered so a later native push force-safe-mirrors back to the same remote.
type GitImportReq struct {
	Org, Project, Repo string
	CloneURL           string // https://github.com/<owner>/<repo>.git (we construct it)
	Token              string // installation access token; env-only downstream, never argv/logs
	MirrorURL          string // outbound target to register; "" ⇒ don't register
}

// GitInboundReq fast-forward-only advances ONE branch from an upstream push (a
// signature-verified webhook). Native is CANONICAL: the fetch NEVER force-
// overwrites a native ref — a divergence is reported as a Conflict and native is
// left unchanged (the split-brain guard). Origin stamps the source host so the
// outbound mirror suppresses the echo (loop prevention).
type GitInboundReq struct {
	Org, Project, Repo string
	Ref                string // FULL ref, e.g. refs/heads/main or refs/tags/v1.2.3
	CloneURL           string
	Token              string
	Origin             string // source host, e.g. "github.com"
}

// GitSyncResult is the outcome of an inbound fast-forward.
type GitSyncResult struct {
	Applied  bool   // native advanced (fast-forward); a push.landed was emitted
	NoOp     bool   // already up to date (tip equal — the loop echo) or not imported
	Conflict bool   // native diverged; native was NOT overwritten (split-brain guard)
	Detail   string // human reason (conflict / skip)
	Before   string // native tip before (set on Applied)
	After    string // native tip after (set on Applied)
}

// GitRepoStatus is the per-repo import + sync status for the console repo list.
type GitRepoStatus struct {
	Imported     bool  // a native repo exists for this name
	Conflict     bool  // a branch diverged on a prior inbound sync (native preserved)
	LastSyncedAt int64 // unix seconds of the last import/sync (0 = never)
}

// GitImporter is the git object-plane seam clients/git registers at Mount.
type GitImporter interface {
	ImportRepo(ctx context.Context, req GitImportReq) error
	InboundSync(ctx context.Context, req GitInboundReq) (GitSyncResult, error)
	RepoStatus(ctx context.Context, org, project string, names []string) (map[string]GitRepoStatus, error)
}

// gitImporter is the registered git object-plane importer. clients/git installs
// it in Mount; clients/integrations calls the package funcs below. The single
// inversion point that lets the integrations plane drive the embedded git server
// with no integrations⇄git import cycle — the same idiom as pushBuilder.
var gitImporter GitImporter

// RegisterGitImporter installs the git object-plane importer. nil-safe.
func RegisterGitImporter(g GitImporter) { gitImporter = g }

// ErrGitImporterUnavailable is returned when the git object plane is not mounted
// (no importer registered) — a fail-closed sentinel, never a silent success.
var ErrGitImporterUnavailable = errors.New("cloud: git importer not registered")

// ImportGitRepo creates + mirrors an external repo into the native git server.
func ImportGitRepo(ctx context.Context, req GitImportReq) error {
	if gitImporter != nil {
		return gitImporter.ImportRepo(ctx, req)
	}
	// Not co-resident: this process is not the one that owns the git store, so the
	// in-process seam is nil and the request travels the internal plane instead.
	// Every subsystem runs as its own process, so the app that DECIDES to import
	// (integrations, holding the provider credential) is never the app that holds
	// the repos — an import answered "git importer not registered" while both were
	// healthy. The org is not sent: the callee reads it from the plane identity, so
	// an argument can never widen the tenant an import lands in.
	if _, err := Ask[plane.ImportIn, plane.Imported](ctx, "git", plane.GitImport, &plane.ImportIn{
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

// InboundGitSync fast-forward-only advances one native branch from an upstream
// push. Never force-overwrites native; a divergence returns Conflict.
func InboundGitSync(ctx context.Context, req GitInboundReq) (GitSyncResult, error) {
	if gitImporter != nil {
		return gitImporter.InboundSync(ctx, req)
	}
	// Not co-resident — the app that receives the push is not the app that holds
	// the repos, so the request travels the plane. See ImportGitRepo.
	out, err := Ask[plane.InboundIn, plane.Synced](ctx, "git", plane.GitInbound, &plane.InboundIn{
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
// Absent co-residency it asks the git app, like the two calls above. It used to
// return ErrGitImporterUnavailable here, which was honest about the seam and
// wrong about the world: the statuses exist, in the process next door, and the
// console repo list rendered every repo as never-imported because the app that
// draws the list is not the app that owns the repos.
//
// A name git knows nothing about is simply MISSING from the reply, and stays
// missing from the map — the same answer the in-process importer gives, so the
// two legs cannot be told apart by their result.
func GitRepoStatuses(ctx context.Context, org, project string, names []string) (map[string]GitRepoStatus, error) {
	if gitImporter != nil {
		return gitImporter.RepoStatus(ctx, org, project, names)
	}
	out, err := Ask[plane.StatusIn, plane.Statuses](For(ctx, org), "git", plane.GitStatus,
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
