package projects

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/clients/sites"
)

// siteResolver adapts the projects Store to the sites.Resolver contract, so the
// public site server (clients/sites, installed at the compose root) resolves a
// `<slug>.hanzo.app` request to its authoritative org + S3 location through
// the ONE project store. projects imports sites (not the reverse) — sites stays
// a leaf that cloud's serve.go can wire without an import cycle.
type siteResolver struct{ store *Store }

// Resolve maps a validated subdomain slug to its Site. The org, bucket, and
// prefix are read ONLY from the store binding (site_hosts → projects), never from
// the request — the org-isolation boundary. A missing binding is a clean
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
		Org:                  p.Org,
		Slug:                 p.Slug,
		Bucket:               p.Bucket,
		Prefix:               sitePrefix(p.Org, p.Slug),
		Status:               p.Status,
		CrossOriginIsolation: crossOriginIsolated(p.Framework),
	}, true, nil
}

// crossOriginIsolated reports whether a project's declared framework is a
// WebGL/WASM game engine whose multithreaded build needs SharedArrayBuffer — the
// OPT-IN signal the site server uses to serve the site cross-origin-isolated
// (COOP/COEP). It is a closed set on an EXISTING per-project field (no schema
// change): only a project a user explicitly declared as a game engine gets the
// isolating headers, which break embedding third-party cross-origin content and
// so must never be global. This is the ONE place projects→sites translates a
// build hint into the serve-time isolation policy.
func crossOriginIsolated(framework string) bool {
	switch framework {
	case "unity", "unreal", "godot":
		return true
	default:
		return false
	}
}
