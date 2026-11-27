package projects

// build_test.go — a site a sandbox builds from the project's own repository:
// the deployment it opens, the grant it hands over, how it ends, the board that
// lists it, and the template fork that starts one at once.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// runner is a stand-in for the agent app's publish op: it records who asked to
// build what, and answers as the real one does once it has opened a deployment.
type runner struct {
	mu    sync.Mutex
	asked []asking
}

type asking struct {
	org string
	// user is the person the call stated: the build's sandbox is leased as theirs.
	user string
	in   client.PublishIn
}

func (r *runner) calls() []asking {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]asking(nil), r.asked...)
}

// serveRunner stands the agent app's publish op up on its socket. The socket
// directory is the one listen(t) set.
func serveRunner(t *testing.T) *runner {
	t.Helper()
	r := &runner{}
	app := zip.New(zip.Config{AppName: agentpeer.App})
	app.Post("/agents/coding/publish",
		func(ctx context.Context, in *client.PublishIn) (*client.Publishing, error) {
			r.mu.Lock()
			r.asked = append(r.asked, asking{org: cloud.Who(ctx).Org, user: zip.CallerOf(ctx).User, in: *in})
			r.mu.Unlock()
			return &client.Publishing{Deployment: "dep_x", Project: in.Project, Ref: "main", Status: "building"}, nil
		}, zip.WithOperationID(client.AgentsCodingPublish))
	go func() { _ = app.Listen(zip.SocketPath(agentpeer.App)) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 200 {
		if c, err := net.Dial("unix", zip.SocketPath(agentpeer.App)); err == nil {
			_ = c.Close()
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the agent stand-in never began listening")
	return nil
}

// as is a plane call's context for org.
func as(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "u_" + org})
}

// mountBuilding is mountForking with object storage that can mint a grant.
func mountBuilding(t *testing.T) (*zip.App, *workshop, *fakeS3) {
	t.Helper()
	s3 := startFakeS3(t)
	t.Setenv("S3_PUBLIC_ENDPOINT", "s3.test")
	app, w := mountForking(t)
	return app, w, s3
}

// A TEMPLATE TAKEN IS PUBLISHED AT ONCE: the fork asks the agent app to build
// the org's copy, as the org and as the person who took it, and names the
// project and nothing else. The person is who the build's sandbox is leased by
// and paid from: a call that named only the org named no wallet, and every
// build's lease was refused.
func TestACopyIsPublishedAtOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountForking(t)
		r := serveRunner(t)
		p := forked(t, app, "acme", map[string]any{"slug": "synapse"})
		got := r.calls()
		if len(got) != 1 || got[0].org != "acme" || got[0].in != (client.PublishIn{Project: p.Slug}) {
			t.Fatalf("the fork asked %+v", got)
		}
		if got[0].user != "u_acme" {
			t.Fatalf("the fork asked for its build as %q, not as the person who took the template", got[0].user)
		}
		forked(t, app, "hanzo", map[string]any{"slug": "synapse", "target": "synapse-estate"})
		if n := len(r.calls()); n != 1 {
			t.Fatalf("the estate's fork asked for a build: %d calls", n)
		}
	})
}

// A COPY THAT CANNOT START ITS BUILD IS STILL A COPY: nothing answers the
// publish, and the fork stands, unpublished.
func TestAForkThatCannotPublishStillForks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountForking(t)
		p := forked(t, app, "acme", map[string]any{"slug": "synapse"})
		if p.Status != "draft" || p.LiveURL != "" {
			t.Fatalf("an unpublished copy reads %+v", p)
		}
	})
}

// A DERIVED SLUG IS NUMBERED PAST AN ADDRESS ANOTHER PROJECT SERVES AT, so the
// copy has one of its own to publish to: another org's live project of the same
// slug, and a host another project is bound to.
func TestACopyIsNumberedPastAnotherProjectsAddress(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountForking(t)
		ctx := t.Context()
		live := mkProject("globex", "synapse", "Synapse")
		live.Status = "live"
		if err := mounted.State.store.CreateProject(ctx, live); err != nil {
			t.Fatal(err)
		}
		if err := mounted.State.store.BindHost(ctx, "synapse-2", "initech", "synapse-2", 1); err != nil {
			t.Fatal(err)
		}
		p := forked(t, app, "acme", map[string]any{"slug": "synapse"})
		if p.Slug != "synapse-3" || !strings.HasSuffix(p.Repo.URL, "/acme/synapse-3.git") {
			t.Fatalf("the copy is %q at %q", p.Slug, p.Repo.URL)
		}
		// A slug the caller names is taken as named, and a name another org holds is
		// the 409, never renumbered.
		if code, out := do(t, app, http.MethodPost, "/v1/project/fork", "acme", map[string]any{"slug": "synapse", "target": "synapse"}); code != http.StatusConflict {
			t.Fatalf("a named slug another org holds answered %d (%s), want 409", code, out)
		}
	})
}

