package sync

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// import_plane.go carries the git seams across a PROCESS boundary.
//
// The app that DECIDES to import is integrations: it holds the provider
// connection and can mint the credential. The app that answers for the sync — the
// advance itself, and the record of what it did — is this one. They are
// different processes, so cloud.RegisterGitImporter, which only registers
// in-process, leaves the importer nil over there; without this the import would
// answer "git importer not registered" while both apps were healthy.
//
// The ops keep the SAME operation ids the retired git app published
// (plane.GitImport and friends). The wire contract did not change — the same
// request, the same reply, the same meaning — only which app answers it, and
// spelling a new id would have made every caller change to say the same thing.
//
// The socket has already decided who may ask (0600, SO_PEERCRED), and the tenant
// comes from the CALLER'S plane identity rather than the argument — which is why
// none of the In shapes carries an org — so an app acting for one tenant cannot
// import into another's.

// exposeImport publishes the git seams on the internal plane. Mount calls it.
//
// All four are published together because they are ONE boundary: the same split
// answers import, inbound, status and the mirror declaration. Splitting them
// across files would say they are different boundaries, and they are not.
func exposeImport() {
	zip.Post[plane.ImportIn, plane.Imported](cloud.Plane(), "/git/import", planeImport,
		zip.WithOperationID(plane.GitImport),
		zip.WithSummary("Create a repo on the forge and advance an upstream into it"))

	zip.Post[plane.InboundIn, plane.Synced](cloud.Plane(), "/git/inbound", planeInbound,
		zip.WithOperationID(plane.GitInbound),
		zip.WithSummary("Advance one ref from an upstream push"))

	zip.Post[plane.StatusIn, plane.Statuses](cloud.Plane(), "/git/status", planeStatus,
		zip.WithOperationID(plane.GitStatus),
		zip.WithSummary("Which of these repos the forge holds, and which are in conflict"))

	zip.Post[plane.MirrorIn, plane.Mirrored](cloud.Plane(), "/git/mirror", planeMirror,
		zip.WithOperationID(plane.GitMirror),
		zip.WithSummary("Declare or withdraw a repo's outbound mirror target"))
}

// planeInbound advances ONE ref of the CALLER's repo from an upstream push.
//
// The forge is canonical: the push that carries the update is non-forcing, so a
// divergence comes back as Conflict with the forge untouched rather than as an
// error — the caller needs to know it diverged, not to retry into an overwrite.
// The org is the caller's plane identity, so a push routed to one org can never
// advance another's refs.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeInbound(ctx context.Context, in *plane.InboundIn) (*plane.Synced, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git inbound: org required")
	}
	// The local implementation directly, never cloud.InboundGitSync. That function
	// falls through to the plane when the in-process seam is nil, so routing back
	// through it would let this process dial its own socket and answer itself. It
	// happens to be non-nil here — Mount registers before it publishes — but
	// relying on that ordering is how the loop gets introduced later.
	res, err := importer{}.InboundSync(ctx, cloud.GitInboundReq{
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

// planeImport creates the repo on the forge and advances the upstream into it,
// for the CALLER's org.
//
// The org is never read off the argument: it is the identity the edge minted and
// the plane carried, so a caller holding one org's context cannot create a repo
// in another's namespace.
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
	if err := (importer{}).ImportRepo(ctx, cloud.GitImportReq{
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

// planeStatus reports which of the named repos the forge holds for the CALLER's
// org and which a prior advance left in conflict.
//
// The app that DRAWS the repo list is integrations (it has the provider's
// catalogue of what could be imported); the app that knows what WAS is this one.
//
// The reply is a SLICE, not a map: a map cannot cross this wire, so each row
// carries the name it answers for. A name with nothing to say is ABSENT rather
// than a false row — the caller reads absence as not-imported, which is the same
// value the in-process leg's zero entry yields, so neither leg can be told from
// the other by its result.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeStatus(ctx context.Context, in *plane.StatusIn) (*plane.Statuses, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git status: org required")
	}
	st, err := importer{}.RepoStatus(ctx, who.Org, in.Project, in.Names)
	if err != nil {
		return nil, err
	}
	// Walked in the caller's order so the reply is stable, and each name is spent
	// as it is emitted so a name asked twice is one row rather than two.
	rows := make([]plane.RepoStatus, 0, len(st))
	for _, n := range in.Names {
		name := normalizeGitName(n)
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

// planeMirror declares (Enabled) or withdraws (!Enabled) one outbound mirror
// target on a repo of the CALLER's org, idempotently either way.
//
// It declares the target and nothing more: the pushing happens on the next
// advance of that repository, so a target that exists is a fact about the repo
// rather than a job somebody has to keep running. EnsureMirror is the same func
// the in-process controller exposes, so a URL crossing the plane passes the
// identical gate — https, no userinfo, on the outbound allowlist — and a remote
// caller cannot declare a push a local one could not.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeMirror(ctx context.Context, in *plane.MirrorIn) (*plane.Mirrored, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git mirror: org required")
	}
	if in.Repo == "" {
		return nil, zip.ErrBadRequest("git mirror: repo is required")
	}
	if err := (mirrorControl{}).EnsureMirror(ctx, who.Org, in.Project, in.Repo, in.URL, in.Enabled); err != nil {
		return nil, err
	}
	return &plane.Mirrored{Repo: in.Repo}, nil
}
