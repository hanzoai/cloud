package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/projects"
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
//   - a live site published by the PLATFORM's own org → the published corpus IF
//     the page it serves passes admission (gate.go), else the platform's OWN
//     corpus with the reason attached
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

// source is one GitHub org the corpus is built from, and it says the two things
// a place can say about what is in it: the BRAND a person browses it by — nobody
// looks for "zoo-apps", they look for zoo — and the ORIGIN, what those repos are
// to a visitor (origin.go). Both are facts about the org, so they live in one
// table; splitting them into two maps is how they would come to disagree.
type source struct{ brand, origin string }

// defaultOrgs IS the fleet. Which org a repo sits in is the whole derivation of
// its lane: the templates org holds starters to fork FROM, the apps orgs and the
// auto-publish community org hold what people BUILT, and everything else is our
// own software.
var defaultOrgs = map[string]source{
	"hanzoai":         {"hanzo", OriginProduct},
	"hanzo-templates": {"hanzo", OriginTemplate},
	"hanzo-apps":      {"hanzo", OriginCommunity},
	// hanzo-community is where a public fork auto-publishes. It is listed before
	// it has repos on purpose: the lane must be fed the hour that lands, not the
	// week somebody remembers to add a line here.
	"hanzo-community": {"hanzo", OriginCommunity},
	"luxfi":           {"lux", OriginProduct},
	"luxcpp":          {"lux", OriginProduct},
	"lux-apps":        {"lux", OriginCommunity},
	"zooai":           {"zoo", OriginProduct},
	"zoo-apps":        {"zoo", OriginCommunity},
	"zenlm":           {"zen", OriginProduct},
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
	License     struct {
		SPDX string `json:"spdx_id"`
	} `json:"license"`
	// Parent is the work a fork was taken FROM. GitHub's org listing omits it and
	// only the single-repo read carries it, which is why crediting a fork costs one
	// extra request (see credit).
	Parent *struct {
		FullName string `json:"full_name"`
	} `json:"parent"`
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
	pub, ferr := fromOrgs(ctx)
	// at is where each published row already sits, so a live site can be FOLDED
	// onto the repo it was built from instead of replacing it. Both key on
	// "<org>/<name>" and so does the index, so before this a demo silently
	// overwrote its own source row and took the repo link down with it —
	// which is exactly why no site row could name where it came from.
	at := make(map[string]int, len(pub))
	for i, e := range pub {
		at[e.ID] = i
	}
	byOrg := map[string][]Entry{}
	sites, err := liveSites(ctx)
	if err != nil {
		ferr = err
	}
	platform := getenv(platformOrgEnv, "hanzo")
	// Which live slugs are OUR starters' demos, read forward from the curated
	// gallery once per pass (origin.go) — the one thing that tells a template's
	// own demo apart from an app somebody built on the platform.
	starter := starters()
	var ours []Entry
	for _, s := range sites {
		e := fromSite(s, starter)
		if s.Org != platform {
			byOrg[s.Org] = append(byOrg[s.Org], e)
			continue
		}
		ours = append(ours, e)
	}
	// Being ours is necessary to be published, not sufficient. The gate reads what
	// each of our sites actually SERVES and holds back the ones with nothing to
	// show (gate.go). A held site is not deleted and its repo row is not touched —
	// it lands in OUR OWN corpus carrying the reason it is not public, so a demo
	// that drops off the public lens can be explained instead of just vanishing.
	show, hold := admit(ctx, ours)
	for _, e := range show {
		if i, ok := at[e.ID]; ok {
			pub[i] = fold(pub[i], e)
			continue
		}
		at[e.ID] = len(pub)
		pub = append(pub, e)
	}
	if len(hold) > 0 {
		// Only when there IS something to hold: creating the key unconditionally
		// would make the "read nothing, reconcile nothing" guard below stop firing.
		byOrg[platform] = append(byOrg[platform], hold...)
	}
	if len(pub) == 0 && len(byOrg) == 0 {
		return nil, nil, ferr
	}
	return pub, byOrg, ferr
}

