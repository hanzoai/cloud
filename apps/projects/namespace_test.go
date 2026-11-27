package projects

// namespace_test.go — a site is kept under the GitHub org its repository lives
// in, and only when its org holds that GitHub account (namespace.go).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// accounts answers the connection question from a fixed table: org → the GitHub
// accounts it holds.
func accounts(table map[string][]string) func(context.Context, string, string) (bool, error) {
	return func(_ context.Context, org, account string) (bool, error) {
		for _, a := range table[org] {
			if a == account {
				return true, nil
			}
		}
		return false, nil
	}
}

func project(t *testing.T, b []byte) projectsProject {
	t.Helper()
	var p projectsProject
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("decode project: %v (%s)", err, b)
	}
	return p
}

// THE NAMESPACE IS THE GITHUB ORG THE ORG HOLDS. zoo holds zooai: a site built
// from github.com/zooai/ngo is kept under zooai/ngo — its deployment's prefix, the
// prefix it serves from and the tag its pages carry. A repository in a GitHub org
// zoo does not hold, and no repository at all, keep the site under zoo.
//
// MUTATION: return remote.Owner(repo) without asking s.State.holds and the
// hanzoai case lands under hanzoai/; skip Keeps and the squat case lands under
// hanzo/; drop the namespace from sitePrefix's caller in startDeployment and the
// deployment opens at zoo/ngo.
func TestTheNamespaceIsTheGitHubOrgTheOrgHolds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		mounted.State.holds = accounts(map[string][]string{"zoo": {"zooai", "hanzo"}})
		if err := mounted.State.store.CreateProject(t.Context(), mkProject("hanzo", "hanzo-ai", "Hanzo")); err != nil {
			t.Fatal(err)
		}

		for _, c := range []struct{ slug, repo, want string }{
			{"ngo", "https://github.com/zooai/ngo", "zooai"},
			{"held-scp", "git@github.com:ZooAI/held-scp.git", "zooai"},
			{"borrowed", "https://github.com/hanzoai/borrowed", "zoo"},
			{"bare", "", "zoo"},
			// Only github.com has GitHub orgs: the forge's zooai is a mirror's name.
			{"mirrored", "https://git.hanzo.ai/zooai/mirrored", "zoo"},
			// zoo holds a GitHub account named hanzo, and hanzo is an org whose sites
			// are kept under hanzo/: the name is hanzo's, not the account's.
			{"squat", "https://github.com/hanzo/squat", "zoo"},
		} {
			body := map[string]any{"name": c.slug, "slug": c.slug}
			if c.repo != "" {
				body["repo"] = map[string]any{"url": c.repo}
			}
			code, b := do(t, app, http.MethodPost, "/v1/project", "zoo", body)
			if code != http.StatusCreated {
				t.Fatalf("create %s: want 201, got %d (%s)", c.slug, code, b)
			}
			if got := project(t, b).Namespace; got != c.want {
				t.Fatalf("%s (%s): namespace %q, want %q", c.slug, c.repo, got, c.want)
			}
		}

		code, b := do(t, app, http.MethodPost, "/v1/project/ngo/deployments", "zoo", map[string]any{"commit": "c0ffee"})
		if code != http.StatusAccepted {
			t.Fatalf("start deployment: want 202, got %d (%s)", code, b)
		}
		var d projectsDeployment
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		if d.Prefix != "zooai/ngo" {
			t.Fatalf("deployment prefix %q, want zooai/ngo", d.Prefix)
		}

		p, err := mounted.State.store.GetProject(t.Context(), "zoo", "ngo")
		if err != nil {
			t.Fatal(err)
		}
		if p.Org != "zoo" {
			t.Fatalf("the owner is the org that made it, got %q", p.Org)
		}
		if got := servePrefix(p); got != "zooai/ngo" {
			t.Fatalf("serves %q, want zooai/ngo", got)
		}
		if got := sites.CacheTag(p.Namespace, p.Slug); got != "site-zooai-ngo" {
			t.Fatalf("cache tag %q, want site-zooai-ngo: the ingress tags a page by its prefix", got)
		}
		if s := planeSite(sites.Site{Org: p.Org, Namespace: p.Namespace, Slug: p.Slug}, true); s.Namespace != "zooai" {
			t.Fatalf("the plane answer dropped the namespace: %+v", s)
		}
	})
}

