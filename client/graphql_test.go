package surface_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/graph"
	"github.com/hanzoai/cloud/surface"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// ── a child that reports what it was asked ───────────────────────────────────

// heard is one request as the child received it. The endpoint's whole job is to turn
// a field into exactly this, so this is what the tests assert against.
type heard struct {
	method string
	path   string
	query  string
	body   string
	org    string
	user   string
}

type stub struct {
	addr  string
	mu    sync.Mutex
	calls []heard
	reply string
}

// echo stands a child on its own socket, speaking the transport the surface dials.
// It is a bare zip app rather than a composed one because what these tests prove
// is what the ENDPOINT sent — a composed child answers its own identity gate first and
// would report nothing about the request.
func echo(t *testing.T, reply string) *stub {
	t.Helper()
	s := &stub{addr: filepath.Join(planetest.Dir(t), "echo.sock"), reply: reply}
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.All("/*", func(c *zip.Ctx) error {
		fc := c.Fiber()
		s.mu.Lock()
		s.calls = append(s.calls, heard{
			method: fc.Method(),
			path:   fc.Path(),
			query:  string(fc.Request().URI().QueryString()),
			body:   string(fc.Body()),
			org:    c.Header("X-Org-Id"),
			user:   c.Header("X-User-Id"),
		})
		s.mu.Unlock()
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(200, []byte(s.reply))
	})
	go func() { _ = app.Listen(s.addr) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, s.addr)
	return s
}

func (s *stub) once(t *testing.T) heard {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) != 1 {
		t.Fatalf("the child was asked %d times, want exactly once: %+v", len(s.calls), s.calls)
	}
	return s.calls[0]
}

// ── the endpoint under test ──────────────────────────────────────────────────

// fromTree is the app's own committed document — the same bytes the host embeds
// at build time, read here from the working tree so these tests fail when the
// document and the plugin disagree.
func fromTree(app string) []byte {
	raw, err := os.ReadFile(filepath.Join("..", "plugin", app, "openapi.json"))
	if err != nil {
		return nil
	}
	return raw
}

// endpointTo builds the endpoint over one app's real document, pointed at an address.
func endpointTo(t *testing.T, app, addr string) *surface.Graph {
	t.Helper()
	subsets, err := openapi.Subsets([]string{app}, fromTree, func(string) string { return "" })
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}
	d, err := openapi.Fleet(subsets)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	return surface.NewGraph(d, func(string) (string, string, error) { return addr, "/", nil })
}

// caller is the request whose headers ride to the child — the identity the host
// established, forwarded so the child scopes the caller exactly as it would over
// REST. This endpoint establishes none of its own.
func caller() *fasthttp.Request {
	r := fasthttp.AcquireRequest()
	r.Header.Set("X-Org-Id", "acme")
	r.Header.Set("X-User-Id", "acme/z@acme.test")
	return r
}

func run(t *testing.T, g *surface.Graph, q string, vars map[string]any) surface.Response {
	t.Helper()
	from := caller()
	defer fasthttp.ReleaseRequest(from)
	return g.Run(surface.Request{Query: q, Variables: vars}, from)
}

// ── what the endpoint sends ──────────────────────────────────────────────────

// TestAFieldBecomesTheOperationsOwnRequest is the core claim: a name in a query
// arrives at the owning app as the REST call that name stands for.
func TestAFieldBecomesTheOperationsOwnRequest(t *testing.T) {
	c := echo(t, `{"assertions":[]}`)
	g := endpointTo(t, "graph", c.addr)

	if g.Fields() == 0 {
		t.Fatal("the endpoint published no fields at all")
	}
	if res := run(t, g, `{ graphRead(entity: "acme/svc/api", limit: 5) { assertions { value } } }`, nil); len(res.Errors) > 0 {
		t.Fatalf("read: %v", res.Errors)
	}

	got := c.once(t)
	if got.method != "GET" || got.path != "/v1/graph" {
		t.Errorf("the child was asked %s %s, want GET /v1/graph", got.method, got.path)
	}
	for _, want := range []string{"entity=acme%2Fsvc%2Fapi", "limit=5"} {
		if !strings.Contains(got.query, want) {
			t.Errorf("query = %q, missing %s", got.query, want)
		}
	}
}

