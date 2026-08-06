package git

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// import_plane.go carries a repo import across a PROCESS boundary.
//
// The app that decides to import is integrations: it holds the provider
// connection and can mint the credential. The app that owns the git store is
// this one. They are different processes, so cloud.RegisterGitImporter — which
// only registers in-process — leaves gitImporter nil on the integrations side,
// and every import answered "git importer not registered" while both apps were
// healthy. The same shape as the KMS assertion: code that assumed co-residence.
//
// The request travels instead. The socket has already decided who may ask (0600,
// SO_PEERCRED), and the tenant comes from the caller's plane identity rather than
// the argument, so an app acting for one org cannot import into another's.

// exposeImport publishes the import on the internal plane. Mount calls it.
//
// All three GitImporter methods are published here because they are ONE
// boundary: the same integrations/git split answers import, inbound and status.
// Splitting the read into its own file would say they are different boundaries,
// and they are not.
func exposeImport() {
	zip.Post[plane.ImportIn, plane.Imported](cloud.Plane(), "/git/import", planeImport,
		zip.WithOperationID(plane.GitImport),
		zip.WithSummary("Create a repo and mirror an upstream into it"))

	zip.Post[plane.InboundIn, plane.Synced](cloud.Plane(), "/git/inbound", planeInbound,
		zip.WithOperationID(plane.GitInbound),
		zip.WithSummary("Advance one branch from an upstream push"))

	zip.Post[plane.StatusIn, plane.Statuses](cloud.Plane(), "/git/status", planeStatus,
		zip.WithOperationID(plane.GitStatus),
		zip.WithSummary("Which of these repos are imported, and which are in conflict"))
}

// planeInbound advances ONE branch of the CALLER's repo from an upstream push.
//
// Native is canonical: the fetch never force-overwrites a native ref, so a
// divergence comes back as Conflict with native untouched rather than as an
// error — the caller needs to know it diverged, not retry into an overwrite.
// The org is the caller's plane identity, so a push routed to one org can never
// advance another's refs.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeInbound(ctx context.Context, in *plane.InboundIn) (*plane.Synced, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git inbound: org required")
	}
	// githubImporter directly, never cloud.InboundGitSync. That function now falls
	// through to the plane when the in-process importer is nil, so routing back
	// through it would let this process dial its own socket and answer itself. It
	// happens to be non-nil here — Mount registers the importer before it publishes
	// this op — but relying on that ordering is how the loop gets introduced later.
	res, err := githubImporter{}.InboundSync(ctx, cloud.GitInboundReq{
		Org: who.Org, Project: in.Project, Repo: in.Repo, Ref: in.Ref,
		CloneURL: in.CloneURL, Token: in.Token, Origin: in.Origin,
	})
	if err != nil {
		return nil, err
	}
	return &plane.Synced{
		Applied: res.Applied, NoOp: res.NoOp, Conflict: res.Conflict,
		Detail: res.Detail, Before: res.Before, After: res.After,
	}, nil
}

// planeImport creates the repo and mirrors the upstream, for the CALLER's org.
//
// The org is never read off the argument: it is the identity the edge minted and
// the plane carried, so a caller holding one org's context cannot create a repo
// in another's namespace. Project carries the provider-side account, which is
// what keeps two upstreams of the same name — hanzoai/ai and hanzo-apps/ai —
// distinct repos rather than one overwriting the other.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeImport(ctx context.Context, in *plane.ImportIn) (*plane.Imported, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git import: org required")
	}
	if in.Repo == "" || in.CloneURL == "" {
		return nil, zip.ErrBadRequest("git import: repo and cloneUrl are required")
	}
	// The local implementation, for the same reason as planeInbound above.
	if err := (githubImporter{}).ImportRepo(ctx, cloud.GitImportReq{
		Org:       who.Org,
		Project:   in.Project,
		Repo:      in.Repo,
		CloneURL:  in.CloneURL,
		Token:     in.Token,
		MirrorURL: in.MirrorURL,
	}); err != nil {
		return nil, err
	}
	return &plane.Imported{Repo: in.Repo}, nil
}

// planeStatus reports which of the named repos the CALLER's org has imported and
// which a prior inbound sync left in conflict.
//
// The app that DRAWS the repo list is integrations (it has the provider's
// catalogue of what could be imported); the app that knows what WAS is this one.
// In a split fleet the in-process importer is nil over there, so the list
// rendered every repo as never-imported — a wrong answer delivered confidently,
// which is worse than the import failure the same split caused, because nothing
// errored.
//
// The reply is a SLICE, not a map: a map cannot cross this wire, so each row
// carries the name it answers for. A name git holds nothing under is ABSENT
// rather than a false row — the caller reads absence as not-imported, which is
// the same value the in-process leg's zero entry yields, so neither leg can be
// told from the other by its result.
//
// It calls the in-process implementation directly rather than
// cloud.GitRepoStatuses: the package func dispatches to whatever is registered,
// and in THIS process that resolution would come back around through the plane
// to this same handler.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeStatus(ctx context.Context, in *plane.StatusIn) (*plane.Statuses, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git status: org required")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(503, "git not mounted")
	}
	st, err := githubImporter{}.RepoStatus(ctx, who.Org, in.Project, in.Names)
	if err != nil {
		return nil, internalErr(err)
	}
	// Walked in the caller's order so the reply is stable, and each name is spent
	// as it is emitted so a name asked twice is one row rather than two.
	rows := make([]plane.RepoStatus, 0, len(st))
	for _, n := range in.Names {
		name := normalizeName(n)
		r, ok := st[name]
		if !ok || r == (cloud.GitRepoStatus{}) {
			continue
		}
		delete(st, name)
		rows = append(rows, plane.RepoStatus{
			Name: name, Imported: r.Imported, Conflict: r.Conflict, LastSyncedAt: r.LastSyncedAt,
		})
	}
	return &plane.Statuses{Rows: rows}, nil
}