// A NAMESPACE IS ITS HOLDER'S. hanzo has kept sites under hanzoai/ from the start;
// an org named hanzoai arrives later and keeps one site of its own. hanzo's next
// hanzoai repository still lands under hanzoai/, and zoo, which also holds a
// GitHub account named hanzoai, is kept under zoo.
//
// MUTATION: consult Keeps before Holder and the late org shuts hanzo out
// (hanzo/next); drop the holder's org check and zoo lands under hanzoai/.
func TestANamespaceIsItsHolders(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		mounted.State.holds = accounts(map[string][]string{"hanzo": {"hanzoai"}, "zoo": {"hanzoai"}})
		first := mkProject("hanzo", "hanzoai-cloud", "Cloud")
		first.Namespace = "hanzoai"
		late := mkProject("hanzoai", "home", "Home")
		late.CreatedAt = first.CreatedAt + 60
		for _, p := range []Project{first, late} {
			if err := mounted.State.store.CreateProject(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range []struct{ org, slug, want string }{
			{"hanzo", "next", "hanzoai"},
			{"zoo", "borrowed", "zoo"},
		} {
			body := map[string]any{"name": c.slug, "slug": c.slug, "repo": map[string]any{"url": "https://github.com/hanzoai/" + c.slug}}
			code, b := do(t, app, http.MethodPost, "/v1/project", c.org, body)
			if code != http.StatusCreated {
				t.Fatalf("create %s/%s: want 201, got %d (%s)", c.org, c.slug, code, b)
			}
			if got := project(t, b).Namespace; got != c.want {
				t.Fatalf("%s/%s: namespace %q, want %q", c.org, c.slug, got, c.want)
			}
		}
	})
}

// AN UNREADABLE CONNECTION REFUSES THE LINK. The integrations app is deployed and
// failing, so whether zoo holds zooai is unknown: the create is refused rather
// than kept under a namespace chosen by the outage, and nothing is left behind.
func TestAnUnreadableConnectionRefusesTheLink(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		mounted.State.holds = func(context.Context, string, string) (bool, error) {
			return false, errors.New("integrations: 502")
		}
		code, b := do(t, app, http.MethodPost, "/v1/project", "zoo",
			map[string]any{"name": "ngo", "slug": "ngo", "repo": map[string]any{"url": "https://github.com/zooai/ngo"}})
		if code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d (%s)", code, b)
		}
		if _, err := mounted.State.store.GetProject(t.Context(), "zoo", "ngo"); !errors.Is(err, errNotFound) {
			t.Fatalf("a refused link left a project: %v", err)
		}
		// The slug is not held either: a retry once the integrations app answers works.
		mounted.State.holds = accounts(map[string][]string{"zoo": {"zooai"}})
		if code, b := do(t, app, http.MethodPost, "/v1/project", "zoo",
			map[string]any{"name": "ngo", "slug": "ngo", "repo": map[string]any{"url": "https://github.com/zooai/ngo"}}); code != http.StatusCreated {
			t.Fatalf("retry: want 201, got %d (%s)", code, b)
		}
	})
}

