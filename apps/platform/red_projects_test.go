package platform

// red_projects_test.go holds the adversarial review of the projects plane: each
// test is a finding, stated as the property that answers it.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	syaml "sigs.k8s.io/yaml"
)

// twoOrgs is a universe where two orgs each declare one app.
func twoOrgs() map[string]string {
	return map[string]string{
		"charts/app/values/acme/web.yaml":              "partOf: web\nimage:\n  repository: ghcr.io/acme/web\n  tag: v1.0.0\n",
		"charts/app/values/beta/api.yaml":              "partOf: api\nimage:\n  repository: ghcr.io/beta/api\n  tag: v1.0.0\n",
		"infra/k8s/hanzo-cd/applicationset-fleet.yaml": fleetSetFixture,
	}
}

// Two owners naming the same project write two review branches, and each carries
// only its own owner's edit.
func TestTwoOwnersNameOneProjectOnTwoBranches(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := harnessOf(t, twoOrgs())
		var a, b projectWrite
		for _, c := range []struct {
			org, app string
			out      *projectWrite
		}{{"acme", "web", &a}, {"beta", "api", &b}} {
			code, body := doAdmin(t, h.app, http.MethodPost, "/v1/platform/projects", c.org,
				map[string]any{"name": "store", "apps": []map[string]string{{"name": c.app}}})
			if code != http.StatusCreated {
				t.Fatalf("%s create: %d %s", c.org, code, body)
			}
			decodeJSON(t, body, c.out)
		}
		if a.Ref == b.Ref {
			t.Fatalf("acme and beta share review branch %s", a.Ref)
		}
		for ref, owner := range map[string]string{a.Ref: "acme", b.Ref: "beta"} {
			if !strings.HasPrefix(ref, declareBranchPrefix+"/projects/"+owner+"/store/") {
				t.Errorf("branch %s does not name its owner %s", ref, owner)
			}
		}
		if got := mustGit(t, h.bare, "show", b.Ref+":"+declarePath("acme", "web")); strings.Contains(got, "store") {
			t.Errorf("beta's branch carries acme's edit:\n%s", got)
		}
		if got := mustGit(t, h.bare, "show", a.Ref+":"+declarePath("beta", "api")); strings.Contains(got, "store") {
			t.Errorf("acme's branch carries beta's edit:\n%s", got)
		}
	})
}

// A review branch is planned on main's tip: a member that lands on main after an
// earlier review of the same change moves with the rest.
func TestAProjectBranchPlansOnMainsTip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newProjectHarness(t)
		rename := func() projectWrite {
			code, body := doSuper(t, h.app, http.MethodPut, "/v1/platform/projects/insights", map[string]any{"name": "analytics"})
			if code != http.StatusOK {
				t.Fatalf("rename: %d %s", code, body)
			}
			var w projectWrite
			decodeJSON(t, body, &w)
			return w
		}
		first := rename()
		if len(first.Moved) != 2 {
			t.Fatalf("first review moved %d", len(first.Moved))
		}

		// A third member lands on main.
		dir := filepath.Join(t.TempDir(), "other")
		mustGit(t, "", "clone", "-q", "--branch", universeBranch, h.remote, dir)
		p := filepath.Join(dir, filepath.FromSlash(declarePath("hanzo", "insights-cron")))
		if err := os.WriteFile(p, []byte("partOf: insights\nimage:\n  repository: ghcr.io/hanzoai/insights-cron\n  tag: v1.0.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, dir, "add", "-A")
		mustGit(t, dir, "-c", "user.name=other", "-c", "user.email=other@test", "commit", "-qm", "insights-cron")
		mustGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/"+universeBranch)
		inventory.invalidate()

		again := rename()
		if again.Ref == first.Ref {
			t.Fatalf("the review after main moved reused %s", first.Ref)
		}
		if len(again.Moved) != 3 {
			t.Fatalf("the review planned on a stale snapshot: moved %+v", again.Moved)
		}
		if got := mustGit(t, h.bare, "show", again.Ref+":"+declarePath("hanzo", "insights-cron")); !strings.Contains(got, "'analytics'") {
			t.Fatalf("the new member did not move:\n%s", got)
		}
	})
}

