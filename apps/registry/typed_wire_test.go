package registry

// typed_wire_test.go — the two ledgers that make this plane a GATE instead of
// a paragraph.
//
// SERVED: every route here is a typed op — there is no untyped route at all —
// and TestEveryRouteIsTyped holds that at zero, so a raw handler added
// tomorrow goes red rather than shipping schema-less.
//
// REFUSED: the product's authored intent (the 13-path Harbor-shaped spec
// hanzoai/openapi deleted as unserved in d86248f) named families the running
// registries do not answer. Those get NO route — a smaller true surface, never
// a bigger fake one — and intentRefused pins each family with the reason,
// measured against the live router (404, absent from the document), so
// reviving one is a deliberate edit.
//
// The fake upstreams below are not invented: the /v2/ 401 challenge, the
// realm's token/access_token envelope, the catalog's Link paging and the npm
// search envelope were each MEASURED against the live oci.hanzo.ai (401
// challenge naming the IAM realm), the IAM GetRegistryToken controller
// (hanzoai/iam controllers/registry_token.go) and the live pkg.hanzo.ai
// verdaccio before this subsystem was written.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// intentRefused is the CLOSED ledger of authored-intent families this plane
// deliberately does NOT serve, keyed by a representative path from the deleted
// spec (relative to /v1/registry). Each reason names what the real backend
// (CNCF distribution + verdaccio + IAM token auth) lacks or must not do.
var intentRefused = map[string]string{
	// Harbor-shaped management rows the running registries do not have. A
	// distribution registry has no project table — the namespace IS the org.
	"/v1/registry/projects/acme":              "a namespace is the IAM org, not a registry row; there is nothing to create, update or delete here",
	"/v1/registry/webhooks":                   "distribution has no per-tenant webhook API (notifications are static deployment config); the fleet's webhook plane is /v1/webhooks",
	"/v1/registry/quotas":                     "no quota engine runs in the registry; storage accounting lives with the S3 backend, unexposed",
	"/v1/registry/projects/acme/repositories": "repository rows live under /v1/registry/images; the Harbor path shape is not served",
	// Real capabilities of the wire deliberately kept OFF this plane.
	"/v1/registry/artifacts": "digest-level artifact reads are the OCI wire itself — clients speak /v2/ on oci.hanzo.ai directly; this plane is control-plane only",
	"/v1/registry/scan":      "no vulnerability scanner is deployed behind the registry; a scan route with no scanner is a fake",
	"/v1/registry/delete":    "destructive manifest deletion ships only after a live proof lane exists for it; an unproven delete on shared images is a footgun, not a feature",
	"/v1/registry/push":      "push credentials are CI custody (KMS-held service accounts), never minted to callers — a push grant here would be privilege escalation",
	// The forge's package ecosystems (go, cargo, … via hanzoai/git on
	// pkg.hanzo.ai/v1/packages) are live but unlisted here for now.
	"/v1/registry/ecosystems": "forge package listing needs the IAM-org→forge-owner mapping, which is unbuilt; npm listing is served because its @scope IS the org slug",
}

// ── fake upstreams: the measured registry wire ──────────────────────────────

// fakeRegistry speaks the distribution wire this subsystem reads: /v2/ with a
// 401 Bearer challenge, a Basic-authed token realm (the IAM GetRegistryToken
// shape), a Link-paged catalog, and per-repo tag lists.
type fakeRegistry struct {
	mu       sync.Mutex
	catalog  []string            // repository names, catalog order
	tags     map[string][]string // repo → tags
	pageSize int                 // catalog page size (0 = all in one page)
	deny     bool                // realm refuses the platform credential when set
	scopes   []string            // every scope the realm was asked for
	basics   []string            // every Basic user the realm saw
	calls    []string            // every authed /v2/ path served
	probes   int                 // every /v2/ challenge probe served
	noAuth   bool                // serve /v2/ open (no challenge) when set
}