// orgRepos reads every source org's public, un-archived repos. It is the FIRST
// of the corpus's two sources, and a package var for the same reason liveSites
// is one: the assembly is testable without a live GitHub.
func orgRepos(ctx context.Context) ([]Entry, error) {
	at := map[string]int{}
	var out []Entry
	var ferr error
	for gh, src := range sourceOrgs() {
		rows, err := repos(ctx, gh)
		if err != nil {
			ferr = err
			continue
		}
		for _, r := range rows {
			if r.Archived || r.Private {
				continue // dead or not ours to show — a corpus decision, not a forkable one
			}
			if r.Fork && !credit(ctx, &r) {
				continue // somebody else's, and we cannot say whose — so we do not list it
			}
			e := fromRepo(r, src)
			i, seen := at[e.ID]
			if !seen {
				at[e.ID] = len(out)
				out = append(out, e)
				continue
			}
			if canonical(e, out[i]) {
				out[i] = e
			}
		}
	}
	return out, ferr
}

// credit fills in the work a fork was taken from, and reports whether the row may
// be listed at all. A fork holds somebody else's code under one of OUR org
// headers, so it is either ATTRIBUTED or it is NOT LISTED — there is no third
// option where we show it with no author on it and let the page's title imply
// the answer.
//
// The cost is one extra request per fork (a few dozen an hour, against a listing
// pass of about a dozen), and it is spent on the forks only. A read that fails
// drops the row rather than admitting it uncredited: the safe direction for
// "whose is this" is silence, not a guess.
func credit(ctx context.Context, r *ghRepo) bool {
	full, err := repoRead(ctx, r.FullName)
	if err != nil || full.Parent == nil || full.Parent.FullName == "" {
		return false
	}
	r.Parent, r.License = full.Parent, full.License
	return true
}

// canonical breaks a name collision between two orgs claiming one catalog id —
// hanzoai/ui and hanzo-apps/ui are both "hanzo/ui". sourceOrgs is a map, so
// without this the winner is Go's iteration order, and hanzo/ui would flip its
// own forkable answer from sync to sync. Ours beats a vendored fork; between
// two of ours, the one people actually use.
func canonical(a, b Entry) bool {
	if a.Forkable != b.Forkable {
		return a.Forkable
	}
	if a.Stars != b.Stars {
		return a.Stars > b.Stars
	}
	// Both ours, both unstarred (hanzoai/gallery vs hanzo-templates/gallery). The
	// tiebreak is arbitrary ON PURPOSE — what matters is that it is TOTAL, so the
	// row does not change its repo link every time the map iterates differently.
	return a.Repo < b.Repo
}

// fromSite maps one live project to a catalog row. Repo and Template are READ
// from the project, never inferred: the platform already records what a site was
// built from and what it was forked from, and a catalog that guessed either
// would be guessing about authorship.
func fromSite(s projects.LiveSite, starter map[string]bool) Entry {
	e := Entry{
		ID: s.Org + "/" + s.Slug, Org: s.Org, Name: s.Slug, Title: s.Name,
		Kind: "site", Archetype: "site", URL: s.URL,
		Repo: s.Repo, Template: s.ForkedFrom,
		// Which lane a demo belongs in is derived from what it IS — our curated
		// starter's own demo, a remix with recorded lineage, a credited kit — never
		// from the fact that we happen to be the ones hosting it (origin.go).
		Origin:  siteOrigin(s, starter),
		Updated: time.Unix(s.UpdatedAt, 0).UTC().Format(time.RFC3339),
		// Authorship is CARRIED, never re-derived: the store already gates Official
		// behind an admin, so the corpus repeats that answer instead of forming an
		// opinion of its own.
		Official: s.Official, Upstream: s.Upstream, License: s.License,
	}
	// You may fork it if there is a source to fork AND nobody has declared the
	// work somebody else's. "Fork this" is an invitation, and a credited kit is
	// ours to SHOW, never ours to hand out.
	e.Forkable = e.Repo != "" && e.Upstream == ""
	return e
}