// A retry of the same project change on the same tip is the same commit: it
// succeeds, names the same branch, and stacks nothing. The sleep puts the retry in
// another second, so a commit dated by the clock would be a different sha.
func TestRetryingTheSameProjectBranchWriteSucceeds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newProjectHarness(t)
		create := func() projectWrite {
			code, body := doSuper(t, h.app, http.MethodPost, "/v1/platform/projects",
				map[string]any{"name": "ops", "apps": []map[string]string{{"org": "hanzo", "name": "loose"}}})
			if code != http.StatusCreated {
				t.Fatalf("create: %d %s", code, body)
			}
			var w projectWrite
			decodeJSON(t, body, &w)
			return w
		}
		first := create()
		time.Sleep(1100 * time.Millisecond)
		again := create()
		if again.Ref != first.Ref || again.Commit != first.Commit {
			t.Fatalf("the retry landed elsewhere: %s@%s vs %s@%s", again.Ref, again.Commit, first.Ref, first.Commit)
		}
		if got := strings.TrimSpace(mustGit(t, h.bare, "rev-list", "--count", universeBranch+".."+first.Ref)); got != "1" {
			t.Fatalf("the retry stacked commits: %s ahead of main", got)
		}
	})
}

// A review of a change main already holds pushes nothing and names main: there
// is nothing to review, and the generator already reads it.
func TestABranchWriteMainAlreadyHoldsIsNoReview(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := harnessOf(t, twoOrgs())
		code, body := doAdmin(t, h.app, http.MethodPut, "/v1/platform/apps/web/project", "acme", map[string]any{"partOf": "web"})
		if code != http.StatusOK {
			t.Fatalf("move: %d %s", code, body)
		}
		var w projectWrite
		decodeJSON(t, body, &w)
		if w.Ref != universeBranch || w.Commit != "" || w.Review != "" || !w.Live {
			t.Fatalf("a no-op review = %+v", w)
		}
		if refs := mustGit(t, h.bare, "for-each-ref", "refs/heads/"+declareBranchPrefix); refs != "" {
			t.Fatalf("a no-op pushed a branch: %s", refs)
		}
	})
}

// A project named with a word YAML 1.1 reads as a bool, a number or null is still
// a string to Helm, which is what renders the chart.
func TestPartOfIsAStringToHelm(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		names := []string{"true", "on", "yes", "no", "off", "123", "0x10", "1e3", "null", "y"}
		for _, name := range names {
			t.Run("move/"+name, func(t *testing.T) {
				h := harnessOf(t, twoOrgs())
				code, body := doAdmin(t, h.app, http.MethodPut, "/v1/platform/apps/web/project", "acme", map[string]any{"partOf": name})
				if code != http.StatusOK {
					t.Fatalf("move to %q: %d %s", name, code, body)
				}
				var w projectWrite
				decodeJSON(t, body, &w)
				got := mustGit(t, h.bare, "show", w.Ref+":"+declarePath("acme", "web"))
				if s, ok := helmString([]byte(got), "partOf"); !ok || s != name {
					var v map[string]any
					_ = syaml.Unmarshal([]byte(got), &v)
					t.Fatalf("Helm reads partOf %q as %T %v", name, v["partOf"], v["partOf"])
				}
			})
		}
		// An app of that name is its own project when it names none, on the create
		// path, which renders the file.
		for _, name := range names {
			spec := testSpec()
			spec.Name, spec.PartOf = name, ""
			if s, ok := helmString(spec.render(), "partOf"); !ok || s != name {
				t.Errorf("a new declaration named %q renders a partOf Helm does not read as that string", name)
			}
		}
		// A line Helm already reads as the value is left as it is.
		root := t.TempDir()
		writeDecl(t, root, "acme", "web", "partOf: shop\nimage:\n  repository: r\n  tag: t\n")
		if _, changed, err := setPartOf(root, "acme", "web", "shop"); err != nil || changed {
			t.Fatalf("an unquoted string Helm reads as the value was rewritten: %v %v", changed, err)
		}
		// And one it reads as something else is rewritten, though YAML 1.2 calls it
		// the same word.
		writeDecl(t, root, "acme", "api", "partOf: yes\nimage:\n  repository: r\n  tag: t\n")
		if _, changed, err := setPartOf(root, "acme", "api", "yes"); err != nil || !changed {
			t.Fatalf("partOf: yes, which Helm reads as true, was left alone: %v %v", changed, err)
		}
	})
}

