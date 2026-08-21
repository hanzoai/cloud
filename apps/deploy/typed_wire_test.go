package deploy

// typed_wire_test.go is the wire proof for the pass that typed this plane's reads,
// and the record of what it REFUSED to type and why.
//
// Two things are pinned, and they are different questions:
//
//   - the BYTES. Every typed op here replaced a handler that wrote a
//     map[string]any, and encoding/json sorts a map's keys while it writes a
//     struct's fields in declaration order. So each model declares its fields in
//     the order the map produced, and the tests below compare against the exact
//     bytes the map wrote — not against an equal JSON value.
//   - the REFUSAL SHAPE. The 403 and the sign-in bounce used to be one function a
//     handler called; they are now the op's returned error and one middleware
//     (bounce, scope.go). The tests drive both arms through the real router.
//
// And one thing is MEASURED rather than asserted: what every POST on this plane
// answers to a malformed body. That measurement is the reason those four routes
// are still raw handlers — see TestPostsStayRawBecauseZipDecodesTheBodyFirst.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mount is the plane's whole route table on a real app, which is what the two
// document tests below read their projection from.
func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, fakeService())
	return app
}

// probe drives one request through the REAL router — group middleware included —
// and returns the status and the raw body bytes.
func probe(t *testing.T, s *cloud.Service[state], req *http.Request) (int, string) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, s)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func adminGET(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-User-IsAdmin", "true")
	return req
}

// TestBootstrapReadsAreByteIdentical pins the three bootstrap projections whose
// bodies were map literals. A map marshals its keys SORTED; the models declare
// their fields in that same order, so these comparisons are byte-for-byte and a
// field added out of order fails here rather than drifting into every SDK.
func TestBootstrapReadsAreByteIdentical(t *testing.T) {
	s := fakeService()

	// settings — every value is a constant of the projection.
	const wantSettings = `{"appsInAnyNamespaceEnabled":false,"dexConfig":{"connectors":[]},` +
		`"execEnabled":false,"googleAnalytics":{"anonymizeUsers":true,"trackingID":""},` +
		`"help":{"binaryUrls":{},"chatText":"","chatUrl":""},"hydratorEnabled":false,` +
		`"kustomizeVersions":[],"oidcConfig":null,"plugins":[],"statusBadgeEnabled":false,` +
		`"statusBadgeRootUrl":"","syncWithReplaceAllowed":false,"uiBannerContent":"",` +
		`"uiCssURL":"","url":"https://cd.hanzo.ai","userLoginsDisabled":true}`
	if code, got := probe(t, s, adminGET("/v1/deploy/settings")); code != http.StatusOK || got != wantSettings {
		t.Errorf("GET /v1/deploy/settings = %d %s\n                        want 200 %s", code, got, wantSettings)
	}

	// userinfo, signed in. groups MUST be [] and loginUrl MUST be absent — which is
	// why the model carries *[]string rather than []string with omitempty.
	req := adminGET("/v1/deploy/session/userinfo")
	req.Header.Set("X-User-Id", "u-1")
	const wantUser = `{"groups":[],"iss":"argocd","loggedIn":true,"logoutUrl":"/v1/deploy/logout","username":"u-1"}`
	if code, got := probe(t, s, req); code != http.StatusOK || got != wantUser {
		t.Errorf("GET userinfo (admin) = %d %s\n                 want 200 %s", code, got, wantUser)
	}

	// userinfo, anonymous. loggedIn + loginUrl and NOTHING else — no groups key.
	const wantAnon = `{"loggedIn":false,"loginUrl":"/v1/deploy/login"}`
	code, got := probe(t, s, httptest.NewRequest(http.MethodGet, "/v1/deploy/session/userinfo", nil))
	if code != http.StatusOK || got != wantAnon {
		t.Errorf("GET userinfo (anon) = %d %s\n                want 200 %s", code, got, wantAnon)
	}

	// version — PascalCase keys, all five always present, BuildDate is a timestamp
	// so it is compared by shape rather than by value.
	code, got = probe(t, s, adminGET("/v1/deploy/version"))
	if code != http.StatusOK {
		t.Fatalf("GET /v1/deploy/version = %d %s", code, got)
	}
	var version map[string]any
	if err := json.Unmarshal([]byte(got), &version); err != nil {
		t.Fatalf("version: %v (%s)", err, got)
	}
	for k, want := range map[string]any{"Compiler": "gc", "GoVersion": "", "Platform": "linux/amd64", "Version": "hanzo-cd (projection)"} {
		if version[k] != want {
			t.Errorf("version[%q] = %v, want %v", k, version[k], want)
		}
	}
	if _, ok := version["BuildDate"].(string); !ok || len(version) != 5 {
		t.Errorf("version = %s, want exactly the five VersionMessage keys with a BuildDate string", got)
	}
}

