package ads

import (
	"bytes"
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

// This file is the MEASUREMENT that typing /v1/ads did not move its wire, and
// the CLOSED ledger of the one route that stayed untyped. Before this pass all
// seven of the subsystem's operations published no summary and no description at
// all, which is exactly the set that projects to NOTHING: no prose, no MCP tool,
// no CLI command, no typed SDK method.

// untypedByDesign is the closed list of ads operations that are NOT typed ops,
// each with the WIRE FACT that typing it would move. It has exactly one entry.
var untypedByDesign = map[string]string{
	"POST /v1/ads/campaigns/{id}/launch": "launch is deliberately BODY-TOLERANT. It reads its " +
		"optional {account} body with the Bind error DISCARDED (`_ = c.Bind(&body)`, launchCampaign in " +
		"ads.go), so a malformed or non-JSON body launches the campaign on the STORED account and " +
		"answers 200. zip v1.18.11's op.invoke unconditionally jsonenc.Unmarshals any non-empty body " +
		"and returns ErrBadRequest when that fails (zip/typed.go:239-243), so a typed op turns today's " +
		"200 into a 400 — a wire break for every caller that posts a body this route has always ignored. " +
		"TestLaunchStillIgnoresAMalformedBody is that measurement, so the conversion is a test away once " +
		"zip can declare a body-tolerant op.",
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer does, once at the root. In a test
// the test IS the composer, so it owes the same install — skipping it does not
// test a stricter program, it tests one where every org-scoped op answers 403
// for a reason production could never produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mountApp mounts the ads surface on a fresh in-memory app with a temp store,
// composed exactly as the unified binary is: cloud.Bridge at the root, the
// subsystem's routes beneath it.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// do issues one request. org=="" is an ANONYMOUS caller — there is no request
// field for the tenant, by design, so the header set is the only way to assert one.
func do(t *testing.T, app *zip.App, method, path, org string, body []byte) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// adOps reads BOTH projections of the LIVE router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router rather than the source is what makes this a
// gate and not prose.
func adOps(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "ads", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/ads/") }
	served, typed = map[string]bool{}, map[string]*openapi.Operation{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when an ads operation is neither a typed op
// nor one named in untypedByDesign — so the next route added here is typed by
// default, and dropping one out of the registry takes a deliberate edit with a
// reason. The two ledgers must also SUM to the served surface, so a stale reason
// cannot hide behind a route that no longer exists.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := adOps(t)
	if len(served) != 7 {
		t.Fatalf("ads serves %d operations, not the 7 these ledgers know: %s", len(served), sortedKeys(served))
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no prose, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it (zip.Get/Post/... in routes()), or add it to untypedByDesign with the "+
			"wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which ads does not serve — a stale reason nobody can re-check", key)
		}
		if _, isTyped := typed[key]; isTyped {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}

// TestEveryTypedOpIsDescribed proves the prose actually reached the registry.
// zipdoc lifts a handler's doc comment into zipdoc_gen.go at BUILD time, so a
// package that loses its //go:generate directive keeps compiling perfectly while
// every one of its operations goes back to publishing nothing at all — which is
// the state this whole subsystem was in.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := adOps(t)
	if len(typed) != 6 {
		t.Fatalf("the registry carries %d operations, want 6", len(typed))
	}
	for key, op := range typed {
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s publishes NO description — the OpenAPI prose and the MCP tool description are both empty", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s publishes NO summary — the CLI command help and every SDK docstring are empty", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s has a line break in its one-line summary: %q", key, op.Summary)
		}
	}
}

// TestLaunchStillIgnoresAMalformedBody is the untypedByDesign entry's
// measurement. The route must NOT 400 on a body it cannot parse: it falls back
// to the campaign's stored account and proceeds. Here the campaign has no ad
// account connected, so the launch fails at the CONNECTOR (424), which is proof
// enough that the body never stopped it — a typed op would have answered 400
// before the handler ran at all.
func TestLaunchStillIgnoresAMalformedBody(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/ads/campaigns", "acme",
		[]byte(`{"name":"Spring Launch","platform":"meta"}`))
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var created AdCampaign
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	code, body = do(t, app, http.MethodPost, "/v1/ads/campaigns/"+created.ID+"/launch", "acme",
		[]byte(`{"account": `)) // deliberately truncated JSON
	if code == http.StatusBadRequest {
		t.Fatalf("launch rejected a malformed body with 400 — the body-tolerance untypedByDesign records is gone (%s)", body)
	}
	if code != http.StatusFailedDependency {
		t.Fatalf("launch want 424 (no connected ad account), got %d (%s)", code, body)
	}
}

