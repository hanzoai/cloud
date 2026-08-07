package projects

import (
	"context"
	"strings"
)

// tags.go — TagsFor, the seam the destinations tag door (GET /v1/tags) reads to answer
// which browser pixels a SITE injects. A project IS a site, and it carries its own
// Project.Tags (platform → non-secret pixel id); this resolves the right site TWO ways so
// one org's many sites never share a tag config:
//
//   - by the publishable KEY, when it is a per-site project key (ResolveKey);
//   - else by the request HOST, when the caller presented an org-level key and the site
//     is decided by where the tag runs (ResolveHost) — "derive from site which to use".
//
// The API SECRET never travels this path — only the non-secret ids, which ship in the
// page anyway. Fails SOFT: unmounted, or no project, ⇒ (nil, false), so the tag door
// answers an empty set and a page never breaks on its config.

// TagsFor returns a site's browser tag config for (key, host). host must already be a
// bare hostname (the caller normalizes the Origin/Referer). Key wins over host.
func TagsFor(ctx context.Context, key, host string) (map[string]string, bool) {
	s := mounted
	if s == nil || s.State.store == nil {
		return nil, false
	}
	if k := strings.TrimSpace(key); k != "" {
		if p, err := s.State.store.ResolveKey(ctx, k); err == nil {
			return p.Tags, true
		}
	}
	if h := strings.TrimSpace(host); h != "" {
		if p, err := s.State.store.ResolveHost(ctx, h); err == nil {
			return p.Tags, true
		}
	}
	return nil, false
}
