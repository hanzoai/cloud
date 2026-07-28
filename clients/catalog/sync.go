package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// sync.go builds the corpus. It is a RECONCILE, not an API: there is no endpoint
// that writes the catalog, so there is no credential that could publish into it
// and no gate anyone could get wrong. The platform syncs its own catalog from the
// two places the truth already lives:
//
//	the source orgs   every public repo of hanzoai / luxfi / zooai and their app
//	                  and template orgs — what we BUILT.
//	the sites store   every project this deployment is serving — what is LIVE.
//
// Where a row lands is the whole tenancy rule, and it is one line each:
//   - a public repo is public by definition        → the published corpus
//   - a live site published by the PLATFORM's own org → the published corpus
//   - a live site published by any other org       → THAT ORG's corpus, and no
//     other tenant ever runs the query that would return it
//
// A customer's project therefore cannot reach the public catalog by any path,
// including a mistake here: the only rows that go public come from an explicit
// org allowlist.

const (
	// sourceOrgsEnv overrides the GitHub orgs the corpus is built from, as a
	// comma-separated list. The default IS the fleet.
	sourceOrgsEnv = "CLOUD_CATALOG_ORGS"

	// platformOrgEnv names the org whose live sites are published to the world —
	// our own. Every other org's sites stay in that org's corpus.
	platformOrgEnv = "CLOUD_CATALOG_PLATFORM_ORG"

	// every is how often the corpus reconciles. The sources change on a human
	// timescale, and each pass is ~2 requests per org.
	every = time.Hour

	// firstAfter delays the first pass so a boot never blocks on the network.
	firstAfter = 90 * time.Second

	perPage  = 100
	maxPages = 5
)

// defaultOrgs maps a GitHub org to the brand it belongs to. The brand — not the
// GitHub org — is what a person browses by: nobody looks for "zoo-apps", they
// look for zoo.
var defaultOrgs = map[string]string{
	"hanzoai": "hanzo", "hanzo-apps": "hanzo", "hanzo-templates": "hanzo",
	"luxfi": "lux", "lux-apps": "lux", "luxcpp": "lux",
	"zooai": "zoo", "zoo-apps": "zoo",
	"zenlm": "zen",
}

// ghRepo is the slice of GitHub's repo object the catalog reads.
type ghRepo struct {
	Name        string   `json:"name"`
	FullName    string   `json:"full_name"`
	Description string   `json:"description"`
	Language    string   `json:"language"`
	Stars       int      `json:"stargazers_count"`
	PushedAt    string   `json:"pushed_at"`
	Homepage    string   `json:"homepage"`
	HTMLURL     string   `json:"html_url"`
	Topics      []string `json:"topics"`
	Fork        bool     `json:"fork"`
	IsTemplate  bool     `json:"is_template"`
	Archived    bool     `json:"archived"`
	Private     bool     `json:"private"`
}

// run reconciles the whole corpus once: the published one, then each org's own.
func run(ctx context.Context) (int, int, error) {
	pub, byOrg, err := corpus(ctx)
	if err != nil && len(pub) == 0 {
		return 0, 0, err
	}
	kept, pruned, err := reconcile(ctx, PublicOrg, pub)
	if err != nil {
		return 0, 0, err
	}
	for org, rows := range byOrg {
		if _, _, e := reconcile(ctx, org, rows); e != nil {
			return kept, pruned, e
		}
	}
	return kept, pruned, nil
}

// corpus assembles the rows from both sources. A source that fails does not
// empty the corpus it feeds — a GitHub outage must not prune every repo out of
// the catalog — so a failed fetch returns its error AND whatever it did read, and
// run only reconciles when it got something.
func corpus(ctx context.Context) ([]Entry, map[string][]Entry, error) {
	var pub []Entry
	var ferr error
	for gh, brand := range sourceOrgs() {
		rows, err := repos(ctx, gh)
		if err != nil {
			ferr = err
			continue
		}
		for _, r := range rows {
			if r.Archived || r.Private {
				continue
			}
			pub = append(pub, fromRepo(r, brand))
		}
	}
	byOrg := map[string][]Entry{}
	sites, err := liveSites(ctx)
	if err != nil {
		ferr = err
	}
	platform := getenv(platformOrgEnv, "hanzo")
	for _, s := range sites {
		e := Entry{
			ID: s.Org + "/" + s.Slug, Org: s.Org, Name: s.Slug, Title: s.Name,
			Kind: "site", Archetype: "site", URL: s.URL,
			Updated: time.Unix(s.UpdatedAt, 0).UTC().Format(time.RFC3339),
		}
		if s.Org == platform {
			e.Forkable = true // our own demos are what a visitor is meant to fork
			pub = append(pub, e)
			continue
		}
		byOrg[s.Org] = append(byOrg[s.Org], e)
	}
	if len(pub) == 0 && len(byOrg) == 0 {
		return nil, nil, ferr
	}
	return pub, byOrg, ferr
}