// TestTheQueryStringCannotRedirectAWrite is the pin on `url:"-"`. zip's binder
// fills an In field from the QUERY as well as the body, so a converted write
// silently starts accepting `?status=` and `?name=` — values these routes have
// never taken there, because the untyped handlers read c.Bind, which is the body
// and nothing else. This is a wire-WIDENING no status-code test sees.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/ads/campaigns?name=HIJACK&status=active&budget=999", "acme",
		[]byte(`{"name":"Spring Launch","platform":"meta","status":"draft","budget":100}`))
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var got AdCampaign
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "Spring Launch" || got.Status != "draft" || got.Budget != 100 {
		t.Fatalf("the query string reached a body-only field: %+v", got)
	}
}

// TestThePathIsTheAddressingAuthority pins what the untyped handlers did with a
// body id: nothing. They read c.Param("id"); the typed ops bind the id from the
// path with `json:"-"`, so a body cannot even name a second target — and a
// second org's id is a 404, never a cross-tenant write.
func TestThePathIsTheAddressingAuthority(t *testing.T) {
	app := mountApp(t)
	_, body := do(t, app, http.MethodPost, "/v1/ads/campaigns", "acme", []byte(`{"name":"Mine","platform":"meta"}`))
	var mine AdCampaign
	if err := json.Unmarshal(body, &mine); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, body = do(t, app, http.MethodPost, "/v1/ads/campaigns", "beta", []byte(`{"name":"Theirs","platform":"meta"}`))
	var theirs AdCampaign
	if err := json.Unmarshal(body, &theirs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// acme PUTs its own id in the path and beta's in the body: the path wins.
	code, body := do(t, app, http.MethodPut, "/v1/ads/campaigns/"+mine.ID, "acme",
		[]byte(`{"id":"`+theirs.ID+`","name":"Renamed","platform":"meta","status":"paused"}`))
	if code != http.StatusOK {
		t.Fatalf("update want 200, got %d (%s)", code, body)
	}
	var saved AdCampaign
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if saved.ID != mine.ID {
		t.Fatalf("the body redirected the write to %s", saved.ID)
	}
	// And beta's campaign is untouched.
	code, body = do(t, app, http.MethodGet, "/v1/ads/campaigns/"+theirs.ID, "beta", nil)
	if code != http.StatusOK {
		t.Fatalf("read want 200, got %d (%s)", code, body)
	}
	var still AdCampaign
	if err := json.Unmarshal(body, &still); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if still.Name != "Theirs" {
		t.Fatalf("a cross-tenant write landed: %+v", still)
	}
}

// TestFailsClosedWithoutAValidatedPrincipal is the tenancy claim: no request
// field can name the tenant, so an anonymous caller reads and writes nothing.
func TestFailsClosedWithoutAValidatedPrincipal(t *testing.T) {
	app := mountApp(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/ads/summary"},
		{http.MethodGet, "/v1/ads/campaigns"},
		{http.MethodPost, "/v1/ads/campaigns"},
		{http.MethodGet, "/v1/ads/campaigns/camp_x"},
		{http.MethodPut, "/v1/ads/campaigns/camp_x"},
		{http.MethodDelete, "/v1/ads/campaigns/camp_x"},
	} {
		var body []byte
		if r.method == http.MethodPost || r.method == http.MethodPut {
			body = []byte(`{"name":"x","platform":"meta"}`)
		}
		if code, got := do(t, app, r.method, r.path, "", body); code != http.StatusForbidden {
			t.Errorf("%s %s anonymous got %d, want 403 (%s)", r.method, r.path, code, got)
		}
	}
}

// TestListAndSummaryEnvelopes pin the two Outs that replaced a map[string]any. A
// struct drops any key its fields do not name, so this asserts the answers are
// still exactly {"data": [...]} and the four-number roll-up.
func TestListAndSummaryEnvelopes(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/ads/campaigns", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if got := strings.TrimSpace(string(body)); got != `{"data":[]}` {
		t.Fatalf("empty list answers %s, want {\"data\":[]}", got)
	}
	code, body = do(t, app, http.MethodGet, "/v1/ads/summary", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("summary want 200, got %d (%s)", code, body)
	}
	if got := strings.TrimSpace(string(body)); got != `{"active":0,"budget":0,"campaigns":0,"spend":0}` {
		t.Fatalf("summary answers %s, want the four-number roll-up in its original key order", got)
	}
}

// TestDeleteAnswers204WithNoBody pins the one status a nil Out carries.
func TestDeleteAnswers204WithNoBody(t *testing.T) {
	app := mountApp(t)
	_, body := do(t, app, http.MethodPost, "/v1/ads/campaigns", "acme", []byte(`{"name":"Gone","platform":"x"}`))
	var c AdCampaign
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	code, got := do(t, app, http.MethodDelete, "/v1/ads/campaigns/"+c.ID, "acme", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d (%s)", code, got)
	}
	if len(bytes.TrimSpace(got)) != 0 {
		t.Fatalf("delete answered a body: %s", got)
	}
	if code, got := do(t, app, http.MethodDelete, "/v1/ads/campaigns/"+c.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("second delete want 404, got %d (%s)", code, got)
	}
}

func sortedKeys(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