// TestRefusalIsA403AndANavigationIsBounced pins BOTH arms of the refusal across
// the seam this pass moved it over. The DECISION is the typed op's returned 403;
// the SHAPE is bounce (scope.go). An API call must still get the 403 it always
// got, and a browser navigation must still get the sign-in redirect — for a typed
// op and for a raw handler alike.
func TestRefusalIsA403AndANavigationIsBounced(t *testing.T) {
	s := fakeService()
	// One typed op (settings), one raw handler (the applications SSE stream), so
	// the rule is proven on both sides of the seam.
	for _, path := range []string{"/v1/deploy/settings", "/v1/deploy/applications", "/v1/deploy/stream/applications"} {
		// An XHR keeps its 403, body included.
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		code, body := probe(t, s, req)
		if code != http.StatusForbidden {
			t.Errorf("GET %s (xhr, no principal) = %d %s, want 403", path, code, body)
		}
		if !strings.Contains(body, "not authorized for this deploy console") {
			t.Errorf("GET %s 403 body = %s, want the console's own refusal message", path, body)
		}

		// A browser NAVIGATION is bounced to sign-in, carrying where to come back to.
		nav := httptest.NewRequest(http.MethodGet, path, nil)
		nav.Header.Set("Sec-Fetch-Dest", "document")
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		routes(app, s)
		resp, err := app.Test(nav)
		if err != nil {
			t.Fatalf("GET %s (navigation): %v", path, err)
		}
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Errorf("GET %s (navigation) = %d, want 302 to sign-in", path, resp.StatusCode)
		}
		if !strings.HasPrefix(loc, loginPath+"?returnTo=") {
			t.Errorf("GET %s (navigation) Location = %q, want the sign-in page with a returnTo", path, loc)
		}
	}
}

// TestCrossTenantNameIsNotFoundNotForbidden pins that the typed detail reads keep
// the no-oracle rule: an org asking for an application it does not own is told the
// application is not found, never that it exists and is refused.
func TestCrossTenantNameIsNotFoundNotForbidden(t *testing.T) {
	cr := appCR("App", "tenant-acme", "billing", "u1", "ghcr.io/hanzoai/billing", "v1", "Running", 1, 1)
	cr.SetLabels(map[string]string{orgLabel: "acme"})
	s := fakeService(cr)

	for _, path := range []string{
		"/v1/deploy/applications/billing",
		"/v1/deploy/applications/billing/resource-tree",
		"/v1/deploy/applications/billing/syncwindows",
		"/v1/deploy/applications/billing/revisions/HEAD/metadata",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("X-User-Id", "u-2")
		req.Header.Set("X-Org-Id", "globex")
		if code, body := probe(t, s, req); code != http.StatusNotFound {
			t.Errorf("GET %s as another org = %d %s, want 404 (no existence oracle)", path, code, body)
		}
	}

	// And the owner still reads it.
	req := httptest.NewRequest(http.MethodGet, "/v1/deploy/applications/billing", nil)
	req.Header.Set("X-User-Id", "u-1")
	req.Header.Set("X-Org-Id", "acme")
	if code, body := probe(t, s, req); code != http.StatusOK {
		t.Fatalf("GET as the owning org = %d %s, want 200", code, body)
	}

	// A name that is not a DNS-1123 label is refused before any cluster read.
	bad := httptest.NewRequest(http.MethodGet, "/v1/deploy/applications/NOT_A_LABEL", nil)
	bad.Header.Set("X-User-IsAdmin", "true")
	if code, body := probe(t, s, bad); code != http.StatusBadRequest {
		t.Errorf("GET a malformed name = %d %s, want 400", code, body)
	}
}

