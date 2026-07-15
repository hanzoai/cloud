package cloud

import (
	"context"
	"errors"
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
	Branch             string // short branch name (refs/heads/<Branch>)
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
	if gitImporter == nil {
		return ErrGitImporterUnavailable
	}
	return gitImporter.ImportRepo(ctx, req)
}

// InboundGitSync fast-forward-only advances one native branch from an upstream
// push. Never force-overwrites native; a divergence returns Conflict.
func InboundGitSync(ctx context.Context, req GitInboundReq) (GitSyncResult, error) {
	if gitImporter == nil {
		return GitSyncResult{}, ErrGitImporterUnavailable
	}
	return gitImporter.InboundSync(ctx, req)
}

// GitRepoStatuses returns the per-repo import + sync status for names (org-scoped).
func GitRepoStatuses(ctx context.Context, org, project string, names []string) (map[string]GitRepoStatus, error) {
	if gitImporter == nil {
		return nil, ErrGitImporterUnavailable
	}
	return gitImporter.RepoStatus(ctx, org, project, names)
}