// serve mounts the fake's two servers (registry + realm) and returns their
// URLs.
func (f *fakeRegistry) serve(t *testing.T) (reg, realm string) {
	t.Helper()
	realmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		user, pass, ok := r.BasicAuth()
		f.basics = append(f.basics, user)
		f.scopes = append(f.scopes, r.URL.Query()["scope"]...)
		if f.deny || !ok || user != "svc-id" || pass != "svc-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":        "tok-" + strings.Join(r.URL.Query()["scope"], "+"),
			"access_token": "tok-" + strings.Join(r.URL.Query()["scope"], "+"),
			"expires_in":   900,
		})
	}))
	t.Cleanup(realmSrv.Close)

	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		auth := r.Header.Get("Authorization")
		if r.URL.Path == "/v2/" {
			f.probes++
			if f.noAuth {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realmSrv.URL+`/v1/iam/registry/token",service="oci.test"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED"}]}`))
			return
		}
		if !f.noAuth && !strings.HasPrefix(auth, "Bearer tok-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.calls = append(f.calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v2/_catalog":
			last, n := r.URL.Query().Get("last"), len(f.catalog)
			start := 0
			if last != "" {
				for i, name := range f.catalog {
					if name == last {
						start = i + 1
					}
				}
			}
			end := len(f.catalog)
			if f.pageSize > 0 && start+f.pageSize < end {
				end = start + f.pageSize
				w.Header().Set("Link", fmt.Sprintf(`</v2/_catalog?last=%s&n=%d>; rel="next"`, f.catalog[end-1], n))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": f.catalog[start:end]})
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/tags/list")
			tags, ok := f.tags[repo]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"code":"NAME_UNKNOWN"}]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(regSrv.Close)
	return regSrv.URL, realmSrv.URL
}

// fakePkg speaks the verdaccio slice this subsystem reads: /-/ping and
// /-/v1/search with the objects envelope.
type fakePkg struct {
	mu       sync.Mutex
	packages []map[string]any // package rows: name/version/description
	searches []string         // every text= the search saw
}

func (f *fakePkg) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/-/ping":
			_, _ = w.Write([]byte(`{}`))
		case "/-/v1/search":
			f.searches = append(f.searches, r.URL.Query().Get("text"))
			objs := []map[string]any{}
			for _, p := range f.packages {
				objs = append(objs, map[string]any{"updated": "2026-07-30T00:00:00.000Z", "package": p})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"objects": objs, "total": len(objs)})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"File not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes says why): the program's composer installs it once at the root, after
// the identity check and before any route registers. In a test the test IS the
// composer, so it owes the same install — a test that skips it does not test a
// stricter program, it tests one where every org-scoped op answers 403 for a
// reason that would never exist in production.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// harness mounts the app against fake upstreams and returns all three.
func harness(t *testing.T) (*zip.App, *fakeRegistry, *fakePkg) {
	t.Helper()
	f := &fakeRegistry{
		catalog: []string{"acme/api", "acme/web", "charts", "globex/secret"},
		tags: map[string][]string{
			"acme/api":      {"v1.0.0", "v1.0.1"},
			"acme/web":      {"latest"},
			"globex/secret": {"v9"},
		},
	}
	reg, _ := f.serve(t)
	p := &fakePkg{packages: []map[string]any{
		{"name": "@acme/ui", "version": "2.1.0", "description": "acme ui kit"},
		{"name": "acme", "version": "1.0.2", "description": "acme cli"},
		{"name": "@globex/x", "version": "0.1.0"},
		{"name": "other", "version": "3.0.0"},
	}}
	t.Setenv("REGISTRY_UPSTREAM", reg)
	t.Setenv("REGISTRY_PKG", p.serve(t))
	t.Setenv("REGISTRY_CLIENT_ID", "svc-id")
	t.Setenv("REGISTRY_CLIENT_SECRET", "svc-secret")

	app := zip.New(zip.Config{Logger: luxlog.New("registrytest"), DisableStartupMessage: true})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app, f, p
}

