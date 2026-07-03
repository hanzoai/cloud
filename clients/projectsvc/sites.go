package projectsvc

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/clients/sites"
)

// siteResolver adapts the projects Store to the sites.Resolver contract, so the
// public site server (clients/sites, installed at the compose root) resolves a
// `<slug>.hanzo.app` request to its authoritative tenant + S3 location through
// the ONE project store. projectsvc imports sites (not the reverse) — sites stays
// a leaf that cloud's serve.go can wire without an import cycle.
type siteResolver struct{ store *Store }

// Resolve maps a validated subdomain slug to its Site. The org, bucket, and
// prefix are read ONLY from the store binding (site_hosts → projects), never from
// the request — the tenant-isolation boundary. A missing binding is a clean
// not-found (honest 404), not an error.
func (r siteResolver) Resolve(ctx context.Context, slug string) (sites.Site, bool, error) {
	p, err := r.store.ResolveHost(ctx, slug)
	if errors.Is(err, errNotFound) {
		return sites.Site{}, false, nil
	}
	if err != nil {
		return sites.Site{}, false, err
	}
	return sites.Site{
		Org:    p.Org,
		Slug:   p.Slug,
		Bucket: p.Bucket,
		Prefix: sitePrefix(p.Org, p.Slug),
		Status: p.Status,
	}, true, nil
}
