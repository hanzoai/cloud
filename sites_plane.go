package cloud

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/plane"
)

// planeSites resolves a published site by asking the app that owns the project
// store, over the internal plane.
//
// The site edge middleware runs at the compose root, in whatever process fronts
// the public port; the store belongs to `projects`. In production those are
// never the same process — the pod boots ~25 single-app processes — so the
// package-level registry projects.Mount writes is nil where the edge reads it.
// That is why every <slug>.hanzo.app served the console SPA: a nil resolver is a
// clean miss, so the request fell through to the API pipeline with no error
// anywhere to notice.
//
// Same seam, same reason, as the balance and scope-rule reads that already cross
// this plane: the reader is an edge middleware and the fact belongs elsewhere.
type planeSites struct{}

// Resolve answers the multi-tenant product URL and bound custom domains.
//
// A site that does not exist comes back Found:false and becomes an honest 404.
// A failure to ASK is an error, and stays one — the edge renders 503 for that.
// Collapsing the two would serve 404s for real, live customer sites during any
// transient failure of the owning app, which looks exactly like the site being
// deleted.
func (planeSites) Resolve(ctx context.Context, slug string) (sites.Site, bool, error) {
	return askSite(ctx, &plane.SiteIn{Slug: slug}, plane.SitesResolve)
}

// ResolveOrg is the first-party path, pinned to one org so an internal host is
// never served by a customer's same-named project.
func (planeSites) ResolveOrg(ctx context.Context, org, slug string) (sites.Site, bool, error) {
	return askSite(ctx, &plane.SiteIn{Slug: slug, Org: org}, plane.SitesResolveOrg)
}

func askSite(ctx context.Context, in *plane.SiteIn, op string) (sites.Site, bool, error) {
	// The site plane read is org-less by construction: the HOST is the tenant
	// key, and the answer names the org. Passing one in would let a caller point
	// at someone else's project.
	out, err := Ask[plane.SiteIn, plane.Site](For(ctx, ""), "projects", op, in)
	if err != nil {
		return sites.Site{}, false, fmt.Errorf("sites: ask projects: %w", err)
	}
	if out == nil {
		return sites.Site{}, false, fmt.Errorf("sites: projects answered nothing")
	}
	if !out.Found {
		return sites.Site{}, false, nil
	}
	return sites.Site{
		Org:                  out.Org,
		Slug:                 out.Slug,
		Bucket:               out.Bucket,
		Prefix:               out.Prefix,
		Status:               out.Status,
		CrossOriginIsolation: out.CrossOriginIsolation,
	}, true, nil
}
