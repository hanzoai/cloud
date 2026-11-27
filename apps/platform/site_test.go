package platform

// site_test.go — a deploy of a repository in the org's own code workspace is
// published as its project's site, built in a sandbox by the agent app; it never
// reaches the BuildKit lane, and a forge address outside the org's workspace is
// refused before anything is built.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// desk stands in for the project and agent apps: the workspace record, the
// org's site builds, and the publishes asked for.
type desk struct {
	mu        sync.Mutex
	published []client.PublishIn
	by        []string
	builds    map[string][]client.SiteBuild
	names     map[string]string // name -> org/holder, the one public namespace
}

func (d *desk) asked() ([]client.PublishIn, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]client.PublishIn(nil), d.published...), append([]string(nil), d.by...)
}

// servesDesk stands the two apps up on sockets, with acme's workspace made and
// the forge at git.test.
func servesDesk(t *testing.T) *desk {
	t.Helper()
	was := forgeHost
	forgeHost = "git.test"
	t.Cleanup(func() { forgeHost = was })
	dir, err := os.MkdirTemp("", "zs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)

	d := &desk{builds: map[string][]client.SiteBuild{}, names: map[string]string{}}
	project := zip.New(zip.Config{AppName: projectpeer.App, DisableStartupMessage: true})
	project.Post("/projects/workspace", func(ctx context.Context, _ *client.WorkspaceIn) (*client.Workspace, error) {
		if zip.CallerOf(ctx).Org == "acme" {
			return &client.Workspace{Owner: "acme", Made: true}, nil
		}
		return &client.Workspace{}, nil
	}, zip.WithOperationID(client.ProjectsWorkspace))
	project.Post("/projects/site/builds", func(ctx context.Context, _ *client.SiteBuildsIn) (*client.SiteBuilds, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		return &client.SiteBuilds{Builds: d.builds[zip.CallerOf(ctx).Org]}, nil
	}, zip.WithOperationID(client.ProjectsSiteBuilds))
	project.Post("/sites/claim", func(ctx context.Context, in *client.NameIn) (*client.NameOut, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		who := zip.CallerOf(ctx).Org + "/" + in.Holder
		if held, ok := d.names[in.Name]; ok && held != who {
			return &client.NameOut{Taken: true}, nil
		}
		d.names[in.Name] = who
		return &client.NameOut{}, nil
	}, zip.WithOperationID(client.SitesClaim))
	project.Post("/sites/release", func(ctx context.Context, in *client.NameIn) (*client.NameOut, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.names[in.Name] == zip.CallerOf(ctx).Org+"/"+in.Holder {
			delete(d.names, in.Name)
		}
		return &client.NameOut{}, nil
	}, zip.WithOperationID(client.SitesRelease))
	agent := zip.New(zip.Config{AppName: agentpeer.App, DisableStartupMessage: true})
	agent.Post("/agents/coding/publish", func(ctx context.Context, in *client.PublishIn) (*client.Publishing, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.published = append(d.published, *in)
		d.by = append(d.by, zip.CallerOf(ctx).Org+"/"+zip.CallerOf(ctx).User)
		return &client.Publishing{Deployment: "dep_1", Project: in.Project, Ref: in.Ref, Status: "building"}, nil
	}, zip.WithOperationID(client.AgentsCodingPublish))
	plane.Bind()
	for name, app := range map[string]*zip.App{projectpeer.App: project, agentpeer.App: agent} {
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		deadline := time.Now().Add(5 * time.Second)
		for {
			if up, err := plane.Listening(zip.SocketPath(name)); err == nil && up {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never came up", name)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return d
}

// doMember fires a request as a member of org who does not administer it.
func doMember(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	req := newJSONRequest(t, method, path, body)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u-"+org)
	resp, err := app.Test(req, budget)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// A MEMBER PUBLISHES THEIR ORG'S PROJECT FROM ITS OWN WORKSPACE: the deploy asks
// the agent app to build it, as the org and as the member, at the ref, and
// answers the build to follow — nothing is declared in universe and no BuildKit
// job is launched. The member is who the build's sandbox is leased by and paid
// from: a call that named only the org named no wallet, and its lease was refused.
func TestAWorkspaceRepositoryIsPublishedAsItsSite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		d := servesDesk(t)
		app := mountDelivery(t)
		code, body := doMember(t, app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{
			"repo": "https://git.test/acme/site.git", "ref": "agent/abc123def456", "name": "site", "partOf": "site", "mode": "branch",
		})
		if code != http.StatusAccepted {
			t.Fatalf("publish answered %d %s", code, body)
		}
		var got declareResp
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Build == nil || got.Build.ID != "dep_1" || got.Build.Status != "building" || got.Build.Ref != "agent/abc123def456" ||
			got.Build.Image != "" || !got.Declaration.Live || got.Declaration.Review != "" {
			t.Fatalf("answered %+v / %+v", got.Build, got.Declaration)
		}
		asked, by := d.asked()
		if len(asked) != 1 || asked[0] != (client.PublishIn{Project: "site", Owner: "acme", Repo: "site", Ref: "agent/abc123def456"}) || by[0] != "acme/u-acme" {
			t.Fatalf("the agent app was asked %+v as %v", asked, by)
		}
	})
}

// A FORGE ADDRESS OUTSIDE THE ORG'S OWN WORKSPACE IS NOT THE ORG'S, for a member
// and an org admin alike: refused before anything is built, and never handed to
// the lane that clones with the forge's own credential.
func TestAForgeAddressOutsideTheWorkspaceIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		d := servesDesk(t)
		app := mountDelivery(t)
		for _, repo := range []string{
			"https://git.test/globex/site.git",
			"https://git.test/hanzoai/cloud.git",
			"https://git.test/acme/site/extra.git",
			"https://u:p@git.test/acme/site.git",
		} {
			body := map[string]any{"repo": repo, "name": "site", "mode": "commit"}
			if code, out := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", "acme", body); code != http.StatusNotFound {
				t.Errorf("an admin's %s answered %d %s", repo, code, out)
			}
			if code, out := doMember(t, app, http.MethodPost, "/v1/platform/apps", "acme", body); code != http.StatusNotFound {
				t.Errorf("a member's %s answered %d %s", repo, code, out)
			}
		}
		// Naming its own org changes nothing: the forge address is still refused,
		// and never reaches the lane below.
		if code, out := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{
			"repo": "https://git.test/globex/site.git", "name": "site", "org": "acme",
		}); code != http.StatusNotFound {
			t.Errorf("an admin naming acme answered %d %s", code, out)
		}
		// An org with no workspace has nothing on the forge.
		if code, out := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", "initech", map[string]any{
			"repo": "https://git.test/initech/site.git", "name": "site",
		}); code != http.StatusNotFound {
			t.Errorf("an org with no workspace answered %d %s", code, out)
		}
		if asked, _ := d.asked(); len(asked) != 0 {
			t.Fatalf("the agent app was asked %+v", asked)
		}
	})
}

