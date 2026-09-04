package openapi

// The hypermedia index, tested as the two properties it lives or dies on:
// it shows a caller exactly the surface a caller may call, and it never answers
// at an address a capability answers at.
//
// Every case is built by COMPOSING parts, because both properties are functions of
// facts only the compose puts on an operation — the tag that names its capability,
// the stage that decides who is shown it, and the audience mark [stamp] writes.
// Constructing a Document by hand would test a shape the fleet never produces.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest/mcp"
	"github.com/zap-proto/zip"
)

// op is one operation as a part carries it before the compose sees it.
func op(id, summary string) *Operation {
	return &Operation{OperationID: id, Summary: summary}
}

// parts is a four-capability fleet with one of every case that decides the
// index: a plain capability, one that answers its OWN root, one that is beta,
// and the operator's product.
//
// PARTS, not a composed document, because the stage arrives with the part — an app
// cannot know its own (see [Part]) — so a test that composed first and served the
// result would hand every capability the same stage and prove nothing about the
// one term of the rule that keeps a beta capability hidden.
func parts() []Part {
	return []Part{
		{App: "kms", Doc: &Document{
			Info: Info{Description: "Package kms is sealed secret custody."},
			Paths: map[string]PathItem{
				"/v1/kms/secrets":        {"get": op("get_kms_secrets", "List the secrets you can open")},
				"/v1/kms/secrets/{name}": {"put": op("put_kms_secret", "Seal a secret under a name")},
			}}},
		{App: "agent", Doc: &Document{
			Info: Info{Description: "Package agents is your agents and their runs."},
			Paths: map[string]PathItem{
				"/v1/agent":      {"get": op("get_agents", "List your agents")},
				"/v1/agent/{id}": {"get": op("get_agent", "Read one agent")},
			}}},
		{App: "search", Doc: &Document{
			Info: Info{Description: "Package search is one query over everything you can see."},
			Paths: map[string]PathItem{
				"/v1/search": {"post": op("post_search", "Search everything you can see")},
			}}},
		{App: "labs", Stage: "beta", Doc: &Document{
			Info: Info{Description: "Package labs is not generally available."},
			Paths: map[string]PathItem{
				"/v1/labs/things": {"get": op("get_labs_things", "List the things")},
			}}},
		{App: "admin", Doc: &Document{
			Info: Info{Description: "Package admin is the operator's view."},
			Paths: map[string]PathItem{
				"/v1/admin/orgs": {"get": op("get_admin_orgs", "List every org")},
			}}},
	}
}

