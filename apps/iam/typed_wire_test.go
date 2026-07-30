package iam

// This file makes iam's typed partition a GATE instead of a paragraph.
//
// iam is a WHOLLY OPAQUE product: every operation it publishes is one of five
// `app.All` wildcards relaying a whole NESTED zip app (iamserver.Handler — 94 typed
// ops of its own, adapted to net/http) verbatim. None of the five can become a typed
// op, and that is a structural fact about the mount, not a backlog item. Written as
// prose it would be a claim nobody can check; written here it is a test, so a route
// added tomorrow as a raw handler — or one of these wildcards silently narrowing —
// says so.
//
// It also pins the fail-closed half, because the two halves must cover the SAME
// addresses: a degraded surface smaller than the mounted one is a hole, and it was one.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of addresses iam serves WITHOUT a typed op, each
// with the wire fact that keeps it raw. A typed op is a route PLUS a registry entry —
// the one value the OpenAPI operation's detail, the MCP tool, the CLI command and every
// generated SDK method come from — so an operation missing from that registry is
// invisible to all four. All five of iam's are missing on purpose.
//
// It is keyed by the PATH, not by "METHOD /path" like its siblings in apps/ml and
// apps/o11y, because here the refusal is a property of the REGISTRATION and one
// `app.All` covers every method at once (iam.go safeMount). Keying by method would
// state one fact six times and let five of the copies rot independently; the test
// expands each entry over the methods the document actually publishes and checks the
// sum, so nothing is lost by saying it once.
var untypedByDesign = map[string]string{
	"/v1/iam":             relayWire,
	"/v1/iam/{wildcard1}": relayWire,
	// The browser authorize surface — the 302 target of /v1/iam/oauth/authorize.
	// Same relay, and additionally a REDIRECT: the response that matters is a 302
	// with a Location header and no body at all.
	"/login/oauth":             relayWire + " " + redirectWire,
	"/login/oauth/{wildcard1}": relayWire + " " + redirectWire,
	// OIDC discovery + JWKS at the issuer root (RFC 8414 / OIDC Discovery 1.0).
	"/.well-known/{wildcard1}": relayWire + " " + discoveryWire,
}

// relayWire is the reason all five refuse, and it is the same reason five times because
// it is ONE registration repeated: `app.All(pattern, zip.AdaptNetHTTP(iamserver.Handler(db)))`.
const relayWire = "a verbatim relay of a whole NESTED app: iamserver.Handler(db) is " +
	"github.com/hanzoai/iam's entire standalone zip app (94 typed ops — OIDC discovery/JWKS, the " +
	"oauth/* protocol endpoints, credential login, the v2 entity CRUD, SCIM, and the Casdoor " +
	"verb-alias compat layer) adapted to net/http and hung on ONE wildcard. Three facts each " +
	"forbid a typed op on their own: (1) one registration serves an OPEN set of sub-paths, so a " +
	"typed op per leaf would have to enumerate another module's route table and any path missed " +
	"would 404 that IAM serves today; (2) the bytes, status and Content-Type are the nested app's " +
	"own — including its Guard's {\"status\":401,\"error\":\"authentication required\"} envelope, " +
	"which is not cloud's error shape — and a typed op answers one declared status with one " +
	"marshalled Out; (3) the oauth token/introspect/revoke endpoints take " +
	"application/x-www-form-urlencoded bodies by RFC 6749/7662/7009, and zip's op.invoke decodes " +
	"every non-empty typed body with jsonenc.Unmarshal, so declaring any In turns a working token " +
	"exchange into a 400. Describing this surface means composing the nested app's OWN document, " +
	"not restating it in cloud — see LLM.md."

const redirectWire = "It also answers 302 with a Location header and an empty body, which no " +
	"(*Out, error) pair can express."

const discoveryWire = "Its document is minted by the nested app from the deployment's signing " +
	"certs and registered applications, so cloud asserting its shape would be a second source of " +
	"truth for another module's value."