// TestTheCallersIdentityRidesToTheChild is the security property. This endpoint
// establishes no principal and decides no authorization: it forwards the one the
// host established, so a field reaches precisely what its REST route reaches for
// whoever asked.
func TestTheCallersIdentityRidesToTheChild(t *testing.T) {
	c := echo(t, `{"assertions":[]}`)
	g := endpointTo(t, "graph", c.addr)

	run(t, g, `{ graphRead { assertions { value } } }`, nil)

	got := c.once(t)
	if got.org != "acme" || got.user != "acme/z@acme.test" {
		t.Errorf("the child saw org=%q user=%q; the caller's identity did not ride along", got.org, got.user)
	}
}

// TestABodyIsSentAsTheRequestBody proves a mutation's one argument becomes the
// JSON the operation already accepts.
func TestABodyIsSentAsTheRequestBody(t *testing.T) {
	c := echo(t, `{"recorded":1}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `mutation {
		graphAssert(body: {assertions: [{entity: "e", relation: "r", value: "v", names: true}]})
	}`, nil)
	if len(res.Errors) > 0 {
		t.Fatalf("assert: %v", res.Errors)
	}

	got := c.once(t)
	if got.method != "POST" || got.path != "/v1/graph" {
		t.Errorf("the child was asked %s %s, want POST /v1/graph", got.method, got.path)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(got.body), &sent); err != nil {
		t.Fatalf("the body was not JSON: %v: %s", err, got.body)
	}
	rows, _ := sent["assertions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("the body did not carry the assertion: %s", got.body)
	}
	if row, _ := rows[0].(map[string]any); row["entity"] != "e" || row["names"] != true {
		t.Errorf("the body lost its values: %s", got.body)
	}
}

// TestVariablesAreResolvedBeforeSending pins that a client sending values apart
// from its query gets those values on the wire.
func TestVariablesAreResolvedBeforeSending(t *testing.T) {
	c := echo(t, `{"assertions":[]}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `query Look($e: String!, $n: Int) { graphRead(entity: $e, limit: $n) { assertions { id } } }`,
		map[string]any{"e": "acme/svc/api", "n": 3})
	if len(res.Errors) > 0 {
		t.Fatalf("variables: %v", res.Errors)
	}
	if q := c.once(t).query; !strings.Contains(q, "limit=3") || !strings.Contains(q, "entity=acme%2Fsvc%2Fapi") {
		t.Errorf("query = %q, want the variables' values", q)
	}
}

// TestAVariableWithNoValueIsRefused guards the other half: a placeholder nobody
// filled must not be sent as the literal text of its own name.
func TestAVariableWithNoValueIsRefused(t *testing.T) {
	c := echo(t, `{}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `query Look($e: String!) { graphRead(entity: $e) { assertions { id } } }`, nil)
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want the one unfilled variable", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "e") {
		t.Errorf("the reason must name the variable, got %q", res.Errors[0].Message)
	}
}

// ── what the endpoint answers ────────────────────────────────────────────────

// TestASelectionNarrowsTheAnswer proves the selection set is honoured rather than
// decorative: a caller that asked for one key gets one key.
func TestASelectionNarrowsTheAnswer(t *testing.T) {
	c := echo(t, `{"relations":["owner"],"rule":["later"],"bound":100}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `{ graphVocabulary { relations } }`, nil)
	if len(res.Errors) > 0 {
		t.Fatalf("vocabulary: %v", res.Errors)
	}
	m, ok := res.Data["graphVocabulary"].(map[string]any)
	if !ok {
		t.Fatalf("vocabulary answered %T", res.Data["graphVocabulary"])
	}
	if _, asked := m["relations"]; !asked {
		t.Errorf("the selected key is missing: %v", m)
	}
	for _, unasked := range []string{"rule", "bound"} {
		if _, got := m[unasked]; got {
			t.Errorf("%q was not selected but came back: %v", unasked, m)
		}
	}
}

// TestASelectionReachesIntoAList pins that selecting over many rows narrows each
// of them, which is what a caller means by it.
func TestASelectionReachesIntoAList(t *testing.T) {
	c := echo(t, `{"assertions":[{"value":"a","source":"s1"},{"value":"b","source":"s2"}]}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `{ graphRead { assertions { value } } }`, nil)
	top, _ := res.Data["graphRead"].(map[string]any)
	rows, _ := top["assertions"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want both", rows)
	}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if _, ok := m["value"]; !ok {
			t.Errorf("the selected key is missing from a row: %v", m)
		}
		if _, ok := m["source"]; ok {
			t.Errorf("an unselected key came back on a row: %v", m)
		}
	}
}

// TestAliasesAnswerUnderTheNameTheCallerChose is what makes asking one operation
// twice in one request possible.
func TestAliasesAnswerUnderTheNameTheCallerChose(t *testing.T) {
	c := echo(t, `{"relations":["owner"]}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `{ mine: graphVocabulary { relations } }`, nil)
	if _, ok := res.Data["mine"]; !ok {
		t.Errorf("the alias is not the key it answered under: %v", keys(res.Data))
	}
	if _, ok := res.Data["graphVocabulary"]; ok {
		t.Errorf("an aliased field must not also answer under its own name: %v", keys(res.Data))
	}
}

