package main

import (
	"context"
	"fmt"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/sites"
)

// The published-site edge, in the process that actually owns the public port.
//
// This is where <slug>.hanzo.app is served, and until now it was served nowhere.
// The middleware was mounted in serve.go — the FUSED composition root — but the
// image runs the light router (this binary) on the public port with one process
// per app beside it, and none of those is serve.go. So every published site fell
// through to the console SPA: a customer got the console instead of their
// website, and the whole /v1 surface answered on their own hostname. Proven at
// the pod — the running /cloud binary did not contain `sites_resolve` at all.
//
// The router "deliberately links none of" the fleet's package graph (main.go),
// and that still holds. apps/sites is a leaf (zip + s3, never the root package),
// and the cross-app call is made HERE with zip.DialApp — the same path wake.go
// uses to publish one op without reaching for cloud.Plane().
// The config is resolved by apps/sites itself (sites.ConfigFromEnv), not spelled
// out again here. This binary and cloud.Listen both mount this middleware, and
// when each read the environment for itself they disagreed: two spellings of the
// first-party keys and two sets of defaults, so the policy in force depended on
// which process the request reached. The domain argument is empty because the
// router has already published its --domain flag as CLOUD_DOMAIN (main.go,
// forward) before anything reads it.
// projectApp is the app that owns the project store — the one this router asks
// to resolve a site, and therefore the one that must be running before either the
// site edge or the console can read a release. Named ONCE: a process name spelled
// at each call site is a chance to wake the wrong one, and the console's start
// (main.go) and this resolver's dial have to mean the same process or neither
// works.
const projectApp = "project"

func mountSites(app *zip.App) {
	// The project store belongs to `project`, in another process, so resolution
	// is a plane call.
	sites.SetFallbackResolver(planeResolver{})

	app.Use(sites.New(sites.ConfigFromEnv(""), app.Logger()).Middleware())
}

// planeResolver answers "which published site is this host?" by asking the app
// that owns the store.
//
// A dial or call failure is RETURNED, never swallowed into not-found: the edge
// renders 503 for an error and 404 only for a genuine miss, so a transient
// failure of the owning app can never look like a customer's site being deleted.
type planeResolver struct{}

func (planeResolver) Resolve(ctx context.Context, slug string) (sites.Site, bool, error) {
	return ask(ctx, "sites_resolve", &sites.PlaneSiteIn{Slug: slug})
}

func (planeResolver) ResolveOrg(ctx context.Context, org, slug string) (sites.Site, bool, error) {
	return ask(ctx, "sites_resolve_org", &sites.PlaneSiteIn{Slug: slug, Org: org})
}

func ask(ctx context.Context, op string, in *sites.PlaneSiteIn) (sites.Site, bool, error) {
	c, err := zip.DialApp(projectApp)
	if err != nil {
		return sites.Site{}, false, fmt.Errorf("sites: dial %s: %w", projectApp, err)
	}
	defer func() { _ = c.Close() }()

	out, err := zip.Call[sites.PlaneSiteIn, sites.PlaneSite](ctx, c, op, in)
	if err != nil {
		return sites.Site{}, false, fmt.Errorf("sites: %s %s: %w", projectApp, op, err)
	}
	s, ok := sites.SiteOf(out)
	return s, ok, nil
}
