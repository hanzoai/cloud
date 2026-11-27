package deploy

// A rollback is a sync to an earlier revision of the application's own, through
// the one sync op and the one lookup (sites.go, resolve.go, dashboard.go sync).
// These pin which applications a caller sees and may roll back, what each kind
// is asked for, and that a key limited to deploy:rollback does that and nothing
// else.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

const (
	relGood = "rel_00000000000000000000000000000977"
	relBad  = "rel_00000000000000000000000000000978"
)

// fakeSites stands in for the projects app: org hanzo holds the site hanzo-ai,
// kept from two deployments, serving the newer one. Like the projects app it
// answers for the org the call states, and for no other.
type fakeSites struct {
	mu      sync.Mutex
	serving string
	asked   []client.SiteRollbackIn
	by      []string
}

func (f *fakeSites) app(history bool) client.SiteApp {
	app := client.SiteApp{Slug: "hanzo-ai", Status: "live", LiveURL: "https://hanzo.ai", Revision: f.serving, UpdatedAt: 1763600000}
	if history {
		app.History = []client.SiteRevision{
			{Release: relBad, Deploy: "dep_b", Version: 978, Commit: "683186b5a", At: 1763600000, Objects: 10179},
			{Release: relGood, Deploy: "dep_a", Version: 977, Commit: "5b8a8c95", At: 1763500000, Objects: 10179},
		}
	}
	return app
}

func standInSites(t *testing.T) *fakeSites {
	t.Helper()
	f := &fakeSites{serving: relBad}
	wasSites, wasRollback := askSites, askSiteRollback
	t.Cleanup(func() { askSites, askSiteRollback = wasSites, wasRollback })
	askSites = func(ctx context.Context, in *client.SiteAppsIn) (*client.SiteApps, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if zip.CallerOf(ctx).Org != "hanzo" {
			if in.Slug != "" {
				return nil, zip.ErrNotFound("site not found")
			}
			return &client.SiteApps{Sites: []client.SiteApp{}}, nil
		}
		if in.Slug == "" {
			return &client.SiteApps{Sites: []client.SiteApp{f.app(false)}}, nil
		}
		if in.Slug != "hanzo-ai" {
			return nil, zip.ErrNotFound("site not found")
		}
		return &client.SiteApps{Sites: []client.SiteApp{f.app(true)}}, nil
	}
	askSiteRollback = func(ctx context.Context, in *client.SiteRollbackIn) (*client.SiteApp, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.asked = append(f.asked, *in)
		f.by = append(f.by, zip.CallerOf(ctx).Org)
		if zip.CallerOf(ctx).Org != "hanzo" || in.Slug != "hanzo-ai" {
			return nil, zip.ErrNotFound("site not found")
		}
		if in.Release == f.serving {
			return nil, zip.ErrConflict("hanzo-ai already serves " + in.Release)
		}
		f.serving = in.Release
		app := f.app(true)
		return &app, nil
	}
	return f
}

// keyOf is key's principal, by address, for call.
func keyOf(limit ...string) *principal.Principal {
	p := key(limit...)
	return &p
}

// key is the principal a key's request carries: the bot's, in org hanzo, with
// the limit its holder gave it.
func key(limit ...string) principal.Principal {
	return principal.Principal{Org: "hanzo", User: "hanzo/grok-bot-iam", Subject: "hanzo/grok-bot-iam",
		Kind: principal.Key, Key: "k-rollback", Limit: limit}
}

// call drives one request through the console's routes, behind a stand-in for
// the identity boundary that attests who (a person when who is nil).
func call(t *testing.T, s *cloud.Service[state], method, path, body string, headers map[string]string, who *principal.Principal) (int, string) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if who != nil {
		p := *who
		app.Use(zip.H(func(c *zip.Ctx) error {
			principal.Mint(c, p)
			return c.Continue()
		}))
	}
	routes(app, s)
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		b.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func rollbackTo(revision string) string { return `{"revision":"` + revision + `"}` }

