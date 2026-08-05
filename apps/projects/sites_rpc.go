package projects

import (
	"context"

	"github.com/zap-proto/zip"

	cloud "github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/plane"
)

// The site edge asks projects which published site a host belongs to.
//
// sites.SetResolver already installs an in-process resolver at Mount, and that
// stays — when the edge and projects happen to share a process it is the right
// answer, with no hop. But the pod boots ~25 SINGLE-app processes, so in
// production they never do: the registry was set inside `projects` and read
// inside whichever process fronts :8000, where it is nil. A nil registry is a
// clean miss rather than a fault, so every published site resolved as not-found
// with no error anywhere and fell through to the API pipeline — every
// <slug>.hanzo.app served the console SPA, and the whole cloud API answered on
// the customer's own hostname. Measured at the pod with the ingress bypassed.
//
// This is the same seam FinanceScopeRules already uses and for the same stated
// reason: the READER is a cloud edge middleware and the fact belongs to another
// app. The store read stays in the one process that owns the store.
//
// No org is taken from the caller on the multi-tenant path. The host IS the
// tenant key here, so accepting an org would let a caller name someone else's
// project; ResolveOrg pins an org only for the first-party path, which is
// exactly what it is for.
func exposeSites() {
	zip.Post[plane.SiteIn, plane.Site](cloud.Plane(), "/sites/resolve", planeResolveSite,
		zip.WithOperationID(plane.SitesResolve),
		zip.WithSummary("Resolve a published site by host label"))

	zip.Post[plane.SiteIn, plane.Site](cloud.Plane(), "/sites/resolve-org", planeResolveSiteOrg,
		zip.WithOperationID(plane.SitesResolveOrg),
		zip.WithSummary("Resolve a published site pinned to one org"))

	zip.Post[plane.LiveSitesIn, plane.LiveSitesOut](cloud.Plane(), "/sites/live", planeLiveSites,
		zip.WithOperationID(plane.SitesLive),
		zip.WithSummary("Every serving site, across orgs"))
}

// planeLiveSites answers the cross-org directory read for the process that
// assembles the catalog, which is never this one.
//
// LiveSites returns nil when this package is unmounted, on the reasoning that a
// deployment which hosts nothing is not a fault. That reads correctly here — the
// process that owns the store is the one answering — and read WRONG in the
// catalog process, where nil meant "ask somewhere else" and was silently
// published as "nothing is live". Every demo URL, the whole `site` kind, and the
// template lane's deployed starters left the corpus without an error anywhere.
//
// No org, on purpose, exactly like the resolve above. This is the one cross-org
// read in the package and the visibility rule that makes it safe lives in its
// query, not in its caller.
func planeLiveSites(ctx context.Context, _ *plane.LiveSitesIn) (*plane.LiveSitesOut, error) {
	live, err := LiveSites(ctx)
	if err != nil {
		return nil, err
	}
	out := &plane.LiveSitesOut{Sites: make([]plane.LiveSite, 0, len(live))}
	for _, s := range live {
		out.Sites = append(out.Sites, plane.LiveSite{
			Org: s.Org, Slug: s.Slug, Name: s.Name, URL: s.URL,
			Repo: s.Repo, ForkedFrom: s.ForkedFrom, UpdatedAt: s.UpdatedAt,
			Upstream: s.Upstream, License: s.License,
		})
	}
	return out, nil
}

// planeResolveSite answers the multi-tenant product URL (<slug>.hanzo.app) and
// bound custom domains. Not-found is `Found:false`, never an error: the edge
// turns that into an honest 404, and an error into a 503. Collapsing the two
// would serve 404s for real live sites during a transient failure.
func planeResolveSite(ctx context.Context, in *plane.SiteIn) (*plane.Site, error) {
	r, err := currentResolver()
	if err != nil {
		return nil, err
	}
	s, ok, err := r.Resolve(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	return wireSite(s, ok), nil
}

// planeResolveSiteOrg is the first-party path: it NEVER falls back to
// unique-across-orgs, so an internal host is served only by our own project and
// never a customer's same-named one.
func planeResolveSiteOrg(ctx context.Context, in *plane.SiteIn) (*plane.Site, error) {
	r, err := currentResolver()
	if err != nil {
		return nil, err
	}
	s, ok, err := r.ResolveOrg(ctx, in.Org, in.Slug)
	if err != nil {
		return nil, err
	}
	return wireSite(s, ok), nil
}

// The resolver this process serves plane answers from. Set at Mount beside
// sites.SetResolver, so the two can never name different stores.
var planeResolver siteResolver

func setResolverForPlane(r siteResolver) { planeResolver = r }

// currentResolver refuses rather than answering not-found when the store is
// absent. A process that has not mounted projects cannot know whether a site
// exists, and saying "no" would take every live site off the air with a 404.
func currentResolver() (siteResolver, error) {
	if planeResolver.store == nil {
		return siteResolver{}, zip.ErrInternal("sites: this process does not own the project store")
	}
	return planeResolver, nil
}

// wireSite projects a resolved Site onto the wire shape, carrying found-ness
// explicitly so the edge can tell "no such site" from "could not ask".
func wireSite(s sites.Site, ok bool) *plane.Site {
	if !ok {
		return &plane.Site{Found: false}
	}
	return &plane.Site{
		Found:                true,
		Org:                  s.Org,
		Slug:                 s.Slug,
		Bucket:               s.Bucket,
		Prefix:               s.Prefix,
		Status:               s.Status,
		CrossOriginIsolation: s.CrossOriginIsolation,
	}
}
