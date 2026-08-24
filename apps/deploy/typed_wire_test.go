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
// answers with an empty body and with a malformed one. The first is what every
// real caller sends and is unchanged by typing them; the second is the ONE answer
// the conversion moved, and it is recorded as a measurement rather than a claim —
// TestTheBodylessPostsKeepTheirWire and TestAMalformedBodyIsTheOneAnswerThatMoved.

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
	routes(app, fakeService())
	return app
}

// probe drives one request through the REAL router — group middleware included —
// and returns the status and the raw body bytes.
func probe(t *testing.T, s *cloud.Service[state], req *http.Request) (int, string) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
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
// the client this pass moved it over. The DECISION is the typed op's returned 403;
// the SHAPE is bounce (scope.go). An API call must still get the 403 it always
// got, and a browser navigation must still get the sign-in redirect — for a typed
// op and for a raw handler alike.
func TestRefusalIsA403AndANavigationIsBounced(t *testing.T) {
	s := fakeService()
	// One typed op (settings), one raw handler (the applications SSE stream), so
	// the rule is proven on both sides of the client.
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

// TestTheBodylessPostsKeepTheirWire pins what typing this plane's four POSTs did
// and did not move, and it is the successor to a refusal, not a restatement of
// one.
//
// The refusal said "zip decodes the request body before the handler and 400s an
// unparseable one, while these routes read none". Re-read at the pin, that holds
// for a NON-EMPTY unparseable body only: op.invoke decodes exactly when
// len(rawIn) > 0 (zip typed.go:487-491), so the EMPTY body every real caller
// sends — the SPA sends none, and none of the four ever read one — never reaches
// a decoder and every answer below is what the raw handler gave.
//
// The gate order is the half worth pinning hardest. It moved from guard()
// middleware into the op (superAdminOf), because a typed op is also reached by
// POST /mcp and the by-name call plane, where no route middleware runs — so a
// non-SuperAdmin must still be refused, and is.
func TestTheBodylessPostsKeepTheirWire(t *testing.T) {
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
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if tc.admin {
			req.Header.Set("X-User-IsAdmin", "true")
		}
		if code, body := probe(t, s, req); code != tc.want {
			t.Errorf("POST %s (admin=%v, no body) = %d %s, want %d", tc.path, tc.admin, code, body, tc.want)
		}
	}
}

// TestAMalformedBodyIsTheOneAnswerThatMoved records the ONE delta the conversion
// took, so it is a measurement in the tree rather than a sentence in a commit
// message.
//
// A caller that sends BYTES THAT ARE NOT JSON to one of these four now gets 400
// where it got 200, 403 or 503 before — op.invoke decodes any non-empty body
// before the handler is entered (zip typed.go:487-491), and a syntax error is
// refused by encoding/json's own checkValid before any custom Unmarshaler could
// see it, so the record-it-and-judge-it-later input other packages use cannot
// recover this one either.
//
// Taken deliberately, and it is the same trade apps/help took: these routes read
// no body, so the sender is a broken client, and answering "your bytes are not
// JSON" discloses strictly less than the 403 it used to get — the 400 is about
// the caller's own request and says nothing about whether the application exists
// or who may reconcile it.
func TestAMalformedBodyIsTheOneAnswerThatMoved(t *testing.T) {
	s := fakeService(appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1", "Running", 1, 1))
	for _, path := range []string{
		"/v1/deploy/logout",
		"/v1/deploy/reconcile",
		"/v1/deploy/applications/iam/sync",
		"/v1/deploy/applications/iam/rollback",
	} {
		for _, admin := range []bool{true, false} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{not json"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			if admin {
				req.Header.Set("X-User-IsAdmin", "true")
			}
			if code, body := probe(t, s, req); code != http.StatusBadRequest {
				t.Errorf("POST %s (admin=%v, malformed body) = %d %s, want 400 — the decode runs before the handler",
					path, admin, code, body)
			}
		}
	}
}

