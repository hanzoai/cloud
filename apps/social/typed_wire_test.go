package social

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	// devmaster keys this test binary: the store opens nothing without a master
	// and a test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// typed_wire_test.go MEASURES what typing this surface could have moved and did
// not: the tenant stays server-resolved, the envelopes keep their exact bytes, the
// query filters keep their forgiving parse, and a body cannot outrank the URL. It
// also holds both halves of the surface as ledgers that must SUM to what the live
// router serves, so prose here cannot drift from the binary.

// mountSocial mounts ONLY this subsystem on a fresh app. routes() installs
// cloud.Bridge on its own group, so nothing here has to compose it — which is the
// point: a typed op that relied on Serve's binary-wide install would 403 in every
// test in this file and work only in production.
func mountSocial(t *testing.T) *zip.App {
	t.Helper()
	_ = Shutdown() // reset process-global state between tests
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Use(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// req issues a request. A non-empty org sets the gateway-minted validated
// principal (X-Org-Id + X-User-Id); an empty one is anonymous.
func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	return raw(t, app, method, path, org, r, body != nil)
}

// raw is req over bytes the caller composed, for the tests that must send a body
// the Go types could not produce.
func raw(t *testing.T, app *zip.App, method, path, org string, body io.Reader, json bool) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	if json {
		r.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		r.Header.Set("X-Org-Id", org)
		r.Header.Set("X-User-Id", "u1")
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// newPost creates a post and returns its id.
func newPost(t *testing.T, app *zip.App, org, content string) string {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/social/posts", org, map[string]any{"content": content})
	if code != http.StatusCreated {
		t.Fatalf("create post: want 201, got %d (%s)", code, body)
	}
	var p socialPost
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode post: %v (%s)", err, body)
	}
	return p.ID
}

// newAccount connects an account and returns its id.
func newAccount(t *testing.T, app *zip.App, org, handle string) string {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/social/accounts", org,
		map[string]any{"handle": handle, "provider": "x"})
	if code != http.StatusCreated {
		t.Fatalf("create account: want 201, got %d (%s)", code, body)
	}
	var a socialAccount
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("decode account: %v (%s)", err, body)
	}
	return a.ID
}

// ── the projection gate ─────────────────────────────────────────────────────

// untypedByDesign is the CLOSED list of operations here that are NOT typed ops,
// each with the wire fact that keeps it out.
//
// It is EMPTY, and that is the finding rather than an omission: nothing in this
// surface is wire-bound. Every operation answers a value its handler assembled
// from the store, refuses with a returned zip error the way a typed op does, and
// carries no stream, no raw body, no second success shape and no domain-bodied
// non-2xx. HIP-1153 said as much ("nothing about these shapes prevents typing —
// they are values") and this is the measurement of it.
//
// The map stays here rather than being deleted because the GATE below quantifies
// over it: a route added untyped tomorrow must either become an op or land here
// with a reason, and an entry naming a route social no longer serves goes red.
var untypedByDesign = map[string]string{}

// socialOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. Reading the REAL mount, not a reconstruction of it, is what makes this a
// gate rather than a restatement.
func socialOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountSocial(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "social", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when an operation here is neither a typed op
// nor one named above — so the next route added is typed by default, and dropping
// one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := socialOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with "+
			"the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which social no longer serves", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The count is pinned so the prose in typed.go and HIP-1153 cannot drift from
	// the binary: thirteen operations, all typed, none refused.
	if len(typed) != 13 || len(untypedByDesign) != 0 {
		t.Errorf("social is %d typed / %d refused; the surface is thirteen operations, all typed",
			len(typed), len(untypedByDesign))
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface: it becomes the OpenAPI description AND
// the MCP tool description a model reads to pick the tool. zipdoc_gen.go carries it
// into the binary, so an op added without regenerating shows up here as a nameless
// tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := socialOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed social ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/social/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed covers the RESPONSE side the op-level gate
// cannot see. A typed op publishes its In's and Out's whole schema, and a property
// that reaches openapi.yaml with no description reaches every generated SDK and
// every MCP inputSchema without one too. socialAccount and socialPost are store ROW
// types, which is exactly the class that ships bare: a reader could see scheduleAt
// was an integer and nowhere that it was unix SECONDS.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountSocial(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "social", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Through JSON, because that is the artifact: only the marshalled form is what
	// an SDK generator actually reads.
	rawDoc, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rawDoc, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	if len(published.Components.Schemas) == 0 {
		t.Fatal("no published schemas at all — a typed op must publish its In/Out")
	}
	var bare []string
	for name, schema := range published.Components.Schemas {
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/social/...",
			len(bare), strings.Join(bare, ", "))
	}
}

