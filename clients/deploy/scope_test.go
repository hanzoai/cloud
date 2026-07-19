package deploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	iamobj "github.com/hanzoai/iam/object"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/provisioning"
	"github.com/zap-proto/zip"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

// superScope is the whole-fleet SuperAdmin scope — the pre-tenant behavior. Used by the
// stream unit tests (which exercise the projection core directly).
func superScope() scope { return scope{superAdmin: true} }

// orgAppCR builds an operator App CR stamped with the tenant + project labels the platform
// stamps (hanzo.ai/org, app.kubernetes.io/part-of). ns is the namespace the CR lives in
// (tenant-<org> for a tenant app).
func orgAppCR(ns, name, org, project string) *unstructured.Unstructured {
	cr := appCR("App", ns, name, "uid-"+ns+"-"+name, "ghcr.io/hanzoai/"+name, "v1", "Running", 1, 1)
	labels := map[string]string{}
	if org != "" {
		labels[orgLabel] = org
	}
	if project != "" {
		labels[projectLabel] = project
	}
	_ = unstructured.SetNestedStringMap(cr.Object, labels, "metadata", "labels")
	return cr
}

// getAs drives a GET through the full router with the given identity headers and returns
// the response (no status assertion — callers test 200/403/404). Mirrors production: the
// gateway/SanitizeIdentity mints X-Org-Id / X-User-Id / X-User-IsAdmin; a test sets them.
func getAs(t *testing.T, s *cloud.Service[state], path string, headers map[string]string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// jsonBody decodes a JSON response body into a map.
func jsonBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// orgHeaders is a VALIDATED org member's identity (X-User-Id present ⇒ principal.Validated,
// X-Org-Id the tenant, no X-User-IsAdmin).
func orgHeaders(org string) map[string]string {
	return map[string]string{"X-Org-Id": org, "X-User-Id": "u_" + org}
}

// appNames collects the projected application names from an ApplicationList body.
func appNames(body map[string]any) map[string]bool {
	out := map[string]bool{}
	items, _ := body["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		meta, _ := m["metadata"].(map[string]any)
		if n, _ := meta["name"].(string); n != "" {
			out[n] = true
		}
	}
	return out
}

// ── resolveScope: the tenant boundary (pure) ─────────────────────────────────

// probeScope resolves a scope from a set of identity headers, driven through a real ctx.
func probeScope(t *testing.T, headers map[string]string) (scope, bool) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	var got scope
	var ok bool
	app.Get("/probe", func(c *zip.Ctx) error {
		got, ok = resolveScope(c)
		return c.JSON(http.StatusOK, map[string]any{})
	})
	req := httptest.NewRequest("GET", "/probe", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	_ = resp.Body.Close()
	return got, ok
}

// TestResolveScope_Boundary proves resolveScope reuses the SAME boundary as
// clients/platform.tenant: SuperAdmin by c.IsAdmin() alone (whole-fleet), a normal org only
// when VALIDATED (X-User-Id present) with a non-empty sanitized org, everything else CLOSED.
func TestResolveScope_Boundary(t *testing.T) {
	// SuperAdmin: c.IsAdmin() alone ⇒ whole-fleet (org == "").
	if sc, ok := probeScope(t, map[string]string{"X-User-IsAdmin": "true"}); !ok || !sc.superAdmin || sc.org != "" {
		t.Fatalf("admin scope = %+v ok=%v, want {superAdmin,org:\"\"}", sc, ok)
	}
	// A SuperAdmin whose X-Org-Id is also set STILL sees the whole fleet (admin wins).
	if sc, ok := probeScope(t, map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "acme", "X-User-Id": "u"}); !ok || !sc.superAdmin {
		t.Fatalf("admin+org scope = %+v ok=%v, want superAdmin", sc, ok)
	}
	// Validated org member ⇒ org scope, sanitized.
	if sc, ok := probeScope(t, orgHeaders("acme")); !ok || sc.superAdmin || sc.org != "acme" {
		t.Fatalf("org scope = %+v ok=%v, want {org:acme}", sc, ok)
	}
	// resolveScope keys the org through the SAME injective provisioning.SanitizeOrg the CR
	// label filter uses — so a resolved org and the hanzo.ai/org label compare like-for-like,
	// and two distinct owners never collide onto one tenant. (Uppercase/dirty inputs are NOT
	// identity-mapped; they carry a hash suffix, which is exactly the injectivity guarantee.)
	for _, raw := range []string{"acme", "ACME", "team1"} {
		sc, ok := probeScope(t, map[string]string{"X-Org-Id": raw, "X-User-Id": "u"})
		want := provisioning.SanitizeOrg(raw)
		if want == "" {
			t.Fatalf("test input %q unexpectedly sanitized to empty", raw)
		}
		if !ok || sc.org != want {
			t.Fatalf("org(%q) scope = %+v ok=%v, want org %q (SanitizeOrg)", raw, sc, ok, want)
		}
	}
	// An org carrying an unsafe rune (whitespace) is refused by SanitizeOrg (→ "") — a
	// non-injective identifier — so resolveScope fails closed, never a fabricated tenant.
	if sc, ok := probeScope(t, map[string]string{"X-Org-Id": "bad org", "X-User-Id": "u"}); ok {
		t.Fatalf("whitespace org resolved a scope %+v — must fail closed", sc)
	}
	// FORGED X-Org-Id with NO validated principal (no X-User-Id) ⇒ fail closed.
	if sc, ok := probeScope(t, map[string]string{"X-Org-Id": "victim"}); ok {
		t.Fatalf("forged X-Org-Id resolved a scope: %+v — must fail closed", sc)
	}
	// Validated but EMPTY org, not admin ⇒ fail closed.
	if _, ok := probeScope(t, map[string]string{"X-User-Id": "u"}); ok {
		t.Fatal("empty-org validated non-admin resolved a scope — must fail closed")
	}
	// Nothing ⇒ fail closed.
	if _, ok := probeScope(t, map[string]string{}); ok {
		t.Fatal("anonymous resolved a scope — must fail closed")
	}
}