// A SITE OPENS ONLY ON THE ORG'S OWN WORKSPACE: the project must be the
// caller's, its repository in the namespace the org's record names, and a
// repository the caller names must be that one. The estate builds in its CI.
func TestASiteOpensOnlyOnTheOrgsOwnWorkspace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w, _ := mountBuilding(t)
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		forked(t, app, "globex", map[string]any{"slug": "saas-landing"})

		got, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if got.Owner != "acme" || got.Repo != "synapse" || got.Branch != "main" || got.ID == "" {
			t.Fatalf("opened %+v", got)
		}
		if got.Upload.Prefix != "acme/synapse" || !strings.HasPrefix(got.Upload.URL, "https://s3.example.test/") || client.Named(got.Upload.Fields)["policy"] == "" {
			t.Fatalf("the grant is %+v", got.Upload)
		}
		p, _ := mounted.State.store.GetProject(t.Context(), "acme", "synapse")
		if p.Status != "building" {
			t.Fatalf("a project that never served reads %q while it builds", p.Status)
		}

		refused := func(org string, in *client.SiteOpenIn, status int) {
			t.Helper()
			_, err := planeSiteOpen(as(org), in)
			var he *zip.HTTPError
			if !errors.As(err, &he) || he.Status != status {
				t.Fatalf("%s opening %+v = %v, want %d", org, in, err, status)
			}
		}
		// Another org's project is not there to open.
		refused("globex", &client.SiteOpenIn{Slug: "synapse"}, http.StatusNotFound)
		// A repository that is not the project's is refused, even one of the org's.
		refused("acme", &client.SiteOpenIn{Slug: "synapse", Owner: "acme", Repo: "other"}, http.StatusForbidden)
		refused("acme", &client.SiteOpenIn{Slug: "synapse", Owner: "globex", Repo: "saas-landing"}, http.StatusForbidden)

		// A project whose repository was pointed at another org's namespace builds
		// nothing: the record, not the row, says which namespace is the org's.
		host := strings.TrimPrefix(w.URL, "http://")
		stolen := mkProject("acme", "stolen", "Stolen")
		stolen.RepoURL = "https://" + host + "/globex/saas-landing.git"
		if err := mounted.State.store.CreateProject(t.Context(), stolen); err != nil {
			t.Fatal(err)
		}
		refused("acme", &client.SiteOpenIn{Slug: "stolen"}, http.StatusConflict)
		// And one whose repository is on another host.
		elsewhere := mkProject("acme", "elsewhere", "Elsewhere")
		elsewhere.RepoURL = "https://github.com/acme/synapse.git"
		if err := mounted.State.store.CreateProject(t.Context(), elsewhere); err != nil {
			t.Fatal(err)
		}
		refused("acme", &client.SiteOpenIn{Slug: "elsewhere"}, http.StatusConflict)

		// A project an import linked to its GitHub repository (hanzo-inc/cloud#766)
		// builds from the copy the import landed in the org's own workspace, under
		// the name the import gives it, and only once that copy is there.
		linked := mkProject("acme", "linked", "Linked")
		linked.RepoURL = "https://github.com/Darkhorse7stars/private-site.git"
		if err := mounted.State.store.CreateProject(t.Context(), linked); err != nil {
			t.Fatal(err)
		}
		refused("acme", &client.SiteOpenIn{Slug: "linked"}, http.StatusConflict)
		w.in(func() { w.repos["acme/darkhorse7stars_private-site"] = &shed{private: true} })
		got, err = planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "linked", Owner: "acme", Repo: "darkhorse7stars_private-site"})
		if err != nil {
			t.Fatalf("an imported project's open: %v", err)
		}
		if got.Owner != "acme" || got.Repo != "darkhorse7stars_private-site" {
			t.Fatalf("an imported project opened %s/%s, want acme/darkhorse7stars_private-site", got.Owner, got.Repo)
		}
		// Its import in ANOTHER org's namespace is never what it builds from.
		w.in(func() { w.repos["globex/darkhorse7stars_other"] = &shed{} })
		other := mkProject("acme", "other-linked", "Other")
		other.RepoURL = "https://github.com/Darkhorse7stars/other.git"
		if err := mounted.State.store.CreateProject(t.Context(), other); err != nil {
			t.Fatal(err)
		}
		refused("acme", &client.SiteOpenIn{Slug: "other-linked"}, http.StatusConflict)
		refused("acme", &client.SiteOpenIn{Slug: "linked", Owner: "globex", Repo: "darkhorse7stars_private-site"}, http.StatusForbidden)

		// The estate's projects build in their own CI.
		forked(t, app, "hanzo", map[string]any{"slug": "synapse", "target": "synapse-estate"})
		refused("hanzo", &client.SiteOpenIn{Slug: "synapse-estate"}, http.StatusForbidden)
		// An org with no workspace has nothing to build from.
		newSiteIn(t, "initech", "plain")
		refused("initech", &client.SiteOpenIn{Slug: "plain"}, http.StatusConflict)
		// And nobody is refused.
		refused("", &client.SiteOpenIn{Slug: "synapse"}, http.StatusForbidden)
	})
}