const siteSyncPath = "/v1/deploy/applications/site-hanzo-ai/sync"

// A site is an application of its own org: listed, read and described for its
// members (and a SuperAdmin acting in that org), and not found for anyone else.
func TestAnOrgsSitesAreItsApplications(t *testing.T) {
	standInSites(t)
	s := fakeService(hanzoGit())

	code, body := call(t, s, http.MethodGet, "/v1/deploy/applications", "", hanzoMember, nil)
	if code != http.StatusOK || !strings.Contains(body, `"name":"site-hanzo-ai"`) {
		t.Fatalf("list as a hanzo member = %d, want site-hanzo-ai in %s", code, body)
	}
	if code, body := call(t, s, http.MethodGet, "/v1/deploy/applications", "", acmeMember, nil); code != http.StatusOK || strings.Contains(body, "site-hanzo-ai") {
		t.Fatalf("list as another org = %d %s, want no hanzo site", code, body)
	}

	code, body = call(t, s, http.MethodGet, "/v1/deploy/applications/site-hanzo-ai", "", hanzoMember, nil)
	if code != http.StatusOK {
		t.Fatalf("read = %d %s", code, body)
	}
	var app argoApp
	if err := json.Unmarshal([]byte(body), &app); err != nil {
		t.Fatal(err)
	}
	if app.Metadata.Namespace != "hanzo" || app.Status.Sync.Revision != relBad || app.Status.Health.Status != "Healthy" {
		t.Fatalf("site = %+v, want hanzo's, healthy, serving %s", app, relBad)
	}
	if h := app.Status.History; len(h) != 2 || h[0].Revision != relGood || h[1].Revision != relBad || h[1].ID != 978 {
		t.Fatalf("history = %+v, want 977 then 978, oldest first", h)
	}

	for _, suffix := range readsOfAnApp {
		path := "/v1/deploy/applications/site-hanzo-ai" + suffix
		for who, h := range map[string]map[string]string{"hanzo member": hanzoMember, "SuperAdmin in hanzo": superInHanzo} {
			if code, body := call(t, s, http.MethodGet, path, "", h, nil); code != http.StatusOK {
				t.Errorf("GET %s as %s = %d %s, want 200", path, who, code, body)
			}
		}
		for who, h := range map[string]map[string]string{"another org": acmeMember, "SuperAdmin in no org": superHeaders} {
			if code, _ := call(t, s, http.MethodGet, path, "", h, nil); code != http.StatusNotFound {
				t.Errorf("GET %s as %s = %d, want 404", path, who, code)
			}
		}
	}
	if code, body := call(t, s, http.MethodGet, "/v1/deploy/applications/site-hanzo-ai/revisions/"+relGood+"/metadata", "", hanzoMember, nil); code != http.StatusOK || !strings.Contains(body, "v977") {
		t.Fatalf("metadata of the earlier release = %d %s, want deployment v977", code, body)
	}
}

// Rolling a site back is syncing it to an earlier revision from its history, by
// a member of its org, and the projects app is asked for exactly that, as them.
func TestASiteRollsBackBySyncingToAnEarlierRevision(t *testing.T) {
	f := standInSites(t)
	s := fakeService()

	code, body := call(t, s, http.MethodPost, siteSyncPath, rollbackTo(relGood), hanzoMember, nil)
	if code != http.StatusOK {
		t.Fatalf("rollback = %d %s", code, body)
	}
	var app argoApp
	if err := json.Unmarshal([]byte(body), &app); err != nil {
		t.Fatal(err)
	}
	if app.Status.Sync.Revision != relGood {
		t.Fatalf("after rollback the site serves %q, want %s", app.Status.Sync.Revision, relGood)
	}
	if len(f.asked) != 1 || f.asked[0] != (client.SiteRollbackIn{Slug: "hanzo-ai", Release: relGood}) || f.by[0] != "hanzo" {
		t.Fatalf("projects was asked %+v by %v, want one rollback of hanzo-ai to %s for hanzo", f.asked, f.by, relGood)
	}
	// A SuperAdmin acting in hanzo may too; the projects app answers 409 for the
	// release it already serves, and the console says so.
	if code, body := call(t, s, http.MethodPost, siteSyncPath, rollbackTo(relGood), superInHanzo, nil); code != http.StatusConflict {
		t.Fatalf("rollback to what it serves = %d %s, want 409", code, body)
	}
}