// partOf is inserted below a document marker however the marker line ends, and a
// move that would change anything else about the declaration is refused.
func TestPartOfLandsBelowADocumentMarker(t *testing.T) {
	for name, body := range map[string]string{
		"comment":   "--- # web, hand-maintained\nimage:\n  repository: ghcr.io/acme/web\n  tag: v1.0.0\nreplicas: 2\n",
		"crlf":      "---\r\nimage:\r\n  repository: ghcr.io/acme/web\r\n  tag: v1.0.0\r\nreplicas: 2\r\n",
		"directive": "%YAML 1.1\n---\nimage:\n  repository: ghcr.io/acme/web\n  tag: v1.0.0\nreplicas: 2\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeDecl(t, root, "acme", "web", body)
			if _, changed, err := setPartOf(root, "acme", "web", "shop"); err != nil || !changed {
				t.Fatalf("setPartOf: %v %v", changed, err)
			}
			got := readFile(t, root, declarePath("acme", "web"))
			var helm map[string]any
			if err := syaml.Unmarshal([]byte(got), &helm); err != nil {
				t.Fatal(err)
			}
			if helm["partOf"] != "shop" || helm["image"] == nil || helm["replicas"] == nil {
				t.Fatalf("Helm reads %v from:\n%q", helm, got)
			}
		})
	}
}

// Only the Deployment's running pods say what an app runs: a finished pod and a
// CronJob's pod carry the selector and must not out-vote it.
func TestFinishedAndJobPodsAreNotTheApp(t *testing.T) {
	dec := Declaration{Name: "web", Org: "hanzo", Repository: "ghcr.io/hanzoai/web", Tag: "v2.0.0", selector: instance("web")}
	live := podObj("hanzo", "web-abc", instance("web"), "ghcr.io/hanzoai/web:v2.0.0", true, false)
	done := func(name, phase string) unstructured.Unstructured {
		o := podObj("hanzo", name, instance("web"), "ghcr.io/hanzoai/web:v1.9.0", false, false)
		o.Object["status"].(map[string]any)["phase"] = phase
		return *o
	}
	job := podObj("hanzo", "nightly-3", instance("web"), "ghcr.io/hanzoai/web:v1.9.0", true, false)
	job.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "nightly", UID: "u"}})
	pods := []unstructured.Unstructured{*live, done("nightly-1", "Succeeded"), done("nightly-2", "Failed"), *job}
	r := runningOf(dec, pods)
	if r == nil || r.Pods != 1 || r.Image != "ghcr.io/hanzoai/web:v2.0.0" {
		t.Fatalf("running = %+v", r)
	}
	if v := appDrift(ProjectApp{Declaration: dec, Image: pullRef(dec), Running: r}, true); v.Severity == SeverityRed {
		t.Fatalf("a rolled app reads un-rolled: %+v", v)
	}
}

// A digest pin names a release by its tag too, and a newer release makes it
// stale — most of the fleet pins both.
func TestADigestPinIsStillJudgedStale(t *testing.T) {
	m := ProjectApp{Declaration: Declaration{Name: "cloud", Repository: "ghcr.io/hanzoai/cloud", Tag: "v1.0.0", Digest: "sha256:abc"}, Latest: "v9.0.0"}
	v := appDrift(m, false)
	if len(v.Flags) != 1 || v.Flags[0].Kind != DriftStale {
		t.Fatalf("v1.0.0@digest against latest v9.0.0 = %+v", v)
	}
}