// TestPostsStayRawBecauseZipDecodesTheBodyFirst is the MEASUREMENT behind the four
// refusals on this plane's POSTs. zip's op.invoke decodes the request body before
// the handler runs and 400s anything it cannot parse (v1.18.11 typed.go:243-247),
// while these routes read no body at all. Typing them would replace every answer
// below with a 400 — and for the three SuperAdmin-gated ones it would put that
// parse error AHEAD of the authorization refusal.
//
// The day zip can declare a bodyless POST (hasBody, openapi.go:270, is method-only)
// this test is what says the four are convertible.
func TestPostsStayRawBecauseZipDecodesTheBodyFirst(t *testing.T) {
	s := fakeService(appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1", "Running", 1, 1))
	for _, tc := range []struct {
		path  string
		admin bool
		want  int
	}{
		{"/v1/deploy/logout", false, http.StatusOK},                           // clears the cookie regardless
		{"/v1/deploy/reconcile", true, http.StatusServiceUnavailable},         // engine disabled
		{"/v1/deploy/reconcile", false, http.StatusForbidden},                 // the gate answers FIRST
		{"/v1/deploy/applications/iam/sync", true, http.StatusOK},             // reconcile requested
		{"/v1/deploy/applications/iam/sync", false, http.StatusForbidden},     // the gate answers FIRST
		{"/v1/deploy/applications/iam/rollback", true, http.StatusOK},         //
		{"/v1/deploy/applications/iam/rollback", false, http.StatusForbidden}, //
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader("{not json"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if tc.admin {
			req.Header.Set("X-User-IsAdmin", "true")
		}
		if code, body := probe(t, s, req); code != tc.want {
			t.Errorf("POST %s (admin=%v, malformed body) = %d %s, want %d — a typed op would answer 400 here",
				tc.path, tc.admin, code, body, tc.want)
		}
	}
}

// TestEveryTypedOpIsInTheDocumentWithProse is the projection proof: each op this
// pass typed carries a description in the app's OWN document, which is the same
// value the MCP tool and the generated SDK method read. It also states, in one
// place, the addresses that are deliberately NOT typed and the wire fact that
// keeps each one raw — so a refusal cannot outlive its reason unnoticed.
//
// TYPED-NESS IS READ FROM THE TYPED REGISTRY, NOT FROM THE PROSE. This test used
// to assert that a deliberately-raw address carries NO description, because
// before openapi.Describe existed the two were the same fact: prose reached the
// document only by zipdoc lifting a typed op's doc comment, so "described" ⟺
// "typed". Describe broke that equivalence on purpose — it is the seam that gives
// an untyped route prose WITHOUT typing it — and an assertion resting on the old
// equivalence would now forbid exactly the thing the seam exists to do, failing
// on prose while a route that genuinely became typed slipped past unnoticed.
// openapi.Typed is the honest signal: a typed op is one zip has in its registry.
//
// So the invariant is strictly stronger than it was: every operation the plane
// serves is EITHER a typed op whose doc comment was lifted, OR a recorded raw
// address whose prose was declared beside its wire fact — and either way it
// carries prose. Nothing on this plane publishes an operationId and nothing else.
func TestEveryTypedOpIsInTheDocumentWithProse(t *testing.T) {
	app := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "deploy", Version: "v1"})
	if err != nil {
		t.Fatalf("openapi.Spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("openapi.Typed: %v", err)
	}

	typed := []string{
		"GET /v1/deploy/applications",
		"GET /v1/deploy/applications/{name}",
		"GET /v1/deploy/applications/{name}/resource-tree",
		"GET /v1/deploy/applications/{name}/revisions/{revision}/metadata",
		"GET /v1/deploy/applications/{name}/syncwindows",
		"GET /v1/deploy/clusters",
		"GET /v1/deploy/gitops",
		"GET /v1/deploy/projects",
		"GET /v1/deploy/session/userinfo",
		"GET /v1/deploy/settings",
		"GET /v1/deploy/version",
	}
	// The addresses that stay raw, each with the measured wire fact that keeps it
	// there. Every one is re-readable against the PINNED zip (v1.18.11).
	raw := map[string]string{
		"GET /v1/deploy/account/can-i/{wildcard1}": "a fiber WILDCARD path: zip renders a typed op's path with " +
			"closeColonParams (openapi.go:407), which leaves '*' alone, while cloud's translate " +
			"(openapi/openapi.go:556) renders the live route as '{wildcard1}' — openapi.Fold then refuses the " +
			"whole document.",
		"GET /v1/deploy/health": "answers 503 carrying the SAME domain body as its 200 (status + the k8s and " +
			"crd booleans); a typed op's only non-2xx is a returned error rendered as the flat HTTPError, and " +
			"zip.WithStatus refuses a non-2xx by design (typed.go:112).",
		"GET /v1/deploy/login":                                    "its success IS a 302 with a Set-Cookie; zip stamps cmp.Or(op.Status, 204) over it (typed.go:305-309).",
		"GET /v1/deploy/callback":                                 "same 302 + Set-Cookie success as login.",
		"GET /v1/deploy/stream/applications":                      "an unbounded text/event-stream; a typed op answers with ONE marshalled Out.",
		"GET /v1/deploy/stream/applications/{name}/resource-tree": "an unbounded text/event-stream, as above.",
		"POST /v1/deploy/logout":                                  "zip decodes the body before the handler and 400s an unparseable one; this route reads none. TestPostsStayRawBecauseZipDecodesTheBodyFirst.",
		"POST /v1/deploy/reconcile":                               "as logout, and the 400 would precede the 403.",
		"POST /v1/deploy/applications/{name}/sync":                "as logout, and the 400 would precede the 403.",
		"POST /v1/deploy/applications/{name}/rollback":            "as logout, and the 400 would precede the 403.",
	}

	described := map[string]bool{}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		for method, op := range item {
			at := strings.ToUpper(method) + " " + path
			served[at] = true
			if op.Description != "" {
				described[at] = true
			}
		}
	}

	for _, at := range typed {
		if !served[at] {
			t.Errorf("%s is not served — the typed op moved or was dropped", at)
			continue
		}
		if reg.Ops[at] == nil {
			t.Errorf("%s is recorded as a typed op but zip's typed registry does not hold it — it was "+
				"un-typed without this list being updated", at)
		}
		if !described[at] {
			t.Errorf("%s carries no description: zipdoc did not lift its doc comment, so it reaches no SDK and no MCP tool", at)
		}
	}
	for at, why := range raw {
		if !served[at] {
			t.Errorf("%s is not served, but is recorded as deliberately untyped (%s) — fix the record", at, why)
			continue
		}
		if reg.Ops[at] != nil {
			t.Errorf("%s is now a typed op, so delete its entry: %s", at, why)
		}
		// A raw address is exempt from being TYPED, never from explaining itself:
		// its prose is declared beside the wire fact instead of being lifted.
		if !described[at] {
			t.Errorf("%s carries no description: it is deliberately raw (%s), so zipdoc has nothing to "+
				"lift and its prose must be declared with openapi.Describe — without it the operation "+
				"publishes an operationId and nothing else", at, why)
		}
	}
	if got, want := len(served), len(typed)+len(raw); got != want {
		t.Errorf("the plane serves %d operations, but this test accounts for %d — a route was added without "+
			"being typed or recorded", got, want)
	}
}

