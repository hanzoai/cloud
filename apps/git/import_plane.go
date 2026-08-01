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
// this one. They are different processes, so cloud.RegisterGitImporter — an
// in-process seam — leaves gitImporter nil on the integrations side, and every
// import answered "git importer not registered" while both apps were healthy.
// The same shape as the KMS assertion: a seam that assumed co-residence.
//
// The request travels instead. The socket has already decided who may ask (0600,
// SO_PEERCRED), and the tenant comes from the caller's plane identity rather than
// the argument, so an app acting for one org cannot import into another's.

// exposeImport publishes the import on the internal plane. Mount calls it.
func exposeImport() {
	zip.Post[plane.ImportIn, plane.Imported](cloud.Plane(), "/git/import", planeImport,
		zip.WithOperationID(plane.GitImport),
		zip.WithSummary("Create a repo and mirror an upstream into it"))

	zip.Post[plane.InboundIn, plane.Synced](cloud.Plane(), "/git/inbound", planeInbound,
		zip.WithOperationID(plane.GitInbound),
		zip.WithSummary("Advance one branch from an upstream push"))
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
	res, err := cloud.InboundGitSync(ctx, cloud.GitInboundReq{
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
	if err := cloud.ImportGitRepo(ctx, cloud.GitImportReq{
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
