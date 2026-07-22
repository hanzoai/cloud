package projects

import (
	"context"
	"errors"
	"strings"

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
//
// A BARE key (no dot — the `<slug>.hanzo.app` product URL) additionally falls
// back to the unique-live-slug resolve: serve iff exactly one live project owns
// that slug across all orgs. Deterministic, shadow-safe (reserved labels never
// reach here), and migration-free for projects published before host binding
// existed. An org-scoped `<slug>.<org>` key resolves ONLY via its binding.
func (r siteResolver) Resolve(ctx context.Context, slug string) (sites.Site, bool, error) {
	p, err := r.store.ResolveHost(ctx, slug)
	if errors.Is(err, errNotFound) && !strings.Contains(slug, ".") {
		p, err = r.store.ResolveUniqueLiveSlug(ctx, slug)
	}
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
		Prefix:               servePrefix(p),
		Status:               p.Status,
		CrossOriginIsolation: crossOriginIsolated(p.Framework),
	}, true, nil
}

// ResolveOrg resolves a slug PINNED to a specific org — the site path for a
// first-party host (cd.hanzo.ai). It NEVER falls back to unique-across-orgs, so an
// internal host is served ONLY by OUR own project, never a customer's same-named one.
func (r siteResolver) ResolveOrg(ctx context.Context, org, slug string) (sites.Site, bool, error) {
	p, err := r.store.ResolveOrgLiveSlug(ctx, org, slug)
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
		Prefix:               servePrefix(p),
		Status:               p.Status,
		CrossOriginIsolation: crossOriginIsolated(p.Framework),
	}, true, nil
}

// servePrefix is the ONE rule for which S3 prefix a site serves from, and the
// read side of the release pointer: a project with an active release serves that
// release's immutable prefix; a project without one serves the legacy mutable
// `<org>/<slug>/` prefix it was deployed to.
//
// The pointer is re-validated HERE against releaseIDRE before it can widen a
// prefix. It was already validated when it was written (activation only accepts
// an id that matches a stored release row, and ids are minted as digests), so
// this is defense in depth: even a poisoned or hand-edited row cannot produce a
// prefix that escapes the org — an unrecognized id falls back to the legacy
// prefix rather than being interpolated.
func servePrefix(p Project) string {
	if id := p.CurrentRelease; id != "" && releaseIDRE.MatchString(id) {
		return releasePrefix(p.Org, p.Slug, id)
	}
	return sitePrefix(p.Org, p.Slug)
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