// A LIVE SITE STAYS LIVE WHILE IT REBUILDS: the edge serves only a live
// project, so a build must not take it down.
func TestALiveSiteStaysLiveWhileItBuilds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mountBuilding(t)
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		first, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := planeSiteClose(as("acme"), &client.SiteCloseIn{Slug: "synapse", ID: first.ID, Live: true,
			Commit: "abc", Files: 1, Bytes: 5, Keys: []string{"index.html"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"}); err != nil {
			t.Fatal(err)
		}
		p, _ := mounted.State.store.GetProject(t.Context(), "acme", "synapse")
		if p.Status != "live" || p.CurrentDeploy != first.ID {
			t.Fatalf("a rebuild moved the live project to %q (%s)", p.Status, p.CurrentDeploy)
		}
	})
}

// A BUILD ENDS ONCE, AND ONLY A SANDBOX BUILD ENDS HERE: live claims the host
// and moves the project; a second ending, or an ending of a deployment CI or an
// upload made, is refused.
func TestABuildEndsOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mountBuilding(t)
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		opened, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatal(err)
		}
		// Another org cannot end it.
		if _, err := planeSiteClose(as("globex"), &client.SiteCloseIn{Slug: "synapse", ID: opened.ID, Live: true}); err == nil {
			t.Fatal("globex ended acme's build")
		}
		got, err := planeSiteClose(as("acme"), &client.SiteCloseIn{Slug: "synapse", ID: opened.ID, Live: true,
			Commit: "0123456789abcdef", Files: 2, Bytes: 10, Keys: []string{"index.html", "a.css"}})
		if err != nil {
			t.Fatalf("close: %v", err)
		}
		if got.Status != "live" || got.LiveURL != "https://synapse.hanzo.app" {
			t.Fatalf("closed %+v", got)
		}
		p, _ := mounted.State.store.GetProject(t.Context(), "acme", "synapse")
		if p.Status != "live" || p.LiveURL != "https://synapse.hanzo.app" || p.CurrentDeploy != opened.ID {
			t.Fatalf("the project reads %+v", p)
		}
		if _, err := planeSiteClose(as("acme"), &client.SiteCloseIn{Slug: "synapse", ID: opened.ID, Live: false}); err == nil {
			t.Fatal("a build ended twice")
		}
		// A CI deployment is CI's to complete.
		now := time.Now().Unix()
		ci := Deployment{ID: "dep_ci", ProjectID: p.ID, Org: "acme", Version: 9, Status: "queued", Source: "git",
			Bucket: "hanzo-sites", Prefix: "acme/synapse", CreatedAt: now, UpdatedAt: now}
		if err := mounted.State.store.InsertDeployment(t.Context(), ci); err != nil {
			t.Fatal(err)
		}
		if _, err := planeSiteClose(as("acme"), &client.SiteCloseIn{Slug: "synapse", ID: "dep_ci", Live: true}); err == nil {
			t.Fatal("a CI deployment was ended as a sandbox build")
		}
	})
}

