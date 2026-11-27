package projects

// An estate org's site is kept as a release each time a deployment makes it
// live, and rolling it back serves one of those releases again (record.go).
//
// hanzo.ai is published file by file into the prefix it serves, so before this
// a publish that went wrong (2026-10-26: half its assets missing) could only be
// undone by publishing again. These tests drive the CI path — open a deployment,
// upload into the site's prefix, complete it live — and then the two plane ops the
// deploy console reads and rolls a site back through.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// keptHarness mounts projects over the S3 double with a funded commerce.
func keptHarness(t *testing.T) (*fakeS3, *zip.App) {
	t.Helper()
	f := startFakeS3(t)
	bs := &billServer{available: 1000000}
	return f, mountSites(t, nil, bs.start(t))
}

// goLive is one CI publish of org's slug: open a deployment, upload a page and a
// fingerprinted asset named by marker into the site's prefix, complete it live
// naming both, and wait for the keep it starts.
func goLive(t *testing.T, app *zip.App, f *fakeS3, org, slug, marker string) projectsDeployment {
	t.Helper()
	code, b := do(t, app, http.MethodPost, "/v1/project/"+slug+"/deployments", org, map[string]any{"commit": "c-" + marker})
	if code != http.StatusAccepted {
		t.Fatalf("open %s: %d %s", marker, code, b)
	}
	var d projectsDeployment
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	asset := "_next/static/chunks/" + marker + ".js"
	f.seed(testBucket, org+"/"+slug+"/index.html", "<title>"+marker+"</title>")
	f.seed(testBucket, org+"/"+slug+"/"+asset, "console.log('"+marker+"')")
	code, b = do(t, app, http.MethodPost, "/v1/project/"+slug+"/deployments/"+d.ID+"/complete", org,
		map[string]any{"status": "live", "keys": []string{"index.html", asset}})
	if code != http.StatusOK {
		t.Fatalf("complete %s: %d %s", marker, code, b)
	}
	mounted.State.keeping.wg.Wait()
	return d
}

// siteOfOrg is the one-site read the deploy console makes, as org.
func siteOfOrg(t *testing.T, org, slug string) client.SiteApp {
	t.Helper()
	out, err := planeSiteApps(as(org), &client.SiteAppsIn{Slug: slug})
	if err != nil {
		t.Fatalf("site %s/%s: %v", org, slug, err)
	}
	if len(out.Sites) != 1 {
		t.Fatalf("site %s/%s: %d sites, want 1", org, slug, len(out.Sites))
	}
	return out.Sites[0]
}

func httpStatus(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func TestAnEstateSiteIsKeptAsAReleaseWhenItGoesLive(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, app := keptHarness(t)
		newSite(t, app, "hanzo", "hanzo-ai")
		d := goLive(t, app, f, "hanzo", "hanzo-ai", "v1")

		st := siteOfOrg(t, "hanzo", "hanzo-ai")
		if !releaseIDRE.MatchString(st.Revision) {
			t.Fatalf("revision = %q, want the release the live files were kept as", st.Revision)
		}
		if st.Version != 1 || st.Commit != "c-v1" {
			t.Fatalf("site = %+v, want deployment v1 at c-v1", st)
		}
		if len(st.History) != 1 || st.History[0].Release != st.Revision || st.History[0].Deploy != d.ID {
			t.Fatalf("history = %+v, want the one live deployment and its release", st.History)
		}
		kept := "hanzo/.releases/hanzo-ai/" + st.Revision + "/"
		if body, ok := f.body(testBucket, kept+"index.html"); !ok || !strings.Contains(body, "v1") {
			t.Fatalf("kept index.html = %q (%v), want v1's page", body, ok)
		}
		// Serving is untouched: the site still reads its own prefix.
		if p := getProject(t, "hanzo", "hanzo-ai"); p.CurrentRelease != "" {
			t.Fatalf("keeping moved the serving pointer to %q", p.CurrentRelease)
		}
		// The kept copy is the platform's, so the org's storage does not count it.
		held, err := footprint(context.Background(), mounted.State.store, f, testBucket, "hanzo")
		if want := f.bytesUnder(testBucket + "/hanzo/hanzo-ai/"); err != nil || held != want || want == 0 {
			t.Fatalf("footprint = %d (%v), want the live files' %d", held, err, want)
		}

		// The list answers every site that has served, without history.
		all, err := planeSiteApps(as("hanzo"), &client.SiteAppsIn{})
		if err != nil {
			t.Fatal(err)
		}
		if len(all.Sites) != 1 || all.Sites[0].Revision != st.Revision || all.Sites[0].Version != 1 || all.Sites[0].History != nil {
			t.Fatalf("list = %+v, want hanzo-ai at v1 with no history", all.Sites)
		}
	})
}