// A PUBLISHED SITE MOVES ONLY ONTO A COPY OF ITSELF. zoo/ngo is published; zoo
// comes to hold zooai, and relinking ngo to zooai/ngo is refused until
// zooai/ngo/ holds the same index.html. Then it moves, and an edit that sends no
// repository leaves it where it is.
func TestAPublishedSiteMovesOnlyOntoItsCopy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		mounted.State.holds = accounts(nil)
		code, b := do(t, app, http.MethodPost, "/v1/project", "zoo",
			map[string]any{"name": "ngo", "slug": "ngo", "repo": map[string]any{"url": "https://github.com/zoo-apps/ngo"}})
		if code != http.StatusCreated || project(t, b).Namespace != "zoo" {
			t.Fatalf("create: %d %s", code, b)
		}
		f.seed(testBucket, "zoo/ngo/index.html", "<title>Zoo</title>")
		mounted.State.holds = accounts(map[string][]string{"zoo": {"zooai"}})

		code, b = do(t, app, http.MethodPatch, "/v1/project/ngo", "zoo",
			map[string]any{"repo": map[string]any{"url": "https://github.com/zooai/ngo"}})
		if code != http.StatusConflict {
			t.Fatalf("a move with nothing copied: want 409, got %d %s", code, b)
		}
		if p, _ := mounted.State.store.GetProject(t.Context(), "zoo", "ngo"); p.Namespace != "zoo" {
			t.Fatalf("a refused move moved the site to %q", p.Namespace)
		}
		f.seed(testBucket, "zooai/ngo/index.html", "<title>Zoo, older</title>")
		if code, b := do(t, app, http.MethodPatch, "/v1/project/ngo", "zoo",
			map[string]any{"repo": map[string]any{"url": "https://github.com/zooai/ngo"}}); code != http.StatusConflict {
			t.Fatalf("a move onto a different index.html: want 409, got %d %s", code, b)
		}
		f.seed(testBucket, "zooai/ngo/index.html", "<title>Zoo</title>")
		code, b = do(t, app, http.MethodPatch, "/v1/project/ngo", "zoo",
			map[string]any{"repo": map[string]any{"url": "https://github.com/zooai/ngo"}})
		if code != http.StatusOK || project(t, b).Namespace != "zooai" {
			t.Fatalf("a move onto its copy: want 200 under zooai, got %d %s", code, b)
		}
		mounted.State.holds = accounts(nil)
		code, b = do(t, app, http.MethodPatch, "/v1/project/ngo", "zoo", map[string]any{"name": "Zoo"})
		if code != http.StatusOK || project(t, b).Namespace != "zooai" {
			t.Fatalf("an edit with no repository moved the site: %d %s", code, b)
		}
	})
}

// THE SAME LINK SENT AGAIN IS AN EDIT, NOT A RELINK. A console saves the whole
// body, repository included. ngo is built from zooai/ngo, published under zoo,
// and zoo has since come to hold zooai: saving a new description succeeds and
// leaves it under zoo. Once its objects are copied to zooai/ngo/, the same save
// moves it there — the operator's move needs no other verb.
func TestTheSameLinkSentAgainMovesOnlyWhereSafe(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		mounted.State.holds = accounts(nil)
		repo := map[string]any{"url": "https://github.com/zooai/ngo"}
		code, b := do(t, app, http.MethodPost, "/v1/project", "zoo", map[string]any{"name": "ngo", "slug": "ngo", "repo": repo})
		if code != http.StatusCreated || project(t, b).Namespace != "zoo" {
			t.Fatalf("create: %d %s", code, b)
		}
		f.seed(testBucket, "zoo/ngo/index.html", "<title>Zoo</title>")
		mounted.State.holds = accounts(map[string][]string{"zoo": {"zooai"}})

		code, b = do(t, app, http.MethodPatch, "/v1/project/ngo", "zoo",
			map[string]any{"description": "Zoo Labs Foundation", "repo": map[string]any{"url": "git@github.com:zooai/ngo.git"}})
		if code != http.StatusOK || project(t, b).Namespace != "zoo" || project(t, b).Description != "Zoo Labs Foundation" {
			t.Fatalf("saving an unchanged link: want 200 under zoo, got %d %s", code, b)
		}
		mounted.State.holds = func(context.Context, string, string) (bool, error) { return false, errors.New("integrations: 502") }
		if code, b := do(t, app, http.MethodPatch, "/v1/project/ngo", "zoo",
			map[string]any{"description": "again", "repo": repo}); code != http.StatusOK {
			t.Fatalf("an unchanged link with integrations down: want 200, got %d %s", code, b)
		}
		mounted.State.holds = accounts(map[string][]string{"zoo": {"zooai"}})
		f.seed(testBucket, "zooai/ngo/index.html", "<title>Zoo</title>")
		code, b = do(t, app, http.MethodPatch, "/v1/project/ngo", "zoo", map[string]any{"repo": repo})
		if code != http.StatusOK || project(t, b).Namespace != "zooai" {
			t.Fatalf("the same link once copied: want 200 under zooai, got %d %s", code, b)
		}
	})
}