// fold merges a live site onto the repo it was built from. The site owns what is
// LIVE — its URL, its human title, when it last deployed. The repo owns what is
// SOURCE — the link, the description, the language, the stars. Nothing the site
// does not carry is blanked, because the merged row is the only one that can
// answer the question the catalog exists for: show me the demo AND its code.
func fold(repo, site Entry) Entry {
	out := repo
	out.Kind, out.Archetype = site.Kind, site.Archetype
	out.URL, out.Updated = site.URL, site.Updated
	if site.Title != "" {
		out.Title = site.Title
	}
	if site.Repo != "" {
		out.Repo = site.Repo // a project that DECLARES its source outranks the name match
	}
	if site.Template != "" {
		out.Template = site.Template
	}
	// The REPO's origin wins by default — which org the source lives in is a
	// harder fact than what a demo's slug spells — but the two things the SITE
	// knows and the repo cannot are both overrides, in strengthening order:
	// recorded lineage means somebody built this FROM something, and a declared
	// credit means it was never ours at all.
	out.Origin = repo.Origin
	if out.Template != "" {
		out.Origin = OriginCommunity
	}
	if site.Upstream != "" {
		out.Origin = OriginThirdParty
	}
	// A DECLARED credit is a veto, not a tie-break. The repo half infers "ours"
	// from the org the repo sits in; the site half is a human statement that the
	// work is somebody else's. An inference must never outrank a statement, or
	// hosting a bought kit in our own template org would launder it into a
	// first-party example — which is precisely how this went wrong.
	//
	// It is a veto and not a REPLACEMENT: the repo half's credit survives when the
	// site declares none, because a GitHub fork's parent is a credit too and
	// blanking it would leave a third-party row with no author on it.
	if site.Upstream != "" {
		out.Upstream, out.License = site.Upstream, site.License
	}
	out.Official = (repo.Official || site.Official) && site.Upstream == ""
	// Forkable stays the REPO's answer, minus the same veto: hosting a demo of
	// someone else's fork does not make it ours to hand over.
	out.Forkable = repo.Forkable && out.Repo != "" && site.Upstream == ""
	return out
}

// fromRepo maps one repo to a catalog row. The archetype is derived from the
// repo's own topics and name rather than guessed by a model: a wrong archetype is
// worse than none, because it silently hides the row from the browse rail.
func fromRepo(r ghRepo, src source) Entry {
	url := strings.TrimSpace(r.Homepage)
	if url != "" && !strings.HasPrefix(url, "http") {
		url = "https://" + url
	}
	// The org the repo lives in says which lane it is in — EXCEPT that GitHub
	// itself says a fork holds upstream's code, and that outranks the address. A
	// starter we vendored from somebody else is not a starter of ours.
	origin, upstream := src.origin, ""
	if r.Fork {
		origin = OriginThirdParty
		if r.Parent != nil {
			upstream = r.Parent.FullName
		}
	}
	return Entry{
		ID: src.brand + "/" + r.Name, Org: src.brand, Name: r.Name, Title: r.Name,
		Kind: "repo", Origin: origin, Archetype: archetype(r), Language: r.Language,
		Description: r.Description, URL: url, Repo: r.HTMLURL,
		// The credit a fork is listed on at all. License is what GitHub can name;
		// when it can name none the row still carries WHOSE work it is, which is
		// the half that keeps us from implying it is ours.
		Upstream: upstream, License: spdx(r),
		// Forkable is the one axis with teeth, so it has to be able to say no.
		// A public repo of ours is yours to take. A repo that is itself a FORK
		// of a third-party upstream is not: its license and its lineage belong
		// to that upstream, and the honest fork button for it points there.
		Forkable: !r.Fork, Stars: r.Stars, Updated: r.PushedAt,
		// Authorship rests on the same one fact, for the same reason: a repo in an
		// org we own is ours, unless GitHub itself says it is a fork — in which
		// case the code is upstream's and the badge would be a lie. Derived from a
		// fact GitHub already asserts, never a guess about what is inside.
		Official: !r.Fork,
	}
}