// fleet is [parts] as the host composes them.
func fleet(t *testing.T) *Document {
	t.Helper()
	d, err := Compose(parts())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The root is the CUSTOMER surface, by the same rule openapi.yaml is: a beta
// capability and the operator's product are in neither, and being missing from
// the root is the same fact as 404ing one segment down — which is what keeps this
// from telling an unflagged caller that something exists.
func TestTheRootListsTheCustomerSurfaceAndNothingBeside(t *testing.T) {
	root, per := Discover(fleet(t))

	var got []string
	for _, c := range root.Capabilities {
		got = append(got, c.Name)
		if c.Href != RootPath+"/"+c.Name {
			t.Errorf("%s links to %q — a row's href is the address it answers under", c.Name, c.Href)
		}
		if c.Stage != "ga" {
			t.Errorf("%s is listed at stage %q; only a generally available capability is listed at all", c.Name, c.Stage)
		}
	}
	if want := "agent kms search"; strings.Join(got, " ") != want {
		t.Errorf("the root lists %q, want %q — sorted, and neither the beta capability nor the operator's product",
			strings.Join(got, " "), want)
	}
	for _, gone := range []string{"labs", "admin"} {
		if _, leaked := per[gone]; leaked {
			t.Errorf("%q has an index — a caller could ask whether it exists and be told", gone)
		}
	}
	if d := root.Capabilities[1].Description; d != "Package kms is sealed secret custody." {
		t.Errorf("kms says %q about itself; the row carries the tag's own synopsis", d)
	}
	for _, rel := range []string{"self", "describedby", "mcp"} {
		if root.Links[rel].Href == "" {
			t.Errorf("the root offers no %q link", rel)
		}
	}
	if root.Links["auth"].Href != mcp.Metadata {
		t.Errorf("the root links auth to %q, want the protected-resource metadata at %q",
			root.Links["auth"].Href, mcp.Metadata)
	}
	if root.Links["describedby"].Href != Path || root.Links["mcp"].Href != mcp.Path {
		t.Errorf("the root's links name %v, not the document and the agent MCP address", root.Links)
	}
}

// A capability's index is exactly its own operations — the address to send, the
// method, the name to call it by, and the sentence lifted from its handler.
func TestACapabilityIndexIsExactlyItsOwnOperations(t *testing.T) {
	_, per := Discover(fleet(t))

	kms := per["kms"]
	if kms == nil {
		t.Fatal("kms has no index, and it answers nothing at its own root")
	}
	var got []string
	for _, o := range kms.Operations {
		got = append(got, o.Method+" "+o.Href)
	}
	if want := "GET /v1/kms/secrets, PUT /v1/kms/secrets/{name}"; strings.Join(got, ", ") != want {
		t.Errorf("kms lists %q, want %q", strings.Join(got, ", "), want)
	}
	if o := kms.Operations[0]; o.OperationID != "get_kms_secrets" || o.Summary != "List the secrets you can open" {
		t.Errorf("the first operation is %+v — an index row carries the name to call it by and what it does", o)
	}
	if kms.Links["up"].Href != RootPath || kms.Links["self"].Href != RootPath+"/kms" {
		t.Errorf("kms's index links are %v — a client must be able to get back to the root", kms.Links)
	}
}

// THE PROPERTY THE INDEX MUST NOT LOSE: an address a capability answers at is
// the capability's, and the index does not have an entry for it. It is read off
// the document rather than configured, so the day a capability starts serving its
// own root the index steps aside with no edit here.
func TestTheIndexYieldsWhereTheCapabilityAnswersItsOwnRoot(t *testing.T) {
	root, per := Discover(fleet(t))

	if _, mine := per["agent"]; mine {
		t.Error("agent has an index at /v1/agent, and GET /v1/agent is its own operation — " +
			"the index would answer in front of the capability's collection")
	}
	// Per (method, path): search ACTS at its own root and reads nothing there, so
	// the GET is free and its href resolves to an index rather than a 405.
	if per["search"] == nil {
		t.Error("search has no index, and POST /v1/search is the only operation at that address")
	}
	var listed bool
	for _, c := range root.Capabilities {
		listed = listed || c.Name == "agent"
	}
	if !listed {
		t.Error("agent is not in the root either — the href is real and a client must be able to follow it")
	}
}

// served drives one request through an app and hands back everything a caller
// can see, headers included.
func served(t *testing.T, app *zip.App, method, path string) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

// indexed builds an app with the index composed ahead of one capability that
// answers its own root, exactly as the host composes it ahead of the mounts
// — and over the same [Subsets] shape, so the compose it does is the fleet's.
func indexed(t *testing.T) *zip.App {
	t.Helper()
	app := newApp()
	UseIndex(app, func() ([]Part, error) { return parts(), nil })
	app.Get("/v1/agent", func(c *zip.Ctx) error {
		c.SetHeader("Link", `</v1/agent?page=2>; rel="next"`)
		return c.String(http.StatusOK, "the agents collection")
	})
	return app
}

func TestTheEndpointsAnswerAndTheCapabilityStillAnswersItsOwn(t *testing.T) {
	app := indexed(t)

	resp, body := served(t, app, http.MethodGet, RootPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", RootPath, resp.StatusCode)
	}
	var root Root
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatalf("GET %s is not the root index (%v): %.120q", RootPath, err, body)
	}
	if len(root.Capabilities) == 0 {
		t.Fatal("the root answered with no capabilities at all — this proved nothing")
	}

	if _, body := served(t, app, http.MethodGet, "/v1/kms"); !strings.Contains(body, `"name":"kms"`) {
		t.Errorf("GET /v1/kms answered %.120q, not kms's index", body)
	}
	if _, body := served(t, app, http.MethodGet, "/v1/agent"); body != "the agents collection" {
		t.Errorf("GET /v1/agent answered %.120q — the index took an address the capability serves", body)
	}
	// An index is a GET. Anything else at the same address belongs to whoever is
	// behind it, which here is nobody.
	if resp, _ := served(t, app, http.MethodPost, RootPath); resp.StatusCode == http.StatusOK {
		t.Errorf("POST %s was answered by the index", RootPath)
	}
}

// An unpublished name is answered exactly as any other address nothing claims,
// so the index cannot be asked whether something exists that it would not list.
func TestAnUnpublishedNameIsAnsweredLikeAnyUnclaimedAddress(t *testing.T) {
	app := indexed(t)

	beta, betaBody := served(t, app, http.MethodGet, "/v1/labs")
	made, madeBody := served(t, app, http.MethodGet, "/v1/nothing-is-here")
	if beta.StatusCode != made.StatusCode || betaBody != madeBody {
		t.Errorf("a beta capability answers %d %.60q and an invented name answers %d %.60q — "+
			"the difference tells a caller which names exist",
			beta.StatusCode, betaBody, made.StatusCode, madeBody)
	}
}