// One values file that does not parse is its own row: every other org's board
// reads, a SuperAdmin reads why, the owning org reads that it does not parse, and
// no tenant is shown the parser's words or the platform's paths.
func TestOneUnreadableFileCostsOnlyItsRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		files := twoOrgs()
		files["charts/app/values/hanzo/x.yaml"] = "partOf: x\nreplicas: \"2\"\nimage:\n  repository: ghcr.io/hanzoai/x\n  tag: v1.0.0\n"
		files["charts/app/values/acme/bad.yaml"] = "partOf: shop\nimage:\n\ttag: [unbalanced\n"
		h := harnessOf(t, files)

		// acme's board reads, and names acme's own broken file without the parser's words.
		code, body := doAdmin(t, h.app, http.MethodGet, "/v1/platform/projects", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("acme board: %d %s", code, body)
		}
		var board projectBoard
		decodeJSON(t, body, &board)
		if len(board.Projects) != 1 || board.Projects[0].Name != "web" {
			t.Fatalf("acme projects = %+v", board.Projects)
		}
		if len(board.Unreadable) != 1 || board.Unreadable[0].Path != declarePath("acme", "bad") || board.Unreadable[0].Reason != unreadableReason {
			t.Fatalf("acme unreadable = %+v", board.Unreadable)
		}
		if strings.Contains(string(body), "hanzo/x") || strings.Contains(string(body), "unmarshal") {
			t.Fatalf("a tenant read the platform's file or the parser's words: %s", body)
		}

		// beta's board and app list read, and carry no row at all.
		code, body = doAdmin(t, h.app, http.MethodGet, "/v1/platform/apps", "beta", nil)
		if code != http.StatusOK || strings.Contains(string(body), "unreadable") {
			t.Fatalf("beta apps: %d %s", code, body)
		}

		// acme's app list names its broken file too; the file itself answers a 502
		// that says only that it does not parse.
		code, body = doAdmin(t, h.app, http.MethodGet, "/v1/platform/apps", "acme", nil)
		var apps declaredResp
		decodeJSON(t, body, &apps)
		if code != http.StatusOK || len(apps.Apps) != 1 || len(apps.Unreadable) != 1 || apps.Unreadable[0].Reason != unreadableReason {
			t.Fatalf("acme apps: %d %s", code, body)
		}
		code, body = doAdmin(t, h.app, http.MethodGet, "/v1/platform/apps/bad", "acme", nil)
		if code != http.StatusBadGateway || strings.Contains(string(body), "unbalanced") || strings.Contains(string(body), "found character") {
			t.Fatalf("acme/bad: %d %s", code, body)
		}

		// A SuperAdmin reads every broken file, with why.
		code, body = doSuper(t, h.app, http.MethodGet, "/v1/platform/projects", nil)
		board = projectBoard{}
		decodeJSON(t, body, &board)
		if code != http.StatusOK || len(board.Unreadable) != 2 || !strings.Contains(board.Unreadable[1].Reason, "unmarshal") {
			t.Fatalf("super board: %d %s", code, body)
		}

		// Writes elsewhere still land; a write that would need the broken file refuses.
		if code, body := doAdmin(t, h.app, http.MethodPut, "/v1/platform/apps/api/project", "beta", map[string]any{"partOf": "shop"}); code != http.StatusOK {
			t.Fatalf("beta move: %d %s", code, body)
		}
		if code, body := doAdmin(t, h.app, http.MethodPut, "/v1/platform/apps/bad/project", "acme", map[string]any{"partOf": "x"}); code != http.StatusConflict {
			t.Fatalf("moving a file that does not parse: %d %s", code, body)
		}
		if code, body := doAdmin(t, h.app, http.MethodPut, "/v1/platform/projects/shop", "acme", map[string]any{"name": "store"}); code != http.StatusConflict {
			t.Fatalf("renaming around a file that does not parse: %d %s", code, body)
		}
	})
}

// universe's token failing to mint is a 503 with a body: it names the missing
// installation to a SuperAdmin and nothing about the platform to a tenant.
// Never a 502 with nothing in it.
func TestAMissingInstallationIsA503ThatSaysSo(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := harnessOf(t, twoOrgs())
		t.Cleanup(swapUniverseRemote("https://github.com/hanzo-inc/universe"))
		serveGrant(t, refuseGrant)
		tenant := func(code int, body []byte) bool {
			return code == http.StatusServiceUnavailable && strings.Contains(string(body), "the cause is in the platform log") &&
				!strings.Contains(string(body), "hanzo-inc") && !strings.Contains(string(body), "universe")
		}
		if code, body := doAdmin(t, h.app, http.MethodGet, "/v1/platform/projects", "acme", nil); !tenant(code, body) {
			t.Fatalf("tenant read: %d %s", code, body)
		}
		if code, body := doAdmin(t, h.app, http.MethodPut, "/v1/platform/apps/web/project", "acme", map[string]any{"partOf": "shop"}); !tenant(code, body) {
			t.Fatalf("tenant write: %d %s", code, body)
		}
		code, body := doSuper(t, h.app, http.MethodGet, "/v1/platform/projects", nil)
		if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "installation on hanzo-inc, connected by org hanzo") {
			t.Fatalf("super: %d %s", code, body)
		}
	})
}