func TestRollingBackServesTheReleaseBeforeTheBadPublish(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, app := keptHarness(t)
		newSite(t, app, "hanzo", "hanzo-ai")
		goLive(t, app, f, "hanzo", "hanzo-ai", "good")
		goLive(t, app, f, "hanzo", "hanzo-ai", "bad")

		st := siteOfOrg(t, "hanzo", "hanzo-ai")
		if len(st.History) != 2 || st.History[0].Version != 2 || st.History[1].Version != 1 {
			t.Fatalf("history = %+v, want v2 then v1", st.History)
		}
		bad, good := st.History[0].Release, st.History[1].Release
		if st.Revision != bad || bad == good {
			t.Fatalf("revision = %q, want the bad publish's %q (good %q)", st.Revision, bad, good)
		}

		// It serves what it serves already: there is nothing to roll back to there.
		if _, err := planeSiteRollback(as("hanzo"), &client.SiteRollbackIn{Slug: "hanzo-ai", Release: bad}); httpStatus(err) != http.StatusConflict {
			t.Fatalf("rollback to the live release: %v, want 409", err)
		}
		// A release the site never had, and a malformed one, are not found.
		for _, rel := range []string{"rel_00000000000000000000000000000000", "../../acme/shop"} {
			if _, err := planeSiteRollback(as("hanzo"), &client.SiteRollbackIn{Slug: "hanzo-ai", Release: rel}); httpStatus(err) != http.StatusNotFound {
				t.Fatalf("rollback to %q: %v, want 404", rel, err)
			}
		}
		// Another org cannot name it at all.
		if _, err := planeSiteRollback(as("acme"), &client.SiteRollbackIn{Slug: "hanzo-ai", Release: good}); httpStatus(err) != http.StatusNotFound {
			t.Fatalf("rollback by another org: %v, want 404", err)
		}
		if _, err := planeSiteApps(as("acme"), &client.SiteAppsIn{Slug: "hanzo-ai"}); httpStatus(err) != http.StatusNotFound {
			t.Fatalf("read by another org: %v, want 404", err)
		}

		got, err := planeSiteRollback(as("hanzo"), &client.SiteRollbackIn{Slug: "hanzo-ai", Release: good})
		if err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if got.Revision != good || got.Version != 1 || got.Commit != "c-good" {
			t.Fatalf("after rollback = %+v, want release %s from v1", got, good)
		}
		if p := getProject(t, "hanzo", "hanzo-ai"); p.CurrentRelease != good || p.Status != "live" {
			t.Fatalf("project = pointer %q status %q, want %s live", p.CurrentRelease, p.Status, good)
		}

		// The next publish serves itself, as a deployment always has, and is kept.
		goLive(t, app, f, "hanzo", "hanzo-ai", "fixed")
		st = siteOfOrg(t, "hanzo", "hanzo-ai")
		if p := getProject(t, "hanzo", "hanzo-ai"); p.CurrentRelease != "" {
			t.Fatalf("a publish after a rollback left the pointer at %q", p.CurrentRelease)
		}
		if st.Version != 3 || st.Revision != st.History[0].Release {
			t.Fatalf("after the fix = %+v, want v3 serving its own release", st)
		}
	})
}

// Retention keeps the newest three, so the site's release space stays bounded.
func TestKeptReleasesAreBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, app := keptHarness(t)
		newSite(t, app, "hanzo", "handbook")
		for _, m := range []string{"a", "b", "c", "d", "e"} {
			goLive(t, app, f, "hanzo", "handbook", m)
		}
		rows, err := mounted.State.store.ListReleases(context.Background(), "hanzo", "handbook", 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != keptReleases {
			t.Fatalf("%d releases kept, want %d", len(rows), keptReleases)
		}
		if st := siteOfOrg(t, "hanzo", "handbook"); len(st.History) != keptReleases || st.History[0].Version != 5 {
			t.Fatalf("history = %+v, want the newest %d", st.History, keptReleases)
		}
	})
}

// A customer's site is billed for what it stores, so it is not kept: it serves as
// it always did, and has nothing to roll back to.
func TestACustomerSiteIsNotKept(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, app := keptHarness(t)
		newSite(t, app, "acme", "shop")
		goLive(t, app, f, "acme", "shop", "v1")

		st := siteOfOrg(t, "acme", "shop")
		if st.Revision != "" || len(st.History) != 0 || st.Status != "live" {
			t.Fatalf("customer site = %+v, want live with no kept release", st)
		}
		if n := f.count(testBucket + "/acme/.releases/"); n != 0 {
			t.Fatalf("%d objects kept for a customer site", n)
		}
	})
}

// A kept release serves the publisher's own objects again, so it keeps what the
// publisher wrote on each: a Markdown twin stays text/markdown.
func TestAKeptReleaseKeepsWhatThePublisherWrote(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, app := keptHarness(t)
		newSite(t, app, "hanzo", "papers")
		code, b := do(t, app, http.MethodPost, "/v1/project/papers/deployments", "hanzo", map[string]any{})
		if code != http.StatusAccepted {
			t.Fatalf("open: %d %s", code, b)
		}
		var d projectsDeployment
		_ = json.Unmarshal(b, &d)
		f.seed(testBucket, "hanzo/papers/index.html", "<title>papers</title>")
		f.seedAt(testBucket, "hanzo/papers/index.md", "# papers", "text/markdown; charset=utf-8", time.Now())
		if code, b := do(t, app, http.MethodPost, "/v1/project/papers/deployments/"+d.ID+"/complete", "hanzo",
			map[string]any{"status": "live", "keys": []string{"index.html", "index.md"}}); code != http.StatusOK {
			t.Fatalf("complete: %d %s", code, b)
		}
		mounted.State.keeping.wg.Wait()
		st := siteOfOrg(t, "hanzo", "papers")
		info, err := f.Stat(context.Background(), testBucket, "hanzo/.releases/papers/"+st.Revision+"/index.md")
		if err != nil {
			t.Fatal(err)
		}
		if info.ContentType != "text/markdown; charset=utf-8" {
			t.Fatalf("kept index.md is %q, want the publisher's text/markdown", info.ContentType)
		}
	})
}
