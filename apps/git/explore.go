// explore.go — Hanzo Git's PUBLIC discovery surface: a GitHub-style landing +
// explore page that lists every PUBLIC repo across all orgs, searchable, with NO
// authentication. Most Hanzo projects are OSS, so the default git.hanzo.ai face is
// open: browse + search + clone public repos anonymously; sign-in is only for
// private repos and writes.
//
// Cross-org listing: repos live in per-org stores ({DataDir}/orgs/{slug}/git.db),
// so there is no global index — we enumerate the orgs/ directory and union each
// store's public rows. Bounded by a cap so a pathological org count can't wedge the
// landing page.

package git

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// exploreRow is one public repo in the explore list (org-qualified, since the
// explore surface spans orgs).
type exploreRow struct {
	Org, Name, Description, DefaultBranch, Size, Updated string
}

// maxExploreScan bounds how many org stores the explore page will open in one
// request — discovery stays snappy even if the fleet grows huge.
const maxExploreScan = 500

// listOrgSlugs enumerates org slugs from {DataDir}/orgs/. Missing dir ⇒ none
// (a fresh install with zero orgs is a valid empty explore, not an error).
func listOrgSlugs(dataDir string) ([]string, error) {
	root := filepath.Join(dataDir, "orgs")
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	slugs := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() && orgRE.MatchString(e.Name()) {
			slugs = append(slugs, e.Name())
		}
	}
	return slugs, nil
}

// allPublicRepos unions every org's PUBLIC repos, optionally filtered by a
// case-insensitive substring query over org/name/description. Newest first.
func allPublicRepos(s *cloud.Service[state], ctx context.Context, query string) ([]exploreRow, error) {
	slugs, err := listOrgSlugs(s.State.dataDir)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	rows := make([]exploreRow, 0, 64)
	scanned := 0
	for _, org := range slugs {
		if scanned >= maxExploreScan {
			break
		}
		scanned++
		st, err := storeFor(s, org)
		if err != nil {
			continue // a broken org store must not sink the whole page
		}
		repos, err := st.ListPublic(ctx, org)
		if err != nil {
			continue
		}
		for _, r := range repos {
			if q != "" && !matchesQuery(q, org, r.Name, r.Description) {
				continue
			}
			rows = append(rows, exploreRow{
				Org: org, Name: r.Name, Description: r.Description,
				DefaultBranch: firstNonEmptyStr(r.DefaultBranch, defaultBranchName),
				Size:          humanBytes(r.SizeBytes), Updated: rfc3339(r.UpdatedAt),
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Updated != rows[j].Updated {
			return rows[i].Updated > rows[j].Updated // rfc3339 sorts lexically by time
		}
		if rows[i].Org != rows[j].Org {
			return rows[i].Org < rows[j].Org
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

func matchesQuery(q, org, name, desc string) bool {
	return strings.Contains(strings.ToLower(org), q) ||
		strings.Contains(strings.ToLower(name), q) ||
		strings.Contains(strings.ToLower(desc), q)
}

// uiExplore renders the public landing/explore page. NO auth: anonymous callers
// see every public repo; the search box filters via ?q=. This is the default
// git.hanzo.ai face for signed-out visitors.
func uiExplore(s *cloud.Service[state], c *zip.Ctx) error {
	query := c.Query("q")
	rows, err := allPublicRepos(s, c.Context(), query)
	if err != nil {
		return zip.Errorf(500, "explore: %v", err)
	}
	base := uiBase(s, c)
	// Whether the viewer is signed in decides the header CTA (Sign in vs their org).
	viewerOrg, signedIn := org(c)
	return render(c, base, 200, "Explore", exploreTmpl, exploreData{
		Base: base, Query: query, Repos: rows,
		SignedIn: signedIn, ViewerOrg: viewerOrg,
	})
}