// A SITE TAKES NO CONTAINER SETTINGS: what does not apply is said, not ignored.
func TestASiteTakesNoContainerSettings(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		d := servesDesk(t)
		app := mountDelivery(t)
		for _, extra := range []map[string]any{{"dockerfile": "Dockerfile"}, {"port": 8080}, {"env": []map[string]any{{"name": "A", "value": "b", "public": true}}}} {
			body := map[string]any{"repo": "https://git.test/acme/site.git", "name": "site"}
			for k, v := range extra {
				body[k] = v
			}
			if code, out := doMember(t, app, http.MethodPost, "/v1/platform/apps", "acme", body); code != http.StatusBadRequest {
				t.Errorf("%v answered %d %s", extra, code, out)
			}
		}
		if asked, _ := d.asked(); len(asked) != 0 {
			t.Fatalf("the agent app was asked %+v", asked)
		}
	})
}

// THE ESTATE DEPLOYS AS IT DID: its forge address goes to the lane it always
// went to, and the agent app is not asked. An estate directory is the
// platform's own, so the deploy is a SuperAdmin's.
func TestTheEstateDeploysAsBefore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		d := servesDesk(t)
		was := selfGitHost
		selfGitHost = "test"
		t.Cleanup(func() { selfGitHost = was })
		app := mountDelivery(t)
		req := newJSONRequest(t, http.MethodPost, "/v1/platform/apps", map[string]any{
			"repo": "https://git.test/lux/node.git", "mode": "commit",
		})
		req.Header.Set("X-Org-Id", "lux")
		req.Header.Set("X-User-Id", "u-lux")
		req.Header.Set("X-User-IsAdmin", "true")
		resp, err := app.Test(req, budget)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		code, out := resp.StatusCode, b
		if code != http.StatusBadGateway || !strings.Contains(string(out), "could not launch the build") {
			t.Fatalf("the estate's deploy answered %d %s", code, out)
		}
		if asked, _ := d.asked(); len(asked) != 0 {
			t.Fatalf("the agent app was asked %+v", asked)
		}
	})
}