// proseless is the CLOSED list of published properties that carry NO description
// because the SEAM they arrived through cannot carry one — not because nobody
// wrote it. Both classes below already carry the doc comment in the Go source;
// the pass that lifts prose files it under a name the published property does not
// have. Line citations are against the pinned zip, v1.31.0.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and this list must shrink then rather than outlive the gap.
var proseless = map[string]bool{
	// EMBEDDED STRUCT. argoNode embeds argoResourceRef, so zip's schema builder
	// PROMOTES those six fields into argoNode's own properties (wireFields,
	// openapi.go:916) and looks each description up under the OUTER type's name
	// (structSchema, openapi.go:724) — argoNode.group. zipdoc files a field's prose
	// under the type that DECLARES it (extract.go:678), which is argoResourceRef,
	// so the two never meet. The argoResourceRef component publishes all six, and
	// so does every parentRefs entry, which $refs it; only the promoted copies on
	// argoNode are bare. Unrolling the embedding into six copies would fill them in
	// by turning one true statement into two that can drift.
	"argoNode.group":     true,
	"argoNode.kind":      true,
	"argoNode.name":      true,
	"argoNode.namespace": true,
	"argoNode.uid":       true,
	"argoNode.version":   true,

	// ANONYMOUS STRUCT. consoleSettings declares dexConfig, googleAnalytics and
	// help as inline struct literals, and an anonymous struct has no name for
	// zipdoc to key its members under (extract.go:631). Each of the three objects
	// IS described — the comment on its own field lands — but its members cannot
	// be. Naming the three types would fix that and would also change the published
	// document, turning an inline object into a $ref to a new component in every
	// generated SDK: a shape change, decided on purpose, not a side effect of
	// writing prose.
	"consoleSettings.dexConfig.connectors":           true,
	"consoleSettings.googleAnalytics.anonymizeUsers": true,
	"consoleSettings.googleAnalytics.trackingID":     true,
	"consoleSettings.help.binaryUrls":                true,
	"consoleSettings.help.chatText":                  true,
	"consoleSettings.help.chatUrl":                   true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the gate above
// cannot see. Typing a route documents its ADDRESS and its SHAPE; the shape's
// FIELDS come from a different place — a doc comment on each one, which zipdoc
// lifts one at a time.
//
// It matters here because almost every value on this plane is a plain string drawn
// from a CLOSED vocabulary, and three different ones share the surface: a sync
// verdict is Synced|OutOfSync|Unknown, a health verdict is
// Healthy|Progressing|Degraded|Suspended|Missing|Unknown, and an operation phase is
// Running|Succeeded|Failed. `revision` is worse than any of them — it is a git
// COMMIT on a row projected from a Hanzo CD Application and an IMAGE TAG on one
// projected from an operator App CR, the same property under two referents. And a
// whole family of fields is empty BY CONSTRUCTION rather than not-yet-known:
// resourceVersion, orphanedNodes, hosts, attemptedAt, serverVersion, author and
// signatureInfo are never populated, so a caller waiting for one waits forever.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mount(t), openapi.Info{Title: "deploy", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("deploy publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/deploy describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