// TestTheWritesAreShutToANonAdminOnTheToolPlane is the test a route test cannot
// be, and it is the reason typing the three admin writes was safe.
//
// A typed op is ALSO an MCP tool: zip dispatches tools/call straight into
// op.invoke, so nothing that hangs off the fiber route runs — not guard(), not
// any middleware. The gate therefore lives in the op (superAdminOf), and this
// drives the tool plane specifically, without any admin attestation, to prove it.
//
// It asserts `isError`, not the message: a tools/call refusal rides an HTTP 200
// carrying the JSON-RPC result, so reading the status or the text alone would
// pass on a refusal AND on a successful write.
//
// Both callers matter and they fail differently. ANONYMOUS proves the plane
// refuses a caller it cannot attest at all. A validated ORG MEMBER is the one
// that discriminates: they resolve a perfectly good TENANT scope, so a gate
// written as scopeOf admits them and only superAdminOf refuses — which is exactly
// the mutation a check on the anonymous caller alone cannot see.
func TestTheWritesAreShutToANonAdminOnTheToolPlane(t *testing.T) {
	tools := []struct{ tool, args string }{
		{"post_deploy_applications_by_name_sync", `{"name":"iam"}`},
		{"post_deploy_applications_by_name_rollback", `{"name":"iam"}`},
		{"post_deploy_reconcile", `{}`},
	}
	callers := map[string]map[string]string{
		"anonymous":  {},
		"org member": {"X-User-Id": "u-2", "X-Org-Id": "acme"},
	}
	for who, headers := range callers {
		for _, tc := range tools {
			app := zip.New(zip.Config{Logger: luxlog.New("test")})
			routes(app, fakeService(appCR("App", "hanzo", "iam", "u1", "ghcr.io/hanzoai/iam", "v1", "Running", 1, 1)))
			call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tc.tool +
				`","arguments":` + tc.args + `}}`
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(call))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("%s as %s: %v", tc.tool, who, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var envelope struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("%s as %s: %v (%s)", tc.tool, who, err, body)
			}
			if !envelope.Result.IsError {
				t.Errorf("tools/call %s as %s was NOT refused: %s\n"+
					"the SuperAdmin gate must be inside the op — no route middleware runs here",
					tc.tool, who, body)
				continue
			}
			if len(envelope.Result.Content) == 0 ||
				!strings.Contains(envelope.Result.Content[0].Text, "not authorized for this deploy console") {
				t.Errorf("tools/call %s as %s refused with %s, want the console's own refusal",
					tc.tool, who, body)
			}
		}
	}
}

// TestSignOutClearsTheSessionCookieVerbatim is the measurement that lets the
// sign-out declare its Set-Cookie as a string instead of writing it through
// fiber's cookie writer from inside the handler.
//
// The header below was measured off the raw handler this op replaced — the
// `clearCookie(c, sessionCookie)` that was `setCookie(…, "", -1)` — so this
// asserts the whole header verbatim rather than a substring. A cookie that
// differs in name, path, or any attribute is a DIFFERENT cookie and would not
// replace the live one: the session would survive a successful-looking sign-out.
func TestSignOutClearsTheSessionCookieVerbatim(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, fakeService())
	resp, err := app.Test(httptest.NewRequest(http.MethodPost, logoutPath, nil))
	if err != nil {
		t.Fatalf("POST %s: %v", logoutPath, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	const wantCookie = "__Host-hanzo_iam_token=; max-age=0; path=/; HttpOnly; secure; SameSite=Lax"
	if got := resp.Header.Get("Set-Cookie"); got != wantCookie {
		t.Errorf("Set-Cookie = %q\n           want %q", got, wantCookie)
	}
	const wantBody = `{"loggedIn":false,"loginUrl":"/v1/deploy/login"}`
	if resp.StatusCode != http.StatusOK || string(body) != wantBody {
		t.Errorf("POST %s = %d %s, want 200 %s", logoutPath, resp.StatusCode, body, wantBody)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}
}

// TestReconcileReportIsByteIdenticalToTheMapItReplaced is what proves naming the
// engine run's answer did not move it. encoding/json writes a map's keys SORTED
// and a struct's fields in DECLARATION order, so the models declare theirs
// alphabetically and this compares the marshalled BYTES rather than an equal
// value — the only comparison that can see the difference.
func TestReconcileReportIsByteIdenticalToTheMapItReplaced(t *testing.T) {
	report := reconcileReport{
		Declared: 3,
		Failed:   1,
		Instance: "universe",
		Prune:    true,
		Pruned:   2,
		Results: []appliedResource{{
			Message: "refused", Resource: "apps/v1/Deployment/hanzo/iam", Status: "SyncFailed",
		}},
		Revision: "abc123",
		Source:   reconcileSource{Path: "infra/k8s/operator/crs", Ref: "main", Repo: "https://github.com/hanzoai/universe"},
		Synced:   4,
	}
	// The map the handler assembled, verbatim.
	prior := map[string]any{
		"revision": "abc123",
		"source": map[string]any{
			"repo": "https://github.com/hanzoai/universe", "ref": "main", "path": "infra/k8s/operator/crs",
		},
		"instance": "universe",
		"prune":    true,
		"declared": 3,
		"synced":   4,
		"pruned":   2,
		"failed":   1,
		"results": []map[string]any{{
			"resource": "apps/v1/Deployment/hanzo/iam", "status": "SyncFailed", "message": "refused",
		}},
	}
	want, err := json.Marshal(prior)
	if err != nil {
		t.Fatalf("marshal the map: %v", err)
	}
	got, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal the report: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("reconcile answer moved:\n got %s\nwant %s", got, want)
	}
}