// do drives one request with minted identity headers (as SanitizeIdentity
// would set them): user!="" makes a validated principal.
func do(t *testing.T, app *zip.App, method, path, user, org, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test(%s %s): %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// ── the served ledger ───────────────────────────────────────────────────────

// TestEveryRouteIsTyped holds the untyped count at ZERO: every operation this
// plane serves is a typed op, so each carries schema, prose, an MCP tool, a
// CLI command and an SDK method. A raw route added tomorrow fails here.
func TestEveryRouteIsTyped(t *testing.T) {
	app, _, _ := harness(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "registry", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("registry serves no operations at all — the mount did not register")
	}
	var untyped []string
	for key := range served {
		if _, ok := reg.Ops[key]; !ok {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped operation(s) on a fully-typed plane: %s", strings.Join(untyped, ", "))
	}
	if len(reg.Ops) != len(served) {
		t.Errorf("%d typed ops but %d served operations — the two projections disagree", len(reg.Ops), len(served))
	}
}

// TestIntentStaysRefused measures the refusal ledger: every named family
// answers a route-level 404 on the live router and appears nowhere in the
// published document. A route added at one of these paths makes this fail,
// which is the point — reviving a family is a decision recorded by editing the
// ledger, never a drive-by.
func TestIntentStaysRefused(t *testing.T) {
	app, _, _ := harness(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "registry", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for path, reason := range intentRefused {
		if reason == "" {
			t.Errorf("refused path %q carries no reason", path)
		}
		status, _ := do(t, app, http.MethodGet, path, "u1", "acme", "")
		if status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want the router's 404 — the refused family answered", path, status)
		}
		for docPath := range doc.Paths {
			if docPath == path || strings.HasPrefix(docPath, path+"/") {
				t.Errorf("document publishes %s, which the ledger refuses", docPath)
			}
		}
	}
}

// ── tenancy (the bar) ───────────────────────────────────────────────────────

// A request with no validated principal is refused 403 and NEVER reaches an
// upstream — the forged-X-Org-Id path is dead.
func TestNoPrincipalIs403AndNoUpstreamByte(t *testing.T) {
	app, f, p := harness(t)
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/registry/status", ""},
		{http.MethodGet, "/v1/registry/projects", ""},
		{http.MethodGet, "/v1/registry/images", ""},
		{http.MethodGet, "/v1/registry/tags?image=api", ""},
		{http.MethodGet, "/v1/registry/packages", ""},
		{http.MethodPost, "/v1/registry/token", `{"image":"api"}`},
	} {
		status, body := do(t, app, probe.method, probe.path, "", "victim", probe.body)
		if status != http.StatusForbidden {
			t.Errorf("%s %s without principal = %d, want 403; body=%s", probe.method, probe.path, status, body)
		}
	}
	f.mu.Lock()
	touched := len(f.calls) + len(f.scopes) + f.probes
	f.mu.Unlock()
	p.mu.Lock()
	touched += len(p.searches)
	p.mu.Unlock()
	if touched != 0 {
		t.Errorf("upstreams were contacted %d time(s) for unvalidated requests; must be 0", touched)
	}
}

// A signed-in caller whose token names NO org is refused too, and just as early.
// Every repository name on this plane is `<org>/<image>`, so a request with no
// tenant cannot address anything: there is no whole-registry view to fall back
// to, and inventing one would be the cross-tenant read the org segment exists to
// make inexpressible. Same 403 the anonymous caller gets — the status is the
// contract — and the same zero upstream bytes.
func TestValidatedWithNoOrgIsRefused(t *testing.T) {
	app, f, p := harness(t)
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/registry/status", ""},
		{http.MethodGet, "/v1/registry/projects", ""},
		{http.MethodGet, "/v1/registry/images", ""},
		{http.MethodGet, "/v1/registry/tags?image=api", ""},
		{http.MethodGet, "/v1/registry/packages", ""},
		{http.MethodPost, "/v1/registry/token", `{"image":"api"}`},
	} {
		status, body := do(t, app, probe.method, probe.path, "u1", "", probe.body)
		if status != http.StatusForbidden {
			t.Errorf("%s %s validated with no org = %d, want 403; body=%s", probe.method, probe.path, status, body)
		}
	}
	f.mu.Lock()
	touched := len(f.calls) + len(f.scopes) + f.probes
	f.mu.Unlock()
	p.mu.Lock()
	touched += len(p.searches)
	p.mu.Unlock()
	if touched != 0 {
		t.Errorf("upstreams were contacted %d time(s) for a tenant-less caller; must be 0", touched)
	}
}