// A plane that could not be read says so to everyone and says why only to a
// SuperAdmin: the cause names the platform's cluster and service account.
func TestAnUnreadablePlaneIsGenericToATenant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := harnessOf(t, twoOrgs())
		cause := errors.New(`pods is forbidden: User "system:serviceaccount:hanzo:cloud" cannot list resource "pods"`)
		h.dyn.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, cause })
		h.dyn.PrependReactor("list", k8s.CDApplications.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(`applications.apps.hanzo.ai is forbidden: User "system:serviceaccount:hanzo:cloud" cannot list`)
		})
		for _, path := range []string{"/v1/platform/projects", "/v1/platform/projects/web", "/v1/platform/apps"} {
			code, body := doAdmin(t, h.app, http.MethodGet, path, "acme", nil)
			if code != http.StatusOK || strings.Contains(string(body), "serviceaccount") || !strings.Contains(string(body), "Hanzo CD could not be read") {
				t.Errorf("tenant %s: %d %s", path, code, body)
			}
		}
		code, body := doAdmin(t, h.app, http.MethodGet, "/v1/platform/projects", "acme", nil)
		if !strings.Contains(string(body), "the cluster could not be read") {
			t.Errorf("tenant board names no cluster fault: %d %s", code, body)
		}
		code, body = doSuper(t, h.app, http.MethodGet, "/v1/platform/projects?org=acme", nil)
		if code != http.StatusOK || !strings.Contains(string(body), "serviceaccount") {
			t.Errorf("super: %d %s", code, body)
		}
	})
}

// A read of the inventory that started before a write invalidated it answers its
// own caller and is not kept: the next read loads again.
func TestAReadStartedBeforeAWriteIsNotKept(t *testing.T) {
	c := &inventoryCache{}
	stale := inventoryRead{dirs: map[string][]Declaration{"acme": {{Name: "old"}}}}
	got, err := c.get(context.Background(), func(context.Context) (inventoryRead, error) {
		c.invalidate() // a write lands while the clone runs
		return stale, nil
	})
	if err != nil || got.dirs["acme"][0].Name != "old" {
		t.Fatalf("the read did not answer its caller: %v %+v", err, got)
	}
	loads := 0
	fresh := inventoryRead{dirs: map[string][]Declaration{"acme": {{Name: "new"}}}}
	got, _ = c.get(context.Background(), func(context.Context) (inventoryRead, error) { loads++; return fresh, nil })
	if loads != 1 || got.dirs["acme"][0].Name != "new" {
		t.Fatalf("the pre-write read was kept: loads=%d %+v", loads, got)
	}
	got, _ = c.get(context.Background(), func(context.Context) (inventoryRead, error) { loads++; return stale, nil })
	if loads != 1 || got.dirs["acme"][0].Name != "new" {
		t.Fatalf("a read with no write between was not kept: loads=%d", loads)
	}
}

// A tag read the deadline cut short is not remembered as the registry's answer.
func TestACutShortTagReadIsNotRemembered(t *testing.T) {
	latestCache.Lock()
	latestCache.tags = map[string]latestEntry{}
	latestCache.Unlock()
	var slow atomic.Bool
	slow.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			time.Sleep(300 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"token":"anon"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tags":["v1.0.0","v2.0.0"]}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(swapRegistryBase(srv.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got := newestTag(ctx, "ghcr.io/hanzoai/web"); got != "" {
		t.Fatalf("a cut-short read answered %q", got)
	}
	slow.Store(false)
	if got := newestTag(context.Background(), "ghcr.io/hanzoai/web"); got != "v2.0.0" {
		t.Fatalf("the cut-short read was remembered: %q", got)
	}
}

// The pull token follows a tag listing's next page only on the registry's own
// host.
func TestTagPagesStayOnTheRegistry(t *testing.T) {
	latestCache.Lock()
	latestCache.tags = map[string]latestEntry{}
	latestCache.Unlock()
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(`{"tags":["v9.0.0"]}`))
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"token":"anon"}`))
		case r.URL.Query().Get("last") == "":
			w.Header().Add("Link", `</v2/hanzoai/web/tags/list?n=1000&last=v1.0.0>; rel="next"`)
			_, _ = w.Write([]byte(`{"tags":["v1.0.0"]}`))
		default:
			w.Header().Add("Link", `<`+other.URL+`/v2/hanzoai/web/tags/list?last=v2.0.0>; rel="next"`)
			_, _ = w.Write([]byte(`{"tags":["v2.0.0"]}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(swapRegistryBase(srv.URL))
	if got := newestTag(context.Background(), "ghcr.io/hanzoai/web"); got != "v2.0.0" {
		t.Fatalf("newest = %q; the registry's own next page was not followed, or another host's was", got)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the pull token went to another host %d time(s)", n)
	}
}