// A GITHUB NAMESPACE IS ENTERED EMPTY. zooai/fund/ holds a hand copy an edge route
// may serve: a site claiming the slug fund cannot be created or relinked into
// it, while a slug nothing is kept for enters freely.
func TestAGitHubNamespaceIsEnteredEmpty(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		app := mountApp(t)
		mounted.State.holds = accounts(map[string][]string{"zoo": {"zooai"}})
		f.seed(testBucket, "zooai/fund/index.html", "<title>Fund</title>")
		f.seed(testBucket, "zooai/.releases/vote/rel_x/index.html", "<title>Vote</title>")

		for _, slug := range []string{"fund", "vote"} {
			if code, b := do(t, app, http.MethodPost, "/v1/project", "zoo",
				map[string]any{"name": slug, "slug": slug, "repo": map[string]any{"url": "https://github.com/zooai/" + slug}}); code != http.StatusConflict {
				t.Fatalf("create %s into an occupied prefix: want 409, got %d %s", slug, code, b)
			}
		}
		code, b := do(t, app, http.MethodPost, "/v1/project", "zoo", map[string]any{"name": "fund", "slug": "fund"})
		if code != http.StatusCreated || project(t, b).Namespace != "zoo" {
			t.Fatalf("fund under zoo: %d %s", code, b)
		}
		if code, b := do(t, app, http.MethodPatch, "/v1/project/fund", "zoo",
			map[string]any{"repo": map[string]any{"url": "https://github.com/zooai/fund"}}); code != http.StatusConflict {
			t.Fatalf("relink into an occupied prefix: want 409, got %d %s", code, b)
		}
		code, b = do(t, app, http.MethodPost, "/v1/project", "zoo",
			map[string]any{"name": "fresh", "slug": "fresh", "repo": map[string]any{"url": "https://github.com/zooai/fresh"}})
		if code != http.StatusCreated || project(t, b).Namespace != "zooai" {
			t.Fatalf("fresh: %d %s", code, b)
		}
	})
}

// A SITE WITH RELEASES DOES NOT MOVE: their rows name the old prefix, and the
// pointer would serve a release that is not there.
func TestASiteWithReleasesDoesNotMove(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		startFakeS3(t)
		app := mountApp(t)
		mounted.State.holds = accounts(map[string][]string{"zoo": {"zooai"}})
		if code, b := do(t, app, http.MethodPost, "/v1/project", "zoo", map[string]any{"name": "ngo", "slug": "ngo"}); code != http.StatusCreated {
			t.Fatalf("create: %d %s", code, b)
		}
		id := "rel_" + strings.Repeat("a", 32)
		if err := mounted.State.store.PutRelease(t.Context(), Release{ID: id, Org: "zoo", Slug: "ngo", Prefix: releasePrefix("zoo", "ngo", id), CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		if code, b := do(t, app, http.MethodPatch, "/v1/project/ngo", "zoo",
			map[string]any{"repo": map[string]any{"url": "https://github.com/zooai/ngo"}}); code != http.StatusConflict {
			t.Fatalf("want 409, got %d %s", code, b)
		}
	})
}

// A RELEASE IS PROMOTED FROM THE ORG'S OWN BYTES. acme keeps widgets under the
// GitHub namespace acme-gh, which is also the name of another org: that org's
// `acme-gh/` holds widgets' bytes, and it may not promote them.
func TestAReleaseSourceIsNeverAnotherOrgsSite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		startFakeS3(t)
		mountApp(t)
		widgets := mkProject("acme", "widgets", "Widgets")
		widgets.Namespace = "acme-gh"
		home := mkProject("acme-gh", "home", "Home")
		for _, p := range []Project{widgets, home} {
			if err := mounted.State.store.CreateProject(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}
		for _, source := range []string{"widgets", "widgets/assets", ".releases/widgets/rel_x"} {
			if _, err := promote(mounted, t.Context(), "acme-gh", home, source); !errors.Is(err, errBadSource) {
				t.Fatalf("promote %q: %v, want errBadSource", source, err)
			}
		}
	})
}