// spdx is the licence of the work a fork carries, as GitHub names it. Only a
// fork gets one: on our own repo the field would be noise, because License here
// means "the terms SOMEBODY ELSE's work is under". NOASSERTION is GitHub saying
// it could not identify the licence, which is not a licence name, so it reads as
// none — the row still carries its upstream, which is the half that matters.
func spdx(r ghRepo) string {
	if !r.Fork || r.License.SPDX == "NOASSERTION" {
		return ""
	}
	return r.License.SPDX
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
// the git mirror already holds) purely for rate limit; the corpus is PUBLIC, so
// the token changes the hourly budget, never the answer — which is exactly why an
// expired or wrong-scoped token must not be able to empty the catalog. A 401/403
// is therefore retried once anonymously rather than reported as a failed source.
func repos(ctx context.Context, org string) ([]ghRepo, error) {
	var out []ghRepo
	for page := 1; page <= maxPages; page++ {
		var rows []ghRepo
		err := ghJSON(ctx, fmt.Sprintf(
			"https://api.github.com/orgs/%s/repos?per_page=%d&type=public&page=%d", org, perPage, page), &rows)
		if err != nil {
			return out, err
		}
		out = append(out, rows...)
		if len(rows) < perPage {
			break
		}
	}
	return out, nil
}

// repoRead is the SINGLE-repo read, and the only reason it exists is `parent`:
// GitHub does not carry it in an org listing, and a fork with no named parent is
// a row we refuse to publish (credit). A package var so the corpus assembly stays
// testable without a live GitHub, like every other source here.
var repoRead = func(ctx context.Context, fullName string) (ghRepo, error) {
	var out ghRepo
	err := ghJSON(ctx, "https://api.github.com/repos/"+fullName, &out)
	return out, err
}

// errAuth marks a credential rejection, the one failure worth retrying without one.
var errAuth = errors.New("catalog: github rejected the credential")

func isAuth(err error) bool { return errors.Is(err, errAuth) }

// ghJSON is the ONE GitHub read. GH_PAT is used when present (the same token the
// git mirror already holds) purely for rate limit; the corpus is PUBLIC, so the
// token changes the hourly budget, never the answer — which is exactly why an
// expired or wrong-scoped token must not be able to empty the catalog, and why a
// 401/403 is retried once anonymously rather than reported as a failed source.
func ghJSON(ctx context.Context, url string, out any) error {
	err := ghOnce(ctx, url, getenv("GH_PAT", ""), out)
	if isAuth(err) {
		err = ghOnce(ctx, url, "", out)
	}
	return err
}

func ghOnce(ctx context.Context, url, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s: %s", errAuth, url, resp.Status)
	default:
		return fmt.Errorf("catalog: github %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// sourceOrgs resolves the GitHub org → source table. An override lists orgs as
// "githuborg:brand:origin", "githuborg:brand" or bare (brand = the org). An
// unstated origin is `product`: whoever pointed the catalog at an org configured
// it as a source of THEIRS, and "our own software" is the answer that claims the
// least — it neither offers the repos as starters nor credits them to a stranger.
func sourceOrgs() map[string]source {
	raw := strings.TrimSpace(getenv(sourceOrgsEnv, ""))
	if raw == "" {
		return defaultOrgs
	}
	out := map[string]source{}
	for _, part := range strings.Split(raw, ",") {
		f := strings.Split(strings.TrimSpace(part), ":")
		gh := strings.TrimSpace(f[0])
		if gh == "" {
			continue
		}
		s := source{brand: gh, origin: OriginProduct}
		if len(f) > 1 && strings.TrimSpace(f[1]) != "" {
			s.brand = strings.TrimSpace(f[1])
		}
		if len(f) > 2 && strings.TrimSpace(f[2]) != "" {
			s.origin = strings.TrimSpace(f[2])
		}
		out[gh] = s
	}
	return out
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