// TestTwoRootFieldsAreOneRequest is what a caller comes to this endpoint for: what
// REST spends two round trips on, answered in one.
func TestTwoRootFieldsAreOneRequest(t *testing.T) {
	c := echo(t, `{"relations":["owner"]}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `{
		vocab: graphVocabulary { relations }
		rows:  graphRead(limit: 1) { assertions { id } }
	}`, nil)
	if len(res.Errors) > 0 {
		t.Fatalf("two fields: %v", res.Errors)
	}
	for _, want := range []string{"vocab", "rows"} {
		if _, ok := res.Data[want]; !ok {
			t.Errorf("%q is missing from a two-field answer: %v", want, keys(res.Data))
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 2 {
		t.Errorf("two fields made %d hops, want 2", len(c.calls))
	}
}

// TestFragmentsAreExpanded covers the last thing real clients send.
func TestFragmentsAreExpanded(t *testing.T) {
	c := echo(t, `{"relations":["owner"],"bound":100}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, "{ graphVocabulary { ...V } }\nfragment V on Vocabulary { relations }", nil)
	if len(res.Errors) > 0 {
		t.Fatalf("fragment query: %v", res.Errors)
	}
	m, _ := res.Data["graphVocabulary"].(map[string]any)
	if _, ok := m["relations"]; !ok {
		t.Errorf("the fragment's selection did not reach the answer: %v", m)
	}
	if _, ok := m["bound"]; ok {
		t.Errorf("the fragment selected one key; %v came back", m)
	}
}

// TestAFragmentThatSpreadsItselfIsRefused guards the one cycle a parser cannot
// see on its own.
func TestAFragmentThatSpreadsItselfIsRefused(t *testing.T) {
	c := echo(t, `{}`)
	g := endpointTo(t, "graph", c.addr)

	if res := run(t, g, "{ graphVocabulary { ...V } }\nfragment V on Vocabulary { ...V }", nil); len(res.Errors) == 0 {
		t.Fatal("a self-spreading fragment must be refused, not run")
	}
}

// ── how the endpoint fails ───────────────────────────────────────────────────