// ONE SITE PER PREFIX, whichever orgs hold the projects.
func TestOnePrefixHoldsOneSite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store := newTestStore(t)
		a := mkProject("zoo", "ngo", "Zoo")
		a.Namespace = "zooai"
		b := mkProject("hanzo", "ngo", "Hanzo")
		b.Namespace = "zooai"
		if err := store.CreateProject(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateProject(t.Context(), b); !errors.Is(err, errConflict) {
			t.Fatalf("a second site at zooai/ngo: %v, want errConflict", err)
		}
	})
}

// A PROJECTS PEER THAT STATES NO NAMESPACE keeps its sites under the org, which
// is where every site was before the namespace existed.
func TestAPlaneSiteWithNoNamespaceIsTheOrgs(t *testing.T) {
	s, ok := sites.SiteOf(&sites.PlaneSite{Found: true, Org: "zoo", Slug: "ngo"})
	if !ok || s.Namespace != "zoo" || sites.CacheTag(s.Namespace, s.Slug) != "site-zoo-ngo" {
		t.Fatalf("%+v", s)
	}
}

// EVERY SITE THAT EXISTS KEEPS ITS PREFIX. The column backfills to the org, so a
// database written before it serves every site from `<org>/<slug>/` exactly as
// before the binary changed.
func TestTheBackfillKeepsEverySiteWhereItIs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store := newTestStore(t)
		if err := store.CreateProject(t.Context(), mkProject("hanzo", "hanzo-ai", "Hanzo")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE projects SET namespace=''`); err != nil {
			t.Fatal(err)
		}
		if err := store.migrate(); err != nil {
			t.Fatal(err)
		}
		p, err := store.GetProject(t.Context(), "hanzo", "hanzo-ai")
		if err != nil {
			t.Fatal(err)
		}
		if p.Namespace != "hanzo" || servePrefix(p) != "hanzo/hanzo-ai" {
			t.Fatalf("namespace %q serves %q, want hanzo/hanzo-ai", p.Namespace, servePrefix(p))
		}
	})
}

// A SITE IS BILLED TO THE ORG THAT KEEPS IT, WHEREVER ITS NAMESPACE. zoo keeps ngo
// under zooai: zoo is measured for it. acme-gh is an IAM org and also a GitHub
// account another org holds: that org's site kept under acme-gh/ is not acme-gh's.
func TestASiteIsMeasuredForItsOrgWhereverItIsKept(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		store := newTestStore(t)
		ngo := mkProject("zoo", "ngo", "Zoo")
		ngo.Namespace = "zooai"
		lodger := mkProject("acme", "widgets", "Widgets")
		lodger.Namespace = "acme-gh"
		for _, p := range []Project{ngo, lodger, mkProject("acme-gh", "home", "Home")} {
			if err := store.CreateProject(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}
		f.seedSparse(testBucket, "zooai/ngo/blue.webm", 7)
		f.seedSparse(testBucket, "zooai/.releases/ngo/rel_x/index.html", 3)
		f.seedSparse(testBucket, "zoo/builds/v1/out.tar", 5)
		f.seedSparse(testBucket, "acme-gh/widgets/app.js", 11)
		f.seedSparse(testBucket, "acme-gh/.releases/widgets/rel_y/app.js", 13)
		f.seedSparse(testBucket, "acme-gh/home/index.html", 17)
		list := lister(t, store)
		for org, want := range map[string]int64{"zoo": 7 + 3 + 5, "acme": 11 + 13, "acme-gh": 17} {
			got, err := list(t.Context(), org)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("%s holds %d bytes, want %d", org, got, want)
			}
		}
	})
}