// A RELEASE OF A SITE BUILD SAYS WHERE THE BUILD IS: live has nothing more to
// ship, anything else is not shippable yet, and another project's build is not
// this one's.
func TestAReleaseOfASiteBuildSaysWhereItIs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		d := servesDesk(t)
		d.builds["acme"] = []client.SiteBuild{
			{ID: "dep_live", Project: "site", Status: "live"},
			{ID: "dep_run", Project: "site", Status: "building"},
			{ID: "dep_bad", Project: "site", Status: "error", Message: "the build exited 1"},
			{ID: "dep_other", Project: "other", Status: "live"},
		}
		app := mountDelivery(t)
		ship := func(tag string) (int, string) {
			code, out := doAdmin(t, app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{
				"repo": "https://git.test/acme/site.git", "name": "site", "mode": "commit", "tag": tag,
			})
			return code, string(out)
		}
		if code, out := ship("dep_live"); code != http.StatusAccepted || !strings.Contains(out, "nothing more to ship") {
			t.Fatalf("a live build answered %d %s", code, out)
		}
		if code, _ := ship("dep_run"); code != http.StatusConflict {
			t.Fatalf("a building build answered %d", code)
		}
		if code, out := ship("dep_bad"); code != http.StatusConflict || !strings.Contains(out, "the build exited 1") {
			t.Fatalf("a failed build answered %d %s", code, out)
		}
		if code, _ := ship("dep_other"); code != http.StatusNotFound {
			t.Fatalf("another project's build answered %d", code)
		}
		if asked, _ := d.asked(); len(asked) != 0 {
			t.Fatalf("a release built: %+v", asked)
		}
	})
}

// THE BOARD SHOWS A SITE BUILD AS IT SHOWS EVERY BUILD, and the estate has none.
func TestTheBoardShowsSiteBuilds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		d := servesDesk(t)
		d.builds["acme"] = []client.SiteBuild{{ID: "dep_1", Project: "site", Repo: "https://git.test/acme/site.git",
			Commit: "0123456789abcdef0123", Status: "live", CreatedAt: 100, UpdatedAt: 160}}
		got, err := siteBuilds("acme")
		if err != nil || len(got) != 1 {
			t.Fatalf("acme's site builds: %+v, %v", got, err)
		}
		row := siteRow(got[0])
		if row.ID != "dep_1" || row.Status != "succeeded" || row.Commit != "0123456789ab" || row.Duration != "1m" {
			t.Fatalf("the row is %+v", row)
		}
		if siteRow(client.SiteBuild{Status: "building"}).Status != "building" || siteRow(client.SiteBuild{Status: "error"}).Status != "failed" {
			t.Fatal("a site build's status is not the board's word for it")
		}
		if got, err := siteBuilds("hanzo"); err != nil || got != nil {
			t.Fatalf("the estate's site builds: %+v, %v", got, err)
		}
	})
}

// A SUPERADMIN ACTING AS AN ORG DOES NOT HAND ITS WORKSPACE TO THE BUILDKIT
// LANE: a forge namespace that is not the estate's is refused.
func TestAnActAsCannotBuildAWorkspaceOnTheLane(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		d := servesDesk(t)
		was := selfGitHost
		selfGitHost = "test"
		t.Cleanup(func() { selfGitHost = was })
		app := mountDelivery(t)
		req := newJSONRequest(t, http.MethodPost, "/v1/platform/apps", map[string]any{
			"repo": "https://git.test/acme/site.git", "mode": "commit", "org": "acme",
		})
		req.Header.Set("X-Org-Id", "admin")
		req.Header.Set("X-User-Id", "u-admin")
		req.Header.Set("X-User-IsAdmin", "true")
		resp, err := app.Test(req, budget)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("an act-as on a workspace answered %d %s", resp.StatusCode, out)
		}
		if asked, _ := d.asked(); len(asked) != 0 {
			t.Fatalf("the agent app was asked %+v", asked)
		}
	})
}
