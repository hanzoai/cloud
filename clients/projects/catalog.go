package projects

import (
	"context"
	"fmt"
)

// catalog.go is projects' READ seam for the cross-org catalog (clients/catalog):
// the live sites this deployment is serving, across every org.
//
// It is deliberately the ONLY cross-org read in this package, and it returns just
// the facts a catalog row needs — org, slug, name, live URL, and provenance: the
// source repo, the parent it was forked from, whether it is FIRST-PARTY, and the
// third-party work it was published from. Everything that makes a project a
// tenant's business (bucket, release pointer, custom domains, analytics) stays
// behind the org-scoped API. A caller that wanted more would be asking projects
// to be a directory, which it is not.
//
// Provenance is READ here rather than reconstructed there because this package
// is where it is written: `repo_url` is what the project declared it was built
// from, `forked_from` is the attribution edge the fork path stamps, `official` is
// the admin-gated first-party marker, `upstream`/`license` credit somebody else's
// work. A catalog that had to guess any of them would be guessing about
// authorship — and a directory that guesses authorship in its own favour is not
// making an error, it is making a claim.
//
// Tenancy is the CALLER's to enforce and catalog does: a site published by an org
// enters that ORG's corpus, and only the platform's own org publishes into the
// world-readable one. This function does not decide that; it reports what is live.

// LiveSite is one deployed site as the catalog sees it. Repo and ForkedFrom are
// the trace back out of the demo: a live URL nobody can get from to the source
// is a screenshot, not a starting point.
type LiveSite struct {
	Org, Slug, Name, URL string
	Repo, ForkedFrom     string
	UpdatedAt            int64
	// Official is the platform-gated first-party marker; Upstream/License credit
	// the third-party work a demo was published from. Reported exactly as stored —
	// this function never infers provenance, because a guessed badge is worse than
	// no badge at all.
	Official          bool
	Upstream, License string
}

// LiveSites returns every project currently serving at its site host, newest
// first. Unmounted ⇒ no sites (a deployment that does not host is not an error).
func LiveSites(ctx context.Context) ([]LiveSite, error) {
	s := mounted
	if s == nil || s.State.store == nil {
		return nil, nil
	}
	rows, err := s.State.store.db.QueryContext(ctx,
		`SELECT org, slug, name, live_url, repo_url, forked_from, updated_at, official, upstream, license
		 FROM projects WHERE status='live' ORDER BY updated_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("catalog: list live sites: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []LiveSite
	for rows.Next() {
		var v LiveSite
		if err := rows.Scan(&v.Org, &v.Slug, &v.Name, &v.URL, &v.Repo, &v.ForkedFrom, &v.UpdatedAt,
			&v.Official, &v.Upstream, &v.License); err != nil {
			return nil, err
		}
		if v.URL == "" {
			v.URL = siteURL(s, v.Org, v.Slug)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