// A sync is requested only on the Application as read, only once CD has compared
// it, and never while an operation is running or terminating.
func TestSyncRefusesWhatItCannotApply(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := func(name, file string, edit func(o *unstructured.Unstructured)) runtime.Object {
			o := cdApplication(name, file, "OutOfSync", "bbb222", nil)
			o.SetResourceVersion("7")
			if edit != nil {
				edit(o)
			}
			return o
		}
		h := harnessOf(t, map[string]string{
			"charts/app/values/hanzo/a.yaml":               "partOf: p\nimage:\n  repository: ghcr.io/hanzoai/a\n  tag: v1.0.0\n",
			"charts/app/values/hanzo/b.yaml":               "partOf: p\nimage:\n  repository: ghcr.io/hanzoai/b\n  tag: v1.0.0\n",
			"charts/app/values/hanzo/c.yaml":               "partOf: p\nimage:\n  repository: ghcr.io/hanzoai/c\n  tag: v1.0.0\n",
			"charts/app/values/hanzo/d.yaml":               "partOf: p\nimage:\n  repository: ghcr.io/hanzoai/d\n  tag: v1.0.0\n",
			"infra/k8s/hanzo-cd/applicationset-fleet.yaml": fleetSetFixture,
		},
			app("hanzo-a", "values/hanzo/a.yaml", nil),
			app("hanzo-b", "values/hanzo/b.yaml", func(o *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(o.Object, "Terminating", "status", "operationState", "phase")
			}),
			app("hanzo-c", "values/hanzo/c.yaml", func(o *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(o.Object, "", "status", "sync", "revision")
			}),
			app("hanzo-d", "values/hanzo/d.yaml", nil),
		)
		var patch []byte
		h.dyn.PrependReactor("patch", k8s.CDApplications.Resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
			p := a.(k8stesting.PatchAction)
			if p.GetName() == "hanzo-d" {
				return true, nil, apierrors.NewConflict(k8s.CDApplications.GroupResource(), "hanzo-d", errors.New("the object has been modified"))
			}
			patch = p.GetPatch()
			return false, nil, nil
		})

		if code, body := doSuper(t, h.app, http.MethodPost, "/v1/platform/apps/a/sync?org=hanzo", nil); code != http.StatusAccepted {
			t.Fatalf("sync a: %d %s", code, body)
		}
		if !strings.Contains(string(patch), `"resourceVersion":"7"`) {
			t.Fatalf("the sync did not carry the resourceVersion it read: %s", patch)
		}
		for app, why := range map[string]string{"b": "terminating", "c": "not compared", "d": "changed meanwhile"} {
			if code, body := doSuper(t, h.app, http.MethodPost, "/v1/platform/apps/"+app+"/sync?org=hanzo", nil); code != http.StatusConflict {
				t.Errorf("sync %s (%s): %d %s", app, why, code, body)
			}
		}
		obj, err := h.dyn.Resource(k8s.CDApplications).Namespace(cdNamespace).Get(context.Background(), "hanzo-c", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, queued, _ := unstructured.NestedMap(obj.Object, "operation"); queued {
			t.Fatal("an Application CD never compared was synced to its branch")
		}
	})
}

// universe is read as a blob-less partial clone, and the test forge serves one,
// so the path that runs in production is the path under test. A server that
// ignores the filter still leaves the clone marked as a promisor, so the proof is
// a blob outside the checkout that never arrived.
func TestUniverseCloneIsPartial(t *testing.T) {
	files := twoOrgs()
	files["docs/elsewhere.md"] = "outside every path this plane reads\n"
	remote, _ := universeRemoteWith(t, files)
	t.Cleanup(swapUniverseRemote(remote))
	u, err := universeGrant(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	env := pinGitEnv(u)
	err = universeClone(context.Background(), u, func(dir string) error {
		objs, err := runGit(context.Background(), dir, env, "rev-list", "--objects", "--missing=print", "HEAD")
		if err != nil {
			return err
		}
		if !strings.Contains(objs, "\n?") && !strings.HasPrefix(objs, "?") {
			t.Errorf("every blob arrived, so the filter was not honoured:\n%s", objs)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(declarePath("acme", "web")))); err != nil {
			t.Errorf("the sparse checkout lacks the values tree: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
