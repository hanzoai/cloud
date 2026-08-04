package main

import (
	"context"
	"fmt"
	"os"
	"strings"

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
// and the cross-app call is made HERE with zip.DialApp — the same door wake.go
// uses to publish one op without reaching for cloud.Plane().
func mountSites(app *zip.App) {
	cfg := sites.Config{
		Apex:            env("CLOUD_SITES_APEX", "hanzo.app"),
		Reserved:        list("CLOUD_SITES_RESERVED"),
		SelfDomains:     list("CLOUD_SITES_SELF_DOMAINS"),
		FirstPartyApex:  env("CLOUD_SITES_FIRST_PARTY_APEX", ""),
		FirstPartySites: list("CLOUD_SITES_FIRST_PARTY"),
		FirstPartyOrg:   env("CLOUD_SITES_FIRST_PARTY_ORG", ""),
	}

	// The project store belongs to `projects`, in another process, so resolution
	// is a plane call.
	sites.SetFallbackResolver(planeResolver{})

	app.Use(sites.New(cfg, app.Logger()).Middleware())
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
	c, err := zip.DialApp("projects")
	if err != nil {
		return sites.Site{}, false, fmt.Errorf("sites: dial projects: %w", err)
	}
	defer func() { _ = c.Close() }()

	out, err := zip.Call[sites.PlaneSiteIn, sites.PlaneSite](ctx, c, op, in)
	if err != nil {
		return sites.Site{}, false, fmt.Errorf("sites: projects %s: %w", op, err)
	}
	s, ok := sites.SiteOf(out)
	return s, ok, nil
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// list splits a comma-separated env var, dropping blanks so a trailing comma or
// an empty value yields no entries rather than one empty label.
func list(k string) []string {
	raw := strings.Split(os.Getenv(k), ",")
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