// A FAILED FIRST BUILD MARKS THE PROJECT, and says why.
func TestAFailedFirstBuildSaysWhy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mountBuilding(t)
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		opened, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := planeSiteClose(as("acme"), &client.SiteCloseIn{Slug: "synapse", ID: opened.ID, Message: "the build exited 1: missing script"})
		if err != nil || got.Status != "error" || got.Message != "the build exited 1: missing script" {
			t.Fatalf("closed %+v, %v", got, err)
		}
		p, _ := mounted.State.store.GetProject(t.Context(), "acme", "synapse")
		if p.Status != "error" {
			t.Fatalf("the project reads %q", p.Status)
		}
	})
}

// A BUILD THAT NEVER REPORTS BACK ENDS. Past the life of its upload grant it
// cannot land, so its deployment is ended failed with why and a project that
// never served stops reading building; the late ending is refused. A build
// inside that life, and a deployment CI or an upload made, is left alone.
func TestABuildThatNeverReportsBackEnds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mountBuilding(t)
		ctx := t.Context()
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		forked(t, app, "globex", map[string]any{"slug": "synapse", "target": "synapse-g"})
		gone, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatal(err)
		}
		working, err := planeSiteOpen(as("globex"), &client.SiteOpenIn{Slug: "synapse-g"})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		old := now.Add(-buildLife - time.Minute).Unix()
		if _, err := mounted.State.store.db.ExecContext(ctx, `UPDATE deployments SET created_at=$1 WHERE id=$2`, old, gone.ID); err != nil {
			t.Fatal(err)
		}
		acme, _ := mounted.State.store.GetProject(ctx, "acme", "synapse")
		ci := Deployment{ID: "dep_ci", ProjectID: acme.ID, Org: "acme", Version: 9, Status: "queued", Source: "git",
			Bucket: "hanzo-sites", Prefix: "acme/synapse", CreatedAt: old, UpdatedAt: old}
		if err := mounted.State.store.InsertDeployment(ctx, ci); err != nil {
			t.Fatal(err)
		}

		n, err := expireBuilds(mounted, ctx, now)
		if err != nil || n != 1 {
			t.Fatalf("expired %d, %v; want acme's one build", n, err)
		}
		d, _ := mounted.State.store.GetDeployment(ctx, "acme", acme.ID, gone.ID)
		if d.Status != "error" || !strings.Contains(d.Message, "did not finish") {
			t.Fatalf("the abandoned build reads %q: %q", d.Status, d.Message)
		}
		if p, _ := mounted.State.store.GetProject(ctx, "acme", "synapse"); p.Status != "error" {
			t.Fatalf("a project whose first build was abandoned reads %q", p.Status)
		}
		if _, err := planeSiteClose(as("acme"), &client.SiteCloseIn{Slug: "synapse", ID: gone.ID, Live: true}); err == nil {
			t.Fatal("an expired build was ended live")
		}
		if d, _ := mounted.State.store.GetDeployment(ctx, "acme", acme.ID, "dep_ci"); d.Status != "queued" {
			t.Fatalf("a CI deployment was expired: %q", d.Status)
		}
		globex, _ := mounted.State.store.GetProject(ctx, "globex", "synapse-g")
		if d, _ := mounted.State.store.GetDeployment(ctx, "globex", globex.ID, working.ID); d.Status != "building" || globex.Status != "building" {
			t.Fatalf("a build still inside its life was ended: %q, project %q", d.Status, globex.Status)
		}
	})
}

// AN ABANDONED BUILD NEVER TAKES DOWN A SITE THAT SERVES, and never marks broken
// a project whose newer build is still running.
func TestAnAbandonedBuildLeavesWhatServesAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mountBuilding(t)
		ctx := t.Context()
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		forked(t, app, "globex", map[string]any{"slug": "synapse", "target": "synapse-g"})
		// acme serves a release: live, with no deployment it points at.
		p, _ := mounted.State.store.GetProject(ctx, "acme", "synapse")
		p.Status, p.CurrentRelease, p.LiveURL = "live", "rel_1", "https://synapse.hanzo.app"
		if err := mounted.State.store.UpdateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
		served, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatal(err)
		}
		// globex has never served, and a second build began after the first.
		first, err := planeSiteOpen(as("globex"), &client.SiteOpenIn{Slug: "synapse-g"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := planeSiteOpen(as("globex"), &client.SiteOpenIn{Slug: "synapse-g"}); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-buildLife - time.Minute).Unix()
		for _, id := range []string{served.ID, first.ID} {
			if _, err := mounted.State.store.db.ExecContext(ctx, `UPDATE deployments SET created_at=$1 WHERE id=$2`, old, id); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := expireBuilds(mounted, ctx, time.Now()); err != nil || n != 2 {
			t.Fatalf("expired %d, %v; want both abandoned builds", n, err)
		}
		if p, _ := mounted.State.store.GetProject(ctx, "acme", "synapse"); p.Status != "live" {
			t.Fatalf("an abandoned build took a release-served site to %q", p.Status)
		}
		if p, _ := mounted.State.store.GetProject(ctx, "globex", "synapse-g"); p.Status != "building" {
			t.Fatalf("a project whose newer build runs reads %q", p.Status)
		}
	})
}