// fromRepo maps one repo to a catalog row. The archetype is derived from the
// repo's own topics and name rather than guessed by a model: a wrong archetype is
// worse than none, because it silently hides the row from the browse rail.
func fromRepo(r ghRepo, brand string) Entry {
	url := strings.TrimSpace(r.Homepage)
	if url != "" && !strings.HasPrefix(url, "http") {
		url = "https://" + url
	}
	return Entry{
		ID: brand + "/" + r.Name, Org: brand, Name: r.Name, Title: r.Name,
		Kind: "repo", Archetype: archetype(r), Language: r.Language,
		Description: r.Description, URL: url, Repo: r.HTMLURL,
		// Every public repo is forkable; a template repo is forkable ON PURPOSE.
		Forkable: true, Stars: r.Stars, Updated: r.PushedAt,
	}
}

// archetypes are ordered: the first match wins, so the more specific topic beats
// the generic one.
var archetypes = []struct {
	name  string
	words []string
}{
	{"model", []string{"model", "llm", "diffusion", "transformer", "inference"}},
	{"contract", []string{"contract", "solidity", "erc20", "defi"}},
	{"chain", []string{"blockchain", "consensus", "node", "vm", "evm", "wallet", "bridge"}},
	{"sdk", []string{"sdk", "client", "library", "api", "protocol", "mcp"}},
	{"template", []string{"template", "starter", "boilerplate", "scaffold"}},
	{"infra", []string{"infra", "operator", "kubernetes", "k8s", "ci", "runner", "registry", "gpu", "cuda"}},
	{"site", []string{"site", "website", "landing", "docs", "blog"}},
	{"app", []string{"app", "dashboard", "console", "ui", "web", "desktop", "mobile"}},
}

func archetype(r ghRepo) string {
	hay := strings.ToLower(strings.Join(append([]string{r.Name, r.Description}, r.Topics...), " "))
	if r.IsTemplate {
		return "template"
	}
	for _, a := range archetypes {
		for _, w := range a.words {
			if hasWord(hay, w) {
				return a.name
			}
		}
	}
	return ""
}

// hasWord matches w as a whole word, so "vm" does not fire on "vmware" and "ci"
// does not fire on every repo whose description contains "specific".
func hasWord(hay, w string) bool {
	for i := 0; i+len(w) <= len(hay); i++ {
		if hay[i:i+len(w)] != w {
			continue
		}
		if (i == 0 || !isWordByte(hay[i-1])) && (i+len(w) == len(hay) || !isWordByte(hay[i+len(w)])) {
			return true
		}
	}
	return false
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// repos pages one org's public repos. GH_PAT is used when present (the same token
// the git mirror already holds) purely for rate limit; the corpus is public
// either way, so an unset token degrades to a smaller hourly budget, not to a
// different answer.
func repos(ctx context.Context, org string) ([]ghRepo, error) {
	var out []ghRepo
	for page := 1; page <= maxPages; page++ {
		url := fmt.Sprintf("https://api.github.com/orgs/%s/repos?per_page=%d&type=public&page=%d", org, perPage, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return out, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := getenv("GH_PAT", ""); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return out, err
		}
		var page1 []ghRepo
		err = json.NewDecoder(resp.Body).Decode(&page1)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return out, fmt.Errorf("catalog: github %s: %s", org, resp.Status)
		}
		if err != nil {
			return out, err
		}
		out = append(out, page1...)
		if len(page1) < perPage {
			break
		}
	}
	return out, nil
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// sourceOrgs resolves the GitHub org → brand map. An override lists orgs as
// "githuborg:brand" or bare (brand = the org).
func sourceOrgs() map[string]string {
	raw := strings.TrimSpace(getenv(sourceOrgsEnv, ""))
	if raw == "" {
		return defaultOrgs
	}
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		gh, brand, ok := strings.Cut(strings.TrimSpace(part), ":")
		if gh = strings.TrimSpace(gh); gh == "" {
			continue
		}
		if !ok || strings.TrimSpace(brand) == "" {
			brand = gh
		}
		out[gh] = strings.TrimSpace(brand)
	}
	return out
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