// TestAnUnknownFieldIsAnErrorAndANull pins the wire a GraphQL client reads: the
// answer still has a shape, and the reason names the field it belongs to.
func TestAnUnknownFieldIsAnErrorAndANull(t *testing.T) {
	c := echo(t, `{"relations":[]}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `{ graphVocabulary { relations } noSuchField }`, nil)
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly the one unknown field", res.Errors)
	}
	if len(res.Errors[0].Path) != 1 || res.Errors[0].Path[0] != "noSuchField" {
		t.Errorf("the failure must name its field, got %v", res.Errors[0].Path)
	}
	if v, ok := res.Data["noSuchField"]; !ok || v != nil {
		t.Errorf("a failed field is null and present, got %v (present=%v)", v, ok)
	}
	if _, ok := res.Data["graphVocabulary"]; !ok {
		t.Error("one bad field must not take the good ones with it")
	}
}

// TestAQueryThatCannotBeParsedNeverBegins pins the other half of that wire: no
// data at all, because execution did not start.
func TestAQueryThatCannotBeParsedNeverBegins(t *testing.T) {
	c := echo(t, `{}`)
	g := endpointTo(t, "graph", c.addr)

	res := run(t, g, `{ graphVocabulary { relations `, nil)
	if len(res.Errors) == 0 {
		t.Fatal("an unclosed query must be refused")
	}
	if res.Data != nil {
		t.Errorf("nothing ran, so there is no data to be right about: %v", res.Data)
	}
}

// TestTheEndpointReachesARealPluginOverItsSocket is the end-to-end hop, against a
// child composed the way its plugin main composes it.
//
// The observable is the CHILD'S OWN VERDICT. A plugin sanitizes identity at its
// own boundary — a header a client sent is not a principal — so this call is
// refused there, and that proves the endpoint carried the request to the app owning
// the field and reported what that app said rather than answering for it. What it
// does not prove is a successful authenticated call: that needs a credential the
// child validates, which is IAM's boundary and not this endpoint's.
func TestTheEndpointReachesARealPluginOverItsSocket(t *testing.T) {
	kid := darkChild(t, "graph", graph.Use)
	g := endpointTo(t, "graph", kid.addr)

	res := run(t, g, `{ graphVocabulary { relations } }`, nil)
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want the child's own refusal", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "403") {
		t.Errorf("the reason must be what the app answered, got %q", res.Errors[0].Message)
	}
	if !strings.Contains(res.Errors[0].Message, "/v1/graph/vocabulary") {
		t.Errorf("the reason must name the address it asked, got %q", res.Errors[0].Message)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── what the endpoint refuses to spend ───────────────────────────────────────

// TestAQueryWiderThanTheCeilingIsRefused pins the fan-out bound. Each root field
// is a hop that may START a lazy child, so the width of one query is how many apps
// a single caller can make this host dial at once.
func TestAQueryWiderThanTheCeilingIsRefused(t *testing.T) {
	c := echo(t, `{"relations":[]}`)
	g := endpointTo(t, "graph", c.addr)

	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 200; i++ {
		b.WriteString(" f")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(": graphVocabulary { relations }")
	}
	b.WriteString(" }")

	res := run(t, g, b.String(), nil)
	if len(res.Errors) == 0 {
		t.Fatal("200 root fields must be refused; each one is a hop into the surface")
	}
	if res.Data != nil {
		t.Error("a refused request runs nothing, so there is no data to be right about")
	}
	// REFUSED, not truncated: nothing was dispatched at all.
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 0 {
		t.Errorf("the endpoint dispatched %d hops for a request it refused", len(c.calls))
	}
}

// TestAQueryAtTheCeilingStillRuns is the other side of that bound: the limit is a
// ceiling on abuse, not a budget ordinary callers can trip over.
func TestAQueryAtTheCeilingStillRuns(t *testing.T) {
	c := echo(t, `{"relations":[]}`)
	g := endpointTo(t, "graph", c.addr)

	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 8; i++ {
		b.WriteString(" f")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(": graphVocabulary { relations }")
	}
	b.WriteString(" }")

	if res := run(t, g, b.String(), nil); len(res.Errors) > 0 {
		t.Fatalf("an ordinary multi-field query was refused: %v", res.Errors)
	}
}

// TestSelectionsDeeperThanTheCeilingAreRefused guards the recursion. Depth buys no
// hop, but it is still a caller-supplied recursion and expansion copies what a
// fragment spreads.
func TestSelectionsDeeperThanTheCeilingAreRefused(t *testing.T) {
	c := echo(t, `{}`)
	g := endpointTo(t, "graph", c.addr)

	q := "{ graphVocabulary " + strings.Repeat("{ a ", 60) + strings.Repeat("} ", 60) + "}"
	if res := run(t, g, q, nil); len(res.Errors) == 0 {
		t.Fatal("a query nesting past the ceiling must be refused")
	}
}

// TestAnIntrospectionQueryIsToldWhereTheSchemaIs covers the first request a
// GraphQL client makes. Introspection is not served here — the schema is a
// projection of a document rather than a type graph this process can walk, and a
// partial __schema would have every generator build against a shape that is not
// the API. Reporting it as an unknown field is true and useless; the refusal
// names the address that does answer.
func TestAnIntrospectionQueryIsToldWhereTheSchemaIs(t *testing.T) {
	c := echo(t, `{}`)
	g := endpointTo(t, "graph", c.addr)

	for _, q := range []string{
		`{ __schema { queryType { name } } }`,
		`{ __type(name: "Assertion") { name } }`,
	} {
		res := run(t, g, q, nil)
		if len(res.Errors) != 1 {
			t.Fatalf("%s: errors = %v, want one", q, res.Errors)
		}
		msg := res.Errors[0].Message
		if !strings.Contains(msg, "introspection") || !strings.Contains(msg, "SDL") {
			t.Errorf("%s: reason = %q, want it to say where the schema is", q, msg)
		}
		if strings.Contains(msg, "no field named") {
			t.Errorf("%s: reported as a typo rather than as an unserved feature", q)
		}
	}
	// And nothing was dispatched for it.
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 0 {
		t.Errorf("an introspection query reached the surface %d times", len(c.calls))
	}
}