// A DEPLOYMENT ENDS ONCE EVEN WHEN TWO ENDINGS RACE: the one written from a copy
// read before the other landed is refused, and leaves the first one's work as it is.
func TestADeploymentEndingRacedIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mountBuilding(t)
		ctx := t.Context()
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		opened, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := mounted.State.store.GetProject(ctx, "acme", "synapse")
		stale, _ := mounted.State.store.GetDeployment(ctx, "acme", p.ID, opened.ID)
		if _, err := planeSiteClose(as("acme"), &client.SiteCloseIn{Slug: "synapse", ID: opened.ID, Live: true,
			Commit: "abc", Files: 1, Bytes: 5, Keys: []string{"index.html"}}); err != nil {
			t.Fatal(err)
		}
		var he *zip.HTTPError
		if _, err := land(mounted, ctx, "acme", p, stale, ending{message: "the build did not finish"}); !errors.As(err, &he) || he.Status != http.StatusConflict {
			t.Fatalf("a raced ending answered %v", err)
		}
		d, _ := mounted.State.store.GetDeployment(ctx, "acme", p.ID, opened.ID)
		now, _ := mounted.State.store.GetProject(ctx, "acme", "synapse")
		if d.Status != "live" || now.Status != "live" || now.CurrentDeploy != opened.ID {
			t.Fatalf("the raced ending overwrote the first: deployment %q, project %q (%s)", d.Status, now.Status, now.CurrentDeploy)
		}
	})
}

// THE BOARD IS THE ORG'S OWN.
func TestTheBuildsBoardIsTheOrgsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mountBuilding(t)
		forked(t, app, "acme", map[string]any{"slug": "synapse"})
		forked(t, app, "globex", map[string]any{"slug": "synapse", "target": "synapse-g"})
		opened, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := planeSiteBuilds(as("acme"), &client.SiteBuildsIn{})
		if err != nil || len(got.Builds) != 1 || got.Builds[0].ID != opened.ID || got.Builds[0].Project != "synapse" || got.Builds[0].Status != "building" {
			t.Fatalf("acme's board: %+v, %v", got, err)
		}
		if got, err := planeSiteBuilds(as("globex"), &client.SiteBuildsIn{}); err != nil || len(got.Builds) != 0 {
			t.Fatalf("globex's board: %+v, %v", got, err)
		}
	})
}

// newSiteIn makes a project with no repository in org, straight in the store.
func newSiteIn(t *testing.T, org, slug string) {
	t.Helper()
	if err := mounted.State.store.CreateProject(t.Context(), mkProject(org, slug, slug)); err != nil {
		t.Fatal(err)
	}
}

// THE REPOSITORY IS READ OUT OF THE ADDRESS EXACTLY: one host, https, owner and
// name, nothing else.
func TestForgeRepoReadsOnlyAPlainAddress(t *testing.T) {
	for raw, want := range map[string]string{
		"https://git.test/acme/site.git":         "acme/site",
		"https://git.test/acme/site":             "acme/site",
		"https://GIT.TEST/acme/site.git":         "acme/site",
		"https://git.test/acme/site/extra.git":   "",
		"http://git.test/acme/site.git":          "",
		"https://u:p@git.test/acme/site.git":     "",
		"https://git.other/acme/site.git":        "",
		"https://git.test/acme/..":               "",
		"https://git.test/acme/site.git?x=1":     "",
		"https://git.test/../hanzoai/cloud.git":  "",
		"https://git.test/acme/.hidden.git":      "",
		"https://git.test/acme":                  "",
		"https://git.test/acme/site%2Fother.git": "",
	} {
		o, r, ok := forgeRepo(raw, "git.test")
		got := ""
		if ok {
			got = o + "/" + r
		}
		if got != want {
			t.Errorf("forgeRepo(%q) = %q, want %q", raw, got, want)
		}
	}
}