// The catalog is filtered to the org's namespace BEFORE the response exists:
// acme sees exactly its own repositories (org-relative names, full refs),
// never globex's and never the unprefixed platform names.
func TestImagesAreOrgScoped(t *testing.T) {
	app, f, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/registry/images", "u1", "acme", "")
	if status != http.StatusOK {
		t.Fatalf("images = %d %s", status, body)
	}
	var out struct {
		Data []struct{ Name, Ref string } `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("images unparseable: %s", body)
	}
	if len(out.Data) != 2 || out.Data[0].Name != "api" || out.Data[1].Name != "web" {
		t.Fatalf("acme images = %+v, want exactly api and web", out.Data)
	}
	for _, leak := range []string{"globex", "secret", "charts"} {
		if strings.Contains(body, leak) {
			t.Errorf("images response leaked %q: %s", leak, body)
		}
	}
	if !strings.HasSuffix(out.Data[0].Ref, "/acme/api") {
		t.Errorf("ref %q does not address the org namespace", out.Data[0].Ref)
	}

	// The walk survives catalog paging: one-name pages, same answer.
	f.mu.Lock()
	f.pageSize = 1
	f.mu.Unlock()
	status, paged := do(t, app, http.MethodGet, "/v1/registry/images", "u1", "acme", "")
	if status != http.StatusOK || paged != body {
		t.Fatalf("paged walk diverged: %d %s (unpaged %s)", status, paged, body)
	}
}

// Tags are read through the owned gate: the org segment comes from the
// principal, so a foreign repository cannot be expressed — globex's repo name
// under acme resolves to acme/… and answers 404, and hostile shapes never
// reach the upstream.
func TestTagsAreOrgScoped(t *testing.T) {
	app, f, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/registry/tags?image=api", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, "v1.0.1") {
		t.Fatalf("tags = %d %s", status, body)
	}
	// globex reads its own repo fine…
	status, body = do(t, app, http.MethodGet, "/v1/registry/tags?image=secret", "u2", "globex", "")
	if status != http.StatusOK || !strings.Contains(body, "v9") {
		t.Fatalf("globex tags = %d %s", status, body)
	}
	// …and acme cannot address it: acme/secret does not exist.
	status, _ = do(t, app, http.MethodGet, "/v1/registry/tags?image=secret", "u1", "acme", "")
	if status != http.StatusNotFound {
		t.Fatalf("cross-org tags = %d, want 404", status)
	}
	// A hostile image shape is refused before any upstream byte.
	for _, evil := range []string{"../catalog", "a//b", "UPPER", "a b", strings.Repeat("x", 300)} {
		status, _ = do(t, app, http.MethodGet, "/v1/registry/tags?image="+esc(evil), "u1", "acme", "")
		if status != http.StatusBadRequest {
			t.Errorf("hostile image %q = %d, want 400", evil, status)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, "..") || strings.Contains(c, "//") {
			t.Errorf("hostile shape reached the upstream: %s", c)
		}
	}
}

// esc query-escapes the characters the hostile probes carry.
func esc(v string) string {
	return strings.NewReplacer("/", "%2F", " ", "%20").Replace(v)
}

// A minted token is pull-only and org-pinned: the realm sees exactly
// repository:<org>/<image>:pull, the caller gets the realm's token verbatim,
// and the platform credential appears in no response.
func TestTokenIsPullScopedAndOrgPinned(t *testing.T) {
	app, f, _ := harness(t)
	status, body := do(t, app, http.MethodPost, "/v1/registry/token", "u1", "acme", `{"image":"api"}`)
	if status != http.StatusOK {
		t.Fatalf("token = %d %s", status, body)
	}
	var out struct {
		Token   string `json:"token"`
		Expires int    `json:"expires"`
		Ref     string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Token == "" {
		t.Fatalf("token unparseable: %s", body)
	}
	if out.Expires != 900 || !strings.HasSuffix(out.Ref, "/acme/api") {
		t.Fatalf("token view = %+v", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, s := range f.scopes {
		if s == "repository:acme/api:pull" {
			found = true
		}
		if strings.Contains(s, "push") || strings.Contains(s, "delete") || strings.Contains(s, "*") {
			t.Errorf("realm was asked for a privileged scope: %q", s)
		}
	}
	if !found {
		t.Errorf("realm never saw the pinned pull scope; saw %v", f.scopes)
	}
	if strings.Contains(body, "svc-secret") || strings.Contains(body, "svc-id") {
		t.Errorf("platform credential leaked into a response: %s", body)
	}
}

// A realm that refuses the platform credential is a deployment fault: the
// caller sees 503, never a 401/403 that would read as their own auth failing.
func TestCredentialRefusalIs503(t *testing.T) {
	app, f, _ := harness(t)
	f.mu.Lock()
	f.deny = true
	f.mu.Unlock()
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/registry/images", ""},
		{http.MethodPost, "/v1/registry/token", `{"image":"api"}`},
	} {
		status, body := do(t, app, probe.method, probe.path, "u1", "acme", probe.body)
		if status != http.StatusServiceUnavailable {
			t.Errorf("%s %s under credential refusal = %d (%s), want 503", probe.method, probe.path, status, body)
		}
		if strings.Contains(body, "invalid credentials") {
			t.Errorf("realm detail leaked to the caller: %s", body)
		}
	}
}

// Packages are the org's scope only — `<org>` and `@<org>/…` — and a caller
// query narrows but never widens it.
func TestPackagesAreOrgScoped(t *testing.T) {
	app, _, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/registry/packages", "u1", "acme", "")
	if status != http.StatusOK {
		t.Fatalf("packages = %d %s", status, body)
	}
	var out struct {
		Data []struct{ Name, Version string } `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Data) != 2 {
		t.Fatalf("acme packages = %s, want exactly @acme/ui and acme", body)
	}
	for _, leak := range []string{"globex", "other"} {
		if strings.Contains(body, leak) {
			t.Errorf("packages response leaked %q: %s", leak, body)
		}
	}
	// A query that names a foreign package still cannot cross the scope.
	status, body = do(t, app, http.MethodGet, "/v1/registry/packages?query=other", "u1", "acme", "")
	if status != http.StatusOK || strings.Contains(body, "other") {
		t.Fatalf("query widened the org scope: %d %s", status, body)
	}
}