// Every /v1 answer says where it came from, where the API is described and where
// the index starts — and says it BESIDE whatever the capability already said,
// never over it.
func TestEveryAnswerCarriesItsLinksAndClobbersNone(t *testing.T) {
	app := indexed(t)

	resp, _ := served(t, app, http.MethodGet, "/v1/agent")
	got := resp.Header.Values("Link")
	want := []string{
		`</v1/agent?page=2>; rel="next"`,
		`</v1/agent>; rel="self"`,
		`<` + Path + `>; rel="describedby"`,
		`<` + RootPath + `>; rel="index"`,
	}
	for _, w := range want {
		var found bool
		for _, g := range got {
			found = found || strings.Contains(g, w)
		}
		if !found {
			t.Errorf("the answer carries %v, missing %q", got, w)
		}
	}

	// Outside the contract's namespace there is nothing to DESCRIBE. self is a
	// different claim and an unconditional one — /healthz is an address, and
	// saying so costs nothing and points at nothing that could 404. What must not
	// appear here are the relations that name the contract.
	app.Get("/healthz", func(c *zip.Ctx) error { return c.String(http.StatusOK, "ok") })
	resp, _ = served(t, app, http.MethodGet, "/healthz")
	for _, l := range resp.Header.Values("Link") {
		for _, rel := range []string{"describedby", "index", "up", "collection"} {
			if strings.Contains(l, `rel="`+rel+`"`) {
				t.Errorf("GET /healthz carries %q — the contract's links are the /v1 namespace's", l)
			}
		}
	}
}