// ── applications: org scoping + cross-org isolation ──────────────────────────

// twoTenantFleet is a fleet with apps for two tenants (in their tenant-<org> namespaces)
// plus a system app in the platform "hanzo" namespace.
func twoTenantFleet() *cloud.Service[state] {
	return fakeSvc(
		orgAppCR("tenant-acme", "acme-web", "acme", "storefront"),
		orgAppCR("tenant-acme", "acme-api", "acme", "storefront"),
		orgAppCR("tenant-bravo", "bravo-web", "bravo", "site"),
		orgAppCR("hanzo", "cloud", "hanzo", ""), // a system/fleet app (platform namespace)
	)
}

// TestDashAppList_OrgSeesOnlyItsApps (requirement a): a normal-org caller sees ONLY apps
// labeled its org, in its tenant namespace — and the projection surfaces the org label.
func TestDashAppList_OrgSeesOnlyItsApps(t *testing.T) {
	s := twoTenantFleet()
	resp := getAs(t, s, "/v1/deploy/applications", orgHeaders("acme"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("acme /applications = %d, want 200", resp.StatusCode)
	}
	names := appNames(jsonBody(t, resp))
	if !names["acme-web"] || !names["acme-api"] {
		t.Fatalf("acme missing its own apps: %v", names)
	}
	if names["bravo-web"] || names["cloud"] {
		t.Fatalf("acme sees another tenant's/system apps (CROSS-ORG LEAK): %v", names)
	}
	if len(names) != 2 {
		t.Fatalf("acme app count = %d, want exactly 2 (its own): %v", len(names), names)
	}
}

// TestDashAppList_CrossOrgIsolation (requirement b): org B never sees org A's apps — not
// with its own header, and not even claiming A's org without a validated principal.
func TestDashAppList_CrossOrgIsolation(t *testing.T) {
	s := twoTenantFleet()

	// bravo sees only bravo.
	bravo := appNames(jsonBody(t, getAs(t, s, "/v1/deploy/applications", orgHeaders("bravo"))))
	if !bravo["bravo-web"] || bravo["acme-web"] || bravo["acme-api"] || bravo["cloud"] {
		t.Fatalf("bravo sees non-bravo apps (CROSS-ORG LEAK): %v", bravo)
	}

	// bravo forging X-Org-Id: acme WITHOUT a validated principal (no X-User-Id) ⇒ 403,
	// never acme's fleet.
	resp := getAs(t, s, "/v1/deploy/applications", map[string]string{"X-Org-Id": "acme"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged X-Org-Id:acme (no principal) = %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestDashAppList_SuperAdminSeesFleet (requirement c): a SuperAdmin sees the whole platform
// fleet (the scanOrder namespaces) exactly as before — the e2e 108/109 contract.
func TestDashAppList_SuperAdminSeesFleet(t *testing.T) {
	s := twoTenantFleet()
	names := appNames(jsonBody(t, getAs(t, s, "/v1/deploy/applications", map[string]string{"X-User-IsAdmin": "true"})))
	if !names["cloud"] {
		t.Fatalf("SuperAdmin missing the fleet's system app: %v", names)
	}
}

// TestDashApp_CrossOrgAppIdIs404 (requirement b): an app id from org A, requested by org B,
// returns a clean 404 — never confirmed to exist (no cross-tenant oracle).
func TestDashApp_CrossOrgAppIdIs404(t *testing.T) {
	s := twoTenantFleet()
	for _, route := range []string{"/v1/deploy/applications/acme-web", "/v1/deploy/applications/acme-web/resource-tree"} {
		resp := getAs(t, s, route, orgHeaders("bravo"))
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bravo GET %s = %d, want 404 (acme's app must be invisible)", route, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	// acme reaches its own app.
	resp := getAs(t, s, "/v1/deploy/applications/acme-web", orgHeaders("acme"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("acme GET its own app = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestDashClusters_OrgCountsOnlyItsApps (requirement b): the ClusterList a tenant sees
// counts ONLY its own apps, and still leaks no cluster credential.
func TestDashClusters_OrgCountsOnlyItsApps(t *testing.T) {
	s := twoTenantFleet()
	body := jsonBody(t, getAs(t, s, "/v1/deploy/clusters", orgHeaders("acme")))
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("acme clusters = %v, want exactly one (in-cluster)", items)
	}
	c0 := items[0].(map[string]any)
	info, _ := c0["info"].(map[string]any)
	if info["applicationsCount"].(float64) != 2 {
		t.Fatalf("acme in-cluster count = %v, want 2 (acme's apps only, not bravo/system)", info["applicationsCount"])
	}
	raw, _ := json.Marshal(body)
	for _, forbidden := range []string{"bearerToken", "tlsClientConfig", "execProviderConfig", "keyData"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("tenant clusters leaked %q: %s", forbidden, raw)
		}
	}
}

// TestDashReads_FailClosedOnUnvalidatedOrg (requirement f): every scoped READ route fails
// closed (403) for a forged X-Org-Id with no validated principal, and for an empty org.
func TestDashReads_FailClosedOnUnvalidatedOrg(t *testing.T) {
	s := twoTenantFleet()
	readRoutes := []string{
		"/v1/deploy/applications",
		"/v1/deploy/applications/acme-web",
		"/v1/deploy/applications/acme-web/resource-tree",
		"/v1/deploy/clusters",
		"/v1/deploy/projects",
		"/v1/deploy/stream/applications",
	}
	for _, r := range readRoutes {
		// Forged org, no validated principal (no X-User-Id).
		resp := getAs(t, s, r, map[string]string{"X-Org-Id": "acme"})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("forged-org GET %s = %d, want 403", r, resp.StatusCode)
		}
		_ = resp.Body.Close()
		// Validated but empty org, not admin.
		resp2 := getAs(t, s, r, map[string]string{"X-User-Id": "u"})
		if resp2.StatusCode != http.StatusForbidden {
			t.Errorf("empty-org GET %s = %d, want 403", r, resp2.StatusCode)
		}
		_ = resp2.Body.Close()
	}
}

// ── spec.project from part-of (projection) ───────────────────────────────────

// TestProjectApp_SpecProjectFromPartOf (requirement d): spec.project is read from the
// app.kubernetes.io/part-of label (the IAM Project), not hard-coded, and the tenant label
// is surfaced in the projection.
func TestProjectApp_SpecProjectFromPartOf(t *testing.T) {
	app := projectApp(orgAppCR("tenant-acme", "acme-web", "acme", "storefront"), "tenant-acme", "v1")
	if app.Spec.Project != "storefront" {
		t.Fatalf("spec.project = %q, want storefront (from part-of)", app.Spec.Project)
	}
	if app.Metadata.Labels[orgLabel] != "acme" {
		t.Fatalf("projected labels missing hanzo.ai/org=acme: %v", app.Metadata.Labels)
	}
}

// TestProjectApp_UnlabeledFallsIntoDefault (requirement e): an App CR with no part-of label
// projects into the "default" project, and carries no org label when the CR has none.
func TestProjectApp_UnlabeledFallsIntoDefault(t *testing.T) {
	// The bare appCR helper carries NO labels at all.
	app := projectApp(appCR("App", "hanzo", "cloud", "u1", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1), "hanzo", "v1")
	if app.Spec.Project != "default" {
		t.Fatalf("spec.project = %q, want default (no part-of)", app.Spec.Project)
	}
	if _, present := app.Metadata.Labels[orgLabel]; present {
		t.Fatalf("unlabeled CR projected an org label: %v", app.Metadata.Labels)
	}
}

// ── projects: IAM reflection + cross-org isolation ───────────────────────────

// TestDashProjects_OrgNeverSeesCrossOrgAppProjects (requirement b): a normal org's /projects
// NEVER surfaces the unscoped cluster-wide AppProject list (that path is SuperAdmin-only) —
// even when real AppProject CRs are served — and always contains 'default'.
func TestDashProjects_OrgNeverSeesCrossOrgAppProjects(t *testing.T) {
	s := fakeSvc(
		orgAppCR("tenant-acme", "acme-web", "acme", "storefront"),
		appProjectCR("team-secret", "https://git.hanzo.ai/team-secret/*"), // a cross-org real AppProject CR
	)
	body := jsonBody(t, getAs(t, s, "/v1/deploy/projects", orgHeaders("acme")))
	items, _ := body["items"].([]any)
	names := map[string]bool{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		meta, _ := m["metadata"].(map[string]any)
		names[meta["name"].(string)] = true
	}
	if names["team-secret"] {
		t.Fatalf("a normal org saw a cluster-wide AppProject CR (CROSS-ORG LEAK): %v", names)
	}
	if !names["default"] {
		t.Fatalf("org /projects missing 'default': %v", names)
	}
}

// TestProjectFromIAM_Reflects (requirement a, pure): an IAM Project reflects to a permissive
// argo AppProject — name = Project.Name, description from DisplayName, an org label — and
// surfaces NONE of Tags/Metadata.
func TestProjectFromIAM_Reflects(t *testing.T) {
	p := &iamobj.Project{
		Owner: "acme", Name: "storefront", Organization: "acme",
		DisplayName: "Storefront", Description: "the shop", IsDefault: false,
		Tags: []string{"secret-tag"}, Metadata: `{"secret":"x"}`,
	}
	proj := projectFromIAM(p)
	if proj.Metadata.Name != "storefront" {
		t.Fatalf("name = %q, want storefront", proj.Metadata.Name)
	}
	if proj.Spec.Description != "Storefront" {
		t.Fatalf("description = %q, want Storefront (DisplayName)", proj.Spec.Description)
	}
	if proj.Metadata.Labels[orgLabel] != "acme" {
		t.Fatalf("org label = %v, want acme", proj.Metadata.Labels)
	}
	b, _ := json.Marshal(proj)
	for _, forbidden := range []string{"secret-tag", "secret", "Metadata", "tags"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("projected IAM project leaked %q: %s", forbidden, b)
		}
	}
}

// TestEnsureDefault (invariant): 'default' is prepended when absent, a no-op when present.
func TestEnsureDefault(t *testing.T) {
	got := ensureDefault(nil)
	if len(got) != 1 || got[0].Metadata.Name != "default" {
		t.Fatalf("ensureDefault(nil) = %v, want [default]", got)
	}
	withDefault := ensureDefault([]argoProject{synthProject("default"), synthProject("team-a")})
	count := 0
	for _, p := range withDefault {
		if p.Metadata.Name == "default" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ensureDefault duplicated 'default': %d copies", count)
	}
}

// ── stream: org scoping ──────────────────────────────────────────────────────

// TestStreamBurst_OrgScoped: a tenant's initial burst emits ONLY its own apps.
func TestStreamBurst_OrgScoped(t *testing.T) {
	s := twoTenantFleet()
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if ok := streamAppBurst(s, scope{org: "acme"}, context.Background(), w); !ok {
		t.Fatal("streamAppBurst(acme) returned false")
	}
	_ = w.Flush()
	frames := parseSSE(buf.String())
	names := map[string]bool{}
	for _, f := range frames {
		var env struct {
			Result applicationWatchEvent `json:"result"`
		}
		if err := json.Unmarshal([]byte(f), &env); err != nil {
			t.Fatalf("bad frame %q: %v", f, err)
		}
		names[env.Result.Application.Metadata.Name] = true
	}
	if !names["acme-web"] || !names["acme-api"] {
		t.Fatalf("acme burst missing its apps: %v", names)
	}
	if names["bravo-web"] || names["cloud"] {
		t.Fatalf("acme burst leaked another tenant's/system app: %v", names)
	}
}

// TestForwardWatch_DropsCrossTenantObject: an org's watch forwarder drops a watched object
// that belongs to another tenant (wrong org label) and one outside its namespace.
func TestForwardWatch_DropsCrossTenantObject(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw := watch.NewFake()
	events := make(chan streamEvent, 4)
	go forwardWatch(ctx, scope{org: "acme"}, "tenant-acme", fw, events)

	go func() {
		// bravo's app in bravo's namespace — must be dropped.
		fw.Action(watch.Added, orgAppCR("tenant-bravo", "bravo-web", "bravo", "site"))
		// an object in acme's namespace but mislabeled bravo — must be dropped (label filter).
		fw.Action(watch.Added, orgAppCR("tenant-acme", "sneaky", "bravo", "x"))
		// acme's own app — must come through.
		fw.Action(watch.Added, orgAppCR("tenant-acme", "acme-web", "acme", "storefront"))
	}()

	select {
	case ev := <-events:
		if ev.obj.GetName() != "acme-web" {
			t.Fatalf("forwarded a cross-tenant object: %q (want only acme-web)", ev.obj.GetName())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acme's own event never arrived (over-filtered)")
	}
}
