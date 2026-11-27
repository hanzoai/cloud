package projects

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
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
// This is the same client FinanceScopeRules already uses and for the same stated
// reason: the READER is a cloud edge middleware and the fact belongs to another
// app. The store read stays in the one process that owns the store.
//
// No org is taken from the caller on the multi-tenant path. The host IS the
// tenant key here, so accepting an org would let a caller name someone else's
// project; ResolveOrg pins an org only for the first-party path, which is
// exactly what it is for.
func exposeSites() {
	cloud.Plane().Post("/sites/resolve", planeResolveSite,
		zip.WithOperationID(client.SitesResolve),
		zip.WithSummary("Resolve a published site by host label"))

	cloud.Plane().Post("/sites/resolve-org", planeResolveSiteOrg,
		zip.WithOperationID(client.SitesResolveOrg),
		zip.WithSummary("Resolve a published site pinned to one org"))

	cloud.Plane().Post("/sites/live", planeLiveSites,
		zip.WithOperationID(client.SitesLive),
		zip.WithSummary("Every serving site, across orgs"))

	cloud.Plane().Post("/sites/claim", planeClaim,
		zip.WithOperationID(client.SitesClaim),
		zip.WithSummary("Claim an app's name in the one public namespace"))

	cloud.Plane().Post("/sites/release", planeRelease,
		zip.WithOperationID(client.SitesRelease),
		zip.WithSummary("Release an app's name from the one public namespace"))

	cloud.Plane().Post("/sites/purge", planePurge,
		zip.WithOperationID(client.SitesPurge),
		zip.WithSummary("Clear URL prefixes from the edge cache"))
}

// purgeMost is Cloudflare's bound on the prefixes of one purge.
const purgeMost = 30

// prefixRE is a URL prefix as the edge takes one: a host name, then a path.
var prefixRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?\.[a-z]{2,}/[A-Za-z0-9._~/-]*$`)

// planePurge clears URL prefixes from the edge cache through the one edge client
// this process holds. It is the platform's to ask: a tenant's site is purged by
// its own cache-tag on its own publish (purgeTag), and a prefix can name any host
// of the zone. The purge is best-effort like every other: an edge that is
// unconfigured, over its ceiling or refused is logged, and the answers lapse by
// their TTL.
func planePurge(ctx context.Context, in *client.EdgePurgeIn) (*client.EdgePurgeOut, error) {
	if org, ok := cloud.Tenant(ctx); !ok || (org != cloud.Brand() && org != authz.AdminOrg) {
		return nil, zip.ErrForbidden("sites purge: only the platform clears the edge by prefix")
	}
	if len(in.Prefixes) == 0 || len(in.Prefixes) > purgeMost {
		return nil, zip.ErrBadRequest("prefixes: between 1 and 30")
	}
	for _, p := range in.Prefixes {
		if !prefixRE.MatchString(p) {
			return nil, zip.ErrBadRequest("a prefix is a host and a path with no scheme, as api.hanzo.ai/v1/pricing")
		}
	}
	s := mounted
	if s == nil || s.State.edge == nil {
		return nil, zip.ErrInternal("projects: this process holds no edge")
	}
	if err := s.State.edge.PurgePrefixes(ctx, in.Prefixes...); err != nil {
		s.Log.Warn("edge prefix purge failed (continuing)", "prefixes", in.Prefixes, "err", err)
	}
	return &client.EdgePurgeOut{Purged: int64(len(in.Prefixes))}, nil
}

// planeClaim takes a name in the one public namespace for an app of the CALLER's
// org. <name>.<apex> is shared by every org's apps and sites, so the claim is a
// site_hosts row like a site's, held by the app (`app:` + its identity) rather
// than by a project slug, which keeps the two from ever reading as one another.
// A name anyone else holds answers Taken, and so does one the platform holds.
func planeClaim(ctx context.Context, in *client.NameIn) (*client.NameOut, error) {
	org, err := nameOrg(ctx, in)
	if err != nil {
		return nil, err
	}
	s := mounted
	if s == nil || s.State.store == nil {
		return nil, zip.ErrInternal("projects: this process does not own the project store")
	}
	switch err := s.State.store.Claim(ctx, siteHost(org, in.Name), org, in.Holder, time.Now().Unix()); {
	case errors.Is(err, errHostTaken), errors.Is(err, errReservedHost):
		return &client.NameOut{Taken: true}, nil
	case err != nil:
		return nil, err
	}
	return &client.NameOut{}, nil
}

// planeRelease gives an app's name back. Scoped to the org and the holder, so
// one app can never release another's name, or a site's.
func planeRelease(ctx context.Context, in *client.NameIn) (*client.NameOut, error) {
	org, err := nameOrg(ctx, in)
	if err != nil {
		return nil, err
	}
	s := mounted
	if s == nil || s.State.store == nil {
		return nil, zip.ErrInternal("projects: this process does not own the project store")
	}
	if err := s.State.store.UnbindHost(ctx, siteHost(org, in.Name), org, in.Holder); err != nil {
		return nil, err
	}
	return &client.NameOut{}, nil
}

// nameOrg is the caller's org for a name op, refusing a name that is no DNS label
// and a holder that is not an app's.
func nameOrg(ctx context.Context, in *client.NameIn) (string, error) {
	org := cloud.Who(ctx).Org
	if org == "" || !orgPathSafe(org) {
		return "", zip.ErrForbidden("sites name: org required")
	}
	if !slugRE.MatchString(in.Name) {
		return "", zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	if !strings.HasPrefix(in.Holder, "app:") || len(in.Holder) == len("app:") {
		return "", zip.ErrBadRequest("holder must name an app")
	}
	return org, nil
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
func planeLiveSites(ctx context.Context, _ *client.LiveSitesIn) (*client.LiveSitesOut, error) {
	live, err := LiveSites(ctx)
	if err != nil {
		return nil, err
	}
	out := &client.LiveSitesOut{Sites: make([]client.LiveSite, 0, len(live))}
	for _, s := range live {
		out.Sites = append(out.Sites, client.LiveSite{
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
func planeResolveSite(ctx context.Context, in *client.SiteIn) (*client.Site, error) {
	r, err := currentResolver()
	if err != nil {
		return nil, err
	}
	s, ok, err := r.Resolve(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	return planeSite(s, ok), nil
}

// planeResolveSiteOrg is the first-party path: it NEVER falls back to
// unique-across-orgs, so an internal host is served only by our own project and
// never a customer's same-named one.
func planeResolveSiteOrg(ctx context.Context, in *client.SiteIn) (*client.Site, error) {
	r, err := currentResolver()
	if err != nil {
		return nil, err
	}
	s, ok, err := r.ResolveOrg(ctx, in.Org, in.Slug)
	if err != nil {
		return nil, err
	}
	return planeSite(s, ok), nil
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

// planeSite projects a resolved Site onto the plane shape, carrying found-ness
// explicitly so the edge can tell "no such site" from "could not ask".
func planeSite(s sites.Site, ok bool) *client.Site {
	if !ok {
		return &client.Site{Found: false}
	}
	return &client.Site{
		Found:                true,
		Org:                  s.Org,
		Namespace:            s.Namespace,
		Slug:                 s.Slug,
		Bucket:               s.Bucket,
		Prefix:               s.Prefix,
		Status:               s.Status,
		CrossOriginIsolation: s.CrossOriginIsolation,
	}
}