// TestNoSecretIsPublished pins that neither row type leaks its two `json:"-"`
// fields into the document. Org is the TENANT KEY and Token is a provider access
// credential; structSchema skips a `json:"-"` field (openapi.go:720), and this is
// the assertion that keeps a later tag edit from publishing either.
func TestNoSecretIsPublished(t *testing.T) {
	app := mountSocial(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "social", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	rawDoc, _ := json.Marshal(doc)
	if bytes.Contains(rawDoc, []byte(`"token"`)) {
		t.Error("the document publishes a `token` property — an account's provider access token must never reach any projection")
	}
	if bytes.Contains(rawDoc, []byte(`"org"`)) {
		t.Error("the document publishes an `org` property — the tenant is resolved server-side and is never a field")
	}
}

// ── the wire ────────────────────────────────────────────────────────────────

// TestSummaryBytesDidNotMove is why socialSummary's fields are declared
// alphabetically. The answer was a map[string]any, which encoding/json writes in
// SORTED key order; a struct writes DECLARATION order. Asserting the marshalled
// BYTES is what proves naming the shape did not move it — equality as JSON would
// pass either way and the difference is what a byte-comparing client sees.
func TestSummaryBytesDidNotMove(t *testing.T) {
	app := mountSocial(t)
	const org = "acme"
	newPost(t, app, org, "one")
	newAccount(t, app, org, "@acme")

	code, body := req(t, app, http.MethodGet, "/v1/social/summary", org, nil)
	if code != http.StatusOK {
		t.Fatalf("summary: want 200, got %d (%s)", code, body)
	}
	// The bytes the map literal produced, for the same counts.
	want, err := json.Marshal(map[string]any{
		"posts": 1, "scheduled": 0, "published": 0, "accounts": 1,
	})
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(body), want) {
		t.Errorf("summary bytes moved:\n typed: %s\n map:   %s", bytes.TrimSpace(body), want)
	}
}

// TestListEnvelopesKeepTheirShape pins the three `{"data":[…]}` envelopes. They
// were map literals too, and a struct that named the key anything else — or
// answered a bare array — would break every client that reads `.data`.
func TestListEnvelopesKeepTheirShape(t *testing.T) {
	app := mountSocial(t)
	const org = "acme"
	newPost(t, app, org, "one")
	newAccount(t, app, org, "@acme")

	for _, path := range []string{"/v1/social/accounts", "/v1/social/posts", "/v1/social/providers"} {
		code, body := req(t, app, http.MethodGet, path, org, nil)
		if code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d (%s)", path, code, body)
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("%s: decode: %v (%s)", path, err, body)
		}
		if len(env) != 1 {
			t.Errorf("%s: envelope has %d keys, want exactly {data}: %s", path, len(env), body)
		}
		if _, ok := env["data"]; !ok {
			t.Errorf("%s: envelope has no `data` key: %s", path, body)
		}
		if !bytes.HasPrefix(bytes.TrimSpace(env["data"]), []byte("[")) {
			t.Errorf("%s: `data` is not an array: %s", path, env["data"])
		}
	}
}