// Projects composes both halves into the caller's one namespace row with live
// counts.
func TestProjectsComposeBothHalves(t *testing.T) {
	app, _, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/registry/projects", "u1", "acme", "")
	if status != http.StatusOK {
		t.Fatalf("projects = %d %s", status, body)
	}
	var out struct {
		Data []struct {
			Project  string `json:"project"`
			Images   int    `json:"images"`
			Packages int    `json:"packages"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Data) != 1 {
		t.Fatalf("projects unparseable: %s", body)
	}
	if row := out.Data[0]; row.Project != "acme" || row.Images != 2 || row.Packages != 2 {
		t.Fatalf("project row = %+v, want acme/2/2", out.Data[0])
	}
}

// The reachability lens answers honestly in both directions: hosts + realm
// when the registries are up, false — not an error — when nothing listens. An
// auth-gated OCI half is REACHABLE by its challenge, which is also where the
// realm comes from.
func TestStatusIsAnHonestLens(t *testing.T) {
	app, _, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/registry/status", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"oci":true`) || !strings.Contains(body, `"pkg":true`) ||
		!strings.Contains(body, "/v1/iam/registry/token") || !strings.Contains(body, `"service":"oci.test"`) {
		t.Fatalf("status(up) = %d %s", status, body)
	}

	down := zip.New(zip.Config{Logger: luxlog.New("registrytest"), DisableStartupMessage: true})
	compose(down)
	t.Setenv("REGISTRY_UPSTREAM", "http://127.0.0.1:1")
	t.Setenv("REGISTRY_PKG", "http://127.0.0.1:1")
	if err := Mount(down, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	status, body = do(t, down, http.MethodGet, "/v1/registry/status", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"oci":false`) || !strings.Contains(body, `"pkg":false`) {
		t.Fatalf("status(down) = %d %s, want an honest oci:false/pkg:false", status, body)
	}
}

// An auth-less registry (dev posture) needs no credential for reads and
// refuses the token op honestly — there is no realm to mint from.
func TestAuthlessRegistryReadsWork(t *testing.T) {
	app, f, _ := harness(t)
	f.mu.Lock()
	f.noAuth = true
	f.mu.Unlock()
	t.Setenv("REGISTRY_CLIENT_ID", "")
	t.Setenv("REGISTRY_CLIENT_SECRET", "")
	status, body := do(t, app, http.MethodGet, "/v1/registry/images", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, "api") {
		t.Fatalf("auth-less images = %d %s", status, body)
	}
	status, _ = do(t, app, http.MethodPost, "/v1/registry/token", "u1", "acme", `{"image":"api"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("auth-less token = %d, want 503 (no realm to mint from)", status)
	}
}