// mountApp mounts iam the way plugin/iam does — the whole Mount, so the ledgers below
// read the surface a deployed binary serves and not a test-only subset.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("iamtest"), DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("iamtest"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// iamOps reads BOTH projections of the live router at their one shared address form:
// what the document says is served, and which of those carry a typed registry entry.
func iamOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "iam", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when an iam operation is neither a typed op nor
// covered by a named refusal above — so the next route added here is typed by default,
// and dropping one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := iamOps(t)
	if len(served) == 0 {
		t.Fatal("iam serves no operations at all — the mount did not register")
	}

	var unnamed []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		_, path, _ := strings.Cut(key, " ")
		if _, named := untypedByDesign[path]; named {
			continue
		}
		unnamed = append(unnamed, key)
	}
	if len(unnamed) > 0 {
		sort.Strings(unnamed)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the /v1/iam group), or add its path to "+
			"untypedByDesign with the reason typing it would move the wire.", strings.Join(unnamed, ", "))
	}

	// The reasons must describe addresses that exist, or the list is stale prose. Every
	// named path has to be served by at least one method.
	live := map[string]bool{}
	for key := range served {
		_, path, _ := strings.Cut(key, " ")
		live[path] = true
	}
	for path := range untypedByDesign {
		if !live[path] {
			t.Errorf("untypedByDesign names %q, which iam no longer serves", path)
		}
	}
	// A typed op named as a refusal is a contradiction — one of the two is wrong.
	for key := range typed {
		_, path, _ := strings.Cut(key, " ")
		if _, named := untypedByDesign[path]; named {
			t.Errorf("untypedByDesign names %q, but %s IS a typed op — delete the entry", path, key)
		}
	}
	// The two ledgers must SUM to the whole surface. Every served operation is either
	// typed or sits at a named path, so counting the named paths' operations and the
	// typed ones must reach exactly what the document publishes: 0 typed + 35 named
	// (5 relays x the 7 methods the document projects) = 35.
	//
	// SEVEN, not the five a REST reader expects: `app.All` registers nine methods and
	// openapi.From publishes seven of them — get/post/put/patch/delete, plus OPTIONS
	// and TRACE. The count is derived here rather than written down for exactly that
	// reason: a hand-counted 25 is what a reader assumes and it is wrong by ten.
	named := 0
	for key := range served {
		_, path, _ := strings.Cut(key, " ")
		if _, ok := untypedByDesign[path]; ok {
			named++
		}
	}
	if got := len(typed) + named; got != len(served) {
		t.Errorf("%d typed + %d named = %d, but iam serves %d operations",
			len(typed), named, got, len(served))
	}
}

// TestTheRelayIsWhyNothingIsTyped pins the wire facts the refusal above rests on, so
// the reason is MEASURED rather than asserted. Each of these bodies is composed by the
// nested app and passed through byte for byte; a typed op produces one marshalled Out
// at one declared status and can carry none of them.
func TestTheRelayIsWhyNothingIsTyped(t *testing.T) {
	app := mountApp(t)

	// The nested app's OWN Guard envelope reaches the caller unchanged. cloud's error
	// shape is nested under "error"; this one is flat with a numeric "status", which is
	// the tell that the response was composed inside github.com/hanzoai/iam.
	status, body := get(t, app, "/v1/iam/users")
	if status != http.StatusUnauthorized {
		t.Errorf("GET /v1/iam/users = %d, want 401 from the nested app's Guard", status)
	}
	var guard struct {
		Status int    `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &guard); err != nil || guard.Status != 401 {
		t.Errorf("GET /v1/iam/users body = %q, want the nested Guard's {\"status\":401,…} envelope (err=%v)", body, err)
	}

	// OIDC discovery: a document minted by the nested app from the deployment's own
	// registered applications and signing certs, at the ISSUER root by spec.
	status, body = get(t, app, "/.well-known/openid-configuration")
	if status != http.StatusOK {
		t.Fatalf("GET /.well-known/openid-configuration = %d, want 200", status)
	}
	if !strings.Contains(body, `"authorization_endpoint"`) {
		t.Errorf("discovery body = %q, want the RFC 8414 metadata the nested app mints", body)
	}
}

// TestFailClosedCoversEveryMountedAddress is the gate on the two halves agreeing. When
// IAM cannot boot, every address safeMount would have served must answer the honest
// JSON 503 — because the terminal handler in every plugin binary is webui.Mount's `/*`
// console catch-all, so an address the degraded half misses does not 404, it answers
// 200 with the SPA's HTML. /.well-known/openid-configuration was exactly that hole: the
// first call every relying party makes, parsing a web page as its discovery document.
func TestFailClosedCoversEveryMountedAddress(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("iamtest"), DisableStartupMessage: true})
	mountFailClosed(app)

	// Concrete addresses, one per pattern — the bare prefixes included, since fiber's
	// greedy `/*` is what covers them and that is worth pinning rather than assuming.
	for _, p := range []string{
		"/v1/iam",
		"/v1/iam/oauth/token",
		"/v1/iam/.well-known/jwks",
		"/login/oauth",
		"/login/oauth/authorize",
		"/.well-known/openid-configuration",
		"/.well-known/oauth-authorization-server",
	} {
		if status, _ := get(t, app, p); status != http.StatusServiceUnavailable {
			t.Errorf("degraded %s = %d, want 503 (fail-closed, not the console SPA)", p, status)
		}
	}

	// And the set is derived, not listed twice: every pattern the real mount uses is a
	// pattern the degraded mount registered.
	registered := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		registered[r.Path] = true
	}
	for _, p := range patterns() {
		if !registered[p] {
			t.Errorf("mountFailClosed does not cover %q, which safeMount serves", p)
		}
	}
}

// get drives one request through the live router and returns the status and body.
func get(t *testing.T, app *zip.App, path string) (int, string) {
	t.Helper()
	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", path, err)
	}
	return resp.StatusCode, string(body)
}