// TestMediaIsAlwaysAnArray pins that a post with no media answers `[]` and never
// null. decodeMedia guarantees a non-nil slice, and `media` has no omitempty on the
// row, so a client may rely on the key being present and iterable.
func TestMediaIsAlwaysAnArray(t *testing.T) {
	app := mountSocial(t)
	id := newPost(t, app, "acme", "no media")
	code, body := req(t, app, http.MethodGet, "/v1/social/posts/"+id, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get post: want 200, got %d (%s)", code, body)
	}
	if !bytes.Contains(body, []byte(`"media":[]`)) {
		t.Errorf("a post with no media must carry `\"media\":[]`, got: %s", body)
	}
}

// TestCreatesAnswer201AndDeletesAnswer204 pins the two statuses typing had to
// DECLARE rather than set per request. A create says zip.WithStatus(201); a delete
// returns a nil Out, which zip stamps with the status the op declared or the 204 a
// nil Out has always meant (typed.go:543-551).
func TestCreatesAnswer201AndDeletesAnswer204(t *testing.T) {
	app := mountSocial(t)
	const org = "acme"
	post := newPost(t, app, org, "bye") // asserts 201 inside
	acct := newAccount(t, app, org, "@bye")

	for _, path := range []string{"/v1/social/posts/" + post, "/v1/social/accounts/" + acct} {
		code, body := req(t, app, http.MethodDelete, path, org, nil)
		if code != http.StatusNoContent {
			t.Errorf("DELETE %s: want 204, got %d (%s)", path, code, body)
		}
		if len(bytes.TrimSpace(body)) != 0 {
			t.Errorf("DELETE %s: 204 must carry no body, got %q", path, body)
		}
		if code, body := req(t, app, http.MethodDelete, path, org, nil); code != http.StatusNotFound {
			t.Errorf("DELETE %s twice: want 404, got %d (%s)", path, code, body)
		}
	}
}

// TestPagingStaysAString is the measurement behind keeping ?limit a string.
//
// limitOf trims before parsing and falls back to the default on anything it cannot
// read. zip's setScalar (typed.go:407) uses strconv.ParseInt with NO trim and leaves
// an unreadable value at the field's ZERO, so an int field would refuse a
// percent-encoded space — fiber percent-decodes a QUERY value — and could not tell
// `?limit=0` from `?limit=abc`. Both spellings below must serve a page today.
func TestPagingStaysAString(t *testing.T) {
	app := mountSocial(t)
	const org = "acme"
	for i := range 3 {
		newPost(t, app, org, string(rune('a'+i)))
	}
	for _, q := range []string{"?limit=2", "?limit=%202", "?limit=abc", "?limit=0", ""} {
		code, body := req(t, app, http.MethodGet, "/v1/social/posts"+q, org, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/social/posts%s: want 200, got %d (%s)", q, code, body)
		}
		var env socialPosts
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("%s: decode: %v", q, err)
		}
		want := 3
		if q == "?limit=2" || q == "?limit=%202" {
			want = 2
		}
		if len(env.Data) != want {
			t.Errorf("GET /v1/social/posts%s returned %d posts, want %d — the forgiving parse moved",
				q, len(env.Data), want)
		}
	}
}

// TestQueryCannotOutrankTheBody is why every body-only field carries `url:"-"`.
//
// zip binds the body first, then the query, then the path (typed.go:488, :507,
// :508), and a body method publishes NO query parameters — so without the opt-out
// each body field would gain an undeclared `?field=` twin that outranks it. The
// route has never read one, and it must stay that way.
func TestQueryCannotOutrankTheBody(t *testing.T) {
	app := mountSocial(t)
	const org = "acme"

	code, body := req(t, app, http.MethodPost,
		"/v1/social/posts?content=HIJACK&channel=linkedin&status=published", org,
		map[string]any{"content": "mine", "channel": "x", "status": "draft"})
	if code != http.StatusCreated {
		t.Fatalf("create post: want 201, got %d (%s)", code, body)
	}
	var p socialPost
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if p.Content != "mine" || p.Channel != "x" || p.Status != statusDraft {
		t.Errorf("a query outranked the body: content=%q channel=%q status=%q — every body field needs url:\"-\"",
			p.Content, p.Channel, p.Status)
	}
}