// Nothing else is a rollback, and nobody else may make one.
func TestARollbackOutsideItsGrantIsRefused(t *testing.T) {
	f := standInSites(t)
	gitApp := hanzoGit()
	unstructured.RemoveNestedField(gitApp.Object, "spec", "syncPolicy", "automated")
	_ = unstructured.SetNestedSlice(gitApp.Object, []any{cdHistory(1, "1111aaaa", "2026-11-18T00:00:00Z"), cdHistory(2, "672f1e2843", "2026-11-19T00:00:00Z")}, "status", "history")
	s := fakeService(gitApp)

	for _, tc := range []struct {
		why     string
		path    string
		body    string
		headers map[string]string
		who     *principal.Principal
		want    int
	}{
		{"a site syncs only to a revision", siteSyncPath, "", hanzoMember, nil, http.StatusBadRequest},
		{"another org's site is not found", siteSyncPath, rollbackTo(relGood), acmeMember, nil, http.StatusNotFound},
		{"a site the org does not hold is not found", "/v1/deploy/applications/site-nope/sync", rollbackTo(relGood), hanzoMember, nil, http.StatusNotFound},
		{"an unvalidated caller is refused", siteSyncPath, rollbackTo(relGood), map[string]string{"X-Org-Id": "hanzo"}, nil, http.StatusForbidden},
		{"a member may not roll back a CD Application", "/v1/deploy/applications/hanzo-git/sync", rollbackTo("1111aaaa"), hanzoMember, nil, http.StatusForbidden},
		{"a member may not sync a CD Application", "/v1/deploy/applications/hanzo-git/sync", "", hanzoMember, nil, http.StatusForbidden},
		{"a key limited to sync may not roll back", siteSyncPath, rollbackTo(relGood), hanzoMember, keyOf("product:deploy", "deploy:sync"), http.StatusForbidden},
		{"a revision longer than any is a 400", siteSyncPath, rollbackTo(strings.Repeat("a", maxRevisionLen+1)), hanzoMember, nil, http.StatusBadRequest},
	} {
		if code, body := call(t, s, http.MethodPost, tc.path, tc.body, tc.headers, tc.who); code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.why, code, body, tc.want)
		}
	}
	if len(f.asked) != 0 {
		t.Fatalf("a refused rollback reached the projects app: %+v", f.asked)
	}
	got, err := s.State.dyn.Resource(k8s.CDApplications).Namespace("hanzo-cd").Get(t.Context(), "hanzo-git", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if op, ok, _ := unstructured.NestedMap(got.Object, "operation"); ok {
		t.Fatalf("a refused sync wrote an operation: %v", op)
	}
}

// The least a key can hold and still undo a bad deploy: it reads the console and
// rolls back its org's sites; it neither syncs nor reaches any other capability,
// so it can publish nothing and edit no configuration.
func TestAKeyLimitedToRollbackReadsAndRollsBackOnly(t *testing.T) {
	f := standInSites(t)
	s := fakeService(hanzoGit())
	bot := key("product:deploy", "deploy:rollback")
	asKey := map[string]string{"X-User-Id": "hanzo/grok-bot-iam", "X-Org-Id": "hanzo"}

	if code, body := call(t, s, http.MethodGet, "/v1/deploy/applications", "", asKey, &bot); code != http.StatusOK || !strings.Contains(body, "site-hanzo-ai") {
		t.Fatalf("list = %d %s, want the org's sites", code, body)
	}
	if code, body := call(t, s, http.MethodPost, siteSyncPath, rollbackTo(relGood), asKey, &bot); code != http.StatusOK {
		t.Fatalf("rollback = %d %s, want 200", code, body)
	}
	if len(f.asked) != 1 {
		t.Fatalf("projects asked %d times, want once", len(f.asked))
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, siteSyncPath, ""},                             // a sync is not a rollback
		{http.MethodPost, "/v1/deploy/applications/hanzo-git/sync", ""}, // nor a CD sync
		{http.MethodGet, "/v1/deploy/settings", ""},                     // nor the console's configuration
	} {
		if code, body := call(t, s, tc.method, tc.path, tc.body, asKey, &bot); code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403", tc.method, tc.path, code, body)
		}
	}
	// And the grant reaches no other capability: the identity boundary refuses a
	// new deploy (POST /v1/project/…, app "project") before any handler runs.
	g := cloud.Grant(bot.Limit)
	if !g.Permits("deploy", true) || g.Permits("project", true) || g.Permits("project", false) || g.Permits("iam", false) {
		t.Fatalf("grant %v reaches beyond the deploy console", bot.Limit)
	}
}