// TestAnEmptyRunReportsAnEmptyListRatherThanNull pins the one presence rule the
// byte comparison above cannot reach: Results is made rather than declared, so a
// run that reconciled nothing answers `[]` and never `null`. A client that
// iterates the list would have to special-case the second.
func TestAnEmptyRunReportsAnEmptyListRatherThanNull(t *testing.T) {
	got, err := json.Marshal(reconcileReport{Results: make([]appliedResource, 0)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(got), `"results":[]`) {
		t.Errorf("empty run = %s, want results:[]", got)
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
// "typed". Describe broke that equivalence on purpose — it is the client that gives
// an untyped route prose WITHOUT typing it — and an assertion resting on the old
// equivalence would now forbid exactly the thing the client exists to do, failing
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
		// Converted when zip's WithStatus became variadic: this probe answers 200 or
		// 503 over ONE shape, and the ANSWER states which (StatusCoder). Its old entry
		// in the ledger cited "WithStatus refuses a non-2xx by design", which was true
		// at the zip of the day and is false at the pinned one — the exact shape of
		// stale reason this file's two-ledger sum exists to surface.
		"GET /v1/deploy/health",
		"GET /v1/deploy/gitops",
		"GET /v1/deploy/projects",
		"GET /v1/deploy/session/userinfo",
		"GET /v1/deploy/settings",
		"GET /v1/deploy/version",
		// The four bodyless POSTs. Their entry read "zip decodes the body before the
		// handler and 400s an unparseable one; this route reads none" — half true at
		// the pin, and the half that expired is the one that mattered: op.invoke
		// decodes exactly when len(rawIn) > 0 (typed.go:487-491), so the empty body
		// every real caller sends never reaches a decoder. The half that survives is
		// a malformed body, and that delta is recorded and measured in
		// TestAMalformedBodyIsTheOneAnswerThatMoved rather than glossed.
		//
		// The gate moved with them, into the op (superAdminOf): a typed op is also
		// reached by POST /mcp and by the by-name call plane, neither of which runs
		// route middleware, so leaving guard() wrapped around the route would have
		// published an unguarded alias of three fleet-mutating admin writes.
		"POST /v1/deploy/applications/{name}/rollback",
		"POST /v1/deploy/applications/{name}/sync",
		"POST /v1/deploy/logout",
		"POST /v1/deploy/reconcile",
	}
	// untypedByDesign is the CLOSED list of addresses that stay raw handlers, each
	// with the wire fact that keeps it there, re-derived from the PINNED zip
	// (v1.36.3) rather than inherited from the comment that claimed it.
	//
	// It is spelled `untypedByDesign` because that is the name the fleet greps for.
	// It was `raw`, which is the same ledger under a name no sweep looks for — and
	// the cost was measured: a fleet-wide scan for gated apps reported this package,
	// which holds one of the strongest ledgers in the tree, as having none.
	untypedByDesign := map[string]string{
		"GET /v1/deploy/account/can-i/{wildcard1}": "a fiber WILDCARD path. zip's Template returns a pattern " +
			"holding no ':' unchanged (address.go:61-64), so the typed registry publishes '/v1/deploy/account/" +
			"can-i/*' while cloud's translate renders the live route '{wildcard1}' (openapi/openapi.go:807-823); " +
			"Fold then finds no live route at zip's key and refuses the WHOLE document (openapi/openapi.go:" +
			"699-705). Naming three params instead breaks the compat contract the console calls: argocd asks " +
			"can-i/{resource}/{action}/{subresource} and the subresource is routinely two segments.",
		"GET /v1/deploy/login": "its success is a 302 carrying a Location, a Set-Cookie and NO BODY. The " +
			"recorded reason — that WithStatus refuses a non-2xx — is FALSE at the pin (it is variadic and " +
			"takes 100..599, typed.go:154-168), and WithResponseHeader + HeaderCoder (typed.go:230-258) " +
			"declare both headers. What blocks it is the body: headers are written only for a NON-NIL Out " +
			"(a nil one takes the early return at typed.go:543-551), and the REST arm then ends in c.JSON(out) " +
			"(typed.go:567) — so declaring Location forces a JSON body and Content-Type over a 302 that carries " +
			"neither today (fiber v3 redirect.go:328-335 sets Location and status and writes nothing).",
		"GET /v1/deploy/callback": "login's body problem, plus one it does not have: callback writes TWO " +
			"Set-Cookie headers on one response — it clears the single-use flow cookie and sets the session " +
			"cookie (login.go) — while HeaderCoder is a map[string]string written with c.Set (typed.go:558), " +
			"which REPLACES. One of the two cookies would simply not be sent.",
		"GET /v1/deploy/stream/applications": "an unbounded text/event-stream written frame by frame with " +
			"c.SendStreamWriter (stream.go), which lives on *zip.Ctx (zip ctx.go:178-180) — a value a typed op " +
			"never receives, because its REST arm ends at one marshalled Out (typed.go:567). v1.36.3 registers " +
			"only Get/Post/Put/Patch/Delete[In,Out] (typed.go:85-108) and has no streaming registrar.",
		"GET /v1/deploy/stream/applications/{name}/resource-tree": "an unbounded text/event-stream, as above " +
			"(detail.go).",
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
	for at, why := range untypedByDesign {
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
	if got, want := len(served), len(typed)+len(untypedByDesign); got != want {
		var loose []string
		for at := range served {
			if reg.Ops[at] == nil && untypedByDesign[at] == "" {
				loose = append(loose, at)
			}
		}
		sort.Strings(loose)
		t.Errorf("the plane serves %d operations, but the two ledgers account for %d — neither typed nor "+
			"recorded: %s", got, want, strings.Join(loose, ", "))
	}
}

// proseless is the CLOSED list of published properties that carry NO description
// because the CLIENT they arrived through cannot carry one — not because nobody
// wrote it. Both classes below already carry the doc comment in the Go source;
// the pass that lifts prose files it under a name the published property does not
// have. Every line citation below is against the PINNED zip — v1.36.3, which is
// what go.mod says, re-read rather than inherited: a version written into prose is
// a claim about a dependency and it expires the moment the pin moves.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and this list must shrink then rather than outlive the gap.
var proseless = map[string]bool{
	// EMBEDDED STRUCT. argoNode embeds argoResourceRef, so zip's schema builder
	// PROMOTES those six fields into argoNode's own properties (wireFields,
	// openapi.go:916) and looks each description up under the OUTER type's name
	// (structSchema, openapi.go:724 keys fields[t.Name()+"."+name]) — argoNode.group.
	// zipdoc files a field's prose under the type that DECLARES it
	// (internal/zipdoc/extract.go:626,678 key out[reflectName(t)+"."+jsonName]),
	// which is argoResourceRef, so the two never meet. The argoResourceRef
	// component publishes all six, and
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
	// zipdoc to key its members under — its own comment says so
	// (internal/zipdoc/extract.go:630-635). Each of the three objects
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