// TestTheURLOutranksTheBodyForTheID is the other half of the same rule, and the one
// that is a tenancy fact rather than a nuisance: the path segment names the row, and
// a body claiming a different id must be invisible. socialAccountWrite.ID is
// `json:"-" url:"id"`, so the decoder never reads one (openapi.go:720 keeps it out
// of the published body; bindURL skips a "-" name at typed.go:372) and the segment
// is what binds.
func TestTheURLOutranksTheBodyForTheID(t *testing.T) {
	app := mountSocial(t)
	const org = "acme"
	mine := newAccount(t, app, org, "@mine")
	other := newAccount(t, app, org, "@other")

	code, body := req(t, app, http.MethodPut, "/v1/social/accounts/"+mine, org,
		map[string]any{"id": other, "handle": "@renamed", "provider": "x", "status": "connected"})
	if code != http.StatusOK {
		t.Fatalf("put account: want 200, got %d (%s)", code, body)
	}
	var got socialAccount
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if got.ID != mine {
		t.Fatalf("a body id redirected the write: wrote %q, URL named %q", got.ID, mine)
	}
	code, body = req(t, app, http.MethodGet, "/v1/social/accounts/"+other, org, nil)
	if code != http.StatusOK {
		t.Fatalf("get other: %d (%s)", code, body)
	}
	if !bytes.Contains(body, []byte(`"@other"`)) {
		t.Errorf("the untouched account changed: %s", body)
	}
}

// TestTenantIsNeverTheCallersToAssert pins that neither a body nor a query can name
// the org. Org is `json:"-"` on the row and is not a field on any In at all, so the
// only source is principal.Acting — read off the validated principal the Bridge
// parked.
func TestTenantIsNeverTheCallersToAssert(t *testing.T) {
	app := mountSocial(t)
	victim := newPost(t, app, "victim", "secret")

	// A body and a query both naming the victim org are ignored: the row lands in
	// the caller's own tenant.
	code, body := req(t, app, http.MethodPost, "/v1/social/posts?org=victim", "attacker",
		map[string]any{"content": "mine", "org": "victim"})
	if code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", code, body)
	}
	code, body = req(t, app, http.MethodGet, "/v1/social/posts", "victim", nil)
	if code != http.StatusOK {
		t.Fatalf("list victim: %d (%s)", code, body)
	}
	var env socialPosts
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 1 || env.Data[0].Content != "secret" {
		t.Errorf("the attacker's row landed in the victim's tenant: %s", body)
	}

	// The victim's row is unaddressable by its id, which the attacker knows.
	if code, body := req(t, app, http.MethodGet, "/v1/social/posts/"+victim, "attacker", nil); code != http.StatusNotFound {
		t.Errorf("cross-tenant read by id: want 404, got %d (%s)", code, body)
	}
	if code, body := req(t, app, http.MethodDelete, "/v1/social/posts/"+victim, "attacker", nil); code != http.StatusNotFound {
		t.Errorf("cross-tenant delete by id: want 404, got %d (%s)", code, body)
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/social/posts/"+victim, "victim", nil); code != http.StatusOK {
		t.Errorf("the victim's own row must survive the attempt, got %d", code)
	}
}