// The endpoints are described where the fleet document describes them, so an SDK
// generated off it can call the index it needs in order to discover anything else.
func TestBothEndpointsAreInTheDocument(t *testing.T) {
	app := newApp()
	Use(app, Info{Title: "t", Version: "v1"})
	stubIndex(app)
	doc, err := Spec(app, Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{RootPath, IndexPath} {
		op := doc.Paths[path]["get"]
		if op == nil {
			t.Fatalf("the document does not carry %s", path)
		}
		if op.Summary == "" {
			t.Errorf("%s publishes an operationId and nothing else", path)
		}
		if op.Security == nil || len(*op.Security) != 0 {
			t.Errorf("%s requires a credential — a client has to read the index before it holds one", path)
		}
		if !Host(path) {
			t.Errorf("%s is served by the host and MCP does not say so", path)
		}
		if Routed(path) {
			t.Errorf("%s reads as a host ROUTE; it is answered ahead of the router", path)
		}
	}
	if doc.Paths[RootPath]["get"].OperationID != "get_capabilities" {
		t.Errorf("the root's id is %q — /v1 is nothing but the segment zip.ID drops, so it is named",
			doc.Paths[RootPath]["get"].OperationID)
	}
}

// A /v1 answer says what its address accepts and what sits above it. Both are
// read off the composed contract, so neither can name an address the fleet does
// not serve — a link to a 404 is worse than no link.
func TestAnAnswerSaysWhatItAcceptsAndWhatIsAboveIt(t *testing.T) {
	app := indexed(t)

	for _, tc := range []struct{ path, allow, rel, up string }{
		// A member's parent is the collection it belongs to (RFC 6573).
		{path: "/v1/agent/a1", allow: "GET", rel: "collection", up: "/v1/agent"},
		{path: "/v1/kms/secrets/db", allow: "PUT", rel: "collection", up: "/v1/kms/secrets"},
		// Asked with the wrong method, the answer still says which one works.
		{path: "/v1/search", allow: "POST"},
		// /v1/kms is not an address, so nothing points at it — and one segment
		// under the root there is nothing above worth naming either, because /v1
		// is already on every answer as the index.
		{path: "/v1/kms/secrets", allow: "GET"},
		// labs is beta. A capability the index will not name does not get its
		// methods advertised here either: one rule, asked in both places.
		{path: "/v1/labs/things", allow: ""},
	} {
		resp, _ := served(t, app, http.MethodGet, tc.path)
		if got := resp.Header.Get("Allow"); got != tc.allow {
			t.Errorf("%s: Allow = %q, want %q", tc.path, got, tc.allow)
		}
		links := strings.Join(resp.Header.Values("Link"), " ")
		if tc.rel == "" {
			if strings.Contains(links, `rel="collection"`) || strings.Contains(links, `rel="up"`) {
				t.Errorf("%s: carries %v — there is no address above it", tc.path, resp.Header.Values("Link"))
			}
			continue
		}
		if want := "<" + tc.up + `>; rel="` + tc.rel + `"`; !strings.Contains(links, want) {
			t.Errorf("%s: carries %v, missing %q", tc.path, resp.Header.Values("Link"), want)
		}
	}
}

// An operation's template is found by SHAPE, because the host proxies and
// the route a request matched there is the proxy's own. Where two templates fit
// the same path, the one made of literals is the address the caller asked for.
func TestALiteralAddressBeatsTheTemplateItFits(t *testing.T) {
	pub := func(id string) *Operation {
		return &Operation{OperationID: id, Summary: id, Public: true}
	}
	a := addressesOf(&Document{Paths: map[string]PathItem{
		"/v1/node":      {"get": pub("get_nodes")},
		"/v1/node/{id}": {"get": pub("get_node")},
		"/v1/node/peer": {"post": pub("post_nodes_peer")},
	}})

	if got, ok := a.at("/v1/node/peer"); !ok || got.allow != "POST" || got.member {
		t.Errorf("/v1/node/peer resolved to %+v — it is an address, not a node id", got)
	}
	if got, ok := a.at("/v1/node/n1"); !ok || got.allow != "GET" || !got.member {
		t.Errorf("/v1/node/n1 resolved to %+v — it is a member of the collection", got)
	}
	if got, ok := a.at("/v1/node"); !ok || got.hasUp {
		t.Errorf("/v1/node resolved to %+v — /v1 is the index, not its collection", got)
	}
	if _, ok := a.at("/v1/node/n1/deeper"); ok {
		t.Error("a path the contract does not serve resolved to an address")
	}
}

// An address answers what it accepts when ASKED the way HTTP has a method for
// asking — RFC 9110 §9.3.7 — and not only as a header on a GET that may not be
// allowed there in the first place.
//
// The three facts that make this an endpoint rather than a decoration: it answers
// 204 with no body, because Allow IS the answer; it answers for a MEMBER template
// as well as a literal, because that is where a client most needs to ask; and it
// YIELDS on an address the contract does not carry, so a capability that grows
// its own OPTIONS keeps it and an unclaimed path still 404s.
func TestOptionsAnswersWhatAnAddressAccepts(t *testing.T) {
	app := indexed(t)

	for _, tc := range []struct {
		path  string
		allow string
		code  int
	}{
		{path: "/v1/agent", allow: "GET", code: http.StatusNoContent},
		{path: "/v1/agent/a1", allow: "GET", code: http.StatusNoContent},
		{path: "/v1/kms/secrets/db", allow: "PUT", code: http.StatusNoContent},
		// Beta: a capability the index will not name does not advertise its
		// methods here either. One rule, asked in both places — so this address
		// is NOT the index's to answer and falls through.
		{path: "/v1/labs/things", allow: "", code: http.StatusNotFound},
	} {
		resp, body := served(t, app, http.MethodOptions, tc.path)
		if resp.StatusCode != tc.code {
			t.Errorf("OPTIONS %s: status = %d, want %d", tc.path, resp.StatusCode, tc.code)
		}
		if got := resp.Header.Get("Allow"); got != tc.allow {
			t.Errorf("OPTIONS %s: Allow = %q, want %q", tc.path, got, tc.allow)
		}
		if tc.code == http.StatusNoContent && body != "" {
			t.Errorf("OPTIONS %s: answered a body (%q); Allow is the answer", tc.path, body)
		}
	}
}

// A CORS PREFLIGHT IS A DIFFERENT QUESTION AND IS NOT THIS ENDPOINT'S. It is
// defined by carrying Access-Control-Request-Method (Fetch, CORS preflight
// request), and cloud's edge middleware owns and short-circuits exactly those.
// Answering one here would be a second CORS authority — the defect
// middleware_edge.go's own comment exists to prevent — so the split is on the
// header that defines it.
func TestAPreflightIsNotThisEndpoint(t *testing.T) {
	app := indexed(t)
	req := httptest.NewRequest(http.MethodOptions, "/v1/agent", nil)
	req.Header.Set("Origin", "https://example.test")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent && resp.Header.Get("Allow") != "" {
		t.Fatalf("a preflight was answered by the contract endpoint (Allow=%q); the edge owns it",
			resp.Header.Get("Allow"))
	}
}