// A CD Application rolls back to a commit CD deployed it at before, for a
// SuperAdmin, unless CD would undo it.
func TestACDApplicationRollsBackToAnEarlierCommit(t *testing.T) {
	manual := func() *unstructured.Unstructured {
		app := hanzoGit()
		unstructured.RemoveNestedField(app.Object, "spec", "syncPolicy", "automated")
		_ = unstructured.SetNestedSlice(app.Object, []any{
			cdHistory(1, "1111aaaa", "2026-11-17T00:00:00Z"),
			cdHistory(2, "2222bbbb", "2026-11-18T00:00:00Z"),
			cdHistory(3, "672f1e2843", "2026-11-19T00:00:00Z"),
		}, "status", "history")
		return app
	}
	gitSync := "/v1/deploy/applications/hanzo-git/sync"

	s := fakeService(manual())
	code, body := call(t, s, http.MethodGet, "/v1/deploy/applications/hanzo-git", "", superHeaders, nil)
	var read argoApp
	if code != http.StatusOK || json.Unmarshal([]byte(body), &read) != nil {
		t.Fatalf("read = %d %s", code, body)
	}
	if h := read.Status.History; len(h) != 3 || h[0] != (argoHistory{ID: 1, Revision: "1111aaaa", DeployedAt: "2026-11-17T00:00:00Z"}) || h[2].Revision != "672f1e2843" {
		t.Fatalf("history = %+v, want its three deploys oldest first", h)
	}
	if code, body := call(t, s, http.MethodPost, gitSync, rollbackTo("1111aaaa"), superHeaders, nil); code != http.StatusOK {
		t.Fatalf("rollback = %d %s", code, body)
	}
	got, err := s.State.dyn.Resource(k8s.CDApplications).Namespace("hanzo-cd").Get(t.Context(), "hanzo-git", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	op, _, _ := unstructured.NestedMap(got.Object, "operation")
	if sync, _ := op["sync"].(map[string]any); sync["revision"] != "1111aaaa" || sync["prune"] != false {
		t.Fatalf("operation = %v, want a sync of 1111aaaa without prune", op)
	}

	for _, tc := range []struct {
		why string
		app *unstructured.Unstructured
		to  string
	}{
		{"a commit CD never deployed", manual(), "deadbeef"},
		{"the commit it runs", manual(), "672f1e2843"},
		{"an application that syncs itself", hanzoGit(), "672f1e2843"},
	} {
		if code, body := call(t, fakeService(tc.app), http.MethodPost, gitSync, rollbackTo(tc.to), superHeaders, nil); code != http.StatusConflict {
			t.Errorf("%s: %d %s, want 409", tc.why, code, body)
		}
	}
	// An App CR keeps no history, so it has nothing to roll back to.
	cr := fakeService(appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1", "Running", 1, 1))
	if code, body := call(t, cr, http.MethodPost, "/v1/deploy/applications/iam/sync", rollbackTo("v0"), superHeaders, nil); code != http.StatusConflict {
		t.Fatalf("App CR rollback = %d %s, want 409", code, body)
	}
}