// TestEveryOpFailsClosedWithoutAPrincipal walks the whole surface anonymously. Every
// operation must give the SAME 403 the untyped handlers gave — principal.Acting is
// principal.Org's context-side twin and both end in the same refused() sentence — and
// that includes the writes, whose bodies decode fine.
func TestEveryOpFailsClosedWithoutAPrincipal(t *testing.T) {
	app := mountSocial(t)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/social/summary", nil},
		{http.MethodGet, "/v1/social/providers", nil},
		{http.MethodGet, "/v1/social/accounts", nil},
		{http.MethodPost, "/v1/social/accounts", map[string]any{"handle": "@x"}},
		{http.MethodGet, "/v1/social/accounts/acct_1", nil},
		{http.MethodPut, "/v1/social/accounts/acct_1", map[string]any{"handle": "@x"}},
		{http.MethodDelete, "/v1/social/accounts/acct_1", nil},
		{http.MethodGet, "/v1/social/posts", nil},
		{http.MethodPost, "/v1/social/posts", map[string]any{"content": "hi"}},
		{http.MethodGet, "/v1/social/posts/post_1", nil},
		{http.MethodPut, "/v1/social/posts/post_1", map[string]any{"content": "hi"}},
		{http.MethodDelete, "/v1/social/posts/post_1", nil},
		{http.MethodPost, "/v1/social/posts/post_1/publish", nil},
	} {
		code, body := req(t, app, c.method, c.path, "", c.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s with no principal: want 403, got %d (%s)", c.method, c.path, code, body)
		}
		if !bytes.Contains(body, []byte("a validated principal is required")) {
			t.Errorf("%s %s: the refusal sentence moved: %s", c.method, c.path, body)
		}
	}
}

// TestPublishFailsClosedWithTheMissingCredentials pins the publish edge's honesty:
// no deployment carries the provider OAuth-app credentials, so a publish answers 503
// NAMING what is absent and never marks the post published. Typing moved nothing
// here — the untyped handler returned a zip error too, which zip renders as the same
// RFC 9457 problem members (zip problem.go).
func TestPublishFailsClosedWithTheMissingCredentials(t *testing.T) {
	t.Setenv("X_API_KEY", "")
	t.Setenv("X_API_SECRET", "")
	app := mountSocial(t)
	const org = "acme"
	newAccount(t, app, org, "@acme")
	id := newPost(t, app, org, "go out")

	code, body := req(t, app, http.MethodPost, "/v1/social/posts/"+id+"/publish", org, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("publish with no credentials: want 503, got %d (%s)", code, body)
	}
	if !bytes.Contains(body, []byte("X_API_KEY")) {
		t.Errorf("the 503 must name the missing credentials, got: %s", body)
	}
	var env struct {
		Status int    `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Status != http.StatusServiceUnavailable {
		t.Errorf("the refusal envelope moved: %s (err=%v)", body, err)
	}
	code, body = req(t, app, http.MethodGet, "/v1/social/posts/"+id, org, nil)
	if code != http.StatusOK {
		t.Fatalf("read back: %d (%s)", code, body)
	}
	var p socialPost
	_ = json.Unmarshal(body, &p)
	if p.Status == statusPublished {
		t.Error("a publish that could not run marked the post published — the edge must fail closed")
	}
}

// TestVocabulariesStillRefuse pins that the fixed provider/status vocabularies
// survived the move onto accountFrom/postFrom: an unknown value is a 400, not a
// coerced default, on BOTH the create and the replace.
func TestVocabulariesStillRefuse(t *testing.T) {
	app := mountSocial(t)
	const org = "acme"
	acct := newAccount(t, app, org, "@acme")
	post := newPost(t, app, org, "hi")

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/social/accounts", map[string]any{"provider": "myspace"}},
		{http.MethodPut, "/v1/social/accounts/" + acct, map[string]any{"provider": "myspace"}},
		{http.MethodPost, "/v1/social/accounts", map[string]any{"status": "pending"}},
		{http.MethodPost, "/v1/social/posts", map[string]any{"content": "x", "channel": "myspace"}},
		{http.MethodPut, "/v1/social/posts/" + post, map[string]any{"content": "x", "status": "publishing"}},
		{http.MethodPost, "/v1/social/posts", map[string]any{"content": "   "}},
	} {
		if code, body := req(t, app, c.method, c.path, org, c.body); code != http.StatusBadRequest {
			t.Errorf("%s %s %v: want 400, got %d (%s)", c.method, c.path, c.body, code, body)
		}
	}
}
